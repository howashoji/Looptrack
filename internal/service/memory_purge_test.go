package service

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 記憶の版の本体の消去（memory_purge.go）のテスト。土台は memory_test.go と同じ attachEnv（アプリ用の DB 利用者で動かす）。中身はすべて合成の文面。

const memPurgeWord = "消したい合成の語"

var allMemoryVersions = MemoryPurgeSelect{AllVersions: true}

func (e *attachEnv) purgeRow(t *testing.T, memoryID int64) (cause, via, reason string, from, through int64, actorNull bool) {
	t.Helper()
	var actor *int64
	if err := e.admin.QueryRowContext(e.ctx, "SELECT cause, via, reason, from_version, through_version, actor_user_id FROM memory_purges WHERE memory_id = ? ORDER BY id DESC LIMIT 1", memoryID).
		Scan(&cause, &via, &reason, &from, &through, &actor); err != nil {
		t.Fatal(err)
	}
	return cause, via, reason, from, through, actor == nil
}

// 運用者: 版を指すとその版だけが消え、記録が残る。省くと全部の版が消え、今の版を含むので記憶は消した印になり、作り直せる。
func TestMemoryPurgeOperator(t *testing.T) {
	e := newAttachEnv(t)
	first, err := e.writeMemory(e.editor, "append", "", "caveat", "leak", memPurgeWord+"\n本文 1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	id := first.Memory.ID
	if _, err := e.writeMemory(e.editor, "replace", "", "caveat", "leak", "書き直した本文", "1", ""); err != nil {
		t.Fatal(err)
	}
	key := MemoryKey{Project: e.demo.Slug, Kind: "caveat", Name: "leak"}
	target := OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "leak", MemoryPurgeSelect: MemoryPurgeSelect{Version: 1}}

	// dry run は何も変えない
	prev, err := e.s.OperatorMemoryPreview(e.ctx, target)
	if err != nil || prev.Bodies != 1 || prev.Current {
		t.Fatalf("dry run: %+v %v", prev, err)
	}
	if v, err := e.s.GetMemoryVersion(e.ctx, e.editor, key, 1); err != nil || !strings.Contains(v.Content, memPurgeWord) {
		t.Fatalf("前提が崩れています: dry run の後に版 1 が読めない: %+v %v", v, err)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, target, " "); memErrCode(err) != "invalid_argument" {
		t.Errorf("理由なし: %v", err)
	}

	res, err := e.s.PurgeOperatorMemory(e.ctx, target, "秘密が混ざった")
	if err != nil || res.Bodies != 1 || res.Current {
		t.Fatalf("版 1 の消去: %+v %v", res, err)
	}
	if _, err := e.s.GetMemoryVersion(e.ctx, e.editor, key, 1); memErrCode(err) != "memory_version_purged" {
		t.Errorf("消した版の読み: %v", err)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, target, "もう一度"); memErrCode(err) != "memory_version_purged" {
		t.Errorf("消した版をもう一度: %v", err)
	}
	d, err := e.memory(e.editor, "", "caveat", "leak")
	if err != nil || d.Memory.Content != "書き直した本文\n" && d.Memory.Content != "書き直した本文" {
		t.Fatalf("今の版は残る: %+v %v", d, err)
	}
	if len(d.Versions) != 2 || !d.Versions[1].Purged || d.Versions[1].Summary != "" || d.Versions[1].UserID != e.editor.UserID || d.Versions[0].Purged {
		t.Errorf("履歴: %+v", d.Versions)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies WHERE memory_id = ? AND content LIKE ?", id, "%"+memPurgeWord+"%"); n != 0 {
		t.Errorf("本体に語が残っている: %d", n)
	}
	if cause, via, reason, from, through, actorNull := e.purgeRow(t, id); cause != "operator" || via != "admin" || reason != "秘密が混ざった" || from != 1 || through != 1 || !actorNull {
		t.Errorf("記録: %s %s %s %d %d %v", cause, via, reason, from, through, actorNull)
	}

	// 全部の版: 今の版を含むので本文が外れ、消した印になる
	all := OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "leak", MemoryPurgeSelect: allMemoryVersions}
	res, err = e.s.PurgeOperatorMemory(e.ctx, all, "全部")
	if err != nil || res.Bodies != 1 || !res.Current {
		t.Fatalf("全部の消去: %+v %v", res, err)
	}
	if n := e.count("SELECT COUNT(*) FROM memories WHERE id = ? AND content = '' AND summary = '' AND deleted = TRUE", id); n != 1 {
		t.Errorf("記憶の行に本文が残っている")
	}
	if _, err := e.memory(e.editor, "", "caveat", "leak"); memErrCode(err) != "not_found" {
		t.Errorf("消した記憶の読み: %v", err)
	}
	// 消した分と作り直した分で版が進み、古い節は戻らない
	again, err := e.writeMemory(e.editor, "append", "", "caveat", "leak", "作り直し", "", "")
	if err != nil || again.Memory.Version != 4 || strings.Contains(again.Memory.Content, "書き直した") {
		t.Fatalf("作り直し: %+v %v", again, err)
	}
	if res, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "none", MemoryPurgeSelect: allMemoryVersions}, "x"); memErrCode(err) != "not_found" {
		t.Errorf("無い記憶: %+v %v", res, err)
	}
}

// 運用者は他人の personal にも届く（持ち主の login で指す）。
func TestMemoryPurgeOperatorPersonal(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.writeMemory(e.editor, "append", "personal", "caveat", "mine", memPurgeWord, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Owner: "nobody", Kind: "caveat", Name: "mine", MemoryPurgeSelect: allMemoryVersions}, "x"); memErrCode(err) != "not_found" {
		t.Errorf("居ない利用者: %v", err)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Owner: "vi", Kind: "caveat", Name: "mine", MemoryPurgeSelect: allMemoryVersions}, "x"); memErrCode(err) != "not_found" {
		t.Errorf("別の持ち主: %v", err)
	}
	res, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Owner: "ed", Kind: "caveat", Name: "mine", MemoryPurgeSelect: allMemoryVersions}, "秘密")
	if err != nil || res.Bodies != 1 {
		t.Fatalf("持ち主の login で指す: %+v %v", res, err)
	}
}

// 権限: viewer・editor は 403、参加していない利用者と他人の personal は 404 で本体は残る。対照としてプロジェクトの admin・サーバの管理者・personal の本人は消せる。
func TestMemoryPurgePermissions(t *testing.T) {
	e := newAttachEnv(t)
	key := MemoryKey{Project: e.demo.Slug, Kind: "caveat", Name: "shared"}
	write := func() {
		t.Helper()
		if _, err := e.writeMemory(e.editor, "append", "", "caveat", "shared", memPurgeWord, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := e.writeMemory(e.editor, "append", "personal", "caveat", "mine", memPurgeWord, "", ""); err != nil {
		t.Fatal(err)
	}
	mine := MemoryKey{Project: e.demo.Slug, Scope: "personal", Kind: "caveat", Name: "mine"}
	bodies := func() int { return e.count("SELECT COUNT(*) FROM memory_bodies") }

	for _, c := range []struct {
		name string
		a    Actor
		key  MemoryKey
		code string
	}{
		{"viewer", e.viewer, key, "forbidden"},
		{"editor", e.editor, key, "forbidden"},
		{"参加していない利用者", e.outside, key, "not_found"},
		{"他人の personal（viewer）", e.viewer, mine, "not_found"},
		{"他人の personal（サーバの管理者）", e.root, mine, "not_found"},
	} {
		if _, err := e.s.PurgeMemory(e.ctx, c.a, c.key, allMemoryVersions, "理由"); memErrCode(err) != c.code {
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
	if bodies() != 2 {
		t.Fatalf("拒まれた消去で本体が減った: %d", bodies())
	}

	// 対照: プロジェクトの admin
	admin := Actor{UserID: e.viewer.UserID, Via: "api", Lang: i18n.JA}
	if err := store.SetMember(e.ctx, e.admin, e.demo.ID, e.viewer.UserID, "admin"); err != nil {
		t.Fatal(err)
	}
	res, err := e.s.PurgeMemory(e.ctx, admin, key, allMemoryVersions, "理由")
	if err != nil || res.Bodies != 1 {
		t.Fatalf("プロジェクトの admin: %+v %v", res, err)
	}
	var m store.Memory
	if m, err = store.MemoryByKey(e.ctx, e.admin, e.demo.ID, "project", 0, "caveat", "shared", false); err != nil {
		t.Fatal(err)
	}
	if cause, via, _, _, _, actorNull := e.purgeRow(t, m.ID); cause != "user" || via != "api" || actorNull {
		t.Errorf("記録: %s %s actorNull=%v", cause, via, actorNull)
	}
	// 対照: サーバの管理者（参加していない）
	write()
	if res, err := e.s.PurgeMemory(e.ctx, e.root, key, allMemoryVersions, "理由"); err != nil || res.Bodies != 1 {
		t.Errorf("サーバの管理者: %+v %v", res, err)
	}
	// 対照: personal の本人（viewer の権限でも自分の記憶なら消せる形。ここでは editor の本人）
	if res, err := e.s.PurgeMemory(e.ctx, e.editor, mine, allMemoryVersions, "理由"); err != nil || res.Bodies != 1 {
		t.Errorf("本人: %+v %v", res, err)
	}
	if bodies() != 0 {
		t.Errorf("対照の消去の後の本体: %d", bodies())
	}
}

// 版の数の上限: 上限を超えると古い版から本体が消え、版の行は残り、間引いた記録も残る。上限ちょうどでは消えない（対照）。
func TestMemoryThinning(t *testing.T) {
	const limit = 100 // 定数を読まずに値で書く（上限を変えたら DESIGN.md §9-9 と一緒に直す）
	e := newAttachEnv(t)
	var id int64
	for i := 1; i <= limit; i++ {
		res, err := e.writeMemory(e.editor, "replace", "", "handoff", "", "本文", strconv.Itoa(i-1), "")
		if err != nil {
			t.Fatal(err)
		}
		id = res.Memory.ID
	}
	if n := e.count("SELECT COUNT(*) FROM memory_purges"); n != 0 {
		t.Fatalf("前提が崩れています: 上限ちょうどで間引いた: %d", n)
	}
	for i := limit + 1; i <= limit+3; i++ {
		if _, err := e.writeMemory(e.editor, "replace", "", "handoff", "", "本文", strconv.Itoa(i-1), ""); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies WHERE memory_id = ?", id); n != limit {
		t.Errorf("本体の数 = %d, want %d", n, limit)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_versions WHERE memory_id = ?", id); n != limit+3 {
		t.Errorf("版の行の数 = %d", n)
	}
	key := MemoryKey{Project: e.demo.Slug, Kind: "handoff"}
	if _, err := e.s.GetMemoryVersion(e.ctx, e.viewer, key, 3); memErrCode(err) != "memory_version_purged" {
		t.Errorf("間引いた版: %v", err)
	}
	if _, err := e.s.GetMemoryVersion(e.ctx, e.viewer, key, 4); err != nil {
		t.Errorf("残る最も古い版: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_purges WHERE memory_id = ? AND cause = 'thinning' AND via = 'system'", id); n != 3 {
		t.Errorf("間引きの記録: %d", n)
	}
	// 本体の無い版には必ず記録がある
	if n := e.count(`SELECT COUNT(*) FROM memory_versions v LEFT JOIN memory_bodies b ON b.memory_id = v.memory_id AND b.version = v.version
WHERE b.memory_id IS NULL AND NOT EXISTS (SELECT 1 FROM memory_purges p WHERE p.memory_id = v.memory_id AND v.version BETWEEN p.from_version AND p.through_version)`); n != 0 {
		t.Errorf("記録の無い消去: %d", n)
	}
}

// 保持期間: 既定は無期限で何も消さない。短くすると古い版と消した印の記憶の今の版が消え、生きている記憶の今の版と新しい版は残る。
func TestMemoryRetention(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	live, err := e.writeMemory(e.editor, "append", "", "handoff", "", "古い節", "", "")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := e.writeMemory(e.editor, "append", "", "caveat", "gone", memPurgeWord, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.writeMemory(e.editor, "delete", "", "caveat", "gone", "", "", ""); err != nil {
		t.Fatal(err)
	}
	now = base.AddDate(0, 0, 40)
	if _, err := e.writeMemory(e.editor, "append", "", "handoff", "", "新しい節", "", ""); err != nil {
		t.Fatal(err)
	}
	now = base.AddDate(0, 0, 45)

	res, err := e.s.PurgeExpiredMemories(e.ctx)
	if err != nil || res.RetentionDays != 0 || res.Memories != 0 {
		t.Fatalf("既定（無期限）: %+v %v", res, err)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies"); n != 4 {
		t.Fatalf("前提が崩れています: 既定で本体が消えた: %d", n)
	}
	if _, _, err := store.SetMemoryRetentionDays(e.ctx, e.admin, 30, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	res, err = e.s.PurgeExpiredMemories(e.ctx)
	if err != nil || res.Memories != 2 || res.Bodies != 3 {
		t.Fatalf("保持期間: %+v %v", res, err)
	}
	key := MemoryKey{Project: e.demo.Slug, Kind: "handoff"}
	if _, err := e.s.GetMemoryVersion(e.ctx, e.editor, key, 1); memErrCode(err) != "memory_version_purged" {
		t.Errorf("古い版: %v", err)
	}
	if d, err := e.s.GetMemory(e.ctx, e.editor, key); err != nil || !strings.Contains(d.Memory.Content, "古い節") {
		t.Errorf("生きている記憶の今の版: %+v %v", d, err)
	}
	if cause, via, reason, _, _, _ := e.purgeRow(t, live.Memory.ID); cause != "retention" || via != "system" || reason != "memory_retention_days=30" {
		t.Errorf("記録: %s %s %s", cause, via, reason)
	}
	if n := e.count("SELECT COUNT(*) FROM memories WHERE id = ? AND content = '' AND deleted = TRUE", gone.Memory.ID); n != 1 {
		t.Errorf("消した印の記憶の本文が残っている")
	}
	// もう一度回しても何も増えない
	if res, err := e.s.PurgeExpiredMemories(e.ctx); err != nil || res.Memories != 0 {
		t.Errorf("2 回目: %+v %v", res, err)
	}
}

// 冪等の鍵の版の本体を消した後の再送は、節を足さずにその版を返す（消した節を戻さない）。別の利用者の同じ鍵は拒む。
func TestMemoryIdempotentRetryAfterPurge(t *testing.T) {
	e := newAttachEnv(t)
	in := MemoryWriteInput{MemoryKey: MemoryKey{Project: e.demo.Slug, Kind: "handoff"}, Op: "append", Content: memPurgeWord, IdempotencyKey: "retry-1"}
	if _, err := e.s.WriteMemory(e.ctx, e.editor, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Kind: "handoff", MemoryPurgeSelect: allMemoryVersions}, "秘密"); err != nil {
		t.Fatal(err)
	}
	res, err := e.s.WriteMemory(e.ctx, e.editor, in)
	if err != nil || res.Memory.Version != 1 {
		t.Fatalf("再送: %+v %v", res, err)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies"); n != 0 {
		t.Errorf("再送で本体が戻った: %d", n)
	}
	if err := store.SetMember(e.ctx, e.admin, e.demo.ID, e.viewer.UserID, "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.WriteMemory(e.ctx, e.viewer, in); memErrCode(err) != "memory_idempotency_conflict" {
		t.Errorf("別の利用者の同じ鍵: %v", err)
	}
}

// 権限の分け方: アプリ用の利用者は memory_bodies を消せるが書き換えられず、memory_purges は書き換えも削除もできない。
func TestMemoryPurgeTablesPrivileges(t *testing.T) {
	e := newAttachEnv(t)
	res, err := e.writeMemory(e.editor, "append", "", "handoff", "", "本文", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Kind: "handoff", MemoryPurgeSelect: allMemoryVersions}, "理由"); err != nil {
		t.Fatalf("アプリ用の利用者の消去: %v", err)
	}
	want := "command denied"
	if testutil.SQLite() {
		want = "append-only"
	}
	for _, stmt := range []string{"UPDATE memory_purges SET reason = 'x' WHERE memory_id = ?", "DELETE FROM memory_purges WHERE memory_id = ?"} {
		_, err := e.app.ExecContext(e.ctx, stmt, res.Memory.ID)
		if !store.IsAppendOnlyViolation(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q で拒否", stmt, err, want)
		}
	}
	if !testutil.SQLite() {
		if _, err := e.app.ExecContext(e.ctx, "UPDATE memory_bodies SET content = 'x'"); !store.IsAppendOnlyViolation(err) {
			t.Errorf("memory_bodies の UPDATE: %v", err)
		}
	}
}

// 今の版を消すと版が進み、消す前に読んだ版の置き換えは 409 で本文が戻らない。対照は今の版を渡した作り直し。
func TestMemoryPurgeBumpsVersion(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "x", memPurgeWord, "", ""); err != nil {
		t.Fatal(err)
	}
	res, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "x", MemoryPurgeSelect: allMemoryVersions}, "秘密")
	if err != nil || !res.Current {
		t.Fatalf("消去: %+v %v", res, err)
	}
	if _, err := e.writeMemory(e.editor, "replace", "", "caveat", "x", memPurgeWord, "1", ""); memErrCode(err) != "memory_version" {
		t.Fatalf("消す前の版の置き換え: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies"); n != 0 {
		t.Fatalf("本文が戻った: %d", n)
	}
	got, err := e.writeMemory(e.editor, "replace", "", "caveat", "x", "作り直し", "2", "")
	if err != nil || got.Memory.Version != 3 {
		t.Fatalf("今の版の作り直し: %+v %v", got, err)
	}
	// 本体の無い版の行が履歴に残り、消した記録が今の版の分まで指す
	if cause, _, _, from, through, _ := e.purgeRow(t, got.Memory.ID); cause != "operator" || from != 1 || through != 2 {
		t.Errorf("記録: %s %d %d", cause, from, through)
	}
	// 古い版だけを消しても版は進まない（対照）
	if _, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "x", MemoryPurgeSelect: MemoryPurgeSelect{Version: 3}}, "今の版"); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT version FROM memories WHERE id = ?", got.Memory.ID); n != 4 {
		t.Errorf("今の版を消した後の版 = %d, want 4", n)
	}
}

// 版の指定は 1 つだけ。0 や省略で全部が消えない。
func TestMemoryPurgeSelectIsExplicit(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "x", "本文", "", ""); err != nil {
		t.Fatal(err)
	}
	key := MemoryKey{Project: e.demo.Slug, Kind: "caveat", Name: "x"}
	for name, sel := range map[string]MemoryPurgeSelect{"省略": {}, "負": {Version: -1}, "両方": {Version: 1, AllVersions: true}} {
		if _, err := e.s.PurgeMemory(e.ctx, e.root, key, sel, "理由"); memErrCode(err) != "invalid_argument" {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := e.s.PurgeOperatorMemory(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "x", MemoryPurgeSelect: sel}, "理由"); memErrCode(err) != "invalid_argument" {
			t.Errorf("運用者 %s: %v", name, err)
		}
		if _, err := e.s.OperatorMemoryPreview(e.ctx, OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "x", MemoryPurgeSelect: sel}); memErrCode(err) != "invalid_argument" {
			t.Errorf("dry run %s: %v", name, err)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies"); n != 1 {
		t.Fatalf("拒んだ指定で本体が減った: %d", n)
	}
	if _, err := e.s.PurgeMemory(e.ctx, e.root, key, MemoryPurgeSelect{Version: 1}, "理由"); err != nil {
		t.Errorf("対照: %v", err)
	}
}

// 理由にも資格情報の検査を掛ける（値は返さない）。対照は普通の理由。
func TestMemoryPurgeReasonSecret(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "x", "本文", "", ""); err != nil {
		t.Fatal(err)
	}
	t1 := OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "x", MemoryPurgeSelect: allMemoryVersions}
	const value = "Synthetic0Value9"
	_, err := e.s.PurgeOperatorMemory(e.ctx, t1, "password="+value)
	if memErrCode(err) != "memory_secret" || strings.Contains(err.Error(), value) {
		t.Fatalf("秘密の形の理由: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_purges"); n != 0 {
		t.Fatalf("拒んだのに記録が残った: %d", n)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, t1, "普通の理由"); err != nil {
		t.Errorf("対照: %v", err)
	}
}

// 保持期間の境界: ちょうど日数が経った版は残し、それより古い版を消す。
func TestMemoryRetentionBoundary(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if _, err := e.writeMemory(e.editor, "append", "", "handoff", "", "節", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.SetMemoryRetentionDays(e.ctx, e.admin, 30, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	now = base.AddDate(0, 0, 30)
	if res, err := e.s.PurgeExpiredMemories(e.ctx); err != nil || res.Bodies != 0 {
		t.Fatalf("ちょうど 30 日: %+v %v", res, err)
	}
	now = now.Add(time.Second)
	if res, err := e.s.PurgeExpiredMemories(e.ctx); err != nil || res.Bodies != 1 {
		t.Fatalf("30 日と 1 秒: %+v %v", res, err)
	}
}

// 本体が今の版の 1 つだけ残った消した記憶も、保持期間で消える（古い版が無いので m.deleted の条件だけで当たる）。
func TestMemoryRetentionDeletedSingleBody(t *testing.T) {
	e := newAttachEnv(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	e.s.Now = func() time.Time { return now }
	res, err := e.writeMemory(e.editor, "append", "", "caveat", "x", memPurgeWord, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.writeMemory(e.editor, "delete", "", "caveat", "x", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.ExecContext(e.ctx, "DELETE FROM memory_bodies WHERE memory_id = ? AND version = 1", res.Memory.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetMemoryRetentionDays(e.ctx, e.admin, 30, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	now = base.AddDate(0, 0, 31)
	r, err := e.s.PurgeExpiredMemories(e.ctx)
	if err != nil || r.Bodies != 1 {
		t.Fatalf("保持期間: %+v %v", r, err)
	}
	if n := e.count("SELECT COUNT(*) FROM memories WHERE id = ? AND content = '' AND deleted = TRUE AND version = 3", res.Memory.ID); n != 1 {
		t.Errorf("記憶の行: 本文が残ったか版が進んでいない")
	}
}

// 保持日数の上限: 100 年を超える値は設定できず、DB に直接入った値は既定（無期限）に戻すので新しい版が消えない。
func TestMemoryRetentionOverflow(t *testing.T) {
	e := newAttachEnv(t)
	for _, v := range []int64{106752, 36501, -1} {
		if _, _, err := store.SetMemoryRetentionDays(e.ctx, e.admin, v, store.SettingChange{Via: "command"}); err == nil {
			t.Errorf("%d を受け付けた", v)
		}
	}
	if _, _, err := store.SetMemoryRetentionDays(e.ctx, e.admin, 36500, store.SettingChange{Via: "command"}); err != nil {
		t.Errorf("対照の上限ちょうど: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.writeMemory(e.editor, "append", "", "handoff", "", "節", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.admin.ExecContext(e.ctx, "UPDATE system_settings SET value = '106752' WHERE name = ?", store.SettingMemoryRetentionDays); err != nil {
		t.Fatal(err)
	}
	if res, err := e.s.PurgeExpiredMemories(e.ctx); err != nil || res.Bodies != 0 || res.RetentionDays != 0 {
		t.Fatalf("範囲外の値: %+v %v", res, err)
	}
	// 連絡と会話記録の保持日数も同じ上限で拒む
	if _, _, err := store.SetMessageRetentionDays(e.ctx, e.admin, 36501, store.SettingChange{Via: "command"}); err == nil {
		t.Error("連絡の保持日数の上限を超えた値を受け付けた")
	}
	if _, _, err := store.SetTranscriptLimit(e.ctx, e.admin, store.SettingTranscriptRetentionDays, 36501, store.SettingChange{Via: "cli"}); err == nil {
		t.Error("会話記録の保持日数の上限を超えた値を受け付けた")
	}
}

// 追記の版の本文は後の版に含まれるので、1 つの版だけを指す消去は何も消さずに 409 で拒む。全部の版なら消せる。
// 対照は、置き換えで本文が後の版に残らない版の消去（今までどおり消える）。
func TestMemoryPurgeRefusesTextInLaterVersion(t *testing.T) {
	e := newAttachEnv(t)
	for _, c := range []string{memPurgeWord, "次の節"} {
		if _, err := e.writeMemory(e.editor, "append", "", "caveat", "x", c, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	key := MemoryKey{Project: e.demo.Slug, Kind: "caveat", Name: "x"}
	one := OperatorMemoryTarget{Project: e.demo.Slug, Kind: "caveat", Name: "x", MemoryPurgeSelect: MemoryPurgeSelect{Version: 1}}
	if _, err := e.s.PurgeMemory(e.ctx, e.root, key, MemoryPurgeSelect{Version: 1}, "理由"); memErrCode(err) != "memory_purge_text_remains" {
		t.Errorf("利用者の経路: %v", err)
	}
	if _, err := e.s.PurgeOperatorMemory(e.ctx, one, "理由"); memErrCode(err) != "memory_purge_text_remains" {
		t.Errorf("運用者の経路: %v", err)
	}
	if _, err := e.s.OperatorMemoryPreview(e.ctx, one); memErrCode(err) != "memory_purge_text_remains" {
		t.Errorf("dry run: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies"); n != 2 {
		t.Fatalf("拒んだのに本体が減った: %d", n)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_purges"); n != 0 {
		t.Fatalf("拒んだのに記録が残った: %d", n)
	}
	// 対照 1: 置き換えの後は版 2 の本文が版 3 に無いので消せる
	if _, err := e.writeMemory(e.editor, "replace", "", "caveat", "x", "書き直した", "2", ""); err != nil {
		t.Fatal(err)
	}
	if res, err := e.s.PurgeMemory(e.ctx, e.root, key, MemoryPurgeSelect{Version: 2}, "理由"); err != nil || res.Bodies != 1 {
		t.Fatalf("後の版に無い本文: %+v %v", res, err)
	}
	// 対照 2: 全部の版なら語はどの本体からも消える
	if _, err := e.s.PurgeMemory(e.ctx, e.root, key, allMemoryVersions, "理由"); err != nil {
		t.Fatalf("全部の版: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM memory_bodies WHERE content LIKE ?", "%"+memPurgeWord+"%"); n != 0 {
		t.Errorf("語が残っている: %d", n)
	}
}
