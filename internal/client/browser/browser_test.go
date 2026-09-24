package browser

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

const u = "https://example.invalid/im/oauth/authorize?a=1&b=2"

func argvs(cs []Command) [][]string {
	var out [][]string
	for _, c := range cs {
		out = append(out, c.Argv)
	}
	return out
}

func TestPlanPerOS(t *testing.T) {
	none := env.FromMap(nil)
	for _, c := range []struct {
		goos string
		e    env.Env
		want [][]string
	}{
		{"darwin", none, [][]string{{"open", u}}},
		{"windows", none, [][]string{{"rundll32", "url.dll,FileProtocolHandler", u}}},
		// 表示の無い Linux は開かない（URL の表示だけで続ける）
		{"linux", none, nil},
		{"linux", env.FromMap(map[string]string{"DISPLAY": ":0"}),
			[][]string{{"xdg-open", u}, {"wslview", u}, {"sensible-browser", u}, {"x-www-browser", u}}},
		{"linux", env.FromMap(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}),
			[][]string{{"xdg-open", u}, {"wslview", u}, {"sensible-browser", u}, {"x-www-browser", u}}},
	} {
		if got := argvs(Plan(c.e, c.goos, u)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v", c.goos, got)
		}
	}
}

func TestPlanBrowserEnv(t *testing.T) {
	e := env.FromMap(map[string]string{"BROWSER": "/opt/my browser:firefox --new-tab %s::"})
	got := Plan(e, "darwin", u)
	want := [][]string{{"/opt/my browser", u}, {"firefox", "--new-tab", u}, {"open", u}}
	if !reflect.DeepEqual(argvs(got), want) || !got[0].Wait || !got[1].Wait || got[2].Wait {
		t.Errorf("BROWSER: %+v", got)
	}
	// Windows の区切りは ;（C:\… の : で分けない）
	got = Plan(env.FromMap(map[string]string{"BROWSER": `C:\b\x.exe;y`}), "windows", u)
	if want := [][]string{{`C:\b\x.exe`, u}, {"y", u}, {"rundll32", "url.dll,FileProtocolHandler", u}}; !reflect.DeepEqual(argvs(got), want) {
		t.Errorf("Windows の BROWSER: %v", argvs(got))
	}
}

// TestOpenRunsBrowserEnv: BROWSER のコマンドに URL を 1 つの引数で渡し（シェルを通さない）、終了を待つ。
// 失敗したら次（OS の既定）へ進む。
func TestOpenRunsBrowserEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ブラウザの代わりはシェルスクリプトのため Windows では省略（Plan は TestPlanBrowserEnv で確かめる）")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "opened")
	ok := filepath.Join(dir, "ok.sh")
	os.WriteFile(ok, []byte("#!/bin/sh\nprintf '%s' \"$1\" > '"+out+"'\n"), 0o755)
	bad := filepath.Join(dir, "bad.sh")
	os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	e := env.FromMap(map[string]string{"BROWSER": bad + ":" + ok})
	if !Open(e, u) {
		t.Fatal("開けなかった")
	}
	if b, _ := os.ReadFile(out); string(b) != u {
		t.Errorf("渡した URL: %q", b)
	}
	// 無いコマンドは飛ばす（OS の既定まで進むと本物のブラウザが開くので、run だけを確かめる）
	if run(Command{Argv: []string{filepath.Join(dir, "missing"), u}, Wait: true}) {
		t.Error("無いコマンドで開けたことにした")
	}
}
