package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 会話記録の service（transcripts.go）のテスト。土台は添付のテストと同じ attachEnv（アプリ用の DB 利用者で動かす）に、
// 会話記録の置き場（添付の置き場と別のルート）とルール session.transcripts を足したもの。
// 中身はすべて合成の行（実際の会話記録は読まない）。

// 合成の sessionId（Claude の形）。
const tSID = "00000000-0000-4000-8000-0000000000aa"

// tSecretValue は合成の秘密の値（domain の規則 password=… に当たる。実在の資格情報ではない）。
const tSecretValue = "password=Synthetic0Value9"

type transcriptEnv struct {
	*attachEnv
	padmin Actor // demo の管理者（project_members の admin）
}

func newTranscriptEnv(t *testing.T) *transcriptEnv {
	t.Helper()
	e := &transcriptEnv{attachEnv: newAttachEnv(t)}
	e.s.TranscriptDir = t.TempDir()
	id, err := store.CreateUser(e.ctx, e.admin, "pa", "pa", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	e.padmin = Actor{UserID: id, Via: "cli", Lang: i18n.JA}
	if err := store.SetMember(e.ctx, e.admin, e.demo.ID, id, "admin"); err != nil {
		t.Fatal(err)
	}
	e.setRule(e.demo, true)
	return e
}

func (e *transcriptEnv) setRule(p store.Project, on bool) {
	e.t.Helper()
	var rules []byte
	if on {
		rules = []byte(`{"session":{"transcripts":true}}`)
	}
	if err := store.SetRules(e.ctx, e.admin, p.ID, rules); err != nil {
		e.t.Fatal(err)
	}
}

func (e *transcriptEnv) register(a Actor, slug, sid string) (*TranscriptState, error) {
	return e.s.RegisterTranscript(e.ctx, a, RegisterTranscriptInput{Project: slug, Agent: store.TranscriptAgentClaude, VendorSessionID: sid,
		MachineLabel: "box-1", OriginOS: "linux", AgentVersion: "2.1.0"})
}

func (e *transcriptEnv) mustRegister(a Actor, sid string) *TranscriptState {
	e.t.Helper()
	st, err := e.register(a, e.demo.Slug, sid)
	if err != nil {
		e.t.Fatalf("登録: %v", err)
	}
	return st
}

// chunkIn は jsonl の本体のチャンクの入力を作る（src_length は本文の長さ）。
func chunkIn(id int64, ifMatch string, key string, offset int64, body string) AppendTranscriptChunkInput {
	return AppendTranscriptChunkInput{TranscriptID: id, IfMatch: ifMatch, FileKey: key, SrcOffset: offset, SrcLength: int64(len(body)),
		Nonce: "0123456789ab", Placeholders: []string{"ROOT0"}, Stats: `{"lines":1}`, Body: strings.NewReader(body)}
}

func (e *transcriptEnv) appendChunk(a Actor, in AppendTranscriptChunkInput) (*AppendTranscriptChunkResult, error) {
	return e.s.AppendTranscriptChunk(e.ctx, a, in)
}

func (e *transcriptEnv) mustAppend(a Actor, in AppendTranscriptChunkInput) *AppendTranscriptChunkResult {
	e.t.Helper()
	r, err := e.appendChunk(a, in)
	if err != nil {
		e.t.Fatalf("追記 %s: %v", in.FileKey, err)
	}
	return r
}

// transcriptFiles は会話記録の置き場の通常のファイル（本体と一時ファイル）の数。
func (e *transcriptEnv) transcriptFiles() int {
	e.t.Helper()
	n := 0
	err := filepath.WalkDir(e.s.TranscriptDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// line は合成の会話記録の 1 行（改行つき）。
func line(text string) string {
	return `{"type":"user","cwd":"{{LT:0123456789ab:ROOT0}}","message":{"role":"user","content":` + strconv.Quote(text) + `}}` + "\n"
}

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// 受け入れ条件 1: 持ち主でない利用者（同じプロジェクトの editor・管理者・利用者の役割 admin・ほかのプロジェクトの人）は
// 取得・本体・消去・追記がすべて not_found。対照として持ち主は読める。
func TestTranscriptOwnerOnly(t *testing.T) {
	e := newTranscriptEnv(t)
	owner := e.viewer // viewer でも登録できる（参加者なら足せる）
	st := e.mustRegister(owner, tSID)
	if !st.Created || st.Seq != 0 || len(st.Files) != 0 || !st.TranscriptsEnabled || st.ProjectSlug != "demo" {
		t.Fatalf("登録の応答 = %+v", st)
	}
	body := line("hello")
	e.mustAppend(owner, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, body))

	// 対照: 持ち主は一覧・取得・本体を読める
	list, err := e.s.ListTranscripts(e.ctx, owner, "")
	if err != nil || len(list) != 1 || list[0].ID != st.Transcript.ID {
		t.Fatalf("持ち主の一覧 = %+v, %v", list, err)
	}
	d, err := e.s.GetTranscript(e.ctx, owner, st.Transcript.ID)
	if err != nil || len(d.Chunks) != 1 || d.Seq != 1 || len(d.Files) != 1 || d.Files[0].Length != int64(len(body)) {
		t.Fatalf("持ち主の取得 = %+v, %v", d, err)
	}
	c, f, err := e.s.OpenTranscriptChunk(e.ctx, owner, st.Transcript.ID, 1)
	if err != nil {
		t.Fatalf("持ち主の本体: %v", err)
	}
	var got bytes.Buffer
	_, _ = got.ReadFrom(f)
	f.Close()
	if got.String() != body || c.LineCount != 1 || c.Encoding != store.TranscriptEncodingIdentity || c.FileKind != store.TranscriptFileJSONL {
		t.Fatalf("本体 = %q, チャンク = %+v", got.String(), c)
	}

	for name, a := range map[string]Actor{"同じプロジェクトの editor": e.editor, "同じプロジェクトの管理者": e.padmin,
		"利用者の役割 admin": e.root, "ほかのプロジェクトの人": e.outside} {
		if _, err := e.s.GetTranscript(e.ctx, a, st.Transcript.ID); errKind(err) != NotFound || errCode(err) != "not_found" {
			t.Errorf("%s の取得 = %v（not_found であること）", name, err)
		}
		if _, f, err := e.s.OpenTranscriptChunk(e.ctx, a, st.Transcript.ID, 1); errKind(err) != NotFound || errCode(err) != "not_found" {
			if f != nil {
				f.Close()
			}
			t.Errorf("%s の本体 = %v（not_found であること）", name, err)
		}
		if _, err := e.s.PurgeTranscript(e.ctx, a, st.Transcript.ID, "x"); errKind(err) != NotFound || errCode(err) != "not_found" {
			t.Errorf("%s の消去 = %v（not_found であること）", name, err)
		}
		if _, err := e.appendChunk(a, chunkIn(st.Transcript.ID, `"1"`, tSID+".jsonl", int64(len(body)), line("x"))); errKind(err) != NotFound {
			t.Errorf("%s の追記 = %v（not_found であること）", name, err)
		}
		if l, err := e.s.ListTranscripts(e.ctx, a, ""); err != nil || len(l) != 0 {
			t.Errorf("%s の一覧 = %+v, %v（空であること）", name, l, err)
		}
	}
	// ほかの人の消去の試みの後も、持ち主の本体は残っている
	if _, f, err := e.s.OpenTranscriptChunk(e.ctx, owner, st.Transcript.ID, 1); err != nil {
		t.Fatalf("ほかの人の操作の後の持ち主の本体: %v", err)
	} else {
		f.Close()
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_purges"); n != 0 {
		t.Fatalf("消去の記録 = %d（ほかの人の消去で増えないこと）", n)
	}
	// 一覧の絞り込み: 無い slug・参加していないプロジェクトは空（存在を確かめる道にしない）
	for _, slug := range []string{"no-such", "other"} {
		if l, err := e.s.ListTranscripts(e.ctx, owner, slug); err != nil || len(l) != 0 {
			t.Errorf("slug %s の一覧 = %+v, %v", slug, l, err)
		}
	}
}

// 受け入れ条件 2: If-Match 無しは 428、違う seq は 409 transcript_seq（今の seq を返す）、src_offset の食い違いは 409 transcript_offset。
func TestTranscriptAppendSeq(t *testing.T) {
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	id := st.Transcript.ID
	key := tSID + ".jsonl"
	if _, err := e.appendChunk(e.editor, chunkIn(id, "", key, 0, line("a"))); errKind(err) != PreconditionRequired || errCode(err) != "transcript_seq_required" {
		t.Fatalf("If-Match 無し = %v", err)
	}
	if _, err := e.appendChunk(e.editor, chunkIn(id, `"x"`, key, 0, line("a"))); errKind(err) != Invalid {
		t.Fatalf("読めない If-Match = %v", err)
	}
	first := line("a")
	r := e.mustAppend(e.editor, chunkIn(id, `"0"`, key, 0, first))
	if r.Chunk.Seq != 1 || r.State.Seq != 1 || len(r.State.Files) != 1 || r.State.Files[0].Length != int64(len(first)) {
		t.Fatalf("1 つ目の追記 = %+v", r)
	}
	for _, ifm := range []string{`"0"`, `"2"`, `W/"5"`} {
		_, err := e.appendChunk(e.editor, chunkIn(id, ifm, key, int64(len(first)), line("b")))
		var se *Error
		if !errors.As(err, &se) || se.Kind != Conflict || se.Code != "transcript_seq" || se.CurrentSeq == nil || *se.CurrentSeq != 1 {
			t.Fatalf("If-Match %s = %v（409 transcript_seq・今の seq 1 であること）", ifm, err)
		}
	}
	// 抜け（先の位置）も重なり（前の位置）も 409 transcript_offset
	for _, off := range []int64{0, int64(len(first)) + 1} {
		if _, err := e.appendChunk(e.editor, chunkIn(id, `"1"`, key, off, line("b"))); errKind(err) != Conflict || errCode(err) != "transcript_offset" {
			t.Fatalf("src_offset %d = %v", off, err)
		}
	}
	// 対照: 正しい連番と位置なら通る。別のファイルは 0 から
	e.mustAppend(e.editor, chunkIn(id, "1", key, int64(len(first)), line("b")))
	r = e.mustAppend(e.editor, chunkIn(id, `"2"`, tSID+"/subagents/agent-a1.jsonl", 0, line("c")))
	if r.Chunk.Seq != 3 || len(r.State.Files) != 2 {
		t.Fatalf("3 つ目の追記 = %+v", r)
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_chunks WHERE transcript_id = ?", id); n != 3 {
		t.Fatalf("チャンクの行 = %d", n)
	}
}

// 受け入れ条件 2: 同じ seq の同時の追記は 1 つだけ通る。2 つの Service（プロセスの中の排他を共有しない）から並べて送る。
func TestTranscriptConcurrentAppendSameSeq(t *testing.T) {
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	s2 := New(e.app, nil)
	s2.AttachDir, s2.TranscriptDir = e.s.AttachDir, e.s.TranscriptDir
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		svc := e.s
		if i%2 == 1 {
			svc = s2
		}
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.AppendTranscriptChunk(context.Background(), e.editor, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, line(fmt.Sprintf("racer %d", i))))
		}(i, svc)
	}
	close(start)
	wg.Wait()
	ok := 0
	for i, err := range errs {
		var se *Error
		switch {
		case err == nil:
			ok++
		case errors.As(err, &se) && se.Code == "transcript_seq" && se.CurrentSeq != nil && *se.CurrentSeq == 1:
		default:
			t.Errorf("追記 %d = %v（通るか 409 transcript_seq・今の seq 1 であること）", i, err)
		}
	}
	if ok != 1 {
		t.Fatalf("通った追記 = %d（1 つだけであること）", ok)
	}
	if c := e.count("SELECT COUNT(*) FROM transcript_chunks WHERE transcript_id = ?", st.Transcript.ID); c != 1 {
		t.Fatalf("チャンクの行 = %d", c)
	}
	// 負けた追記が先に置いた本体は残らない（どこからも指されないので消す）。残るのは勝った 1 つだけ
	if n := e.transcriptFiles(); n != 1 {
		t.Fatalf("置き場のファイル = %d（勝った追記の本体 1 つだけであること）", n)
	}
}

// 同じ利用者の、別々の会話記録への同時の追記は、合わせて 1 利用者の上限に収まる分だけが通る（使用量の判定を利用者ごとに 1 つずつにする）。
// 2 つの Service（プロセスの中の排他を共有しない）から並べて送る。対照: 上限は 3 つ分なので、通るのはちょうど 3 つ（0 ではない）。
func TestTranscriptConcurrentAppendQuota(t *testing.T) {
	e := newTranscriptEnv(t)
	const n, fit = 8, 3
	body := func(i int) string { return line(fmt.Sprintf("racer %d", i)) }
	size := int64(len(body(0)))
	if _, _, err := store.SetTranscriptLimit(e.ctx, e.admin, store.SettingTranscriptMaxUser, fit*size+size/2, store.SettingChange{Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	s2 := New(e.app, nil)
	s2.AttachDir, s2.TranscriptDir = e.s.AttachDir, e.s.TranscriptDir
	sids := make([]string, n)
	ids := make([]int64, n)
	for i := range sids {
		sids[i] = fmt.Sprintf("00000000-0000-4000-8000-0000000001%02x", i)
		ids[i] = e.mustRegister(e.editor, sids[i]).Transcript.ID
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		svc := e.s
		if i%2 == 1 {
			svc = s2
		}
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.AppendTranscriptChunk(context.Background(), e.editor, chunkIn(ids[i], `"0"`, sids[i]+".jsonl", 0, body(i)))
		}(i, svc)
	}
	close(start)
	wg.Wait()
	ok := 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case errKind(err) == Rejected && errCode(err) == "transcript_quota":
		default:
			t.Errorf("追記 %d = %v（通るか 422 transcript_quota であること）", i, err)
		}
	}
	if ok != fit {
		t.Fatalf("通った追記 = %d（上限に収まる %d つだけであること）", ok, fit)
	}
	used, err := store.TranscriptUserBytes(e.ctx, e.admin, e.editor.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if used != fit*size {
		t.Fatalf("使用量 = %d（%d であること）", used, fit*size)
	}
	if f := e.transcriptFiles(); f != fit {
		t.Fatalf("置き場のファイル = %d（通った追記の本体 %d つだけであること）", f, fit)
	}
}

// 追記は users の行をロックしない: 別のトランザクションが持ち主の users の行を握っていても、追記は待たずに通る。
// 対照: その行は本当に握られている（別の接続からの同じ行の UPDATE はロック待ちで時間切れになる）。
// SQLite は書き込みのトランザクションを 1 つずつにするので、行ロックの区別が無い（MySQL だけで確かめる）。
func TestTranscriptAppendDoesNotLockUsersRow(t *testing.T) {
	if testutil.Dialect() != testutil.DialectMySQL {
		t.Skip("行ロックは MySQL だけで確かめる（SQLite は書き込みを 1 つずつにする）")
	}
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	holder, err := e.admin.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	var uid int64
	if err := holder.QueryRowContext(e.ctx, "SELECT id FROM users WHERE id = ? FOR UPDATE", e.editor.UserID).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	// 対照: 同じ行の UPDATE は待たされる
	conn, err := e.admin.Conn(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(e.ctx, "SET SESSION innodb_lock_wait_timeout = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(e.ctx, "UPDATE users SET lang = lang WHERE id = ?", e.editor.UserID); err == nil || !strings.Contains(err.Error(), "1205") {
		t.Fatalf("前提が崩れています: 握った users の行の UPDATE = %v（ロック待ちの時間切れ 1205 であること）", err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	r, err := e.s.AppendTranscriptChunk(ctx, e.editor, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("while users is locked")))
	if err != nil {
		t.Fatalf("users の行が握られている間の追記 = %v（待たずに通ること）", err)
	}
	if r.Chunk.Seq != 1 {
		t.Fatalf("追記の seq = %d", r.Chunk.Seq)
	}
}

// 受け入れ条件 3: 値に秘密が残ったチャンクは 422 transcript_secret で、本体のファイルも行も残らない。文面に値が入らない。
// 対照: 値ごとにマスクした行は通る（その行は行全体に FindSecret を当てると当たる。サーバは行全体には当てない）。
func TestTranscriptSecretRejected(t *testing.T) {
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	id := st.Transcript.ID
	key := tSID + ".jsonl"
	cases := map[string]string{
		"値":       line("ok") + line("run with "+tSecretValue),
		"キー":      `{"` + tSecretValue + `":1}` + "\n",
		"重複したキー":  `{"a":"` + tSecretValue + `","a":"plain"}` + "\n",
		"深い入れ子の値": `{"x":[{"y":{"z":["` + tSecretValue + `"]}}]}` + "\n",
	}
	for name, body := range cases {
		for _, enc := range []string{"", "gzip"} {
			in := chunkIn(id, `"0"`, key, 0, body)
			if enc == "gzip" {
				in.Encoding, in.Body = "gzip", bytes.NewReader(gz(t, body))
			}
			_, err := e.appendChunk(e.editor, in)
			if errKind(err) != Rejected || errCode(err) != "transcript_secret" {
				t.Fatalf("%s（%s）= %v（422 transcript_secret であること）", name, enc, err)
			}
			for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
				if msg := i18n.Text(lang, err); strings.Contains(msg, "Synthetic0Value9") {
					t.Fatalf("%s: 拒否の文面に値が入っている: %s", name, msg)
				}
			}
		}
	}
	// text（tool-results）は全体に当てる
	textIn := chunkIn(id, `"0"`, tSID+"/tool-results/toolu_01.txt", 0, "line 1\n"+tSecretValue+"\n")
	if _, err := e.appendChunk(e.editor, textIn); errCode(err) != "transcript_secret" {
		t.Fatalf("text の秘密 = %v", err)
	}
	// stats に値を紛れ込ませても止める
	statsIn := chunkIn(id, `"0"`, key, 0, line("ok"))
	statsIn.Stats = `{"note":"` + tSecretValue + `"}`
	if _, err := e.appendChunk(e.editor, statsIn); errCode(err) != "transcript_secret" {
		t.Fatalf("stats の秘密 = %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_chunks"); n != 0 {
		t.Fatalf("拒んだ後のチャンクの行 = %d", n)
	}
	if n := e.transcriptFiles(); n != 0 {
		t.Fatalf("拒んだ後の置き場のファイル = %d（本体も一時ファイルも残らないこと）", n)
	}

	// 対照: 値ごとにマスクした行は通る。前提として、その行は行全体に当てると FindSecret が当たる
	masked, _ := domain.MaskSecretsCount("run with " + tSecretValue)
	maskedLine := `{"a":` + strconv.Quote(masked) + `,"b":"x"}` + "\n"
	if _, hit := domain.FindSecret(maskedLine); !hit {
		t.Fatal("前提が崩れています: 値ごとにマスクした行に、行全体で FindSecret を当てても当たらない（行全体に当てない理由の対照にならない）")
	}
	if _, hit := domain.FindSecret(masked); hit {
		t.Fatal("前提が崩れています: マスクした値そのものに FindSecret が当たる")
	}
	r := e.mustAppend(e.editor, chunkIn(id, `"0"`, key, 0, maskedLine))
	if r.Chunk.Seq != 1 {
		t.Fatalf("マスクした行の追記 = %+v", r.Chunk)
	}
}

// 受け入れ条件 3: 読めない行は 422 transcript_unreadable（文面に行の中身が入らない。本体も行も残らない）。
func TestTranscriptUnreadable(t *testing.T) {
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	id := st.Transcript.ID
	for name, c := range map[string]struct{ key, body string }{
		"壊れた行":      {tSID + ".jsonl", line("ok") + `{"a":"unreadable-marker"` + "\n"},
		"空の行":       {tSID + ".jsonl", line("ok") + "\n"},
		"改行で終わらない行": {tSID + ".jsonl", line("ok") + `{"a":1}`},
		"json の全体":  {tSID + "/subagents/agent-a1.meta.json", `{"a":"unreadable-marker"`},
	} {
		_, err := e.appendChunk(e.editor, chunkIn(id, `"0"`, c.key, 0, c.body))
		if errKind(err) != Rejected || errCode(err) != "transcript_unreadable" {
			t.Fatalf("%s = %v", name, err)
		}
		if msg := i18n.Text(i18n.EN, err); strings.Contains(msg, "unreadable-marker") {
			t.Fatalf("%s: 文面に行の中身が入っている: %s", name, msg)
		}
	}
	// gzip を名乗って gzip でない本文
	in := chunkIn(id, `"0"`, tSID+".jsonl", 0, line("ok"))
	in.Encoding = "gzip"
	if _, err := e.appendChunk(e.editor, in); errCode(err) != "transcript_unreadable" {
		t.Fatalf("gzip でない本文 = %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_chunks"); n != 0 {
		t.Fatalf("拒んだ後のチャンクの行 = %d", n)
	}
	if n := e.transcriptFiles(); n != 0 {
		t.Fatalf("拒んだ後の置き場のファイル = %d", n)
	}
	// 対照: json の全体が読めれば通る（line_count 1）
	r := e.mustAppend(e.editor, chunkIn(id, `"0"`, tSID+"/subagents/agent-a1.meta.json", 0, `{"agentType":"x"}`))
	if r.Chunk.FileKind != store.TranscriptFileJSON || r.Chunk.LineCount != 1 {
		t.Fatalf("json の追記 = %+v", r.Chunk)
	}
}

// 受け入れ条件 4: gzip を伸ばした大きさが transcript_max_chunk_bytes を超えると拒む（圧縮率の高い合成の本文）。
func TestTranscriptInflatedSizeLimit(t *testing.T) {
	e := newTranscriptEnv(t)
	const limit = 64 << 10
	if _, _, err := store.SetTranscriptLimit(e.ctx, e.admin, store.SettingTranscriptMaxChunk, limit, store.SettingChange{Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	st := e.mustRegister(e.editor, tSID)
	big := strings.Repeat(line(strings.Repeat("a", 200)), (1<<20)/len(line(strings.Repeat("a", 200))))
	compressed := gz(t, big)
	if len(compressed) >= limit || len(big) <= limit {
		t.Fatalf("前提が崩れています: 圧縮 %d バイト・伸ばした %d バイト（圧縮は上限未満、伸ばすと上限超えであること）", len(compressed), len(big))
	}
	in := chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, big)
	in.Encoding, in.Body, in.Declared = "gzip", bytes.NewReader(compressed), int64(len(compressed))
	if _, err := e.appendChunk(e.editor, in); errKind(err) != TooLarge || errCode(err) != "transcript_too_large" {
		t.Fatalf("伸ばすと上限を超える gzip = %v", err)
	}
	// identity で上限を超える本文も拒む（申告で読まずに・申告なしで読みながら）
	for _, declared := range []int64{int64(len(big)), 0} {
		in := chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, big)
		in.Declared = declared
		if _, err := e.appendChunk(e.editor, in); errCode(err) != "transcript_too_large" {
			t.Fatalf("identity（申告 %d）= %v", declared, err)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_chunks"); n != 0 {
		t.Fatalf("拒んだ後のチャンクの行 = %d", n)
	}
	if n := e.transcriptFiles(); n != 0 {
		t.Fatalf("拒んだ後の置き場のファイル = %d", n)
	}
	// 対照: 上限の内側の gzip は通り、gzip の形のまま置く
	small := big[:len(line(strings.Repeat("a", 200)))*100]
	in = chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, small)
	zs := gz(t, small)
	in.Encoding, in.Body = "gzip", bytes.NewReader(zs)
	r := e.mustAppend(e.editor, in)
	if r.Chunk.Encoding != "gzip" || r.Chunk.Size != int64(len(zs)) || r.Chunk.LineCount != 100 || r.Chunk.SrcLength != int64(len(small)) {
		t.Fatalf("上限の内側の gzip = %+v", r.Chunk)
	}
}

// 1 利用者の上限（消去していないチャンクの size の合計）を超える追記は 422 transcript_quota。
func TestTranscriptUserQuota(t *testing.T) {
	e := newTranscriptEnv(t)
	first := line("first")
	if _, _, err := store.SetTranscriptLimit(e.ctx, e.admin, store.SettingTranscriptMaxUser, int64(len(first))+10, store.SettingChange{Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	st := e.mustRegister(e.editor, tSID)
	e.mustAppend(e.editor, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, first))
	if _, err := e.appendChunk(e.editor, chunkIn(st.Transcript.ID, `"1"`, tSID+".jsonl", int64(len(first)), line("second"))); errKind(err) != Rejected || errCode(err) != "transcript_quota" {
		t.Fatalf("上限を超える追記 = %v", err)
	}
	if n := e.transcriptFiles(); n != 1 {
		t.Fatalf("拒んだ追記の後の置き場のファイル = %d（拒んだ本体は残さない）", n)
	}
	// 消去すれば使用量は戻る
	if _, err := e.s.PurgeTranscript(e.ctx, e.editor, st.Transcript.ID, "space"); err != nil {
		t.Fatal(err)
	}
	e.mustAppend(e.editor, chunkIn(st.Transcript.ID, `"1"`, tSID+".jsonl", 0, line("second")))
}

// 受け入れ条件 5: 消去の後に同じ会話を src_offset 0 から送り直せる。同じ本体を指す、消去していないチャンクがあれば本体を残す。
func TestTranscriptPurgeAndResend(t *testing.T) {
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	id := st.Transcript.ID
	key := tSID + ".jsonl"
	a, b := line("a"), line("b")
	e.mustAppend(e.editor, chunkIn(id, `"0"`, key, 0, a))
	e.mustAppend(e.editor, chunkIn(id, `"1"`, key, int64(len(a)), b))
	// 同じ本文を別の会話記録にも送る（同じ本体を指す）
	other := e.mustRegister(e.editor, "00000000-0000-4000-8000-0000000000bb")
	e.mustAppend(e.editor, chunkIn(other.Transcript.ID, `"0"`, "00000000-0000-4000-8000-0000000000bb.jsonl", 0, a))
	if n := e.transcriptFiles(); n != 2 {
		t.Fatalf("置き場の本体 = %d（a と b の 2 つ）", n)
	}

	res, err := e.s.PurgeTranscript(e.ctx, e.editor, id, "  test purge  ")
	if err != nil || !res.Changed || len(res.ChunkIDs) != 2 || res.Removed != 1 {
		t.Fatalf("消去 = %+v, %v（2 チャンク・本体は b だけ消える）", res, err)
	}
	if n := e.transcriptFiles(); n != 1 {
		t.Fatalf("消去の後の本体 = %d（もう一方の記録が指す a は残る）", n)
	}
	if _, f, err := e.s.OpenTranscriptChunk(e.ctx, e.editor, other.Transcript.ID, 1); err != nil {
		t.Fatalf("もう一方の記録の本体: %v", err)
	} else {
		f.Close()
	}
	if _, _, err := e.s.OpenTranscriptChunk(e.ctx, e.editor, id, 1); errCode(err) != "transcript_purged" {
		t.Fatalf("消去したチャンクの本体 = %v", err)
	}
	var cause, via, reason string
	var actor int64
	if err := e.admin.QueryRowContext(e.ctx, "SELECT cause, via, reason, actor_user_id FROM transcript_purges WHERE transcript_id = ?", id).Scan(&cause, &via, &reason, &actor); err != nil {
		t.Fatal(err)
	}
	if cause != "owner" || via != "cli" || reason != "test purge" || actor != e.editor.UserID {
		t.Fatalf("消去の記録 = %s %s %q %d", cause, via, reason, actor)
	}
	// 2 回目の消去は何もしない
	if res, err := e.s.PurgeTranscript(e.ctx, e.editor, id, "again"); err != nil || res.Changed {
		t.Fatalf("2 回目の消去 = %+v, %v", res, err)
	}
	d, err := e.s.GetTranscript(e.ctx, e.editor, id)
	if err != nil || d.Seq != 2 || len(d.Files) != 0 || !d.Chunks[0].Purged || !d.Chunks[1].Purged {
		t.Fatalf("消去の後の状態 = %+v, %v（連番は 2 のまま・送った長さは無し）", d, err)
	}
	// 送り直し: 連番は続き、位置は 0 から
	r := e.mustAppend(e.editor, chunkIn(id, `"2"`, key, 0, a))
	if r.Chunk.Seq != 3 || len(r.State.Files) != 1 || r.State.Files[0].Length != int64(len(a)) {
		t.Fatalf("送り直し = %+v", r)
	}
	if _, f, err := e.s.OpenTranscriptChunk(e.ctx, e.editor, id, 3); err != nil {
		t.Fatalf("送り直したチャンクの本体: %v", err)
	} else {
		f.Close()
	}
	l, err := e.s.ListTranscripts(e.ctx, e.editor, "demo")
	if err != nil || len(l) != 2 {
		t.Fatalf("一覧 = %+v, %v", l, err)
	}
}

// 受け入れ条件 6: 保持期間の消去が transcript_purges に cause retention を残し、transcript_retention_days 0 では何も消さない。
func TestTranscriptRetention(t *testing.T) {
	e := newTranscriptEnv(t)
	now := time.Now()
	at := func(ts time.Time) { e.s.Now = func() time.Time { return ts } }

	old := e.mustRegister(e.editor, tSID)
	at(now.Add(-100 * 24 * time.Hour))
	e.mustAppend(e.editor, chunkIn(old.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("old")))
	fresh := e.mustRegister(e.viewer, "00000000-0000-4000-8000-0000000000cc")
	at(now.Add(-24 * time.Hour))
	e.mustAppend(e.viewer, chunkIn(fresh.Transcript.ID, `"0"`, "00000000-0000-4000-8000-0000000000cc.jsonl", 0, line("fresh")))
	at(now)

	// 0（無期限）では何も消さない（どちらも古くした時刻で確かめる）
	if _, _, err := store.SetTranscriptLimit(e.ctx, e.admin, store.SettingTranscriptRetentionDays, 0, store.SettingChange{Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	at(now.Add(1000 * 24 * time.Hour))
	res, err := e.s.PurgeExpiredTranscripts(e.ctx)
	if err != nil || res.RetentionDays != 0 || len(res.Purged) != 0 {
		t.Fatalf("保持日数 0 = %+v, %v", res, err)
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_purges"); n != 0 {
		t.Fatalf("保持日数 0 の消去の記録 = %d", n)
	}
	if n := e.transcriptFiles(); n != 2 {
		t.Fatalf("保持日数 0 の後の本体 = %d", n)
	}

	// 90 日: 最後のチャンクから 100 日の記録だけが消える（1 日前のものは残る）
	if _, _, err := store.SetTranscriptLimit(e.ctx, e.admin, store.SettingTranscriptRetentionDays, 90, store.SettingChange{Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	at(now)
	res, err = e.s.PurgeExpiredTranscripts(e.ctx)
	if err != nil || res.RetentionDays != 90 || len(res.Purged) != 1 || res.Purged[0].TranscriptID != old.Transcript.ID || res.Purged[0].Removed != 1 {
		t.Fatalf("保持日数 90 = %+v, %v", res, err)
	}
	var cause, via string
	var actorNull bool
	if err := e.admin.QueryRowContext(e.ctx, "SELECT cause, via, actor_user_id IS NULL FROM transcript_purges WHERE transcript_id = ?", old.Transcript.ID).Scan(&cause, &via, &actorNull); err != nil {
		t.Fatal(err)
	}
	if cause != "retention" || via != "system" || !actorNull {
		t.Fatalf("保持期間の消去の記録 = %s %s actor NULL=%v", cause, via, actorNull)
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_purges"); n != 1 {
		t.Fatalf("消去の記録 = %d（期限内の記録は消さない）", n)
	}
	if _, f, err := e.s.OpenTranscriptChunk(e.ctx, e.viewer, fresh.Transcript.ID, 1); err != nil {
		t.Fatalf("期限内の本体: %v", err)
	} else {
		f.Close()
	}
	// もう一度回しても記録は増えない
	if res, err := e.s.PurgeExpiredTranscripts(e.ctx); err != nil || len(res.Purged) != 0 {
		t.Fatalf("2 回目 = %+v, %v", res, err)
	}
}

// ルール session.transcripts・参加者・アーカイブの拒否。
func TestTranscriptGate(t *testing.T) {
	e := newTranscriptEnv(t)
	// ルールが切のプロジェクト（other。editor は参加している）
	if _, err := e.register(e.editor, "other", tSID); errKind(err) != Rejected || errCode(err) != "transcript_rule_off" {
		t.Fatalf("ルールが切 = %v", err)
	}
	// 参加していないプロジェクトは、無いものと同じ（root は利用者の役割 admin でも参加していない）
	for _, a := range []Actor{e.root, e.outside} {
		if _, err := e.register(a, "demo", tSID); errKind(err) != NotFound {
			t.Fatalf("参加していない人の登録 = %v", err)
		}
	}
	if _, err := e.register(e.editor, "no-such", tSID); errKind(err) != NotFound {
		t.Fatalf("無いプロジェクト = %v", err)
	}
	st := e.mustRegister(e.editor, tSID)
	// 後からルールを切にすると追記も 422 transcript_rule_off
	e.setRule(e.demo, false)
	if _, err := e.appendChunk(e.editor, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("a"))); errCode(err) != "transcript_rule_off" {
		t.Fatalf("ルールを切にした後の追記 = %v", err)
	}
	e.setRule(e.demo, true)
	// アーカイブ済みのプロジェクトは登録も追記も拒む
	if _, err := e.s.SetProjectArchived(e.ctx, e.demo, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.register(e.editor, "demo", "00000000-0000-4000-8000-0000000000dd"); errCode(err) != "project_archived" {
		t.Fatalf("アーカイブ済みへの登録 = %v", err)
	}
	if _, err := e.appendChunk(e.editor, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("a"))); errCode(err) != "project_archived" {
		t.Fatalf("アーカイブ済みへの追記 = %v", err)
	}
	if _, err := e.s.SetProjectArchived(e.ctx, e.demo, false); err != nil {
		t.Fatal(err)
	}
	// プロジェクトから外れた持ち主は追記できない（読むことはできる）
	if err := store.RemoveMember(e.ctx, e.admin, e.demo.ID, e.editor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.appendChunk(e.editor, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("a"))); errKind(err) != Forbidden {
		t.Fatalf("外れた持ち主の追記 = %v", err)
	}
	if _, err := e.s.GetTranscript(e.ctx, e.editor, st.Transcript.ID); err != nil {
		t.Fatalf("外れた持ち主の取得: %v", err)
	}
	// 置き場が無ければ使えない（ほかの操作は動く）
	e.s.TranscriptDir = ""
	if _, err := e.register(e.viewer, "demo", tSID); errCode(err) != "transcripts_unavailable" {
		t.Fatalf("置き場なし = %v", err)
	}
}

// 登録は同じものを返し、入力の形を確かめる。
func TestTranscriptRegister(t *testing.T) {
	e := newTranscriptEnv(t)
	a := e.mustRegister(e.editor, tSID)
	b := e.mustRegister(e.editor, tSID)
	if !a.Created || b.Created || a.Transcript.ID != b.Transcript.ID {
		t.Fatalf("2 回目の登録 = %+v / %+v（同じものを返すこと）", a, b)
	}
	// 別の利用者の同じ sessionId は別の記録
	c := e.mustRegister(e.viewer, tSID)
	if !c.Created || c.Transcript.ID == a.Transcript.ID {
		t.Fatalf("別の利用者の登録 = %+v", c)
	}
	bad := []RegisterTranscriptInput{
		{Agent: "copilot", VendorSessionID: tSID, OriginOS: "linux"},
		{Agent: "codex", VendorSessionID: "../x", OriginOS: "linux"},
		{Agent: "codex", VendorSessionID: "", OriginOS: "linux"},
		{Agent: "codex", VendorSessionID: strings.Repeat("a", 129), OriginOS: "linux"},
		{Agent: "codex", VendorSessionID: tSID, OriginOS: "plan9"},
		{Agent: "codex", VendorSessionID: tSID, OriginOS: "linux", MachineLabel: "a\nb"},
		{Agent: "codex", VendorSessionID: tSID, OriginOS: "linux", AgentVersion: strings.Repeat("v", 65)},
	}
	for i, in := range bad {
		in.Project = "demo"
		if _, err := e.s.RegisterTranscript(e.ctx, e.editor, in); errKind(err) != Invalid {
			t.Errorf("入力 %d = %v（400 であること）", i, err)
		}
	}
}

// file_key の形（agent ごと）と読み方。
func TestTranscriptFileKind(t *testing.T) {
	for _, c := range []struct {
		agent, key, kind string
	}{
		{"claude-code", tSID + ".jsonl", "jsonl"},
		{"claude-code", tSID + "/subagents/agent-a1.jsonl", "jsonl"},
		{"claude-code", tSID + "/subagents/agent-a1.meta.json", "json"},
		{"claude-code", tSID + "/tool-results/toolu_01.txt", "text"},
		{"codex", "2026/10/25/rollout-2026-10-25T01-02-03-" + tSID + ".jsonl", "jsonl"},
		{"claude-code", "00000000-0000-4000-8000-0000000000bb.jsonl", ""}, // ほかのセッションの名前
		{"claude-code", tSID + "/workflows/x.json", ""},
		{"claude-code", tSID + "/subagents/../x.jsonl", ""},
		{"claude-code", "/" + tSID + ".jsonl", ""},
		{"claude-code", tSID + "/subagents/.hidden.jsonl", ""},
		{"claude-code", tSID + "\\x.jsonl", ""},
		{"claude-code", tSID + "/tool-results/a\x01b", ""},
		{"claude-code", tSID + "/tool-results/" + strings.Repeat("a", 300), ""},
		{"codex", "2026/10/25/notes.jsonl", ""},
		{"codex", "C:/2026/10/25/rollout-x.jsonl", ""},
		{"codex", tSID + ".jsonl", ""},
	} {
		kind, ok := TranscriptFileKind(c.agent, tSID, c.key)
		if kind != c.kind || ok != (c.kind != "") {
			t.Errorf("TranscriptFileKind(%s, %q) = %q, %v（期待 %q）", c.agent, c.key, kind, ok, c.kind)
		}
	}
}

// 追記の入力（本文以外）の形の拒否。
func TestTranscriptChunkMetaInvalid(t *testing.T) {
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	base := func() AppendTranscriptChunkInput {
		return chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("a"))
	}
	for name, mod := range map[string]func(*AppendTranscriptChunkInput){
		"file_key":     func(in *AppendTranscriptChunkInput) { in.FileKey = "../x.jsonl" },
		"file_kind":    func(in *AppendTranscriptChunkInput) { in.FileKind = "text" },
		"encoding":     func(in *AppendTranscriptChunkInput) { in.Encoding = "br" },
		"src_offset":   func(in *AppendTranscriptChunkInput) { in.SrcOffset = -1 },
		"src_length":   func(in *AppendTranscriptChunkInput) { in.SrcLength = 0 },
		"nonce":        func(in *AppendTranscriptChunkInput) { in.Nonce = "{{LT" },
		"placeholders": func(in *AppendTranscriptChunkInput) { in.Placeholders = []string{"/home/x"} },
		"stats":        func(in *AppendTranscriptChunkInput) { in.Stats = `[1]` },
		"empty":        func(in *AppendTranscriptChunkInput) { in.Body = strings.NewReader("") },
	} {
		in := base()
		mod(&in)
		if _, err := e.appendChunk(e.editor, in); errKind(err) != Invalid {
			t.Errorf("%s = %v（400 であること）", name, err)
		}
	}
	if n := e.transcriptFiles(); n != 0 {
		t.Fatalf("拒んだ後の置き場のファイル = %d", n)
	}
	// 対照: 同じ土台の入力は通る
	e.mustAppend(e.editor, base())
}

// 運用者の消去（cause operator・理由は必須・利用者の記録をまとめて）。
func TestPurgeUserTranscripts(t *testing.T) {
	e := newTranscriptEnv(t)
	st := e.mustRegister(e.editor, tSID)
	e.mustAppend(e.editor, chunkIn(st.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("a")))
	keep := e.mustRegister(e.viewer, tSID)
	e.mustAppend(e.viewer, chunkIn(keep.Transcript.ID, `"0"`, tSID+".jsonl", 0, line("keep")))
	if _, err := e.s.PurgeUserTranscripts(e.ctx, "ed", " "); errKind(err) != Invalid {
		t.Fatalf("理由なし = %v", err)
	}
	if _, err := e.s.PurgeUserTranscripts(e.ctx, "nobody", "leak"); errKind(err) != NotFound {
		t.Fatalf("無い利用者 = %v", err)
	}
	res, err := e.s.PurgeUserTranscripts(e.ctx, "ed", "leak")
	if err != nil || len(res) != 1 || res[0].TranscriptID != st.Transcript.ID {
		t.Fatalf("運用者の消去 = %+v, %v", res, err)
	}
	var cause, via string
	var actorNull bool
	if err := e.admin.QueryRowContext(e.ctx, "SELECT cause, via, actor_user_id IS NULL FROM transcript_purges").Scan(&cause, &via, &actorNull); err != nil {
		t.Fatal(err)
	}
	if cause != "operator" || via != "admin" || !actorNull {
		t.Fatalf("運用者の消去の記録 = %s %s %v", cause, via, actorNull)
	}
	if _, f, err := e.s.OpenTranscriptChunk(e.ctx, e.viewer, keep.Transcript.ID, 1); err != nil {
		t.Fatalf("ほかの利用者の本体: %v", err)
	} else {
		f.Close()
	}
}
