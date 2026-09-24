package domain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
)

// 見出しの英語の別名（DESIGN.md §5-13）。## Acceptance criteria / ## Verify commands は英字の大小を問わず、
// 日本語の見出しと同じ規則（前後の空白・コードブロックの中は見出しでない・次の ## まで・最初の 1 つだけ）で読む。

func TestHeadingAliases(t *testing.T) {
	yes := []string{
		"## 受け入れ条件", "##  受け入れ条件 ", "##\t受け入れ条件\t",
		"## Acceptance criteria", "## Acceptance Criteria", "## ACCEPTANCE CRITERIA", "## acceptance criteria",
		"##  Acceptance  criteria  ", "##\tAcceptance\tCriteria",
	}
	no := []string{
		"##受け入れ条件", "### 受け入れ条件", " ## 受け入れ条件", "## 受け入れ条件です",
		"##Acceptance criteria", "### Acceptance criteria", " ## Acceptance criteria", "## Acceptance", "## AcceptanceCriteria",
		"## Acceptance criteria:", "## Acceptance criterias", "# Acceptance criteria",
	}
	for _, l := range yes {
		if !AcceptanceHeading.MatchString(l) {
			t.Errorf("見出しとして読まない: %q", l)
		}
	}
	for _, l := range no {
		if AcceptanceHeading.MatchString(l) {
			t.Errorf("見出しとして読んだ: %q", l)
		}
	}
	for _, l := range []string{"## 検証コマンド", "## Verify commands", "## Verify Commands", "## VERIFY COMMANDS"} {
		if !VerifyHeading.MatchString(l) {
			t.Errorf("検証コマンドの見出しとして読まない: %q", l)
		}
	}
	// regexp の (?i) だと ſ（U+017F）を s と読む。ASCII だけで畳む
	for _, l := range []string{"## Verify command\u017f", "## Verify command"} {
		if VerifyHeading.MatchString(l) {
			t.Errorf("検証コマンドの見出しとして読んだ: %q", l)
		}
	}
}

// 英語の見出しの本文は、日本語の見出しの本文と同じ結果になる（大小の違い・コードブロック内の見出し・次の節・2 つの節）。
func TestHeadingAliasesSameAsJapanese(t *testing.T) {
	type pair struct{ acc, ver string }
	variants := []pair{
		{"## 受け入れ条件", "## 検証コマンド"},
		{"## Acceptance criteria", "## Verify commands"},
		{"## ACCEPTANCE CRITERIA", "## verify COMMANDS"},
	}
	bodies := []struct {
		name       string
		body       string // {A} = 受け入れ条件の見出し・{V} = 検証コマンドの見出し
		acceptance string
		commands   []string
	}{
		{"両方の節", "説明\n\n{A}\n\n- [ ] 動く\n- [ ] テストが通る\n\n{V}\n\n```bash\ngo test ./...\n# コメント\nmake lint\n```\n\n## 関連\n\n- `false`\n",
			"- [ ] 動く\n- [ ] テストが通る", []string{"go test ./...", "make lint"}},
		{"コードブロック内の見出しは読まない", "説明\n\n```md\n{A}\n- [ ] 例\n{V}\necho x\n```\n",
			"", nil},
		{"コードブロック内の見出しの後の本物の節", "```md\n{A}\n- [ ] 例\n```\n\n{A}\n\n- [ ] 本物\n",
			"- [ ] 本物", nil},
		{"節の中のコードブロックの ## では終わらない", "{A}\n\n- [ ] 次を出す:\n```md\n## 見出し\n```\n- [ ] 2 つ目\n\n## 関連\n",
			"- [ ] 次を出す:\n```md\n## 見出し\n```\n- [ ] 2 つ目", nil},
		{"節が 2 つなら最初だけ", "{A}\n\n- [ ] 1 つ目\n\n{A}\n\n- [ ] 2 つ目\n\n{V}\n\n- `a`\n\n{V}\n\n- `b`\n",
			"- [ ] 1 つ目", []string{"a"}},
		{"CRLF", "説明\r\n\r\n{A}\r\n\r\n- [ ] 動く\r\n\r\n{V}\r\n\r\n- `true`\r\n",
			"- [ ] 動く", []string{"true"}},
		{"節なし", "説明だけ\n\n## 内容\n\n- `true`\n", "", nil},
	}
	for _, b := range bodies {
		for _, v := range variants {
			body := strings.NewReplacer("{A}", v.acc, "{V}", v.ver).Replace(b.body)
			if got := AcceptanceCriteria(body); got != b.acceptance {
				t.Errorf("%s（%s）: AcceptanceCriteria = %q, want %q", b.name, v.acc, got, b.acceptance)
			}
			if got := HasAcceptanceSection(body); got != (b.acceptance != "") {
				t.Errorf("%s（%s）: HasAcceptanceSection = %v", b.name, v.acc, got)
			}
			if got := VerifyCommands(body); fmt.Sprint(got) != fmt.Sprint(b.commands) {
				t.Errorf("%s（%s）: VerifyCommands = %q, want %q", b.name, v.ver, got, b.commands)
			}
		}
	}
	// 日本語と英語が混ざってもよい。最初の見出しだけ
	mixed := "## Acceptance criteria\n\n- [ ] en\n\n## 受け入れ条件\n\n- [ ] ja\n\n## 検証コマンド\n\n- `ja`\n\n## Verify commands\n\n- `en`\n"
	if got := AcceptanceCriteria(mixed); got != "- [ ] en" {
		t.Errorf("混在の受け入れ条件: %q", got)
	}
	if got := VerifyCommands(mixed); fmt.Sprint(got) != "[ja]" {
		t.Errorf("混在の検証コマンド: %q", got)
	}
}

// 起票の雛形: 本文が英語の受け入れ条件の見出しを持つなら、テンプレートの節（日本語）を付けない（日本語の見出しのときと同じ）。
func TestNewDocumentEnglishAcceptance(t *testing.T) {
	for _, tc := range []struct {
		body string
		tmpl bool
	}{
		{"説明\n\n## Acceptance criteria\n\n- [ ] works", false},
		{"説明\n\n## acceptance CRITERIA\n- [ ] works", false},
		{"説明\n\n```md\n## Acceptance criteria\n```\n", true}, // コードブロックの中は見出しでない
		{"説明だけ", true},
	} {
		doc := NewDocument("X-0001", NewIssueInput{Title: "t", Type: "task", Status: "Todo", Priority: "P2", Body: tc.body}, "2026-09-19 10:00", i18n.JA)
		if got := strings.Contains(doc.BodyMain, strings.TrimSpace(i18n.T(i18n.JA, "domain.template.acceptance"))); got != tc.tmpl {
			t.Errorf("%q: テンプレートの受け入れ条件節の有無 = %v, want %v", tc.body, got, tc.tmpl)
		}
	}
}

// 起票の雛形は**起票した利用者の言語**で入る（DESIGN.md §9-6）。
// 日本語の雛形でも英語の雛形でも、受け入れ条件の取り出し（next・require_on_close が使う）は同じように効く。
// コメント節の見出し（mdformat.CommentSection）は構造の目印なので、どちらの言語でも訳さない。
func TestNewDocumentTemplateFollowsLang(t *testing.T) {
	for _, tc := range []struct {
		lang            i18n.Lang
		heading, verify string
	}{
		{i18n.JA, "## 受け入れ条件", "## 検証コマンド"},
		{i18n.EN, "## Acceptance criteria", "## Verify commands"},
	} {
		doc := NewDocument("X-0001", NewIssueInput{Title: "起票の雛形", Type: "task", Status: "Todo", Priority: "P2"},
			"2026-09-20 10:00", tc.lang)
		body := doc.BodyMain
		if !strings.Contains(body, tc.heading+"\n") {
			t.Errorf("%s: 雛形の受け入れ条件の見出しが %q でない:\n%s", tc.lang, tc.heading, body)
		}
		if !doc.HasCommentSection {
			t.Errorf("%s: コメント節が見つからない（見出しを訳すと mdformat が分割できない）:\n%s", tc.lang, mdformat.Render(doc))
		}
		if !HasAcceptanceSection(body) {
			t.Errorf("%s: 受け入れ条件の節を取り出せない:\n%s", tc.lang, body)
		}
		if got := AcceptanceCriteria(body); !strings.HasPrefix(got, "- [ ] ") {
			t.Errorf("%s: 受け入れ条件の中身 = %q", tc.lang, got)
		}
		// 同じ本文に反対の言語の見出しを足しても、検証コマンドは別名で取り出せる
		withVerify := body + "\n\n" + tc.verify + "\n\n- `go test ./...`\n"
		if got := VerifyCommands(withVerify); len(got) != 1 || got[0] != "go test ./..." {
			t.Errorf("%s: 検証コマンド = %q", tc.lang, got)
		}
		t.Logf("lang=%s 受け入れ条件=%q 検証コマンド=%q\n%s", tc.lang, AcceptanceCriteria(body), VerifyCommands(withVerify), body)
	}
}

// Web の起票フォーム（render.js の buildIssueBody）は、本文の受け入れ条件の見出しを
// domain.template.acceptance_heading（作成者の言語）から取る。その見出しが
//   - 雛形（domain.template.acceptance）の見出しと同じ語であること（言語ごとに揃う）
//   - AcceptanceHeading が読めること（日英どちらも）
//   - それを含む本文から起票すると雛形の節が重ならず、反対の言語の見出しが混ざらないこと
//
// を確かめる。あわせて、既に反対の言語の見出しで書かれた本文からも節を取り出せることを見る（既存のイシューが壊れない）。
func TestAcceptanceHeadingKeyFollowsLang(t *testing.T) {
	for _, tc := range []struct {
		lang        i18n.Lang
		want, other string
	}{
		{i18n.JA, "## 受け入れ条件", "## Acceptance criteria"},
		{i18n.EN, "## Acceptance criteria", "## 受け入れ条件"},
	} {
		h := i18n.T(tc.lang, "domain.template.acceptance_heading")
		if h != tc.want {
			t.Errorf("%s: domain.template.acceptance_heading = %q, want %q", tc.lang, h, tc.want)
		}
		if first := strings.SplitN(i18n.T(tc.lang, "domain.template.acceptance"), "\n", 2)[0]; first != h {
			t.Errorf("%s: 雛形の見出し %q と起票フォームの見出し %q が違う", tc.lang, first, h)
		}
		if !AcceptanceHeading.MatchString(h) {
			t.Errorf("%s: %q を受け入れ条件の見出しとして読まない", tc.lang, h)
		}
		// buildIssueBody が作る形の本文
		body := i18n.T(tc.lang, "domain.template.empty") + "\n\n" + h + "\n\n- [ ] works"
		doc := NewDocument("X-0001", NewIssueInput{Title: "t", Type: "task", Status: "Todo", Priority: "P2", Body: body}, "2026-09-24 10:00", tc.lang)
		if n := strings.Count(doc.BodyMain, h); n != 1 {
			t.Errorf("%s: 見出し %q が %d 回（want 1）:\n%s", tc.lang, h, n, doc.BodyMain)
		}
		if strings.Contains(doc.BodyMain, tc.other) {
			t.Errorf("%s: 反対の言語の見出し %q が混ざった:\n%s", tc.lang, tc.other, doc.BodyMain)
		}
		if got := AcceptanceCriteria(doc.BodyMain); got != "- [ ] works" {
			t.Errorf("%s: 受け入れ条件 = %q", tc.lang, got)
		}
		// 既存の本文（反対の言語の見出しで書かれたもの）からも受け入れ条件を取り出せる
		old := "text\n\n" + tc.other + "\n\n- [ ] legacy"
		if !HasAcceptanceSection(old) || AcceptanceCriteria(old) != "- [ ] legacy" {
			t.Errorf("%s: 既存の見出し %q の本文から受け入れ条件を取り出せない", tc.lang, tc.other)
		}
	}
}
