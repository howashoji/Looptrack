package usagesnap

// 以前の CLI（1.0.0 より前）と同じ結果にするための部品。ここがずれると会話 ID・区間・丸めが以前の CLI と変わり、
// サーバで二重計上や inconsistent になる。どれも以前の CLI の挙動に合わせ、差分テストで確かめている。

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// ---------------------------------------------------------------- 会話記録の読み方

// readLines はファイルを UTF-8 として読んで行に分ける（以前の CLI（1.0.0 より前）と同じ分け方）。
// 不正な UTF-8 は最大の部分列ごとに U+FFFD 1 つで置き換え、改行は \r\n・\r・\n のどれでも区切る。
// 返す行に改行は含めない（JSON として読んだ結果は変わらない）。
func readLines(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, &os.PathError{Op: "open", Path: path, Err: errIsDir}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return splitUniversal(decodeReplace(b)), nil
}

type isDirError struct{}

func (isDirError) Error() string { return "is a directory" }

var errIsDir error = isDirError{}

// readFirstLine は f.readline()（先頭の 1 行。改行は含めない）。ファイル全体は読まない。
func readFirstLine(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", &os.PathError{Op: "open", Path: path, Err: errIsDir}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var buf []byte
	chunk := make([]byte, 64*1024)
	for {
		n, err := f.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if i := bytes.IndexAny(buf, "\r\n"); i >= 0 {
			return decodeReplace(buf[:i]), nil
		}
		if err != nil {
			return decodeReplace(buf), nil
		}
	}
}

func splitUniversal(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			out = append(out, s[start:i])
			start = i + 1
		case '\r':
			out = append(out, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// decodeReplace は bytes.decode("utf-8", "replace")。
func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			sb.Write(b[i : i+size])
			i += size
			continue
		}
		// 不正な列: 先頭バイトから続く正しい途中までを 1 つの U+FFFD にする（Unicode の maximal subpart）
		n, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			n = 1
		case c == 0xE0:
			n, lo = 2, 0xA0
		case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
			n = 2
		case c == 0xED:
			n, hi = 2, 0x9F
		case c == 0xF0:
			n, lo = 3, 0x90
		case c >= 0xF1 && c <= 0xF3:
			n = 3
		case c == 0xF4:
			n, hi = 3, 0x8F
		}
		j := i + 1
		for k := 0; k < n && j < len(b); k++ {
			x := b[j]
			if k == 0 {
				if x < lo || x > hi {
					break
				}
			} else if x < 0x80 || x > 0xBF {
				break
			}
			j++
		}
		sb.WriteRune(utf8.RuneError)
		i = j
	}
	return sb.String()
}

// decodeLine は 1 行を JSON として読む。値は map[string]any・[]any・json.Number・string・bool・nil
// （重なったキーは後の値＝以前の CLI と同じ）。
func decodeLine(line string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

// ---------------------------------------------------------------- JSON の値（オブジェクト / 配列 / 整数 / 実数 / 文字列）

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// get は v がオブジェクトならその key の値（オブジェクトでなければ nil）。
func get(v any, key string) any {
	if m := asMap(v); m != nil {
		return m[key]
	}
	return nil
}

func has(v any, key string) bool {
	m := asMap(v)
	if m == nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// orMap は v のオブジェクト（オブジェクトでなければ空）。
func orMap(v any) map[string]any {
	if m := asMap(v); m != nil {
		return m
	}
	return map[string]any{}
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// truthy は値の真偽（nil・false・""・0・空のオブジェクト/配列が偽。以前の CLI（1.0.0 より前）と同じ）。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		return numberNonZero(string(x))
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func numberNonZero(s string) bool {
	if isIntLiteral(s) {
		for _, c := range s {
			if c >= '1' && c <= '9' {
				return true
			}
		}
		return false
	}
	f, err := strconv.ParseFloat(s, 64)
	return err != nil || f != 0
}

// isIntLiteral は JSON の数が整数になる字面か（小数点・指数が無い）。
func isIntLiteral(s string) bool { return !strings.ContainsAny(s, ".eE") }

// toNum は整数（真偽値を除く）ならその値、他は 0。
func toNum(v any) int64 {
	n, ok := v.(json.Number)
	if !ok || !isIntLiteral(string(n)) {
		return 0
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil {
		if strings.HasPrefix(string(n), "-") {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	return i
}

// isIntValue は isinstance(v, int)（bool も int）。
func isIntValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return true
	case json.Number:
		return isIntLiteral(string(x))
	}
	return false
}

// toStr は値を文字列にする（以前の CLI（1.0.0 より前）と同じ字面。nil は "None"、真偽は "True" / "False"）。
func toStr(v any) string {
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
	case json.Number:
		return jsonorder.FormatNumber(jsonorder.Number(x))
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// orStr は偽の値なら ""、他は toStr。
func orStr(v any) string {
	if !truthy(v) {
		return ""
	}
	return toStr(v)
}

// hashKey は辞書・集合のキーとしての同一性（型ごとに分ける。以前の CLI（1.0.0 より前）と同じ）。
func hashKey(v any) string {
	switch x := v.(type) {
	case nil:
		return "none"
	case string:
		return "s:" + x
	case json.Number:
		return "n:" + jsonorder.FormatNumber(jsonorder.Number(x))
	case bool:
		if x {
			return "n:1"
		}
		return "n:0"
	}
	b, _ := json.Marshal(v)
	return "j:" + string(b)
}

// ---------------------------------------------------------------- 文字列

// isSpaceRune は str.isspace() の 1 文字（re の \s と同じ）。
func isSpaceRune(r rune) bool {
	switch {
	case r == ' ', r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x1f:
		return true
	case r < 0x80:
		return false
	}
	switch r {
	case 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func trimSpace(s string) string { return strings.TrimFunc(s, isSpaceRune) }

// splitLines は行に分ける（区切りは捨てる。以前の CLI（1.0.0 より前）と同じ区切り）。
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			if r == '\r' && i+1 < len(s) && s[i+1] == '\n' {
				size = 2
			}
			start = i + size
		}
		i += size
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// runeLen は len(str)（コードポイントの数）。
func runeLen(s string) int64 { return int64(utf8.RuneCountInString(s)) }

func runePrefix(s string, n int) string {
	i := 0
	for k := range s {
		if i == n {
			return s[:k]
		}
		i++
	}
	return s
}

// label は _label(text): 最初の空でない行から先頭の記号と空白を除いた先頭 44 文字。
func label(text string) string {
	for _, line := range splitLines(text) {
		s := strings.TrimLeftFunc(trimSpace(line), func(r rune) bool {
			return r == '-' || r == '*' || r == '#' || r == '>' || isSpaceRune(r)
		})
		if s != "" {
			return runePrefix(s, 44)
		}
	}
	return "(no text)"
}

var (
	autoRE = regexp.MustCompile(`利用制限に達し|利用上限に達し|^もう一度試す`)
	// EXCLUDE_RE の本体。以前の CLI（1.0.0 より前）の否定の後読み (?<![「『"“'`]) は RE2 に無いので excluded() で前の 1 文字を見る
	excludeBodyRE = regexp.MustCompile(`^(?:本|この)セッションは[^\n「」『』〜～]{0,15}レポートの?対象外`)
)

// excluded は EXCLUDE_RE.search(text) が当たるか（「」や引用符の直後は指示の引用なので拾わない）。
func excluded(text string) bool {
	for i := 0; i < len(text); i++ {
		if !strings.HasPrefix(text[i:], "本") && !strings.HasPrefix(text[i:], "この") {
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:i])
			if strings.ContainsRune("「『\"“'`", prev) {
				continue
			}
		}
		if excludeBodyRE.MatchString(text[i:]) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 数

// round1 は round(x, 1)（正しく丸めた 10 進・ちょうど半分は偶数へ）。
func round1(x float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 1, 64), 64)
	return f
}

// round0 は round(x)（int。ちょうど半分は偶数へ）。
func round0(x float64) int64 { return int64(math.RoundToEven(x)) }

// median は statistics.median。
func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// ---------------------------------------------------------------- 時刻（精度はマイクロ秒）

// isoTime は ISO 8601 の日時を読む（末尾の "Z" は "+00:00" とみなす）。タイムゾーンの無い形は現地時刻とみなす
// （以前の CLI（1.0.0 より前）がタイムゾーンの無い日時を現地として扱ったときと同じ）。読めなければ ok = false。
func isoTime(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	return fromISOFormat(strings.ReplaceAll(ts, "Z", "+00:00"))
}

// fromISOFormat は以前の CLI（1.0.0 より前）の日時の読み方と同じ規則で読む。
func fromISOFormat(s string) (time.Time, bool) {
	if len(s) < 7 {
		return time.Time{}, false
	}
	// 区切り（11 文字目）が ASCII でなければ C 実装は置き換えて読むが、ここでは読まない（実物に無い形）
	b := []byte(s)
	sep := isoSeparator(b)
	if sep < 0 || sep > len(b) {
		return time.Time{}, false
	}
	year, month, day, ok := isoDate(b[:sep])
	if !ok {
		return time.Time{}, false
	}
	var hh, mm, ss, us, tzSec, tzUS int
	aware := false
	if len(b) > sep {
		var rv int
		hh, mm, ss, us, tzSec, tzUS, rv = isoTimePart(b[sep+1:])
		if rv < 0 {
			return time.Time{}, false
		}
		aware = rv == 1
	}
	if year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 || day > daysIn(year, month) ||
		hh > 23 || mm > 59 || ss > 59 {
		return time.Time{}, false
	}
	if !aware {
		return time.Date(year, time.Month(month), day, hh, mm, ss, us*1000, time.Local), true
	}
	off := time.Duration(tzSec)*time.Second + time.Duration(tzUS)*time.Microsecond
	if off <= -24*time.Hour || off >= 24*time.Hour {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, hh, mm, ss, us*1000, time.UTC).Add(-off), true
}

func daysIn(y, m int) int {
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func at(b []byte, i int) byte {
	if i < len(b) && i >= 0 {
		return b[i]
	}
	return 0
}

// parseDigits は C の parse_digits（n 桁ちょうど。足りなければ失敗）。
func parseDigits(b []byte, p, n int) (int, int, bool) {
	v := 0
	for k := 0; k < n; k++ {
		c := at(b, p+k)
		if !isDigit(c) {
			return 0, p, false
		}
		v = v*10 + int(c-'0')
	}
	return v, p + n, true
}

func isoSeparator(b []byte) int {
	n := len(b)
	if n == 7 {
		return 7
	}
	if b[4] == '-' {
		if b[5] == 'W' {
			if n > 8 && b[8] == '-' {
				if n == 9 {
					return -1
				}
				if n > 10 && isDigit(b[10]) {
					return 8
				}
				return 10
			}
			return 8
		}
		return 10
	}
	if b[4] == 'W' {
		idx := 7
		for idx < n && isDigit(b[idx]) {
			idx++
		}
		if idx < 9 {
			return idx
		}
		if idx%2 == 0 {
			return 7
		}
		return 8
	}
	return 8
}

func isoDate(b []byte) (y, m, d int, ok bool) {
	p := 0
	if y, p, ok = parseDigits(b, p, 4); !ok {
		return
	}
	useSep := at(b, p) == '-'
	if useSep {
		p++
	}
	if at(b, p) == 'W' {
		p++
		var week, wd int
		if week, p, ok = parseDigits(b, p, 2); !ok {
			return
		}
		if p < len(b) {
			if useSep {
				if at(b, p) != '-' {
					return 0, 0, 0, false
				}
				p++
			}
			if wd, _, ok = parseDigits(b, p, 1); !ok {
				return
			}
		} else {
			wd = 1
		}
		return isoWeekToDate(y, week, wd)
	}
	if m, p, ok = parseDigits(b, p, 2); !ok {
		return
	}
	if useSep {
		if at(b, p) != '-' {
			return 0, 0, 0, false
		}
		p++
	}
	if d, _, ok = parseDigits(b, p, 2); !ok {
		return
	}
	return y, m, d, true
}

func isoWeekToDate(year, week, day int) (int, int, int, bool) {
	if year < 1 || year > 9999 || day < 1 || day > 7 {
		return 0, 0, 0, false
	}
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	wd := int(jan4.Weekday()+6) % 7 // 月曜 = 0
	monday1 := jan4.AddDate(0, 0, -wd)
	if week < 1 || week > 53 {
		return 0, 0, 0, false
	}
	if week == 53 {
		first := int(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Weekday()) // 日曜 = 0
		leap := daysIn(year, 2) == 29
		if !(first == 4 || (first == 3 && leap)) {
			return 0, 0, 0, false
		}
	}
	t := monday1.AddDate(0, 0, (week-1)*7+day-1)
	return t.Year(), int(t.Month()), t.Day(), true
}

// hhmmss は C の parse_hh_mm_ss_ff。rv: 0 = 終わりまで読んだ、1 = 残りがある、負 = 失敗。
func hhmmss(b []byte, end int) (hh, mm, ss, us, rv int) {
	vals := [3]int{}
	p := 0
	hasSep := true
	for i := 0; i < 3; i++ {
		v, np, ok := parseDigits(b, p, 2)
		if !ok {
			return 0, 0, 0, 0, -3
		}
		vals[i] = v
		p = np
		c := at(b, p)
		p++
		if i == 0 {
			hasSep = c == ':'
		}
		if p >= end {
			r := 0
			if c != 0 {
				r = 1
			}
			return vals[0], vals[1], vals[2], 0, r
		} else if hasSep && c == ':' {
			continue
		} else if c == '.' || c == ',' {
			break
		} else if !hasSep {
			p--
		} else {
			return 0, 0, 0, 0, -4
		}
	}
	remain := end - p
	toParse := remain
	if remain >= 6 {
		toParse = 6
	}
	v, np, ok := parseDigits(b, p, toParse)
	if !ok || toParse <= 0 { // ここに来るとき残りは 1 文字以上ある
		return 0, 0, 0, 0, -3
	}
	corr := []int{100000, 10000, 1000, 100, 10}
	if toParse < 6 {
		v *= corr[toParse-1]
	}
	p = np
	for isDigit(at(b, p)) {
		p++
	}
	r := 0
	if at(b, p) != 0 {
		r = 1
	}
	return vals[0], vals[1], vals[2], v, r
}

// isoTimePart は C の parse_isoformat_time。rv: 0 = タイムゾーンなし、1 = あり、負 = 失敗。
func isoTimePart(b []byte) (hh, mm, ss, us, tzSec, tzUS, rv int) {
	tz := 0
	for {
		c := at(b, tz)
		if c == 'Z' || c == '+' || c == '-' {
			break
		}
		tz++
		if tz >= len(b) {
			break
		}
	}
	var r int
	hh, mm, ss, us, r = hhmmss(b, tz)
	if r < 0 {
		return 0, 0, 0, 0, 0, 0, r
	}
	if tz >= len(b) {
		if r == 1 {
			return 0, 0, 0, 0, 0, 0, -5
		}
		return hh, mm, ss, us, 0, 0, 0
	}
	if b[tz] == 'Z' {
		if tz+1 < len(b) {
			return 0, 0, 0, 0, 0, 0, -5
		}
		return hh, mm, ss, us, 0, 0, 1
	}
	sign := 1
	if b[tz] == '-' {
		sign = -1
	}
	rest := b[tz+1:]
	th, tm, ts, tus, r2 := hhmmss(rest, len(rest))
	if r2 != 0 {
		return 0, 0, 0, 0, 0, 0, -5
	}
	return hh, mm, ss, us, sign * (th*3600 + tm*60 + ts), sign * tus, 1
}

// utcString は _utc(dt): UTC の "%Y-%m-%dT%H:%M:%S.%fZ"。
func utcString(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// seconds は (b - a).total_seconds()（マイクロ秒単位の差を 1e6 で割る）。
func seconds(a, b time.Time) float64 {
	return float64(b.Sub(a).Microseconds()) / 1e6
}

// truncUS は日時の精度（マイクロ秒）に切り詰める。
func truncUS(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

// fromTimestamp は UNIX 時刻（秒）から UTC の日時を作る（マイクロ秒は半分を偶数へ丸める）。範囲外は ok = false。
func fromTimestamp(x float64) (time.Time, bool) {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return time.Time{}, false
	}
	ip, fp := math.Modf(x)
	fp = math.RoundToEven(fp * 1e6)
	if fp >= 1e6 {
		fp -= 1e6
		ip += 1
	} else if fp < 0 {
		fp += 1e6
		ip -= 1
	}
	// 日時として扱える範囲（1..9999 年）
	if ip < -62135596800 || ip > 253402300799 {
		return time.Time{}, false
	}
	return time.Unix(int64(ip), int64(fp)*1000).UTC(), true
}

// ---------------------------------------------------------------- パス

func pathSeps() string {
	if runtime.GOOS == "windows" {
		return `\/`
	}
	return "/"
}

// baseName はパスの末尾の要素（最後の区切りより後ろ）。
func baseName(p string) string {
	return p[strings.LastIndexAny(p, pathSeps())+1:]
}

// dirName はパスの親ディレクトリ（末尾の区切りを除く。"a.jsonl" は ""）。
func dirName(p string) string {
	head := p[:strings.LastIndexAny(p, pathSeps())+1]
	if head != "" && strings.Trim(head, pathSeps()) != "" {
		head = strings.TrimRight(head, pathSeps())
	}
	return head
}

// joinPath はパスをつなぐ（空の要素は区切りを足さない。絶対パスが来たらそこから）。
func joinPath(parts ...string) string {
	out := ""
	for _, p := range parts {
		switch {
		case filepath.IsAbs(p) || strings.HasPrefix(p, "/"):
			out = p
		case out == "" || strings.HasSuffix(out, "/") || strings.HasSuffix(out, string(filepath.Separator)):
			out += p
		default:
			out += string(filepath.Separator) + p
		}
	}
	return out
}

// realpath は symlink を解いた絶対パス（無いパスでもエラーにせず、たどれるところまで解く）。
func realpath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	dir, base := filepath.Split(abs)
	dir = filepath.Clean(dir)
	if dir == abs || base == "" {
		return abs
	}
	return filepath.Join(realpath(dir), base)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func mtime(p string) float64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return float64(st.ModTime().UnixNano()) / 1e9
}

// globPaths は glob.glob（ワイルドカードの要素は "." で始まる名前に当たらない）。順は不定（呼び出し側が並べる）。
func globPaths(pattern string) []string {
	hits, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	pp := strings.Split(filepath.ToSlash(pattern), "/")
	var out []string
	for _, h := range hits {
		hp := strings.Split(filepath.ToSlash(h), "/")
		if len(hp) == len(pp) {
			hidden := false
			for i := range pp {
				if strings.ContainsAny(pp[i], "*?[") && !strings.HasPrefix(pp[i], ".") && strings.HasPrefix(hp[i], ".") {
					hidden = true
					break
				}
			}
			if hidden {
				continue
			}
		}
		out = append(out, h)
	}
	return out
}

// globRecursiveJSONL は dir の下（サブディレクトリも）の *.jsonl を名前順に返す。
// "." で始まるディレクトリ・ファイルは入れない。ディレクトリへの symlink はたどる（同じ実体は 1 回）。
func globRecursiveJSONL(dir string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(d string)
	walk = func(d string) {
		rp := realpath(d)
		if seen[rp] {
			return
		}
		seen[rp] = true
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := joinPath(d, name)
			if strings.HasSuffix(name, ".jsonl") {
				out = append(out, p)
			}
			if isDir(p) {
				walk(p)
			}
		}
	}
	walk(dir)
	// glob は "dir/**/*.jsonl" を "dir/" + 相対パスで返す
	sort.Strings(out)
	return out
}
