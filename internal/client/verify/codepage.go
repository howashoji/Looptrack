package verify

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// Windows の ANSI コードページの出力を読む。
//
// 検証コマンドの多くは UTF-8 で書く（Git Bash・Go など）が、cmd.exe の組み込みコマンドや古い Windows の
// ツールはパイプへ ANSI コードページ（日本語版は cp932 = Shift_JIS）で書く。そのまま U+FFFD に置き換えると日本語の
// エラーメッセージが全部読めなくなるので、Windows では出力全体が UTF-8 として不正なときに限り、ANSI コードページとして
// 読み直す（読み直しても不正な並びが残るなら、従来どおり UTF-8 の置き換えにする）。macOS・Linux は変えない。

// codePage は今の ANSI コードページ（Windows の GetACP。他の OS は 0 = 読み直さない）。
var codePage = func() int { return 0 }

// legacyEncoding はコードページの番号に対応する符号化（知らない番号・UTF-8（65001）は nil）。
func legacyEncoding(cp int) encoding.Encoding {
	switch cp {
	case 932:
		return japanese.ShiftJIS
	case 936:
		return simplifiedchinese.GBK
	case 949:
		return korean.EUCKR
	case 950:
		return traditionalchinese.Big5
	case 874:
		return charmap.Windows874
	case 1250:
		return charmap.Windows1250
	case 1251:
		return charmap.Windows1251
	case 1252:
		return charmap.Windows1252
	case 1253:
		return charmap.Windows1253
	case 1254:
		return charmap.Windows1254
	case 1255:
		return charmap.Windows1255
	case 1256:
		return charmap.Windows1256
	case 1257:
		return charmap.Windows1257
	case 1258:
		return charmap.Windows1258
	}
	return nil
}

// decodeLegacy は b をコードページ cp として読む。UTF-8 として正しい・読めない・不正な並びが残るなら ok=false。
func decodeLegacy(cp int, b []byte) (string, bool) {
	if utf8.Valid(b) {
		return "", false
	}
	enc := legacyEncoding(cp)
	if enc == nil {
		return "", false
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil || !utf8.Valid(out) || strings.ContainsRune(string(out), utf8.RuneError) {
		return "", false
	}
	return string(out), true
}
