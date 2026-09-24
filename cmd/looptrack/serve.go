package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/localserve"
	"github.com/howashoji/looptrack/internal/relver"
	"github.com/howashoji/looptrack/internal/server"
	"github.com/howashoji/looptrack/internal/store"
)

// serve は HTTP サーバを起動する。設定は環境変数から読む（秘密を引数に取らない）。
//
//	LOOPTRACK_DSN              DB の接続先（必須）。MySQL は user:pass@tcp(host:3306)/im、SQLite は sqlite:<ファイルのパス>
//	LOOPTRACK_SECRET_KEY       TOTP シークレットの暗号化鍵（必須。`looptrack secret-key` で生成）。ローカルモード + SQLite で無ければ
//	                    DB の隣の <db>.secret-key（本人だけ）を読み、無ければ作る
//	LOOPTRACK_LISTEN           待ち受けアドレス（既定 :8090。ローカルモードは既定 127.0.0.1:8090）
//	LOOPTRACK_BASE_PATH        URL の接頭辞（既定 /looptrack。既存の URL・Cookie の Path を保つときは以前の接頭辞を設定する）
//	LOOPTRACK_COOKIE_SECURE    Cookie に Secure を付ける（既定 true。ローカルの http 確認時だけ false。ローカルモードは既定 false）
//	LOOPTRACK_TRUSTED_PROXIES  X-Real-IP を信用する接続元（既定 127.0.0.1/32,::1/128,172.16.0.0/12。ローカルモードは既定なし）
//	LOOPTRACK_PUBLIC_URL       外から見た URL の基点（例 https://example.com）。OAuth のメタデータに使う
//	LOOPTRACK_TOTP_ISSUER      TOTP の発行者名（認証アプリに表示される名前。既定 Looptrack。登録済みの表示を保つときは以前の名前を設定する）
//	LOOPTRACK_DIST_DIR         looptrack の配布ディレクトリ（dist.sh の成果物と SHA256SUMS。GET /api/v1/dist の binaries）
//	LOOPTRACK_CLIENT_MIN_VERSION 対応する looptrack の最低の版（これより古い導入に【配布スクリプトの更新】を出す。空なら判定しない）
//	LOOPTRACK_LOCAL_MODE       1 でローカルモード（DESIGN.md §5-12）。待ち受けは 127.0.0.1 / ::1 / localhost だけ（それ以外は起動しない）。
//	                    Web・REST API・MCP を認証なしで最初の管理者として通す。起動時にスキーマを最新にし、管理者が 0 人なら
//	                    画面に初回設定を出す

// defaultBasePath は URL の接頭辞の既定（LOOPTRACK_BASE_PATH で変える）。
const defaultBasePath = "/looptrack"

func serve() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, addr, err := serveConfig(logger)
	if err != nil {
		return fail(err)
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if err := prepareDB(context.Background(), cfg, db, logger); err != nil {
		return fail(err)
	}
	warnSQLitePerms(os.Getenv("LOOPTRACK_DSN"), logger)
	if !store.IsSQLite(db) { // SQLite の接続の数は store.Open が決める（sqlite::memory: は 1 本でなければ DB が分かれる）
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(4)
		db.SetConnMaxLifetime(30 * time.Minute)
	}

	h, err := server.New(cfg, db)
	if err != nil {
		return fail(err)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go server.Housekeeping(ctx, db, logger)

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr, "base", cfg.BasePath, "version", version, "local_mode", cfg.LocalMode)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return fail(err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return fail(err)
		}
		logger.Info("stopped")
	}
	return 0
}

// serveConfig は serve の設定を環境変数から読む（DB には触れない）。返すのはサーバの設定と待ち受けアドレス。
func serveConfig(logger *slog.Logger) (server.Config, string, error) {
	local := envBool("LOOPTRACK_LOCAL_MODE", false)
	cfg := server.Config{
		BasePath:     envOr("LOOPTRACK_BASE_PATH", defaultBasePath),
		CookieSecure: envBool("LOOPTRACK_COOKIE_SECURE", !local),
		PublicURL:    os.Getenv("LOOPTRACK_PUBLIC_URL"),
		Issuer:       strings.TrimSpace(os.Getenv("LOOPTRACK_TOTP_ISSUER")),
		DistDir:      os.Getenv("LOOPTRACK_DIST_DIR"),
		Logger:       logger,
		LocalMode:    local,
	}
	addr := envOr("LOOPTRACK_LISTEN", ":8090")
	proxies := envOr("LOOPTRACK_TRUSTED_PROXIES", "127.0.0.1/32,::1/128,172.16.0.0/12")
	if local {
		// ローカルモードは認証を省くので、外から届く待ち受けを許さない（0.0.0.0・:8090・LAN のアドレスは起動エラー）
		addr = envOr("LOOPTRACK_LISTEN", "127.0.0.1:8090")
		if err := server.CheckLocalListen(addr); err != nil {
			return cfg, "", i18n.Wrapf(err, "cmd.err.listen")
		}
		proxies = os.Getenv("LOOPTRACK_TRUSTED_PROXIES") // 前段のプロキシは置かない前提（既定は空）
	}
	if v := os.Getenv("LOOPTRACK_REQUIRE_TOTP"); v != "" {
		// 二段階認証の必須 / 任意は DB の設定になった（looptrack settings two-factor・<base path>/admin/security）
		logger.Warn("LOOPTRACK_REQUIRE_TOTP は使われなくなりました（二段階認証の設定は DB に持ちます: looptrack settings two-factor）", "value", v)
	}
	if v := strings.TrimSpace(os.Getenv("LOOPTRACK_CLIENT_MIN_VERSION")); v != "" {
		if !relver.Valid(v) {
			return cfg, "", i18n.Errorf("cmd.err.client_min_version", "value", v)
		}
		cfg.ClientMinVersion = v
	}
	key := os.Getenv("LOOPTRACK_SECRET_KEY")
	if key == "" && local {
		// ローカルモード + SQLite で鍵が無ければ、DB の隣の本人だけのファイルに作って使う（未設定のデスクトップ版）
		if path, ok := store.SQLitePath(os.Getenv("LOOPTRACK_DSN")); ok && path != "" && path != ":memory:" {
			var err error
			if key, err = localSecretKey(path, logger); err != nil {
				return cfg, "", err
			}
		}
	}
	box, err := auth.NewBox(key)
	if err != nil {
		return cfg, "", err
	}
	cfg.Box = box
	for _, s := range strings.Split(proxies, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return cfg, "", fmt.Errorf("LOOPTRACK_TRUSTED_PROXIES: %w", err)
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, p)
	}
	return cfg, addr, nil
}

// secretKeySuffix は、ローカルモード + SQLite で作る鍵のファイルの名前（DB のファイル名にこれを足す）。
const secretKeySuffix = localserve.SecretKeySuffix

// localSecretKey は DB の隣の鍵のファイル（<db>.secret-key）を読み、無ければ作る（本体は localserve.SecretKey。
// デスクトップ版と共通）。
func localSecretKey(dbPath string, logger *slog.Logger) (string, error) {
	return localserve.SecretKey(dbPath, logger)
}

// warnSQLitePerms は SQLite の DB のファイルが本人以外も読める権限なら警告する（本体は localserve.WarnSQLitePerms）。
func warnSQLitePerms(dsn string, logger *slog.Logger) {
	localserve.WarnSQLitePerms(dsn, logger)
}

// prepareDB は起動前の DB の準備。ローカルモードではスキーマを最新にする（LOOPTRACK_DSN だけで起動する未設定のデスクトップ版で
// テーブルが無いため）。チームのサーバは従来どおり looptrack migrate を別に行う。
func prepareDB(ctx context.Context, cfg server.Config, db *sql.DB, logger *slog.Logger) error {
	if !cfg.LocalMode {
		return nil
	}
	return localserve.Migrate(ctx, db, logger)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// healthcheck は自分自身の /healthz を叩く（scratch イメージには curl が無いため、コンテナの healthcheck から使う）。
//
//	LOOPTRACK_HEALTH_URL  既定 http://127.0.0.1:8090<LOOPTRACK_BASE_PATH>/healthz
func healthcheck() int {
	url := os.Getenv("LOOPTRACK_HEALTH_URL")
	if url == "" {
		addr := envOr("LOOPTRACK_LISTEN", ":8090")
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		url = "http://" + addr + envOr("LOOPTRACK_BASE_PATH", defaultBasePath) + "/healthz"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(url)
	if err != nil {
		return fail(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("%s: HTTP %d", url, res.StatusCode))
	}
	return 0
}
