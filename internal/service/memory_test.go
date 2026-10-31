package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// プロジェクト記憶の service（memory.go）のテスト。土台は添付のテストと同じ attachEnv（アプリ用の DB 利用者で動かす）。
// demo の参加者は editor（ed）と viewer（vi）。root は利用者の admin で demo に参加していない（viewer として読める）。
// out は other だけの参加者。中身はすべて合成の文面。

func memErrCode(err error) string {
	var se *Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func (e *attachEnv) writeMemory(a Actor, op, scope, kind, name, content, ifMatch, label string) (*MemoryResult, error) {
	return e.s.WriteMemory(e.ctx, a, MemoryWriteInput{MemoryKey: MemoryKey{Project: e.demo.Slug, Scope: scope, Kind: kind, Name: name},
		Op: op, Content: content, IfMatch: ifMatch, MachineLabel: label})
}

func (e *attachEnv) memory(a Actor, scope, kind, name string) (*MemoryDetail, error) {
	return e.s.GetMemory(e.ctx, a, MemoryKey{Project: e.demo.Slug, Scope: scope, Kind: kind, Name: name})
}

// 受け入れ条件: 2 つのセッション（別の端末のラベルと別のセッション ID）が同時に追記しても、両方の節が残る。
// 行がまだ無い状態から始めるので、同じ行を同時に用意する経路（store.EnsureMemoryRow）も通る。
func TestMemoryConcurrentAppendsKeepEverySection(t *testing.T) {
	e := newAttachEnv(t)
	ed2ID, err := store.CreateUser(e.ctx, e.admin, "ed2", "ed2", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(e.ctx, e.admin, e.demo.ID, ed2ID, "editor"); err != nil {
		t.Fatal(err)
	}
	sessA := e.editor
	sessA.SessionID = "session-a"
	sessB := Actor{UserID: ed2ID, Via: "cli", Lang: i18n.JA, SessionID: "session-b"}
	const rounds = 6
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for _, w := range []struct {
		a     Actor
		label string
	}{{sessA, "box-a"}, {sessB, "box-b"}} {
		wg.Add(1)
		go func(a Actor, label string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if _, err := e.writeMemory(a, "append", "", "handoff", "", fmt.Sprintf("## %s の節 %d\n本文 %s-%d", label, i, label, i), "", label); err != nil {
					errs <- fmt.Errorf("%s %d: %w", label, i, err)
				}
			}
		}(w.a, w.label)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("追記: %v", err)
	}
	d, err := e.memory(e.viewer, "", "handoff", "main")
	if err != nil {
		t.Fatalf("取得: %v", err)
	}
	for _, label := range []string{"box-a", "box-b"} {
		for i := 0; i < rounds; i++ {
			if want := fmt.Sprintf("本文 %s-%d", label, i); !strings.Contains(d.Memory.Content, want) {
				t.Errorf("本文に %q が残っていない", want)
			}
		}
	}
	if d.Memory.Version != 2*rounds || len(d.Versions) != 2*rounds {
		t.Errorf("版 %d・履歴 %d 件（どちらも %d のはず）", d.Memory.Version, len(d.Versions), 2*rounds)
	}
	seen := map[string]int{}
	for _, v := range d.Versions {
		seen[v.SessionID+"/"+v.MachineLabel]++
		if v.Op != store.MemoryOpAppend {
			t.Errorf("版 %d の操作が %q", v.Version, v.Op)
		}
	}
	if seen["session-a/box-a"] != rounds || seen["session-b/box-b"] != rounds {
		t.Errorf("履歴の書き手のセッションと端末: %v", seen)
	}
}

// 受け入れ条件: 古い版を渡した置き換えは何も書かずに 409（memory_version）で拒まれ、新しい版を渡せば通る（対照）。
func TestMemoryReplaceRejectsStaleVersion(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.writeMemory(e.editor, "create", "", "decision", "db-choice", "MySQL を使う\n", "", "box-a"); err != nil {
		t.Fatalf("作成: %v", err)
	}
	if _, err := e.writeMemory(e.editor, "append", "", "decision", "db-choice", "SQLite はデスクトップ版だけ", "", "box-b"); err != nil {
		t.Fatalf("追記: %v", err)
	}
	before, err := e.memory(e.editor, "", "decision", "db-choice")
	if err != nil || before.Memory.Version != 2 {
		t.Fatalf("前提が崩れています: 版 2 になっていない（%v, %+v）", err, before)
	}
	_, err = e.writeMemory(e.editor, "replace", "", "decision", "db-choice", "古い版から書いた本文\n", "1", "box-a")
	var se *Error
	if !errors.As(err, &se) || se.Code != "memory_version" || se.Kind != Conflict || se.CurrentSeq == nil || *se.CurrentSeq != 2 {
		t.Fatalf("古い版の置き換え: err = %v（409 memory_version・今の版 2 のはず）", err)
	}
	after, err := e.memory(e.editor, "", "decision", "db-choice")
	if err != nil || after.Memory.Version != 2 || after.Memory.Content != before.Memory.Content || len(after.Versions) != 2 {
		t.Fatalf("拒まれたのに書き換わった: %+v %v", after, err)
	}
	// If-Match の無い置き換えは 428
	if _, err := e.writeMemory(e.editor, "replace", "", "decision", "db-choice", "x\n", "", ""); memErrCode(err) != "memory_version_required" {
		t.Errorf("If-Match なし: err = %v", err)
	}
	// 対照: 今の版を渡せば通る
	res, err := e.writeMemory(e.editor, "replace", "", "decision", "db-choice", "PostgreSQL に移る\n", `"2"`, "box-a")
	if err != nil || res.Memory.Version != 3 {
		t.Fatalf("今の版の置き換え: %v %+v", err, res)
	}
	got, err := e.memory(e.viewer, "", "decision", "db-choice")
	if err != nil || got.Memory.Content != "PostgreSQL に移る\n" || got.Memory.Summary != "PostgreSQL に移る" {
		t.Fatalf("置き換えた本文: %+v %v", got, err)
	}
	if got.Versions[0].Op != store.MemoryOpReplace || got.Versions[0].UserID != e.editor.UserID {
		t.Errorf("最新の版の履歴: %+v", got.Versions[0])
	}
}

// 受け入れ条件: 参加者でない利用者は読めない。personal はほかの利用者（管理者を含む）に見えず、本人には見える（対照）。
func TestMemoryAccessByMembershipAndScope(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "shared", "全員に見える注意", "", ""); err != nil {
		t.Fatalf("project の記憶: %v", err)
	}
	if _, err := e.writeMemory(e.editor, "append", "personal", "caveat", "mine", "本人だけの注意", "", ""); err != nil {
		t.Fatalf("personal の記憶: %v", err)
	}

	// 参加していない利用者: 一覧も取得も無いものと同じ 404
	if _, _, err := e.s.ListMemories(e.ctx, e.outside, e.demo.Slug, false); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない一覧: err = %v", err)
	}
	if _, err := e.memory(e.outside, "", "caveat", "shared"); memErrCode(err) != "not_found" {
		t.Errorf("参加者でない取得: err = %v", err)
	}
	// 対照: 参加者（viewer）は project の記憶を読める
	if d, err := e.memory(e.viewer, "", "caveat", "shared"); err != nil || d.Memory.Content == "" {
		t.Errorf("viewer の取得: %v", err)
	}
	// viewer は project の記憶を書けない（403）。書いていないことも確かめる
	if _, err := e.writeMemory(e.viewer, "append", "", "caveat", "shared", "viewer の追記", "", ""); memErrCode(err) != "forbidden" {
		t.Errorf("viewer の追記: err = %v", err)
	}
	// 参加していない管理者は viewer として project の記憶を読める
	if _, err := e.memory(e.root, "", "caveat", "shared"); err != nil {
		t.Errorf("参加していない管理者の取得: %v", err)
	}

	// personal: 管理者にも viewer にも見えない（一覧にも載らない）
	for name, a := range map[string]Actor{"root": e.root, "viewer": e.viewer} {
		if _, err := e.memory(a, "personal", "caveat", "mine"); memErrCode(err) != "not_found" {
			t.Errorf("%s が他人の personal を取得: err = %v", name, err)
		}
		list, _, err := e.s.ListMemories(e.ctx, a, e.demo.Slug, true)
		if err != nil {
			t.Fatalf("%s の一覧: %v", name, err)
		}
		for _, m := range list {
			if m.Scope == store.MemoryScopePersonal {
				t.Errorf("%s の一覧に他人の personal が載った: %s/%s", name, m.Kind, m.Name)
			}
		}
		if len(list) != 1 {
			t.Errorf("%s の一覧が %d 件（project の 1 件のはず）", name, len(list))
		}
	}
	// 対照: 本人には見える（取得と一覧）
	d, err := e.memory(e.editor, "personal", "caveat", "mine")
	if err != nil || d.Memory.Content != "本人だけの注意\n" || d.Memory.OwnerUserID != e.editor.UserID {
		t.Fatalf("本人の personal: %+v %v", d, err)
	}
	list, _, err := e.s.ListMemories(e.ctx, e.editor, e.demo.Slug, false)
	if err != nil || len(list) != 2 {
		t.Fatalf("本人の一覧: %d 件 %v（2 件のはず）", len(list), err)
	}
	// viewer も自分の personal は書ける（本人の範囲）
	if _, err := e.writeMemory(e.viewer, "append", "personal", "handoff", "", "viewer の申し送り", "", ""); err != nil {
		t.Errorf("viewer の personal: %v", err)
	}
}

// 受け入れ条件: 資格情報の形の本文は拒まれ、理由に値が出ない。普通の本文は通る（対照）。
func TestMemoryRejectsCredentials(t *testing.T) {
	e := newAttachEnv(t)
	for _, op := range []string{"append", "create", "replace"} {
		ifMatch := ""
		if op == "replace" {
			ifMatch = "0"
		}
		_, err := e.writeMemory(e.editor, op, "", "caveat", "secret-"+op, "接続の設定\n"+tSecretValue+"\n", ifMatch, "")
		var se *Error
		if !errors.As(err, &se) || se.Code != "memory_secret" {
			t.Fatalf("%s: err = %v（memory_secret のはず）", op, err)
		}
		for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
			if msg := i18n.Text(lang, err); strings.Contains(msg, "Synthetic0Value9") || !strings.Contains(msg, "2") {
				t.Errorf("%s の理由（%s）に値が出ているか行番号が無い: %s", op, lang, msg)
			}
		}
		if _, err := e.memory(e.editor, "", "caveat", "secret-"+op); memErrCode(err) != "not_found" {
			t.Errorf("%s: 拒んだのに記憶が残った（err = %v）", op, err)
		}
	}
	// 対照: 秘密の形を含まない本文は通る
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "plain", "接続の設定は環境変数から読む\n", "", ""); err != nil {
		t.Fatalf("普通の本文: %v", err)
	}
	if e.count("SELECT COUNT(*) FROM memory_versions") != 1 {
		t.Errorf("履歴の行の数が 1 でない（拒んだ書き込みが残った）")
	}
}

// 形の検査・上限・消す・作り直す。
func TestMemoryRulesAndDelete(t *testing.T) {
	e := newAttachEnv(t)
	for _, c := range []struct {
		kind, name, scope, code string
	}{
		{"note", "x", "", "invalid_argument"},
		{"caveat", "", "", "invalid_argument"},
		{"caveat", "Upper", "", "invalid_argument"},
		{"caveat", "-lead", "", "invalid_argument"},
		{"caveat", strings.Repeat("a", 65), "", "invalid_argument"},
		{"caveat", "ok", "team", "invalid_argument"},
	} {
		if _, err := e.writeMemory(e.editor, "append", c.scope, c.kind, c.name, "x", "", ""); memErrCode(err) != c.code {
			t.Errorf("%s/%s/%s: err = %v（%s のはず）", c.scope, c.kind, c.name, err, c.code)
		}
	}
	// 対照: 64 文字の名前は通る
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", strings.Repeat("a", 64), "x", "", ""); err != nil {
		t.Errorf("64 文字の名前: %v", err)
	}
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "big", strings.Repeat("a", MaxMemoryAppendBytes+1), "", ""); memErrCode(err) != "memory_too_large" {
		t.Errorf("追記の上限: err = %v", err)
	}
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "long-summary", strings.Repeat("あ", maxMemorySummary+1), "", ""); memErrCode(err) != "memory_summary_too_long" {
		t.Errorf("要約の上限: err = %v", err)
	}
	// 追記を重ねて本文の上限を越えると拒む（越える前の本文は残る）
	chunk := strings.Repeat("b\n", (MaxMemoryAppendBytes-10)/2)
	var last error
	n := 0
	for i := 0; i < 6 && last == nil; i++ {
		if _, last = e.writeMemory(e.editor, "append", "", "caveat", "grow", chunk, "", ""); last == nil {
			n++
		}
	}
	if memErrCode(last) != "memory_too_large" || n != 4 {
		t.Errorf("本文の上限: %d 回通って err = %v（4 回通って memory_too_large のはず）", n, last)
	}

	// 消す: 消した印の版を足し、取得は 404（今の版つき）、一覧から外れる。履歴は残る
	res, err := e.writeMemory(e.editor, "delete", "", "caveat", "grow", "", "", "")
	if err != nil || !res.Memory.Deleted || res.Memory.Version != 5 {
		t.Fatalf("消す: %+v %v", res, err)
	}
	_, err = e.memory(e.editor, "", "caveat", "grow")
	var se *Error
	if !errors.As(err, &se) || se.Kind != NotFound || se.CurrentSeq == nil || *se.CurrentSeq != 5 {
		t.Fatalf("消したものの取得: %v", err)
	}
	if v, err := e.s.GetMemoryVersion(e.ctx, e.editor, MemoryKey{Project: e.demo.Slug, Kind: "caveat", Name: "grow"}, 5); err != nil || v.Op != store.MemoryOpDelete || v.Content == "" {
		t.Errorf("消した版の履歴: %+v %v", v, err)
	}
	if _, err := e.writeMemory(e.editor, "delete", "", "caveat", "grow", "", "", ""); memErrCode(err) != "not_found" {
		t.Errorf("2 度目の削除: err = %v", err)
	}
	// 作り直す: 消した後の版を渡した置き換え
	if res, err := e.writeMemory(e.editor, "replace", "", "caveat", "grow", "作り直した\n", "5", ""); err != nil || res.Memory.Version != 6 || res.Memory.Deleted {
		t.Errorf("作り直し: %+v %v", res, err)
	}
	// 作る: あれば 409 memory_exists
	if _, err := e.writeMemory(e.editor, "create", "", "caveat", "grow", "x", "", ""); memErrCode(err) != "memory_exists" {
		t.Errorf("既にある記憶の作成: err = %v", err)
	}
}

// memory_versions は追記専用（アプリ用の利用者で UPDATE・DELETE できない）。memories の UPDATE は通る（対照）。
func TestMemoryVersionsAreAppendOnly(t *testing.T) {
	e := newAttachEnv(t)
	res, err := e.writeMemory(e.editor, "append", "", "handoff", "", "最初の節", "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "command denied"
	if testutil.SQLite() {
		want = "append-only"
	}
	for _, stmt := range []string{"UPDATE memory_versions SET via = 'cli' WHERE memory_id = ?", "DELETE FROM memory_versions WHERE memory_id = ?"} {
		_, err := e.app.ExecContext(e.ctx, stmt, res.Memory.ID)
		if !store.IsAppendOnlyViolation(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q で拒否", stmt, err, want)
		}
	}
	if _, err := e.app.ExecContext(e.ctx, "UPDATE memories SET summary = summary WHERE id = ?", res.Memory.ID); err != nil {
		t.Errorf("前提が崩れています: アプリ用の利用者で memories を UPDATE できない: %v", err)
	}
}

// 別々の名前の記憶を同時に作っても、互いを待って失敗しない。行が無いまま FOR UPDATE すると、MySQL では一意性の索引の
// ギャップロックを取り合ってデッドロックになり、inTx のやり直しを使い切って Error 1213 が返っていた（失敗 0 件で固定する）。
func TestMemoryConcurrentCreatesOfDifferentNames(t *testing.T) {
	e := newAttachEnv(t)
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, err := e.writeMemory(e.editor, "append", "", "caveat", fmt.Sprintf("name-%02d", i), "注意の本文", "", ""); err != nil {
				errs <- fmt.Errorf("%d: %w", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	failed := 0
	for err := range errs {
		failed++
		t.Error(err)
	}
	list, _, err := e.s.ListMemories(e.ctx, e.editor, e.demo.Slug, false)
	if err != nil || len(list) != n || failed != 0 {
		t.Fatalf("失敗 %d 件・一覧 %d 件・err = %v（失敗 0 件・一覧 %d 件のはず）", failed, len(list), err, n)
	}
}

// 資格情報の検査は足す節だけでなく足した後の本文の全体・名前・端末のラベルにも当たり、理由に値を出さない。普通の入力は通る（対照）。
func TestMemorySecretScanCoversJoinedTextNameAndLabel(t *testing.T) {
	e := newAttachEnv(t)
	const fake = "Hunter2value"
	noValue := func(what string, err error, value string) {
		t.Helper()
		if memErrCode(err) != "memory_secret" {
			t.Fatalf("%s: err = %v（memory_secret のはず）", what, err)
		}
		for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
			if msg := i18n.Text(lang, err); strings.Contains(msg, value) {
				t.Errorf("%s の理由に値が出た: %s", what, msg)
			}
		}
	}
	// 節に分けると、それぞれは当たらない
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "split", "run mysql", "", ""); err != nil {
		t.Fatalf("前半の節: %v", err)
	}
	_, err := e.writeMemory(e.editor, "append", "", "caveat", "split", "-p"+fake, "", "")
	noValue("つなぐと当たる節", err, fake)
	if d, err := e.memory(e.editor, "", "caveat", "split"); err != nil || d.Memory.Version != 1 || strings.Contains(d.Memory.Content, fake) {
		t.Fatalf("拒んだ節が残った: %+v %v", d, err)
	}
	// 対照: 続きが秘密の形でなければ通る
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "split", "-P 13306 はポートの指定", "", ""); err != nil {
		t.Errorf("普通の節: %v", err)
	}

	name := "sk-" + strings.Repeat("a", 24)
	_, err = e.writeMemory(e.editor, "append", "", "caveat", name, "本文", "", "")
	noValue("名前", err, strings.Repeat("a", 24))
	_, err = e.memory(e.editor, "", "caveat", name)
	noValue("読むときの名前", err, strings.Repeat("a", 24))
	_, err = e.writeMemory(e.editor, "append", "", "caveat", "ok-name", "本文", "", "password="+fake)
	noValue("端末のラベル", err, fake)
	// 対照: 普通の名前とラベルは通る
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "ok-name", "本文", "", "build-box-1"); err != nil {
		t.Errorf("普通の名前とラベル: %v", err)
	}

	// 形の違う名前の 400 にも入力の値を返さない
	_, err = e.writeMemory(e.editor, "append", "", "caveat", "password="+fake, "本文", "", "")
	if memErrCode(err) != "invalid_argument" {
		t.Fatalf("形の違う名前: err = %v", err)
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		if msg := i18n.Text(lang, err); strings.Contains(msg, fake) {
			t.Errorf("名前の誤りの理由に値が出た: %s", msg)
		}
	}
}

// 参加していない管理者は project の記憶を読むだけだが、自分の personal は読み書きできる。ほかの利用者の personal は読めない（対照）。
func TestMemoryNonMemberAdminKeepsOwnPersonal(t *testing.T) {
	e := newAttachEnv(t)
	if _, err := e.writeMemory(e.root, "append", "", "caveat", "shared", "管理者の追記", "", ""); memErrCode(err) != "forbidden" {
		t.Errorf("参加していない管理者の project への追記: err = %v", err)
	}
	if _, err := e.writeMemory(e.root, "append", "personal", "caveat", "own", "管理者のメモ", "", ""); err != nil {
		t.Fatalf("自分の personal: %v", err)
	}
	if d, err := e.memory(e.root, "personal", "caveat", "own"); err != nil || d.Memory.OwnerUserID != e.root.UserID {
		t.Errorf("自分の personal の取得: %+v %v", d, err)
	}
	if _, err := e.memory(e.editor, "personal", "caveat", "own"); memErrCode(err) != "not_found" {
		t.Errorf("他人の personal: err = %v", err)
	}
	if _, err := e.writeMemory(e.editor, "append", "personal", "caveat", "theirs", "本人のメモ", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.memory(e.root, "personal", "caveat", "theirs"); memErrCode(err) != "not_found" {
		t.Errorf("管理者が他人の personal を取得: err = %v", err)
	}
}

// 下書きの行（拒まれた最初の書き込みが残す）は、取得・一覧（消したものを含む）・作成のどれからも無いものと同じに見える。
func TestMemoryDraftRowIsInvisible(t *testing.T) {
	e := newAttachEnv(t)
	long := strings.Repeat("あ", maxMemorySummary+1)
	if _, err := e.writeMemory(e.editor, "append", "", "caveat", "draft", long, "", ""); memErrCode(err) != "memory_summary_too_long" {
		t.Fatalf("前提が崩れています: err = %v", err)
	}
	if e.count("SELECT COUNT(*) FROM memories WHERE version = 0") != 1 {
		t.Fatal("前提が崩れています: 下書きの行が無い")
	}
	if _, err := e.memory(e.editor, "", "caveat", "draft"); memErrCode(err) != "not_found" {
		t.Errorf("取得: err = %v", err)
	}
	if list, _, err := e.s.ListMemories(e.ctx, e.editor, e.demo.Slug, true); err != nil || len(list) != 0 {
		t.Errorf("一覧: %d 件 %v", len(list), err)
	}
	if _, err := e.writeMemory(e.editor, "delete", "", "caveat", "draft", "", "", ""); memErrCode(err) != "not_found" {
		t.Errorf("消す: err = %v", err)
	}
	// 対照: 同じ名前で作れて、版は 1 から始まり Created が立つ
	res, err := e.writeMemory(e.editor, "create", "", "caveat", "draft", "本文\n", "", "")
	if err != nil || res.Memory.Version != 1 || !res.Created {
		t.Fatalf("作成: %+v %v", res, err)
	}
}

// MCP が渡す操作も含め、不正な種類・範囲・操作・版の理由に入力の値を出さない。対照の正しい値は通す。
func TestMemoryInvalidInputErrorsDoNotEchoValue(t *testing.T) {
	e := newAttachEnv(t)
	secret := "password=" + "Synthetic0Value9"
	for name, in := range map[string]MemoryWriteInput{
		"種類": {MemoryKey: MemoryKey{Project: e.demo.Slug, Kind: secret, Name: "a"}, Op: "append", Content: "x"},
		"範囲": {MemoryKey: MemoryKey{Project: e.demo.Slug, Scope: secret, Kind: "caveat", Name: "a"}, Op: "append", Content: "x"},
		"操作": {MemoryKey: MemoryKey{Project: e.demo.Slug, Kind: "caveat", Name: "a"}, Op: secret, Content: "x"},
		"版":  {MemoryKey: MemoryKey{Project: e.demo.Slug, Kind: "caveat", Name: "a"}, Op: "replace", Content: "x", IfMatch: secret},
	} {
		_, err := e.s.WriteMemory(e.ctx, e.editor, in)
		if memErrCode(err) != "invalid_argument" {
			t.Fatalf("%s: err = %v", name, err)
		}
		for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
			if msg := i18n.Text(lang, err); strings.Contains(msg, "Synthetic0Value9") {
				t.Errorf("%s の理由に値が出た: %s", name, msg)
			}
		}
	}
	if _, err := e.writeMemory(e.editor, "replace", "project", "caveat", "a", "x", "0", ""); err != nil {
		t.Errorf("対照の正しい値: %v", err)
	}
}
