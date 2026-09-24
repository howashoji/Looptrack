package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	clientenv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// TestHostSessionKindTable は、器のセッション ID（CLAUDE_CODE_HOST_SESSION_ID）だけが渡る窓の判定を、
// 送る側（CLI の session.Detect）と受け取る側（サーバの actor・service.UsageTarget）と
// トークン情報の収集（usagesnap.Detect）の 3 つまとめて表で固定する。
//
// 送る側と受け取る側を別々のテストに分けると、片方の前提だけで期待値が書き換わる（kit/loop の rules の
// working-discipline.md「送る側と受け取る側を別のセッションが担当すると、片方の前提だけで期待値が書き換わる」）。
// ここでは 1 つの表から両方を判定する。
//
//	環境変数                        | (a) X-Looptrack-Session / -Session-Kind | (b) 付与の対象 | (c) 会話記録
//	CLAUDE_CODE_SESSION_ID だけ     | cc-1 / （無し）                         | 対象           | 引ける
//	CLAUDE_CODE_HOST_SESSION_ID だけ| host-1 / host                           | 対象外         | 引けない  ← 本件
//	両方                            | cc-1 / （無し）                         | 対象           | 引ける
//	どちらも無し                    | （無し）/（無し）                       | 対象外         | 引けない
func TestHostSessionKindTable(t *testing.T) {
	home := t.TempDir()
	// Claude Code の会話記録は ~/.claude/projects/<slug>/<セッション ID>.jsonl（usagesnap.claudeFindTranscript）。
	// CLAUDE_CODE_SESSION_ID の値のファイルだけを置く（器の ID のファイルは存在しない、が本件の前提）。
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cc-1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		env  map[string]string
		// (a) CLI が送るヘッダ
		wantSession string
		wantKind    string
		// (b) サーバが付与の対象とするか（service.UsageTarget）
		wantTarget bool
		// (c) usagesnap が会話記録を引けるか
		wantTranscript bool
	}{
		{"① CLAUDE_CODE_SESSION_ID のみ", map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-1"},
			"cc-1", "", true, true},
		{"② CLAUDE_CODE_HOST_SESSION_ID のみ", map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "host-1"},
			"host-1", session.KindHost, false, false},
		{"③ 両方", map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-1", "CLAUDE_CODE_HOST_SESSION_ID": "host-1"},
			"cc-1", "", true, true},
		{"④ どちらも無し", map[string]string{}, "", "", false, false},
	}
	svc := &service.Service{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := clientenv.FromMap(c.env)
			// (a) CLI が送るヘッダ
			h := session.Detect(e).Headers()
			if h["X-Looptrack-Session"] != c.wantSession {
				t.Errorf("(a) X-Looptrack-Session = %q（期待 %q）", h["X-Looptrack-Session"], c.wantSession)
			}
			if h["X-Looptrack-Session-Kind"] != c.wantKind {
				t.Errorf("(a) X-Looptrack-Session-Kind = %q（期待 %q）", h["X-Looptrack-Session-Kind"], c.wantKind)
			}
			// (b) サーバがそのヘッダをどう読むか（実物の actor を通す）
			r := httptest.NewRequest("POST", "/im/api/v1/issues", nil)
			r.Header.Set("X-Looptrack-Client", "cli")
			for k, v := range h {
				r.Header.Set(k, v)
			}
			r = r.WithContext(withPrincipal(r.Context(), &principal{User: store.User{ID: 1}, Via: "api"}))
			a := actor(r)
			if a.Via != "cli" {
				t.Fatalf("via = %q（期待 cli）", a.Via)
			}
			// 計測が任意の AI ではないので、DB を見ずに判定が決まる（q は nil でよい）
			got, err := svc.UsageTarget(context.Background(), nil, a, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.wantTarget {
				t.Errorf("(b) UsageTarget = %v（期待 %v・session=%q kind=%q）", got, c.wantTarget, a.SessionID, a.SessionKind)
			}
			// (c) トークン情報の収集が会話記録を引けるか
			client, path, _ := usagesnap.Detect("", usagesnap.Options{Env: e, Home: home})
			if found := client != "" && path != ""; found != c.wantTranscript {
				t.Errorf("(c) usagesnap.Detect = (%q, %q)（期待 引ける=%v）", client, path, c.wantTranscript)
			}
		})
	}
}
