package core

// MCP の呼び出しを、呼んだ会話のセッションに結ぶ合鍵を届ける hook（設計 §6「セッションの見分け方」）。
//
//	PreToolUse（Claude Code だけに配線する）: looptrack の MCP のツール（サーバ名が LOOPTRACK_MCP_SERVER（既定 "looptrack"）に
//	    合うもの）を呼ぶ直前に、ツール呼び出しの ID（tool_use_id）と会話のセッション ID を POST /projects/<slug>/session-binds で送る。
//
// MCP の接続設定はヘッダを持てないことが多く、サーバは MCP の呼び出しの時点で呼んだ会話を知らない。Claude Code は同じ
// tool_use_id を tools/call の _meta["claudecode/toolUseId"] に入れてくるので、サーバはそれで合鍵を引いて会話のセッション ID を使う。
// 合鍵は MCP の呼び出しより先に届いていないと役に立たないので、切り離さずに同期で送り、短い待ち時間で打ち切る。
// 出力は何も無い（LOOPTRACK_MCP_SERVER が RE2 で読めないときの知らせ 1 行だけが例外。MCP のツールの呼び出しのときに限る）。URL・プロジェクト・トークンが無い・届かない・時間切れ・古いサーバ（404 unknown_api）・拒否のどれでも、
// 何も出さず何も記録せずにツールの呼び出しを通す（fail-open）。合鍵が届かなかった呼び出しは、サーバで従来どおり
// 接続 ID か空のセッション ID になるだけ。

import (
	"context"
	"regexp"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/hookio"
)

// sessionBindTimeout は合鍵の送信の待ち時間。hook が返るまでツールの呼び出しは待たされるので短くする
// （配線の timeout と登録表の Timeout はこれより長い）。
const sessionBindTimeout = 1500 * time.Millisecond

// mcpToolNameRe は Claude Code の MCP のツール名（mcp__<サーバ>__<ツール>）。サーバ名の絞り方は usage の plan と同じ
// （LOOPTRACK_MCP_SERVER の RE2。解釈できなければ一致しないとして、読めない旨を知らせる）。ツールは絞らない（looptrack のツールはどれも操作の主体を決める）。
var mcpToolNameRe = regexp.MustCompile(`^mcp__(?P<server>.+?)__(?P<tool>[A-Za-z0-9][A-Za-z0-9_-]*)$`)

// SessionBind は `looptrack hook issue-session-bind`。出力は無い。
func SessionBind(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error) {
	if ev.Name != hookio.PreToolUse || ev.Tool == nil || ev.Tool.UseID == "" {
		return hookio.Result{}, nil
	}
	m := mcpToolNameRe.FindStringSubmatch(ev.Tool.RawName)
	if m == nil {
		return hookio.Result{}, nil
	}
	if ok, notice := c.mcpServerMatch(m[1]); !ok {
		// LOOPTRACK_MCP_SERVER が読めないときだけ知らせる（結び付けないことは変えない）
		return hookio.Result{SystemMessage: notice}, nil
	}
	if c.apiBase() == "" {
		return hookio.Result{}, nil
	}
	slug, err := c.project()
	if err != nil {
		return hookio.Result{}, nil
	}
	// 会話のセッション ID は hook の入力の session_id だけを使う（付与の hook が送るスナップショットの session_id と同じ値）。
	// 環境変数（CLI の判定）には倒さない。器の窓では器の ID（種類 host）になり、それを MCP の操作に付けると、
	// 付与できている MCP の操作まで付与の対象から外してしまう（§9-5「器のセッション ID」の「MCP 経路で印を送るか」）。
	// したがって種類（kind）も送らない（会話のセッション ID は種類が空）。
	sid := rawString(ev.Raw, "session_id", "sessionId")
	if sid == "" {
		return hookio.Result{}, nil
	}
	if r := []rune(sid); len(r) > session.MaxSessionLen {
		sid = string(r[:session.MaxSessionLen])
	}
	cl, err := api.NewWithTimeout(*c.Vars, sessionBindTimeout)
	if err != nil {
		return hookio.Result{}, nil
	}
	body := map[string]string{"tool_use_id": ev.Tool.UseID, "session_id": sid}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer hookio.Recover(nil, nil)
		// 結果は見ない（届かない・拒否・古いサーバのどれでも、呼び出しは従来どおりに戻るだけ）
		_, _ = cl.Do(api.Request{Method: "POST", Path: "/projects/" + api.PathEscape(slug) + "/session-binds", Body: body})
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return hookio.Result{}, nil
}
