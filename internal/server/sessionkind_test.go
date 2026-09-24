package server

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// TestSessionKindHostConstantsAgree は、器のセッション ID の印 "host" を表す 3 つの定数が同じ値であることを固定する。
//
// 定義がパッケージをまたいで 3 か所にある（store は service を参照できないので、同じ値を別に持っている）:
//
//	session.KindHost         送る側（CLI が X-Looptrack-Session-Kind に入れる値）
//	service.SessionKindHost  受け取る側（actor / mcpCallOf が読み、UsageTarget が判定に使う）
//	store.SessionKindHost    DB 側（issue_events.detail の "session_kind"。usageHostSessionCond が SQL に埋め込む）
//
// この 3 つがずれると、付与の対象から外す判定が黙って効かなくなる。送る側と受け取る側（session ↔ service）は
// TestHostSessionKindTable が経路ごと固定しているが、DB 側（store）はこれまで DB の要るテストでしか結ばれていなかった。
// そのため store.SessionKindHost だけを別の値に変えても、DB 不要の検査（go build / go vet / gofmt と DB 不要のパッケージ）は
// すべて緑のままだった（実測）。ここは DB 無しで走るので、その抜けを塞ぐ。
func TestSessionKindHostConstantsAgree(t *testing.T) {
	consts := []struct {
		name  string
		value string
	}{
		{"session.KindHost", session.KindHost},
		{"service.SessionKindHost", service.SessionKindHost},
		{"store.SessionKindHost", store.SessionKindHost},
	}
	// 値そのものも固定する（3 つ揃って別の値に変わっても、CLI とサーバの版が違えば判定が食い違うため）。
	const want = "host"
	for _, c := range consts {
		if c.value != want {
			t.Errorf("%s = %q（期待 %q）", c.name, c.value, want)
		}
	}
	for _, c := range consts[1:] {
		if c.value != consts[0].value {
			t.Errorf("%s = %q と %s = %q がずれている", consts[0].name, consts[0].value, c.name, c.value)
		}
	}
}

// mcpReqOf は mcpCallOf に渡す最小の要求（ヘッダと認証済みの利用者だけ）。
func mcpReqOf(h http.Header) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{
		TokenInfo: &auth.TokenInfo{Extra: map[string]any{"principal": &principal{User: store.User{ID: 1}, Via: "api", TokenID: 7}}},
		Header:    h,
	}}
}

// TestMCPSessionKind は、MCP 経路が X-Looptrack-Session-Kind を REST（actor）と同じに読むことを固定する。
//
// 印はセッション ID の種類を表すので、経路（CLI / MCP）では絞らない。ただし接続 ID の代用
// （Mcp-Session-Id → service.MCPSessionPrefix）にはサーバが発行した値が入るので、印は付けない。
func TestMCPSessionKind(t *testing.T) {
	cases := []struct {
		name     string
		header   map[string]string
		wantSID  string
		wantKind string
	}{
		{"器のセッション ID（印あり）",
			map[string]string{"X-Looptrack-Session": "host-1", "X-Looptrack-Session-Kind": session.KindHost}, "host-1", service.SessionKindHost},
		{"大文字小文字は問わない",
			map[string]string{"X-Looptrack-Session": "host-1", "X-Looptrack-Session-Kind": "HOST"}, "host-1", service.SessionKindHost},
		{"会話のセッション ID（印なし）",
			map[string]string{"X-Looptrack-Session": "sess-a"}, "sess-a", ""},
		{"知らない種類は読み捨てる",
			map[string]string{"X-Looptrack-Session": "sess-a", "X-Looptrack-Session-Kind": "something-new"}, "sess-a", ""},
		{"接続 ID の代用には印を付けない",
			map[string]string{"Mcp-Session-Id": "conn-1", "X-Looptrack-Session-Kind": session.KindHost}, service.MCPSessionPrefix + "conn-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range c.header {
				h.Set(k, v)
			}
			call, err := mcpCallOf(mcpReqOf(h))
			if err != nil {
				t.Fatal(err)
			}
			if call.actor.Via != "mcp" {
				t.Fatalf("via = %q（期待 mcp）", call.actor.Via)
			}
			if call.actor.SessionID != c.wantSID {
				t.Errorf("SessionID = %q（期待 %q）", call.actor.SessionID, c.wantSID)
			}
			if call.actor.SessionKind != c.wantKind {
				t.Errorf("SessionKind = %q（期待 %q）", call.actor.SessionKind, c.wantKind)
			}
		})
	}
}

// notQueried は「この判定は DB を見ないで決まる」ことを確かめるための store.Queryer。
// 触られたらその場でテストを落とす（nil を渡すと、触った瞬間に nil ポインタで落ちるだけで、
// どの段が DB に降りたのかが分からない）。
type notQueried struct{ t *testing.T }

func (q notQueried) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	q.t.Fatalf("DB を見ない判定のはずが ExecContext を呼んだ: %s", query)
	return nil, nil
}

func (q notQueried) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	q.t.Fatalf("DB を見ない判定のはずが QueryContext を呼んだ: %s", query)
	return nil, nil
}

func (q notQueried) QueryRowContext(_ context.Context, query string, _ ...any) *sql.Row {
	q.t.Fatalf("DB を見ない判定のはずが QueryRowContext を呼んだ: %s", query)
	return nil
}

// TestUsageTargetSessionKindByVia は、器の印の付いた操作が経路によらず付与の対象から外れることと、
// その判定の順序（入口の経路 → 器の印 → 計測が任意の AI）を固定する。
//
// ここで確かめるのは、どの組み合わせでも DB を見ずに答えが決まることでもある。q には notQueried を渡すので、
// 判定が DB に降りたらその場で落ちる。入口（api / admin / import を弾く条件）や器の印の判定を外すと、
// 計測が任意の AI（Agent: copilot）を混ぜた行が store.HasRecentClientUsage まで進み、notQueried が捕まえる。
func TestUsageTargetSessionKindByVia(t *testing.T) {
	cases := []struct {
		name  string
		actor service.Actor
		want  bool
	}{
		{"cli・会話のセッション ID", service.Actor{Via: "cli", SessionID: "sess-a"}, true},
		{"cli・器のセッション ID", service.Actor{Via: "cli", SessionID: "host-1", SessionKind: service.SessionKindHost}, false},
		{"mcp・印なし", service.Actor{Via: "mcp", SessionID: service.MCPSessionPrefix + "conn-1"}, true},
		{"mcp・器のセッション ID", service.Actor{Via: "mcp", SessionID: "host-1", SessionKind: service.SessionKindHost}, false},
		{"web", service.Actor{Via: "web"}, false},

		// 入口（AI の経路でないものを弾く条件）が器の印の判定より先に働くこと。
		// api / admin / import は AI の操作ではないので、印の有無によらず対象外。
		{"api・器のセッション ID", service.Actor{Via: "api", SessionID: "host-1", SessionKind: service.SessionKindHost}, false},
		{"api・会話のセッション ID", service.Actor{Via: "api", SessionID: "sess-a"}, false},
		{"admin・器のセッション ID", service.Actor{Via: "admin", SessionID: "host-1", SessionKind: service.SessionKindHost}, false},
		{"admin・会話のセッション ID", service.Actor{Via: "admin", SessionID: "sess-a"}, false},
		{"import・器のセッション ID", service.Actor{Via: "import", SessionID: "host-1", SessionKind: service.SessionKindHost}, false},
		{"import・会話のセッション ID", service.Actor{Via: "import", SessionID: "sess-a"}, false},
		// 入口は計測が任意の AI の判定（DB を見る段）よりも先。入口を広げると notQueried が捕まえる。
		{"api・計測が任意の AI", service.Actor{Via: "api", SessionID: "sess-a", Agent: "copilot"}, false},
		// セッション ID の無い CLI は人の操作なので、印が付いていても入口で外れる。
		{"cli・セッション ID なし・器の印", service.Actor{Via: "cli", SessionKind: service.SessionKindHost}, false},

		// 器の印は計測が任意の AI の判定より先。印が付いていれば DB を見るまでもなく対象外。
		{"mcp・器のセッション ID・計測が任意の AI", service.Actor{Via: "mcp", SessionID: "host-1", SessionKind: service.SessionKindHost, Agent: "copilot"}, false},
		{"cli・器のセッション ID・計測が任意の AI", service.Actor{Via: "cli", SessionID: "host-1", SessionKind: service.SessionKindHost, Agent: "copilot"}, false},

		// 器の印「以外」は対象のまま（判定は == で、ホワイトリストや否定条件ではない）。
		// ヘッダの未知の値は mcpCallOf / actor が空にするが、UsageTarget 自体も空以外の未知の値で落ちてはいけない。
		{"mcp・知らない種類の印", service.Actor{Via: "mcp", SessionID: "sess-a", SessionKind: "something-new"}, true},
		{"cli・知らない種類の印", service.Actor{Via: "cli", SessionID: "sess-a", SessionKind: "something-new"}, true},
		// 大文字小文字をそろえるのは読み取る側（actor / mcpCallOf）の仕事で、UsageTarget は正規化しない。
		// ここを「小文字にしてから比べる」に変えるなら、送る側・読む側と合わせて決めること。
		{"cli・印の大文字（正規化は読み取る側の仕事）", service.Actor{Via: "cli", SessionID: "host-1", SessionKind: "HOST"}, true},
	}
	// Now は器の印と入口の判定では呼ばれないが、判定が DB を見る段まで降りたときに
	// nil の Now で落ちて理由が分からなくなるのを避けるために入れておく。
	svc := &service.Service{Now: func() time.Time { return time.Unix(0, 0).UTC() }}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := svc.UsageTarget(context.Background(), notQueried{t}, c.actor, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("UsageTarget = %v（期待 %v）", got, c.want)
			}
		})
	}
}

// TestUsageTargetOptInAgentRecentUsage は、計測が任意の AI（store.UsageOptInAgents）の操作が
// 「その利用者のその AI のスナップショットが直近 store.UsageOptInWindow 以内に届いているか」で決まることを固定する。
//
// TestUsageTargetSessionKindByVia は Agent を設定しないので store.UsageOptIn("") == false の枝しか通らず、
// store.HasRecentClientUsage（DB を見る枝）を一度も呼ばない。ここがその枝を通す唯一のテストなので、
// HasRecentClientUsage の SQL（project_id / user_id / client / received_at）を書き換えたときの回帰はここで落ちる。
func TestUsageTargetOptInAgentRecentUsage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	other := e.user("other", "other-password-1", "member")
	svc := service.New(e.db, e.clock.Now)
	now := e.clock.Now()

	actor := func(agent, kind string) service.Actor {
		return service.Actor{UserID: u.ID, Via: "mcp", SessionID: service.MCPSessionPrefix + "conn-1", Agent: agent, SessionKind: kind}
	}
	target := func(t *testing.T, a service.Actor) bool {
		t.Helper()
		got, err := svc.UsageTarget(ctx, e.db, a, pr.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	snapshot := func(userID int64, client string, receivedAt time.Time, key string) {
		t.Helper()
		if _, dup, err := store.InsertUsageSnapshot(ctx, e.db, store.UsageSnapshot{
			ProjectID: pr.ID, UserID: userID, Client: client, SessionID: "sess-" + key, ConversationID: "c-" + key,
			Trigger: "stop", At: receivedAt, DedupeKey: "dedupe-" + key, ReceivedAt: receivedAt,
		}); err != nil || dup {
			t.Fatalf("スナップショットの挿入: dup=%v err=%v", dup, err)
		}
	}

	// 計測が任意でない AI は、スナップショットが 1 件も無くても対象（DB の結果で決めない）。
	if !target(t, actor("claude-code", "")) {
		t.Error("計測が任意でない AI が対象外になった")
	}
	// copilot は、その利用者のスナップショットが届いていなければ対象外（付けようがない）。
	if target(t, actor("copilot", "")) {
		t.Error("スナップショットが無い copilot が対象になった")
	}
	// 別の利用者の copilot のスナップショットでは対象に戻らない（user_id の条件）。
	snapshot(other.ID, "copilot", now.Add(-time.Minute), "other")
	if target(t, actor("copilot", "")) {
		t.Error("別の利用者のスナップショットで対象になった")
	}
	// 別の AI のスナップショットでも戻らない（client の条件）。
	snapshot(u.ID, "claude-code", now.Add(-time.Minute), "claude")
	if target(t, actor("copilot", "")) {
		t.Error("別の AI のスナップショットで対象になった")
	}
	// 窓より古いスナップショットでも戻らない（received_at の条件）。
	snapshot(u.ID, "copilot", now.Add(-store.UsageOptInWindow-time.Minute), "old")
	if target(t, actor("copilot", "")) {
		t.Error("窓より古いスナップショットで対象になった")
	}
	// 窓の中に届いていれば対象。
	snapshot(u.ID, "copilot", now.Add(-time.Minute), "recent")
	if !target(t, actor("copilot", "")) {
		t.Error("窓の中のスナップショットがあるのに対象外になった")
	}
	// 器のセッション ID の印は、この判定より先に効く（スナップショットが届いていても対象外）。
	if target(t, actor("copilot", service.SessionKindHost)) {
		t.Error("器のセッション ID が、計測が任意の AI の判定に追い越された")
	}
}
