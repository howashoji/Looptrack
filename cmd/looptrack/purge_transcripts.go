package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
)

// purge-transcripts: サーバの運用者が、利用者の会話記録の本体を消す（Web からは他人の会話記録を消せない。運用者のこのサブコマンドだけ）。
//
//	looptrack purge-transcripts --user <login>                       消す対象の件数と大きさを報告するだけ（既定。何も変えない）
//	looptrack purge-transcripts --user <login> --reason <text> --yes 消す（本体が無くなり、transcript_purges に cause operator で残る。理由は必須）
//	looptrack purge-transcripts --user <login> --transcript <id> …   その利用者の会話記録 1 つだけに絞る
//
// 置き場は looptrack serve と同じ決め方（service.TranscriptDirFromEnv）。利用者が居ない・会話記録が 1 つも無いときは終了コード 1。
func purgeTranscriptsCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("purge-transcripts", flag.ExitOnError)
	user := fs.String("user", "", i18n.T(lang, "cmd.arg.purge_transcripts.user"))
	id := fs.Int64("transcript", 0, i18n.T(lang, "cmd.arg.purge_transcripts.transcript"))
	reason := fs.String("reason", "", i18n.T(lang, "cmd.arg.purge_transcripts.reason"))
	yes := fs.Bool("yes", false, i18n.T(lang, "cmd.arg.purge_transcripts.yes"))
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return usageErr(i18n.T(lang, "cmd.err.extra_args", "args", strings.Join(fs.Args(), " ")))
	}
	if strings.TrimSpace(*user) == "" {
		return usageErr(i18n.T(lang, "cmd.err.purge_transcripts.user_required"))
	}
	if *id < 0 {
		return usageErr(i18n.T(lang, "cmd.err.purge_transcripts.transcript_invalid"))
	}
	dir := service.TranscriptDirFromEnv(os.Getenv)
	if dir == "" && *yes {
		return fail(i18n.Errorf("cmd.err.no_transcript_dir"))
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	// 割り込み（Ctrl-C・SIGTERM）でプロセスをすぐには止めず、文脈を取り消す。本体を .tmp へ外してから消すか戻すまで
	// （ファイルの操作）は取り消しで途切れないので、外したまま本体を残さない。続く本体は DB の読み出しが取り消されて止まる
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := purgeTranscripts(ctx, db, service.AttachDirFromEnv(os.Getenv), dir, lang, os.Stdout, *user, *id, *reason, *yes); err != nil {
		return fail(err)
	}
	return 0
}

// purgeTranscripts は利用者 login の会話記録（id が 0 でなければその 1 つ）の消す対象を w に報告し、yes なら消す。
// 報告に出すのは数と大きさだけ（題名・ファイル名・セッション ID は出さない）。dir は消すときだけ使う。
func purgeTranscripts(ctx context.Context, db *sql.DB, attachDir, dir string, lang i18n.Lang, w io.Writer, login string, id int64, reason string, yes bool) error {
	svc := service.New(db, nil)
	svc.AttachDir = attachDir
	svc.TranscriptDir = dir
	list, err := svc.OperatorTranscripts(ctx, login, id)
	if err != nil {
		return err
	}
	var transcripts, chunks, bytes int64
	for _, t := range list {
		if t.LiveChunks > 0 {
			transcripts++
			chunks += t.LiveChunks
			bytes += t.LiveBytes
		}
	}
	if !yes {
		fmt.Fprintln(w, i18n.T(lang, "cmd.purge_transcripts.total_dry_run", "login", login, "transcripts", transcripts, "chunks", chunks, "bytes", bytes))
		return nil
	}
	if service.BlobRootsNested(attachDir, dir) {
		return i18n.Errorf("cmd.err.transcript_dir_nested", "transcript_dir", dir, "attach_dir", attachDir)
	}
	res, err := svc.PurgeOperatorTranscripts(ctx, login, id, reason)
	var removed int
	for _, r := range res {
		removed += r.Removed
		fmt.Fprintln(w, i18n.T(lang, "cmd.purge_transcripts.purged", "transcript", r.TranscriptID, "chunks", len(r.ChunkIDs), "removed", r.Removed))
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(w, i18n.T(lang, "cmd.purge_transcripts.total_applied", "login", login, "transcripts", len(res), "bytes", bytes, "removed", removed))
	return nil
}
