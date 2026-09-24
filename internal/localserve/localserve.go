// Package localserve はローカルモード（127.0.0.1・SQLite）のサーバを同じプロセスの中で動かす部品。
//
// looptrack serve（LOOPTRACK_LOCAL_MODE=1）とデスクトップ版（looptrack の desktop ビルド）が同じ手順を使う:
// 鍵のファイル（<db>.secret-key）→ DB を開く → スキーマを最新にする → server.New → 待ち受け。
// サーバ側のパッケージなので、コマンドを起動しない（TestServerNeverExecutes。ブラウザ・トレイは internal/client/desktop）。
package localserve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/privfile"
	"github.com/howashoji/looptrack/internal/server"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/migrations"
)

// SecretKeySuffix は、ローカルモード + SQLite で作る鍵のファイルの名前（DB のファイル名にこれを足す）。
const SecretKeySuffix = ".secret-key"

// SecretKey は DB（SQLite のファイル）の隣の鍵のファイル（<db>.secret-key）を読み、無ければ作る（本人だけ: unix は 0600、
// Windows は本人だけの ACL。privfile）。DB のディレクトリが無ければ作る（本人だけ。privfile.MkdirAll）。
// 鍵を失うと二段階認証の登録が使えなくなるので、読めない・壊れたファイルは作り直さずにエラーにする。
func SecretKey(dbPath string, logger *slog.Logger) (string, error) {
	path := dbPath + SecretKeySuffix
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		key := strings.TrimSpace(string(raw))
		if _, err := auth.NewBox(key); err != nil {
			return "", fmt.Errorf("鍵のファイル %s: %w", path, err)
		}
		if err := privfile.Check(path); err != nil {
			logger.Warn("鍵のファイルを本人以外も読めます", "path", path, "err", err)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("鍵のファイル %s を読めません: %w", path, err)
	}
	// 新しく作るディレクトリは本人だけ（unix 0700・Windows は中のファイルに継承させる本人だけの ACL。
	// Windows の SQLite の -wal・-shm はディレクトリの ACL を継承するため）
	if err := privfile.MkdirAll(filepath.Dir(path)); err != nil {
		return "", err
	}
	key, err := auth.NewSecretKey()
	if err != nil {
		return "", err
	}
	if err := privfile.WriteFile(path, []byte(key+"\n")); err != nil {
		return "", fmt.Errorf("鍵のファイル %s を作れません: %w", path, err)
	}
	logger.Info("LOOPTRACK_SECRET_KEY が無いため鍵を作りました（失うと二段階認証の登録が使えなくなります。控えてください）", "path", path)
	return key, nil
}

// WarnSQLitePerms は SQLite の DB のファイル（本体・-wal・-shm）が本人以外も読める権限なら警告する。
// 自動では直さない（サービスの利用者とグループで共有している運用を壊さないため）。直し方をログに添える。
// 接続を開いた後に呼ぶ（WAL の -wal・-shm は開いている間だけある）。
func WarnSQLitePerms(dsn string, logger *slog.Logger) {
	path, ok := store.SQLitePath(dsn)
	if !ok {
		return
	}
	broad := store.CheckSQLitePerms(path)
	if len(broad) == 0 {
		return
	}
	fix := "chmod 600 " + shellQuoteAll(broad)
	if runtime.GOOS == "windows" {
		fix = `icacls <ファイル> /inheritance:r /grant:r "%USERNAME%:F"（DB のフォルダも本人だけにすると、作り直される -wal・-shm も本人だけになる）`
	}
	logger.Warn("SQLite の DB のファイルを本人以外も読めます（パスワードのハッシュやイシューの本文が入っています）。本人だけにしてください",
		"files", broad, "fix", fix)
}

// shellQuoteAll はパスを sh に渡せる形（単引用符）で空白区切りに並べる。
func shellQuoteAll(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// Migrate はローカルモードの起動時にスキーマを最新にする（LOOPTRACK_DSN だけで起動する未設定のデスクトップ版にはテーブルが無いため）。
func Migrate(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	applied, err := store.Migrate(ctx, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("マイグレーション: %w", err)
	}
	if len(applied) > 0 {
		logger.Info("migrated", "applied", len(applied), "last", applied[len(applied)-1])
	}
	return nil
}

// Options はデスクトップ版のサーバの設定。
type Options struct {
	DBPath   string       // SQLite のファイル（無ければ作る。ディレクトリも本人だけで作る）
	Listener net.Listener // 待ち受け（127.0.0.1 の TCP。呼び出し側が開く＝ポートの選び方は呼び出し側が決める）
	BasePath string       // 既定 /looptrack
	Logger   *slog.Logger
}

// Instance は動いているサーバ。
type Instance struct {
	DB       *sql.DB
	BasePath string
	srv      *http.Server
	done     chan error
	stopHK   context.CancelFunc
}

// Start は鍵・DB・スキーマを用意して、Listener で待ち受けを始める（戻った時点で要求を受け付ける）。
// 待ち受けのアドレスは 127.0.0.1 / ::1 だけを許す（ローカルモードは認証を省くため。server.CheckLocalListen）。
func Start(ctx context.Context, o Options) (*Instance, error) {
	if o.BasePath == "" {
		o.BasePath = setupwiz.DefaultBasePath
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if o.Listener == nil {
		return nil, errors.New("localserve: Listener がありません")
	}
	if err := server.CheckLocalListen(o.Listener.Addr().String()); err != nil {
		return nil, err
	}
	key, err := SecretKey(o.DBPath, o.Logger)
	if err != nil {
		return nil, err
	}
	box, err := auth.NewBox(key)
	if err != nil {
		return nil, err
	}
	dsn := "sqlite:" + o.DBPath
	db, err := store.Open(dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(ctx, db, o.Logger); err != nil {
		db.Close()
		return nil, err
	}
	WarnSQLitePerms(dsn, o.Logger)
	h, err := server.New(server.Config{
		BasePath:     o.BasePath,
		CookieSecure: false, // http（127.0.0.1）で使う
		Box:          box,
		Logger:       o.Logger,
		LocalMode:    true,
	}, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	hkCtx, stopHK := context.WithCancel(context.Background())
	go server.Housekeeping(hkCtx, db, o.Logger)
	inst := &Instance{DB: db, BasePath: o.BasePath, srv: srv, done: make(chan error, 1), stopHK: stopHK}
	go func() {
		o.Logger.Info("listening", "addr", o.Listener.Addr().String(), "base", o.BasePath, "local_mode", true)
		err := srv.Serve(o.Listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		inst.done <- err
	}()
	return inst, nil
}

// Done は待ち受けが終わったら（Shutdown か異常）値を 1 つ返す。
func (i *Instance) Done() <-chan error { return i.done }

// Shutdown は受け付けを止め、処理中の要求を待ってから DB を閉じる。
func (i *Instance) Shutdown(ctx context.Context) error {
	err := i.srv.Shutdown(ctx)
	i.stopHK()
	if cerr := i.DB.Close(); err == nil {
		err = cerr
	}
	return err
}
