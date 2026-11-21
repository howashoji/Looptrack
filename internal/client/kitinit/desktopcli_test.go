package kitinit

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TestDesktopBundled: Windows のデスクトップ版に同梱の CLI（<アプリ>\cli\looptrack.exe。cli の親に Looptrack.exe）だけを同梱と見る。
// OS とパスは注入する（手元の OS に依らない）。対照として、写し先（Programs\looptrack）・Looptrack.exe の無い cli・Windows 以外は同梱と見ない。
func TestDesktopBundled(t *testing.T) {
	app := filepath.Join("C", "Users", "u", "AppData", "Local", "Programs", "Looptrack Desktop")
	exists := func(p string) bool { return p == filepath.Join(app, "Looptrack.exe") }
	bundled := filepath.Join(app, "cli", "looptrack.exe")
	if !desktopBundled("windows", bundled, exists) {
		t.Errorf("同梱の CLI を見分けない: %s", bundled)
	}
	if !desktopBundled("windows", filepath.Join(app, "CLI", "LOOPTRACK.EXE"), exists) {
		t.Errorf("大小の違う同梱の CLI を見分けない")
	}
	for _, c := range []struct{ goos, exe string }{
		{"windows", filepath.Join("C", "Users", "u", "AppData", "Local", "Programs", "looptrack", "looptrack.exe")},
		{"windows", filepath.Join("C", "elsewhere", "cli", "looptrack.exe")},
		{"darwin", bundled},
		{"windows", ""},
	} {
		if desktopBundled(c.goos, c.exe, exists) {
			t.Errorf("同梱でないものを同梱と見た: %+v", c)
		}
	}
}

// TestDesktopPathHints: 同梱の CLI のときの init と doctor の PATH の案内は、トレイメニューの文言（対訳表の desktop.menu.install_cli）と
// 写し先（%LOCALAPPDATA%\Programs\looptrack）を示す。対照として、同梱でないときは従来の案内（メニューを書かない）。
func TestDesktopPathHints(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		menu := i18n.T(lang, "desktop.menu.install_cli")
		exe := `C:\x\Looptrack Desktop\cli\looptrack.exe`
		for name, got := range map[string]string{
			"init":   wiredAbsNote(lang, exe, true),
			"doctor": pathMissingText(lang, "windows", "copy", "redo", true),
			"server": pathMissingText(lang, "windows", "server", "redo", true),
		} {
			if !strings.Contains(got, menu) || !strings.Contains(got, `%LOCALAPPDATA%\Programs\looptrack`) {
				t.Errorf("%s %s: メニューと写し先を示していない: %s", lang, name, got)
			}
		}
		if got := wiredAbsNote(lang, exe, false); strings.Contains(got, menu) {
			t.Errorf("%s: 同梱でないのにメニューを案内した: %s", lang, got)
		}
		if got := pathMissingText(lang, "windows", "copy", "redo", false); strings.Contains(got, menu) {
			t.Errorf("%s: 同梱でないのにメニューを案内した（doctor）: %s", lang, got)
		}
	}
}

// TestStaleEmbeddedKit: 埋め込みの kit で入れた導入（copy）の控えの bundle_sha256 が実行中の looptrack の kit と違えば、違う層を返す。
// 対照として、同じ・サーバから入れた導入（server）・kit の正本・loop を入れていない導入では返さない。
func TestStaleEmbeddedKit(t *testing.T) {
	files := map[string]string{"kit/core/a.md": "a", "kit/loop/manifest.json": "{}", "kit/loop/b.md": "b"}
	coreSum := bundleSHA(map[string]string{"kit/core/a.md": sha("a")})
	loopSum := bundleSHA(map[string]string{"kit/loop/manifest.json": sha("{}"), "kit/loop/b.md": sha("b")})
	kj := func(source, core, loop string, loopOn bool) *jsonorder.Object {
		return jsonorder.NewObject().Set("source", source).
			Set("core", jsonorder.NewObject().Set("bundle_sha256", core)).
			Set("loop", jsonorder.NewObject().Set("installed", loopOn).Set("bundle_sha256", loop))
	}
	cases := []struct {
		name string
		data *jsonorder.Object
		self bool
		want string
	}{
		{"同じ", kj("copy", coreSum, loopSum, true), false, ""},
		{"core が古い", kj("copy", "old", loopSum, true), false, "core"},
		{"loop が古い", kj("copy", coreSum, "old", true), false, "loop"},
		{"両方", kj("copy", "old", "old", true), false, "core・loop"},
		{"loop を入れていない", kj("copy", coreSum, "old", false), false, ""},
		{"サーバから入れた", kj("server", "old", "old", true), false, ""},
		{"kit の正本", kj("copy", "old", "old", true), true, ""},
	}
	for _, c := range cases {
		if got := staleEmbeddedKit(c.data, files, c.self); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDoctorStaleKit: doctor は埋め込みの kit と導入済みの控えが違えば init のやり直しを促す（注意）。同じなら促さない。
func TestDoctorStaleKit(t *testing.T) {
	stubs(t, true)
	executable = func() (string, error) { return fakeBin, nil }
	core := map[string]string{}
	for n, b := range embedded() {
		if strings.HasPrefix(n, "kit/core/") {
			core[n] = sha(b)
		}
	}
	settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "looptrack hook summary --agent claude-code", "timeout": 5}]}]}}`
	for _, c := range []struct {
		sum  string
		want bool
	}{{"0000", true}, {bundleSHA(core), false}} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".claude", "settings.json"), settings)
		writeFile(t, filepath.Join(dir, ".claude", ".looptrack-kit.json"), `{"project": "demo", "source": "copy", "core": {"bundle_sha256": "`+c.sum+`"}, "loop": {"installed": false}}`)
		_, out := doctor(t, dir, nil)
		got := strings.Contains(out, "埋め込まれた kit と違います") && strings.Contains(out, "issue init")
		if got != c.want {
			t.Errorf("bundle_sha256=%s: 促したか %v（%v のはず）:\n%s", c.sum, got, c.want, out)
		}
	}
}
