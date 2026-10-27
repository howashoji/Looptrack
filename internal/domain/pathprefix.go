package domain

import "strings"

// パスの値を本文の中で置き換えるときの境界の規則。セッション要約の path_hints（DESIGN.md §9-7）と、
// 会話記録の搬送でパスを印に置き換える変換（§9-8）が同じ規則を使うよう、1 か所に置く。
// 置き換え先は呼ぶ側が渡す（要約は「./」「~」など、会話記録は印）。

// PathPrefix は置き換える値 1 つ（末尾の区切りを除いた形）と置き換え先。
type PathPrefix struct {
	Path, Repl string
}

// ReplacePathPrefixes は text の中の ps の値を置き換え先に置き換える。左から 1 回だけ読み、各位置で ps の並びの先頭から順に試す
// （長い値が先になるよう、並べるのは呼ぶ側）。置き換えた結果はもう読まない。
// 当たるのは、値の前が語の境界（先頭か、パスに使う文字（英数字・_ - . ~ / \）でない文字）で、
// 値の後ろが区切り（/ か \。区切りは残す）か語の境界（終わりか、英数字・_ - . でない文字。
// 「.」は後ろが英数字・_ - でなければ文の終わりの句点として境界と読む）のとき。
// /home/u/proj は /home/u/proj2 や /x/home/u/proj の中では当たらない。
func ReplacePathPrefixes(text string, ps []PathPrefix) string {
	if len(ps) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		if i == 0 || !pathByte(text[i-1], true) {
			if p, ok := matchPathPrefix(text[i:], ps); ok {
				b.WriteString(p.Repl)
				i += len(p.Path)
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

func matchPathPrefix(s string, ps []PathPrefix) (PathPrefix, bool) {
	for _, p := range ps {
		if !strings.HasPrefix(s, p.Path) {
			continue
		}
		rest := s[len(p.Path):]
		switch {
		case rest == "", rest[0] == '/', rest[0] == '\\':
			return p, true
		case rest[0] == '.':
			if len(rest) == 1 || !pathByte(rest[1], false) {
				return p, true
			}
		case !pathByte(rest[0], false):
			return p, true
		}
	}
	return PathPrefix{}, false
}

// pathByte はパスの語の途中に現れる ASCII の文字か（before なら値の前の文字として ~ / \ も含める）。
// ASCII でない文字（日本語の文の途中など）は境界と読む。
func pathByte(c byte, before bool) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '_', c == '-', c == '.':
		return true
	case before && (c == '~' || c == '/' || c == '\\'):
		return true
	}
	return false
}
