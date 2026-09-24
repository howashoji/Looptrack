; Looptrack のデスクトップ版（Windows）のインストーラ（DESIGN.md §5-14・RELEASE.md「デスクトップ版」）。
;
; Inno Setup 7（ISCC）でコンパイルする。Windows でしか動かないので、組み立ては release.yml の
; desktop-windows-installer ジョブ（windows-2025 / windows-11-arm）で行う。手元からは
;
;   bash deploy/release/desktop.sh windows-installer <版> <amd64|arm64> <zip> <出力先>
;
; が ISCC を呼ぶ（zip の中身をそのまま入れる＝持ち運び用の zip と同じ実行ファイル）。
;
; 利用者の判断（2026-09-20）:
;   - 管理者権限は要らない（PrivilegesRequired=lowest）。置き場は %LOCALAPPDATA%\Programs\Looptrack Desktop
;     （CLI の置き場 %LOCALAPPDATA%\Programs\looptrack と、大小の違いだけで同じ名前にならないようにする）
;   - スタートメニューには必ず登録する。デスクトップのショートカットは出さない
;   - インストール時の選択肢は「ログイン時に起動する」「CLI を使えるようにする」の 2 つだけ
;   - アンインストールでデータを消す選択肢は出さない（データは残す。消し方は利用者ガイド）
;   - 署名はしない（SmartScreen の「詳細情報 → 実行」を利用者ガイドに書く）
;
; desktop.sh が /D で渡すもの（既定値は手元で構文を確かめるためのもの）:
;   MyAppVersion（例 v1.0.0）・MyVersionInfo（0〜65535 の数字を点で 2〜4 つ。試しの版は 0.0.0）・MyArch（amd64|arm64）
;   MySourceDir（zip を展開した Looptrack\ のパス）・MyOutputDir・MyOutputBase・MyIconFile

#ifndef MyAppVersion
  #define MyAppVersion "v0.0.0-dev"
#endif
#ifndef MyVersionInfo
  #define MyVersionInfo "0.0.0"
#endif
#ifndef MyArch
  #define MyArch "amd64"
#endif
#ifndef MySourceDir
  #define MySourceDir "."
#endif
#ifndef MyOutputDir
  #define MyOutputDir "."
#endif
#ifndef MyOutputBase
  #define MyOutputBase "Looptrack_setup"
#endif
#ifndef MyIconFile
  #define MyIconFile ""
#endif

#if MyArch == "arm64"
  #define MyArchAllowed "arm64"
#else
  ; x64compatible は x64 と、x64 を実行できる arm64 を含む（arm64 用が取れなかったときの逃げ道）
  #define MyArchAllowed "x64compatible"
#endif

[Setup]
; AppId は**変えない**（変えると更新が上書きにならず、アンインストールの登録が二重になる）。amd64 と arm64 で同じ
; ＝片方をもう片方で上書きして入れ替えられる
AppId={{90128D15-CB14-42C2-9C7E-38680F79F5C9}
AppName=Looptrack
AppVersion={#MyAppVersion}
AppVerName=Looptrack {#MyAppVersion}
VersionInfoVersion={#MyVersionInfo}
VersionInfoProductName=Looptrack
AppPublisher=Looptrack
AppPublisherURL=https://github.com/howashoji/looptrack
AppSupportURL=https://github.com/howashoji/looptrack/blob/main/docs/guide/desktop.md
AppUpdatesURL=https://github.com/howashoji/looptrack/releases
; 管理者権限を要らなくする（利用者ごとの導入。昇格の選択肢も出さない）
PrivilegesRequired=lowest
DefaultDirName={localappdata}\Programs\Looptrack Desktop
DefaultGroupName=Looptrack
; スタートメニューのフォルダは選ばせない（必ず登録する）
DisableProgramGroupPage=yes
; 起動中の Looptrack.exe は Restart Manager で閉じる（[Code] の desktop --quit と合わせて二重の備え）
CloseApplications=yes
RestartApplications=no
ArchitecturesAllowed={#MyArchAllowed}
MinVersion=10.0
OutputDir={#MyOutputDir}
OutputBaseFilename={#MyOutputBase}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
UninstallDisplayName=Looptrack
UninstallDisplayIcon={app}\Looptrack.exe
#if MyIconFile != ""
SetupIconFile={#MyIconFile}
#endif

[Languages]
Name: "japanese"; MessagesFile: "compiler:Languages\Japanese.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[CustomMessages]
japanese.TaskGroup=追加の設定:
english.TaskGroup=Additional options:
japanese.TaskStartup=ログイン時に起動する
english.TaskStartup=Start Looptrack when you log in
japanese.TaskCLI=CLI を使えるようにする（端末・AI から looptrack を呼べるようにする）
english.TaskCLI=Make the looptrack CLI available (for terminals and coding agents)
japanese.StatusStartup=ログイン時の起動を登録しています…
english.StatusStartup=Registering Looptrack to start at login...
japanese.StatusCLI=CLI を使えるようにしています…
english.StatusCLI=Making the looptrack CLI available...
japanese.LaunchApp=Looptrack を起動する
english.LaunchApp=Launch Looptrack

[Tasks]
Name: "startup"; Description: "{cm:TaskStartup}"; GroupDescription: "{cm:TaskGroup}"
Name: "cli"; Description: "{cm:TaskCLI}"; GroupDescription: "{cm:TaskGroup}"

[Files]
Source: "{#MySourceDir}\Looptrack.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MySourceDir}\cli\looptrack.exe"; DestDir: "{app}\cli"; Flags: ignoreversion
; 第三者のライセンス文。メモ帳で開けるよう .txt にする
Source: "{#MySourceDir}\NOTICE"; DestDir: "{app}"; DestName: "NOTICE.txt"; Flags: ignoreversion
Source: "{#MySourceDir}\OFL-BIZUDGothic.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
; スタートメニューだけ（デスクトップのショートカットは出さない）。lowest なので {autoprograms} = {userprograms}
Name: "{group}\Looptrack"; Filename: "{app}\Looptrack.exe"; Comment: "Looptrack"

[Run]
; 選択肢（Tasks）は、アプリを起動する前に済ませる。どちらも管理者権限は要らず、サーバも上げない
Filename: "{app}\Looptrack.exe"; Parameters: "desktop --enable-autostart"; Tasks: startup; \
  StatusMsg: "{cm:StatusStartup}"; Flags: runhidden waituntilterminated
Filename: "{app}\Looptrack.exe"; Parameters: "desktop --install-cli"; Tasks: cli; \
  StatusMsg: "{cm:StatusCLI}"; Flags: runhidden waituntilterminated
Filename: "{app}\Looptrack.exe"; Description: "{cm:LaunchApp}"; Flags: nowait postinstall skipifsilent

[Code]
function AppExe(): String;
begin
  Result := ExpandConstant('{app}\Looptrack.exe');
end;

// 起動中の Looptrack を止める（更新の上書きの前・アンインストールの前）。
// Windows の GUI のプロセスなのでシグナルは送れない。--quit がトレイの窓に WM_CLOSE を送り、
// 止まらなければ TerminateProcess に進む（internal/client/desktop）。
procedure StopRunningApp();
var
  Code: Integer;
begin
  if FileExists(AppExe()) then
    Exec(AppExe(), 'desktop --quit', '', SW_HIDE, ewWaitUntilTerminated, Code);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  StopRunningApp();
  Result := '';
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Code: Integer;
begin
  if CurUninstallStep = usUninstall then
  begin
    StopRunningApp();
    // このアプリが登録した「ログイン時の起動」（HKCU の Run）と、このアプリが置いた CLI の写しを消す。
    // **データ（%LOCALAPPDATA%\Looptrack）は消さない**（利用者の判断。消し方は利用者ガイド）
    if FileExists(AppExe()) then
      Exec(AppExe(), 'desktop --unregister', '', SW_HIDE, ewWaitUntilTerminated, Code);
  end;
end;
