package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
)

// unsetForTest は key を未設定にし、テストの後に元へ戻す。
func unsetForTest(t *testing.T, key string) {
	t.Setenv(key, "")
	os.Unsetenv(key)
}

// serve --env-file は .env を読み、既に決まっている環境変数を優先する。
func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("# looptrack setup が書く\n\nLOOPTRACK_T_A=plain\nexport LOOPTRACK_T_B=\"quoted value\"\nLOOPTRACK_T_C='single'\nLOOPTRACK_T_D=from-file\nLOOPTRACK_T_E=a=b\n"), 0o600)
	for _, k := range []string{"LOOPTRACK_T_A", "LOOPTRACK_T_B", "LOOPTRACK_T_C", "LOOPTRACK_T_E"} {
		unsetForTest(t, k)
	}
	t.Setenv("LOOPTRACK_T_D", "from-env")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"LOOPTRACK_T_A": "plain", "LOOPTRACK_T_B": "quoted value", "LOOPTRACK_T_C": "single", "LOOPTRACK_T_D": "from-env", "LOOPTRACK_T_E": "a=b"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	bad := filepath.Join(dir, "bad.env")
	os.WriteFile(bad, []byte("NOEQUALS\n"), 0o600)
	if err := loadEnvFile(bad); err == nil {
		t.Error("KEY=VALUE でない行を受け付けた")
	}
	if err := loadEnvFile(filepath.Join(dir, "missing.env")); err == nil {
		t.Error("無いファイルでエラーにならない")
	}
}

func TestServeEnvFileLocalMode(t *testing.T) {
	key, _ := auth.NewSecretKey()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("LOOPTRACK_LOCAL_MODE=1\nLOOPTRACK_LISTEN=0.0.0.0:18090\nLOOPTRACK_COOKIE_SECURE=false\nLOOPTRACK_SECRET_KEY="+key+"\n"), 0o600)
	for _, k := range []string{"LOOPTRACK_LOCAL_MODE", "LOOPTRACK_LISTEN", "LOOPTRACK_COOKIE_SECURE", "LOOPTRACK_SECRET_KEY", "LOOPTRACK_DSN"} {
		unsetForTest(t, k)
	}
	// ファイルの 0.0.0.0 はローカルモードで起動しない
	if code := serveCmd([]string{"--env-file", path}); code == 0 {
		t.Fatal("0.0.0.0 で起動した")
	}
	// 環境変数が先に決まっていればそちら（127.0.0.1）を使う
	t.Setenv("LOOPTRACK_LISTEN", "127.0.0.1:18090")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	cfg, addr, err := serveConfig(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || addr != "127.0.0.1:18090" || !cfg.LocalMode || cfg.CookieSecure {
		t.Errorf("addr=%q local=%v secure=%v err=%v", addr, cfg.LocalMode, cfg.CookieSecure, err)
	}
	if code := serveCmd([]string{"extra"}); code == 0 {
		t.Error("余分な引数を受け付けた")
	}
}
