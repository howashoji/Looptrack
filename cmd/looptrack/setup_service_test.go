package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
)

// setup --service systemd（deploy/install.sh が systemd を選んだとき）。compose.yaml を書かず、
// .env の LOOPTRACK_DSN は --sqlite-path のまま・LOOPTRACK_LISTEN は 127.0.0.1、最後の案内は systemctl。
func TestSetupServiceSystemd(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "lib", "im.db")
	r := doSetup(context.Background(), t, map[string]string{setupwiz.EnvAdminPassword: setupPW}, "", setupBackend{},
		"--dir", dir, "--yes", "--mode", "team", "--store", "sqlite", "--service", "systemd", "--sqlite-path", db,
		"--port", "8123", "--public-url", "https://im.example.com", "--two-factor", "required")
	if r.code != 0 {
		t.Fatalf("setup: %d\n%s\n%s", r.code, r.stdout, r.errOut)
	}
	envPath := filepath.Join(dir, setupwiz.EnvFile)
	if v := envValue(t, envPath, "LOOPTRACK_DSN"); v != "sqlite:"+db {
		t.Errorf("LOOPTRACK_DSN = %q", v)
	}
	if v := envValue(t, envPath, "LOOPTRACK_LISTEN"); v != "127.0.0.1:8123" {
		t.Errorf("LOOPTRACK_LISTEN = %q", v)
	}
	if _, err := os.Stat(filepath.Join(dir, setupwiz.ComposeFile)); err == nil {
		t.Error("compose.yaml を書いた")
	}
	if !strings.Contains(r.stdout, "systemctl enable --now looptrack") || strings.Contains(r.stdout, "docker compose") {
		t.Errorf("起動の案内:\n%s", r.stdout)
	}
	sq, err := store.Open("sqlite:" + db)
	if err != nil {
		t.Fatal(err)
	}
	defer sq.Close()
	if n, err := store.CountActiveAdmins(context.Background(), sq); err != nil || n != 1 {
		t.Errorf("管理者 = %d %v", n, err)
	}
	if r := doSetup(context.Background(), t, nil, "", setupBackend{}, "--dir", t.TempDir(), "--yes", "--service", "bogus"); r.code == 0 {
		t.Error("不正な --service を受け付けた")
	}
}
