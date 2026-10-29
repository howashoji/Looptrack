# Windows のインストーラの通しの smoke test（導入 → 起動中の上書き → 無人の上書きでの起動し直し → アンインストール）。
# release.yml の desktop-windows-installer が run_with_deadline.ps1 を通して起動する（作業ディレクトリはリポジトリの根）。
#
#   pwsh -NoProfile -File deploy/release/run_with_deadline.ps1 -Script deploy/release/installer_smoke.ps1 -Seconds 540 -DiagDir <置き場>
#
# 使う環境変数: APP_ID（Looptrack.iss の AppId）・RUNNER_TEMP（インストーラのログの置き場）。
# out\*_setup.exe（desktop.sh windows-installer の出力）を導入する。
# 段の中で直接動かさないのは、外のプロセスが段の標準出力を握ったまま残ると、段の上限でもジョブの上限でも
# runner が段を終えられなくなるため（run_with_deadline.ps1 の冒頭）。

$ErrorActionPreference = 'Stop'
# 出力はファイルを経てジョブのログに写る。日本語が化けないよう UTF-8 で書く
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$PSNativeCommandUseErrorActionPreference = $false
# 外のプロセスは Invoke-Bounded で上限つきで待つ（超えたら待っていたプロセスと子孫の一覧を出して throw する）
. ./deploy/release/bounded_wait.ps1
$setup = (Get-ChildItem out\*_setup.exe)[0].FullName
$app   = Join-Path $env:LOCALAPPDATA 'Programs\Looptrack Desktop'
$data  = Join-Path $env:LOCALAPPDATA 'Looptrack'
$cli   = Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$unKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\$($env:APP_ID)_is1"
$lnk = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Looptrack\Looptrack.lnk'
$setupArgs = @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART','/SP-','/TASKS=startup,cli',
          "/LOG=$env:RUNNER_TEMP\install.log")

Write-Stage 'サイレント導入（選択肢は両方 on）'
$p = Invoke-Bounded -FilePath $setup -ArgumentList $setupArgs -Seconds 120 -What 'インストーラ（サイレント導入）'
if ($p.ExitCode -ne 0) { throw "インストーラが $($p.ExitCode) で終わりました" }
foreach ($f in @('Looptrack.exe','cli\looptrack.exe','NOTICE.txt','OFL-BIZUDGothic.txt')) {
  if (-not (Test-Path (Join-Path $app $f))) { throw "$f がありません（$app）" }
}
if (-not (Test-Path $unKey)) { throw "HKCU の Uninstall キーがありません: $unKey" }
if (-not (Test-Path $lnk)) { throw "スタートメニューの .lnk がありません: $lnk" }
Write-Stage '同梱の CLI と、選択肢で置いた CLI の写し'
& (Join-Path $app 'cli\looptrack.exe') version
if ($LASTEXITCODE -ne 0) { throw "同梱の CLI が動きません（$LASTEXITCODE）" }
if (-not (Test-Path $cli)) { throw "CLI の写しがありません: $cli" }
& $cli version
if ($LASTEXITCODE -ne 0) { throw "CLI の写しが動きません（$LASTEXITCODE）" }
$run = (Get-ItemProperty -Path $runKey -Name Looptrack -ErrorAction SilentlyContinue).Looptrack
if (-not $run) { throw 'ログイン時の起動（HKCU の Run の値）が登録されていません' }
Write-Host "Run の値: $run"

Write-Stage '起動してから上書き（更新）'
$proc = Start-Process -FilePath (Join-Path $app 'Looptrack.exe') `
  -ArgumentList 'desktop','--background','--no-tray' -PassThru
$up = $false
for ($i = 0; $i -lt 60; $i++) {
  & $cli desktop --status *> $null
  if ($LASTEXITCODE -eq 0) { $up = $true; break }
  Start-Sleep -Seconds 1
}
if (-not $up) { throw 'デスクトップ版が起動しません' }
if (-not (Test-Path (Join-Path $data 'looptrack.db'))) { throw 'データの置き場に DB がありません' }
'smoke' | Out-File -FilePath (Join-Path $data 'smoke.txt') -Encoding utf8
$p = Invoke-Bounded -FilePath $setup -ArgumentList $setupArgs -Seconds 120 -What 'インストーラ（起動中の上書き）'
if ($p.ExitCode -ne 0) { throw "起動中の上書きが $($p.ExitCode) で終わりました" }
Wait-Process -Id $proc.Id -Timeout 60 -ErrorAction SilentlyContinue
$proc.Refresh()
if (-not $proc.HasExited) { throw '上書きのときに起動中のインスタンスが止まっていません' }
if (-not (Test-Path (Join-Path $app 'Looptrack.exe'))) { throw '上書きの後に Looptrack.exe がありません' }
if (-not (Test-Path (Join-Path $data 'smoke.txt'))) { throw '上書きでデータが消えました' }

# デスクトップ版の 1 クリックの置き換え（internal/client/desktop/replace.go）と同じ引数で、起動中に無人で上書きする。
# 並びは replace.go の installerArgs と同じ（TestInstallerScriptSettings が両方を同じ読み方で確かめる）。
# /TASKS は付けない（前回の選択肢を引き継ぐ）。/RELAUNCH=1 で Looptrack.iss の DeinitializeSetup が起動し直す
$updateArgs = @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART','/SP-','/RELAUNCH=1')
$statePath = Join-Path $data 'desktop.json'
# setup.exe の本体だけを待つ（Invoke-Bounded）。Start-Process -Wait は子孫のプロセスの終了まで待つので、
# DeinitializeSetup が起こした Looptrack.exe が動き続けると止まる
function Invoke-Setup([string[]]$SetupArgs, [string]$What) {
  return Invoke-Bounded -FilePath $setup -ArgumentList $SetupArgs -Seconds 120 -What $What
}
function Wait-Status([int]$OldPid) {
  for ($i = 0; $i -lt 60; $i++) {
    & $cli desktop --status *> $null
    if ($LASTEXITCODE -eq 0) {
      $st = Get-Content $statePath -Raw | ConvertFrom-Json
      if ($st.pid -and $st.pid -ne $OldPid) { return $st.pid }
    }
    Start-Sleep -Seconds 1
  }
  return 0
}

Write-Stage '無人の上書きで起動し直す（成功の経路）'
$proc = Start-Process -FilePath (Join-Path $app 'Looptrack.exe') `
  -ArgumentList 'desktop','--background','--no-tray' -PassThru
if ((Wait-Status 0) -ne $proc.Id) { throw 'デスクトップ版が起動しません（無人の上書きの前）' }
$p = Invoke-Setup ($updateArgs + "/LOG=$env:RUNNER_TEMP\update.log") 'インストーラ（無人の上書き）'
if ($p.ExitCode -ne 0) { throw "無人の上書きが $($p.ExitCode) で終わりました" }
Wait-Process -Id $proc.Id -Timeout 60 -ErrorAction SilentlyContinue
$proc.Refresh()
if (-not $proc.HasExited) { throw '無人の上書きのときに起動中のインスタンスが止まっていません' }
$relaunched = Wait-Status $proc.Id
if (-not $relaunched) { throw '無人の上書きの後、60 秒以内に起動し直していません（desktop --status が 0 に戻らない）' }
Write-Host "起動し直した pid: $relaunched"
if (-not (Get-ItemProperty -Path $runKey -Name Looptrack -ErrorAction SilentlyContinue)) {
  throw '無人の上書きでログイン時の起動（HKCU の Run の値）が消えました（/TASKS を付けずに前回の選択肢を引き継ぐはず）'
}
if (-not (Test-Path $cli)) { throw "無人の上書きで CLI の写しが消えました: $cli" }
if (-not (Test-Path (Join-Path $data 'smoke.txt'))) { throw '無人の上書きでデータが消えました' }

Write-Stage '無人の上書きが失敗しても起動し直す（NOTICE.txt を共有なしで開いたまま上書きさせる）'
# NOTICE.txt は CloseApplicationsFilter（*.exe,*.dll,*.chm）の外なので、Restart Manager はこの pwsh を閉じない。
# Inno はファイルを置き換えられずに中止して元に戻し、DeinitializeSetup が前の版を起動し直す
$notice = Join-Path $app 'NOTICE.txt'
$held = [System.IO.File]::Open($notice, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::None)
try {
  $p = Invoke-Setup ($updateArgs + "/LOG=$env:RUNNER_TEMP\update-fail.log") 'インストーラ（失敗させる無人の上書き）'
} finally {
  $held.Close()
}
# 対照: 失敗の経路を通ったこと（0 でない終了コード）。0 なら共有なしで開いても上書きが通っており、失敗の経路を見ていない
if ($p.ExitCode -eq 0) {
  throw '前提が崩れています: NOTICE.txt を共有なしで開いたままでも無人の上書きが 0 で終わりました（失敗の経路を通っていません）'
}
Write-Host "失敗した上書きの終了コード: $($p.ExitCode)"
Wait-Process -Id $relaunched -Timeout 60 -ErrorAction SilentlyContinue
if (Get-Process -Id $relaunched -ErrorAction SilentlyContinue) { throw '失敗した上書きのときに起動中のインスタンスが止まっていません' }
$again = Wait-Status $relaunched
if (-not $again) { throw '失敗した上書きの後、60 秒以内に前の版が起動し直していません' }
Write-Host "失敗の後に起動し直した pid: $again"
if (-not (Test-Path (Join-Path $app 'Looptrack.exe'))) { throw '失敗した上書きの後に Looptrack.exe がありません' }
& $cli desktop --quit
if ($LASTEXITCODE -ne 0) { throw "起動し直したインスタンスを止められません（$LASTEXITCODE）" }

Write-Stage 'アンインストール（データは残り、Run の値と CLI の写しは消える）'
$uninst = (Get-ItemProperty -Path $unKey).UninstallString.Trim('"')
# unins000.exe は自分を消すために子プロセスに引き継いで先に終わるので、子孫が終わるまで待ち（-WaitDescendants）、
# そのうえでファイルが消えるまで待つ
$p = Invoke-Bounded -FilePath $uninst -ArgumentList '/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART' -Seconds 120 -What 'アンインストーラ' -WaitDescendants
Write-Host "アンインストーラの終了コード: $($p.ExitCode)"
for ($i = 0; $i -lt 60; $i++) {
  if (-not (Test-Path (Join-Path $app 'Looptrack.exe'))) { break }
  Start-Sleep -Seconds 1
}
if (Test-Path (Join-Path $app 'Looptrack.exe')) { throw 'アンインストールされていません' }
if (Test-Path $unKey) { throw 'HKCU の Uninstall キーが残っています' }
if (Test-Path $lnk) { throw 'スタートメニューの .lnk が残っています' }
if (-not (Test-Path (Join-Path $data 'smoke.txt'))) { throw 'アンインストールでデータが消えました' }
if ((Get-ItemProperty -Path $runKey -Name Looptrack -ErrorAction SilentlyContinue)) {
  throw 'ログイン時の起動（HKCU の Run の値）が残っています'
}
if (Test-Path $cli) { throw "CLI の写しが残っています: $cli" }
Write-Host 'smoke test: OK'
exit 0
