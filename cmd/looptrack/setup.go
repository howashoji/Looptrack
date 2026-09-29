package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/term"

	"github.com/howashoji/looptrack/internal/dbgrants"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/migrations"
)

// looptrack setup — 初回立ち上げの対話ウィザード（本体は internal/setupwiz）。
//
//	looptrack setup [--dir <出力先>]                         対話で①使い方 ②保存先 ③待ち受け ④最初の管理者 ⑤二段階認証 ⑥最初のプロジェクト を聞く
//	looptrack setup --yes --mode team --store mysql …        非対話（秘密は環境変数 LOOPTRACK_SETUP_DSN・LOOPTRACK_SETUP_MIGRATE_DSN・LOOPTRACK_SETUP_ADMIN_PASSWORD）
//	looptrack setup --force                                  設定済みでも作り直す（LOOPTRACK_SECRET_KEY は引き継ぐ）
func setupCmd(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var secret func() (string, error)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		// パスワード入力中に Ctrl-C で抜けても、端末のエコーを戻す
		if st, err := term.GetState(fd); err == nil {
			defer term.Restore(fd, st) //nolint:errcheck
		}
		secret = func() (string, error) {
			b, err := term.ReadPassword(fd)
			return string(b), err
		}
	}
	return runSetup(ctx, args, os.Getenv, os.Stdin, os.Stdout, os.Stderr, secret, setupBackend{}, terminalPrompter)
}

// runSetup は引数を読んでウィザードを動かす（テストから呼ぶ）。
// ask は MySQL の DB がまだ無いときに管理用の資格情報と作るかの確認を尋ねるもの（looptrack grants apply と同じ。nil なら尋ねずに止まる）。
func runSetup(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer,
	secret func() (string, error), backend setupwiz.Backend, ask prompter) int {
	// 画面に出す文面の言語。渡された getenv から決める（os.Getenv を直に読むと、
	// 環境変数を差し替えて動かす呼び出し元とテストで結果が変わる）
	lang := i18n.FromEnv(getenv)
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", i18n.T(lang, "cmd.arg.setup.dir"))
	yes := fs.Bool("yes", false, i18n.T(lang, "cmd.arg.setup.yes"))
	force := fs.Bool("force", false, i18n.T(lang, "cmd.arg.setup.force"))
	mode := fs.String("mode", "", i18n.T(lang, "cmd.arg.setup.mode"))
	service := fs.String("service", "", i18n.T(lang, "cmd.arg.setup.service"))
	storeKind := fs.String("store", "", i18n.T(lang, "cmd.arg.setup.store", "env", setupwiz.EnvDSN))
	sqlitePath := fs.String("sqlite-path", "", i18n.T(lang, "cmd.arg.setup.sqlite_path"))
	port := fs.Int("port", 0, i18n.T(lang, "cmd.arg.setup.port"))
	basePath := fs.String("base-path", "", i18n.T(lang, "cmd.arg.setup.base_path"))
	publicURL := fs.String("public-url", "", i18n.T(lang, "cmd.arg.setup.public_url"))
	login := fs.String("admin-login", "", i18n.T(lang, "cmd.arg.setup.admin_login"))
	name := fs.String("admin-name", "", i18n.T(lang, "cmd.arg.setup.admin_name"))
	pwFile := fs.String("admin-password-file", "", i18n.T(lang, "cmd.arg.setup.admin_password_file", "env", setupwiz.EnvAdminPassword))
	twoFactor := fs.String("two-factor", "", i18n.T(lang, "cmd.arg.setup.two_factor"))
	project := fs.String("project", "", i18n.T(lang, "cmd.arg.setup.project", "default", setupwiz.DefaultLocalProject))
	projectPrefix := fs.String("project-prefix", "", i18n.T(lang, "cmd.arg.setup.project_prefix"))
	projectName := fs.String("project-name", "", i18n.T(lang, "cmd.arg.setup.project_name"))
	fs.Usage = func() {
		fmt.Fprintln(stderr, i18n.T(lang, "cmd.usage.setup", "dsn", setupwiz.EnvDSN, "migrate_dsn", setupwiz.EnvMigrateDSN, "password", setupwiz.EnvAdminPassword))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, i18n.T(lang, "cmd.prefix.error", "msg", i18n.T(lang, "cmd.err.extra_args", "args", strings.Join(fs.Args(), " "))))
		return 2
	}
	pre := setupwiz.Preset{
		Mode: *mode, Service: *service, Store: *storeKind, SQLitePath: *sqlitePath, Port: *port, BasePath: *basePath, PublicURL: *publicURL,
		AdminLogin: *login, AdminName: *name, TwoFactor: *twoFactor,
		ProjectSlug: *project, ProjectPrefix: *projectPrefix, ProjectName: *projectName,
		DSN: getenv(setupwiz.EnvDSN), MigrateDSN: getenv(setupwiz.EnvMigrateDSN), AdminPassword: getenv(setupwiz.EnvAdminPassword),
	}
	in := bufio.NewReader(stdin)
	if *pwFile != "" {
		var raw string
		if *pwFile == "-" {
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				fmt.Fprintln(stderr, i18n.T(lang, "cmd.prefix.error", "msg", i18n.T(lang, "cmd.err.password_stdin")))
				return 1
			}
			raw = line
		} else {
			b, err := os.ReadFile(*pwFile)
			if err != nil {
				return failTo(stderr, lang, err)
			}
			raw = string(b)
		}
		pre.AdminPassword = strings.TrimRight(strings.SplitN(raw, "\n", 2)[0], "\r")
	}
	// compose のイメージ（scratch）は linux なので、載せられるのは linux で動いているときの自分自身だけ。
	// macOS・Windows では置かず、ウィザードが「linux の実行ファイルを置く」案内を出す。
	var linuxBinary string
	if runtime.GOOS == "linux" {
		if self, err := os.Executable(); err == nil {
			linuxBinary = self
		}
	}
	var prepare func(context.Context, string, string) error
	if ask != nil {
		// DB がまだ無いときは、looptrack grants apply と同じ処理で DB とアプリ用の利用者を作り、表を作って権限を与える。
		// 作るかの確認は --yes でも省かない（--yes は setup の問いに答えないことで、DB を作ることへの同意ではない）
		prepare = func(ctx context.Context, dsn, setupDSN string) error {
			cfg, err := store.NormalizeDSN(dsn)
			if err != nil {
				return err
			}
			// 管理用の接続と、アプリ用の利用者で読めることの確認は、DB が無いと分かった接続先（setupDSN）の宛先で行う。
			// アプリ用の接続先の宛先は、サービスから見たもの（compose の mysql:3306 など）で、ここからは届かないことがある
			if setupDSN != "" && setupDSN != dsn {
				scfg, err := store.NormalizeDSN(setupDSN)
				if err != nil {
					return err
				}
				cfg.Net, cfg.Addr = scfg.Net, scfg.Addr
			}
			err = applyGrants(ctx, grantsApplyOptions{target: cfg, appDSN: cfg.FormatDSN()}, stdout, lang, ask)
			var ie *i18n.Error
			if errors.As(err, &ie) && ie.Msg.ID == "cmd.err.grants_no_terminal" && dbgrants.ValidDBName(cfg.DBName) {
				// 端末が無い（自動化）。setup には管理用の資格情報を渡す引数が無いので、先に作る方法を示す
				return i18n.Errorf("cmd.err.setup_no_db_no_terminal", "db", cfg.DBName, "sql", dbgrants.CreateDatabaseSQL(cfg.DBName))
			}
			return err
		}
	}
	_, err := setupwiz.Run(ctx, setupwiz.Options{
		Dir: *dir, Yes: *yes, Force: *force, In: in, Out: stdout, ReadSecret: secret, Preset: pre, Backend: backend,
		PrepareDatabase: prepare, LinuxBinary: linuxBinary, Lang: lang,
	})
	switch {
	case errors.Is(err, setupwiz.ErrInterrupted):
		fmt.Fprintf(stderr, "\n%s\n", i18n.Text(lang, err))
		return 130
	case err != nil:
		return failTo(stderr, lang, err)
	}
	return 0
}

func failTo(w io.Writer, lang i18n.Lang, err error) int {
	// setupwiz が返す利用者向けの理由は ID を持つ（i18n.Error）。ここで利用者の言語の文面にする
	fmt.Fprintln(w, i18n.T(lang, "cmd.prefix.error", "msg", err))
	return 1
}

// setupBackend は setup の DB 操作（store と user add の処理を使う）。
type setupBackend struct{}

// open は dsn で繋ぐ。sqlite:<パス> は store.Open が SQLite に対応していれば開き、対応していなければわかりやすいエラーにする。
func (setupBackend) open(ctx context.Context, dsn string) (*sql.DB, error) {
	sqlite := strings.HasPrefix(dsn, "sqlite:")
	db, err := store.Open(dsn)
	if err == nil && sqlite {
		if _, isMySQL := db.Driver().(*mysql.MySQLDriver); isMySQL {
			// store.Open が sqlite: を MySQL の DSN として読んだ（SQLite に未対応の looptrack）
			db.Close()
			err = i18n.Errorf("cmd.err.sqlite_unsupported")
		}
	}
	if err == nil {
		if err = db.PingContext(ctx); err != nil {
			db.Close()
		}
	}
	if err != nil {
		if sqlite {
			return nil, i18n.Wrapf(err, "cmd.err.sqlite_open", "path", strings.TrimPrefix(dsn, "sqlite:"))
		}
		return nil, err
	}
	return db, nil
}

func (b setupBackend) Inspect(ctx context.Context, dsn string) (int, error) {
	db, err := b.open(ctx, dsn)
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1049 { // ER_BAD_DB_ERROR: 接続先の DB が無い
			return 0, &setupwiz.NoDatabaseError{Err: err}
		}
		return 0, err
	}
	defer db.Close()
	n, err := store.CountUsers(ctx, db)
	if err != nil && isNoTable(err) {
		return 0, nil // マイグレーション前
	}
	return n, err
}

func isNoTable(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1146 {
		return true
	}
	return strings.Contains(err.Error(), "no such table")
}

func (b setupBackend) Migrate(ctx context.Context, dsn string) ([]string, error) {
	db, err := b.open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return store.Migrate(ctx, db, migrations.FS)
}

// CreateAdmin は最初の管理者・二段階認証の設定・最初のプロジェクトを setupwiz.Provision で 1 つのトランザクションで作る
// （画面版の初回設定と同じ関数。失敗したら何も残さない）。利用者がすでにいる（--force で作り直す）ときは、
// 同じログイン名がいればそのまま残し、いなければ管理者として足す。二段階認証の変更は settings two-factor と同じく記録する。
// lang は setup の画面の言語。結果の行と、二段階認証の変更記録の備考をこの言語で書く。
func (b setupBackend) CreateAdmin(ctx context.Context, dsn string, a setupwiz.Admin, p setupwiz.FirstProject, lang i18n.Lang) (string, error) {
	db, err := b.open(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer db.Close()
	return setupwiz.Provision(ctx, db, a, p, setupwiz.ProvisionOptions{Via: "command", Source: "looptrack setup", Note: "looptrack setup --force", Lang: lang})
}
