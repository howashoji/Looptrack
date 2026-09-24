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
	"github.com/howashoji/looptrack/internal/store"
)

// repair-lists: ID の項目（blocked_by / traces / refs）に空白区切りで 1 要素として入った値を分割する。
//
//	looptrack repair-lists [slug…]                         対象と差分を表示するだけ（dry-run・既定）
//	looptrack repair-lists --apply [slug…]                 補正する（issue_events に kind repair_lists・via admin で残る）
//	looptrack repair-lists --fields refs,traces [slug…]    項目を限る（既定 blocked_by,traces,refs。labels も指定できる）
//
// slug を省略すると全プロジェクト。クローズ済みも対象（DESIGN.md §3「既存データの補正」）。
func repairListsCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("repair-lists", flag.ExitOnError)
	apply := fs.Bool("apply", false, i18n.T(lang, "cmd.arg.repair.apply"))
	fields := fs.String("fields", "blocked_by,traces,refs", i18n.T(lang, "cmd.arg.repair.fields"))
	// 既定の理由は issue_events に残る記録なので訳さない（記録が動かした人の言語で変わらないようにする）
	reason := fs.String("reason", "空白区切りで 1 要素に入った ID を分割", i18n.T(lang, "cmd.arg.repair.reason"))
	var slugs []string
	for len(args) > 0 { // フラグと slug の順序を問わない
		_ = fs.Parse(args)
		if fs.NArg() == 0 {
			break
		}
		slugs = append(slugs, fs.Arg(0))
		args = fs.Args()[1:]
	}
	fl := strings.Split(*fields, ",")
	if err := service.ValidateRepairFields(fl); err != nil {
		return fail(err)
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if err := repairLists(context.Background(), db, lang, os.Stdout, slugs, fl, *apply, *reason); err != nil {
		return fail(err)
	}
	return 0
}

// repairLists は slugs（空なら全プロジェクト）の対象を w に表示し、apply なら補正する。
func repairLists(ctx context.Context, db *sql.DB, lang i18n.Lang, w io.Writer, slugs, fields []string, apply bool, reason string) error {
	var projects []store.Project
	if len(slugs) == 0 {
		ps, err := store.ListProjects(ctx, db)
		if err != nil {
			return err
		}
		projects = ps
	} else {
		for _, s := range slugs {
			p, err := store.ProjectBySlug(ctx, db, s)
			if err != nil {
				return i18n.Wrapf(err, "cmd.err.project", "slug", s)
			}
			projects = append(projects, p)
		}
	}
	svc := service.New(db, nil)
	total := 0
	for _, p := range projects {
		var list []service.IssueRepair
		var err error
		if apply {
			list, err = svc.RepairLists(ctx, service.Actor{Via: "admin"}, p, fields, reason)
		} else {
			list, err = svc.SpacedLists(ctx, p, fields)
		}
		for _, r := range list {
			for _, c := range r.Changes {
				fmt.Fprintf(w, "%s %s (%s) %s: [%s] → [%s]\n", p.Slug, r.ID, r.Status, c.Field,
					strings.Join(c.Before, ", "), strings.Join(c.After, ", "))
			}
		}
		if err != nil {
			return i18n.Wrapf(err, "cmd.repair.partial", "slug", p.Slug, "count", len(list))
		}
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair.count", "slug", p.Slug, "count", len(list)))
		total += len(list)
	}
	if apply {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair.total_applied", "count", total, "kind", service.RepairKind))
	} else {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair.total_dry_run", "count", total))
	}
	return nil
}
