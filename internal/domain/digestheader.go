package domain

import (
	"strings"
	"unicode/utf8"
)

// セッション要約（DESIGN.md §9-7）の見出しの読み方。検査（必須・値の形・重なり）は service（digest.go）が行い、
// ここには「どの行を見出しの項目として読むか」だけを置く。サーバの検査と、CLI が欠けた項目を足す処理
// （looptrack session digest）が同じ読み方をするよう、読み方そのものは 1 か所にする。

// DigestHeaderLines は、見出しの必須の行（branch・head・ai）を探す範囲（先頭からの行数。1 行目の印と端末のラベルを含む）。
const DigestHeaderLines = 8

// DigestHeaderKeys は見出しの必須の項目（この順に並べる）。
var DigestHeaderKeys = []string{"branch", "head", "ai"}

// DigestHeaderField は見出しの 1 行を「キー: 値」として読む（キーが branch・head・ai のどれかのときだけ ok）。
// キーは前後の空白を除いて ASCII の大小を問わず、コロンは半角・全角のどちらでもよい。値は前後の空白を除く。
func DigestHeaderField(line string) (key, val string, ok bool) {
	i := strings.IndexAny(line, ":：")
	if i < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:i]))
	switch key {
	case "branch", "head", "ai":
	default:
		return "", "", false
	}
	_, size := utf8.DecodeRuneInString(line[i:])
	return key, strings.TrimSpace(line[i+size:]), true
}
