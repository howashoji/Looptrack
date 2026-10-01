// Package setuppath は、setup が looptrack を置く場所（macOS・Linux は ~/.local/bin、Windows は
// %LOCALAPPDATA%\Programs\looptrack）を利用者の PATH に足す手順の断片を組む（DESIGN.md §5-1「配布と更新」）。
//
// サーバの setup ツールの取得 + init の手順（internal/server の setup_go.go）がこの断片を包みの中に入れ、
// クライアントの doctor が「PATH に looptrack が無い」ときの直し方として同じ断片を単独の形で示す
// （規則を 2 か所に写さない）。
//
// kit・MCP・hook・CLI の文面は素の `looptrack` で書いてあり、AI の許可のパターン（`Bash(looptrack issue:*)`）も
// 名前で当てる。置き場を PATH に足せば、それらがそのまま動く。
//
// 足す先（理由は DESIGN.md に書く）:
//   - macOS・Linux は利用者の既定のシェル（$SHELL）の起動ファイル。zsh は ~/.zshenv（zsh は対話でもログインでもない
//     `zsh -c` でもこれだけは読む。AI のアプリはコマンドを `$SHELL -c` か `-lc` で流す）。zsh が起動のときに読むのは
//     「環境の ZDOTDIR の下、無ければ HOME の下」の .zshenv なので、必ず ~/.zshenv に書き、ZDOTDIR が環境にあって HOME と
//     違えば $ZDOTDIR/.zshenv にも書く（~/.zshenv の中で ZDOTDIR を決めている利用者のシェルから流すと、ZDOTDIR が環境に
//     ある。そこにだけ書くと、ZDOTDIR を持たない新しい端末や GUI から起動した AI が読まない）。
//     bash は ~/.bashrc と、ログインのシェルが読むファイル（~/.bash_profile → ~/.bash_login → ~/.profile の最初に
//     あるもの。どれも無ければ ~/.profile を作る。~/.bash_profile を新しく作ると ~/.profile が読まれなくなるので作らない）。
//     fish は conf.d の looptrack.fish。それ以外（sh・dash・ksh・不明）は ~/.profile。
//   - Windows は利用者の環境変数 Path（HKCU\Environment）。setx は 1024 文字で切り詰めるので使わない。
//     [Environment]::SetEnvironmentVariable で書くと値の種類が REG_SZ になり、%USERPROFILE% のような項目が
//     展開されなくなるので、レジストリを展開せずに読み、元の種類のまま書く。書いた後の通知（WM_SETTINGCHANGE）は
//     [Environment]::SetEnvironmentVariable が送るので、使い捨ての変数を置いて消すことで送る。
//
// 冪等: いまの PATH に置き場があれば何もしない。起動ファイルに同じ行があれば足さない（Windows は Path に同じ項目が
// あれば足さない）。2 回流しても増えない。
//
// その場の端末: 断片は包み（sh のサブシェル・PowerShell の & { }）の中で動くので、貼った端末の PATH は変わらない。
// そのため、足した（または既に起動ファイルにあった）ときは「AI のアプリと端末を開き直す」を出す。
package setuppath

import (
	"fmt"

	"github.com/howashoji/looptrack/internal/i18n"
)

const (
	// PosixDir は macOS・Linux の置き場（シェルの書き方）。
	PosixDir = `$HOME/.local/bin`
	// Marker は起動ファイルに足す行の末尾の印（人が見て、どこが足したかを分かるように）。
	Marker = "# looptrack"
	// PosixLine は sh・bash・zsh の起動ファイルに足す 1 行。PATH に既にあれば足さない形にして、起動ファイルが
	// 何度読まれても（入れ子のシェル）PATH に重ならないようにする。
	PosixLine = `case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) export PATH="$HOME/.local/bin:$PATH" ;; esac ` + Marker
	// FishLine は fish の conf.d に置く 1 行。
	FishLine = `contains -- "$HOME/.local/bin" $PATH; or set -gx PATH "$HOME/.local/bin" $PATH ` + Marker

	// WinEnvKey は利用者の環境変数のレジストリのキーを開く式（テストは別のキーに差し替える）。
	WinEnvKey = `[Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')`
	// WinBroadcast は環境変数が変わったことを開いているアプリ（エクスプローラ）に知らせる文（テストは外す）。
	// SetEnvironmentVariable の User は書いた後に WM_SETTINGCHANGE を送るので、使い捨ての変数を置いて消す。
	WinBroadcast = `[Environment]::SetEnvironmentVariable('LOOPTRACK_PATH_REFRESH', '1', 'User'); ` +
		`[Environment]::SetEnvironmentVariable('LOOPTRACK_PATH_REFRESH', $null, 'User')`
)

// PosixBody は sh の断片。変数 D に置き場（$HOME/.local/bin）が入っている前提で、包みの中に置く。
// 変数 L・W と関数 lt_rc を使う（包みの外には残らない）。書けなかったときは理由を出して成功で終える
// （hook は init が絶対パスで配線するので、PATH を足せなくても導入は続ける。doctor と要約が知らせる）。
func PosixBody(lang i18n.Lang) string {
	// 変数は ${…} で囲む（macOS の bash 3.2 は UTF-8 のロケールで、変数名に続く全角の文字を名前の一部として読む）
	added := i18n.T(lang, "setuppath.added", "dir", "${D}", "files", "${W}")
	failed := i18n.T(lang, "setuppath.failed", "dir", "${D}")
	// grep は command で呼ぶ（AI のシェルが grep を関数で覆っていることがある）
	return fmt.Sprintf(`case ":$PATH:" in *":$D:"*|*":$D/:"*) ;; *) L='%s'; W=''; `+
		`lt_rc() { { [ -f "$1" ] && command grep -qxF "$L" "$1"; } || { mkdir -p "$(dirname "$1")" && printf '\n%%s\n' "$L" >> "$1"; } || return; W="${W:+$W }$1"; }; `+
		`case "${SHELL##*/}" in `+
		`zsh) lt_rc "$HOME/.zshenv" && if [ -n "${ZDOTDIR:-}" ] && [ "${ZDOTDIR%%/}" != "${HOME%%/}" ]; then lt_rc "$ZDOTDIR/.zshenv"; fi;; `+
		`bash) lt_rc "$HOME/.bashrc" && if [ -f "$HOME/.bash_profile" ]; then lt_rc "$HOME/.bash_profile"; `+
		`elif [ -f "$HOME/.bash_login" ]; then lt_rc "$HOME/.bash_login"; else lt_rc "$HOME/.profile"; fi;; `+
		`fish) L='%s'; lt_rc "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/looptrack.fish";; `+
		`*) lt_rc "$HOME/.profile";; esac && echo "%s" >&2 || echo "%s" >&2;; esac`,
		PosixLine, FishLine, added, failed)
}

// WinBody は PowerShell の断片。変数 $D に置き場が入っている前提で、& { } の中に置く。
// $R・$O・$H・$K を使う（包みの外には残らない）。書けなかったときは警告を出して続ける（PosixBody と同じ理由）。
func WinBody(lang i18n.Lang) string {
	added := i18n.T(lang, "setuppath.added_windows", "dir", "${D}")
	failed := i18n.T(lang, "setuppath.failed", "dir", "${D}")
	// 項目は %…% を展開し、末尾の \ を外し、大小を区別せずに比べる（-contains は大小を区別しない）
	return fmt.Sprintf(`try { $R = %s; $O = [string]$R.GetValue('Path', '', 'DoNotExpandEnvironmentNames'); `+
		`$H = { param($v) @(($v -split ';') | ForEach-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') }) -contains $D.TrimEnd('\') }; `+
		`if (-not (& $H $env:Path)) { if (-not (& $H $O)) { $K = if ($O) { $R.GetValueKind('Path') } else { 'ExpandString' }; `+
		`$R.SetValue('Path', ((@($O.TrimEnd(';'), $D) | Where-Object { $_ }) -join ';'), $K); %s }; Write-Host "%s" }; $R.Close() } `+
		`catch { Write-Warning "%s: $_" }`,
		WinEnvKey, WinBroadcast, added, failed)
}

// PosixCommand は単独で流す sh のコマンド（doctor が直し方として示す）。サブシェルで包み、呼び出し元に残さない。
func PosixCommand(lang i18n.Lang) string {
	return `(D="` + PosixDir + `" && ` + PosixBody(lang) + `)`
}

// WinCommand は単独で流す PowerShell のコマンド（doctor が直し方として示す）。& { } で包み、呼び出し元に残さない。
func WinCommand(lang i18n.Lang) string {
	return `& { $ErrorActionPreference = 'Stop'; $D = Join-Path $env:LOCALAPPDATA 'Programs\looptrack'; ` + WinBody(lang) + ` }`
}
