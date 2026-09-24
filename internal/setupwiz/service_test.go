package setupwiz

import (
	"github.com/howashoji/looptrack/internal/i18n"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// チームのサーバの起動の方法（--service）。systemd は compose.yaml を書かず、127.0.0.1 で待ち受け、
// SQLite は --sqlite-path をそのまま .env に書く。none は起動の案内を出さない。対話の問いは増やさない。
func TestService(t *testing.T) {
	base := Preset{Mode: "team", Store: "sqlite", PublicURL: "https://im.example.com", AdminPassword: pw, TwoFactor: "required"}
	for _, svc := range []string{ServiceSystemd, ServiceNone} {
		t.Run(svc, func(t *testing.T) {
			dir := t.TempDir()
			db := filepath.Join(t.TempDir(), "var", "lib", "im", "im.db")
			pre := base
			pre.Service, pre.SQLitePath = svc, db
			fb := &fakeBackend{}
			_, out, err := run(t, Options{Dir: dir, Yes: true, Backend: fb, Preset: pre})
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			env := envOf(t, dir)
			if env["LOOPTRACK_DSN"] != "sqlite:"+db || env["LOOPTRACK_LISTEN"] != "127.0.0.1:8090" || env["LOOPTRACK_COOKIE_SECURE"] != "true" {
				t.Errorf("env = %v", env)
			}
			if _, ok := env["LOOPTRACK_LOCAL_MODE"]; ok {
				t.Error("LOOPTRACK_LOCAL_MODE が付いた")
			}
			mustNotExist(t, filepath.Join(dir, ComposeFile))
			if fb.dsns[0] != "sqlite:"+db {
				t.Errorf("setup の接続先 = %v", fb.dsns)
			}
			if strings.Contains(out, "docker compose") {
				t.Errorf("docker compose を案内した:\n%s", out)
			}
			hasStart := strings.Contains(out, "■ 起動")
			if svc == ServiceSystemd && (!hasStart || !strings.Contains(out, "  systemctl enable --now looptrack\n")) {
				t.Errorf("systemd の起動の案内が無い:\n%s", out)
			}
			if svc == ServiceNone && hasStart {
				t.Errorf("none で起動の案内を出した:\n%s", out)
			}
		})
	}
	t.Run("既定は compose（従来どおり）", func(t *testing.T) {
		dir := t.TempDir()
		_, out, err := run(t, Options{Dir: dir, Yes: true, Backend: &fakeBackend{}, Preset: base})
		if err != nil {
			t.Fatal(err)
		}
		if env := envOf(t, dir); env["LOOPTRACK_DSN"] != "sqlite:/data/im.db" || env["LOOPTRACK_LISTEN"] != ":8090" {
			t.Errorf("env = %v", env)
		}
		if _, err := os.Stat(filepath.Join(dir, ComposeFile)); err != nil || !strings.Contains(out, "docker compose up -d") {
			t.Errorf("compose: %v\n%s", err, out)
		}
	})
	t.Run("対話の問いは増えない", func(t *testing.T) {
		dir := t.TempDir()
		fb := &fakeBackend{}
		_, out, err := run(t, Options{Dir: dir, In: teamAnswers(), Backend: fb, Preset: Preset{Service: ServiceSystemd}})
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		mustNotExist(t, filepath.Join(dir, ComposeFile))
		if env := envOf(t, dir); env["LOOPTRACK_LISTEN"] != "127.0.0.1:9000" || !strings.Contains(out, "起動の方法: systemd") {
			t.Errorf("env = %v\n%s", env, out)
		}
	})
	for want, pre := range map[string]Preset{
		"チームのサーバ（--mode team）のときだけ": {Service: ServiceSystemd, AdminPassword: pw, TwoFactor: "optional"},
		"compose・systemd・none":      {Mode: "team", Store: "sqlite", PublicURL: "https://x.example", Service: "launchd", AdminPassword: pw, TwoFactor: "optional"},
	} {
		dir := filepath.Join(t.TempDir(), "out")
		_, _, err := run(t, Options{Dir: dir, Yes: true, Backend: &fakeBackend{}, Preset: pre})
		if msg := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(msg, want) {
			t.Errorf("%s: err = %v（%s）", want, err, msg)
		}
		mustNotExist(t, dir)
	}
}
