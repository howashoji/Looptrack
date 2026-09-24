package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// ローカルモード（設計 DESIGN.md §5-12）と、管理者 0 人のときの「セットアップ未完了」。
//
// ローカルモードは 127.0.0.1 / ::1 / localhost だけで待ち受け、Web・REST API・MCP を認証なしで
// 「最初の管理者」（無効化されていない管理者のうち ID が最小の人）として通す。
// 認証を省く代わりに、ブラウザ経由の攻撃（DNS rebinding・別サイトからの CSRF）を Host と Origin の検査で止める。

func msgSetupRequired(lang i18n.Lang) string { return i18n.T(lang, "server.err.setup_required") }

// localHosts はローカルモードで受け付ける待ち受けのホスト名・Host ヘッダのホスト名。
var localHosts = map[string]bool{"127.0.0.1": true, "::1": true, "localhost": true}

// CheckLocalListen はローカルモードの待ち受けアドレスを検査する（127.0.0.1 / ::1 / localhost 以外はエラー）。
func CheckLocalListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return i18n.Wrapf(err, "server.err.local_listen_invalid", "addr", strconv.Quote(addr))
	}
	if !localHosts[strings.ToLower(host)] {
		return i18n.Errorf("server.err.local_listen_not_loopback", "addr", strconv.Quote(addr))
	}
	if port == "" {
		return i18n.Errorf("server.err.local_listen_no_port", "addr", strconv.Quote(addr))
	}
	return nil
}

// localHostHeader は Host ヘッダ（またはその URL の host:port）が loopback の名前か。
func localHostHeader(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return localHosts[strings.ToLower(host)]
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// localRequestAllowed はローカルモードでブラウザ経由の攻撃を止める。拒否したら応答を書いて false を返す。
//   - Host が loopback の名前でなければ拒否（DNS rebinding。攻撃者のドメインを 127.0.0.1 に向けても Host はそのドメインのまま）
//   - 変更系（GET / HEAD / OPTIONS 以外）は、Origin があればこのサーバ自身（scheme://Host）と一致しなければ拒否。
//     Sec-Fetch-Site があれば same-origin / none 以外を拒否（別サイト・同じサイトの別ポートからのフォーム送信・fetch）。
//     どちらも無い要求（CLI・MCP クライアント等のブラウザ以外）は通す
func (s *Server) localRequestAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !localHostHeader(r.Host) {
		s.localReject(w, r, http.StatusMisdirectedRequest, "bad_host", i18n.T(reqLang(r), "server.err.local_bad_host"))
		return false
	}
	if safeMethod(r.Method) {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		s.localReject(w, r, http.StatusForbidden, "cross_origin", i18n.T(reqLang(r), "server.err.local_cross_origin"))
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) {
			s.localReject(w, r, http.StatusForbidden, "cross_origin", i18n.T(reqLang(r), "server.err.local_cross_origin"))
			return false
		}
	}
	return true
}

func (s *Server) localReject(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	if s.wantsJSON(r) {
		writeError(w, status, code, msg)
		return
	}
	http.Error(w, msg, status)
}

// wantsJSON は API・MCP・OAuth など機械向けの経路か（エラーを JSON で返す）。
func (s *Server) wantsJSON(r *http.Request) bool {
	b := s.cfg.BasePath
	p := r.URL.Path
	return strings.HasPrefix(p, b+"/api/") || p == b+"/mcp" || strings.HasPrefix(p, b+"/mcp/") ||
		strings.HasPrefix(p, b+"/oauth/") || strings.HasPrefix(p, b+"/setup/")
}

// setupGated はセットアップ未完了の検査の対象か（静的ファイル・healthz・OAuth のメタデータは除く）。
func (s *Server) setupGated(r *http.Request) bool {
	b := s.cfg.BasePath
	p := r.URL.Path
	if p != b && !strings.HasPrefix(p, b+"/") {
		return false
	}
	return !strings.HasPrefix(p, b+"/static/") && p != b+"/healthz" && !strings.HasPrefix(p, b+"/.well-known/")
}

// errNoAdmin は有効な管理者が 1 人もいないこと。
var errNoAdmin = i18n.Errorf("server.err.no_admin")

// setupReady は有効な管理者がいるか。一度いると分かったら以後は数えない（画面は管理者を 0 人にする変更を拒否する。管理コマンドで 0 人にしたときは再起動まで検査しない）。
// 0 人の間は要求ごとに数える（looptrack setup で作られたらすぐ通す）。
func (s *Server) setupReady(ctx context.Context) (bool, error) {
	if s.cfg.AllowNoAdmin || s.adminReady.Load() {
		return true, nil
	}
	n, err := store.CountActiveAdmins(ctx, s.db)
	if err != nil {
		return false, err
	}
	if n > 0 {
		s.adminReady.Store(true)
		return true, nil
	}
	return false, nil
}

// localAdmin はローカルモードの利用者（無効化されていない管理者のうち ID が最小の人）。
// 要求ごとに引く（通常モードのセッション・トークンの検証と同じく 1 回の問い合わせ。無効化・役割の変更がすぐ効く）。
func (s *Server) localAdmin(ctx context.Context) (store.User, error) {
	u, err := store.FirstActiveAdmin(ctx, s.db)
	if errors.Is(err, store.ErrNotFound) {
		return u, errNoAdmin
	}
	return u, err
}

// localJoinAll は、ローカルの利用者の参加の行が無いプロジェクトすべてに、その人を admin で参加させる。
// ローカルモードの利用者は 1 人なので、looptrack project create などで作ったプロジェクトにもすぐ書けるようにする
// （管理者でも参加していないプロジェクトは閲覧のみ）。役割を要求ごとに計算するのではなく行を作るのは、
// 担当者の候補（AssignableMembers）と一覧（MemberProjects）も参加の行で決まるため。既存の行は変えない
// （画面で自分を viewer・editor にしたものはそのまま）。チームのサーバ（通常モード）では呼ばない。
func (s *Server) localJoinAll(ctx context.Context, u store.User) error {
	ids, err := store.ProjectsWithoutMember(ctx, s.db, u.ID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		added, err := store.AddMemberIfAbsent(ctx, s.db, id, u.ID, "admin")
		if err != nil {
			return err
		}
		if added {
			s.cfg.Logger.Info("local_mode", "action", "member", "target", u.Login, "project_id", id, "role", "admin")
		}
	}
	return nil
}

type localUserKey struct{}

// localUserFrom は ServeHTTP が入れたローカルモードの利用者。
func localUserFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(localUserKey{}).(store.User)
	return u, ok
}

// gate はローカルモードの検査とセットアップ未完了の検査を行う。通すなら（利用者を入れた）要求を返す。
func (s *Server) gate(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if s.cfg.LocalMode && !s.localRequestAllowed(w, r) {
		return nil, false
	}
	if !s.setupGated(r) {
		return r, true
	}
	ctx := r.Context()
	if s.cfg.LocalMode {
		u, err := s.localAdmin(ctx)
		if errors.Is(err, errNoAdmin) {
			if s.wantsJSON(r) {
				s.setupRequired(w, r)
			} else {
				s.firstRun(w, r) // 画面は初回設定
			}
			return nil, false
		}
		if err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
		if err := s.localJoinAll(ctx, u); err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
		return r.WithContext(context.WithValue(ctx, localUserKey{}, u)), true
	}
	ready, err := s.setupReady(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	if !ready {
		s.setupRequired(w, r)
		return nil, false
	}
	return r, true
}

// setupRequired は「セットアップ未完了」を返す（API・MCP・OAuth は 503 の JSON、画面は 503 の案内）。
func (s *Server) setupRequired(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	if s.wantsJSON(r) {
		msg := msgSetupRequired(reqLang(r))
		if s.cfg.LocalMode {
			msg = msgFirstRun(reqLang(r))
		}
		writeError(w, http.StatusServiceUnavailable, "setup_required", msg)
		return
	}
	s.render(w, r, http.StatusServiceUnavailable, "setup_required.html", map[string]any{"Message": msgSetupRequired(reqLang(r))})
}

// localPrincipal はローカルモードの API・MCP の利用者（トークンなし。経路は api）。
func (s *Server) localPrincipal(r *http.Request) (*principal, error) {
	u, ok := localUserFrom(r.Context())
	if !ok {
		return nil, errNoAdmin
	}
	return &principal{User: u, Via: "api"}, nil
}

// localWeb はローカルモードの画面の認証。セッションが無い・別の利用者のものなら、ローカルの利用者のセッションを作る
// （ログイン画面を経ない自動ログイン）。POST の CSRF の検査は通常モードと同じ（作ったばかりのセッションの POST は通らない）。
func (s *Server) localWeb(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request, *principal)) {
	u, ok := localUserFrom(r.Context())
	if !ok {
		s.internalError(w, r, errNoAdmin)
		return
	}
	sess, su, err := s.sessionFrom(r)
	if err != nil || !sess.MFAPassed || su.ID != u.ID {
		created, err := s.createSession(w, r, u, true, false) // TOTP は経ていない（通常モードで同じ DB を使えば必須の設定で止まる）
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		sess = &created
	}
	if r.Method == http.MethodPost && !s.validCSRF(r, sess.CSRFToken) {
		http.Error(w, i18n.T(reqLang(r), "server.web.err.csrf"), http.StatusForbidden)
		return
	}
	p := &principal{User: u, Via: "web", Session: sess}
	r = r.WithContext(withPrincipal(r.Context(), p))
	setContentLanguage(w, r)
	next(w, r, p)
}

// localAPI はローカルモードの API の認証。Authorization は見ない。画面のセッション（Cookie）があれば経路 web として
// 通常どおり CSRF を検査し、無ければ経路 api（CLI 等）として通す。
func (s *Server) localAPI(w http.ResponseWriter, r *http.Request, next http.Handler) {
	p, err := s.localPrincipal(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if sess, su, err := s.sessionFrom(r); err == nil && sess.MFAPassed && su.ID == p.User.ID {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.validCSRF(r, sess.CSRFToken) {
			writeError(w, http.StatusForbidden, "csrf", i18n.T(reqLang(r), "server.api.err.csrf"))
			return
		}
		p = &principal{User: p.User, Via: "web", Session: sess}
	}
	r = r.WithContext(withPrincipal(r.Context(), p))
	setContentLanguage(w, r)
	next.ServeHTTP(w, r)
}
