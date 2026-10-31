package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
)

// purge-memory: サーバの運用者が、プロジェクト記憶の版の本体を消す（秘密が混ざったと分かったとき。他人の personal の記憶にも届くのはこの経路だけ）。
//
//	looptrack purge-memory --project <slug> --kind <kind> [--name <name>] [--user <login>] --version <n>|--all-versions   消す対象の数と大きさを報告するだけ（既定）
//	… --reason <text> --yes   消す（版の行は残り、本文と要約が無くなる。memory_purges に cause operator で残る。理由は必須）
//
// --user を付けるとその利用者の personal の記憶、付けないとプロジェクトの範囲の記憶を指す。版は --version か --all-versions のどちらかで指す。
func purgeMemoryCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("purge-memory", flag.ExitOnError)
	project := fs.String("project", "", i18n.T(lang, "cmd.arg.purge_memory.project"))
	kind := fs.String("kind", "", i18n.T(lang, "cmd.arg.purge_memory.kind"))
	name := fs.String("name", "", i18n.T(lang, "cmd.arg.purge_memory.name"))
	user := fs.String("user", "", i18n.T(lang, "cmd.arg.purge_memory.user"))
	version := fs.Int64("version", 0, i18n.T(lang, "cmd.arg.purge_memory.version"))
	all := fs.Bool("all-versions", false, i18n.T(lang, "cmd.arg.purge_memory.all_versions"))
	reason := fs.String("reason", "", i18n.T(lang, "cmd.arg.purge_memory.reason"))
	yes := fs.Bool("yes", false, i18n.T(lang, "cmd.arg.purge_memory.yes"))
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return usageErr(i18n.T(lang, "cmd.err.extra_args", "args", strings.Join(fs.Args(), " ")))
	}
	if strings.TrimSpace(*project) == "" || strings.TrimSpace(*kind) == "" {
		return usageErr(i18n.T(lang, "cmd.err.purge_memory.required"))
	}
	if *all == (*version > 0) || *version < 0 {
		return usageErr(i18n.T(lang, "cmd.err.purge_memory.select"))
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	t := service.OperatorMemoryTarget{Project: *project, Owner: *user, Kind: *kind, Name: *name, MemoryPurgeSelect: service.MemoryPurgeSelect{Version: *version, AllVersions: *all}}
	if err := purgeMemory(context.Background(), db, lang, os.Stdout, t, *reason, *yes); err != nil {
		return fail(err)
	}
	return 0
}

// purgeMemory は記憶の消す対象を w に報告し、yes なら消す。報告に出すのは数と大きさだけ（本文と要約は出さない）。
func purgeMemory(ctx context.Context, db *sql.DB, lang i18n.Lang, w io.Writer, t service.OperatorMemoryTarget, reason string, yes bool) error {
	svc := service.New(db, nil)
	var res *service.MemoryPurgeResult
	var err error
	if yes {
		res, err = svc.PurgeOperatorMemory(ctx, t, reason)
	} else {
		res, err = svc.OperatorMemoryPreview(ctx, t)
	}
	if err != nil {
		return err
	}
	args := []any{"project", res.Project, "scope", res.Scope, "kind", res.Kind, "name", res.Name, "bodies", res.Bodies, "bytes", res.Bytes}
	if yes {
		fmt.Fprintln(w, i18n.T(lang, "cmd.purge_memory.applied", args...))
	} else {
		fmt.Fprintln(w, i18n.T(lang, "cmd.purge_memory.dry_run", args...))
	}
	if res.Current {
		fmt.Fprintln(w, i18n.T(lang, "cmd.purge_memory.current_note"))
	}
	return nil
}
