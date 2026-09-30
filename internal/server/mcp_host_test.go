package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// MCP の DNS rebinding の対策（mcpHostAllowed）。
// 127.0.0.1 で待ち受け、リバースプロキシが公開の Host を渡す配置（install.sh の systemd 方式）で、
// 公開の URL の Host の要求が通り、それ以外の Host は 403 のままであることを確かめる。

const mcpToolsListBody = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`

// mcpVia は、待ち受けのローカルアドレスを local（http.LocalAddrContextKey）にし、Host を host にした
// POST <BasePath>/mcp をサーバに直接渡す（net/http のサーバが要求に付ける値を、テストで明示して入れる）。
func mcpVia(t *testing.T, e *env, local net.Addr, host, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/im/mcp", strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, local))
	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, req)
	return rec
}

func TestMCPHostBehindProxy(t *testing.T) {
	loop := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8090}
	e := newEnvWith(t, func(cfg *Config) {
		cfg.AllowNoAdmin = true
		cfg.PublicURL = "https://issues.example.com/"
	})
	e.clock.t = e.clock.t.Truncate(time.Minute).Add(30 * time.Second)
	u := e.user("ed", "ed-password-123", "member")
	token := e.apiAs(u).token

	// 通る側: 公開の URL の host（ポートの有無・大文字小文字を問わない）
	for _, host := range []string{"issues.example.com", "ISSUES.example.com", "issues.example.com:443"} {
		// トークンなしは 401（403 ではない）で、OAuth の入口の案内が付く
		if rec := mcpVia(t, e, loop, host, "", mcpInitBody); rec.Code != http.StatusUnauthorized ||
			!strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("Host %q・トークンなし: %d %q, want 401: %s", host, rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body)
		}
		// トークン付きは tools/list に 200 で答える
		rec := mcpVia(t, e, loop, host, token, mcpToolsListBody)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"create_project"`) {
			t.Errorf("Host %q・トークン付きの tools/list: %d, want 200 とツールの一覧: %s", host, rec.Code, rec.Body)
		}
	}
	// 通る側: ループバックの Host（SDK の既定と同じ範囲）
	for _, host := range []string{"127.0.0.1:8090", "localhost:8090", "[::1]:8090", "127.0.0.2", "localhost"} {
		if rec := mcpVia(t, e, loop, host, token, mcpToolsListBody); rec.Code != http.StatusOK {
			t.Errorf("Host %q（ループバック）: %d, want 200: %s", host, rec.Code, rec.Body)
		}
	}

	// 止まる側（対照）: 公開の URL でもループバックでもない Host は、トークンの有無に関わらず 403
	for _, host := range []string{"evil.example", "evil.example:443", "issues.example.com.evil.example", "example.com"} {
		for _, tok := range []string{"", token} {
			rec := mcpVia(t, e, loop, host, tok, mcpToolsListBody)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "invalid Host header") {
				t.Errorf("Host %q（トークン %v）: %d, want 403: %s", host, tok != "", rec.Code, rec.Body)
			}
		}
	}
	// X-Forwarded-Host は判定に使わない（偽装できる）
	req := httptest.NewRequest("POST", "/im/mcp", strings.NewReader(mcpToolsListBody))
	req.Host = "evil.example"
	req.Header.Set("X-Forwarded-Host", "issues.example.com")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, loop))
	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("X-Forwarded-Host で公開の host を名乗る: %d, want 403", rec.Code)
	}

	// ループバック以外のアドレスで受けた要求（コンテナの中で受ける配置）は判定しない（SDK の既定と同じ）
	if rec := mcpVia(t, e, &net.TCPAddr{IP: net.ParseIP("172.18.0.5"), Port: 8090}, "evil.example", token, mcpToolsListBody); rec.Code != http.StatusOK {
		t.Errorf("ループバック以外で受けた要求: %d, want 200: %s", rec.Code, rec.Body)
	}
}

// 公開の URL が無い（デスクトップ版の形）ときは、SDK の既定と同じくループバックの Host だけを通す。
func TestMCPHostWithoutPublicURL(t *testing.T) {
	loop := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8090}
	e, _, ed := newAPIEnv(t)
	if e.s.cfg.PublicURL != "" {
		t.Fatalf("前提が崩れています: 公開の URL が設定されている（%q）", e.s.cfg.PublicURL)
	}
	// 通る側（対照）: ループバックの Host
	if rec := mcpVia(t, e, loop, "127.0.0.1:8090", ed.token, mcpToolsListBody); rec.Code != http.StatusOK {
		t.Errorf("Host 127.0.0.1:8090: %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := mcpVia(t, e, loop, "localhost:8090", "", mcpInitBody); rec.Code != http.StatusUnauthorized {
		t.Errorf("Host localhost:8090・トークンなし: %d, want 401: %s", rec.Code, rec.Body)
	}
	// 止まる側: ループバックでない Host は 403
	for _, host := range []string{"issues.example.com", "evil.example:8090"} {
		for _, tok := range []string{"", ed.token} {
			if rec := mcpVia(t, e, loop, host, tok, mcpToolsListBody); rec.Code != http.StatusForbidden {
				t.Errorf("Host %q（トークン %v）: %d, want 403: %s", host, tok != "", rec.Code, rec.Body)
			}
		}
	}
}
