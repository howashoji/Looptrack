package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 案 B: 結果キャッシュの印が付いた記録は、コメントの見出しと該当の行に注記を残す（失敗にはしない）。
func TestVerifyCommentCachedNote(t *testing.T) {
	// 記録の文面は記録した利用者の言語で決まるので、日本語に固定して確かめる
	cachedLabel, selfReportedLabel := i18n.T(i18n.JA, "service.verify.last.cached"), i18n.T(i18n.JA, "service.verify.last.self_reported")
	zero := 0
	d := store.VerifyDetail{BodySHA256: "abcdef0123456789", OK: true, Passed: 2, Cached: true, Results: []store.VerifyResult{
		{Command: "go test ./internal/docscheck/", Status: "ok", ExitCode: &zero, DurationMS: 30, Cached: true},
		{Command: "shellcheck -S error deploy/dev/up.sh", Status: "ok", ExitCode: &zero, DurationMS: 120},
	}}
	got := verifyComment(i18n.JA, d)
	for _, want := range []string{
		"検証コマンド（" + cachedLabel + "）: 2/2 成功",
		"- ok `go test ./internal/docscheck/`（0.0 秒・" + cachedLabel + "）",
		"- ok `shellcheck -S error deploy/dev/up.sh`（0.1 秒）",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("コメントに %q がありません:\n%s", want, got)
		}
	}
	if strings.Contains(got, "失敗") {
		t.Errorf("注記が失敗として書かれています:\n%s", got)
	}

	// 注記が無い記録の文面は変わらない（既存の記録の読み方を壊さない）
	d.Cached, d.Results[0].Cached = false, false
	if got := verifyComment(i18n.JA, d); strings.Contains(got, cachedLabel) {
		t.Errorf("印が無いのに注記が付いています:\n%s", got)
	}

	// MCP の自己申告と両方付いたら「・」で並べる
	d.Cached, d.SelfReported = true, true
	if want := "検証コマンド（" + selfReportedLabel + "・" + cachedLabel + "）"; !strings.Contains(verifyComment(i18n.JA, d), want) {
		t.Errorf("コメントに %q がありません:\n%s", want, verifyComment(i18n.JA, d))
	}
}

// 直近の記録の 1 行にも注記を出す（next・verify --last・MCP で共通の文面）。
func TestVerifyLastLineCachedNote(t *testing.T) {
	cachedLabel := i18n.T(i18n.JA, "service.verify.last.cached")
	s := &Service{Loc: time.UTC}
	ev := &store.VerifyEvent{At: time.Date(2026, 9, 21, 1, 2, 0, 0, time.UTC),
		VerifyDetail: store.VerifyDetail{OK: true, Passed: 1, Cached: true}}
	got := s.lastLine(i18n.JA, &VerifyPlan{Last: ev, Current: true})
	if !strings.Contains(got, cachedLabel) {
		t.Errorf("1 行に注記がありません: %s", got)
	}
	ev.Cached = false
	if got := s.lastLine(i18n.JA, &VerifyPlan{Last: ev, Current: true}); strings.Contains(got, cachedLabel) {
		t.Errorf("印が無いのに注記が付いています: %s", got)
	}
}

// 記録の detail は、results のどれかに結果キャッシュの注記があれば注記を持つ（成否・件数には数えない）。
func TestNewVerifyDetailCached(t *testing.T) {
	ok := store.VerifyResult{Command: "a", Status: "ok"}
	cached := store.VerifyResult{Command: "b", Status: "ok", Cached: true}
	fail := store.VerifyResult{Command: "c", Status: "fail"}

	d := newVerifyDetail("sha", "h", "w", false, []store.VerifyResult{ok, cached})
	if !d.Cached || d.Passed != 2 || d.Failed != 0 || !d.OK {
		t.Errorf("注記が付いた記録がおかしい: %+v", d)
	}
	if d := newVerifyDetail("sha", "h", "w", false, []store.VerifyResult{ok, fail}); d.Cached || d.Passed != 1 || d.Failed != 1 || d.OK {
		t.Errorf("注記の無い記録がおかしい: %+v", d)
	}
	// 出力はマスクして切る（従来どおり）
	if d := newVerifyDetail("sha", "", "", true, []store.VerifyResult{{Command: "a", Status: "ok", OutputTail: "token=abcdef"}}); d.Results[0].OutputTail != "token=***" || !d.SelfReported {
		t.Errorf("出力のマスク・自己申告の印がおかしい: %+v", d.Results[0])
	}
}

// 起票時の節のハッシュ（issue_events kind create / update の detail.sections）と
// 現在の本文を突き合わせ、「受け入れ条件は更新されたのに検証コマンドは起票時のまま」を警告する。
//
// 古い記録（sections を持たないイシュー）を読んでも落ちず、警告も出さないことを併せて確かめる。
func TestDriftFromOldRecords(t *testing.T) {
	filed := "# EX-0101 題\n\n## 受け入れ条件\n\n- [ ] 追跡されていないことを示す\n\n## 検証コマンド\n\n```\ngit ls-files .claude/memories/\n```\n"
	updated := strings.Replace(filed, "- [ ] 追跡されていないことを示す", "- [ ] private/memories/ へ移す", 1)
	base, err := json.Marshal(domain.NewSectionHashes(filed))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  []byte
		body string
		want bool
	}{
		{"起票時の記録があり、受け入れ条件だけ更新された", base, updated, true},
		{"起票時の記録があり、本文は起票時のまま", base, filed, false},
		// 以下はすべて「判定しない」。落ちてはいけない（この仕組みより前に起票されたイシューがここに来る）
		{"記録が無い（古いイシュー。detail に sections が無い）", nil, updated, false},
		{"記録が JSON の null（JSON_EXTRACT が null を返した）", []byte("null"), updated, false},
		{"記録が空の JSON オブジェクト", []byte("{}"), updated, false},
		{"記録が壊れている", []byte("{壊れ"), updated, false},
		{"記録が別の形（配列）", []byte(`["a"]`), updated, false},
		{"記録が別の形（数値）", []byte("123"), updated, false},
		{"記録の節の名前が違う（知らない鍵）", []byte(`{"other":"x"}`), updated, false},
	}
	for _, c := range cases {
		got := driftFrom(c.raw, c.body)
		if got != c.want {
			t.Errorf("%s: driftFrom(%s) = %v, want %v", c.name, c.raw, got, c.want)
		}
	}
}

// 注記は PlanVerify の Text にも入る（CLI の --list・MCP の verify_issue・GET …/verify が同じ文面を出す）。
func TestVerifyTextSectionDriftNote(t *testing.T) {
	s := &Service{Loc: time.UTC}
	p := &VerifyPlan{ID: "EX-0102", Commands: []string{"go test ./..."}, BodySHA256: "abcdef0123456789",
		Command: domain.VerifyCommand("EX-0102"), LastLine: "直近の verify: 記録なし",
		SectionDrift: true, SectionDriftNote: domain.SectionDriftMsg("EX-0102").In(i18n.JA)}
	_, text := s.verifyText(i18n.JA, p)
	if !strings.Contains(text, p.SectionDriftNote) {
		t.Errorf("Text に注記がありません:\n%s", text)
	}
	// 食い違いが無ければ文面は変わらない（既存の表示を壊さない）
	p.SectionDrift, p.SectionDriftNote = false, ""
	if _, text := s.verifyText(i18n.JA, p); strings.Contains(text, "起票時のまま") {
		t.Errorf("食い違いが無いのに注記が出ています:\n%s", text)
	}
}

// 表示用の 1 行・案内は渡した言語で作る（日本語と英語の両方。英語の側に日本語が残らない）。
func TestVerifyLinesFollowLang(t *testing.T) {
	s := &Service{Loc: time.UTC}
	ev := &store.VerifyEvent{At: time.Date(2026, 9, 21, 1, 2, 0, 0, time.UTC),
		VerifyDetail: store.VerifyDetail{OK: false, Passed: 1, Failed: 1, Cached: true, SelfReported: true}}
	p := &VerifyPlan{ID: "EX-1", Commands: []string{"go test ./...", "make lint"}, BodySHA256: "abcdef0123456789",
		Command: domain.VerifyCommand("EX-1"), Last: ev, Current: false}
	got := map[i18n.Lang]string{}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		p.LastLine = s.lastLine(lang, p)
		_, text := s.verifyText(lang, p)
		got[lang] = text
		for _, id := range []string{"service.verify.last.stale", "service.verify.last.self_reported", "service.verify.last.cached", "service.verify.run"} {
			if w := i18n.T(lang, id, "command", p.Command); !strings.Contains(text, w) {
				t.Errorf("%s: text に %q（%s）が無い:\n%s", lang, w, id, text)
			}
		}
	}
	if got[i18n.JA] == got[i18n.EN] || !strings.Contains(got[i18n.JA], "直近の verify") {
		t.Fatalf("前提が崩れている（言語で文面が分かれていない）:\n%s", got[i18n.JA])
	}
	for _, r := range got[i18n.EN] {
		if r >= 0x3040 && r <= 0x9fff {
			t.Fatalf("英語の text に日本語が残っている:\n%s", got[i18n.EN])
		}
	}
	// 節なしの案内も同じ言語
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		if m, _ := s.verifyText(lang, &VerifyPlan{ID: "EX-2"}); m != domain.NoVerifyCommandsMsg("EX-2").In(lang) {
			t.Errorf("%s: 節なし = %q", lang, m)
		}
	}
}

// 記録のコメントは記録した利用者の言語で書く（DB に残る文面は書いた利用者の言語・DESIGN §9-6）。
// 同じ記録を日本語と英語で組み立て、全文が各言語の形になり、英語の側に日本語が 1 文字も残らないことを見る。
func TestVerifyCommentFollowsWriterLang(t *testing.T) {
	zero, two := 0, 2
	d := store.VerifyDetail{BodySHA256: "abcdef0123456789", Passed: 1, Failed: 2, SelfReported: true, Cached: true, Results: []store.VerifyResult{
		{Command: "go test ./...", Status: "ok", ExitCode: &zero, DurationMS: 1200, Cached: true},
		{Command: "make lint", Status: "fail", ExitCode: &two, DurationMS: 300},
		{Command: "sleep 99", Status: "timeout", DurationMS: 5000},
	}}
	want := map[i18n.Lang]string{
		i18n.JA: "検証コマンド（MCP の自己申告・結果キャッシュあり）: 1/3 成功・2 失敗（6.5 秒・本文 abcdef01）\n" +
			"- ok `go test ./...`（1.2 秒・結果キャッシュあり）\n- fail `make lint`（exit 2・0.3 秒）\n- timeout `sleep 99`（5.0 秒）",
		i18n.EN: "Verification commands (self-reported via MCP, results cached): 1/3 passed, 2 failed (6.5s, body abcdef01)\n" +
			"- ok `go test ./...` (1.2s, results cached)\n- fail `make lint` (exit 2, 0.3s)\n- timeout `sleep 99` (5.0s)",
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		if got := verifyComment(lang, d); got != want[lang] {
			t.Errorf("%s:\n%s\nwant\n%s", lang, got, want[lang])
		}
	}
	for _, r := range verifyComment(i18n.EN, d) {
		if r > 0x2FFF {
			t.Errorf("英語の記録に日本語の文字 %q が残っています:\n%s", r, verifyComment(i18n.EN, d))
			break
		}
	}
	// 終了コードが無い失敗（exit ?）と、印の無い記録も各言語の形
	d.Results[1].ExitCode, d.SelfReported, d.Cached, d.Results[0].Cached = nil, false, false, false
	for lang, w := range map[i18n.Lang]string{i18n.JA: "検証コマンド: 1/3 成功・2 失敗（", i18n.EN: "Verification commands: 1/3 passed, 2 failed ("} {
		if got := verifyComment(lang, d); !strings.HasPrefix(got, w) || !strings.Contains(got, "exit ?") {
			t.Errorf("%s: 印の無い記録・exit ? の形でない:\n%s", lang, got)
		}
	}
}
