package i18n

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Lang
		ok   bool
	}{
		{"ja", JA, true},
		{"JA", JA, true},
		{"ja-JP", JA, true},
		{"ja_JP.UTF-8", JA, true},
		{"jpn", JA, true},
		{"en", EN, true},
		{"en_US.UTF-8", EN, true},
		{"C", EN, true},
		{"POSIX", EN, true},
		{"", EN, false},
		{"fr", EN, false}, // 知らない言語は英語にするが、次の手がかりを見に行けるよう ok は false
		{"zh-CN", EN, false},
	} {
		got, ok := Parse(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Parse(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	for _, tt := range []struct {
		name string
		m    map[string]string
		want Lang
	}{
		{"何も無ければ英語", map[string]string{}, EN},
		{"LANG だけ", map[string]string{"LANG": "ja_JP.UTF-8"}, JA},
		{"LC_ALL が LANG より強い", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "ja_JP.UTF-8"}, EN},
		{"LC_MESSAGES が LANG より強い", map[string]string{"LC_MESSAGES": "ja_JP.UTF-8", "LANG": "en_US.UTF-8"}, JA},
		{"LOOPTRACK_LANG が最も強い", map[string]string{"LOOPTRACK_LANG": "ja", "LC_ALL": "en_US.UTF-8"}, JA},
		{"読めない値は次の手がかりへ", map[string]string{"LOOPTRACK_LANG": "fr", "LANG": "ja_JP.UTF-8"}, JA},
	} {
		if got := FromEnv(env(tt.m)); got != tt.want {
			t.Errorf("%s: FromEnv = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Lang
	}{
		{"", EN},
		{"ja", JA},
		{"ja-JP,ja;q=0.9,en;q=0.8", JA},
		{"en-US,en;q=0.9,ja;q=0.8", EN},
		{"en;q=0.5,ja;q=0.9", JA},
		{"ja;q=0.4,en;q=0.9", EN},
		{"ja,en", JA},          // 同じ品質なら先に現れたほう
		{"en,ja", EN},          //
		{"fr-FR,de;q=0.9", EN}, // 知らない言語しかなければ英語
		{"*", EN},
	} {
		if got := FromAcceptLanguage(tt.in); got != tt.want {
			t.Errorf("FromAcceptLanguage(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestT(t *testing.T) {
	// 埋め込んだ対訳表ではなく、この検査のための表に差し替える
	orig := catalogs
	t.Cleanup(func() { catalogs = orig })
	catalogs = map[Lang]map[string]string{
		JA: {"greet": "こんにちは、{name}さん", "only_ja": "日本語だけ"},
		EN: {"greet": "Hello, {name}"},
	}

	if got, want := T(JA, "greet", "name", "太郎"), "こんにちは、太郎さん"; got != want {
		t.Errorf("T(JA) = %q, want %q", got, want)
	}
	if got, want := T(EN, "greet", "name", "Taro"), "Hello, Taro"; got != want {
		t.Errorf("T(EN) = %q, want %q", got, want)
	}
	// 英語に無ければ日本語に戻す（取りこぼしても読める形で出す。抜けはテストが見つける）
	if got, want := T(EN, "only_ja"), "日本語だけ"; got != want {
		t.Errorf("T(EN, 取りこぼし) = %q, want %q", got, want)
	}
	// どちらにも無ければ ID をそのまま返す（動いている最中に落とさない）
	if got, want := T(EN, "missing.id"), "missing.id"; got != want {
		t.Errorf("T(EN, 未登録) = %q, want %q", got, want)
	}
	// 置き換えの値は文字列以外も受ける
	catalogs[JA]["count"] = "{n} 件"
	if got, want := T(JA, "count", "n", 3), "3 件"; got != want {
		t.Errorf("T(JA, int) = %q, want %q", got, want)
	}
}

// 受け入れ条件「訳の抜けを検出するテストがある」の 1 つ目。
// ja.json と en.json の ID の集合は完全に一致していなければならない。
// 片方にだけ足した ID があると、その文面は片方の言語で出なくなる。
func TestCatalogsHaveSameIDs(t *testing.T) {
	ja, en := IDs(JA), IDs(EN)
	slices.Sort(ja)
	slices.Sort(en)
	for _, id := range ja {
		if !Has(EN, id) {
			t.Errorf("en.json に %q がありません（ja.json にはあります）", id)
		}
	}
	for _, id := range en {
		if !Has(JA, id) {
			t.Errorf("ja.json に %q がありません（en.json にはあります）", id)
		}
	}
}

// Msg と Error は、文面を作る場所が相手の言語を知らなくて済むようにする。
func TestMsgAndError(t *testing.T) {
	orig := catalogs
	t.Cleanup(func() { catalogs = orig })
	catalogs = map[Lang]map[string]string{
		JA: {"bad.port": "ポート番号は 1〜65535 の数字で指定してください: {value}"},
		EN: {"bad.port": "The port must be a number from 1 to 65535: {value}"},
	}

	// 検査する側は言語を知らずに理由を返す
	err := Errorf("bad.port", "value", "abc")
	if got, want := Text(JA, err), "ポート番号は 1〜65535 の数字で指定してください: abc"; got != want {
		t.Errorf("Text(JA) = %q, want %q", got, want)
	}
	if got, want := Text(EN, err), "The port must be a number from 1 to 65535: abc"; got != want {
		t.Errorf("Text(EN) = %q, want %q", got, want)
	}
	// %v・ログには ID が出る（訳を持たない場所で困らないように）
	if got, want := err.Error(), "bad.port"; got != want {
		t.Errorf("err.Error() = %q, want %q", got, want)
	}
	// 包まれていても取り出せる
	wrapped := fmt.Errorf("設定を読めません: %w", err)
	if got, want := Text(EN, wrapped), "The port must be a number from 1 to 65535: abc"; got != want {
		t.Errorf("Text(EN, wrapped) = %q, want %q", got, want)
	}
	// i18n の error でなければ、そのままの文言を返す（OS や内部の失敗は訳さない）
	if got, want := Text(EN, errors.New("permission denied")), "permission denied"; got != want {
		t.Errorf("Text(EN, 素の error) = %q, want %q", got, want)
	}
	if got := Text(EN, nil); got != "" {
		t.Errorf("Text(EN, nil) = %q, want 空", got)
	}
	// Msg は持ち回ってから言語を決める
	if got, want := M("bad.port", "value", "x").In(JA), "ポート番号は 1〜65535 の数字で指定してください: x"; got != want {
		t.Errorf("Msg.In(JA) = %q, want %q", got, want)
	}
}

// Wrapf は、もとの error を包んだまま理由を ID で足す。
func TestWrapf(t *testing.T) {
	orig := catalogs
	t.Cleanup(func() { catalogs = orig })
	catalogs = map[Lang]map[string]string{
		JA: {"read.failed": "{path} を読めません: {reason}"},
		EN: {"read.failed": "Cannot read {path}: {reason}"},
	}
	base := os.ErrNotExist
	err := Wrapf(base, "read.failed", "path", "/tmp/x")

	if got, want := Text(JA, err), "/tmp/x を読めません: file does not exist"; got != want {
		t.Errorf("Text(JA) = %q, want %q", got, want)
	}
	if got, want := Text(EN, err), "Cannot read /tmp/x: file does not exist"; got != want {
		t.Errorf("Text(EN) = %q, want %q", got, want)
	}
	// 包んだ先の判定が壊れないこと（これが Errorf ではなく Wrapf を使う理由）
	if !errors.Is(err, os.ErrNotExist) {
		t.Error("errors.Is が包んだ先まで届いていない")
	}
	// さらに外から包まれても取り出せる
	if got, want := Text(EN, fmt.Errorf("setup: %w", err)), "Cannot read /tmp/x: file does not exist"; got != want {
		t.Errorf("Text(EN, 二重に包む) = %q, want %q", got, want)
	}
}

// TestWrapfNestedI18nError は、Wrapf が i18n の error を包んだときに {reason} へ
// 内側の**文面**が入ることを確かめる。
//
// Error() は設計どおり ID を返す（ログ用）ので、文面を作るところで Error() に落とすと
// {reason} に ID がそのまま出る。落ちるテストが無いと黙って ID が出続けるので、ここで固定する。
func TestWrapfNestedI18nError(t *testing.T) {
	orig := catalogs
	t.Cleanup(func() { catalogs = orig })
	catalogs = map[Lang]map[string]string{
		JA: {
			"inner.bad":    "設定は required か optional で指定してください: {value}",
			"outer.failed": "{login} の設定を保存できませんでした: {reason}",
			"mid.wrap":     "保存の途中で失敗しました: {reason}",
		},
		EN: {
			"inner.bad":    "The setting must be required or optional: {value}",
			"outer.failed": "Could not save the setting for {login}: {reason}",
			"mid.wrap":     "Failed while saving: {reason}",
		},
	}
	inner := Errorf("inner.bad", "value", `"x"`)
	err := Wrapf(inner, "outer.failed", "login", "alice")

	if got, want := Text(JA, err), `alice の設定を保存できませんでした: 設定は required か optional で指定してください: "x"`; got != want {
		t.Errorf("Text(JA) = %q, want %q", got, want)
	}
	if got, want := Text(EN, err), `Could not save the setting for alice: The setting must be required or optional: "x"`; got != want {
		t.Errorf("Text(EN) = %q, want %q", got, want)
	}
	// 包みが 2 段でも、いちばん内側の文面まで展開される
	deep := Wrapf(Wrapf(inner, "mid.wrap"), "outer.failed", "login", "alice")
	if got, want := Text(JA, deep), `alice の設定を保存できませんでした: 保存の途中で失敗しました: 設定は required か optional で指定してください: "x"`; got != want {
		t.Errorf("Text(JA, 2 段) = %q, want %q", got, want)
	}
	// i18n の error を fmt.Errorf で包んだものを Wrapf した場合も、内側の文面が出る
	viaFmt := Wrapf(fmt.Errorf("setup: %w", inner), "mid.wrap")
	if got, want := Text(EN, viaFmt), `Failed while saving: The setting must be required or optional: "x"`; got != want {
		t.Errorf("Text(EN, fmt で包んだものを Wrapf) = %q, want %q", got, want)
	}
	// 包んだ先の判定は壊れない
	if !errors.Is(err, inner) {
		t.Error("errors.Is が包んだ先まで届いていない")
	}
	// i18n の error でない中身は、これまでどおりそのままの文言が入る
	if got, want := Text(JA, Wrapf(os.ErrNotExist, "mid.wrap")), "保存の途中で失敗しました: file does not exist"; got != want {
		t.Errorf("Text(JA, 素の error) = %q, want %q", got, want)
	}
	// Error() は ID のまま（ログ用の設計を変えていないこと）
	if got, want := err.Error(), "outer.failed"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestEnglishCatalogHasNoJapanese は en.json の文面に日本語が混ざっていないことを確かめる。
//
// 見出しの引用がとくに危ない。internal/domain/heading.go は「## 受け入れ条件」「## 検証コマンド」に
// 英語の別名（## Acceptance criteria / ## Verify commands）を認めるので、英語の案内で日本語の見出しを
// 引くと「その綴りで書け」という誤った案内になる。英語の別名のほうを引くこと。
// enJapaneseAllowed は、en.json に日本語が残っていてよい ID と、その理由。
// 訳すと動かなくなるもの（AI に打ち込む合図の文字列など）だけを入れる。表示のためだけの文面は入れない。
var enJapaneseAllowed = map[string]string{
	// kit/core/skills/token-report/SKILL.md の description が、この呼び出し方を日本語でしか
	// 書いていない。画面はその文字列を「AI にこう伝えてください」と見せるだけなので、英語に
	// 訳すと skill が起動しなくなる（落ちないまま静かに効かなくなる類）。
	"server.web.report_requests.not_done_hint": "skill の起動語（kit/core/skills/token-report/SKILL.md）そのもの。訳すと起動しなくなる",
}

func TestEnglishCatalogHasNoJapanese(t *testing.T) {
	for _, id := range IDs(EN) {
		if _, ok := enJapaneseAllowed[id]; ok {
			if !containsJapanese(catalogs[EN][id]) {
				t.Errorf("enJapaneseAllowed の %q に日本語がありません（訳し終えたなら、この行を消してください）", id)
			}
			continue
		}
		if containsJapanese(catalogs[EN][id]) {
			t.Errorf("en.json の %q に日本語が混ざっています: %q\n"+
				"（見出しなら英語の別名を引く: 「## 受け入れ条件」→ \"## Acceptance criteria\"、"+
				"「## 検証コマンド」→ \"## Verify commands\"。internal/domain/heading.go）",
				id, catalogs[EN][id])
		}
	}
}

// 英語の自己申告の印は 1 つの語にそろえる（"self-reported via MCP"。利用者のガイドの記述と同じ）。
// 同じ印が画面・CLI・記録のコメントで別の語になると、人が同じものと見分けられず、文面で探しても片方が漏れる。
// "self-reported" を含む値を全部拾い、どれも印の語を含むことを見る。拾った件数が 0 なら、検査が空振りしている。
func TestEnglishSelfReportedMarkIsUniform(t *testing.T) {
	const mark = "self-reported via MCP"
	n := 0
	for id, v := range catalogs[EN] {
		if !strings.Contains(strings.ToLower(v), "self-reported") {
			continue
		}
		n++
		if !strings.Contains(v, mark) {
			t.Errorf("%s: 自己申告の印が %q になっていない: %q", id, mark, v)
		}
	}
	if n == 0 {
		t.Fatalf("前提が崩れている: en.json に \"self-reported\" を含む値が 1 つも無い（検査が空振りしている）")
	}
	t.Logf("self-reported を含む値: %d 件", n)
}

// TN は件数 n で単数と複数を分ける。n が 1 で <ID>_one があるときだけ単数の文面を使う。
func TestTN(t *testing.T) {
	orig := catalogs
	t.Cleanup(func() { catalogs = orig })
	catalogs = map[Lang]map[string]string{
		JA: {
			"items":     "{count} 件",
			"items_one": "{count} 件",
			"plain":     "{count} 個",
		},
		EN: {
			"items":     "{count} items",
			"items_one": "{count} item",
			"plain":     "{count} things",
		},
	}
	for _, tt := range []struct {
		lang Lang
		id   string
		n    int
		want string
	}{
		{EN, "items", 0, "0 items"},
		{EN, "items", 1, "1 item"},
		{EN, "items", 2, "2 items"},
		{EN, "items", 21, "21 items"},
		// 単数の文面が無ければ、1 件でも基の文面を使う
		{EN, "plain", 1, "1 things"},
		// 日本語は単数と複数で同じ文面（英語の対照）
		{JA, "items", 0, "0 件"},
		{JA, "items", 1, "1 件"},
		{JA, "items", 2, "2 件"},
		{JA, "plain", 1, "1 個"},
	} {
		if got := TN(tt.lang, tt.id, tt.n, "count", tt.n); got != tt.want {
			t.Errorf("TN(%s, %q, %d) = %q, want %q", tt.lang, tt.id, tt.n, got, tt.want)
		}
	}
	// 単数の文面を別の言語から借りない（英語に _one が無いとき、日本語の _one を出さない）
	delete(catalogs[EN], "items_one")
	if got, want := TN(EN, "items", 1, "count", 1), "1 items"; got != want {
		t.Errorf("英語に単数の文面が無いとき TN = %q, want %q", got, want)
	}
}

// MN は、件数つきの語句を別の文面の {名前} に埋める使い方と、error に持ち回る使い方ができる。
func TestMNAndCountedErrors(t *testing.T) {
	orig := catalogs
	t.Cleanup(func() { catalogs = orig })
	catalogs = map[Lang]map[string]string{
		JA: {
			"done": "{slug} {issues}・{comments}", "done.issues": "イシュー {n} 件", "done.issues_one": "イシュー {n} 件",
			"done.comments": "コメント {n} 件", "done.comments_one": "コメント {n} 件",
			"fail": "{count} 件の問題", "fail_one": "{count} 件の問題", "wrap": "{count} 件の途中: {reason}", "wrap_one": "{count} 件の途中: {reason}",
		},
		EN: {
			"done": "{slug}: {issues} and {comments}", "done.issues": "{n} issues", "done.issues_one": "{n} issue",
			"done.comments": "{n} comments", "done.comments_one": "{n} comment",
			"fail": "{count} problems", "fail_one": "{count} problem", "wrap": "after {count} items: {reason}", "wrap_one": "after {count} item: {reason}",
		},
	}
	// 2 つの件数を 1 つの文面に入れる（片方が 1 でも、もう片方は複数のまま）
	for _, tt := range []struct {
		lang   Lang
		si, sc int
		want   string
	}{
		{EN, 1, 2, "p: 1 issue and 2 comments"},
		{EN, 2, 1, "p: 2 issues and 1 comment"},
		{EN, 1, 1, "p: 1 issue and 1 comment"},
		{EN, 0, 0, "p: 0 issues and 0 comments"},
		{JA, 1, 1, "p イシュー 1 件・コメント 1 件"},
		{JA, 2, 0, "p イシュー 2 件・コメント 0 件"},
	} {
		got := T(tt.lang, "done", "slug", "p",
			"issues", MN("done.issues", tt.si, "n", tt.si), "comments", MN("done.comments", tt.sc, "n", tt.sc))
		if got != tt.want {
			t.Errorf("%s (%d, %d) = %q, want %q", tt.lang, tt.si, tt.sc, got, tt.want)
		}
	}
	// error（言語を決めずに持ち回る）も件数で選ぶ
	for _, tt := range []struct {
		lang Lang
		n    int
		want string
	}{{EN, 1, "1 problem"}, {EN, 3, "3 problems"}, {JA, 1, "1 件の問題"}, {JA, 3, "3 件の問題"}} {
		if got := Text(tt.lang, ErrorfN("fail", tt.n, "count", tt.n)); got != tt.want {
			t.Errorf("ErrorfN %s n=%d = %q, want %q", tt.lang, tt.n, got, tt.want)
		}
	}
	base := os.ErrNotExist
	err := WrapfN(base, "wrap", 1, "count", 1)
	if got, want := Text(EN, err), "after 1 item: file does not exist"; got != want {
		t.Errorf("WrapfN(1) = %q, want %q", got, want)
	}
	if got, want := Text(EN, WrapfN(base, "wrap", 2, "count", 2)), "after 2 items: file does not exist"; got != want {
		t.Errorf("WrapfN(2) = %q, want %q", got, want)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Error("errors.Is が包んだ先まで届いていない")
	}
	if got, want := MN("fail", 1, "count", 1).In(EN), "1 problem"; got != want {
		t.Errorf("MN.In(EN) = %q, want %q", got, want)
	}
}

// TNFunc はテンプレートから呼ぶ TN。件数は int のほか、読める整数型を受ける。
func TestTNFunc(t *testing.T) {
	orig := catalogs
	t.Cleanup(func() { catalogs = orig })
	catalogs = map[Lang]map[string]string{
		JA: {"items": "{n} 件", "items_one": "{n} 件"},
		EN: {"items": "{n} items", "items_one": "{n} item"},
	}
	for _, tt := range []struct {
		lang any
		n    any
		want string
	}{
		{EN, 1, "1 item"},
		{"en", int64(1), "1 item"},
		{EN, 2, "2 items"},
		{"en", uint(1), "1 item"},
		{JA, 1, "1 件"},
		{"ja", 2, "2 件"},
		{nil, 1, "1 件"}, // 言語が空なら、TFunc と同じく正本（日本語）で出る
		{EN, "1", "1 items"},
	} {
		n := tt.n
		if got := TNFunc(tt.lang, "items", n, "n", n); got != tt.want {
			t.Errorf("TNFunc(%v, %v) = %q, want %q", tt.lang, tt.n, got, tt.want)
		}
	}
}
