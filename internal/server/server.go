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
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// static/icon.png と static/favicon.ico は、デスクトップ版のアイコン（internal/client/desktop/icon の app_256.png と app.ico）の複製。
// サーバはクライアント側のパッケージに依存しない（TestServerNeverExecutes）ので、import で共有できない。
// 同じ絵であることは favicon_test.go が実ファイルで突き合わせる。
//
// 埋め込むのは実際に配信するものだけに絞る。static/* （中身を丸ごと）だと、node --test で回す
// static/render_test.mjs まで実行ファイルに入り、下の GET <base>/static/ から認証なしで配信されていた。
// テストコードの置き場は動かしていない（絞ったのはこのパターンだけ）。embed_assets_test.go が実測する。
//
//go:embed templates/*.html static/*.css static/*.js static/icon.png static/favicon.ico
var assets embed.FS

// Config はサーバの設定。
type Config struct {
	BasePath       string         // "/im"
	CookieSecure   bool           // 本番は true（HTTPS 前提）
	Box            *auth.Box      // TOTP シークレットの暗号化
	TrustedProxies []netip.Prefix // X-Real-IP を信用する接続元（ホスト Nginx → Docker ブリッジ）
	Issuer         string         // TOTP の発行者名（認証アプリに表示される名前。LOOPTRACK_TOTP_ISSUER。空なら DefaultTOTPIssuer）
	PublicURL      string         // 外から見た URL の基点（例 https://example.com）。OAuth のメタデータで使う
	// 実行ファイルの配布と版の判定（DESIGN.md §5-1）
	DistDir string // looptrack の配布ディレクトリ（LOOPTRACK_DIST_DIR。空なら binaries は空の一覧）
	// DistOff は LOOPTRACK_DIST_DIR を空の値で明示した（配らないと決めた）こと。未設定とは分け、配布物の遅れを知らせない
	// （install.sh も空の値には触らない。looptrack serve が os.LookupEnv で決める）
	DistOff          bool
	ClientMinVersion string // 対応する looptrack の最低の版（LOOPTRACK_CLIENT_MIN_VERSION。空なら判定しない）
	// Version はサーバ自身の版（looptrack serve が渡す）。配布物がこの版にそろっているかを起動時のログと管理者の帯で知らせる
	// （distLagStatus）。空・比べられない版（dev など）なら比べない
	Version string
	Logger  *slog.Logger
	Now     func() time.Time // テスト用
	// LocalMode はローカルモード（DESIGN.md §3-3）。127.0.0.1 固定の待ち受け（serve が検査）で、Web・API・MCP を
	// 認証なしで最初の管理者として通す。Host・Origin の検査でブラウザ経由の攻撃を止める
	LocalMode bool
	// AllowNoAdmin は管理者 0 人でも「セットアップ未完了」にしない（テスト用。本番の serve は常に false）
	AllowNoAdmin bool
	// UpdateNotice は画面の共通ヘッダの帯に出す新しい版（nil を返せば出さない）。デスクトップ版と looptrack serve が
	// 確認の結果を渡す（internal/updatecheck）。nil なら帯を出さない
	UpdateNotice func() *updatecheck.Notice
	// UpdateStopInTray は帯の止め方の案内をトレイのメニュー（デスクトップ版の「新しい版を確認する」）にするか。
	// false・nil なら環境変数 LOOPTRACK_UPDATE_CHECK=off を案内する（トレイを出していない headless・--no-tray）
	UpdateStopInTray func() bool
	// UpdateServer は帯をサーバ版の知らせにするか（looptrack serve が true にする）。true なら帯は role が admin の利用者にだけ出し、
	// 案内は install.sh で入れたサーバの更新の 1 行（updatecheck.ServerUpgradeCommand）と止め方（.env の LOOPTRACK_UPDATE_CHECK=off）。
	// GET /api/v1/dist の server_update（admin のときだけ）にも更新の 1 行を載せる
	UpdateServer bool
	// UpdateApplier は帯の「更新する」ボタン（POST {base}/update/apply）が呼ぶ置き換え。デスクトップ版だけが渡す
	// （localserve.Options.UpdateApplier）。nil ならボタンを出さず、POST {base}/update/apply は 404
	UpdateApplier UpdateApplier
	// AttachDir は添付の本体の置き場（service.Service.AttachDir。looptrack serve は service.AttachDirFromEnv、デスクトップ版は
	// DataDir/attachments）。空なら添付の操作だけが「置き場が設定されていない」で失敗し、サーバはそのまま動く
	AttachDir string
}

// UpdateApplier は新しい版への置き換えを画面の帯から始める部品（デスクトップ版の App が満たす。手順はトレイの
// 「新しい版 <版> に更新する」と同じ: 取得・照合・置き換え・起動し直し・失敗したら戻す）。
type UpdateApplier interface {
	// UpdateReplaceable は知らせている新しい版に 1 クリックで置き換えられるか（false ならボタンを出さない）
	UpdateReplaceable() bool
	// StartUpdate は置き換えを背景で始める。始めたら true（置き換えられない・進行中なら false で何もしない）
	StartUpdate() bool
	// UpdateApplyState は置き換えの状態（進行中か・直近の失敗の版と理由。失敗が無ければ空）
	UpdateApplyState() (running bool, failedVersion, failedReason string)
}

// updateApplyView は帯の「更新する」の表示（layout.html の update_notice が使う）。
type updateApplyView struct {
	Ready                       bool // ボタンを出せる（置き換えられて、進行中でない）
	Running                     bool
	FailedVersion, FailedReason string
}

// セッション・ログイン制限の時間。
const (
	sessionLifetime    = 7 * 24 * time.Hour
	sessionIdleTimeout = 12 * time.Hour
	// persistentLifetime は「ログインしたままにする」のセッションの期限（最終アクセスから数える）。
	// 400 日はブラウザ（Chrome）が Cookie の期限を打ち切る上限で、それより長い Cookie は作れないため。
	persistentLifetime = 400 * 24 * time.Hour
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
	// binds は MCP の呼び出しを会話のセッションに結ぶ合鍵の置き場（session_binds.go）。メモリだけに持つ
	binds *sessionBinds
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
	var srv *Server // distLag が配布ディレクトリを読むのに使う（テンプレートの関数は Server を作る前に決めるため）
	funcs := template.FuncMap{
		"base": func() string { return cfg.BasePath },
		// T は画面の文面を対訳表から出す（{{T .Lang "server.web.…"}}）。
		// 実体は i18n.TFunc。まだ .Lang を詰めていない画面では nil が渡り、対訳表の正本（日本語）で出る。
		"T": i18n.TFunc,
		// TN は件数で単数と複数を分ける文面を出す（{{TN .Lang "server.web.…" (len .Items) "slug" .Slug}}）。実体は i18n.TNFunc。
		"TN": i18n.TNFunc,
		// headData は共通の <head>（layout.html の "head"）へ渡す組。テンプレートは引数を 1 つしか
		// 取れないので、表示の言語と題名をここで束ねる。言語を渡さないと <html lang> と製品名だけが
		// 既定の言語に固定される（画面の本文は .Lang で正しく出るので、見落としやすい）。
		"headData": func(lang any, title string) map[string]any {
			return map[string]any{"Lang": lang, "Title": title}
		},
		// localMode はローカルモードか（ログアウトを出さない）
		"localMode": func() bool { return cfg.LocalMode },
		// updateNotice は共通ヘッダの帯に出す新しい版（無ければ nil で、帯を出さない）
		"updateNotice": func() *updatecheck.Notice {
			if cfg.UpdateNotice == nil {
				return nil
			}
			return cfg.UpdateNotice()
		},
		// updateStopInTray は帯の止め方の案内をトレイのメニューにするか（しないなら環境変数）
		"updateStopInTray": func() bool { return cfg.UpdateStopInTray != nil && cfg.UpdateStopInTray() },
		// updateServer は帯をサーバ版の知らせ（管理者だけ・更新の 1 行）にするか。updateCommand はその 1 行
		"updateServer":  func() bool { return cfg.UpdateServer },
		"updateCommand": func() string { return updatecheck.ServerUpgradeCommand },
		// distLag は管理者の帯に出す、配布物（クライアントに配る looptrack）がサーバの版にそろっていないことの文面（そろっていれば空）
		"distLag": func(lang any) string {
			if srv == nil {
				return ""
			}
			lag := srv.distLagStatus()
			if lag == nil {
				return ""
			}
			return distLagText(templateLang(lang), lag, cfg.DistDir, "")
		},
		// updateApply は帯の「更新する」の表示（デスクトップ版が UpdateApplier を渡したときだけ。サーバ版の帯では nil）
		"updateApply": func() *updateApplyView {
			if cfg.UpdateApplier == nil || cfg.UpdateServer {
				return nil
			}
			v := &updateApplyView{}
			v.Running, v.FailedVersion, v.FailedReason = cfg.UpdateApplier.UpdateApplyState()
			v.Ready = !v.Running && cfg.UpdateApplier.UpdateReplaceable()
			return v
		},
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
	s := &Server{cfg: cfg, db: db, tmpl: tmpl, mux: http.NewServeMux(), svc: service.New(db, cfg.Now), hashSlot: make(chan struct{}, 2),
		binds: newSessionBinds(cfg.Now)}
	s.svc.AttachDir = cfg.AttachDir
	srv = s
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	b := s.cfg.BasePath
	static, _ := fs.Sub(assets, "static")
	s.mux.Handle("GET "+b+"/static/", http.StripPrefix(b+"/static/", http.FileServerFS(static)))
	// favicon。head の link（/static/icon.png）と、ブラウザが直接取りに来る /favicon.ico。どちらも認証は要らない。
	s.mux.HandleFunc("GET "+b+"/static/icon.png", serveIcon("image/png", "static/icon.png"))
	s.mux.HandleFunc("GET "+b+"/favicon.ico", serveIcon("image/x-icon", "static/favicon.ico"))
	s.mux.HandleFunc("GET "+b+"/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.db.PingContext(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if s.cfg.LocalMode {
			// 認証を省いているサーバであることを、認証の要らないこの口で名乗る（DESIGN.md §3-3）。
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
	s.mux.HandleFunc("POST "+b+"/update/apply", s.web(s.updateApply)) // 帯の「更新する」（デスクトップ版だけ。update_apply.go）

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
	// 添付（エビデンスのファイル。attachments_api.go）。この経路だけ本文の上限と締切が違う
	api.HandleFunc("POST "+b+"/api/v1/issues/{id}/attachments", s.apiPostAttachment)
	api.HandleFunc("GET "+b+"/api/v1/issues/{id}/attachments", s.apiListAttachments)
	api.HandleFunc("GET "+b+"/api/v1/attachments/{id}", s.apiGetAttachment)
	api.HandleFunc("POST "+b+"/api/v1/attachments/{id}/purge", s.apiPurgeAttachment) // 本体の消去（管理者だけ）
	// MCP の呼び出しを会話に結ぶ合鍵（PreToolUse の hook が送る・session_binds.go）
	api.HandleFunc("POST "+b+"/api/v1/projects/{slug}/session-binds", s.apiPostSessionBind)
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
	s.mux.HandleFunc("GET "+b+"/admin/attachments", s.adminForbidden(s.adminAttachmentsPage)) // 添付の上限・使用量・消去（管理者以外は 403）
	s.mux.HandleFunc("POST "+b+"/admin/attachments/limits", s.adminForbidden(s.adminAttachmentLimits))
	s.mux.HandleFunc("POST "+b+"/admin/attachments/{id}/purge", s.adminForbidden(s.adminAttachmentPurge))
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
	if !strings.HasPrefix(r.URL.Path, s.cfg.BasePath+"/static/") && r.URL.Path != s.cfg.BasePath+"/favicon.ico" {
		h.Set("Cache-Control", "no-store")
	}
	if s.attachmentPath(r.URL.Path) { // 認証の誤り（401）を含め、添付の経路のどの応答にも付ける（attachments_api.go）
		h.Set("Content-Security-Policy", attachCSP)
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
