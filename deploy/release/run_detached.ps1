# run_with_deadline.ps1 を段のプロセスから切り離して起動し、ログと終了コードのファイルを上限つきで見る（release.yml の Windows のジョブで使う）。
#
#   pwsh -NoProfile -File deploy/release/run_detached.ps1 -Script <スクリプト> -Seconds 540 -OuterSeconds 570 -DiagDir <置き場> [-Logs '<ファイル>,<ファイル>']
#
# 標準ハンドルの受け継ぎを外すだけでは、ほかの受け継ぎで段のパイプを握られる形が残り、段の上限でも段が終わらない。
# そこで WMI の Win32_Process Create で起動する。起動するのは WMI のプロバイダなので、段のプロセスとの間に
# 親子関係もハンドルの受け継ぎも無い。段のパイプを持つのはこのプロセスだけで、このプロセスは子を起こさない。
# それでも段が終わらずジョブのログも消えるなら、原因はパイプではなく runner か VM の側にある。
#
# WMI で起動したプロセスは段の環境変数を受け継がないので、環境の写しをファイルで渡す（DiagDir は artifact に入るので置かない）。
#
# 締切は 2 段ある。切り離した側は Seconds 秒で中身を止めて終わる。切り離した側が OuterSeconds 秒までに終わらないか
# 終了コードを残さずに終わったら、こちらが一覧 → ログの写し → 木ごと止める の順で片付けて失敗で終える。
# 3 つの時間は 540 < 570 < 600 の順で、間にそれぞれ 30 秒の余裕を置く。540 から 570 は切り離した側の後始末の時間で、
# 570 から 600 はこちらの後始末の時間。こちらの最悪は 570 + 待ちの 1 回 2 秒 + 終了の待ち 5 秒 + WMI の一覧 2 回の 10 秒 +
# 止めた後の 1 秒の 588 秒で、残りをログの写しに使う（段が取り消されても切り離した側は Seconds 秒で自分で終わる）。
param(
  [Parameter(Mandatory = $true)][string]$Script,
  [Parameter(Mandatory = $true)][int]$Seconds,
  [Parameter(Mandatory = $true)][int]$OuterSeconds,
  [Parameter(Mandatory = $true)][string]$DiagDir,
  # 片付けのときに DiagDir に写すファイル（, 区切りの 1 語。無いものは飛ばす）
  [string]$Logs = ''
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'bounded_wait.ps1')
# 片付けの時間を上の足し算に収める
$script:CimTimeoutSec = 5

New-Item -ItemType Directory -Force -Path $DiagDir | Out-Null
$DiagDir = (Resolve-Path -LiteralPath $DiagDir).Path
$logFiles = @($Logs -split ',' | Where-Object { $_ })
$hostLog = Join-Path $DiagDir 'deadline-host.log'
$codeFile = Join-Path $DiagDir 'deadline-exit-code.txt'
$procFile = Join-Path $DiagDir 'processes.txt'
$envFile = Join-Path ([System.IO.Path]::GetTempPath()) "looptrack-env-$([guid]::NewGuid().ToString('N')).json"
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$detachedId = 0
$at = 0L

# 読めない回は読んだ位置を動かさず、次の回に回す。読めないまま続いてもループの締切で片付けに進む
function Copy-HostLog {
  try {
    $script:at = Copy-NewText $hostLog $script:at
  } catch {
    Write-Host "切り離した側のログを読めません（次の回に読みます）: $_"
  }
}

# 切り離した木と名前の当たるプロセスを、一覧 → ログの写し → 止める の順で片付ける。
# WMI が応答しないときは名前だけで探す（締切の後で WMI の上限を何度も待たない）
function Stop-Detached([string]$Title) {
  $cim = $true
  try {
    $rows = @(Get-Leftover $detachedId -IncludeRoot)
  } catch {
    $cim = $false
    Write-Host "WMI で一覧を取れません: $_"
    $rows = @(Get-Process | Where-Object {
        $n = $_.ProcessName
        $_.Id -eq $detachedId -or @($script:LeftoverNames | Where-Object { $n -like $_ }).Count -gt 0
      } | Select-Object @{ n = 'ProcessId'; e = { $_.Id } }, @{ n = 'ParentProcessId'; e = { 0 } }, @{ n = 'Name'; e = { $_.ProcessName } }, @{ n = 'CreationDate'; e = { $_.StartTime } }, @{ n = 'CommandLine'; e = { '' } })
  }
  Write-Rows $rows $Title $procFile
  Save-Logs $logFiles $DiagDir -Tail
  Stop-Rows ($rows | Sort-Object ProcessId -Descending)
  if ($cim) {
    Start-Sleep -Seconds 1
    try {
      Write-Rows @(Get-Leftover $detachedId -IncludeRoot) '止めた後に残っていたプロセス' $procFile
    } catch {
      Write-Host "WMI で一覧を取れません: $_"
    }
  }
}

$exitCode = 1
try {
  Remove-Item -LiteralPath $codeFile, "$codeFile.tmp" -Force -ErrorAction SilentlyContinue
  $vars = @{}
  foreach ($e in [Environment]::GetEnvironmentVariables().GetEnumerator()) {
    if ($e.Key -notlike '=*') { $vars[[string]$e.Key] = [string]$e.Value }
  }
  $vars | ConvertTo-Json | Set-Content -LiteralPath $envFile -Encoding utf8

  $pwshPath = (Get-Process -Id $PID).Path
  $supervisor = Join-Path $PSScriptRoot 'run_with_deadline.ps1'
  # 標準出力は cmd のリダイレクトでファイルにつなぐ。切り離した側はパイプを 1 本も持たない
  $inner = ('"{0}" -NoProfile -NonInteractive -File "{1}" -Script "{2}" -Seconds {3} -DiagDir "{4}" -Logs "{5}" -EnvFile "{6}" -CodeFile "{7}" > "{8}" 2>&1' -f
    $pwshPath, $supervisor, $Script, $Seconds, $DiagDir, $Logs, $envFile, $codeFile, $hostLog)
  $created = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -OperationTimeoutSec 30 -Arguments @{
    CommandLine      = "$env:ComSpec /d /s /c `"$inner`""
    CurrentDirectory = (Get-Location).Path
  }
  if ($created.ReturnValue -ne 0) { throw "WMI で起動できません（Win32_Process Create の戻り値 $($created.ReturnValue)）" }
  $detachedId = [int]$created.ProcessId
  Write-Host "--- [$([DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ'))] 切り離して起動しました（PID $detachedId・段の側の締切 $OuterSeconds 秒）"

  # PID の使い回しに当たらないよう、番号ではなくプロセスのハンドルで終わりを見る
  $proc = $null
  try {
    $proc = [System.Diagnostics.Process]::GetProcessById($detachedId)
    $null = $proc.Handle
  } catch {
    $proc = $null
  }

  $ended = $false
  while ($sw.Elapsed.TotalSeconds -lt $OuterSeconds) {
    $ended = ($null -eq $proc) -or $proc.WaitForExit(2000)
    Copy-HostLog
    if ($ended -or (Test-Path -LiteralPath $codeFile)) { break }
  }
  # 終了コードが書かれた後は、プロセスの終わりを短く待つ
  if (-not $ended -and (Test-Path -LiteralPath $codeFile)) { $ended = $proc.WaitForExit(5000) }
  Copy-HostLog

  $code = $null
  if (Test-Path -LiteralPath $codeFile) {
    try {
      $code = [int](Get-Content -LiteralPath $codeFile -Raw).Trim()
    } catch {
      Write-Host "終了コードを読めません: $_"
    }
  }
  if ($null -ne $code) {
    if (-not $ended) { Stop-Detached '終了コードの後も残っていたプロセス' }
    $exitCode = $code
  } else {
    $why = if ($ended) { '終了コードを残さずに終わりました' } else { "段の側の締切 $OuterSeconds 秒までに終わりません" }
    Write-Host "::error::切り離して起動した $Script が${why}（経過 $([int]$sw.Elapsed.TotalSeconds) 秒）。一覧に出して止めます"
    Stop-Detached "段の側の締切の時点のプロセス（切り離した PID $detachedId とその子孫・名前の当たるもの）"
    Copy-HostLog
    Write-Host "--- 一覧とログの写し: $DiagDir"
  }
} finally {
  Remove-Item -LiteralPath $envFile -Force -ErrorAction SilentlyContinue
}
exit $exitCode
