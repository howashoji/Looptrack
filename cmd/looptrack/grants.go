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
	"strings"
	"syscall"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/term"

	"github.com/howashoji/looptrack/internal/dbgrants"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/migrations"
)

// looptrack grants — MySQL のアプリ用の利用者に最小権限（deploy/grants.sql）を与える。
//
//	looptrack grants print [--db <名前>] [--user <名前>]   GRANT 文を出す（既定は LOOPTRACK_DSN の DB 名・利用者名）
//	looptrack grants apply [--admin-user <名前>] [--admin-password-file <パス>] [--yes]
//	    管理用の資格情報を端末から尋ね（表示しない・保存しない）、DB とアプリ用の利用者が無ければ確かめてから作り、
//	    表が無ければ作り（migrate）、GRANT を流し、アプリ用の利用者（LOOPTRACK_DSN）で全部の表の権限がそろったことを確かめる
//	looptrack grants check
//	    アプリ用の利用者（LOOPTRACK_DSN）に、grants.sql が挙げる全部の表の権限があるかを確かめる。
//	    そろっていれば 0、足りなければ足りない表と権限を出して 3（migrate --check の「未適用あり」と同じ値）。
//	    インストーラは migrate の後にこれを呼び、3 なら管理用の資格情報を尋ねて与え直す（表が増えた更新でも気づくため）
//
// 表ごとの GRANT は表ができてからしか流せない（DB 単位で与えると表単位で取り消せない）ので、インストーラは
// setup（表を作る）の後にこれを呼ぶ。管理用の資格情報は接続にだけ使い、ファイル・環境変数・引数に残さない。
func grantsCmd(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runGrants(ctx, args, os.Getenv, os.Stdout, os.Stderr, terminalPrompter)
}

// prompter は端末から 1 行（secret なら表示せずに）読む。端末が無ければ ok が false。
type prompter func(lang i18n.Lang, prompt string, secret bool) (answer string, ok bool, err error)

// terminalPrompter は制御端末（/dev/tty。無ければ端末である標準入力）から読む。curl … | sh でも尋ねられるようにするため。
func terminalPrompter(lang i18n.Lang, prompt string, secret bool) (string, bool, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", false, nil
		}
		f = os.Stdin
	} else {
		defer f.Close()
	}
	fmt.Fprint(os.Stderr, prompt)
	if secret {
		fd := int(f.Fd())
		// 入力中に Ctrl-C で抜けても端末のエコーを戻す
		if st, err := term.GetState(fd); err == nil {
			defer term.Restore(fd, st) //nolint:errcheck
		}
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), true, err
	}
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return "", true, err
	}
	return strings.TrimRight(line, "\r\n"), true, nil
}

func runGrants(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, ask prompter) int {
	lang := i18n.FromEnv(getenv)
	usage := func() { fmt.Fprintln(stderr, i18n.T(lang, "cmd.usage.grants")) }
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "print":
		return grantsPrint(args[1:], getenv, stdout, stderr, lang)
	case "apply":
		return grantsApply(ctx, args[1:], getenv, stdout, stderr, lang, ask)
	case "check":
		return grantsCheck(ctx, args[1:], getenv, stdout, stderr, lang)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, i18n.T(lang, "cmd.usage.grants"))
		return 0
	}
	usage()
	return 2
}

// grantsTarget は LOOPTRACK_DSN から MySQL の接続先を読む（SQLite なら nil）。
func grantsTarget(getenv func(string) string) (*mysql.Config, error) {
	dsn := getenv("LOOPTRACK_DSN")
	if dsn == "" {
		return nil, i18n.Errorf("cmd.err.no_dsn")
	}
	if _, ok := store.SQLitePath(dsn); ok {
		return nil, nil
	}
	return store.NormalizeDSN(dsn)
}

func grantsPrint(args []string, getenv func(string) string, stdout, stderr io.Writer, lang i18n.Lang) int {
	fs := flag.NewFlagSet("grants print", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", i18n.T(lang, "cmd.arg.grants.db"))
	user := fs.String("user", "", i18n.T(lang, "cmd.arg.grants.user"))
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *db == "" || *user == "" {
		if getenv("LOOPTRACK_DSN") != "" {
			cfg, err := grantsTarget(getenv)
			if err != nil {
				return failTo(stderr, lang, err)
			}
			if cfg == nil {
				fmt.Fprintln(stderr, i18n.T(lang, "cmd.grants.sqlite"))
				return 0
			}
			if *db == "" {
				*db = cfg.DBName
			}
			if *user == "" {
				*user = cfg.User
			}
		}
		if *db == "" {
			*db = "im"
		}
		if *user == "" {
			*user = "im_app"
		}
	}
	stmts, err := dbgrants.Statements(*db, *user)
	if err != nil {
		return failTo(stderr, lang, err)
	}
	fmt.Fprintln(stdout, strings.Join(stmts, "\n"))
	return 0
}

func grantsApply(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, lang i18n.Lang, ask prompter) int {
	fs := flag.NewFlagSet("grants apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	adminUser := fs.String("admin-user", "", i18n.T(lang, "cmd.arg.grants.admin_user"))
	pwFile := fs.String("admin-password-file", "", i18n.T(lang, "cmd.arg.grants.admin_password_file"))
	yes := fs.Bool("yes", false, i18n.T(lang, "cmd.arg.grants.yes"))
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, err := grantsTarget(getenv)
	if err != nil {
		return failTo(stderr, lang, err)
	}
	if cfg == nil {
		fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.sqlite"))
		return 0
	}
	o := grantsApplyOptions{target: cfg, appDSN: getenv("LOOPTRACK_DSN"), adminUser: *adminUser, pwFile: *pwFile, yes: *yes}
	if err := applyGrants(ctx, o, stdout, lang, ask); err != nil {
		return failTo(stderr, lang, err)
	}
	return 0
}

// checkGrants は権限の確かめ（looptrack grants check と、grants apply の最後の確かめ）。テストが差し替えて、
// apply の最後の確かめがこれを通っていることを確かめる。
var checkGrants = dbgrants.Check

// grantsCheckMissing は looptrack grants check が「権限が足りない」ときの終了コード（接続できないなどの誤りの 1 と分ける）。
const grantsCheckMissing = 3

func grantsCheck(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, lang i18n.Lang) int {
	fs := flag.NewFlagSet("grants check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, err := grantsTarget(getenv)
	if err != nil {
		return failTo(stderr, lang, err)
	}
	if cfg == nil {
		fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.sqlite"))
		return 0
	}
	app, err := store.Open(getenv("LOOPTRACK_DSN"))
	if err != nil {
		return failTo(stderr, lang, err)
	}
	defer app.Close()
	missing, err := checkGrants(ctx, app)
	if err != nil {
		return failTo(stderr, lang, i18n.Errorf("cmd.err.grants_check", "reason", err))
	}
	if len(missing) > 0 {
		fmt.Fprintln(stderr, i18n.T(lang, "cmd.grants.check_missing", "user", cfg.User, "missing", dbgrants.FormatMissing(missing)))
		return grantsCheckMissing
	}
	fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.verified", "user", cfg.User))
	return 0
}

// grantsApplyOptions は applyGrants の設定。
type grantsApplyOptions struct {
	target    *mysql.Config // アプリ用の接続先（appDSN を読んだもの）
	appDSN    string        // アプリ用の接続先（LOOPTRACK_DSN の形）
	adminUser string        // 空なら尋ねる
	pwFile    string        // 空なら尋ねる（表示しない）
	yes       bool          // DB・アプリ用の利用者を作る前の確認を省く
}

// applyGrants は looptrack grants apply の中身。管理用の資格情報を尋ね（表示しない・保存しない）、DB とアプリ用の利用者が
// 無ければ確かめてから作り、表が無ければ作り（migrate）、GRANT を流し、アプリ用の利用者で全部の表の権限がそろったことを
// 確かめる（looptrack grants check と同じ dbgrants.Check）。
// looptrack grants apply と、DB がまだ無いときの looptrack setup（setupwiz.Options.PrepareDatabase）の両方がこれを呼ぶ
// （DB・利用者を作るかの判断と作り方を 1 か所に置くため）。
func applyGrants(ctx context.Context, o grantsApplyOptions, stdout io.Writer, lang i18n.Lang, ask prompter) error {
	cfg := o.target
	stmts, err := dbgrants.Statements(cfg.DBName, cfg.User)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.target", "addr", cfg.Addr, "db", cfg.DBName, "user", cfg.User))

	// 管理用の資格情報（接続にだけ使う。表示しない・保存しない）
	adminUser := o.adminUser
	if adminUser == "" {
		ans, ok, err := ask(lang, i18n.T(lang, "cmd.grants.ask_admin_user", "default", "root"), false)
		if err != nil {
			return err
		}
		if !ok {
			return i18n.Errorf("cmd.err.grants_no_terminal")
		}
		adminUser = strings.TrimSpace(ans)
		if adminUser == "" {
			adminUser = "root"
		}
	}
	var adminPW string
	if o.pwFile != "" {
		b, err := os.ReadFile(o.pwFile)
		if err != nil {
			return err
		}
		adminPW = strings.TrimRight(strings.SplitN(string(b), "\n", 2)[0], "\r")
	} else {
		pw, ok, err := ask(lang, i18n.T(lang, "cmd.grants.ask_admin_password", "user", adminUser), true)
		if err != nil {
			return err
		}
		if !ok {
			return i18n.Errorf("cmd.err.grants_no_terminal")
		}
		adminPW = pw
	}
	confirm := func(prompt string) (bool, error) {
		if o.yes {
			return true, nil
		}
		ans, ok, err := ask(lang, prompt, false)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, i18n.Errorf("cmd.err.grants_no_terminal")
		}
		switch strings.ToLower(strings.TrimSpace(ans)) {
		case "", "y", "yes":
			return true, nil
		}
		return false, nil
	}

	acfg := cfg.Clone()
	acfg.User, acfg.Passwd, acfg.DBName = adminUser, adminPW, ""
	admin, err := sql.Open("mysql", acfg.FormatDSN())
	if err != nil {
		return err
	}
	defer admin.Close()
	admin.SetMaxOpenConns(1)
	if err := admin.PingContext(ctx); err != nil {
		return i18n.Errorf("cmd.err.grants_admin_connect", "addr", cfg.Addr, "user", adminUser, "reason", err)
	}

	ok, err := dbgrants.DatabaseExists(ctx, admin, cfg.DBName)
	if err != nil {
		return err
	}
	if !ok {
		c, err := confirm(i18n.T(lang, "cmd.grants.confirm_create_db", "db", cfg.DBName))
		if err != nil {
			return err
		}
		if !c {
			// 作らずに止める。自分で作るときに流す文を添える（何をすればよいかが分かるように）
			return i18n.Errorf("cmd.err.grants_db_declined", "db", cfg.DBName, "sql", dbgrants.CreateDatabaseSQL(cfg.DBName))
		}
		if err := dbgrants.CreateDatabase(ctx, admin, cfg.DBName); err != nil {
			return err
		}
		fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.created_db", "db", cfg.DBName))
	}
	ok, err = dbgrants.UserExists(ctx, admin, cfg.User)
	if err != nil {
		return err
	}
	if !ok {
		c, err := confirm(i18n.T(lang, "cmd.grants.confirm_create_user", "user", cfg.User))
		if err != nil {
			return err
		}
		if !c {
			return i18n.Errorf("cmd.err.grants_declined")
		}
		if err := dbgrants.CreateUser(ctx, admin, cfg.User, cfg.Passwd); err != nil {
			return err
		}
		fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.created_user", "user", cfg.User))
	}
	ok, err = dbgrants.TablesExist(ctx, admin, cfg.DBName)
	if err != nil {
		return err
	}
	if !ok {
		// 表ごとの GRANT は表ができてからしか流せない。setup の前に呼ばれたときは、管理用の資格情報で表を作る
		fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.migrate"))
		mcfg := acfg.Clone()
		mcfg.DBName = cfg.DBName
		mdb, err := store.Open(mcfg.FormatDSN())
		if err != nil {
			return err
		}
		applied, err := store.Migrate(ctx, mdb, migrations.FS)
		mdb.Close()
		for _, name := range applied {
			fmt.Fprintln(stdout, i18n.T(lang, "cmd.migrate.applied", "name", name))
		}
		if err != nil {
			return err
		}
	}
	if err := dbgrants.Apply(ctx, admin, stmts); err != nil {
		return err
	}
	fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.applied", "n", len(stmts)))

	// アプリ用の利用者（LOOPTRACK_DSN）で、全部の表の権限がそろったこと（looptrack grants check と同じ確かめ）
	app, err := store.Open(o.appDSN)
	if err != nil {
		return err
	}
	defer app.Close()
	missing, err := checkGrants(ctx, app)
	if err != nil {
		return i18n.Errorf("cmd.err.grants_verify", "reason", err)
	}
	if len(missing) > 0 {
		return i18n.Errorf("cmd.err.grants_verify", "reason", dbgrants.FormatMissing(missing))
	}
	fmt.Fprintln(stdout, i18n.T(lang, "cmd.grants.verified", "user", cfg.User))
	return nil
}
