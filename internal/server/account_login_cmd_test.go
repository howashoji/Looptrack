package server

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// TestAccountLoginCommandsUsePlacedLooptrack は、アカウント設定の画面の login の案内（ブラウザでのログインと、発行した
// トークンを貼る login）が、setup が置いた looptrack の絶対パスで macOS・Linux 向けと Windows 向けの両方を示すことを確かめる。
// サーバ版の利用者の端末に PATH の looptrack は無い。サーバは利用者の OS を知らないので両方を並べる。
// 対照: 配布ディレクトリに looptrack が無いときは、置き場が無いので PATH の looptrack の案内になる（同じ組み方の分岐を通ること）。
func TestAccountLoginCommandsUsePlacedLooptrack(t *testing.T) {
	e := newEnv(t)
	e.project("req")
	e.user("alice", "alice-password-1", "member")
	c := e.client()
	e.enroll(c, "alice", "alice-password-1")
	base := e.srv.URL + "/im"
	text := func(page string) string { return html.UnescapeString(htmlTagRe.ReplaceAllString(page, " ")) }
	posix := func(args string) string { return `"$HOME/.local/bin/looptrack" ` + args + " --url " + base }
	win := func(args string) string {
		return `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') ` + args + " --url " + base
	}

	// 対照: 配布ディレクトリが無い → PATH の looptrack（置き場の絶対パスは出ない）
	_, page := e.get(c, "/im/account")
	if s := text(page); !strings.Contains(s, "looptrack issue login --browser --url "+base) || strings.Contains(s, ".local/bin/looptrack") {
		t.Fatalf("前提が崩れています: 配布の無いサーバで PATH の looptrack の案内にならない:\n%s", s)
	}

	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{
		"looptrack_v1.0.0_darwin_arm64": "mac", "looptrack_v1.0.0_linux_amd64": "linux", "looptrack_v1.0.0_windows_amd64.exe": "win",
	})
	_, page = e.get(c, "/im/account")
	s := text(page)
	for _, want := range []string{posix("issue login --browser"), win("issue login --browser"), "# macOS・Linux", "# Windows（PowerShell）"} {
		if !strings.Contains(s, want) {
			t.Errorf("アカウント設定に %q が無い:\n%s", want, s)
		}
	}
	if bad := bareLooptrackRe.FindAllString(s, -1); len(bad) > 0 {
		t.Errorf("パスの付かない looptrack の案内が残っている: %q", bad)
	}

	// 発行した直後のトークンを貼る login も同じ形
	_, body := e.formAt(c, "/im/account", "/im/account/tokens", url.Values{"name": {"MacBook"}, "days": {"90"}})
	s = text(body)
	for _, want := range []string{posix("issue login"), win("issue login")} {
		if !strings.Contains(s, want+"\n") && !strings.Contains(s, want+" ") {
			t.Errorf("トークンを貼る login に %q が無い:\n%s", want, s)
		}
	}
	if bad := bareLooptrackRe.FindAllString(s, -1); len(bad) > 0 {
		t.Errorf("発行の画面にパスの付かない looptrack の案内が残っている: %q", bad)
	}

	// Windows 向けだけを配るサーバでは Windows の置き場の 1 行（見出しなし）
	e.s.cfg.DistDir = t.TempDir()
	writeDist(t, e.s.cfg.DistDir, map[string]string{"looptrack_v1.0.0_windows_amd64.exe": "win"})
	_, page = e.get(c, "/im/account")
	s = text(page)
	if !strings.Contains(s, win("issue login --browser")) || strings.Contains(s, ".local/bin/looptrack") || strings.Contains(s, "# Windows") {
		t.Errorf("Windows 向けだけを配るサーバでは、Windows の置き場の 1 行（見出しなし）になる:\n%s", s)
	}
}
