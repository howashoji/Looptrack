// Package jsonorder は、キーの順と数の字面を保つ JSON を読み書きする。
//
// --json の出力は、以前の CLI（1.0.0 より前）が出していた形をそのまま引き継ぐ。これを読む道具が壊れないよう、次を守る。
//
//   - オブジェクトのキーは受け取った順のまま（Object）。
//   - 数は字面を保ち（Number）、出すときに決まった形にする（整数はそのまま・小数は 1.0 / 1e-05 / 1e+16 の形）。
//   - 文字列は ASCII 以外をそのまま出す（制御文字は \n などに逃がす）。サーバの < なども解いて出す。
//   - 字下げ 2 の改行と字下げ、空の {} と []。区切りは字下げなしのとき ", " と ": "。
package jsonorder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Number は JSON の数の字面。
type Number string

// Member はオブジェクトの 1 項目。
type Member struct {
	Key   string
	Value any
}

// Object はキーの順を保つ JSON のオブジェクト。値は nil・bool・Number・string・[]any・*Object。
type Object struct {
	Members []Member
}

// NewObject は空のオブジェクト。
func NewObject() *Object { return &Object{} }

// Get はキーの値（キーが重なっていれば後の値）。
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	for i := len(o.Members) - 1; i >= 0; i-- {
		if o.Members[i].Key == key {
			return o.Members[i].Value, true
		}
	}
	return nil, false
}

// Has はキーがあるか。
func (o *Object) Has(key string) bool { _, ok := o.Get(key); return ok }

// Set は値を置き換える（無ければ末尾に足す。既にあるキーは元の位置のまま）。
func (o *Object) Set(key string, v any) *Object {
	for i := range o.Members {
		if o.Members[i].Key == key {
			o.Members[i].Value = v
			// 重なったキーの後ろの分を除く
			rest := o.Members[i+1:]
			kept := o.Members[:i+1]
			for _, m := range rest {
				if m.Key != key {
					kept = append(kept, m)
				}
			}
			o.Members = kept
			return o
		}
	}
	o.Members = append(o.Members, Member{key, v})
	return o
}

// Delete はキーを除く（無ければ何もしない）。
func (o *Object) Delete(key string) {
	kept := o.Members[:0]
	for _, m := range o.Members {
		if m.Key != key {
			kept = append(kept, m)
		}
	}
	o.Members = kept
}

// Keys はキーの一覧（順のまま・重なりは 1 つ）。
func (o *Object) Keys() []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range o.Members {
		if !seen[m.Key] {
			seen[m.Key] = true
			out = append(out, m.Key)
		}
	}
	return out
}

// Clone は浅くない複製。
func (o *Object) Clone() *Object {
	if o == nil {
		return nil
	}
	c := &Object{Members: make([]Member, len(o.Members))}
	for i, m := range o.Members {
		c.Members[i] = Member{m.Key, clone(m.Value)}
	}
	return c
}

func clone(v any) any {
	switch x := v.(type) {
	case *Object:
		return x.Clone()
	case []any:
		c := make([]any, len(x))
		for i := range x {
			c[i] = clone(x[i])
		}
		return c
	}
	return v
}

// String はキーの値が文字列ならそれを返す（無い・文字列でなければ ""）。
func (o *Object) String(key string) string {
	v, _ := o.Get(key)
	s, _ := v.(string)
	return s
}

// Object はキーの値がオブジェクトならそれを返す。
func (o *Object) Object(key string) *Object {
	v, _ := o.Get(key)
	x, _ := v.(*Object)
	return x
}

// Decode は JSON を読む（キーの順と数の字面を保つ）。余分な値が続けばエラー。
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("JSON の後ろに余分なデータがあります")
	}
	return v, nil
}

// DecodeObject は JSON のオブジェクトを読む（オブジェクトでなければエラー）。
func DecodeObject(data []byte) (*Object, error) {
	v, err := Decode(data)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("JSON のオブジェクトではありません")
	}
	return o, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			o := &Object{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("オブジェクトのキーが文字列ではありません: %v", kt)
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.Members = append(o.Members, Member{k, v})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			a := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return a, nil
		}
		return nil, fmt.Errorf("想定しない区切り: %v", x)
	case json.Number:
		return Number(x), nil
	case string, bool, nil:
		return x, nil
	}
	return nil, fmt.Errorf("想定しない値: %v", t)
}

// Indent は json.dumps(v, ensure_ascii=False, indent=n) と同じ文字列（末尾の改行なし）。
func Indent(v any, n int) string {
	var b strings.Builder
	write(&b, v, n, 0)
	return b.String()
}

// Compact は json.dumps(v, ensure_ascii=False)（区切り ", " と ": "）と同じ文字列。
func Compact(v any) string { return Indent(v, -1) }

// Marshal は要求の本文用（Compact と同じ形の UTF-8）。
func Marshal(v any) []byte { return []byte(Compact(v)) }

func write(b *strings.Builder, v any, indent, depth int) {
	nl := func(d int) {
		if indent >= 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*d))
		}
	}
	itemSep := ", "
	if indent >= 0 {
		itemSep = ","
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case Number:
		b.WriteString(FormatNumber(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(FormatFloat(x))
	case string:
		b.WriteString(Quote(x))
	case []string:
		a := make([]any, len(x))
		for i := range x {
			a[i] = x[i]
		}
		write(b, a, indent, depth)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(itemSep)
			}
			nl(depth + 1)
			write(b, e, indent, depth+1)
		}
		nl(depth)
		b.WriteByte(']')
	case *Object:
		if x == nil {
			b.WriteString("null")
			return
		}
		keys := x.Keys()
		if len(keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(itemSep)
			}
			nl(depth + 1)
			b.WriteString(Quote(k))
			b.WriteString(": ")
			val, _ := x.Get(k)
			write(b, val, indent, depth+1)
		}
		nl(depth)
		b.WriteByte('}')
	default:
		// 想定外の型は Go の JSON にしてから読み直す（順は Go の規則）
		raw, err := json.Marshal(x)
		if err != nil {
			b.WriteString("null")
			return
		}
		d, err := Decode(raw)
		if err != nil {
			b.WriteString("null")
			return
		}
		write(b, d, indent, depth)
	}
}

// Quote は JSON の文字列にする（ASCII 以外はそのまま出す）。
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// FormatNumber は数を出すときの形（整数はそのまま、それ以外は FormatFloat の形）。
func FormatNumber(n Number) string {
	s := string(n)
	if !strings.ContainsAny(s, ".eE") {
		if i, ok := new(big.Int).SetString(s, 10); ok {
			return i.String()
		}
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		return s
	}
	return FormatFloat(f)
}

// FormatFloat は小数を出すときの形（1.0 / 1e-05 / 1e+16 のように、読み戻すと元に戻る最短の字面）。
func FormatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // -1.2345e+06
	sign := ""
	if strings.HasPrefix(e, "-") {
		sign, e = "-", e[1:]
	}
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)
	if exp >= -4 && exp < 16 {
		var s string
		if exp >= 0 {
			if len(digits) <= exp+1 {
				s = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
			} else {
				s = digits[:exp+1] + "." + digits[exp+1:]
			}
		} else {
			s = "0." + strings.Repeat("0", -exp-1) + digits
		}
		return sign + s
	}
	m := digits[:1]
	if len(digits) > 1 {
		m += "." + digits[1:]
	}
	es := "+"
	if exp < 0 {
		es, exp = "-", -exp
	}
	return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
}

// Truthy は値の真偽（nil・false・0・空文字・空の配列とオブジェクトが偽）。
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return err != nil || f != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case *Object:
		return x != nil && len(x.Members) > 0
	}
	return true
}

// Str は値を表示に埋め込むときの文字列。オブジェクト・配列は JSON の形で代える。
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case Number:
		return FormatNumber(x)
	}
	return Compact(v)
}

// Int は数（Number・int・float64）を整数にする（小数は 0 の側へ切り捨て）。
func Int(v any) (int64, bool) {
	switch x := v.(type) {
	case Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return i, true
		}
		if f, err := strconv.ParseFloat(string(x), 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return int64(f), true
		}
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		if !math.IsInf(x, 0) && !math.IsNaN(x) {
			return int64(x), true
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

// Float は数（Number・int・float64）を float64 にする。文字列・真偽値は ok = false。
func Float(v any) (float64, bool) {
	switch x := v.(type) {
	case Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}
