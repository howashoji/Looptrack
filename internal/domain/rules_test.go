package domain

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"

	"github.com/howashoji/looptrack/deploy"
)

// loadRules は deploy/rules/<slug>.json を読む。keys を渡すと、その最上位のキー（ルール）だけに絞る
// （例 example.json の一部のルールだけを持つプロジェクトとして判定するため）。
func loadRules(t *testing.T, slug string, keys ...string) *Rules {
	t.Helper()
	raw, err := deploy.Rules.ReadFile("rules/" + slug + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) > 0 {
		var all map[string]json.RawMessage
		if err := json.Unmarshal(raw, &all); err != nil {
			t.Fatal(err)
		}
		sub := map[string]json.RawMessage{}
		for _, k := range keys {
			if all[k] == nil {
				t.Fatalf("rules/%s.json に %s が無い", slug, k)
			}
			sub[k] = all[k]
		}
		raw, _ = json.Marshal(sub)
	}
	r, err := ParseRules(raw)
	if err != nil || r == nil {
		t.Fatalf("%s: %v", slug, err)
	}
	return r
}

func TestParseRulesErrors(t *testing.T) {
	for in, want := range map[string]string{
		`{"forbid_statuss": {"statuses": ["In Review"]}}`:                      "unknown field",
		`{"forbid_status": {"statuses": ["Reviewing"]}}`:                       "状態ではありません",
		`{"forbid_status": {"statuses": []}}`:                                  "空です",
		`{"done_requires_keyword": {"keyword": " "}}`:                          "keyword が空",
		`{"forbid_checkbox_pattern": {"env": "(", "action": "a", "ask": "b"}}`: "正規表現が不正",
		`{"forbid_checkbox_pattern": {"env": "a", "action": "a"}}`:             "ask が空",
		`[1]`: "解釈できません",
	} {
		// ParseRules は ID を持つ error を返すので、文面は i18n.Text で作ってから比べる（Error() は ID）
		if _, err := ParseRules([]byte(in)); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), want) {
			t.Errorf("%s: err = %v, want %q", in, i18n.Text(i18n.JA, err), want)
		}
	}
	for _, in := range []string{"", "null", " null\n"} {
		if r, err := ParseRules([]byte(in)); r != nil || err != nil {
			t.Errorf("%q: %v %v", in, r, err)
		}
	}
	var none *Rules
	if v := none.CheckTransition(i18n.JA, Transition{To: "In Review"}); v != nil {
		t.Error("ルールなしで違反になった")
	}
}

// TestParseRulesIgnoresRemovedZeroBugGate は、撤去したゼロバグゲート（2026-09-20）のキーが
// projects.rules に残っているプロジェクトでも、ルールの解釈が落ちないことを確かめる
// （ParseRules は DisallowUnknownFields を使うので、無視用のフィールドが無いと全操作が失敗する）。
func TestParseRulesIgnoresRemovedZeroBugGate(t *testing.T) {
	// 撤去前の設定の形（usage・verify に zero_bug_gate が付いていた）
	const before = `{
  "usage": {"require_on_close": true, "message": "クローズ前にトークン情報を付けてください。"},
  "verify": {"require_on_close": true},
  "zero_bug_gate": {"statuses": ["In Progress"], "allow_types": ["bug", "test"], "message": "未解決の bug が {count} 件あります"}
}`
	r, err := ParseRules([]byte(before))
	if err != nil || r == nil {
		t.Fatalf("撤去前のルールを読めません: %v", err)
	}
	if !r.RequiresUsage("Done") || !r.RequiresVerify("Done") {
		t.Errorf("他のルールが読めていません: %+v", r)
	}
	// 解釈も判定もしない（未クローズの bug があっても遷移を止めない）
	if v := r.CheckTransition(i18n.JA, Transition{ID: "IM-0001", Type: "task", To: "In Progress"}); v != nil {
		t.Errorf("撤去したゼロバグゲートで止まりました: %+v", v)
	}
}

// TestExampleTransitions は、例のルール（使わない状態・キーワード・コメント 0 件）の判定と順序を確かめる。
func TestExampleTransitions(t *testing.T) {
	r := loadRules(t, "example")
	type tc struct {
		name string
		tr   Transition
		rule string // 空なら通る
	}
	id := "EX-0001"
	for _, c := range []tc{
		{"Backlog への遷移", Transition{ID: id, To: "Backlog", Comments: []string{"動作確認: PASS"}}, "forbid_status"},
		{"Backlog で起票", Transition{Creating: true, To: "Backlog"}, "forbid_status"},
		{"コメント 0 件で In Progress", Transition{ID: id, To: "In Progress"}, "require_comment_before"},
		{"同時コメントで In Progress", Transition{ID: id, To: "In Progress", NewComment: "着手"}, ""},
		{"空白だけの同時コメント", Transition{ID: id, To: "In Progress", NewComment: " \n"}, "require_comment_before"},
		{"前置きだけでは件数に数えない", Transition{ID: id, To: "In Progress", Preamble: "メモ"}, "require_comment_before"},
		{"既存コメントがあれば In Progress", Transition{ID: id, To: "In Progress", Comments: []string{"調査"}}, ""},
		{"空のコメントも 1 件", Transition{ID: id, To: "In Progress", Comments: []string{""}}, ""},
		{"In Progress で起票", Transition{Creating: true, To: "In Progress"}, "require_comment_before"},
		{"Todo で起票", Transition{Creating: true, To: "Todo"}, ""},
		{"動作確認なしで Done（キーワードが先）", Transition{ID: id, To: "Done"}, "done_requires_keyword"},
		{"既存コメントの動作確認", Transition{ID: id, To: "Done", Comments: []string{"x", "単体 PASS / 動作確認: PASS"}}, ""},
		{"前置きの動作確認", Transition{ID: id, To: "Done", Preamble: "動作確認: 対象外（設計のみ）", NewComment: "完了"}, ""},
		{"同時コメントの動作確認", Transition{ID: id, To: "Done", NewComment: "動作確認: PASS"}, ""},
		{"キーワードはあるがコメント 0 件（前置きのみ）", Transition{ID: id, To: "Done", Preamble: "動作確認: PASS"}, "require_comment_before"},
		{"Done で起票", Transition{Creating: true, To: "Done"}, "done_requires_keyword"},
		{"Canceled は対象外", Transition{ID: id, To: "Canceled"}, ""},
		{"In Review は対象外", Transition{ID: id, To: "In Review"}, ""},
	} {
		v := r.CheckTransition(i18n.JA, c.tr)
		got := ""
		if v != nil {
			got = v.Rule
			if strings.Contains(v.Message, "{") {
				t.Errorf("%s: 置き換えていないプレースホルダ: %s", c.name, v.Message)
			}
			if !c.tr.Creating && !strings.Contains(v.Message, id) && v.Rule != "forbid_checkbox_pattern" {
				t.Errorf("%s: メッセージに ID が無い", c.name)
			}
			if c.tr.Creating && strings.Contains(v.Message, "新しいイシュー") == false && v.Rule == "forbid_status" {
				t.Errorf("%s: 起票時の ID 表記: %s", c.name, v.Message)
			}
		}
		if got != c.rule {
			t.Errorf("%s: rule = %q, want %q", c.name, got, c.rule)
		}
	}
}

// checkboxCases は、forbid_checkbox_pattern の前身だった hook（受け入れ条件に反映依頼を書かせないもの。2026-09-17 に
// サーバのルールへ一本化して削除）との互換の固定表。hit は削除前の hook が検出した行（空 = 検出なし）をそのまま記録したもの
// （同じ時点の Go 実装とも一致を確認した）。deploy/rules/example.json の env / action / ask / exempt はその hook と同じ式。
// 判定を変えるときは、hook との互換を意図して外すことになるので、この表の hit を理由と一緒に直す。
var checkboxCases = []struct{ text, hit string }{
	{"- [ ] test / staging / 本番への定義反映をユーザーへ依頼した", "- [ ] test / staging / 本番への定義反映をユーザーへ依頼した"},
	{"- [x] staging へのマージをお願いする", "- [x] staging へのマージをお願いする"},
	{"- [ ] 本番へのデプロイをしてください", "- [ ] 本番へのデプロイをしてください"},
	{"* [X] Master にマージしていただく", "* [X] Master にマージしていただく"},
	{"- [ ] test / staging / 本番は未反映（反映はユーザーが実施）", ""},
	{"- [ ] 本番へのリリース依頼は対象外", ""},
	{"- [ ] ~~staging へ反映を依頼~~", ""},
	{"- [ ] 各環境へ反映しない。依頼もしない", ""},
	{"- [ ] 画面の表示を確認する", ""},
	{"本文: staging へ反映を依頼する（チェックボックスではない）", ""},
	{"## 受け入れ条件\n\n- [ ] 単体テスト PASS\n- [ ] 全環境へのデプロイ依頼\n- [ ] E2E PASS", "- [ ] 全環境へのデプロイ依頼"},
	{"--body \"## 受け入れ条件 - [ ] 単体テスト - [ ] テスト環境への反映を依頼 - [ ] 完了\"", "- [ ] テスト環境への反映を依頼"},
	{"- [ ] TEST 環境に反映をお願い", "- [ ] TEST 環境に反映をお願い"},
	{"-[ ]staging反映依頼", "-[ ]staging反映依頼"},
	{"- [ ] 他環境へのマージ下さい\n- [ ] 他環境へのマージは誤り", "- [ ] 他環境へのマージ下さい"},
}

// TestCheckboxCompatWithHook は、例のルール forbid_checkbox_pattern が削除前の hook と同じ行を検出することを固定表で確かめる。
func TestCheckboxCompatWithHook(t *testing.T) {
	r := loadRules(t, "example")
	hitLine := regexp.MustCompile(`検出した行: (.*)`)
	for _, c := range checkboxCases {
		got := ""
		if v := r.CheckText(i18n.JA, "EX-0001", "", c.text); v != nil {
			if v.Rule != "forbid_checkbox_pattern" {
				t.Errorf("%q: 別のルールで止まった: %+v", c.text, v)
				continue
			}
			m := hitLine.FindStringSubmatch(v.Message)
			if m == nil {
				t.Errorf("%q: メッセージに検出した行が無い: %q", c.text, v.Message)
				continue
			}
			got = m[1]
		}
		if got != c.hit {
			t.Errorf("%q: 検出=%q 期待（hook）=%q", c.text, got, c.hit)
		}
	}
}

func TestCheckTextOnlyNewLines(t *testing.T) {
	r := loadRules(t, "example")
	old := "## 受け入れ条件\n\n- [ ] staging へのマージを依頼した\n"
	if v := r.CheckText(i18n.JA, "X", old, old+"\n本文を追記\n"); v != nil {
		t.Errorf("既存の記述だけで止まった: %v", v)
	}
	if v := r.CheckText(i18n.JA, "X", old, old+"- [ ] staging へのマージを依頼した\n"); v == nil {
		t.Error("同じ行を 2 つ目として書き足したのに通った")
	}
	if v := r.CheckText(i18n.JA, "X", old, "- [ ] 本番へデプロイしてください"); v == nil || v.Rule != "forbid_checkbox_pattern" {
		t.Errorf("新しい記述: %v", v)
	}
	if v := loadRules(t, "example", "verify").CheckText(i18n.JA, "X", "", "- [ ] 本番へデプロイしてください"); v != nil {
		t.Error("ルールの無いプロジェクトで止まった")
	}
}

// クローズ時のトークン情報の必須化（usage.require_on_close）。
func TestCheckUsage(t *testing.T) {
	if _, err := ParseRules([]byte(`{"usage": {"require_on_close": true, "statuses": ["Closed"]}}`)); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "usage.statuses") {
		t.Errorf("不正な statuses: %v", err)
	}
	if _, err := ParseRules([]byte(`{"usage": {"required": true}}`)); err == nil {
		t.Error("未知のキーを受け付けた")
	}
	r, err := ParseRules([]byte(`{"usage": {"require_on_close": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := UsageCheck{ID: "REQ-0001", To: "Done", Target: true}
	_, v := r.CheckUsage(i18n.JA, c)
	if v == nil || v.Rule != "usage_required_on_close" || !v.Overridable ||
		!strings.Contains(v.Message, "looptrack issue usage attach REQ-0001") || !strings.Contains(v.Message, `--override "理由"`) {
		t.Errorf("拒否: %+v", v)
	}
	for name, cc := range map[string]UsageCheck{
		"付与済み":  {ID: "REQ-0001", To: "Done", Target: true, Attached: true},
		"人の操作":  {ID: "REQ-0001", To: "Done"},
		"途中の状態": {ID: "REQ-0001", To: "In Progress", Target: true},
	} {
		if o, v := r.CheckUsage(i18n.JA, cc); o != nil || v != nil {
			t.Errorf("%s: %+v %+v", name, o, v)
		}
	}
	if _, v := r.CheckUsage(i18n.JA, UsageCheck{ID: "REQ-0001", To: "Canceled", Target: true}); v == nil {
		t.Error("Canceled も既定の対象")
	}
	c.OverrideReason = " 端末で確認 "
	if o, v := r.CheckUsage(i18n.JA, c); v != nil || o == nil || o.Rule != "usage_required_on_close" || o.Reason != "端末で確認" {
		t.Errorf("上書き: %+v %+v", o, v)
	}
	// 既定（設定なし・require_on_close: false）は拒否しない（警告だけ）
	var none *Rules
	off, _ := ParseRules([]byte(`{"usage": {"require_on_close": false}}`))
	for _, rr := range []*Rules{none, off} {
		if _, v := rr.CheckUsage(i18n.JA, UsageCheck{ID: "REQ-0001", To: "Done", Target: true}); v != nil {
			t.Errorf("既定で拒否した: %+v", v)
		}
	}
	custom, _ := ParseRules([]byte(`{"usage": {"require_on_close": true, "statuses": ["Done"], "message": "{id} は {command} の後で"}}`))
	if _, v := custom.CheckUsage(i18n.JA, UsageCheck{ID: "X-1", To: "Done", Target: true}); v == nil || v.Message != "X-1 は looptrack issue usage attach X-1 の後で" {
		t.Errorf("メッセージの上書き: %+v", v)
	}
}

// 案件ラベルの正規表現（usage.case_pattern）。require_on_close を立てずに設定でき、不正・空に一致する式は拒否する。
func TestUsageCasePattern(t *testing.T) {
	r, err := ParseRules([]byte(`{"usage": {"case_pattern": "CASE-\\d+"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if re := r.CasePattern(); re == nil || re.FindString("CASE-101 ログイン画面の改修") != "CASE-101" {
		t.Errorf("case_pattern: %v", re)
	}
	if r.RequiresUsage("Done") {
		t.Error("case_pattern だけでクローズ時の必須化が有効になった")
	}
	for _, bad := range []string{`{"usage": {"case_pattern": "CASE-("}}`, `{"usage": {"case_pattern": "x*"}}`} {
		if _, err := ParseRules([]byte(bad)); err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "usage.case_pattern") {
			t.Errorf("%s: %v", bad, i18n.Text(i18n.JA, err))
		}
	}
	var none *Rules
	if none.CasePattern() != nil {
		t.Error("ルールなしで正規表現がある")
	}
	if off, _ := ParseRules([]byte(`{"usage": {"require_on_close": true}}`)); off.CasePattern() != nil {
		t.Error("未設定で正規表現がある")
	}
}

// 例のルールは案件（ラベル・ブランチ名の CASE-nnn）の正規表現と、クローズ時の必須化（既定の Done / Canceled）を持つ。
func TestExampleUsage(t *testing.T) {
	r := loadRules(t, "example")
	if re := r.CasePattern(); re == nil || re.FindString("feature/CASE-101-login") != "CASE-101" || re.MatchString("main") {
		t.Errorf("example の案件の正規表現: %v", re)
	}
	if !r.RequiresUsage("Done") || !r.RequiresUsage("Canceled") || r.RequiresUsage("In Progress") {
		t.Error("example のクローズ時のトークン情報の必須化が Done / Canceled でない")
	}
	noCase, err := ParseRules([]byte(`{"usage": {"require_on_close": true, "message": "クローズ前にトークン情報を付けてください。"}}`))
	if err != nil || noCase == nil {
		t.Fatalf("case_pattern の無い usage: %v", err)
	}
	if noCase.CasePattern() != nil {
		t.Error("case_pattern の無い usage に案件の正規表現がある")
	}
}

// TestExampleCoversAllRules は、例のルール（deploy/rules/example.json）がすべての種類のルールを使っていることを確かめる。
// ルールの種類を足したら例にも足す（公開物で設定の書き方を示す唯一の実例のため）。
func TestExampleCoversAllRules(t *testing.T) {
	r := reflect.ValueOf(*loadRules(t, "example"))
	for i := 0; i < r.NumField(); i++ {
		f := r.Type().Field(i)
		if f.Name == "ZeroBugGate" { // 撤去したゼロバグゲートの無視用フィールド（例には置かない）
			continue
		}
		if f.IsExported() && r.Field(i).IsNil() {
			t.Errorf("example.json に %s（%s）が無い", f.Name, f.Tag.Get("json"))
		}
	}
	if v := loadRules(t, "example"); !v.Verify.RequireOnClose || !v.Usage.RequireOnClose {
		t.Error("example.json の require_on_close が立っていない")
	}
}

// usage.send_prompts（指示文の作業名を送ってよいか）。既定は送らない。
func TestUsageSendPrompts(t *testing.T) {
	var none *Rules
	if none.SendPrompts() {
		t.Error("ルールなしは送らない")
	}
	for raw, want := range map[string]bool{
		`{"usage": {"send_prompts": true}}`:                              true,
		`{"usage": {"send_prompts": false}}`:                             false,
		`{"usage": {"require_on_close": true}}`:                          false,
		`{"usage": {"case_pattern": "CASE-\\d+", "send_prompts": true}}`: true,
		`{"zero_bug_gate": {}}`:                                          false,
	} {
		r, err := ParseRules([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if r.SendPrompts() != want {
			t.Errorf("%s: SendPrompts=%v", raw, r.SendPrompts())
		}
		if r.RequiresUsage("Done") != strings.Contains(raw, "require_on_close") {
			t.Errorf("%s: send_prompts だけでクローズの必須化が変わった", raw)
		}
	}
	if _, err := ParseRules([]byte(`{"usage": {"send_prompts": "yes"}}`)); err == nil {
		t.Error("真偽値でない send_prompts は拒否する")
	}
	if _, err := ParseRules([]byte(`{"usage": {"send_prompt": true}}`)); err == nil {
		t.Error("綴り違いは拒否する")
	}
}

// 違反の文面（既定の文面と、設定された文面に埋める値）は、呼ぶ側が渡した言語で作る。
// 日本語と英語の両方を見る（片方だけだと「日本語一択」と区別できない）。
func TestViolationFollowsLang(t *testing.T) {
	r, err := ParseRules([]byte(`{"forbid_status": {"statuses": ["In Review"], "message": "[{id}] {status}"},
		"require_comment_before": {"statuses": ["In Progress"]},
		"verify": {"require_on_close": true, "message": "{id}: {state}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ja, en := i18n.T(i18n.JA, "domain.rules.new_issue"), i18n.T(i18n.EN, "domain.rules.new_issue"); ja == en {
		t.Fatalf("前提が崩れている（新しいイシューの語が言語で分かれていない）: %q", ja)
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		// 設定された文面に埋める {id}（起票）
		v := r.CheckTransition(lang, Transition{To: "In Review", Creating: true})
		if want := "[" + i18n.T(lang, "domain.rules.new_issue") + "] In Review"; v == nil || v.Message != want {
			t.Errorf("%s: 設定された文面 = %+v, want %q", lang, v, want)
		}
		// 既定の文面
		v = r.CheckTransition(lang, Transition{ID: "EX-1", To: "In Progress"})
		if want := i18n.T(lang, "domain.rules.require_comment_before", "id", "EX-1", "status", "In Progress"); v == nil || v.Message != want || v.Msg.In(lang) != want {
			t.Errorf("%s: 既定の文面 = %+v, want %q", lang, v, want)
		}
		// 設定された文面に埋める {state}
		_, v = r.CheckVerify(lang, VerifyCheck{ID: "EX-1", To: "Done", HasCommands: true, State: VerifyStale})
		if want := "EX-1: " + i18n.T(lang, "domain.rules.verify.state.stale"); v == nil || v.Message != want {
			t.Errorf("%s: {state} = %+v, want %q", lang, v, want)
		}
		// 検証コマンドの上限の理由
		many := make([]string, MaxVerifyCommands+1)
		for i := range many {
			many[i] = "true"
		}
		e := CheckVerifyCommands(lang, "EX-1", many)
		if e == nil || e.Message != e.Msg.In(lang) {
			t.Errorf("%s: 上限の理由 = %+v", lang, e)
		}
	}
}
