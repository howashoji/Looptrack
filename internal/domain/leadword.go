package domain

import (
	"strings"
	"unicode/utf8"
)

// コメントの先頭語（DESIGN.md §9-3-6）。
// 本文の**先頭**が「フィードバック:」「判断:」「差し戻し:」（コロンは半角・全角のどちらでも）なら、その種別。
// 先頭の空白・改行は許さない（SQL の LIKE 'フィードバック:%' で引けるようにするため）。
// 英語の別名（DESIGN.md §9-6）: 「Feedback:」「Decision:」「Changes requested:」。英字の大小を問わない（ASCII だけで畳む。
// SQL の LOWER / UPPER と同じ結果にするため Unicode の畳み込みは使わない）。コロンは半角だけ。
// 「セッション要約:」「Session digest:」は別の端末のセッションに作業の文脈を渡す要約の印（DESIGN.md §9-7）。
// 判定の規則はほかの先頭語と同じで、見出しの検査・パスの正規化・秘密の検査は service（digest.go）が行う。
// 例は testdata/leadword.json（leadword_test.go が読む。英語の別名の例は lead_en・excerpt_en・sequences_en）。

// 先頭語の種別。
const (
	LeadFeedback = "feedback"
	LeadDecision = "decision"
	LeadSendback = "sendback"
	LeadDigest   = "digest"
)

var leadWords = []struct {
	word, kind string
	en         bool // 英語の別名（英字の大小を問わない・半角コロンだけ）
}{
	{"フィードバック", LeadFeedback, false},
	{"判断", LeadDecision, false},
	{"差し戻し", LeadSendback, false},
	{"セッション要約", LeadDigest, false},
	{"feedback", LeadFeedback, true},
	{"decision", LeadDecision, true},
	{"changes requested", LeadSendback, true},
	{"session digest", LeadDigest, true},
}

// LeadWord はコメント本文の先頭語の種別（"feedback" / "decision" / "sendback" / "digest"）を返す。無ければ ""。
func LeadWord(content string) string {
	kind, _ := leadWord(content)
	return kind
}

// LeadWordRest は種別と、先頭語（コロンまで）を除いた残りを返す（種別が "" なら content のまま）。
func LeadWordRest(content string) (kind, rest string) {
	return leadWord(content)
}

// leadWord は種別と、先頭語（コロンまで）を除いた残りを返す。
func leadWord(content string) (string, string) {
	for _, w := range leadWords {
		if w.en {
			if len(content) > len(w.word) && asciiEqualFold(content[:len(w.word)], w.word) && content[len(w.word)] == ':' {
				return w.kind, content[len(w.word)+1:]
			}
			continue
		}
		rest, ok := strings.CutPrefix(content, w.word)
		if !ok {
			continue
		}
		for _, colon := range []string{":", "："} {
			if r, ok := strings.CutPrefix(rest, colon); ok {
				return w.kind, r
			}
		}
	}
	return "", content
}

// LeadWordSpellings は種別 kind の先頭語の綴りを返す（SQL の前方一致で引く側が、綴りを自分で写さずに済むように
// 先頭語の一覧をここに 1 か所だけ置く）。ja はコロンまで含む前方一致の文字列（半角・全角の両方。大小を区別する）、
// en は小文字のコロンまで含む綴り（ASCII の大小を問わず比べる。コロンは半角だけ）。
func LeadWordSpellings(kind string) (ja, en []string) {
	for _, w := range leadWords {
		if w.kind != kind {
			continue
		}
		if w.en {
			en = append(en, w.word+":")
		} else {
			ja = append(ja, w.word+":", w.word+"：")
		}
	}
	return ja, en
}

// asciiEqualFold は s と t（小文字の ASCII）が ASCII の英字の大小を除いて等しいか（K（U+212A）を k と読まない）。
func asciiEqualFold(s, t string) bool {
	if len(s) != len(t) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != t[i] {
			return false
		}
	}
	return true
}

// FeedbackExcerpt は summary に出すフィードバックの抜粋（先頭語を除き、空白・改行を 1 つの空白に詰め、n 文字まで。
// 切ったら末尾に「…」）。
func FeedbackExcerpt(content string, n int) string {
	_, rest := leadWord(content)
	s := strings.Join(strings.Fields(rest), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
