package domain

import (
	"strings"
	"unicode/utf8"
)

// コメントの先頭語（DESIGN.md §5-8-6）。
// 本文の**先頭**が「フィードバック:」「判断:」「差し戻し:」（コロンは半角・全角のどちらでも）なら、その種別。
// 先頭の空白・改行は許さない（SQL の LIKE 'フィードバック:%' で引けるようにするため）。
// 英語の別名（§5-13）: 「Feedback:」「Decision:」「Changes requested:」。英字の大小を問わない（ASCII だけで畳む。
// SQL の LOWER / UPPER と同じ結果にするため Unicode の畳み込みは使わない）。コロンは半角だけ。
// 例は testdata/leadword.json（leadword_test.go が読む。英語の別名の例は lead_en・excerpt_en・sequences_en）。

// 先頭語の種別。
const (
	LeadFeedback = "feedback"
	LeadDecision = "decision"
	LeadSendback = "sendback"
)

var leadWords = []struct {
	word, kind string
	en         bool // 英語の別名（英字の大小を問わない・半角コロンだけ）
}{
	{"フィードバック", LeadFeedback, false},
	{"判断", LeadDecision, false},
	{"差し戻し", LeadSendback, false},
	{"feedback", LeadFeedback, true},
	{"decision", LeadDecision, true},
	{"changes requested", LeadSendback, true},
}

// LeadWord はコメント本文の先頭語の種別（"feedback" / "decision" / "sendback"）を返す。無ければ ""。
func LeadWord(content string) string {
	kind, _ := leadWord(content)
	return kind
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
