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
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/privfile"
	"github.com/howashoji/looptrack/internal/server"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/updatecheck"
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
			return "", i18n.Wrapf(err, "localserve.err.key_invalid", "path", path)
		}
		if err := privfile.Check(path); err != nil {
			logger.Warn(i18n.T(i18n.FromEnv(os.Getenv), "localserve.log.key_too_open"), "path", path, "err", err)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", i18n.Wrapf(err, "localserve.err.key_read", "path", path)
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
		return "", i18n.Wrapf(err, "localserve.err.key_write", "path", path)
	}
	logger.Info(i18n.T(i18n.FromEnv(os.Getenv), "localserve.log.key_created"), "path", path)
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
	lang := i18n.FromEnv(os.Getenv) // サーバを起動した人（ログを読む人）の言語
	fix := "chmod 600 " + shellQuoteAll(broad)
	if runtime.GOOS == "windows" {
		fix = i18n.T(lang, "localserve.log.sqlite_fix_windows")
	}
	logger.Warn(i18n.T(lang, "localserve.log.sqlite_too_open"), "files", broad, "fix", fix)
}

// shellQuoteAll はパスを sh に渡せる形（単引用符）で空白区切りに並べる。
func shellQuoteAll(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// BackupDirName は、migrate の前に取る DB の控えを置くディレクトリの名前（DB のファイルと同じディレクトリの下に作る）。
const BackupDirName = "backups"

// BackupKeep は残す控えの数（新しいものから）。
const BackupKeep = 2

// backupStamp は控えの名前に入れる時刻（UTC）の書式。名前の順が作った順になる。
const backupStamp = "20060102T150405Z"

// Migrate はローカルモードの起動時にスキーマを最新にする（LOOPTRACK_DSN だけで起動する未設定のデスクトップ版にはテーブルが無いため）。
//
// dbPath は SQLite のファイル（MySQL・:memory:・空なら控えを取らない）。適用するマイグレーションが 1 本以上あり、
// DB に適用記録が既にある（前の版で使っていた）ときだけ、適用の前に DB の控えを <dbPath のディレクトリ>/backups/ に取り、
// 新しいものから BackupKeep 個を残す。新しい版に置き換えた後で前の版に戻すとき、前の版はこの版が migrate した DB を
// 使わない（store.checkAppliedRecords）ので、控えを DB に戻して使う。控えを取れなければ migrate しない（DB は前の版の形のまま）。
func Migrate(ctx context.Context, db *sql.DB, dbPath string, logger *slog.Logger) error {
	return migrate(ctx, db, dbPath, migrations.FS, time.Now, logger)
}

func migrate(ctx context.Context, db *sql.DB, dbPath string, fsys fs.FS, now func() time.Time, logger *slog.Logger) error {
	if store.IsSQLite(db) && dbPath != "" && dbPath != ":memory:" {
		pending, recorded, err := store.Pending(ctx, db, fsys)
		if err != nil {
			return i18n.Wrapf(err, "localserve.err.migrate")
		}
		if len(pending) > 0 && recorded > 0 {
			path, err := backupSQLite(ctx, db, dbPath, now().UTC())
			if err != nil {
				return err
			}
			logger.Info(i18n.T(i18n.FromEnv(os.Getenv), "localserve.log.backup_created"), "path", path, "pending", len(pending))
			pruneBackups(dbPath, path, logger)
		}
	}
	applied, err := store.Migrate(ctx, db, fsys)
	if err != nil {
		return i18n.Wrapf(err, "localserve.err.migrate")
	}
	if len(applied) > 0 {
		logger.Info("migrated", "applied", len(applied), "last", applied[len(applied)-1])
	}
	return nil
}

// BackupDir は dbPath の控えの置き場（<dbPath のディレクトリ>/backups）。
func BackupDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), BackupDirName)
}

// backupName は控えのファイル名の形（<DB のファイル名>.<UTC の時刻>。同じ秒に 2 つ目を作るときは -2 から後ろに番号を足す）。
func backupName(dbPath string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(filepath.Base(dbPath)) + `\.\d{8}T\d{6}Z(-\d+)?$`)
}

// backupSQLite は DB の控えを BackupDir に作り、そのパスを返す。本人だけのファイルとして作る（DB と同じ。privfile）。
// 途中で失敗したときに半端な控えを残さないよう、.tmp の名前で書いてから名前を変える。
func backupSQLite(ctx context.Context, db *sql.DB, dbPath string, at time.Time) (string, error) {
	dir := BackupDir(dbPath)
	if err := privfile.MkdirAll(dir); err != nil {
		return "", i18n.Wrapf(err, "localserve.err.backup", "path", dir)
	}
	base := filepath.Join(dir, filepath.Base(dbPath)+"."+at.Format(backupStamp))
	final := base
	for n := 2; ; n++ {
		if _, err := os.Lstat(final); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", i18n.Wrapf(err, "localserve.err.backup", "path", final)
		}
		if n > 99 {
			return "", i18n.Wrapf(os.ErrExist, "localserve.err.backup", "path", base)
		}
		final = base + "-" + strconv.Itoa(n)
	}
	tmp := final + ".tmp"
	_ = os.Remove(tmp) // 前に途中で止まった残り
	if _, err := privfile.CreateEmpty(tmp); err != nil {
		return "", i18n.Wrapf(err, "localserve.err.backup", "path", tmp)
	}
	if err := store.BackupSQLite(ctx, db, tmp); err != nil {
		os.Remove(tmp)
		return "", i18n.Wrapf(err, "localserve.err.backup", "path", tmp)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", i18n.Wrapf(err, "localserve.err.backup", "path", final)
	}
	return final, nil
}

// pruneBackups は BackupDir の控えのうち、新しいものから BackupKeep 個（いま作った keep を必ず含む）を残して消す。
// 途中で止まった .tmp も消す。消せなくても起動は止めない（警告だけ）。時計が戻って keep の名前が古い順に並んでも keep は消さない。
func pruneBackups(dbPath, keep string, logger *slog.Logger) {
	dir := BackupDir(dbPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		logger.Warn(i18n.T(i18n.FromEnv(os.Getenv), "localserve.log.backup_prune_failed"), "path", dir, "err", err)
		return
	}
	re := backupName(dbPath)
	var drop, old []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir():
		case strings.HasSuffix(name, ".tmp") && re.MatchString(strings.TrimSuffix(name, ".tmp")):
			drop = append(drop, name) // 途中で止まった残り
		case re.MatchString(name) && name != filepath.Base(keep):
			old = append(old, name)
		}
	}
	sort.Strings(old)
	if extra := len(old) - (BackupKeep - 1); extra > 0 {
		drop = append(drop, old[:extra]...)
	}
	for _, n := range drop {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warn(i18n.T(i18n.FromEnv(os.Getenv), "localserve.log.backup_prune_failed"), "path", filepath.Join(dir, n), "err", err)
		}
	}
}

// Options はデスクトップ版のサーバの設定。
type Options struct {
	DBPath   string       // SQLite のファイル（無ければ作る。ディレクトリも本人だけで作る）
	Listener net.Listener // 待ち受け（127.0.0.1 の TCP。呼び出し側が開く＝ポートの選び方は呼び出し側が決める）
	BasePath string       // 既定 /looptrack
	Logger   *slog.Logger
	// UpdateNotice は画面の共通ヘッダの帯に出す新しい版（server.Config.UpdateNotice。nil なら出さない）
	UpdateNotice func() *updatecheck.Notice
	// UpdateStopInTray は帯の止め方をトレイのメニューで案内するか（server.Config.UpdateStopInTray）
	UpdateStopInTray func() bool
	// UpdateApplier は帯の「更新する」ボタンが呼ぶ置き換え（server.Config.UpdateApplier。nil ならボタンを出さない）
	UpdateApplier server.UpdateApplier
	// AttachDir は添付の本体の置き場（server.Config.AttachDir）。空なら DB と同じディレクトリの attachments
	AttachDir string
	// Version は動いている looptrack の版（server.Config.Version）。利用者メニューの「ガイド」の行き先を決める
	Version string
}

// Instance は動いているサーバ。
type Instance struct {
	DB       *sql.DB
	BasePath string
	srv      *http.Server
	done     chan error
	stopHK   context.CancelFunc
}

// serverConfig は Options からサーバの設定を組む（Start が使う。受け取った値がサーバに渡ることを表で確かめるために切り出した）。
func serverConfig(o Options, box *auth.Box) server.Config {
	return server.Config{
		BasePath:         o.BasePath,
		CookieSecure:     false, // http（127.0.0.1）で使う
		Box:              box,
		Logger:           o.Logger,
		LocalMode:        true,
		UpdateNotice:     o.UpdateNotice,
		UpdateStopInTray: o.UpdateStopInTray,
		UpdateApplier:    o.UpdateApplier,
		AttachDir:        o.AttachDir,
		Version:          o.Version,
	}
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
		return nil, i18n.Errorf("localserve.err.no_listener")
	}
	if o.AttachDir == "" {
		o.AttachDir = filepath.Join(filepath.Dir(o.DBPath), service.AttachDirName)
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
	if err := Migrate(ctx, db, o.DBPath, o.Logger); err != nil {
		db.Close()
		return nil, err
	}
	WarnSQLitePerms(dsn, o.Logger)
	h, err := server.New(serverConfig(o, box), db)
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
