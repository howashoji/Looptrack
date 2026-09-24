package i18n

import (
	"errors"
	"fmt"
	"os"
	"slices"
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
