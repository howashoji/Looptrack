package server

import (
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
)

// MCP の呼び出しを、呼んだ会話のセッションに結ぶ合鍵（DESIGN.md §6「セッションの見分け方」）。
//
// MCP の接続設定はヘッダを持てないことが多く、新しいプロトコルの Claude Code は Mcp-Session-Id も返さないので、
// MCP の呼び出しの時点では呼んだ会話のセッション ID が分からない。そこで PreToolUse の hook（issue-session-bind）が
// 呼び出しの直前に、そのツール呼び出しの ID（tool_use_id）と会話のセッション ID を REST で届け、ここに短い間だけ置く。
// Claude Code は同じ ID を tools/call の _meta["claudecode/toolUseId"] に入れてくるので、mcpCallOf がそのハッシュで引く。
//
// 置き場はメモリだけに持つ（表にしない）。届いてから MCP の呼び出しまでは数秒なので、再起動をまたぐ分と、
// 複数台に振り分けたときに別の台へ届いた分は引けないが、そのときは従来どおり接続 ID か空に戻るだけで害は無い。
// 生の tool_use_id は持たない（service.ToolUseHash のハッシュだけを鍵にする）。

const (
	// sessionBindTTL は合鍵を置いておく時間。hook は MCP の呼び出しの直前に届けるので、数秒で足りる。
	// 長くするほど、使われない合鍵がメモリに残る。
	sessionBindTTL = 2 * time.Minute
	// sessionBindMax は置いておく合鍵の件数の上限。超えるときは期限切れを掃除し、それでも満ちていれば期限の近いものから捨てる
	// （捨てられた合鍵の呼び出しは従来どおりに戻るだけで、要求は失敗しない）。
	sessionBindMax = 10000
)

type sessionBindKey struct {
	user int64
	hash string // service.ToolUseHash の戻り
}

type sessionBind struct {
	session string
	kind    string // service.SessionKindHost か空（X-Looptrack-Session-Kind と同じ意味）
	expires time.Time
}

// sessionBinds は合鍵の置き場（利用者 × ツール呼び出しの ID のハッシュ → セッション ID と種類）。
// nil でも使える（引けない・置かない）。
type sessionBinds struct {
	mu        sync.Mutex
	now       func() time.Time
	ttl       time.Duration
	max       int
	m         map[sessionBindKey]sessionBind
	lastSweep time.Time
}

func newSessionBinds(now func() time.Time) *sessionBinds {
	if now == nil {
		now = time.Now
	}
	return &sessionBinds{now: now, ttl: sessionBindTTL, max: sessionBindMax, m: map[sessionBindKey]sessionBind{}}
}

// put は合鍵を置く（同じ鍵は上書きする）。
func (b *sessionBinds) put(user int64, hash, session, kind string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	k := sessionBindKey{user, hash}
	if _, exists := b.m[k]; !exists {
		if len(b.m) >= b.max || now.Sub(b.lastSweep) >= b.ttl {
			b.sweepLocked(now)
		}
		for len(b.m) >= b.max {
			b.evictSoonestLocked()
		}
	}
	b.m[k] = sessionBind{session: session, kind: kind, expires: now.Add(b.ttl)}
}

// get は合鍵を引く（期限切れは引かずに捨てる）。1 回の呼び出しの中で何度引いても同じ値が返るよう、引いても消さない。
func (b *sessionBinds) get(user int64, hash string) (session, kind string, ok bool) {
	if b == nil || hash == "" {
		return "", "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	k := sessionBindKey{user, hash}
	v, found := b.m[k]
	if !found {
		return "", "", false
	}
	if !b.now().Before(v.expires) {
		delete(b.m, k)
		return "", "", false
	}
	return v.session, v.kind, true
}

// len は置いている件数（期限切れで、まだ掃除していないものを含む）。
func (b *sessionBinds) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m)
}

func (b *sessionBinds) sweepLocked(now time.Time) {
	for k, v := range b.m {
		if !now.Before(v.expires) {
			delete(b.m, k)
		}
	}
	b.lastSweep = now
}

func (b *sessionBinds) evictSoonestLocked() {
	var soonest sessionBindKey
	var at time.Time
	first := true
	for k, v := range b.m {
		if first || v.expires.Before(at) {
			soonest, at, first = k, v.expires, false
		}
	}
	if first {
		return
	}
	delete(b.m, soonest)
}

// sessionBindRequest は POST /projects/{slug}/session-binds の本文。
type sessionBindRequest struct {
	ToolUseID string `json:"tool_use_id"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind,omitempty"`
}

// apiPostSessionBind は合鍵を受け取る（PreToolUse の hook が MCP の呼び出しの直前に送る）。
// 置くのは認証した利用者の合鍵だけで、引くのも同じ利用者の MCP の呼び出しだけ（mcpCallOf）。
// プロジェクトは書き込みの権限の確認にだけ使う（MCP の呼び出しがどのプロジェクトを触るかは、呼び出しの時点まで決まらない）。
func (s *Server) apiPostSessionBind(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	lang := reqLang(r)
	if !canWrite(role) {
		s.serviceError(w, r, forbidden(lang, pr.Slug, i18n.T(lang, "server.api.err.what_session_bind")))
		return
	}
	var req sessionBindRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, http.StatusBadRequest, "invalid_argument", msg) }
	hash, ok := service.ToolUseHash(strings.TrimSpace(req.ToolUseID))
	if !ok {
		bad(i18n.T(lang, "server.api.err.session_bind_tool_use"))
		return
	}
	// セッション ID は X-Looptrack-Session と同じ上限。接続 ID の印で始まる値は、サーバが発行した接続 ID と
	// 区別できなくなるので受け付けない（service.ComparableSessions）。制御文字（改行・NUL・タブなど）も受け付けない
	// （ヘッダでは運べない値で、記録や表示に混ざると行や欄を壊す）。
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" || len(sid) > 128 || strings.HasPrefix(sid, service.MCPSessionPrefix) || strings.IndexFunc(sid, unicode.IsControl) >= 0 {
		bad(i18n.T(lang, "server.api.err.session_bind_session"))
		return
	}
	// 種類は X-Looptrack-Session-Kind と同じ読み方（host だけを印にし、知らない値は読み捨てる）
	kind := ""
	if strings.EqualFold(strings.TrimSpace(req.Kind), service.SessionKindHost) {
		kind = service.SessionKindHost
	}
	s.binds.put(principalFrom(r.Context()).User.ID, hash, sid, kind)
	w.WriteHeader(http.StatusNoContent)
}
