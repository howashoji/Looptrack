// Package cred は API のアクセストークン（資格情報）のファイルを扱う（DESIGN.md §5-11）。
//
// 置き場: Windows は %APPDATA%\looptrack\credentials.json、他は $XDG_CONFIG_HOME/looptrack/credentials.json
// （XDG_CONFIG_HOME が無ければ ~/.config）。
//
// 中身はサーバの URL → {token, refresh_token, client_id, expires_at, login, …} の JSON（以前の CLI と同じ形）。キーの順と
// 知らない項目は保つ（jsonorder.Object）。書き方は indent=2・末尾の改行なし・一時ファイルからの置き換え。
//
// 以前の CLI（1.0.0 より前）の置き場は読まない（その CLI は撤去済み。並行していた間は looptrack がこちらにも書いていた）。
//
// 保護: POSIX は 0600（ディレクトリは 0700）で書き、読むときに group・other の権限があれば拒否する。
// Windows は DACL を本人だけにして継承を切り（ディレクトリも）、読むときに本人・SYSTEM・Administrators 以外に読み取りを
// 許す ACE があれば拒否する（ACL の作り方と確かめ方は internal/privfile と共通）。
// ロックは POSIX は flock、Windows は LockFileEx（*.lock のファイル）。
package cred

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/privfile"
)

// FileName は資格情報のファイル名。
const FileName = "credentials.json"

// AppDir は置き場のディレクトリ名。
const AppDir = "looptrack"

// ReloginCommand は壊れた・権限の広いファイルの案内に出すログインのコマンド。
var ReloginCommand = "looptrack issue login --browser"

// Paths は資格情報のファイルの置き場。
type Paths struct {
	Primary string // 置き場（読み書きする）
}

// DefaultPaths は環境変数と OS から置き場を決める。
func DefaultPaths(e env.Env) (Paths, error) { return pathsFor(e, runtime.GOOS) }

func pathsFor(e env.Env, goos string) (Paths, error) {
	join := filepath.Join
	if goos == "windows" {
		join = func(elem ...string) string { return joinWindows(elem...) }
	}
	home := e.Get("HOME")
	if goos == "windows" {
		home = e.Get("USERPROFILE")
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, i18n.Wrapf(err, "cred.err.no_home")
		}
		home = h
	}
	var p Paths
	if goos == "windows" {
		appdata := e.Get("APPDATA")
		if appdata == "" {
			appdata = join(home, "AppData", "Roaming")
		}
		p.Primary = join(appdata, AppDir, FileName)
	} else {
		cfg := e.Get("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = join(home, ".config")
		}
		p.Primary = join(cfg, AppDir, FileName)
	}
	return p, nil
}

// joinWindows は Windows のパスの連結（他の OS から Windows の置き場を試すテスト用。区切りは \）。
func joinWindows(elem ...string) string {
	out := ""
	for _, e := range elem {
		if e == "" {
			continue
		}
		if out == "" {
			out = e
			continue
		}
		if out[len(out)-1] == '\\' || out[len(out)-1] == '/' {
			out += e
		} else {
			out += `\` + e
		}
	}
	return out
}

// PermError は資格情報のファイルを他の利用者が読める。
type PermError struct{ Path string }

// Error はログと、i18n を通していない表示のための日本語（対訳表の正本）。利用者の言語の文面は
// i18n.Text が Unwrap の ID から作る。
func (e *PermError) Error() string { return i18n.Text(i18n.JA, e.Unwrap()) }

// Unwrap は ID を持つ文面を返す（i18n.Text が errors.As でこれを見つけて訳す）。
func (e *PermError) Unwrap() error {
	if runtime.GOOS == "windows" {
		return i18n.Errorf("cred.err.too_open_windows", "path", e.Path, "command", ReloginCommand)
	}
	return i18n.Errorf("cred.err.too_open", "path", e.Path)
}

// BrokenError は資格情報のファイルが JSON のオブジェクトとして読めない。
type BrokenError struct{ Path string }

func (e *BrokenError) Error() string { return i18n.Text(i18n.JA, e.Unwrap()) }

// Unwrap は ID を持つ文面を返す（PermError と同じ）。
func (e *BrokenError) Unwrap() error {
	return i18n.Errorf("cred.err.broken", "path", e.Path, "command", ReloginCommand)
}

// Store は資格情報のファイル。
type Store struct {
	Paths Paths
}

// Open は既定の置き場の Store。
func Open(e env.Env) (*Store, error) {
	p, err := DefaultPaths(e)
	if err != nil {
		return nil, err
	}
	return &Store{Paths: p}, nil
}

// Path は表示に使う置き場。
func (s *Store) Path() string { return s.Paths.Primary }

func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// readFile は 1 つのファイルを読む（無ければ空）。権限を先に確かめる。
func readFile(path string) (*jsonorder.Object, error) {
	if !exists(path) {
		return jsonorder.NewObject(), nil
	}
	if err := checkPrivate(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	o, err := jsonorder.DecodeObject(b)
	if err != nil {
		return nil, &BrokenError{path}
	}
	return o, nil
}

// Load は資格情報の全体を読む。
func (s *Store) Load() (*jsonorder.Object, error) {
	return readFile(s.Paths.Primary)
}

// Entry は URL の項目（無い・オブジェクトでなければ空のオブジェクト。複製を返す）。
func (s *Store) Entry(url string) (*jsonorder.Object, error) {
	all, err := s.Load()
	if err != nil {
		return nil, err
	}
	if e := all.Object(url); e != nil {
		return e.Clone(), nil
	}
	return jsonorder.NewObject(), nil
}

// SaveEntry は URL の項目を書き換える（Lock を取ってから呼ぶ）。
func (s *Store) SaveEntry(url string, entry *jsonorder.Object) error {
	all, err := s.Load()
	if err != nil {
		return err
	}
	all.Set(url, entry)
	return writePrivate(s.Paths.Primary, []byte(jsonorder.Indent(all, 2)))
}

// Lock は資格情報の読み直し〜書き換えを他のプロセス（looptrack の hook と CLI）と直列化する（credentials.json.lock）。
func (s *Store) Lock() (unlock func(), err error) {
	p := s.Paths.Primary + ".lock"
	if err := mkdirPrivate(filepath.Dir(p)); err != nil {
		return nil, err
	}
	u, err := lockFile(p)
	if err != nil {
		return nil, i18n.Wrapf(err, "cred.err.lock", "path", p)
	}
	return u, nil
}

// writePrivate は本人だけが読める形でファイルを置き換える（同じディレクトリの一時ファイルに書いてから rename）。
func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := mkdirPrivate(dir); err != nil {
		return err
	}
	f, err := privfile.CreateTemp(dir, filepath.Base(path)+".tmp*") // 書く前に本人だけにする（POSIX 0600・Windows の DACL）
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// IsNotExist は置き場が無いことによるエラーか。
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// CheckPrivate は path が本人だけ読める形か確かめる（テスト・doctor 用）。
func CheckPrivate(path string) error { return checkPrivate(path) }
