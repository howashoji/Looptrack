package desktop

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

// AppName は表示名（.app の名前・トレイのツールチップ・データのフォルダ）。公開名は公開の準備で確定する。
const AppName = "Looptrack"

// Paths はデスクトップ版のファイルの置き場。
type Paths struct {
	DataDir string // DB・鍵・状態のファイル（更新しても消さない。アンインストールで消す）
	LogDir  string // ログ
}

// DB は SQLite のファイル。鍵は localserve が隣に <db>.secret-key で作る。
func (p Paths) DB() string { return filepath.Join(p.DataDir, "looptrack.db") }

// Lock は二重起動の防止に使うロックのファイル（中身は使わない。OS のファイルロックだけを見る）。
func (p Paths) Lock() string { return filepath.Join(p.DataDir, "desktop.lock") }

// State は起動中のインスタンスの情報（pid・URL・版）と、次の起動で使うポート。
func (p Paths) State() string { return filepath.Join(p.DataDir, "desktop.json") }

// Log はログのファイル。
func (p Paths) Log() string { return filepath.Join(p.LogDir, "looptrack.log") }

// ResolvePaths は OS ごとの置き場を決める（DESIGN.md §5-14）。
//
//	LOOPTRACK_DATA_DIR があれば、データはそこ・ログはその下の logs（テスト・持ち運び用）
//	macOS   : ~/Library/Application Support/Looptrack・~/Library/Logs/Looptrack
//	Windows : %LOCALAPPDATA%\Looptrack・%LOCALAPPDATA%\Looptrack\logs
//	Linux 等: $XDG_DATA_HOME/looptrack（既定 ~/.local/share/looptrack）・$XDG_STATE_HOME/looptrack（既定 ~/.local/state/looptrack）
//
// goos・home は呼び出し側が渡す（他の OS の分もテストで確かめるため）。パスの組み立ては goos の区切りで行う。
func ResolvePaths(e env.Env, goos, home string) (Paths, error) {
	join := func(elem ...string) string { return joinFor(goos, elem...) }
	if d := e.Get("LOOPTRACK_DATA_DIR"); d != "" {
		return Paths{DataDir: d, LogDir: join(d, "logs")}, nil
	}
	switch goos {
	case "darwin":
		if home == "" {
			return Paths{}, i18n.Errorf("desktop.err.no_home")
		}
		return Paths{
			DataDir: join(home, "Library", "Application Support", AppName),
			LogDir:  join(home, "Library", "Logs", AppName),
		}, nil
	case "windows":
		base := e.Get("LOCALAPPDATA")
		if base == "" {
			if home == "" {
				return Paths{}, i18n.Errorf("desktop.err.no_localappdata")
			}
			base = join(home, "AppData", "Local")
		}
		d := join(base, AppName)
		return Paths{DataDir: d, LogDir: join(d, "logs")}, nil
	}
	data, state := e.Get("XDG_DATA_HOME"), e.Get("XDG_STATE_HOME")
	if (data == "" || state == "") && home == "" {
		return Paths{}, i18n.Errorf("desktop.err.no_home")
	}
	if data == "" {
		data = join(home, ".local", "share")
	}
	if state == "" {
		state = join(home, ".local", "state")
	}
	return Paths{DataDir: join(data, "looptrack"), LogDir: join(state, "looptrack")}, nil
}

// joinFor は goos の区切りでパスをつなぐ（filepath.Join は実行中の OS の区切りなので、他の OS の分を組み立てるときに使う）。
func joinFor(goos string, elem ...string) string {
	if goos == "windows" {
		var parts []string
		for i, e := range elem {
			if e == "" {
				continue
			}
			if i > 0 {
				e = strings.Trim(e, `\/`)
			} else {
				e = strings.TrimRight(e, `\/`)
			}
			parts = append(parts, e)
		}
		return strings.Join(parts, `\`)
	}
	return path.Join(elem...)
}
