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

// repair-transcripts: 会話記録の DB（メタデータ）と置き場（本体）の食い違いを調べる（repair-attachments と同じ形）。
//
//	looptrack repair-transcripts           本体の欠けと、どこからも指されない本体を報告するだけ（dry-run・既定。何も変えない）
//	looptrack repair-transcripts --apply   どこからも指されない本体を消す（本体の欠けは直さない。書かれて service.OrphanGrace 以内の本体は消さない）
//
// --apply は先に、途中で止まった削除が置き場の .tmp に外したまま残した本体を戻し（本体のパスが空いていれば。空いていなければ捨てる）、
// 落ちた追記が残した古い印を消す。.tmp は本体の検査が見ないので、ここで拾わないと外した本体はどこからも見えない。
//
// 置き場は looptrack serve と同じ決め方（service.TranscriptDirFromEnv）。添付の置き場（service.AttachDirFromEnv）と入れ子なら
// 何もせずに止める（サーバもその設定では会話記録を使えなくする）。
func repairTranscriptsCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("repair-transcripts", flag.ExitOnError)
	apply := fs.Bool("apply", false, i18n.T(lang, "cmd.arg.repair_transcripts.apply"))
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return usageErr(i18n.T(lang, "cmd.err.extra_args", "args", strings.Join(fs.Args(), " ")))
	}
	dir := service.TranscriptDirFromEnv(os.Getenv)
	if dir == "" {
		return fail(i18n.Errorf("cmd.err.no_transcript_dir"))
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if err := repairTranscripts(context.Background(), db, service.AttachDirFromEnv(os.Getenv), dir, lang, os.Stdout, *apply); err != nil {
		return fail(err)
	}
	return 0
}

// repairTranscripts は会話記録の置き場 dir と DB を突き合わせて w に報告し、apply ならどこからも指されない本体を消す。
// attachDir は添付の置き場（入れ子かを確かめるだけで、中は見ない）。
func repairTranscripts(ctx context.Context, db *sql.DB, attachDir, dir string, lang i18n.Lang, w io.Writer, apply bool) error {
	if service.BlobRootsNested(attachDir, dir) {
		return i18n.Errorf("cmd.err.transcript_dir_nested", "transcript_dir", dir, "attach_dir", attachDir)
	}
	svc := service.New(db, nil)
	svc.AttachDir = attachDir
	svc.TranscriptDir = dir
	left, err := svc.RecoverTranscriptLeftovers(ctx, apply)
	if err != nil {
		return err
	}
	switch {
	case left.Asides == 0 && left.StaleMarks == 0:
	case apply:
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_transcripts.leftovers_applied", "asides", left.Asides, "restored", left.Restored, "dropped", left.Dropped, "marks", left.StaleMarks))
	default:
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_transcripts.leftovers_dry_run", "asides", left.Asides, "marks", left.StaleMarks))
	}
	c, err := svc.CheckTranscripts(ctx)
	if err != nil {
		return err
	}
	for _, m := range c.Missing {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_transcripts.missing", "transcript", m.TranscriptID, "seq", m.Seq, "id", m.ChunkID, "sha256", m.SHA256))
	}
	for _, o := range c.Orphans {
		if o.Recent {
			fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.orphan_recent", "path", o.Path, "grace", service.OrphanGrace.String()))
		} else {
			fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.orphan", "path", o.Path))
		}
	}
	if !apply {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_transcripts.total_dry_run", "missing", len(c.Missing), "orphans", len(c.Orphans)))
		return nil
	}
	removed, err := svc.RemoveTranscriptOrphans(ctx, c)
	for _, p := range removed {
		fmt.Fprintln(w, i18n.T(lang, "cmd.repair_attachments.removed", "path", p))
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(w, i18n.TN(lang, "cmd.repair_transcripts.total_applied", len(c.Orphans), "missing", len(c.Missing), "orphans", len(c.Orphans), "removed", len(removed)))
	return nil
}
