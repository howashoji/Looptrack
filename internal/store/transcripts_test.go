package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 会話記録の表（マイグレーション 0009）のテスト。testutil.AppDB を使うので store_test のパッケージに置く
// （testutil が store を読むため、store のパッケージの中からは使えない）。
// DB は LOOPTRACK_TEST_DB で選ぶ: MySQL なら本番と同じ権限（deploy/grants.sql）のアプリ用の利用者、SQLite ならトリガで追記専用を守る。

type transcriptEnv struct {
	t          *testing.T
	ctx        context.Context
	admin, app *sql.DB
	alice, bob int64
	demo, misc int64
}

func newTranscriptEnv(t *testing.T) *transcriptEnv {
	t.Helper()
	admin, app := testutil.AppDB(t)
	e := &transcriptEnv{t: t, ctx: context.Background(), admin: admin, app: app}
	var err error
	if e.demo, err = store.CreateProject(e.ctx, admin, store.Project{Slug: "demo", Prefix: "DEMO", Width: 4, Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if e.misc, err = store.CreateProject(e.ctx, admin, store.Project{Slug: "misc", Prefix: "MISC", Width: 4, Name: "misc"}); err != nil {
		t.Fatal(err)
	}
	if e.alice, err = store.CreateUser(e.ctx, admin, "alice", "alice", "x", "member"); err != nil {
		t.Fatal(err)
	}
	if e.bob, err = store.CreateUser(e.ctx, admin, "bob", "bob", "x", "member"); err != nil {
		t.Fatal(err)
	}
	return e
}

// transcript はアプリ用の利用者で会話記録を 1 つ登録する。
func (e *transcriptEnv) transcript(user, project int64, vendor string) int64 {
	e.t.Helper()
	id, err := store.InsertTranscript(e.ctx, e.app, store.Transcript{UserID: user, ProjectID: project, Agent: store.TranscriptAgentClaude,
		VendorSessionID: vendor, MachineLabel: "laptop", OriginOS: "linux", AgentVersion: "2.1.0", Via: "cli"})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// chunk はアプリ用の利用者でチャンクを 1 つ足す。
func (e *transcriptEnv) chunk(transcript, seq int64, file string, offset, length, size int64, sha string) (int64, error) {
	return store.InsertTranscriptChunk(e.ctx, e.app, store.TranscriptChunk{TranscriptID: transcript, Seq: seq, FileKey: file,
		FileKind: store.TranscriptFileJSONL, SrcOffset: offset, SrcLength: length, LineCount: 3, SHA256: sha, Size: size,
		Encoding: store.TranscriptEncodingGzip, Nonce: "0123456789ab", Placeholders: `["root"]`, Stats: `{"masked":{}}`, Via: "cli"})
}

func (e *transcriptEnv) mustChunk(transcript, seq int64, file string, offset, length, size int64, sha string) int64 {
	e.t.Helper()
	id, err := e.chunk(transcript, seq, file, offset, length, size, sha)
	if err != nil {
		e.t.Fatalf("チャンク %d: %v", seq, err)
	}
	return id
}

func (e *transcriptEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.admin.QueryRowContext(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func sha(c byte) string { return strings.Repeat(string(c), 64) }

// 受け入れ条件: 3 つの表の UPDATE・DELETE は、アプリ用の利用者では store.IsAppendOnlyViolation が true になる
// （SQLite はトリガ、MySQL は deploy/grants.sql の権限）。
// 対照: 同じ接続で 3 つの表への INSERT は通り、追記専用でない表（users）の UPDATE も通る（接続そのものが書けないのではない）。
func TestTranscriptTablesAreAppendOnly(t *testing.T) {
	e := newTranscriptEnv(t)
	tid := e.transcript(e.alice, e.demo, "s-1")
	free := e.transcript(e.alice, e.demo, "s-free") // チャンク・消去から指されない行（外部キーで止まったのを読み違えないため）
	cid := e.mustChunk(tid, 1, "s-1.jsonl", 0, 100, 40, sha('a'))
	if err := store.InsertTranscriptPurge(e.ctx, e.app, store.TranscriptPurge{TranscriptID: tid, ThroughChunkID: cid,
		Cause: store.TranscriptPurgeOwner, ActorUserID: e.alice, Via: "cli", Reason: "誤って送った"}); err != nil {
		t.Fatalf("前提が崩れています: アプリ用の利用者で消去の記録（INSERT）を足せない: %v", err)
	}
	if e.count("SELECT COUNT(*) FROM transcripts") != 2 || e.count("SELECT COUNT(*) FROM transcript_chunks") != 1 || e.count("SELECT COUNT(*) FROM transcript_purges") != 1 {
		t.Fatal("前提が崩れています: 3 つの表の行の数が違う")
	}
	want := "command denied"
	if testutil.SQLite() {
		want = "append-only"
	}
	for _, c := range []struct {
		stmt string
		arg  int64
	}{
		{"UPDATE transcripts SET machine_label = 'x' WHERE id = ?", free},
		{"DELETE FROM transcripts WHERE id = ?", free},
		{"UPDATE transcript_chunks SET size = 1 WHERE id = ?", cid},
		{"DELETE FROM transcript_chunks WHERE id = ?", cid},
		{"UPDATE transcript_purges SET reason = 'x' WHERE transcript_id = ?", tid},
		{"DELETE FROM transcript_purges WHERE transcript_id = ?", tid},
	} {
		_, err := e.app.ExecContext(e.ctx, c.stmt, c.arg)
		if !store.IsAppendOnlyViolation(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q で拒否（IsAppendOnlyViolation）", c.stmt, err, want)
		}
	}
	if e.count("SELECT COUNT(*) FROM transcripts WHERE machine_label = 'laptop'") != 2 || e.count("SELECT COUNT(*) FROM transcript_chunks WHERE size = 40") != 1 ||
		e.count("SELECT COUNT(*) FROM transcript_purges WHERE reason <> 'x'") != 1 {
		t.Error("行が書き換わった・消えた")
	}
	// 対照
	if _, err := e.app.ExecContext(e.ctx, "UPDATE users SET display_name = display_name WHERE id = ?", e.alice); err != nil {
		t.Errorf("前提が崩れています: アプリ用の利用者で users を UPDATE できない: %v", err)
	}
	if _, err := e.chunk(tid, 2, "s-1.jsonl", 100, 10, 5, sha('b')); err != nil {
		t.Errorf("前提が崩れています: アプリ用の利用者でチャンク（INSERT）を足せない: %v", err)
	}
}

// 受け入れ条件: 同じ (transcript_id, seq) の 2 つ目の挿入は一意性の違反になる（楽観ロックの取り合いはこれで決まる）。
// 対照: 次の seq と、別の記録の同じ seq は通る。登録の (利用者, agent, vendor_session_id) も一意。
func TestTranscriptChunkSeqIsUnique(t *testing.T) {
	e := newTranscriptEnv(t)
	tid, other := e.transcript(e.alice, e.demo, "s-1"), e.transcript(e.bob, e.demo, "s-1")
	e.mustChunk(tid, 1, "s-1.jsonl", 0, 100, 40, sha('a'))
	_, err := e.chunk(tid, 1, "s-1.jsonl", 100, 50, 20, sha('b'))
	if !store.IsDuplicateKey(err) {
		t.Fatalf("同じ seq の 2 つ目: err = %v, want 一意性の違反", err)
	}
	if n := e.count("SELECT COUNT(*) FROM transcript_chunks WHERE transcript_id = ?", tid); n != 1 {
		t.Errorf("違反の後のチャンクの数 = %d, want 1", n)
	}
	// 対照
	e.mustChunk(tid, 2, "s-1.jsonl", 100, 50, 20, sha('b'))
	e.mustChunk(other, 1, "s-1.jsonl", 0, 10, 5, sha('c'))
	if seq, err := store.TranscriptSeq(e.ctx, e.app, tid); err != nil || seq != 2 {
		t.Errorf("今の連番 = %d %v, want 2", seq, err)
	}
	if seq, err := store.TranscriptSeq(e.ctx, e.app, e.transcript(e.alice, e.misc, "s-empty")); err != nil || seq != 0 {
		t.Errorf("チャンクの無い記録の連番 = %d %v, want 0", seq, err)
	}
	// 登録の一意性: 同じ利用者・agent・セッション ID は違反。既存の行は TranscriptByVendor で読める
	if _, err := store.InsertTranscript(e.ctx, e.app, store.Transcript{UserID: e.alice, ProjectID: e.misc, Agent: store.TranscriptAgentClaude,
		VendorSessionID: "s-1", OriginOS: "darwin", Via: "cli"}); !store.IsDuplicateKey(err) {
		t.Errorf("同じ (利用者, agent, セッション ID) の 2 つ目: err = %v, want 一意性の違反", err)
	}
	if got, err := store.TranscriptByVendor(e.ctx, e.app, e.alice, store.TranscriptAgentClaude, "s-1"); err != nil || got.ID != tid || got.ProjectID != e.demo {
		t.Errorf("TranscriptByVendor = %+v %v", got, err)
	}
	// 対照: agent が違えば別の記録
	if _, err := store.InsertTranscript(e.ctx, e.app, store.Transcript{UserID: e.alice, ProjectID: e.demo, Agent: store.TranscriptAgentCodex,
		VendorSessionID: "s-1", OriginOS: "linux", Via: "cli"}); err != nil {
		t.Errorf("agent の違う同じセッション ID: %v", err)
	}
}

// 受け入れ条件: 設定が無い・壊れているときは既定（32MiB・2GiB・90 日）を返す。
// 対照: 正しい値は読み、保持日数の 0（無期限）も読む。書き込みは setting_changes に記録し、同じ値なら記録しない。
func TestTranscriptLimitsDefaults(t *testing.T) {
	e := newTranscriptEnv(t)
	def := store.TranscriptLimits{MaxChunk: 32 << 20, MaxUser: 2 << 30, RetentionDays: 90}
	if l, err := store.ReadTranscriptLimits(e.ctx, e.app); err != nil || l != def {
		t.Fatalf("設定が無いとき = %+v %v, want %+v", l, err, def)
	}
	put := func(name, v string) {
		t.Helper()
		if _, err := e.admin.ExecContext(e.ctx, "INSERT INTO system_settings (name, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)", name, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, broken := range []struct{ chunk, user, days string }{
		{"abc", "", "x"},
		{"0", "0", "-1"},
		{"-5", "-1", "-90"},
		{"1.5", "1e9", "9.5"},
		{" 10", "10 ", "99999999999999999999"},
	} {
		put(store.SettingTranscriptMaxChunk, broken.chunk)
		put(store.SettingTranscriptMaxUser, broken.user)
		put(store.SettingTranscriptRetentionDays, broken.days)
		if l, err := store.ReadTranscriptLimits(e.ctx, e.app); err != nil || l != def {
			t.Errorf("壊れた値 %+v = %+v %v, want 既定 %+v", broken, l, err, def)
		}
	}
	// 対照: 正しい値は読む（保持日数の 0 は無期限として読む）
	put(store.SettingTranscriptMaxChunk, "1024")
	put(store.SettingTranscriptMaxUser, "4096")
	put(store.SettingTranscriptRetentionDays, "0")
	if l, err := store.ReadTranscriptLimits(e.ctx, e.app); err != nil || l != (store.TranscriptLimits{MaxChunk: 1024, MaxUser: 4096, RetentionDays: 0}) {
		t.Errorf("正しい値 = %+v %v", l, err)
	}

	// 書き込み（添付の上限と同じ形）
	set := func(name string, v int64) (string, bool, error) {
		return store.SetTranscriptLimit(e.ctx, e.app, name, v, store.SettingChange{Via: "command", Note: "test"})
	}
	if old, changed, err := set(store.SettingTranscriptRetentionDays, 30); err != nil || !changed || old != "0" {
		t.Errorf("保持日数 0 → 30: old=%q changed=%v %v", old, changed, err)
	}
	if _, changed, err := set(store.SettingTranscriptRetentionDays, 30); err != nil || changed {
		t.Errorf("同じ値で変更扱いになった: %v %v", changed, err)
	}
	if _, changed, err := set(store.SettingTranscriptRetentionDays, 0); err != nil || !changed {
		t.Errorf("保持日数 0（無期限）を受け付けない: %v %v", changed, err)
	}
	for _, bad := range []struct {
		name string
		v    int64
	}{{store.SettingTranscriptMaxChunk, 0}, {store.SettingTranscriptMaxUser, -1}, {store.SettingTranscriptRetentionDays, -1}, {store.SettingAttachMaxFile, 5}, {"two_factor", 5}} {
		if _, _, err := set(bad.name, bad.v); err == nil {
			t.Errorf("%s = %d を受け付けた", bad.name, bad.v)
		}
	}
	cs, err := store.SettingChanges(e.ctx, e.admin, store.SettingTranscriptRetentionDays, 10)
	if err != nil || len(cs) != 2 {
		t.Fatalf("setting_changes の記録 = %+v %v, want 2 件", cs, err)
	}
	// 新しい順: 30 → 0、0 → 30
	if cs[0].OldValue != "30" || cs[0].NewValue != "0" || cs[1].OldValue != "0" || cs[1].NewValue != "30" || cs[1].Via != "command" || cs[1].Note != "test" {
		t.Errorf("setting_changes の中身 = %+v", cs)
	}
	if l, err := store.ReadTranscriptLimits(e.ctx, e.app); err != nil || l.RetentionDays != 0 {
		t.Errorf("書いた後の保持日数 = %+v %v", l, err)
	}
	// 直接書いた値と同じ値なら変更扱いにしない（old はその値）
	if old, changed, err := set(store.SettingTranscriptMaxChunk, 1024); err != nil || changed || old != "1024" {
		t.Errorf("同じ値（1024）: old=%q changed=%v %v", old, changed, err)
	}
}

// ファイルごとの送った長さ・利用者の使用量・一覧は、消去していないチャンクだけを数え、持ち主の外には見せない。
func TestTranscriptLengthsUsageAndList(t *testing.T) {
	e := newTranscriptEnv(t)
	tid := e.transcript(e.alice, e.demo, "s-1")
	e.mustChunk(tid, 1, "s-1.jsonl", 0, 100, 40, sha('a'))
	e.mustChunk(tid, 2, "s-1/subagents/agent-1.jsonl", 0, 30, 12, sha('b'))
	last := e.mustChunk(tid, 3, "s-1.jsonl", 100, 50, 20, sha('c'))
	other := e.transcript(e.alice, e.misc, "s-2")
	e.mustChunk(other, 1, "s-2.jsonl", 0, 10, 7, sha('a')) // 同じ本体を別の記録も指す
	bobs := e.transcript(e.bob, e.demo, "s-3")
	e.mustChunk(bobs, 1, "s-3.jsonl", 0, 5, 3, sha('d'))

	lengths, err := store.TranscriptFileLengths(e.ctx, e.app, tid)
	if err != nil || len(lengths) != 2 || lengths["s-1.jsonl"] != 150 || lengths["s-1/subagents/agent-1.jsonl"] != 30 {
		t.Errorf("ファイルごとの長さ = %v %v", lengths, err)
	}
	if n, err := store.TranscriptUserBytes(e.ctx, e.app, e.alice); err != nil || n != 40+12+20+7 {
		t.Errorf("alice の使用量 = %d %v", n, err)
	}

	// 持ち主の外には同じ ErrNotFound
	if _, err := store.TranscriptByID(e.ctx, e.app, e.bob, tid); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ほかの利用者の記録: %v, want ErrNotFound", err)
	}
	if got, err := store.TranscriptByID(e.ctx, e.app, e.alice, tid); err != nil || got.VendorSessionID != "s-1" || got.MachineLabel != "laptop" || got.OriginOS != "linux" || got.AgentVersion != "2.1.0" || got.CreatedAt.IsZero() {
		t.Errorf("TranscriptByID = %+v %v", got, err)
	}

	// 消去: その時点の最後のチャンクまで。その後に送り直したチャンクだけを数える
	if err := store.InsertTranscriptPurge(e.ctx, e.app, store.TranscriptPurge{TranscriptID: tid, ThroughChunkID: last, Cause: store.TranscriptPurgeOwner,
		ActorUserID: e.alice, Via: "cli", Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if lengths, err := store.TranscriptFileLengths(e.ctx, e.app, tid); err != nil || len(lengths) != 0 {
		t.Errorf("消去の後のファイルごとの長さ = %v %v, want 空（最初から送れる）", lengths, err)
	}
	if n, _ := store.TranscriptUserBytes(e.ctx, e.app, e.alice); n != 7 {
		t.Errorf("消去の後の alice の使用量 = %d, want 7", n)
	}
	if n, _ := store.LiveTranscriptChunkCount(e.ctx, e.app, sha('a')); n != 1 {
		t.Errorf("消去の後に本体 a を指すチャンク = %d, want 1（別の記録が指している）", n)
	}
	if n, _ := store.LiveTranscriptChunkCount(e.ctx, e.app, sha('c')); n != 0 {
		t.Errorf("消去の後に本体 c を指すチャンク = %d, want 0", n)
	}
	e.mustChunk(tid, 4, "s-1.jsonl", 0, 60, 25, sha('e'))
	if lengths, _ := store.TranscriptFileLengths(e.ctx, e.app, tid); len(lengths) != 1 || lengths["s-1.jsonl"] != 60 {
		t.Errorf("送り直した後のファイルごとの長さ = %v", lengths)
	}
	if seq, _ := store.TranscriptSeq(e.ctx, e.app, tid); seq != 4 {
		t.Errorf("消去の後の連番 = %d, want 4（消去で連番は戻らない）", seq)
	}
	chunks, err := store.TranscriptChunks(e.ctx, e.app, tid)
	if err != nil || len(chunks) != 4 || !chunks[0].Purged || !chunks[2].Purged || chunks[3].Purged || chunks[3].Placeholders != `["root"]` || chunks[3].Encoding != "gzip" || chunks[3].LineCount != 3 {
		t.Errorf("チャンクの一覧 = %+v %v", chunks, err)
	}
	if c, err := store.TranscriptChunkBySeq(e.ctx, e.app, tid, 2); err != nil || c.FileKey != "s-1/subagents/agent-1.jsonl" || !c.Purged {
		t.Errorf("TranscriptChunkBySeq(2) = %+v %v", c, err)
	}
	if _, err := store.TranscriptChunkBySeq(e.ctx, e.app, tid, 9); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("無い seq: %v", err)
	}

	usage, err := store.TranscriptUsageByUser(e.ctx, e.app)
	if err != nil || usage[e.alice] != (store.TranscriptUsage{Transcripts: 2, Bytes: 7 + 25}) || usage[e.bob] != (store.TranscriptUsage{Transcripts: 1, Bytes: 3}) || len(usage) != 2 {
		t.Errorf("利用者ごとの使用量 = %+v %v", usage, err)
	}

	list, err := store.ListTranscripts(e.ctx, e.app, e.alice, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("alice の一覧 = %+v %v", list, err)
	}
	s := list[1] // 新しい順なので s-1 は 2 つ目
	if s.ID != tid || s.ProjectSlug != "demo" || s.Seq != 4 || s.LiveChunks != 1 || s.LiveFiles != 1 || s.LiveBytes != 25 || s.Purges != 1 || s.Purged() || s.LastChunkAt.IsZero() {
		t.Errorf("一覧の s-1 = %+v", s)
	}
	if list[0].ID != other || list[0].ProjectSlug != "misc" || list[0].LiveBytes != 7 || list[0].Purged() {
		t.Errorf("一覧の s-2 = %+v", list[0])
	}
	if list, err := store.ListTranscripts(e.ctx, e.app, e.alice, e.misc); err != nil || len(list) != 1 || list[0].ID != other {
		t.Errorf("プロジェクトで絞った一覧 = %+v %v", list, err)
	}
	if list, err := store.ListTranscripts(e.ctx, e.app, e.bob, 0); err != nil || len(list) != 1 || list[0].ID != bobs {
		t.Errorf("bob の一覧 = %+v %v", list, err)
	}
	// 消去の後に何も送っていない記録は「消去済み」
	if err := store.InsertTranscriptPurge(e.ctx, e.app, store.TranscriptPurge{TranscriptID: bobs, ThroughChunkID: 1 << 40, Cause: store.TranscriptPurgeRetention, Via: "system"}); err != nil {
		t.Fatalf("保持期間の切れ（actor なし・via system）: %v", err)
	}
	if list, _ := store.ListTranscripts(e.ctx, e.app, e.bob, 0); len(list) != 1 || !list[0].Purged() || list[0].LiveBytes != 0 {
		t.Errorf("消去済みの bob の一覧 = %+v", list)
	}
}
