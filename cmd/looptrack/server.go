// サーバ側のサブコマンド（docs/server/DESIGN.md）。以前は別の実行ファイルだったサーバを looptrack に統合してある。
//
//	looptrack setup [--dir <dir>] [--yes]  初回立ち上げの対話ウィザード（.env・compose.yaml・最初の管理者。setup.go）
//	looptrack serve [--env-file <path>]   HTTP サーバ（既定 /looptrack。LOOPTRACK_BASE_PATH）を起動する
//	looptrack user|member|token …         利用者・権限・アクセストークンの管理
//	looptrack settings two-factor …       二段階認証の必須 / 任意（settings.go）
//	looptrack project create|list          プロジェクトの作成・一覧（ADD-PROJECT.md）
//	looptrack project rules set|show|clear プロジェクト別ルール
//	looptrack migrate                     DB スキーマを最新にする（接続先は環境変数 LOOPTRACK_DSN）
//	looptrack import --root <dir> [slug…] 旧形式（Markdown）を DB に取り込む（プロジェクト単位で置き換え）
//	looptrack verify --root <dir> [slug…] DB から再生成したファイルが旧形式とバイト一致するか確認する
//	looptrack export --out <dir> [slug…]  DB を旧形式で書き出す（移行時の確認・一時出力用）
//	looptrack verify-files --root <dir>   Markdown の往復一致を検査する（DB 不要）
//	looptrack repair-lists [--apply] [slug…] 空白区切りで 1 要素に入った ID を分割する（repair.go）
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/transfer"
	"github.com/howashoji/looptrack/migrations"
)

// serverCommands はサーバ側のサブコマンド。
// 引数はサブコマンド名の後ろ。返り値は終了コード。
var serverCommands = map[string]func(args []string) int{
	"setup":        setupCmd,
	"serve":        serveCmd,
	"user":         userCmd,
	"member":       memberCmd,
	"token":        tokenCmd,
	"settings":     settingsCmd,
	"project":      projectCmd,
	"healthcheck":  func([]string) int { return healthcheck() },
	"secret-key":   func([]string) int { return secretKeyCmd() },
	"migrate":      func([]string) int { return migrate() },
	"import":       importCmd,
	"verify":       verifyCmd,
	"export":       exportCmd,
	"verify-files": verifyFiles,
	"repair-lists": repairListsCmd,
}

// serverUsage は looptrack -h の「サーバの操作」の節。
func serverUsage(lang i18n.Lang) string { return i18n.T(lang, "cmd.usage.server") }

// cmdLang はサーバ側のサブコマンドが出す文面の言語。looptrack を 1 回動かす間は変わらないので、
// 経路の入口（各サブコマンドと fail・usageErr）で os の環境変数から決める
// （クライアント側は run が env.Env から決めて渡す）。
func cmdLang() i18n.Lang { return i18n.FromEnv(os.Getenv) }

// openDB は環境変数 LOOPTRACK_DSN で接続する（go-sql-driver/mysql 形式、または sqlite:<ファイルのパス>）。DSN は引数に取らない（ps に出さないため）。
func openDB() (*sql.DB, error) {
	dsn := os.Getenv("LOOPTRACK_DSN")
	if dsn == "" {
		return nil, i18n.Errorf("cmd.err.no_dsn")
	}
	db, err := store.Open(dsn)
	if err != nil {
		return nil, err
	}
	return db, db.Ping()
}

func migrate() int {
	lang := cmdLang()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	applied, err := store.Migrate(context.Background(), db, migrations.FS)
	for _, name := range applied {
		fmt.Println(i18n.T(lang, "cmd.migrate.applied", "name", name))
	}
	if err != nil {
		return fail(err)
	}
	if len(applied) == 0 {
		fmt.Println(i18n.T(lang, "cmd.migrate.up_to_date"))
	}
	return 0
}

func verifyFiles(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("verify-files", flag.ExitOnError)
	root := fs.String("root", ".", i18n.T(lang, "cmd.arg.root"))
	_ = fs.Parse(args)

	files, err := mdformat.CollectIssueFiles(*root)
	if err != nil {
		return fail(err)
	}
	var stats mdformat.Stats
	failed := 0
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err == nil {
			var doc *mdformat.Document
			if doc, err = mdformat.Parse(string(raw)); err == nil {
				stats.Add(doc)
				continue
			}
		}
		failed++
		rel, _ := filepath.Rel(*root, path)
		fmt.Fprintf(os.Stderr, "NG %s: %v\n", rel, err)
	}
	fmt.Printf("%s\n%s\n", i18n.T(lang, "cmd.verify_files.result", "ok", len(files)-failed, "total", len(files)), stats)
	if failed > 0 {
		return 1
	}
	return 0
}

func fail(err error) int {
	// 利用者向けの理由は ID を持つ（i18n.Error）。ここで利用者の言語の文面にする
	fmt.Fprintln(os.Stderr, i18n.T(cmdLang(), "cmd.prefix.error", "msg", err))
	return 1
}

func importCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	root := fs.String("root", ".", i18n.T(lang, "cmd.arg.root"))
	_ = fs.Parse(args)
	src, err := transfer.ReadSource(*root, fs.Args())
	if err != nil {
		return fail(err)
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	results, err := transfer.Import(context.Background(), db, src)
	for _, r := range results {
		fmt.Println(i18n.T(lang, "cmd.import.done", "slug", r.Slug, "issues", r.Issues, "comments", r.Comments))
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func verifyCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	root := fs.String("root", ".", i18n.T(lang, "cmd.arg.root"))
	_ = fs.Parse(args)
	src, err := transfer.ReadSource(*root, fs.Args())
	if err != nil {
		return fail(err)
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	reports, err := transfer.Verify(context.Background(), db, src)
	if err != nil {
		return fail(err)
	}
	bad := 0
	for _, r := range reports {
		if len(r.Problems) == 0 {
			fmt.Println(i18n.T(lang, "cmd.verify.match", "slug", r.Slug, "files", r.Files))
			continue
		}
		bad += len(r.Problems)
		fmt.Println(i18n.T(lang, "cmd.verify.mismatch", "slug", r.Slug, "files", r.Files, "problems", len(r.Problems)))
		for _, p := range r.Problems {
			fmt.Printf("  - %s\n", p)
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func exportCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	out := fs.String("out", "", i18n.T(lang, "cmd.arg.export.out"))
	_ = fs.Parse(args)
	if *out == "" {
		return fail(i18n.Errorf("cmd.err.out_required"))
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	n, err := transfer.Export(context.Background(), db, *out, fs.Args())
	if err != nil {
		return fail(err)
	}
	fmt.Println(i18n.T(lang, "cmd.export.done", "count", n, "dir", *out))
	return 0
}
