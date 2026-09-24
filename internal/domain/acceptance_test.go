package domain

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// section は「## 受け入れ条件」節だけを持つ本文を作る（見出しの綴りは AcceptanceHeading と同じ正本から作らない。
// 実際の本文と同じ書き方で書く）。
func section(items string) string {
	return "# X タイトル\n\n## 内容\n\nなにか\n\n## 受け入れ条件\n\n" + items + "\n\n## コメント\n"
}

// TestAcceptanceFilled は「空の項目だけ」と「中身のある項目」の線引きを表で確かめる。
// 弾く側（want=false）と弾いてはいけない側（want=true）を必ず両方並べる。
func TestAcceptanceFilled(t *testing.T) {
	jaTemplate := strings.TrimSpace(AcceptanceCriteria(i18n.T(i18n.JA, "domain.template.acceptance")))
	enTemplate := strings.TrimSpace(AcceptanceCriteria(i18n.T(i18n.EN, "domain.template.acceptance")))
	if jaTemplate == "" || enTemplate == "" {
		t.Fatalf("雛形を対訳表から取れない: ja=%q en=%q", jaTemplate, enTemplate)
	}
	for _, tc := range []struct {
		name  string
		items string
		want  bool
	}{
		// 弾く（雛形・空の項目だけ）
		{"日本語の雛形そのもの", jaTemplate, false},
		{"英語の雛形そのもの", enTemplate, false},
		{"日英の雛形が両方ある", jaTemplate + "\n" + enTemplate, false},
		{"中身の無いチェックボックス", "- [ ]", false},
		{"中身の無いチェックボックス（全角の空白）", "- [ ] 　", false},
		{"チェック済みだが中身が無い", "- [x]", false},
		{"箇条書きの印だけ", "-", false},
		{"未記入の印（日本語）", i18n.T(i18n.JA, "domain.template.empty"), false},
		{"未記入の印（英語）", i18n.T(i18n.EN, "domain.template.empty"), false},
		{"チェックボックスの中が未記入の印", "- [ ] " + i18n.T(i18n.JA, "domain.template.empty"), false},
		{"強調で飾った未記入の印", "**" + i18n.T(i18n.JA, "domain.template.empty") + "**", false},
		{"TODO だけ", "- [ ] TODO", false},
		{"TBD だけ", "- [ ] tbd", false},
		{"N/A だけ", "- [ ] N/A", false},
		{"空行だけ", "\n   \n", false},
		{"雛形が複数行と空行", jaTemplate + "\n\n" + jaTemplate, false},
		{"雛形の括弧を外した形", "- [ ] テスト可能な形で書く。曖昧語を使わない", false},

		// 弾いてはいけない（短くても有効な条件・中身のあるチェックボックス）
		{"短いが有効な条件", "- [ ] CI が緑", true},
		{"極端に短い条件", "- [ ] 緑", true},
		{"英語の短い条件", "- [ ] tests pass", true},
		{"チェック済みで中身がある", "- [x] go test ./... が通る", true},
		{"雛形の文面を含むが同じではない", "- [ ] （テスト可能な形で書く）を満たす説明が本文にある", true},
		{"英語の雛形の一部で始まる条件", "- [ ] Write it so that it can be tested by go test.", true},
		{"チェックボックスの無い散文", "本文の「期待」に書いた挙動になること", true},
		{"雛形と中身のある条件が混ざる", jaTemplate + "\n- [ ] CI が緑", true},
		{"中身のある条件と未記入の印が混ざる", i18n.T(i18n.JA, "domain.template.empty") + "\n- [ ] CI が緑", true},
		{"TODO を含むが同じではない", "- [ ] TODO コメントが残っていない", true},
		{"N/A を含むが同じではない", "- [ ] N/A のときの表示が空欄になる", true},
		{"番号付きの条件", "1. サーバが 422 を返す", true},
	} {
		if got := AcceptanceFilled(section(tc.items)); got != tc.want {
			t.Errorf("%s: AcceptanceFilled(%q) = %v, want %v", tc.name, tc.items, got, tc.want)
		}
	}
}

// 節そのものが無い本文は Filled が false（関門は HasAcceptanceSection で別に見る）。
func TestAcceptanceFilledNoSection(t *testing.T) {
	body := "# X タイトル\n\n## 内容\n\nなにか\n"
	if HasAcceptanceSection(body) {
		t.Error("節が無い本文で HasAcceptanceSection が true")
	}
	if AcceptanceFilled(body) {
		t.Error("節が無い本文で AcceptanceFilled が true")
	}
	// 英語の見出しでも同じに読む
	en := "# X title\n\n## Acceptance criteria\n\n- [ ] CI is green\n"
	if !HasAcceptanceSection(en) || !AcceptanceFilled(en) {
		t.Error("英語の見出しの節を読めていない")
	}
}

// ParseRules の既定: statuses は Done だけ（Canceled には効かない）。
func TestAcceptanceRuleStatuses(t *testing.T) {
	r, err := ParseRules([]byte(`{"acceptance": {"require_on_close": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !r.RequiresAcceptance("Done") {
		t.Error("既定で Done に効かない")
	}
	for _, s := range []string{"Canceled", "In Progress", "Todo", "In Review"} {
		if r.RequiresAcceptance(s) {
			t.Errorf("既定で %s にも効いている", s)
		}
	}
	off, err := ParseRules([]byte(`{"acceptance": {"require_on_close": false}}`))
	if err != nil || off.RequiresAcceptance("Done") {
		t.Errorf("require_on_close: false で効いている（%v）", err)
	}
	var none *Rules
	if none.RequiresAcceptance("Done") {
		t.Error("ルールなしで効いている")
	}
	if _, err := ParseRules([]byte(`{"acceptance": {"require_on_close": true, "statuses": ["Reviewing"]}}`)); err == nil {
		t.Error("状態でない statuses を通した")
	}
	if _, err := ParseRules([]byte(`{"acceptance": {"require_on_closed": true}}`)); err == nil {
		t.Error("綴り違いを通した")
	}
	// statuses を明示すれば Canceled にも掛けられる（プロジェクトが決める）
	both, err := ParseRules([]byte(`{"acceptance": {"require_on_close": true, "statuses": ["Done", "Canceled"]}}`))
	if err != nil || !both.RequiresAcceptance("Canceled") {
		t.Errorf("statuses の明示が効かない（%v）", err)
	}
}

// CheckAcceptance: 拒否・素通り・上書きの 3 通り。
func TestCheckAcceptance(t *testing.T) {
	r, err := ParseRules([]byte(`{"acceptance": {"require_on_close": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	base := AcceptanceCheck{ID: "X-1", To: "Done", HasSection: true, Filled: false}

	// 雛形のまま → 拒否（上書きできる違反）
	o, v := r.CheckAcceptance(i18n.JA, base)
	if o != nil || v == nil {
		t.Fatalf("雛形のままを拒否しない: o=%v v=%v", o, v)
	}
	if v.Rule != "acceptance_required_on_close" || !v.Overridable {
		t.Errorf("違反の中身: rule=%q overridable=%v", v.Rule, v.Overridable)
	}
	for _, want := range []string{"X-1", "Done", AcceptanceEditCommand("X-1")} {
		if !strings.Contains(v.Msg.In(i18n.JA), want) {
			t.Errorf("日本語の文面に %q が無い: %s", want, v.Msg.In(i18n.JA))
		}
		if !strings.Contains(v.Msg.In(i18n.EN), want) {
			t.Errorf("英語の文面に %q が無い: %s", want, v.Msg.In(i18n.EN))
		}
	}

	// 記入済み・節なし・対象外の状態は素通り
	for _, tc := range []struct {
		name string
		c    AcceptanceCheck
	}{
		{"記入済み", AcceptanceCheck{ID: "X-1", To: "Done", HasSection: true, Filled: true}},
		{"節なし", AcceptanceCheck{ID: "X-1", To: "Done", HasSection: false}},
		{"Canceled", AcceptanceCheck{ID: "X-1", To: "Canceled", HasSection: true}},
		{"In Progress", AcceptanceCheck{ID: "X-1", To: "In Progress", HasSection: true}},
	} {
		if o, v := r.CheckAcceptance(i18n.JA, tc.c); o != nil || v != nil {
			t.Errorf("%s: 素通りしない（o=%v v=%v）", tc.name, o, v)
		}
	}

	// 理由を付ければ通り、記録する上書きが返る（service が rule_override イベントに残す）
	with := base
	with.OverrideReason = "記録のみのイシューのため受け入れ条件なし"
	o, v = r.CheckAcceptance(i18n.JA, with)
	if v != nil {
		t.Fatalf("上書きしたのに拒否した: %v", v)
	}
	if o == nil || o.Rule != "acceptance_required_on_close" || o.Reason != with.OverrideReason {
		t.Errorf("上書きの記録: %+v", o)
	}
	// 空白だけの理由は上書きにならない
	blank := base
	blank.OverrideReason = "   "
	if o, v := r.CheckAcceptance(i18n.JA, blank); o != nil || v == nil {
		t.Errorf("空白だけの理由で通した: o=%v v=%v", o, v)
	}
}

// プロジェクトが message を設定したときは、その文面を使い訳さない（既定の文面と同じ作法）。
func TestCheckAcceptanceProjectMessage(t *testing.T) {
	r := loadRules(t, "example")
	if !r.RequiresAcceptance("Done") || r.RequiresAcceptance("Canceled") {
		t.Fatal("example.json の acceptance が Done だけになっていない")
	}
	_, v := r.CheckAcceptance(i18n.JA, AcceptanceCheck{ID: "EX-9", To: "Done", HasSection: true})
	if v == nil {
		t.Fatal("example の設定で拒否しない")
	}
	if v.Msg.ID != "" {
		t.Errorf("設定した文面なのに既定の ID が付いている: %q", v.Msg.ID)
	}
	for _, want := range []string{"EX-9", "Done", AcceptanceEditCommand("EX-9")} {
		if !strings.Contains(v.Message, want) {
			t.Errorf("設定した文面に %q が無い: %s", want, v.Message)
		}
	}
}
