# 段の中身（pwsh のスクリプト）を別のプロセスで動かし、締切で子孫ごと止める（release.yml の Windows のジョブで使う）。
# 段からは直接呼ばない。run_detached.ps1 が段と親子関係もハンドルの受け継ぎも切って起動する（-EnvFile・-CodeFile はそのための引数）。
#
#   pwsh -NoProfile -File deploy/release/run_detached.ps1 -Script <スクリプト> -Seconds 540 -OuterSeconds 570 -DiagDir <置き場> [-Logs '<ファイル>,<ファイル>']
#
# 段の timeout-minutes だけに頼らないのは、Windows の runner が段を終えるのは段のプロセスが終わり、かつ段の標準出力の
# パイプが閉じたときだから。段の pwsh が起こしたプロセス（インストーラや、インストーラが起こし直した Looptrack.exe）が
# パイプの複製を受け継いだまま残ると、段の上限で runner が段のプロセスの木を止めてもパイプは閉じない。親の終わった
# プロセスは木から外れるので止められず、段の上限もジョブの上限も効かないまま runner が戻らなくなる（ログも残らない）。
#
# そこで次の 3 つを行う:
#   1. このプロセスの標準入出力のハンドルを受け継がせない印にしてから中身を起こす。中身の標準入出力はファイルにつなぐ。
#      このプロセス自身も段のパイプを持たない（run_detached.ps1 が出力をファイルにつないで起動する）
#   2. 中身が Seconds 秒の締切までに終わらなければ、待っていたプロセスの一覧（親子関係・コマンド行）を出し、
#      ログを DiagDir に写してから、中身の子孫と名前の当たるプロセスを止めて失敗で終える。締切は段の上限より短くする
#   3. 中身が終わった後も名前の当たるプロセスが残っていれば、最大 10 秒だけ自分で終わるのを待ち、ログを写してから
#      一覧に出して止める（残すと次の段のパイプを握りうる）。止めるのはいつもログを写した後にする
#
# 中身の出力はファイルから読み出してこのプロセスの出力に写すので、ジョブのログにも 2 秒ほど遅れて出る。
param(
  [Parameter(Mandatory = $true)][string]$Script,
  [Parameter(Mandatory = $true)][int]$Seconds,
  [Parameter(Mandatory = $true)][string]$DiagDir,
  # 締切と失敗のときに DiagDir に写すファイル（, 区切りの 1 語。-File では配列を渡せない。無いものは飛ばす）
  [string]$Logs = '',
  # 段の環境変数の写し（JSON）。WMI で起動したプロセスは段の環境を受け継がないので、読み込んだらすぐ消す
  [string]$EnvFile = '',
  # 終了コードを書くファイル。run_detached.ps1 はこれを見て段の結果を決める
  [string]$CodeFile = ''
)

$ErrorActionPreference = 'Stop'
if ($EnvFile) {
  $vars = Get-Content -LiteralPath $EnvFile -Raw | ConvertFrom-Json -AsHashtable
  foreach ($k in $vars.Keys) { [Environment]::SetEnvironmentVariable($k, [string]$vars[$k]) }
  Remove-Item -LiteralPath $EnvFile -Force
  # 出力はファイルを経てジョブのログに写る。日本語が化けないよう UTF-8 で書く
  try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { Write-Host "出力の文字コードを変えられません: $_" }
}
$logFiles = @($Logs -split ',' | Where-Object { $_ })

# 終了コードを別名で書いてから改名する（読む側に書きかけを見せない）。途中の例外で書かれなければ読む側が失敗として扱う
function Exit-Run([int]$Code) {
  if ($CodeFile) {
    Set-Content -LiteralPath "$CodeFile.tmp" -Value $Code
    Move-Item -LiteralPath "$CodeFile.tmp" -Destination $CodeFile -Force
  }
  exit $Code
}

. (Join-Path $PSScriptRoot 'bounded_wait.ps1')

New-Item -ItemType Directory -Force -Path $DiagDir | Out-Null
$outLog = Join-Path $DiagDir 'script-stdout.log'
$errLog = Join-Path $DiagDir 'script-stderr.log'
$inFile = Join-Path $DiagDir 'script-stdin.txt'
Set-Content -Path $inFile -Value '' -NoNewline

if ($IsWindows) {
  Add-Type -Namespace LooptrackRelease -Name StdHandle -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)]
public static extern System.IntPtr GetStdHandle(int nStdHandle);
[DllImport("kernel32.dll", SetLastError = true)]
public static extern bool SetHandleInformation(System.IntPtr hObject, uint dwMask, uint dwFlags);
'@
  # STD_INPUT_HANDLE・STD_OUTPUT_HANDLE・STD_ERROR_HANDLE から HANDLE_FLAG_INHERIT（1）を外す
  foreach ($n in -10, -11, -12) {
    $h = [LooptrackRelease.StdHandle]::GetStdHandle($n)
    if ($h -eq [IntPtr]::Zero -or $h -eq [IntPtr]::new(-1)) { continue }
    if (-not [LooptrackRelease.StdHandle]::SetHandleInformation($h, 1, 0)) {
      Write-Host "標準ハンドル $n の受け継ぎを外せません（$([System.Runtime.InteropServices.Marshal]::GetLastWin32Error())）"
    }
  }
}

$pwshPath = (Get-Process -Id $PID).Path
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$p = Start-Process -FilePath $pwshPath -ArgumentList '-NoProfile', '-NonInteractive', '-File', $Script `
  -WorkingDirectory (Get-Location).Path -NoNewWindow -PassThru `
  -RedirectStandardInput $inFile -RedirectStandardOutput $outLog -RedirectStandardError $errLog
$null = $p.Handle
Write-Host "--- [$([DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ'))] $Script を締切 $Seconds 秒で起動しました（PID $($p.Id)）"

$outAt = 0L
$errAt = 0L
$finished = $false
while ($sw.Elapsed.TotalSeconds -lt $Seconds) {
  if ($p.WaitForExit(2000)) { $finished = $true; break }
  $outAt = Copy-NewText $outLog $outAt
  $errAt = Copy-NewText $errLog $errAt
}
$outAt = Copy-NewText $outLog $outAt
$errAt = Copy-NewText $errLog $errAt
$procFile = Join-Path $DiagDir 'processes.txt'

if (-not $finished) {
  $elapsed = [int]$sw.Elapsed.TotalSeconds
  $msg = "$Script が締切 $Seconds 秒までに終わりません（経過 $elapsed 秒）。待っていたプロセスを一覧に出して止めます"
  Write-Host "::error::$msg"
  $rows = @(Get-CimInstance Win32_Process -Filter "ProcessId=$($p.Id)") + @(Get-Leftover $p.Id)
  Write-Rows $rows "締切の時点のプロセス（中身の PID $($p.Id) とその子孫・名前の当たるもの）" $procFile
  # 順序: 一覧 → ログを写す → 止める
  Save-Logs $logFiles $DiagDir -Tail
  Stop-Rows (@(Get-ProcessTree $p.Id) | Sort-Object ProcessId -Descending)
  Stop-Rows (Get-Leftover $p.Id)
  $outAt = Copy-NewText $outLog $outAt
  $errAt = Copy-NewText $errLog $errAt
  Write-Host "--- 一覧とログの写し: $DiagDir"
  Exit-Run 1
}

$code = $p.ExitCode
$rest = @(Get-Leftover $p.Id)
if ($rest.Count -gt 0) {
  # 残ったものが自分で終わる（インストーラがログを書き終える）のを最大 10 秒だけ待ってから、ログを写して止める
  for ($i = 0; $i -lt 20 -and $rest.Count -gt 0; $i++) {
    Start-Sleep -Milliseconds 500
    $rest = @(Get-Leftover $p.Id)
  }
}
if ($rest.Count -gt 0 -or $code -ne 0) {
  Save-Logs $logFiles $DiagDir -Tail:($code -ne 0 -or $rest.Count -gt 0)
}
if ($rest.Count -gt 0) {
  Write-Rows $rest "中身（終了コード $code）が終わった後に残っていたプロセス（ログを写した後で止めます）" $procFile
  Stop-Rows $rest
}
if ($code -ne 0) {
  Write-Host "::error::$Script が終了コード $code で終わりました（経過 $([int]$sw.Elapsed.TotalSeconds) 秒）"
}
Exit-Run $code
