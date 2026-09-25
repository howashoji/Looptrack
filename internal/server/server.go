// Package server は HTTP サーバ（/im 配下の Web 画面・REST API）を持つ。
package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 埋め込むのは実際に配信するものだけに絞る。static/* （中身を丸ごと）だと、node --test で回す
// static/render_test.mjs まで実行ファイルに入り、下の GET <base>/static/ から認証なしで配信されていた。
// テストコードの置き場は動かしていない（絞ったのはこのパターンだけ）。embed_assets_test.go が実測する。
//
//go:embed templates/*.html static/*.css static/*.js
var assets embed.FS

// Config はサーバの設定。
type Config struct {
	BasePath       string         // "/im"
	CookieSecure   bool           // 本番は true（HTTPS 前提）
	Box            *auth.Box      // TOTP シークレットの暗号化
	TrustedProxies []netip.Prefix // X-Real-IP を信用する接続元（ホスト Nginx → Docker ブリッジ）
	Issuer         string         // TOTP の発行者名（認証アプリに表示される名前。LOOPTRACK_TOTP_ISSUER。空なら DefaultTOTPIssuer）
	PublicURL      string         // 外から見た URL の基点（例 https://example.com）。OAuth のメタデータで使う
	// 実行ファイルの配布と版の判定（DESIGN.md §5-11）
	DistDir          string // looptrack の配布ディレクトリ（LOOPTRACK_DIST_DIR。空なら binaries は空の一覧）
	ClientMinVersion string // 対応する looptrack の最低の版（LOOPTRACK_CLIENT_MIN_VERSION。空なら判定しない）
	Logger           *slog.Logger
	Now              func() time.Time // テスト用
	// LocalMode はローカルモード（§5-12）。127.0.0.1 固定の待ち受け（serve が検査）で、Web・API・MCP を
	// 認証なしで最初の管理者として通す。Host・Origin の検査でブラウザ経由の攻撃を止める
	LocalMode bool
	// AllowNoAdmin は管理者 0 人でも「セットアップ未完了」にしない（テスト用。本番の serve は常に false）
	AllowNoAdmin bool
}

// セッション・ログイン制限の時間。
const (
	sessionLifetime    = 7 * 24 * time.Hour
	sessionIdleTimeout = 12 * time.Hour
	pendingLifetime    = 10 * time.Minute
	lockWindow         = 15 * time.Minute
	lockPerLogin       = 5
	lockPerIP          = 20
)

// Server は HTTP ハンドラ。
type Server struct {
	cfg      Config
	db       *sql.DB
	tmpl     *template.Template
	mux      *http.ServeMux
	svc      *service.Service
	hashSlot chan struct{} // argon2id の同時実行数（メモリ 19MiB × 2）
	// adminReady は有効な管理者がいると分かったか（分かったら以後は数えない）
	adminReady atomic.Bool
	// firstRunMu は画面版の初回設定の送信を 1 つずつ行う
	firstRunMu sync.Mutex
	// mcpServersBuilt は MCP のサーバ（ツール定義）を組んだ回数。言語ごとに起動時の 1 回だけで、
	// 要求ごとには組み直さない（テストがこれを数える）
	mcpServersBuilt atomic.Int64
}

// DefaultTOTPIssuer は TOTP の発行者名の既定（認証アプリに表示される）。
// 登録済みの認証アプリの表示を変えたくないサーバは LOOPTRACK_TOTP_ISSUER で以前の名前を設定する。
const DefaultTOTPIssuer = "Looptrack"

// New はサーバを作る。
func New(cfg Config, db *sql.DB) (*Server, error) {
	if cfg.BasePath == "" {
		cfg.BasePath = "/im"
	}
	if cfg.Issuer == "" {
		cfg.Issuer = DefaultTOTPIssuer
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	funcs := template.FuncMap{
		"base": func() string { return cfg.BasePath },
		// T は画面の文面を対訳表から出す（{{T .Lang "server.web.…"}}）。
		// 実体は i18n.TFunc。まだ .Lang を詰めていない画面では nil が渡り、対訳表の正本（日本語）で出る。
		"T": i18n.TFunc,
		// headData は共通の <head>（layout.html の "head"）へ渡す組。テンプレートは引数を 1 つしか
		// 取れないので、表示の言語と題名をここで束ねる。言語を渡さないと <html lang> と製品名だけが
		// 既定の言語に固定される（画面の本文は .Lang で正しく出るので、見落としやすい）。
		"headData": func(lang any, title string) map[string]any {
			return map[string]any{"Lang": lang, "Title": title}
		},
		// localMode はローカルモードか（ログアウトを出さない）
		"localMode": func() bool { return cfg.LocalMode },
		// who は画面に出す利用者名（表示名。無ければログイン名）
		"who": func(u store.User) string {
			if strings.TrimSpace(u.DisplayName) != "" {
				return u.DisplayName
			}
			return u.Login
		},
		// initial はユーザーメニューのアイコンに出す 1 文字
		"initial": func(name string) string {
			for _, r := range strings.TrimSpace(name) {
				return strings.ToUpper(string(r))
			}
			return "?"
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, db: db, tmpl: tmpl, mux: http.NewServeMux(), svc: service.New(db, cfg.Now), hashSlot: make(chan struct{}, 2)}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	b := s.cfg.BasePath
	static, _ := fs.Sub(assets, "static")
	s.mux.Handle("GET "+b+"/static/", http.StripPrefix(b+"/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET "+b+"/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.db.PingContext(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if s.cfg.LocalMode {
			// 認証を省いているサーバであることを、認証の要らないこの口で名乗る（§5-12）。
			// 同梱の CLI は、資格情報が無いときこれを見てトークンなしで呼ぶ（api.LocalModeHeader）。
			// ここに届くのは Host が loopback の名前の要求だけ（gate の DNS rebinding の検査）。
			w.Header().Set("X-Looptrack-Local-Mode", "1")
		}
		w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("GET "+b, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, b+"/", http.StatusMovedPermanently) })

	s.mux.HandleFunc("GET "+b+"/login", s.loginPage)
	s.mux.HandleFunc("POST "+b+"/login", s.loginSubmit)
	s.mux.HandleFunc("GET "+b+"/login/totp", s.pending(s.totpPage))
	s.mux.HandleFunc("POST "+b+"/login/totp", s.pending(s.totpSubmit))
	s.mux.HandleFunc("GET "+b+"/login/totp/setup", s.pending(s.totpSetupPage))
	s.mux.HandleFunc("POST "+b+"/login/totp/setup", s.pending(s.totpSetupSubmit))
	s.mux.HandleFunc("POST "+b+"/logout", s.web(s.logout))

	api := http.NewServeMux()
	api.HandleFunc("GET "+b+"/api/v1/me", s.apiMe)
	api.HandleFunc("GET "+b+"/api/v1/projects", s.apiProjects)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}", s.apiProject)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/issues", s.apiListIssues)
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/issues", s.apiCreateIssue)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/issues.xlsx", s.apiExportIssues)    // 課題管理表（絞り込みはサーバで適用）
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/issues.xlsx", s.apiExportSelected) // 課題管理表（画面が出している ID のまま）
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/ready", s.apiReady)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/matrix", s.apiMatrix)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/summary", s.apiSummary)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/board", s.apiBoard)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/board/search", s.apiBoardSearch) // 画面の本文の検索（当たった ID だけを返す）
	api.HandleFunc("GET "+b+"/api/v1/issues/{id}", s.apiIssue)
	api.HandleFunc("GET "+b+"/api/v1/issues/{id}/usage", s.apiIssueUsage)                      // トークン消費の段階別
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/usage", s.apiPostUsage)                  // スナップショットの受け取り
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/usage/report", s.apiUsageReport)          // レポート用の集計
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/usage/report.xlsx", s.apiUsageReportXLSX) // 同じ集計の数表
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/usage/ledger", s.apiUsageLedger)          // レポートの台帳
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/usage/ledger", s.apiAddUsageLedger)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/usage/coverage", s.apiUsageCoverage) // 付与漏れと充足率
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/usage/requests", s.apiUsageRequests) // レポートの作成依頼
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/usage/requests", s.apiAddUsageRequest)
	api.HandleFunc("PATCH "+b+"/api/v1/issues/{id}", s.apiUpdateIssue)
	api.HandleFunc("POST "+b+"/api/v1/issues/{id}/comments", s.apiComment)
	api.HandleFunc("POST "+b+"/api/v1/issues/{id}/status", s.apiStatus)
	api.HandleFunc("GET "+b+"/api/v1/issues/{id}/verify", s.apiGetVerify)   // 検証コマンドと直近の記録
	api.HandleFunc("POST "+b+"/api/v1/issues/{id}/verify", s.apiPostVerify) // CLI が手元で実行した結果の記録
	api.HandleFunc("POST "+b+"/api/v1/issues/{id}/assign", s.apiAssign)     // 担当者の変更
	api.HandleFunc("GET "+b+"/api/v1/activity", s.apiActivity)
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/guide", s.apiGuide) // 使い方とルール
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/next", s.apiNext)  // ループ運用の着手
	api.HandleFunc("GET "+b+"/api/v1/dist", s.apiDist)                   // 配布スクリプトの一覧と SHA-256
	api.HandleFunc("GET "+b+"/api/v1/dist/{name...}", s.apiDistFile)
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/install", s.apiPostInstall) // 導入済み通知
	api.HandleFunc("GET "+b+"/api/v1/projects/{slug}/install", s.apiGetInstall)
	// 経路が無いときの 404。コードは unknown_api（資源が無い 404 の not_found とは別物として分けてある）。
	// 後方互換のため、古いサーバ（このコードより前）は引き続き not_found + 日本語の文面
	// 「API が見つかりません」を返す前提で、クライアントは両方を 404 として扱う。
	api.HandleFunc(b+"/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown_api", i18n.T(reqLang(r), "server.api.err.api_not_found"))
	})
	s.mux.Handle(b+"/api/", s.api(gzipJSON(api))) // JSON の応答は gzip で返す（gzip.go）
	s.mux.Handle(b+"/mcp", s.mcpHandler())
	// setup ツールが返す期限つきの取得 URL（トークンの無い端末が配布物を取る）
	s.mux.HandleFunc("GET "+b+"/setup/{ticket}/{$}", s.setupDistList)
	s.mux.HandleFunc("GET "+b+"/setup/{ticket}/{name...}", s.setupDistFile)

	// OAuth 2.1。メタデータはホスト直下（/.well-known/…/im…）に置く
	s.mux.HandleFunc("GET /.well-known/oauth-protected-resource"+b+"/mcp", s.protectedResourceMetadata)
	s.mux.HandleFunc("GET /.well-known/oauth-protected-resource"+b, s.protectedResourceMetadata)
	s.mux.HandleFunc("GET /.well-known/oauth-authorization-server"+b, s.authorizationServerMetadata)
	s.mux.HandleFunc("GET "+b+"/.well-known/oauth-protected-resource", s.protectedResourceMetadata)
	s.mux.HandleFunc("GET "+b+"/.well-known/oauth-authorization-server", s.authorizationServerMetadata)
	s.mux.HandleFunc(b+"/oauth/register", s.oauthRegister)
	s.mux.HandleFunc(b+"/oauth/token", s.oauthToken)
	s.mux.HandleFunc("GET "+b+"/oauth/authorize", s.web(s.authorizePage))
	s.mux.HandleFunc("POST "+b+"/oauth/authorize", s.web(s.authorizeSubmit))

	// アカウント設定・利用者管理
	s.mux.HandleFunc("GET "+b+"/account", s.web(s.accountPage))
	s.mux.HandleFunc("POST "+b+"/account/password", s.web(s.accountPassword))
	s.mux.HandleFunc("POST "+b+"/account/lang", s.web(s.accountLang)) // 表示の言語（設定なしに戻せる）
	s.mux.HandleFunc("POST "+b+"/account/tokens", s.web(s.accountCreateToken))
	s.mux.HandleFunc("POST "+b+"/account/tokens/revoke", s.web(s.accountRevokeToken))
	s.mux.HandleFunc("GET "+b+"/account/totp", s.web(s.accountTOTPPage)) // 二段階認証の登録・解除
	s.mux.HandleFunc("POST "+b+"/account/totp", s.web(s.accountTOTPSubmit))
	s.mux.HandleFunc("POST "+b+"/account/totp/disable", s.web(s.accountTOTPDisable))
	s.mux.HandleFunc("GET "+b+"/admin/users", s.admin(s.adminUsersPage))
	s.mux.HandleFunc("POST "+b+"/admin/users", s.admin(s.adminCreateUser))
	s.mux.HandleFunc("GET "+b+"/admin/users/{login}", s.admin(s.adminUserPage))
	s.mux.HandleFunc("POST "+b+"/admin/users/{login}/{action}", s.admin(s.adminUserAction))
	s.mux.HandleFunc("GET "+b+"/admin/security", s.adminForbidden(s.adminSecurityPage)) // 二段階認証の必須 / 任意
	s.mux.HandleFunc("POST "+b+"/admin/security", s.adminForbidden(s.adminSecuritySubmit))
	s.mux.HandleFunc("GET "+b+"/admin/projects", s.admin(s.adminProjectsPage))   // プロジェクト管理
	s.mux.HandleFunc("POST "+b+"/admin/projects", s.admin(s.adminCreateProject)) // プロジェクトの作成
	s.mux.HandleFunc("POST "+b+"/admin/projects/{slug}/member", s.admin(s.adminProjectMember))
	s.mux.HandleFunc("POST "+b+"/admin/projects/{slug}/send-prompts", s.adminForbidden(s.adminSendPrompts))   // 指示文の作業名を送るかの切り替え（管理者以外は 403）
	s.mux.HandleFunc("POST "+b+"/admin/projects/{slug}/rename", s.adminForbidden(s.adminProjectRename))       // 表示名の変更（管理者以外は 403）
	s.mux.HandleFunc("POST "+b+"/admin/projects/{slug}/archive", s.adminForbidden(s.adminProjectArchive))     // アーカイブ（画面の「削除」。論理削除）
	s.mux.HandleFunc("POST "+b+"/admin/projects/{slug}/unarchive", s.adminForbidden(s.adminProjectUnarchive)) // アーカイブから戻す

	s.mux.HandleFunc("GET "+b+firstRunPath+"/done", s.web(s.firstRunDone)) // 初回設定の最後の画面（ローカルモードだけ）
	s.mux.HandleFunc("GET "+b+"/{$}", s.web(s.hub))
	s.mux.HandleFunc("GET "+b+"/p/{slug}/{$}", s.web(s.board))
	s.mux.HandleFunc("GET "+b+"/p/{slug}", s.web(s.board))
	s.mux.HandleFunc("GET "+b+"/p/{slug}/report-requests", s.web(s.reportRequestsPage)) // レポート作成の依頼
	s.mux.HandleFunc("POST "+b+"/p/{slug}/report-requests", s.web(s.reportRequestsSubmit))
	s.mux.HandleFunc("POST "+b+"/p/{slug}/issues/{id}/assign", s.web(s.assignSubmit)) // 担当の変更（閲覧のみの例外）
	s.mux.HandleFunc(b+"/", s.web(func(w http.ResponseWriter, r *http.Request, p *principal) {
		s.notFoundPage(w, r, i18n.T(reqLang(r), "server.web.err.page_not_found"))
	}))
}

// ServeHTTP はセキュリティヘッダを付けてルーティングする。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	if s.cfg.CookieSecure && !s.cfg.LocalMode {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	if !strings.HasPrefix(r.URL.Path, s.cfg.BasePath+"/static/") {
		h.Set("Cache-Control", "no-store")
	}
	// ローカルモードの Host・Origin の検査と、管理者 0 人のときの「セットアップ未完了」
	r, ok := s.gate(w, r)
	if !ok {
		return
	}
	s.mux.ServeHTTP(w, r)
}

// clientIP は接続元 IP。信用するプロキシ（ホスト Nginx）経由なら X-Real-IP を使う。
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(addr.Unmap()) {
			if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
				if a, err := netip.ParseAddr(real); err == nil {
					return a.String()
				}
			}
			break
		}
	}
	return addr.Unmap().String()
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var e apiError
	e.Error.Code, e.Error.Message = code, message
	writeJSON(w, status, e)
}

// render は画面を描く。表示の言語は要求から決めて（reqLang）テンプレートのデータに詰めるので、
// 呼び出し側は Lang を用意しなくてよい（詰め忘れると、その画面だけ既定の言語に固定される）。
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Lang"] = reqLang(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.cfg.Logger.Error("template", "name", name, "err", err)
	}
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.cfg.Logger.Error("internal error", "path", r.URL.Path, "err", err)
	msg := i18n.T(reqLang(r), "server.api.err.internal")
	if strings.HasPrefix(r.URL.Path, s.cfg.BasePath+"/api/") {
		writeError(w, http.StatusInternalServerError, "internal", msg)
		return
	}
	http.Error(w, msg, http.StatusInternalServerError)
}
