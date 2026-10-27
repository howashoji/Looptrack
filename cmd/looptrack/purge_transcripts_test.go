package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// purgeFixture は利用者 ed（会話記録 2 つ。1 つ目は 2 チャンク）と、対照の利用者 bo（会話記録 1 つ）、会話記録の無い利用者 nobody を作り、
// チャンクが指す本体のファイルを置く。
type purgeFixture struct {
	dir                  string
	edFirst, edSecond    int64 // ed の会話記録の id
	boTranscript         int64
	edBodies, boBody     []string // ed の本体（1 つ目の 2 つ・2 つ目の 1 つ）と bo の本体
	edBytes, edFirstSize int64
}

func newPurgeFixture(t *testing.T, db *sql.DB, dir string) *purgeFixture {
	t.Helper()
	ctx := context.Background()
	f := &purgeFixture{dir: dir}
	pid, err := store.CreateProject(ctx, db, store.Project{Slug: "pur", Prefix: "PUR", Width: 4, Name: "pur"})
	if err != nil {
		t.Fatal(err)
	}
	user := func(login string) int64 {
		t.Helper()
		id, err := store.CreateUser(ctx, db, login, login, "x", "member")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ed, bo := user("ed"), user("bo")
	user("nobody")
	transcript := func(uid int64, session string) int64 {
		t.Helper()
		id, err := store.InsertTranscript(ctx, db, store.Transcript{UserID: uid, ProjectID: pid, Agent: store.TranscriptAgentClaude,
			VendorSessionID: session, OriginOS: "linux", Via: "cli"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	chunk := func(tid, seq int64, sum, content string) string {
		t.Helper()
		p := service.TranscriptBodyPath(f.dir, sum)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.InsertTranscriptChunk(ctx, db, store.TranscriptChunk{TranscriptID: tid, Seq: seq, FileKey: "s.jsonl",
			FileKind: store.TranscriptFileJSONL, SrcOffset: (seq - 1) * 10, SrcLength: 10, LineCount: 1, SHA256: sum, Size: int64(len(content)),
			Encoding: store.TranscriptEncodingIdentity, Nonce: "0123456789ab", Via: "cli"}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.edFirst, f.edSecond, f.boTranscript = transcript(ed, "s-1"), transcript(ed, "s-2"), transcript(bo, "s-3")
	f.edBodies = []string{
		chunk(f.edFirst, 1, strings.Repeat("a1", 32), "first-1"),
		chunk(f.edFirst, 2, strings.Repeat("a2", 32), "first-22"),
		chunk(f.edSecond, 1, strings.Repeat("a3", 32), "second-333"),
	}
	f.boBody = []string{chunk(f.boTranscript, 1, strings.Repeat("b1", 32), "other-user")}
	f.edFirstSize, f.edBytes = int64(len("first-1")+len("first-22")), int64(len("first-1")+len("first-22")+len("second-333"))
	return f
}

func (f *purgeFixture) exists(t *testing.T, when string, want bool, paths ...string) {
	t.Helper()
	for _, p := range paths {
		_, err := os.Stat(p)
		if want && err != nil {
			t.Errorf("%s: %s が無い: %v", when, p, err)
		}
		if !want && !os.IsNotExist(err) {
			t.Errorf("%s: %s が残っている: %v", when, p, err)
		}
	}
}

func purgeRows(t *testing.T, db *sql.DB) (n int, cause, via, reason string, actorNull bool) {
	t.Helper()
	if err := db.QueryRow("SELECT COUNT(*) FROM transcript_purges").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return
	}
	var actor sql.NullInt64
	if err := db.QueryRow("SELECT cause, via, reason, actor_user_id FROM transcript_purges ORDER BY id LIMIT 1").Scan(&cause, &via, &reason, &actor); err != nil {
		t.Fatal(err)
	}
	return n, cause, via, reason, !actor.Valid
}

// looptrack purge-transcripts: --yes が無ければ消す対象の数と大きさを報告するだけで何も変えない。--yes で本体が無くなり、
// transcript_purges に cause operator・操作した利用者なしで残る。対照: 別の利用者の会話記録と、--transcript で絞った残りは消えない。
func TestPurgeTranscriptsCmd(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	f := newPurgeFixture(t, admin, filepath.Join(t.TempDir(), "transcripts"))
	attachDir := filepath.Join(t.TempDir(), "attachments")
	run := func(login string, id int64, reason string, yes bool) (string, error) {
		var out bytes.Buffer
		err := purgeTranscripts(ctx, app, attachDir, f.dir, i18n.JA, &out, login, id, reason, yes)
		return out.String(), err
	}
	all := append(append([]string{}, f.edBodies...), f.boBody...)

	// 既定: 数と大きさを報告するだけ
	out, err := run("ed", 0, "", false)
	if err != nil {
		t.Fatal(err)
	}
	want := "ed の消す対象: 会話記録 2・消去していないチャンク 3・" + strconv.FormatInt(f.edBytes, 10) + " バイト（dry-run。何も消していません。消すには --reason を付けて --yes を付け、再実行）\n"
	if out != want {
		t.Errorf("dry-run:\n%s\nwant\n%s", out, want)
	}
	if out, err = run("ed", f.edFirst, "", false); err != nil || !strings.Contains(out, "会話記録 1・消去していないチャンク 2・"+strconv.FormatInt(f.edFirstSize, 10)+" バイト") {
		t.Errorf("--transcript の dry-run: %q %v", out, err)
	}
	f.exists(t, "dry-run", true, all...)
	if n, _, _, _, _ := purgeRows(t, admin); n != 0 {
		t.Errorf("dry-run が transcript_purges に %d 行を足した", n)
	}

	// --yes でも理由が無ければ消さない
	if _, err := run("ed", 0, "  ", true); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "--reason") {
		t.Errorf("理由なしの --yes: err = %v", err)
	}
	f.exists(t, "理由なし", true, all...)

	// --yes --transcript: その 1 つだけ消える（同じ利用者の別の会話記録・別の利用者のものは残る）
	if out, err = run("ed", f.edFirst, "rotated secret", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "消した: 会話記録 "+strconv.FormatInt(f.edFirst, 10)+"（チャンク 2・本体のファイル 2）\n") {
		t.Errorf("--yes --transcript:\n%s", out)
	}
	f.exists(t, "--transcript", false, f.edBodies[0], f.edBodies[1])
	f.exists(t, "--transcript の残り", true, f.edBodies[2], f.boBody[0])
	n, cause, via, reason, actorNull := purgeRows(t, admin)
	if n != 1 || cause != "operator" || via != "admin" || reason != "rotated secret" || !actorNull {
		t.Errorf("transcript_purges: %d 行・cause=%q via=%q reason=%q actor NULL=%v", n, cause, via, reason, actorNull)
	}

	// --yes: 残りの ed の会話記録が消える（bo のものは残る）。消去済みの 1 つ目は数えない
	if out, err = run("ed", 0, "offboarding", true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "ed の会話記録 1・10 バイトを消し、本体のファイル 1 を削除しました\n") || strings.Count(out, "消した: ") != 1 {
		t.Errorf("--yes:\n%s", out)
	}
	f.exists(t, "--yes", false, f.edBodies...)
	f.exists(t, "対照: 別の利用者", true, f.boBody...)
	if n, _, _, _, _ = purgeRows(t, admin); n != 2 {
		t.Errorf("transcript_purges が %d 行（2 のはず）", n)
	}
	// もう一度: 消す対象は 0 で、行は増えない
	if out, err = run("ed", 0, "again", true); err != nil || !strings.Contains(out, "会話記録 0・0 バイト") {
		t.Errorf("再実行: %q %v", out, err)
	}
	if n, _, _, _, _ = purgeRows(t, admin); n != 2 {
		t.Errorf("再実行で transcript_purges が %d 行になった", n)
	}
}

// 存在しない利用者・会話記録の無い利用者・その利用者のものでない会話記録は、報告も消去もせずに理由を返す（dry-run も --yes も）。
func TestPurgeTranscriptsErrors(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	f := newPurgeFixture(t, admin, filepath.Join(t.TempDir(), "transcripts"))
	all := append(append([]string{}, f.edBodies...), f.boBody...)
	for _, c := range []struct {
		name, login string
		id          int64
		want        string
	}{
		{"存在しない利用者", "ghost", 0, "利用者が見つかりません: ghost"},
		{"会話記録の無い利用者", "nobody", 0, "利用者 nobody の会話記録はありません"},
		{"ほかの利用者の会話記録", "ed", f.boTranscript, "会話記録が見つかりません: " + strconv.FormatInt(f.boTranscript, 10)},
	} {
		for _, yes := range []bool{false, true} {
			var out bytes.Buffer
			err := purgeTranscripts(ctx, app, "", f.dir, i18n.JA, &out, c.login, c.id, "r", yes)
			if txt := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(txt, c.want) {
				t.Errorf("%s（yes=%v）: err = %q, want %q", c.name, yes, txt, c.want)
			}
			if out.Len() != 0 {
				t.Errorf("%s（yes=%v）: 出力した: %s", c.name, yes, out.String())
			}
		}
	}
	f.exists(t, "エラーの後", true, all...)
	if n, _, _, _, _ := purgeRows(t, admin); n != 0 {
		t.Errorf("エラーなのに transcript_purges に %d 行", n)
	}
}

// 終了コード: 利用者が居ない・会話記録が無いのは 1（理由は標準エラー）、--user なしは使い方の誤りの 2、--yes は理由がなければ 1。
// 対照: dry-run と --yes は 0。接続は環境変数 LOOPTRACK_DSN（SQLite）で、置き場は DB の隣の transcripts。
func TestPurgeTranscriptsExitCodes(t *testing.T) {
	dir := t.TempDir()
	dsn := "sqlite:" + filepath.Join(dir, "p.db")
	t.Setenv("LOOPTRACK_DSN", dsn)
	t.Setenv("LOOPTRACK_LANG", "ja")
	t.Setenv("LOOPTRACK_ATTACH_DIR", "")
	t.Setenv("LOOPTRACK_TRANSCRIPT_DIR", "")
	t.Setenv("STATE_DIRECTORY", "")
	if code, out, errOut := captureCmd(t, migrateCmd); code != 0 {
		t.Fatalf("migrate: %d\n%s%s", code, out, errOut)
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f := newPurgeFixture(t, db, filepath.Join(dir, "transcripts"))

	for _, c := range []struct {
		name    string
		args    []string
		code    int
		errPart string
	}{
		{"存在しない利用者", []string{"--user", "ghost"}, 1, "利用者が見つかりません: ghost"},
		{"会話記録の無い利用者", []string{"--user", "nobody", "--reason", "x", "--yes"}, 1, "利用者 nobody の会話記録はありません"},
		{"理由なしの --yes", []string{"--user", "ed", "--yes"}, 1, "--reason"},
		{"--user なし", []string{}, 2, "--user"},
		{"余分な引数", []string{"--user", "ed", "extra"}, 2, "余分な引数"},
	} {
		code, out, errOut := captureCmd(t, purgeTranscriptsCmd, c.args...)
		if code != c.code || !strings.Contains(errOut, c.errPart) || out != "" {
			t.Errorf("%s: 終了コード %d（want %d）・stdout %q・stderr %q（%q を含むはず）", c.name, code, c.code, out, errOut, c.errPart)
		}
	}
	f.exists(t, "エラーの後", true, f.edBodies...)

	if code, out, errOut := captureCmd(t, purgeTranscriptsCmd, "--user", "ed"); code != 0 || !strings.Contains(out, "dry-run") || errOut != "" {
		t.Errorf("対照: dry-run: %d %q %q", code, out, errOut)
	}
	f.exists(t, "dry-run", true, f.edBodies...)
	if code, out, errOut := captureCmd(t, purgeTranscriptsCmd, "--user", "ed", "--reason", "x", "--yes"); code != 0 || !strings.Contains(out, "削除しました") || errOut != "" {
		t.Errorf("対照: --yes: %d %q %q", code, out, errOut)
	}
	f.exists(t, "--yes", false, f.edBodies...)
	f.exists(t, "対照: 別の利用者", true, f.boBody...)
}

// looptrack purge-transcripts --yes は、サーバ（別のプロセス）で同じ本体を置いてから記録を確定する前の追記があれば、
// 本体を消さない。その追記が確定した後は本体を読める。対照: 確定した後に、その追記の持ち主の記録を消すと本体は消える。
// サーバは DB と置き場だけを共有する別の Service で作る（プロセスの中の排他を共有しない）。
func TestPurgeTranscriptsKeepsBodyOfAppendInAnotherProcess(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "transcripts")
	attachDir := filepath.Join(t.TempDir(), "attachments")
	pid, err := store.CreateProject(ctx, admin, store.Project{Slug: "pur", Prefix: "PUR", Width: 4, Name: "pur"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRules(ctx, admin, pid, []byte(`{"session":{"transcripts":true}}`)); err != nil {
		t.Fatal(err)
	}
	actor := func(login string) service.Actor {
		t.Helper()
		id, err := store.CreateUser(ctx, admin, login, login, "x", "member")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetMember(ctx, admin, pid, id, "editor"); err != nil {
			t.Fatal(err)
		}
		return service.Actor{UserID: id, Via: "cli", Lang: i18n.JA}
	}
	ed, bo := actor("ed"), actor("bo")
	server := service.New(app, nil)
	server.AttachDir, server.TranscriptDir = attachDir, dir
	register := func(a service.Actor, sid string) int64 {
		t.Helper()
		st, err := server.RegisterTranscript(ctx, a, service.RegisterTranscriptInput{Project: "pur", Agent: store.TranscriptAgentClaude,
			VendorSessionID: sid, MachineLabel: "box-1", OriginOS: "linux", AgentVersion: "2.1.0"})
		if err != nil {
			t.Fatal(err)
		}
		return st.Transcript.ID
	}
	const edSID, boSID = "00000000-0000-4000-8000-0000000000aa", "00000000-0000-4000-8000-0000000000bb"
	shared := `{"type":"user","cwd":"{{LT:0123456789ab:ROOT0}}","message":{"role":"user","content":"shared body"}}` + "\n"
	in := func(id int64, sid string) service.AppendTranscriptChunkInput {
		return service.AppendTranscriptChunkInput{TranscriptID: id, IfMatch: `"0"`, FileKey: sid + ".jsonl", SrcLength: int64(len(shared)),
			Nonce: "0123456789ab", Placeholders: []string{"ROOT0"}, Stats: `{"lines":1}`, Body: strings.NewReader(shared)}
	}
	edID, boID := register(ed, edSID), register(bo, boSID)
	r, err := server.AppendTranscriptChunk(ctx, ed, in(edID, edSID))
	if err != nil {
		t.Fatal(err)
	}
	body := service.TranscriptBodyPath(dir, r.Chunk.SHA256)

	// サーバで bo の同じ内容の追記を、本体を置いた後・記録を確定する前で止める（待つのは 30 秒まで）
	placed, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let)
	server.TranscriptPlaced = func(string) {
		close(placed)
		<-release
	}
	go func() {
		_, err := server.AppendTranscriptChunk(ctx, bo, in(boID, boSID))
		done <- err
	}()
	select {
	case <-placed:
	case err := <-done:
		t.Fatalf("前提が崩れています: 追記が本体を置く前に終わった: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("前提が崩れています: 追記が 30 秒以内に本体を置かなかった")
	}
	run := func(login string) string {
		t.Helper()
		var out bytes.Buffer
		if err := purgeTranscripts(ctx, app, attachDir, dir, i18n.JA, &out, login, 0, "while appending", true); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := run("ed"); !strings.Contains(out, "消した: 会話記録 "+strconv.FormatInt(edID, 10)+"（チャンク 1・本体のファイル 0）\n") {
		t.Errorf("追記が確定する前の purge-transcripts:\n%s（チャンクは消去済み・本体は消さない）", out)
	}
	if _, err := os.Stat(body); err != nil {
		t.Fatalf("サーバの追記が確定する前に本体が消えた: %v", err)
	}
	let()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("止めた追記: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("止めた追記が、続けてから 30 秒以内に終わらなかった")
	}
	server.TranscriptPlaced = nil
	if _, f, err := server.OpenTranscriptChunk(ctx, bo, boID, 1); err != nil {
		t.Fatalf("確定した追記の本体: %v", err)
	} else {
		f.Close()
	}

	// 対照: 置こうとしている追記が無ければ、どこからも指されなくなった本体は消える
	if out := run("bo"); !strings.Contains(out, "消した: 会話記録 "+strconv.FormatInt(boID, 10)+"（チャンク 1・本体のファイル 1）\n") {
		t.Errorf("確定した後の purge-transcripts:\n%s（本体を 1 つ消す）", out)
	}
	if _, err := os.Stat(body); !os.IsNotExist(err) {
		t.Fatalf("確定した後の purge-transcripts で本体が残った: %v", err)
	}
}
