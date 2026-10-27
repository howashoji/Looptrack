package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// 会話記録の REST（transcripts_api.go）と、保持期間の消去を呼ぶ定期の処理（housekeeping.go）。
// 規則の本体は service（transcripts.go）にあり、ここでは HTTP の形（状態コード・code・ヘッダ・本文）と、
// どの経路でもほかの利用者には無いものと同じ 404 になることを確かめる。中身はすべて合成の行。

// 合成の sessionId（Claude の形）。
const (
	trSID   = "00000000-0000-4000-8000-0000000000aa"
	trSID2  = "00000000-0000-4000-8000-0000000000bb"
	trNonce = "0123456789ab"
	// trSecret は合成の秘密の値（domain の規則 password=… に当たる。実在の資格情報ではない）
	trSecret = "password=Synthetic0Value9"
)

// trLine は合成の会話記録の 1 行（改行つき）。
func trLine(text string) string {
	return `{"type":"user","cwd":"{{LT:` + trNonce + `:ROOT0}}","message":{"role":"user","content":` + strconv.Quote(text) + `}}` + "\n"
}

type transcriptAPIEnv struct {
	*env
	dir                              string
	pr, off                          store.Project
	owner, peer, padmin, root, stray *apiClient
	ownerUser, peerUser, padminUser  store.User
}

func newTranscriptAPIEnv(t *testing.T) *transcriptAPIEnv {
	t.Helper()
	dir := t.TempDir()
	e := newEnvWith(t, func(c *Config) { c.AllowNoAdmin = true; c.AttachDir = t.TempDir(); c.TranscriptDir = dir })
	ctx := context.Background()
	a := &transcriptAPIEnv{env: e, dir: dir, pr: e.project("trs"), off: e.project("troff")}
	if err := store.SetRules(ctx, e.db, a.pr.ID, []byte(`{"session":{"transcripts":true}}`)); err != nil {
		t.Fatal(err)
	}
	member := func(login, role string, projects ...store.Project) store.User {
		u := e.user(login, login+"-password-1", "member")
		for _, p := range projects {
			if err := store.SetMember(ctx, e.db, p.ID, u.ID, role); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	a.ownerUser = member("tr-owner", "viewer", a.pr, a.off)
	a.peerUser = member("tr-peer", "editor", a.pr)
	a.padminUser = member("tr-padmin", "admin", a.pr)
	a.owner, a.peer, a.padmin = e.apiAs(a.ownerUser), e.apiAs(a.peerUser), e.apiAs(a.padminUser)
	rootUser := e.user("tr-root", "root-password-1", "admin")
	if err := store.SetMember(ctx, e.db, a.pr.ID, rootUser.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	a.root = e.apiAs(rootUser)
	a.stray = e.apiAs(member("tr-stray", "viewer")) // どのプロジェクトにも参加していない
	return a
}

// req は本文をそのまま送る要求（ヘッダは名前と値の組）。
func (a *transcriptAPIEnv) req(c *apiClient, method, path string, body []byte, header ...string) (int, http.Header, []byte) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, a.srv.URL+"/im/api/v1"+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept-Language", "ja")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := c.c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

type trStateResp struct {
	Transcript transcriptJSON `json:"transcript"`
	Created    bool           `json:"created"`
}

type trErr struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Current json.RawMessage `json:"current"`
	} `json:"error"`
}

func (a *transcriptAPIEnv) decodeErr(b []byte) trErr {
	a.t.Helper()
	var e trErr
	if err := json.Unmarshal(b, &e); err != nil || e.Error.Code == "" || e.Error.Message == "" {
		a.t.Fatalf("誤りの本文: %v %s", err, b)
	}
	return e
}

// register は会話記録を登録し、状態コードと応答を返す。
func (a *transcriptAPIEnv) register(c *apiClient, slug, sid string) (int, http.Header, []byte) {
	a.t.Helper()
	body, _ := json.Marshal(map[string]string{"agent": "claude-code", "vendor_session_id": sid, "machine_label": "box-1",
		"origin_os": "linux", "agent_version": "2.1.0"})
	return a.req(c, "POST", "/projects/"+slug+"/transcripts", body, "Content-Type", "application/json")
}

func (a *transcriptAPIEnv) mustRegister(c *apiClient, sid string, want int) transcriptJSON {
	a.t.Helper()
	code, _, b := a.register(c, a.pr.Slug, sid)
	if code != want {
		a.t.Fatalf("登録 %s: %d %s, want %d", sid, code, b, want)
	}
	var out trStateResp
	if err := json.Unmarshal(b, &out); err != nil || out.Transcript.ID == 0 {
		a.t.Fatalf("登録の応答: %v %s", err, b)
	}
	return out.Transcript
}

// appendChunk はチャンクを足す。header は追加・上書きするヘッダ（値が空なら送らない）。
func (a *transcriptAPIEnv) appendChunk(c *apiClient, id int64, ifMatch, key string, offset int64, body []byte, header ...string) (int, http.Header, []byte) {
	a.t.Helper()
	h := map[string]string{
		"If-Match":                   ifMatch,
		transcriptFileHeader:         url.PathEscape(key),
		transcriptOffsetHeader:       strconv.FormatInt(offset, 10),
		transcriptLengthHeader:       strconv.Itoa(len(body)),
		transcriptNonceHeader:        trNonce,
		transcriptPlaceholdersHeader: `["ROOT0"]`,
		transcriptStatsHeader:        `{"lines":1}`,
		"Content-Type":               "application/octet-stream",
	}
	for i := 0; i+1 < len(header); i += 2 {
		h[header[i]] = header[i+1]
	}
	var pairs []string
	for k, v := range h {
		if v != "" {
			pairs = append(pairs, k, v)
		}
	}
	return a.req(c, "POST", "/transcripts/"+strconv.FormatInt(id, 10)+"/chunks", body, pairs...)
}

func (a *transcriptAPIEnv) mustAppend(c *apiClient, id int64, ifMatch, key string, offset int64, body []byte, header ...string) transcriptChunkJSON {
	a.t.Helper()
	code, h, b := a.appendChunk(c, id, ifMatch, key, offset, body, header...)
	if code != http.StatusCreated {
		a.t.Fatalf("追記 %s: %d %s", key, code, b)
	}
	var out struct {
		Chunk      transcriptChunkJSON `json:"chunk"`
		Transcript transcriptJSON      `json:"transcript"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Chunk.Seq == 0 || out.Transcript.Seq != out.Chunk.Seq {
		a.t.Fatalf("追記の応答: %v %s", err, b)
	}
	if got := h.Get("ETag"); got != `"`+strconv.FormatInt(out.Chunk.Seq, 10)+`"` {
		a.t.Errorf("追記の ETag = %q, want 今の seq %d", got, out.Chunk.Seq)
	}
	return out.Chunk
}

func (a *transcriptAPIEnv) count(q string, args ...any) int {
	a.t.Helper()
	var n int
	if err := a.db.QueryRow(q, args...).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

// bodies は会話記録の置き場の本体（一時ファイルを除く）の数。
func (a *transcriptAPIEnv) bodies() int {
	a.t.Helper()
	n := 0
	_ = filepath.WalkDir(a.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if rel, _ := filepath.Rel(a.dir, p); !strings.HasPrefix(filepath.ToSlash(rel), ".tmp/") {
			n++
		}
		return nil
	})
	return n
}

func (a *transcriptAPIEnv) setLimit(name string, v int64) {
	a.t.Helper()
	if _, _, err := store.SetTranscriptLimit(context.Background(), a.db, name, v, store.SettingChange{Via: "command", Note: "test"}); err != nil {
		a.t.Fatal(err)
	}
}

// 登録: 新しく作れば 201、同じ (利用者, agent, セッション ID) をもう一度送れば 200 で同じ記録を返す。ルールが切なら 422、
// 参加していないプロジェクトは無いものと同じ 404。ほかの利用者が同じセッション ID を送っても別の記録になる（持ち主ごと）。
func TestTranscriptAPIRegister(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	code, h, b := a.register(a.owner, a.pr.Slug, trSID)
	var first trStateResp
	if code != http.StatusCreated || json.Unmarshal(b, &first) != nil || !first.Created || first.Transcript.Seq != 0 ||
		first.Transcript.Project != a.pr.Slug || !first.Transcript.TranscriptsEnabled || first.Transcript.Files == nil || h.Get("ETag") != `"0"` {
		t.Fatalf("最初の登録: %d %s ETag %q", code, b, h.Get("ETag"))
	}
	code, _, b = a.register(a.owner, a.pr.Slug, trSID)
	var again trStateResp
	if code != http.StatusOK || json.Unmarshal(b, &again) != nil || again.Created || again.Transcript.ID != first.Transcript.ID {
		t.Fatalf("同じ登録の 2 回目: %d %s, want 200 で同じ ID %d", code, b, first.Transcript.ID)
	}
	if n := a.count("SELECT COUNT(*) FROM transcripts"); n != 1 {
		t.Fatalf("2 回登録した後の行 = %d, want 1", n)
	}
	// ほかの利用者が同じセッション ID を送ると、その人の別の記録になる（先の記録は返さない）
	other := a.mustRegister(a.peer, trSID, http.StatusCreated)
	if other.ID == first.Transcript.ID {
		t.Fatalf("ほかの利用者の登録が先の記録 %d を返した", first.Transcript.ID)
	}
	// ルールが切のプロジェクト（owner は参加している）
	if code, _, b := a.register(a.owner, a.off.Slug, trSID2); code != http.StatusUnprocessableEntity || a.decodeErr(b).Error.Code != "transcript_rule_off" {
		t.Errorf("ルールが切: %d %s, want 422 transcript_rule_off", code, b)
	}
	// 参加していないプロジェクトと無いプロジェクトは同じ 404
	for _, slug := range []string{a.pr.Slug, "no-such-project"} {
		if code, _, b := a.register(a.stray, slug, trSID2); code != http.StatusNotFound || a.decodeErr(b).Error.Code != "not_found" {
			t.Errorf("参加していない人の登録 %s: %d %s, want 404", slug, code, b)
		}
	}
	// 入力の誤りは 400（agent が無い値）
	body := []byte(`{"agent":"other-ai","vendor_session_id":"` + trSID2 + `","machine_label":"m","origin_os":"linux","agent_version":"1"}`)
	if code, _, b := a.req(a.owner, "POST", "/projects/"+a.pr.Slug+"/transcripts", body, "Content-Type", "application/json"); code != http.StatusBadRequest || a.decodeErr(b).Error.Code != "invalid_argument" {
		t.Errorf("知らない agent: %d %s, want 400", code, b)
	}
}

// 追記: If-Match が無ければ 428、違えば 409 transcript_seq（error.current に今の seq）、中身の検査に外れれば 422、
// 上限を超えれば 413。拒否のときは本体もメタデータも残らない。対照: 正しい要求は 201 で連番が 1 つ進む。
func TestTranscriptAPIAppendCodes(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	tr := a.mustRegister(a.owner, trSID, http.StatusCreated)
	key := trSID + ".jsonl"
	l1 := []byte(trLine("first"))
	chunks := func() int { return a.count("SELECT COUNT(*) FROM transcript_chunks") }

	// 428: If-Match が無い
	if code, _, b := a.appendChunk(a.owner, tr.ID, "", key, 0, l1); code != http.StatusPreconditionRequired || a.decodeErr(b).Error.Code != "transcript_seq_required" {
		t.Fatalf("If-Match なし: %d %s, want 428 transcript_seq_required", code, b)
	}
	// 対照: 正しい要求は 201 で seq 1
	c1 := a.mustAppend(a.owner, tr.ID, `"0"`, key, 0, l1)
	if c1.Seq != 1 || c1.FileKind != "jsonl" || c1.LineCount != 1 || c1.Encoding != "identity" || string(c1.Placeholders) != `["ROOT0"]` {
		t.Fatalf("追記の結果: %+v", c1)
	}
	// 409: 古い連番。error.current に今の seq（数）
	l2 := []byte(trLine("second"))
	code, _, b := a.appendChunk(a.owner, tr.ID, `"0"`, key, int64(len(l1)), l2)
	if code != http.StatusConflict {
		t.Fatalf("古い連番: %d %s, want 409", code, b)
	}
	if e := a.decodeErr(b); e.Error.Code != "transcript_seq" || string(e.Error.Current) != "1" {
		t.Fatalf("古い連番の本文: code %q current %s, want transcript_seq と 1", e.Error.Code, e.Error.Current)
	}
	// 409: 送った長さと違う位置（抜け）
	if code, _, b := a.appendChunk(a.owner, tr.ID, `"1"`, key, int64(len(l1))+5, l2); code != http.StatusConflict || a.decodeErr(b).Error.Code != "transcript_offset" {
		t.Fatalf("抜けのある位置: %d %s, want 409 transcript_offset", code, b)
	}
	// 422: マスクしていない秘密が値に残っている（値はサーバの文面に出ない）
	secret := []byte(trLine("token " + trSecret))
	code, _, b = a.appendChunk(a.owner, tr.ID, `"1"`, key, int64(len(l1)), secret)
	if code != http.StatusUnprocessableEntity || a.decodeErr(b).Error.Code != "transcript_secret" || strings.Contains(string(b), "Synthetic0Value9") {
		t.Fatalf("秘密の残った行: %d %s, want 422 transcript_secret で値を出さない", code, b)
	}
	// 422: JSON として読めない行
	if code, _, b := a.appendChunk(a.owner, tr.ID, `"1"`, key, int64(len(l1)), []byte("not json\n")); code != http.StatusUnprocessableEntity || a.decodeErr(b).Error.Code != "transcript_unreadable" {
		t.Fatalf("読めない行: %d %s, want 422 transcript_unreadable", code, b)
	}
	// 400: file_key の形が違う（ほかのセッションの名前）・ヘッダが読めない
	if code, _, b := a.appendChunk(a.owner, tr.ID, `"1"`, trSID2+".jsonl", 0, l2); code != http.StatusBadRequest || a.decodeErr(b).Error.Code != "transcript_file_key" {
		t.Fatalf("ほかのセッションの file_key: %d %s, want 400 transcript_file_key", code, b)
	}
	for _, hd := range []string{transcriptOffsetHeader, transcriptLengthHeader, transcriptPlaceholdersHeader} {
		if code, _, b := a.appendChunk(a.owner, tr.ID, `"1"`, key, int64(len(l1)), l2, hd, "x"); code != http.StatusBadRequest || a.decodeErr(b).Error.Code != "invalid_argument" {
			t.Errorf("読めない %s: %d %s, want 400", hd, code, b)
		}
	}
	if code, _, b := a.appendChunk(a.owner, tr.ID, `"1"`, key, int64(len(l1)), l2, transcriptFileHeader, "%zz"); code != http.StatusBadRequest {
		t.Errorf("パーセント符号が壊れた file_key: %d %s, want 400", code, b)
	}
	// 413: 本文が transcript_max_chunk_bytes を超える（Content-Length で読まずに拒む）
	a.setLimit(store.SettingTranscriptMaxChunk, 64)
	big := []byte(trLine(strings.Repeat("x", 200)))
	code, h, b := a.appendChunk(a.owner, tr.ID, `"1"`, key, int64(len(l1)), big)
	if code != http.StatusRequestEntityTooLarge || a.decodeErr(b).Error.Code != "transcript_too_large" {
		t.Fatalf("上限を超える本文: %d %s, want 413 transcript_too_large", code, b)
	}
	if h.Get("Content-Security-Policy") != attachCSP {
		t.Errorf("413 の応答の CSP = %q", h.Get("Content-Security-Policy"))
	}
	a.setLimit(store.SettingTranscriptMaxChunk, store.DefaultTranscriptMaxChunk)
	// 422: 1 利用者の上限
	a.setLimit(store.SettingTranscriptMaxUser, int64(len(l1))+4)
	if code, _, b := a.appendChunk(a.owner, tr.ID, `"1"`, key, int64(len(l1)), l2); code != http.StatusUnprocessableEntity || a.decodeErr(b).Error.Code != "transcript_quota" {
		t.Fatalf("1 利用者の上限: %d %s, want 422 transcript_quota", code, b)
	}
	a.setLimit(store.SettingTranscriptMaxUser, store.DefaultTranscriptMaxUser)
	if n := chunks(); n != 1 {
		t.Fatalf("拒否の後のチャンク = %d, want 1（拒否はメタデータを残さない）", n)
	}
	if n := a.bodies(); n != 1 {
		t.Fatalf("拒否の後の本体 = %d, want 1（拒否は本体を残さない）", n)
	}
	// 対照: 今の連番と位置なら通る
	if c2 := a.mustAppend(a.owner, tr.ID, `"1"`, key, int64(len(l1)), l2); c2.Seq != 2 || c2.SrcOffset != int64(len(l1)) {
		t.Fatalf("2 つ目のチャンク: %+v", c2)
	}
}

// Content-Encoding: gzip の本文は gzip のまま置き、本体の応答は置いた形のまま X-Looptrack-Transcript-Encoding で形を知らせる。
// 本体の応答には安全のヘッダ（CSP sandbox・nosniff・Content-Disposition: attachment）を付け、Content-Encoding は付けない
// （要求が Accept-Encoding: gzip を持っていても。HTTP の層で勝手に伸ばされないように）。
func TestTranscriptAPIChunkBodyHeaders(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	tr := a.mustRegister(a.owner, trSID, http.StatusCreated)
	key := trSID + ".jsonl"
	plain := []byte(trLine("identity"))
	a.mustAppend(a.owner, tr.ID, `"0"`, key, 0, plain)
	gzLine := []byte(trLine("gzip"))
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(gzLine)
	zw.Close()
	c2 := a.mustAppend(a.owner, tr.ID, `"1"`, key, int64(len(plain)), zb.Bytes(), "Content-Encoding", "gzip", transcriptLengthHeader, strconv.Itoa(len(gzLine)))
	if c2.Encoding != "gzip" || c2.Size != int64(zb.Len()) || c2.SrcLength != int64(len(gzLine)) {
		t.Fatalf("gzip のチャンク: %+v", c2)
	}
	for _, tc := range []struct {
		seq      string
		encoding string
		want     []byte
	}{{"1", "identity", plain}, {"2", "gzip", zb.Bytes()}} {
		code, h, b := a.req(a.owner, "GET", "/transcripts/"+strconv.FormatInt(tr.ID, 10)+"/chunks/"+tc.seq, nil, "Accept-Encoding", "gzip")
		if code != http.StatusOK || !bytes.Equal(b, tc.want) {
			t.Fatalf("本体 %s: %d %q, want 置いた形のまま", tc.seq, code, b)
		}
		checks := map[string]string{
			"Content-Security-Policy": "sandbox; default-src 'none'",
			"X-Content-Type-Options":  "nosniff",
			"Content-Disposition":     "attachment",
			transcriptEncodingHeader:  tc.encoding,
			"Content-Type":            "application/octet-stream",
		}
		for name, want := range checks {
			if got := h.Get(name); got != want {
				t.Errorf("本体 %s の %s = %q, want %q", tc.seq, name, got, want)
			}
		}
		if _, ok := h["Content-Encoding"]; ok {
			t.Errorf("本体 %s に Content-Encoding が付いた: %q", tc.seq, h.Get("Content-Encoding"))
		}
	}
	// 対照: 同じ要求の JSON の応答（チャンクの一覧）は Accept-Encoding: gzip で圧縮される（Content-Encoding を見ている検査が生きている）
	_, h, _ := a.req(a.owner, "GET", "/transcripts/"+strconv.FormatInt(tr.ID, 10), nil, "Accept-Encoding", "gzip")
	if h.Get("Content-Encoding") != "gzip" {
		t.Fatalf("前提が崩れています: JSON の応答が圧縮されない（Content-Encoding %q）", h.Get("Content-Encoding"))
	}
	// 無い seq・読めない seq は 404
	for _, seq := range []string{"3", "0", "x"} {
		if code, _, b := a.req(a.owner, "GET", "/transcripts/"+strconv.FormatInt(tr.ID, 10)+"/chunks/"+seq, nil); code != http.StatusNotFound || a.decodeErr(b).Error.Code != "not_found" {
			t.Errorf("チャンク %s: %d %s, want 404", seq, code, b)
		}
	}
}

// ほかの利用者（同じプロジェクトの editor・プロジェクトの管理者・利用者の役割 admin・参加していない人）には、
// どの経路でも無い ID と同じ 404 を返し、何も変えない。一覧には載らない。対照: 持ち主は同じ経路で通る。
func TestTranscriptAPIOtherUsersGet404(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	tr := a.mustRegister(a.owner, trSID, http.StatusCreated)
	key := trSID + ".jsonl"
	l1 := []byte(trLine("mine"))
	a.mustAppend(a.owner, tr.ID, `"0"`, key, 0, l1)
	id := strconv.FormatInt(tr.ID, 10)
	missing := strconv.FormatInt(tr.ID+1000, 10)
	type route struct {
		name, method, path string
		body               func(id string) []byte
		header             []string
	}
	routes := []route{
		{"追記", "POST", "/transcripts/%s/chunks", func(string) []byte { return []byte(trLine("theirs")) },
			[]string{"If-Match", `"1"`, transcriptFileHeader, url.PathEscape(key), transcriptOffsetHeader, strconv.Itoa(len(l1)),
				transcriptLengthHeader, strconv.Itoa(len(trLine("theirs"))), transcriptNonceHeader, trNonce}},
		{"取得", "GET", "/transcripts/%s", nil, nil},
		{"本体", "GET", "/transcripts/%s/chunks/1", nil, nil},
		{"消去", "POST", "/transcripts/%s/purge", func(string) []byte { return []byte(`{"reason":"誤り"}`) }, []string{"Content-Type", "application/json"}},
	}
	call := func(c *apiClient, r route, target string) (int, []byte) {
		var body []byte
		if r.body != nil {
			body = r.body(target)
		}
		code, _, b := a.req(c, r.method, strings.Replace(r.path, "%s", target, 1), body, r.header...)
		return code, b
	}
	for _, who := range []struct {
		name string
		c    *apiClient
	}{{"同じプロジェクトの editor", a.peer}, {"プロジェクトの管理者", a.padmin}, {"利用者の役割 admin", a.root}, {"参加していない人", a.stray}} {
		for _, r := range routes {
			code, b := call(who.c, r, id)
			mcode, mb := call(who.c, r, missing)
			if code != http.StatusNotFound || a.decodeErr(b).Error.Code != "not_found" || code != mcode ||
				a.decodeErr(b).Error.Code != a.decodeErr(mb).Error.Code {
				t.Errorf("%s の%s: %d %s, want 無い ID（%d %s）と同じ 404", who.name, r.name, code, b, mcode, mb)
			}
		}
		code, _, b := a.req(who.c, "GET", "/transcripts", nil)
		var list struct {
			Transcripts []transcriptSummaryJSON `json:"transcripts"`
		}
		if code != http.StatusOK || json.Unmarshal(b, &list) != nil {
			t.Fatalf("%s の一覧: %d %s", who.name, code, b)
		}
		for _, s := range list.Transcripts {
			if s.ID == tr.ID {
				t.Errorf("%s の一覧に持ち主の記録 %d が載った", who.name, tr.ID)
			}
		}
	}
	if n := a.count("SELECT COUNT(*) FROM transcript_chunks"); n != 1 {
		t.Errorf("ほかの利用者の要求の後のチャンク = %d, want 1", n)
	}
	if n := a.count("SELECT COUNT(*) FROM transcript_purges"); n != 0 {
		t.Errorf("ほかの利用者の要求の後の消去の記録 = %d, want 0", n)
	}
	// 対照: 持ち主は同じ経路で通る（消去は最後）
	for _, r := range routes {
		want := http.StatusOK
		if r.method == "POST" && strings.HasSuffix(r.path, "/chunks") {
			want = http.StatusCreated
		}
		if code, b := call(a.owner, r, id); code != want {
			t.Errorf("持ち主の%s: %d %s, want %d", r.name, code, b, want)
		}
	}
}

// 一覧（自分のものだけ・?project= で絞る）と取得（チャンクの一覧）と消去。消去の後の本体は 422 transcript_purged で、
// 同じ会話を最初から送り直せる（送った長さが 0 に戻る）。
func TestTranscriptAPIListGetPurge(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	tr := a.mustRegister(a.owner, trSID, http.StatusCreated)
	key := trSID + ".jsonl"
	l1 := []byte(trLine("one"))
	a.mustAppend(a.owner, tr.ID, `"0"`, key, 0, l1)
	sub := trSID + "/subagents/agent-1.jsonl"
	a.mustAppend(a.owner, tr.ID, `"1"`, sub, 0, []byte(trLine("sub")))
	a.mustRegister(a.peer, trSID2, http.StatusCreated)

	var list struct {
		Transcripts []transcriptSummaryJSON `json:"transcripts"`
		Count       int                     `json:"count"`
	}
	a.owner.json(200, "GET", "/transcripts?project="+a.pr.Slug, nil, &list)
	if list.Count != 1 || list.Transcripts[0].ID != tr.ID || list.Transcripts[0].Chunks != 2 || list.Transcripts[0].Files != 2 ||
		list.Transcripts[0].Seq != 2 || list.Transcripts[0].Purged || list.Transcripts[0].LastChunkAt == "" {
		t.Fatalf("一覧: %+v", list)
	}
	a.owner.json(200, "GET", "/transcripts?project=no-such-project", nil, &list)
	if list.Count != 0 || list.Transcripts == nil {
		t.Fatalf("無いプロジェクトで絞った一覧: %+v, want 空の配列", list)
	}
	var got struct {
		Transcript transcriptJSON        `json:"transcript"`
		Chunks     []transcriptChunkJSON `json:"chunks"`
	}
	h := a.owner.json(200, "GET", "/transcripts/"+strconv.FormatInt(tr.ID, 10), nil, &got)
	if len(got.Chunks) != 2 || got.Transcript.Seq != 2 || len(got.Transcript.Files) != 2 || h.Get("ETag") != `"2"` ||
		got.Chunks[1].FileKind != "jsonl" || !strings.HasSuffix(got.Chunks[0].URL, "/api/v1/transcripts/"+strconv.FormatInt(tr.ID, 10)+"/chunks/1") {
		t.Fatalf("取得: %+v ETag %q", got, h.Get("ETag"))
	}

	var purged struct {
		TranscriptID int64   `json:"transcript_id"`
		Changed      bool    `json:"changed"`
		ChunkIDs     []int64 `json:"chunk_ids"`
		Removed      int     `json:"removed"`
		Message      string  `json:"message"`
	}
	a.owner.json(200, "POST", "/transcripts/"+strconv.FormatInt(tr.ID, 10)+"/purge", map[string]string{"reason": "要らない"}, &purged)
	if !purged.Changed || len(purged.ChunkIDs) != 2 || purged.Removed != 2 || purged.Message == "" {
		t.Fatalf("消去: %+v", purged)
	}
	if n := a.bodies(); n != 0 {
		t.Fatalf("消去の後の本体 = %d, want 0", n)
	}
	code, _, b := a.req(a.owner, "GET", "/transcripts/"+strconv.FormatInt(tr.ID, 10)+"/chunks/1", nil)
	if code != http.StatusUnprocessableEntity || a.decodeErr(b).Error.Code != "transcript_purged" {
		t.Fatalf("消去済みの本体: %d %s, want 422 transcript_purged", code, b)
	}
	// 2 回目の消去は何も変えない
	a.owner.json(200, "POST", "/transcripts/"+strconv.FormatInt(tr.ID, 10)+"/purge", nil, &purged)
	if purged.Changed || len(purged.ChunkIDs) != 0 {
		t.Fatalf("2 回目の消去: %+v", purged)
	}
	// 送り直し: 送った長さは 0 に戻り、連番は続きから
	again := a.mustRegister(a.owner, trSID, http.StatusOK)
	if again.Seq != 2 || len(again.Files) != 0 {
		t.Fatalf("消去の後の登録: %+v", again)
	}
	if c := a.mustAppend(a.owner, tr.ID, `"2"`, key, 0, l1); c.Seq != 3 {
		t.Fatalf("送り直し: %+v", c)
	}
	a.owner.json(200, "GET", "/transcripts", nil, &list)
	if list.Count != 1 || list.Transcripts[0].Purges != 1 || list.Transcripts[0].Purged {
		t.Fatalf("送り直した後の一覧: %+v", list)
	}
}

// 置き場が設定されていないサーバでは、会話記録の経路だけが 422 transcripts_unavailable になる（ほかの API は動く）。
func TestTranscriptAPIUnavailable(t *testing.T) {
	e := newEnvWith(t, func(c *Config) { c.AllowNoAdmin = true })
	pr := e.project("trnone")
	if err := store.SetRules(context.Background(), e.db, pr.ID, []byte(`{"session":{"transcripts":true}}`)); err != nil {
		t.Fatal(err)
	}
	u := e.user("tr-none", "tr-none-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	c := e.apiAs(u)
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/projects/trnone/transcripts", map[string]string{"agent": "claude-code", "vendor_session_id": trSID, "machine_label": "m", "origin_os": "linux", "agent_version": "1"}},
		{"GET", "/transcripts", nil},
		{"GET", "/transcripts/1", nil},
	} {
		if e := c.fail(http.StatusUnprocessableEntity, r.method, r.path, r.body); e.Error.Code != "transcripts_unavailable" {
			t.Errorf("%s %s: code %q, want transcripts_unavailable", r.method, r.path, e.Error.Code)
		}
	}
	// 対照: ほかの API は動く
	c.json(201, "POST", "/projects/trnone/issues", map[string]any{"title": "動く"}, nil)
}

// 経路の無い会話記録の下の要求は unknown_api（資源の 404 とは別）で、CSP sandbox は会話記録の経路の認証の誤りにも付く。
func TestTranscriptAPIUnknownAndUnauthenticated(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	if code, _, b := a.req(a.owner, "GET", "/transcripts/1/nothing", nil); code != http.StatusNotFound || a.decodeErr(b).Error.Code != "unknown_api" {
		t.Errorf("無い経路: %d %s, want 404 unknown_api", code, b)
	}
	for _, p := range []string{"/im/api/v1/transcripts", "/im/api/v1/transcripts/1/chunks/1", "/im/api/v1/projects/trs/transcripts"} {
		res, _ := a.get(&http.Client{}, p)
		if res.StatusCode != http.StatusUnauthorized || res.Header.Get("Content-Security-Policy") != attachCSP {
			t.Errorf("認証なしの %s: %d CSP %q, want 401 と %q", p, res.StatusCode, res.Header.Get("Content-Security-Policy"), attachCSP)
		}
	}
	// 対照: 会話記録の経路でない API の 401 は既定の CSP のまま
	res, _ := a.get(&http.Client{}, "/im/api/v1/projects")
	if res.StatusCode != http.StatusUnauthorized || res.Header.Get("Content-Security-Policy") == attachCSP {
		t.Errorf("前提が崩れています: 会話記録でない経路の CSP = %q", res.Header.Get("Content-Security-Policy"))
	}
}

// 定期の処理の 1 回分（housekeepOnce）が保持期間の消去を呼ぶ: 最後のチャンクから保持日数を過ぎた会話の本体が消え、
// 期限内の会話の本体は残る（消去の記録は cause retention・via system）。対照: service を渡さない回は何も消さない。
func TestHousekeepPurgesExpiredTranscripts(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	now := a.clock.Now()
	set := func(ts time.Time) { a.clock.mu.Lock(); a.clock.t = ts; a.clock.mu.Unlock() }

	old := a.mustRegister(a.owner, trSID, http.StatusCreated)
	set(now.Add(-100 * 24 * time.Hour))
	a.mustAppend(a.owner, old.ID, `"0"`, trSID+".jsonl", 0, []byte(trLine("old")))
	fresh := a.mustRegister(a.peer, trSID2, http.StatusCreated)
	set(now.Add(-24 * time.Hour))
	a.mustAppend(a.peer, fresh.ID, `"0"`, trSID2+".jsonl", 0, []byte(trLine("fresh")))
	set(now)
	if n := a.bodies(); n != 2 {
		t.Fatalf("準備の本体 = %d, want 2", n)
	}

	// 対照: service を渡さない回（会話記録を扱わない）は何も消さない
	housekeepOnce(ctx, a.app, logger, nil)
	if n := a.bodies(); n != 2 || a.count("SELECT COUNT(*) FROM transcript_purges") != 0 {
		t.Fatalf("service なしの回で消えた: 本体 %d", n)
	}

	housekeepOnce(ctx, a.app, logger, a.s.Service())
	if n := a.bodies(); n != 1 {
		t.Fatalf("定期の処理の後の本体 = %d, want 1（期限切れだけが消える）", n)
	}
	var cause, via string
	var actorNull bool
	if err := a.db.QueryRow("SELECT cause, via, actor_user_id IS NULL FROM transcript_purges WHERE transcript_id = ?", old.ID).Scan(&cause, &via, &actorNull); err != nil {
		t.Fatalf("期限切れの会話の消去の記録: %v", err)
	}
	if cause != "retention" || via != "system" || !actorNull {
		t.Errorf("消去の記録 = %s %s actor NULL=%v, want retention system true", cause, via, actorNull)
	}
	if n := a.count("SELECT COUNT(*) FROM transcript_purges WHERE transcript_id = ?", fresh.ID); n != 0 {
		t.Errorf("期限内の会話に消去の記録 %d 件", n)
	}
	// 期限内の本体はそのまま読める・期限切れの本体は消去済み
	if code, _, b := a.req(a.peer, "GET", "/transcripts/"+strconv.FormatInt(fresh.ID, 10)+"/chunks/1", nil); code != http.StatusOK || string(b) != trLine("fresh") {
		t.Errorf("期限内の本体: %d %q", code, b)
	}
	if code, _, b := a.req(a.owner, "GET", "/transcripts/"+strconv.FormatInt(old.ID, 10)+"/chunks/1", nil); code != http.StatusUnprocessableEntity || a.decodeErr(b).Error.Code != "transcript_purged" {
		t.Errorf("期限切れの本体: %d %s, want 422 transcript_purged", code, b)
	}
	// もう一度回しても記録は増えない
	housekeepOnce(ctx, a.app, logger, a.s.Service())
	if n := a.count("SELECT COUNT(*) FROM transcript_purges"); n != 1 {
		t.Errorf("2 回目の後の消去の記録 = %d, want 1", n)
	}
}
