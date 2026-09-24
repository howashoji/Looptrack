// Package textenc は、利用者が手元で作ったテキスト（JSON など）の BOM と UTF-16 を UTF-8 に揃える。
//
// Windows の PowerShell 5.1 は `>`・Out-File で UTF-16LE（BOM 付き）のファイルを書き、メモ帳などは UTF-8 に BOM を付ける。
// `looptrack issue usage report --json > r.json` のように作ったファイルを `--from-report r.json` で読むと、
// JSON として読めずに止まる。読む側で BOM を見て揃える（BOM の無い UTF-16 は推測しない）。
package textenc

import (
	"unicode/utf16"
	"unicode/utf8"
)

// Decode は b の先頭の BOM を見て UTF-8 にする。
//
//   - EF BB BF（UTF-8 の BOM）: 外す
//   - FF FE（UTF-16LE）・FE FF（UTF-16BE）: UTF-8 に直す（対になっていない代理は U+FFFD）
//   - それ以外: そのまま
func Decode(b []byte) []byte {
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return b[3:]
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return fromUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return fromUTF16(b[2:], true)
	}
	return b
}

func fromUTF16(b []byte, bigEndian bool) []byte {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	out := make([]byte, 0, len(u)*2)
	for _, r := range utf16.Decode(u) {
		out = utf8.AppendRune(out, r)
	}
	if len(b)%2 == 1 { // 半端な 1 バイト
		out = utf8.AppendRune(out, utf8.RuneError)
	}
	return out
}
