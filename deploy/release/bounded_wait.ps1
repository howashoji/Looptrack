# release.yml の Windows のジョブが、外のプロセスを秒数の上限つきで待つための関数（dot-source して使う）。
#
#   . ./deploy/release/bounded_wait.ps1
#   Write-Stage 'サイレント導入'
#   $p = Invoke-Bounded -FilePath $setup -ArgumentList $args -Seconds 120 -What 'インストーラ'
#   if ($p.ExitCode -ne 0) { throw … }
#
# Start-Process -Wait と引数なしの WaitForExit() は上限が無く、止まるとジョブの上限まで何も出さずに取り消される
# （取り消されると後ろの段のログの保存も走らない）。ここでは上限を超えたら、どの段で何を待っていたか・経過秒・
# 待っていたプロセスとその子孫の一覧を出して throw する。
# 後半の関数は run_with_deadline.ps1 と run_detached.ps1 が共有する（一覧・ログの写し・止め方を 1 か所に置く）。

# いまの段の名前。上限を超えたときの文面に入れる
$script:BoundedWaitStage = ''

# 中身の子孫でなくても一覧に出して止めるプロセスの名前（-like）。親が先に終わったプロセスは子孫として辿れないので名前でも拾う
$script:LeftoverNames = @('Looptrack*', 'unins*', '_iu*', '*_setup*')

# WMI の問い合わせの上限（秒）。WMI 自体が応答しないときに、締切の後の後始末まで止まらないようにする
$script:CimTimeoutSec = 15

# 段の始まりを時刻つきで出す。止まった段をログで見分けるため
function Write-Stage([string]$Name) {
  $script:BoundedWaitStage = $Name
  Write-Host "--- [$([DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ'))] $Name"
}

# RootId のプロセスとその子孫を Win32_Process の ParentProcessId でたどって返す。
# 本体が先に終わっていても、残った子は ParentProcessId に本体の PID を持ったままなので拾える
function Get-ProcessTree([int]$RootId, $Snapshot = $null) {
  $all = if ($null -ne $Snapshot) { @($Snapshot) } else { @(Get-CimInstance Win32_Process -OperationTimeoutSec $script:CimTimeoutSec) }
  $seen = @{}
  $queue = [System.Collections.Generic.Queue[int]]::new()
  $queue.Enqueue($RootId)
  $rows = @()
  while ($queue.Count -gt 0) {
    $id = $queue.Dequeue()
    if ($seen.ContainsKey($id)) { continue }
    $seen[$id] = $true
    $rows += @($all | Where-Object { $_.ProcessId -eq $id })
    foreach ($c in @($all | Where-Object { $_.ParentProcessId -eq $id -and $_.ProcessId -ne $id })) {
      $queue.Enqueue([int]$c.ProcessId)
    }
  }
  return $rows
}

function Write-ProcessTree([int]$RootId) {
  $rows = @(Get-ProcessTree $RootId)
  if ($rows.Count -eq 0) {
    Write-Host "（PID $RootId とその子孫は見つかりません）"
    return
  }
  $rows | Format-Table -AutoSize -Wrap ProcessId, ParentProcessId, Name, CreationDate, CommandLine | Out-String -Width 400 | Write-Host
}

# FilePath を起動し、本体が終わるまで最大 Seconds 秒待って、終わったプロセスを返す（ExitCode は呼び出し側で見る）。
# 既定では本体だけを待つ。Start-Process -Wait は子孫のプロセスの終了まで待つので、インストーラが起こした
# Looptrack.exe が動き続けると止まる。-WaitDescendants を付けると、本体が終わった後も子孫が無くなるまで
# 同じ上限の中で待つ（unins000.exe は自分を消すために子に引き継いで先に終わるので、こちらを使う）。
# Handle を先に取らないと、終わった後に ExitCode を読めないことがある。
# 上限を超えても待っていたプロセスは止めない。止めると Inno Setup のログの終わりが書かれずに失われ、
# 何で止まっていたかを後ろの段で残すログから追えなくなる。残ったプロセスは、run_with_deadline.ps1 が中身の終わりに
# インストーラのログを写してから一覧に出して止める
function Invoke-Bounded {
  param(
    [Parameter(Mandatory = $true)][string]$FilePath,
    [string[]]$ArgumentList = @(),
    [Parameter(Mandatory = $true)][int]$Seconds,
    [Parameter(Mandatory = $true)][string]$What,
    [switch]$WaitDescendants
  )
  $sw = [System.Diagnostics.Stopwatch]::StartNew()
  if ($ArgumentList.Count -gt 0) {
    $p = Start-Process -FilePath $FilePath -ArgumentList $ArgumentList -PassThru
  } else {
    $p = Start-Process -FilePath $FilePath -PassThru
  }
  $null = $p.Handle
  $name = [System.IO.Path]::GetFileName($FilePath)
  $waiting = '本体'
  if ($p.WaitForExit($Seconds * 1000)) {
    if (-not $WaitDescendants) { return $p }
    $waiting = '子孫'
    # 上限は Seconds 秒の締切で切る（本体を待った時間も含む）
    while ($sw.Elapsed.TotalSeconds -lt $Seconds) {
      $rest = @(Get-ProcessTree $p.Id | Where-Object { $_.ProcessId -ne $p.Id })
      if ($rest.Count -eq 0) { return $p }
      Start-Sleep -Milliseconds 500
    }
  }
  $elapsed = [int]$sw.Elapsed.TotalSeconds
  $msg = "段「$($script:BoundedWaitStage)」: $($What)（$($name)・PID $($p.Id)）の${waiting}が $Seconds 秒の上限までに終わりません（経過 $elapsed 秒）"
  Write-Host "::error::$msg"
  Write-Host "--- 待っていたプロセスとその子孫（PID $($p.Id)）"
  Write-ProcessTree $p.Id
  throw $msg
}

# 名前の当たるプロセスと、RootId の子孫を重ねずに返す（RootId 自身は -IncludeRoot のときだけ含める）
function Get-Leftover([int]$RootId, [switch]$IncludeRoot) {
  $all = @(Get-CimInstance Win32_Process -OperationTimeoutSec $script:CimTimeoutSec)
  $rows = @{}
  foreach ($r in @(Get-ProcessTree $RootId $all)) {
    if ($IncludeRoot -or $r.ProcessId -ne $RootId) { $rows[[int]$r.ProcessId] = $r }
  }
  foreach ($r in $all) {
    foreach ($pat in $script:LeftoverNames) {
      if ($r.Name -like $pat) { $rows[[int]$r.ProcessId] = $r; break }
    }
  }
  return @($rows.Values | Sort-Object ProcessId)
}

function Write-Rows($Rows, [string]$Title, [string]$File) {
  # 空の結果は $null で届くことがあるので、行として数えない
  $Rows = @(@($Rows) | Where-Object { $null -ne $_ })
  $text = "--- $Title`n"
  if ($Rows.Count -eq 0) {
    $text += "（ありません）`n"
  } else {
    $text += ($Rows | Format-Table -AutoSize -Wrap ProcessId, ParentProcessId, Name, CreationDate, CommandLine | Out-String -Width 400)
  }
  Write-Host $text
  if ($File) { Add-Content -Path $File -Value $text }
}

function Stop-Rows($Rows) {
  foreach ($r in @($Rows)) {
    # 空の結果は $null で届く。Stop-Process -Id $null は引数の検査で止まり、ErrorAction では抑えられない
    if ($null -eq $r) { continue }
    Stop-Process -Id $r.ProcessId -Force -ErrorAction SilentlyContinue
  }
}

# ファイルの Offset から後ろを読み、読み終えた位置を返す（書き込み中でも読めるよう共有で開く）
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

# インストーラのログを DiagDir に写し、-Tail なら末尾をジョブのログにも出す。プロセスを止める前に呼ぶ。
# 止めると Inno Setup がログの終わりを書かずに終わり、何で止まっていたかが追えなくなるため
function Save-Logs([string[]]$Files, [string]$DiagDir, [switch]$Tail) {
  foreach ($f in @($Files)) {
    if (-not (Test-Path $f)) { continue }
    Copy-Item -Path $f -Destination $DiagDir -Force -ErrorAction SilentlyContinue
    if ($Tail) {
      Write-Host "--- $f（末尾 60 行）"
      Get-Content -Path $f -Tail 60 -ErrorAction SilentlyContinue | ForEach-Object { Write-Host $_ }
    }
  }
}
