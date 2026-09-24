package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
)

// ローカルモードは 127.0.0.1 / ::1 / localhost 以外の待ち受けで起動しない。
func TestServeConfigLocalModeListen(t *testing.T) {
	key, _ := auth.NewSecretKey()
	t.Setenv("LOOPTRACK_SECRET_KEY", key)
	t.Setenv("LOOPTRACK_DSN", "")    // DB には触れない（触れる前に止まることも確かめる）
	t.Setenv("LOOPTRACK_LANG", "ja") // 文面は実行する機械の言語に依らず固定する
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Setenv("LOOPTRACK_LOCAL_MODE", "1")
	for _, addr := range []string{"0.0.0.0:8090", ":8090", "192.168.1.10:8090", "[::]:8090", "example.com:8090", "127.0.0.2:8090", "127.0.0.1"} {
		t.Setenv("LOOPTRACK_LISTEN", addr)
		// 理由は ID を持つ error なので、利用者に出る文面（i18n.Text）で見る
		if _, _, err := serveConfig(logger); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "LOOPTRACK_LISTEN") {
			t.Errorf("LOOPTRACK_LISTEN=%q: err = %v, want 起動エラー", addr, err)
		}
		if code := serve(); code == 0 {
			t.Errorf("LOOPTRACK_LISTEN=%q: serve が起動した", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8090", "[::1]:8090", "localhost:8090", "LOCALHOST:0"} {
		t.Setenv("LOOPTRACK_LISTEN", addr)
		cfg, got, err := serveConfig(logger)
		if err != nil || got != addr || !cfg.LocalMode {
			t.Errorf("LOOPTRACK_LISTEN=%q: addr=%q local=%v err=%v", addr, got, cfg.LocalMode, err)
		}
		if cfg.CookieSecure || len(cfg.TrustedProxies) != 0 {
			t.Errorf("LOOPTRACK_LISTEN=%q: 既定の Secure=%v プロキシ=%v, want false・なし", addr, cfg.CookieSecure, cfg.TrustedProxies)
		}
	}
	t.Setenv("LOOPTRACK_LISTEN", "")
	if _, got, err := serveConfig(logger); err != nil || got != "127.0.0.1:8090" {
		t.Errorf("既定の待ち受け = %q (%v), want 127.0.0.1:8090", got, err)
	}

	// 通常モードは従来どおり（全アドレスで待ち受け・Secure・既定のプロキシ）
	t.Setenv("LOOPTRACK_LOCAL_MODE", "")
	cfg, got, err := serveConfig(logger)
	if err != nil || got != ":8090" || cfg.LocalMode || !cfg.CookieSecure || len(cfg.TrustedProxies) != 3 {
		t.Errorf("通常モード: addr=%q local=%v secure=%v proxies=%v err=%v", got, cfg.LocalMode, cfg.CookieSecure, cfg.TrustedProxies, err)
	}
	t.Setenv("LOOPTRACK_LISTEN", "0.0.0.0:8090")
	if _, got, err := serveConfig(logger); err != nil || got != "0.0.0.0:8090" {
		t.Errorf("通常モードの 0.0.0.0: %q %v", got, err)
	}
}

// TestServeConfigListenMessageLang は、ローカルモードの待ち受けアドレスの誤りが利用者の言語で出ることを
// 確かめる（回帰: server.CheckLocalListen が日本語のリテラルを返していた）。
func TestServeConfigListenMessageLang(t *testing.T) {
	key, _ := auth.NewSecretKey()
	t.Setenv("LOOPTRACK_SECRET_KEY", key)
	t.Setenv("LOOPTRACK_DSN", "")
	t.Setenv("LOOPTRACK_LOCAL_MODE", "1")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		lang i18n.Lang
		addr string
		want string
	}{
		{i18n.JA, "0.0.0.0:8090", `LOOPTRACK_LISTEN: ローカルモードは 127.0.0.1・::1・localhost でだけ待ち受けます（指定 "0.0.0.0:8090"）`},
		{i18n.EN, "0.0.0.0:8090", `LOOPTRACK_LISTEN: Local mode only listens on 127.0.0.1, ::1 and localhost (given "0.0.0.0:8090")`},
		{i18n.JA, "127.0.0.1", `LOOPTRACK_LISTEN: ローカルモードの待ち受けアドレス "127.0.0.1" が不正です（例 127.0.0.1:8090）: address 127.0.0.1: missing port in address`},
		{i18n.EN, "127.0.0.1", `LOOPTRACK_LISTEN: The local-mode listen address "127.0.0.1" is not valid (for example 127.0.0.1:8090): address 127.0.0.1: missing port in address`},
	} {
		t.Setenv("LOOPTRACK_LANG", string(c.lang))
		t.Setenv("LOOPTRACK_LISTEN", c.addr)
		_, _, err := serveConfig(logger)
		if err == nil {
			t.Fatalf("%s %q: エラーにならない", c.lang, c.addr)
		}
		if got := i18n.Text(c.lang, err); got != c.want {
			t.Errorf("%s %q:\n got  %q\n want %q", c.lang, c.addr, got, c.want)
		}
	}
}

// TOTP の発行者名（認証アプリの表示）は LOOPTRACK_TOTP_ISSUER で以前の名前に戻せる（空なら server の既定 Looptrack）。
func TestServeConfigTOTPIssuer(t *testing.T) {
	key, _ := auth.NewSecretKey()
	t.Setenv("LOOPTRACK_SECRET_KEY", key)
	t.Setenv("LOOPTRACK_LOCAL_MODE", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for in, want := range map[string]string{"": "", "  ": "", "old-name": "old-name"} {
		t.Setenv("LOOPTRACK_TOTP_ISSUER", in)
		if cfg, _, err := serveConfig(logger); err != nil || cfg.Issuer != want {
			t.Errorf("LOOPTRACK_TOTP_ISSUER=%q: Issuer %q (%v), want %q", in, cfg.Issuer, err, want)
		}
	}
}
