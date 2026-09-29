package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/usage"
)

// MCP の着手を、後から届いたトークン情報のスナップショットの会話（session_id）に結ぶ（DESIGN.md「MCP の操作を会話に結ぶ」）。
//
// MCP の接続は、クライアントが名乗るセッション ID（X-Looptrack-Session）を送れないことが多い。そのときの着手のイベントは
// 接続 ID（mcp-conn:…）か空のセッション ID を持ち、CLI から見ると「同じセッションか判定できない」になる。
// 付与の hook は、同じツール呼び出しの ID を持つスナップショットを会話のセッション ID 付きで送ってくるので、
// 呼び出しの時点で ID のハッシュを detail に残しておき、スナップショットが届いたら突き合わせて結ぶ。
// 結びは読むときの session_id を差し替えるだけで、操作の利用者・トークン・権限は変えない。他人のイベントは結べない。

// maxToolUseID はツール呼び出しの ID として受け付ける長さの上限。
const maxToolUseID = 128

// ToolUseHash はツール呼び出しの ID を検査し、記録に使うハッシュ（SHA-256 の 16 進の先頭 32 字）を返す。
// 1〜128 字の英数字・`_`・`-` だけを受け付け、外れたら ok = false（呼び出した側は黙って捨てる）。
// 生の ID は残さない。MCP の呼び出し（_meta）とスナップショットの受信の両方がこの関数を使う（片方だけ変えると突き合わなくなる）。
func ToolUseHash(id string) (hash string, ok bool) {
	if id == "" || len(id) > maxToolUseID {
		return "", false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", false
		}
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:32], true
}

// LinkMCPEventSessions は、受け取ったスナップショット（dedupe_key で引く。重複で足さなかった再送も元の行で結ぶ）が
// MCP の変更操作のもの（trigger issue_op・via mcp）でツール呼び出しの ID を持つとき、同じ ID のハッシュを持つ、
// 同じプロジェクト・同じ利用者（スナップショットを送った、認証した利用者）の MCP のイベントのうち、
// 接続 ID か空のセッション ID のもの（クライアントが名乗ったセッション ID のものは結ばない）で、
// スナップショットを送ろうとした時刻の前 store.UsageAttachWindow 以内のものを、スナップショットの session_id に結ぶ。
// 結んだ件数を返す。何度呼んでも結果は変わらない。
func (s *Service) LinkMCPEventSessions(ctx context.Context, userID int64, dedupeKey, toolUseID string) (int64, error) {
	hash, ok := ToolUseHash(toolUseID)
	if !ok {
		return 0, nil
	}
	snap, err := store.UsageSnapshotByDedupe(ctx, s.DB, dedupeKey)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// 元の行を送った利用者と、いま送ってきた利用者が違えば結ばない（重複の鍵が他人の行に当たった再送）。
	// スナップショットのセッション ID が接続 ID の印を持つものも結ばない（結ぶと、比べる種類が混ざる）。
	if snap.UserID != userID || snap.Trigger != usage.TriggerIssueOp || snap.Via != "mcp" ||
		snap.SessionID == "" || strings.HasPrefix(snap.SessionID, MCPSessionPrefix) {
		return 0, nil
	}
	t := snap.ReceivedAt
	if !snap.AttemptedAt.IsZero() {
		t = snap.AttemptedAt
	}
	return store.LinkEventSessions(ctx, s.DB, store.EventSessionLink{
		Snapshot: snap, UserID: userID, ToolUse: hash,
		From: t.Add(-store.UsageAttachWindow), To: t,
		ConnPrefix: MCPSessionPrefix, LinkedAt: s.Now(),
	})
}

// StarterSession は、着手したイベント st のセッション ID のうち、見る側（viewer）と比べるのに使う値を返す。
// 結んだ値（LinkedSessionID）を使うのは、見る側が会話のセッション ID を名乗っているときだけ:
//   - 見る側が器の ID（SessionKindHost）なら、結んだ値（会話のセッション ID）とは種類が違うので比べない（記録した値のまま）。
//   - 見る側が MCP の接続 ID なら、記録した値（同じ接続 ID）で比べる（結んだ値に替えると自分の着手が別の経路に見える）。
//   - 見る側にセッション ID が無ければ、どちらでも比べられない。
func StarterSession(viewer Actor, st store.Starter) string {
	if st.LinkedSessionID == "" || viewer.SessionID == "" || viewer.SessionKind == SessionKindHost ||
		strings.HasPrefix(viewer.SessionID, MCPSessionPrefix) {
		return st.SessionID
	}
	return st.LinkedSessionID
}
