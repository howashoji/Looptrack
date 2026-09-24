package domain

import (
	"regexp"
	"strings"
)

// 本文の中で意味を読む節の見出し（DESIGN.md §5-8-1・§5-13）。
// 「## 受け入れ条件」（next の受け入れ条件）と「## 検証コマンド」（verify・require_on_close）の見出しを 1 か所で決める。
// どちらも英語の別名（## Acceptance criteria / ## Verify commands。英字の大小を問わない）を常に認める。
// 規則は日本語と同じ: 行の先頭の「##」の後に空白 1 つ以上・語・行末の空白だけ。コードブロックの中の行は見出しとしない。
// 範囲は次の（コードブロックの外の）「## 」見出しまで。見出しが 2 つ以上あれば最初のものだけ（日本語と英語が混ざっても同じ）。
//
// 英字の大小は ASCII だけで畳む（regexp の (?i) は Unicode の畳み込みで ſ（U+017F）を s と読むため使わない）。

// AcceptanceHeading は受け入れ条件の節の見出し。
var AcceptanceHeading = headingRe("受け入れ条件", "acceptance criteria")

// VerifyHeading は検証コマンドの節の見出し。
var VerifyHeading = headingRe("検証コマンド", "verify commands")

// headingRe は「## 日本語」または「## 英語の別名」の 1 行に一致する正規表現を作る。
// 英語の語の間の空白は 1 つ以上の空白・タブを許す。
func headingRe(ja, en string) *regexp.Regexp {
	var b strings.Builder
	for i, w := range strings.Fields(en) {
		if i > 0 {
			b.WriteString(`[ \t]+`)
		}
		for _, r := range w {
			if r >= 'a' && r <= 'z' {
				b.WriteString("[" + string(r) + string(r-'a'+'A') + "]")
			} else {
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
	}
	return regexp.MustCompile(`^##[ \t]+(?:` + regexp.QuoteMeta(ja) + `|` + b.String() + `)[ \t]*$`)
}

// HasAcceptanceSection は本文に受け入れ条件の見出し（コードブロックの外）があるか。
func HasAcceptanceSection(body string) bool {
	_, found := SectionLines(body, AcceptanceHeading)
	return found
}

// AcceptanceCriteria は本文の受け入れ条件の節の中身（見出しの次の行から次の ## 見出しの前まで。前後の空白を除く）。無ければ空。
func AcceptanceCriteria(body string) string {
	lines, found := SectionLines(body, AcceptanceHeading)
	if !found {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
