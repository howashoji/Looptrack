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

// repair-attachments: 添付の DB（メタデータ）と置き場（本体）の食い違いを調べる。
//
//	looptrack repair-attachments           本体の欠けと、どこからも指されない本体を報告するだけ（dry-run・既定。何も変えない）
//	looptrack repair-attachments --apply   どこからも指されない本体を消す（本体の欠けは直さない。書かれて service.OrphanGrace 以内の本体は消さない）
//
// 置き場は looptrack serve と同じ決め方（service.AttachDirFromEnv）。install.sh の --upgrade の控えから DB だけを戻すと、
// 戻した後の添付の本体が「どこからも指されない本体」として残る。
func repairAttachmentsCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("repair-attachments", flag.ExitOnError)
	apply := fs.Bool("apply", false, i18n.T(lang, "cmd.arg.repair_attachments.apply"))
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return usageErr(i18n.T(lang, "cmd.err.extra_args", "args", strings.Join(fs.Args(), " ")))
	}
	dir := service.AttachDirFromEnv(os.Getenv)
	if dir == "" {
		return fail(i18n.Errorf("cmd.err.no_attach_dir"))
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if err := repairAttachments(context.Background(), db, dir, lang, os.Stdout, *apply); err != nil {
		return fail(err)
	}
	return 0
}

// repairAttachments は置き場 dir と DB を突き合わせて w に報告し、apply ならどこからも指されない本体を消す。
func repairAttachments(ctx context.Context, db *sql.DB, dir string, lang i18n.Lang, w io.Writer, apply bool) error {
	svc := service.New(db, nil)
	svc.AttachDir = dir
	c, err := svc.CheckAttachments(ctx)
	if err != nil {
		return err
	}
	for _, at := range c.Missing {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.missing", "id", at.ID, "slug", at.ProjectSlug, "issue", at.IssueDisplayID,
			"filename", at.Filename, "sha256", at.SHA256))
	}
	for _, o := range c.Orphans {
		if o.Recent {
			fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.orphan_recent", "path", o.Path, "grace", service.OrphanGrace.String()))
		} else {
			fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.orphan", "path", o.Path))
		}
	}
	if !apply {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.total_dry_run", "missing", len(c.Missing), "orphans", len(c.Orphans)))
		return nil
	}
	removed, err := svc.RemoveOrphanBodies(ctx, c)
	for _, p := range removed {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.removed", "path", p))
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(w, i18n.TN(lang, "cmd.repair_attachments.total_applied", len(c.Orphans), "missing", len(c.Missing), "orphans", len(c.Orphans), "removed", len(removed)))
	return nil
}
