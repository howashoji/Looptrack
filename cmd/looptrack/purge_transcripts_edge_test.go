package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 置き場が添付の置き場と入れ子なら、消す操作（--yes）は何も消さずに止める（どちらの向きも・同じ場所も）。
// dry-run は消さないので止めずに数を報告する。対照: 別の場所なら消える。
func TestPurgeTranscriptsNestedRefusesOnlyWhenDeleting(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	base := t.TempDir()
	f := newPurgeFixture(t, admin, filepath.Join(base, "transcripts"))
	all := append(append([]string{}, f.edBodies...), f.boBody...)
	nested := []struct{ attach, transcripts string }{
		{base, f.dir},                        // 会話記録が添付の中
		{filepath.Join(f.dir, "att"), f.dir}, // 添付が会話記録の中
		{f.dir, f.dir},                       // 同じ場所
	}
	for _, c := range nested {
		var out bytes.Buffer
		err := purgeTranscripts(ctx, app, c.attach, c.transcripts, i18n.JA, &out, "ed", 0, "r", true)
		if txt := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(txt, "一方がもう一方の中にある") || !strings.Contains(txt, c.transcripts) {
			t.Errorf("添付 %q・会話記録 %q: err = %q", c.attach, c.transcripts, txt)
		}
		if out.Len() != 0 {
			t.Errorf("止めたのに出力した: %s", out.String())
		}
		f.exists(t, "入れ子で止めた後", true, all...)
		if n, _, _, _, _ := purgeRows(t, admin); n != 0 {
			t.Errorf("止めたのに transcript_purges に %d 行", n)
		}
		out.Reset()
		if err := purgeTranscripts(ctx, app, c.attach, c.transcripts, i18n.JA, &out, "ed", 0, "", false); err != nil || !strings.Contains(out.String(), "dry-run") {
			t.Errorf("入れ子でも dry-run は報告する（消さない）: %q %v", out.String(), err)
		}
	}
	// 対照: 入れ子でなければ消える（止めたのが入れ子の判定であって、ほかの理由ではない）
	var out bytes.Buffer
	if err := purgeTranscripts(ctx, app, filepath.Join(t.TempDir(), "att"), f.dir, i18n.JA, &out, "ed", 0, "r", true); err != nil {
		t.Fatalf("前提が崩れています: 入れ子でないのに消せない: %v", err)
	}
	f.exists(t, "対照", false, f.edBodies...)
	f.exists(t, "対照: 別の利用者", true, f.boBody...)
}

// コマンドとしては、入れ子の --yes は終了コード 1（標準エラーに文面・標準出力は空）、dry-run は 0。
func TestPurgeTranscriptsNestedExitCode(t *testing.T) {
	dir := t.TempDir()
	dsn := "sqlite:" + filepath.Join(dir, "p.db")
	t.Setenv("LOOPTRACK_DSN", dsn)
	t.Setenv("LOOPTRACK_LANG", "ja")
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("LOOPTRACK_ATTACH_DIR", filepath.Join(dir, "att"))
	t.Setenv("LOOPTRACK_TRANSCRIPT_DIR", filepath.Join(dir, "att", "transcripts"))
	if code, out, errOut := captureCmd(t, migrateCmd); code != 0 {
		t.Fatalf("migrate: %d\n%s%s", code, out, errOut)
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f := newPurgeFixture(t, db, filepath.Join(dir, "att", "transcripts"))

	code, out, errOut := captureCmd(t, purgeTranscriptsCmd, "--user", "ed", "--reason", "x", "--yes")
	if code != 1 || out != "" || !strings.Contains(errOut, "一方がもう一方の中にある") || !strings.Contains(errOut, f.dir) {
		t.Errorf("入れ子の --yes: 終了コード %d・stdout %q・stderr %q", code, out, errOut)
	}
	f.exists(t, "入れ子の --yes の後", true, f.edBodies...)
	if code, out, errOut = captureCmd(t, purgeTranscriptsCmd, "--user", "ed"); code != 0 || !strings.Contains(out, "dry-run") || errOut != "" {
		t.Errorf("入れ子の dry-run: %d %q %q", code, out, errOut)
	}
}

// 置き場が決まらないとき（LOOPTRACK_TRANSCRIPT_DIR・STATE_DIRECTORY・SQLite の DSN のどれも無い）。
// dry-run は置き場を使わないので、数を報告して終わる（拒まない）。--yes は何も消さずに止める: コマンドは DB を開く前に
// 「置き場が決まりません」で終了コード 1、関数は「会話記録の置き場が設定されていません」相当の拒否。
func TestPurgeTranscriptsNoStore(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	f := newPurgeFixture(t, admin, filepath.Join(t.TempDir(), "transcripts"))
	all := append(append([]string{}, f.edBodies...), f.boBody...)

	var out bytes.Buffer
	if err := purgeTranscripts(ctx, app, "", "", i18n.JA, &out, "ed", 0, "", false); err != nil ||
		!strings.Contains(out.String(), "会話記録 2・消去していないチャンク 3・") || !strings.Contains(out.String(), "dry-run") {
		t.Errorf("置き場なしの dry-run: %q %v", out.String(), err)
	}
	out.Reset()
	err := purgeTranscripts(ctx, app, "", "", i18n.JA, &out, "ed", 0, "r", true)
	if want := i18n.T(i18n.JA, "service.err.transcript.unavailable"); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), want) {
		t.Errorf("置き場なしの --yes: err = %q（%q を含むはず）", i18n.Text(i18n.JA, err), want)
	}
	if out.Len() != 0 {
		t.Errorf("止めたのに出力した: %s", out.String())
	}
	f.exists(t, "置き場なし", true, all...)
	if n, _, _, _, _ := purgeRows(t, admin); n != 0 {
		t.Errorf("止めたのに transcript_purges に %d 行", n)
	}

	// コマンド: 置き場が決まらない設定（SQLite でない DSN）の --yes は、DB を開く前に終了コード 1
	t.Setenv("LOOPTRACK_DSN", "u:p@tcp(127.0.0.1:1)/im")
	t.Setenv("LOOPTRACK_LANG", "ja")
	t.Setenv("LOOPTRACK_ATTACH_DIR", "")
	t.Setenv("LOOPTRACK_TRANSCRIPT_DIR", "")
	t.Setenv("STATE_DIRECTORY", "")
	if service.TranscriptDirFromEnv(func(k string) string { return map[string]string{"LOOPTRACK_DSN": "u:p@tcp(127.0.0.1:1)/im"}[k] }) != "" {
		t.Fatal("前提が崩れています: この設定でも置き場が決まります")
	}
	code, stdout, stderr := captureCmd(t, purgeTranscriptsCmd, "--user", "ed", "--reason", "x", "--yes")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "会話記録の置き場が決まりません") {
		t.Errorf("置き場なしの --yes（コマンド）: %d %q %q", code, stdout, stderr)
	}
	// 対照: 置き場が決まる設定（STATE_DIRECTORY）なら、同じ引数はこの検査では止まらない
	// （DB に届かないので別の理由で 1 になるが、置き場の文面ではない）
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	if _, _, stderr = captureCmd(t, purgeTranscriptsCmd, "--user", "ed", "--reason", "x", "--yes"); strings.Contains(stderr, "会話記録の置き場が決まりません") {
		t.Errorf("前提が崩れています: 置き場が決まる設定でも置き場の文面が出た: %q", stderr)
	}
}

// 同じ sha256 の本体は 1 つのファイルを共有する（placeBlob は本体があれば上書きしない）。
// 1 つの会話記録を消しても、ほかの会話記録のチャンクが指す間は本体を残し、最後の 1 つを消したときに初めて消す。
func TestPurgeTranscriptsKeepsSharedBody(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	f := newPurgeFixture(t, admin, filepath.Join(t.TempDir(), "transcripts"))
	shared := f.edBodies[2] // ed の 2 つ目の会話記録の本体
	sum := strings.Repeat("a3", 32)
	// bo の会話記録のチャンクも同じ sha256（本体のファイルは増やさない）
	if _, err := store.InsertTranscriptChunk(ctx, admin, store.TranscriptChunk{TranscriptID: f.boTranscript, Seq: 2, FileKey: "s.jsonl",
		FileKind: store.TranscriptFileJSONL, SrcOffset: 10, SrcLength: 10, LineCount: 1, SHA256: sum, Size: int64(len("second-333")),
		Encoding: store.TranscriptEncodingIdentity, Nonce: "0123456789ab", Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	run := func(login string, id int64) string {
		t.Helper()
		var out bytes.Buffer
		if err := purgeTranscripts(ctx, app, "", f.dir, i18n.JA, &out, login, id, "r", true); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	// ed の 2 つ目を消す: 共有の本体は bo のチャンクが指すので残る（消した本体のファイルは 0）
	if out := run("ed", f.edSecond); !strings.Contains(out, "チャンク 1・本体のファイル 0）") {
		t.Errorf("共有の本体を消したか数えている:\n%s", out)
	}
	f.exists(t, "共有", true, shared, f.boBody[0])
	// 対照: 共有していない ed の 1 つ目の本体は、消すと消える
	if out := run("ed", f.edFirst); !strings.Contains(out, "チャンク 2・本体のファイル 2）") {
		t.Errorf("共有していない本体:\n%s", out)
	}
	f.exists(t, "共有していない", false, f.edBodies[0], f.edBodies[1])
	f.exists(t, "共有はまだ残る", true, shared)
	// 最後に指していた bo の会話記録を消すと、共有の本体も消える
	if out := run("bo", f.boTranscript); !strings.Contains(out, "チャンク 2・本体のファイル 2）") {
		t.Errorf("最後の参照を消したとき:\n%s", out)
	}
	f.exists(t, "最後の参照", false, shared, f.boBody[0])
}
