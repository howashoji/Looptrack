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

# いまの段の名前。上限を超えたときの文面に入れる
$script:BoundedWaitStage = ''

# 段の始まりを時刻つきで出す。止まった段をログで見分けるため
function Write-Stage([string]$Name) {
  $script:BoundedWaitStage = $Name
  Write-Host "--- [$([DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ'))] $Name"
}

# RootId のプロセスとその子孫を Win32_Process の ParentProcessId でたどって返す。
# 本体が先に終わっていても、残った子は ParentProcessId に本体の PID を持ったままなので拾える
function Get-ProcessTree([int]$RootId) {
  $all = @(Get-CimInstance Win32_Process)
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
# 何で止まっていたかを後ろの段で残すログから追えなくなる。残ったプロセスはジョブの終わりに runner が片付ける
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
