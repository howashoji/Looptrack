package report

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 入力（集計 JSON・本文 JSON）の読み取り。以前の実装はキーが無い・null を 0 や "" として扱ったので、ここでも同じにする。
// ここでも同じにするため、型を決めた構造体ではなく JSON の値（map・[]any・json.Number）のまま扱う。

// 入力の誤りは i18n.Errorf（*i18n.Error）で返す。文面は直し方を含め、表示する側が i18n.Text で言語を決める。

// Decode は JSON を数値の表記を保ったまま読む（json.Number）。
func Decode(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, i18n.Errorf("report.input.err.trailing_data")
	}
	return v, nil
}

type obj = map[string]any

// get は dict.get（オブジェクトでなければ nil）。
func get(v any, key string) any {
	if m, ok := v.(obj); ok {
		return m[key]
	}
	return nil
}

func has(v any, key string) bool {
	m, ok := v.(obj)
	if !ok {
		return false
	}
	_, ok = m[key]
	return ok
}

// truthy は値が真とみなせるか（null・0・""・空の配列やオブジェクトは偽）。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case []any:
		return len(x) > 0
	case obj:
		return len(x) > 0
	}
	return true
}

// or は a が真なら a、そうでなければ b。
func or(a, b any) any {
	if truthy(a) {
		return a
	}
	return b
}

// toInt は数と整数の文字列を整数にする（無い・偽なら 0。小数は 0 に向けて切り捨て）。
func toInt(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return int64(math.Trunc(f))
		}
	case bool:
		if x {
			return 1
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return i
		}
	}
	return 0
}

// isInt は JSON の整数か（true / false は除く）。
func isInt(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(n.String(), ".eE") {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}

// str は値を表示に埋め込むときの文字列にする。
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if s, ok := e.(string); ok {
				parts[i] = "'" + s + "'"
			} else {
				parts[i] = str(e)
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// sget は文字列の値（無い・null は ""。文字列以外は str）。
func sget(v any, key string) string {
	x := get(v, key)
	if x == nil {
		return ""
	}
	return str(x)
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// strList は文字列の配列か。
func strList(v any) ([]string, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(l))
	for i, e := range l {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}
