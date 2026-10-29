# 段の中身（pwsh のスクリプト）を別のプロセスで動かし、締切で子孫ごと止める（release.yml の Windows のジョブで使う）。
#
#   pwsh -NoProfile -File deploy/release/run_with_deadline.ps1 -Script <スクリプト> -Seconds 540 -DiagDir <置き場> [-Logs '<ファイル>,<ファイル>']
#
# 段の timeout-minutes だけに頼らないのは、Windows の runner が段を終えるのは段のプロセスが終わり、かつ段の標準出力の
# パイプが閉じたときだから。段の pwsh が起こしたプロセス（インストーラや、インストーラが起こし直した Looptrack.exe）が
# パイプの複製を受け継いだまま残ると、段の上限で runner が段のプロセスの木を止めてもパイプは閉じない。親の終わった
# プロセスは木から外れるので止められず、段の上限もジョブの上限も効かないまま runner が戻らなくなる（ログも残らない）。
#
# そこで次の 3 つを行う:
#   1. このプロセスの標準入出力のハンドルを受け継がせない印にしてから中身を起こす。中身の標準入出力はファイルにつなぐ。
#      中身とその子孫が何を残しても、段のパイプを握るのはこのプロセスだけになる
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
  # 中身の子孫でなくても一覧に出して止めるプロセスの名前（-like）。親が先に終わったプロセスは子孫として辿れないので名前でも拾う
  [string[]]$ProcessNames = @('Looptrack*', 'unins*', '_iu*', '*_setup*')
)

$ErrorActionPreference = 'Stop'
$logFiles = @($Logs -split ',' | Where-Object { $_ })
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

# 名前の当たるプロセスと、RootId の子孫を重ねずに返す（RootId 自身を除く）
function Get-Leftover([int]$RootId) {
  $rows = @{}
  foreach ($r in @(Get-ProcessTree $RootId)) {
    if ($r.ProcessId -ne $RootId) { $rows[[int]$r.ProcessId] = $r }
  }
  foreach ($r in @(Get-CimInstance Win32_Process)) {
    foreach ($pat in $ProcessNames) {
      if ($r.Name -like $pat) { $rows[[int]$r.ProcessId] = $r; break }
    }
  }
  return @($rows.Values | Sort-Object ProcessId)
}

function Write-Rows($Rows, [string]$Title, [string]$File) {
  $text = "--- $Title`n"
  if (@($Rows).Count -eq 0) {
    $text += "（ありません）`n"
  } else {
    $text += ($Rows | Format-Table -AutoSize -Wrap ProcessId, ParentProcessId, Name, CreationDate, CommandLine | Out-String -Width 400)
  }
  Write-Host $text
  if ($File) { Add-Content -Path $File -Value $text }
}

function Stop-Rows($Rows) {
  foreach ($r in @($Rows)) {
    Stop-Process -Id $r.ProcessId -Force -ErrorAction SilentlyContinue
  }
}

# ファイルの Offset から後ろを読み、読み終えた位置を返す（中身が書き込み中でも読めるよう共有で開く）
function Copy-NewText([string]$Path, [long]$Offset) {
  if (-not (Test-Path $Path)) { return $Offset }
  $fs = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite -bor [System.IO.FileShare]::Delete)
  try {
    if ($fs.Length -le $Offset) { return $Offset }
    $null = $fs.Seek($Offset, [System.IO.SeekOrigin]::Begin)
    $reader = [System.IO.StreamReader]::new($fs, [System.Text.Encoding]::UTF8)
    $text = $reader.ReadToEnd()
    if ($text.Length -gt 0) { Write-Host -NoNewline $text }
    return $fs.Length
  } finally {
    $fs.Dispose()
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

# インストーラのログを DiagDir に写し、末尾をジョブのログにも出す。プロセスを止める前に呼ぶ。
# 止めると Inno Setup がログの終わりを書かずに終わり、何で止まっていたかが追えなくなるため
function Save-Logs([switch]$Tail) {
  foreach ($f in $logFiles) {
    if (-not (Test-Path $f)) { continue }
    Copy-Item -Path $f -Destination $DiagDir -Force -ErrorAction SilentlyContinue
    if ($Tail) {
      Write-Host "--- $f（末尾 60 行）"
      Get-Content -Path $f -Tail 60 -ErrorAction SilentlyContinue | ForEach-Object { Write-Host $_ }
    }
  }
}

if (-not $finished) {
  $elapsed = [int]$sw.Elapsed.TotalSeconds
  $msg = "$Script が締切 $Seconds 秒までに終わりません（経過 $elapsed 秒）。待っていたプロセスを一覧に出して止めます"
  Write-Host "::error::$msg"
  $rows = @(Get-CimInstance Win32_Process -Filter "ProcessId=$($p.Id)") + @(Get-Leftover $p.Id)
  Write-Rows $rows "締切の時点のプロセス（中身の PID $($p.Id) とその子孫・名前の当たるもの）" $procFile
  # 順序: 一覧 → ログを写す → 止める
  Save-Logs -Tail
  Stop-Rows (@(Get-ProcessTree $p.Id) | Sort-Object ProcessId -Descending)
  Stop-Rows (Get-Leftover $p.Id)
  $outAt = Copy-NewText $outLog $outAt
  $errAt = Copy-NewText $errLog $errAt
  Write-Host "--- 一覧とログの写し: $DiagDir"
  exit 1
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
  Save-Logs -Tail:($code -ne 0 -or $rest.Count -gt 0)
}
if ($rest.Count -gt 0) {
  Write-Rows $rest "中身（終了コード $code）が終わった後に残っていたプロセス（ログを写した後で止めます）" $procFile
  Stop-Rows $rest
}
if ($code -ne 0) {
  Write-Host "::error::$Script が終了コード $code で終わりました（経過 $([int]$sw.Elapsed.TotalSeconds) 秒）"
}
exit $code
