package transcript

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/i18n"
)

// JSON の文字列の字面を、元のバイトを保ったまま差し替えるための読み方。
//
// 変わった行を jsonorder.Marshal で書き直すと、区切り（", "・": "）と数の字面が変わり、
// 戻した後も元のバイトに戻らない（AI の記録は区切りに空白を入れず、数の字面も書いた側の形）。
// そこで、行を JSON として読めることは jsonorder.Decode で確かめたうえで、文字列の字面の範囲だけを
// ここで拾い、変わった文字列の、変わった部分だけを書き直す。区切り・数・キー・変わらない文字列は元のバイトのまま残る。

// seg はキーの経路の 1 段（オブジェクトのキーか、配列の要素）。
// at はキーの段のとき、その値の字面の先頭の位置（同じ名前のオブジェクトを 1 つずつ見分けるのに使う）。
type seg struct {
	key   string
	index bool
	at    int
}

// visitFunc は文字列の字面 1 つ（引用符を含む b[start:end]）ごとに呼ばれる。key はオブジェクトのキーのとき。
// path はその文字列の位置（値なら最後の段がキーか要素、キーなら持ち主のオブジェクトまで）。
type visitFunc func(path []seg, key bool, start, end int)

type tokScanner struct {
	b     []byte
	i     int
	path  []seg
	visit visitFunc
}

// scanJSON は b（JSON の値 1 つ。前後の空白は許す）の文字列の字面を順に visit に渡す。
// 読めることは呼ぶ側が jsonorder.Decode で先に確かめる。ここでは字面の範囲を拾うだけで、形の誤りはエラーにする。
func scanJSON(b []byte, visit visitFunc) error {
	sc := &tokScanner{b: b, visit: visit}
	if err := sc.value(); err != nil {
		return err
	}
	sc.ws()
	if sc.i != len(sc.b) {
		return errMalformed()
	}
	return nil
}

func errMalformed() error { return i18n.Errorf("transcript.err.malformed") }

func (sc *tokScanner) ws() {
	for sc.i < len(sc.b) {
		switch sc.b[sc.i] {
		case ' ', '\t', '\n', '\r':
			sc.i++
		default:
			return
		}
	}
}

func (sc *tokScanner) value() error {
	sc.ws()
	if sc.i >= len(sc.b) {
		return errMalformed()
	}
	switch sc.b[sc.i] {
	case '{':
		sc.i++
		sc.ws()
		if sc.i < len(sc.b) && sc.b[sc.i] == '}' {
			sc.i++
			return nil
		}
		for {
			sc.ws()
			if sc.i >= len(sc.b) || sc.b[sc.i] != '"' {
				return errMalformed()
			}
			start := sc.i
			end, err := sc.str()
			if err != nil {
				return err
			}
			sc.visit(sc.path, true, start, end)
			key, _, _ := decodeString(sc.b[start:end])
			sc.ws()
			if sc.i >= len(sc.b) || sc.b[sc.i] != ':' {
				return errMalformed()
			}
			sc.i++
			sc.ws()
			sc.path = append(sc.path, seg{key: key, at: sc.i})
			if err := sc.value(); err != nil {
				return err
			}
			sc.path = sc.path[:len(sc.path)-1]
			sc.ws()
			if sc.i >= len(sc.b) {
				return errMalformed()
			}
			switch sc.b[sc.i] {
			case ',':
				sc.i++
			case '}':
				sc.i++
				return nil
			default:
				return errMalformed()
			}
		}
	case '[':
		sc.i++
		sc.ws()
		if sc.i < len(sc.b) && sc.b[sc.i] == ']' {
			sc.i++
			return nil
		}
		for {
			sc.path = append(sc.path, seg{index: true})
			if err := sc.value(); err != nil {
				return err
			}
			sc.path = sc.path[:len(sc.path)-1]
			sc.ws()
			if sc.i >= len(sc.b) {
				return errMalformed()
			}
			switch sc.b[sc.i] {
			case ',':
				sc.i++
			case ']':
				sc.i++
				return nil
			default:
				return errMalformed()
			}
		}
	case '"':
		start := sc.i
		end, err := sc.str()
		if err != nil {
			return err
		}
		sc.visit(sc.path, false, start, end)
		return nil
	default:
		// 数・true・false・null。区切りまで読み飛ばす（字面は触らない）
		start := sc.i
		for sc.i < len(sc.b) {
			switch sc.b[sc.i] {
			case ',', ']', '}', ' ', '\t', '\n', '\r':
				if sc.i == start {
					return errMalformed()
				}
				return nil
			}
			sc.i++
		}
		if sc.i == start {
			return errMalformed()
		}
		return nil
	}
}

// str は sc.i の '"' から始まる文字列の字面の終わり（閉じの引用符の次）を返す。
func (sc *tokScanner) str() (int, error) {
	sc.i++
	for sc.i < len(sc.b) {
		switch sc.b[sc.i] {
		case '\\':
			sc.i += 2
		case '"':
			sc.i++
			return sc.i, nil
		default:
			sc.i++
		}
	}
	return 0, errMalformed()
}

// decodeString は文字列の字面（引用符を含む）を読み、値と、値の中の位置と字面の中の位置の対応を返す。
// 対応は「単位」（逃がしの 1 つか、生の 1 文字）の始まりごとに 1 組で、最後に終わりの組が付く。
// 読み方は encoding/json と同じ（対にならないサロゲートと UTF-8 でないバイトは U+FFFD）。
func decodeString(tok []byte) (string, []int, []int) {
	body := tok[1 : len(tok)-1]
	var b strings.Builder
	b.Grow(len(body))
	var decOff, rawOff []int
	for j := 0; j < len(body); {
		decOff = append(decOff, b.Len())
		rawOff = append(rawOff, j)
		c := body[j]
		if c == '\\' && j+1 < len(body) {
			switch body[j+1] {
			case '"', '\\', '/':
				b.WriteByte(body[j+1])
				j += 2
			case 'b':
				b.WriteByte('\b')
				j += 2
			case 'f':
				b.WriteByte('\f')
				j += 2
			case 'n':
				b.WriteByte('\n')
				j += 2
			case 'r':
				b.WriteByte('\r')
				j += 2
			case 't':
				b.WriteByte('\t')
				j += 2
			case 'u':
				r, ok := hex4(body, j+2)
				if !ok {
					b.WriteRune(utf8.RuneError)
					j += 2
					break
				}
				j += 6
				if utf16.IsSurrogate(r) {
					if r < 0xDC00 && j+6 <= len(body) && body[j] == '\\' && body[j+1] == 'u' {
						if r2, ok := hex4(body, j+2); ok && r2 >= 0xDC00 && r2 <= 0xDFFF {
							b.WriteRune(utf16.DecodeRune(r, r2))
							j += 6
							break
						}
					}
					b.WriteRune(utf8.RuneError)
					break
				}
				b.WriteRune(r)
			default:
				b.WriteByte(body[j+1])
				j += 2
			}
			continue
		}
		r, size := utf8.DecodeRune(body[j:])
		if r == utf8.RuneError && size <= 1 {
			b.WriteRune(utf8.RuneError)
			j++
			continue
		}
		b.Write(body[j : j+size])
		j += size
	}
	decOff = append(decOff, b.Len())
	rawOff = append(rawOff, len(body))
	return b.String(), decOff, rawOff
}

func hex4(b []byte, at int) (rune, bool) {
	if at+4 > len(b) {
		return 0, false
	}
	v, err := strconv.ParseUint(string(b[at:at+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(v), true
}

// jsonInner は s を JSON の文字列の中身（引用符なし）にする。逃がすのは JavaScript の JSON.stringify と
// Rust の serde_json が逃がすものだけ（" \ と制御文字）。ASCII でない文字と / はそのまま。
func jsonInner(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				const hexd = "0123456789abcdef"
				b.WriteString(`\u00`)
				b.WriteByte(hexd[c>>4])
				b.WriteByte(hexd[c&0xF])
				continue
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// spliceString は字面 tok（値は old）を、値が nw になる字面にする。old と nw の共通の頭と尻は元の字面のまま残し、
// 間だけを jsonInner で書く（元の字面の逃がし方が JSON.stringify と違っても、触らない部分は変わらない）。
func spliceString(tok []byte, old string, decOff, rawOff []int, nw string) []byte {
	p := 0
	for p < len(old) && p < len(nw) && old[p] == nw[p] {
		p++
	}
	q := 0
	for q < len(old)-p && q < len(nw)-p && old[len(old)-1-q] == nw[len(nw)-1-q] {
		q++
	}
	// 頭は p 以下の単位の境、尻は len(old)-q 以上の単位の境にそろえる
	a := sort.Search(len(decOff), func(k int) bool { return decOff[k] > p }) - 1
	z := sort.Search(len(decOff), func(k int) bool { return decOff[k] >= len(old)-q })
	da, dz := decOff[a], decOff[z]
	mid := nw[da : len(nw)-(len(old)-dz)]
	out := make([]byte, 0, len(tok)+len(nw)-len(old)+8)
	out = append(out, tok[:1+rawOff[a]]...)
	out = append(out, jsonInner(mid)...)
	out = append(out, tok[1+rawOff[z]:]...)
	return out
}
