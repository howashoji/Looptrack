# Windows のデスクトップ版（zip を展開したもの）の起動を確かめる（release.yml の Windows のジョブと手元で使う）。
#
#   pwsh -File deploy/release/desktop_smoke.ps1 -Exe <展開先>\Looptrack\Looptrack.exe
#
# desktop_smoke.sh（macOS・Linux 用）の Windows 版。同じことを確かめる:
#   1. desktop --no-tray --background で起動 → --status で URL が分かり、healthz が ok、画面は初回設定へ転送（303）
#   2. 2 つ目の起動（desktop --background）は二重に立たず「既に起動しています」で 0 で終わる
#   3. desktop --quit で止まり、--status が 1 になる
#   4. データの置き場に DB・鍵・設定・ログができている
#
# データは一時ディレクトリに置く（利用者の本物の置き場を使わない）。--background はブラウザを開かない。
param(
  [Parameter(Mandatory = $true)][string]$Exe
)

$ErrorActionPreference = 'Stop'
# 外部コマンドの終了コードは自分で見る（$LASTEXITCODE。0 でないだけで例外にしない）
$PSNativeCommandUseErrorActionPreference = $false

if (-not (Test-Path $Exe)) { throw "desktop_smoke: 実行ファイルがありません: $Exe" }
$Exe = (Resolve-Path $Exe).Path
$Cli = Join-Path (Split-Path -Parent $Exe) 'cli\looptrack.exe'
if (-not (Test-Path $Cli)) { throw "desktop_smoke: CLI 実行ファイルがありません: $Cli" }
$Cli = (Resolve-Path $Cli).Path

$base = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { $env:TEMP }
$work = Join-Path $base ("looptrack-smoke-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work -Force | Out-Null
$data = Join-Path $work 'data'
$state = Join-Path $data 'desktop.json'
$out = Join-Path $work 'out.txt'
$err = Join-Path $work 'err.txt'
$env:LOOPTRACK_DATA_DIR = $data
$env:LOOPTRACK_DESKTOP_PORT = '0'   # OS に空きポートを選ばせる

$proc = $null
function Fail([string]$msg) {
  Write-Host "desktop_smoke: NG: $msg"
  foreach ($f in @($out, $err, (Join-Path $data 'logs\looptrack.log'))) {
    if (Test-Path $f) {
      Write-Host "--- $f"
      Get-Content $f -Tail 20 | ForEach-Object { Write-Host $_ }
    }
  }
  if ($proc -and -not $proc.HasExited) { $proc.Kill() }
  throw "desktop_smoke: $msg"
}

Write-Host "--- 起動: $Exe desktop --no-tray --background"
$proc = Start-Process -FilePath $Exe -ArgumentList 'desktop', '--no-tray', '--background' `
  -PassThru -RedirectStandardOutput $out -RedirectStandardError $err

$url = ''
for ($i = 0; $i -lt 150; $i++) {
  # --status 自体が起動中のインスタンスを最大 30 秒待つので、起動待ちの短い再試行には使わない。
  # primary が原子的に書く状態ファイルを見て、URL が出た後に --status も別に検査する。
  if (Test-Path $state) {
    try {
      $s = Get-Content $state -Raw | ConvertFrom-Json
      if ($s.pid -gt 0 -and $s.url) { $url = [string]$s.url; break }
    } catch {
      # 書き換えと重なったら次の 200 ms で読み直す。
    }
  }
  $proc.Refresh()
  if ($proc.HasExited) { Fail "起動しないで終わった（終了コード $($proc.ExitCode)）" }
  Start-Sleep -Milliseconds 200
}
if (-not $url) { Fail '30 秒で起動しない' }
$url = $url.Trim()
Write-Host "起動: $url"

$health = Invoke-WebRequest -UseBasicParsing -Uri "${url}healthz"
if ($health.Content.Trim() -ne 'ok') { Fail "healthz が ok でない: $($health.Content)" }

$status = (& $Cli desktop --status 2>&1 | Out-String).Trim()
if ($LASTEXITCODE -ne 0) { Fail "--status が失敗した（$LASTEXITCODE）: $status" }
if ($status -ne $url) { Fail "--status の URL が違う: $status（起動: $url）" }

# 画面（/）は初回設定へ転送する。Invoke-WebRequest の -MaximumRedirection 0 は PowerShell 7 でも
# 転送を例外にするので、自動転送を切った HttpClient で 303 と Location を直接見る。
$handler = [System.Net.Http.HttpClientHandler]::new()
$handler.AllowAutoRedirect = $false
$client = [System.Net.Http.HttpClient]::new($handler)
try {
  $top = $client.GetAsync($url).GetAwaiter().GetResult()
  if ([int]$top.StatusCode -ne 303) { Fail "画面が 303 で転送しない: $([int]$top.StatusCode)" }
  $to = $top.Headers.Location.ToString()
  if (-not $to.EndsWith('/first-run')) { Fail "転送先が初回設定でない: $to" }
} finally {
  if ($top) { $top.Dispose() }
  $client.Dispose()
  $handler.Dispose()
}

Write-Host '--- 2 つ目の起動は二重に立たない'
$second = (& $Cli desktop --background 2>&1 | Out-String)
if ($LASTEXITCODE -ne 0) { Fail "2 つ目の起動が失敗した（$LASTEXITCODE）: $second" }
if ($second -notmatch '既に起動しています') { Fail "2 つ目の起動が二重に立った？: $second" }

Write-Host '--- --quit で止まる'
& $Cli desktop --quit | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "--quit が失敗した（$LASTEXITCODE）" }
Wait-Process -Id $proc.Id -Timeout 30 -ErrorAction SilentlyContinue
$proc.Refresh()
if (-not $proc.HasExited) { Fail '--quit の後も動いている' }
# 穏やかに止まったことを終了コードで見る。Windows のデスクトップ版は、トレイの窓に依らない「終了を頼む印」で
# シグナルと同じ道から終わる。頼んでも止まらなかったときだけ強制終了に進むので、そのときは 0 にならない。
if ($proc.ExitCode -ne 0) { Fail "--quit が穏やかに止めていない（終了コード $($proc.ExitCode)）" }
Write-Host '止まった（終了コード 0）'
$proc = $null

& $Cli desktop --status *> $null
if ($LASTEXITCODE -eq 0) { Fail '止めた後も --status が 0' }

foreach ($f in @('looptrack.db', 'looptrack.db.secret-key', 'desktop.json', 'logs\looptrack.log')) {
  if (-not (Test-Path (Join-Path $data $f))) { Fail "$f が無い" }
}

Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
Write-Host 'desktop_smoke: OK（起動・画面・二重起動の防止・--quit）'
