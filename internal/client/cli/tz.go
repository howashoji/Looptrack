package cli

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // 応答の timezone（IANA 名）を、tzdata の無い環境でも読めるようにする

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// defaultServerTZ は、サーバが応答に timezone を載せる前に時刻を描いていた時間帯。
// この名前のときは、以前と同じ「日本時間」の呼び名を出す（文面を変えない）。
const defaultServerTZ = "Asia/Tokyo"

// defaultTZ は、応答に timezone が無いとき（timezone を載せる前の古いサーバ・応答を持たない手元のファイル）に
// 時刻を描く時間帯。**端末のローカル時間帯には倒さない**: 倒すと、サーバを更新していない利用者の表示が黙って変わる
// （新しい CLI + 古いサーバの組み合わせで、日本の利用者の時刻が変わる退行を出さないための決定）。
// tzdata を埋め込んでいるので、どの環境でも Asia/Tokyo を読める（読めなければ UTC+9 の固定に倒す）。
var defaultTZ = loadDefaultTZ()

func loadDefaultTZ() *time.Location {
	if loc, err := time.LoadLocation(defaultServerTZ); err == nil {
		return loc
	}
	return time.FixedZone("JST", 9*3600)
}

// serverTZ は、応答の timezone（サーバのローカル時刻の IANA 名）から、時刻を描く時間帯と
// 注記に出すその呼び名を決める。無い・読めない名前は日本時間（timezone を載せる前のサーバと同じ）。
func serverTZ(lang i18n.Lang, res *jsonorder.Object) (*time.Location, string) {
	if name := strings.TrimSpace(orStr(res, "timezone", "")); name != "" && name != defaultServerTZ {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, name
		}
	}
	return defaultTZ, i18n.T(lang, "cli.usage.ledger.tz_jst")
}

// localTimeIn は API の RFC 3339（UTC）を loc の「YYYY-MM-DD HH:MM」にする（読めなければそのまま）。
// 時間帯は必ず引数で受け取る（サーバと同じ時刻を描くため。既定へ倒すかどうかは呼ぶ側が serverTZ で決める）。
// タイムゾーンの無い値は手元のタイムゾーンとみなす。
func localTimeIn(value string, loc *time.Location) string {
	if value == "" {
		return ""
	}
	s := strings.Replace(value, "Z", "+00:00", 1)
	for _, layout := range []string{"2006-01-02T15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02T15:04-07:00", "2006-01-02 15:04-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.In(loc).Format("2006-01-02 15:04")
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.In(loc).Format("2006-01-02 15:04")
		}
	}
	return value
}

// posixTZ は TZ の POSIX 形式（例 JST-9・<+09>-9・EST5）の標準時の部分。夏時間の部分は読まない。
var posixTZ = regexp.MustCompile(`^([A-Za-z]{3,}|<[^>]+>)([+-]?)(\d{1,2})(?::(\d{2}))?`)

// fixLocalZone は、TZ が POSIX 形式（JST-9 など）で Go が読めない（UTC になる）とき、標準時の部分から手元のタイムゾーンを作る。
// 以前の CLI（libc の tzset で TZ を読んでいた）と同じ手元の時刻を出すため
// （タイムゾーンを持たない文字列の時刻を読むときの既定。localTimeIn の ParseInLocation）。
func fixLocalZone(e env.Env) {
	tz := e.Get("TZ")
	if tz == "" || tz[0] == ':' {
		return
	}
	if _, err := time.LoadLocation(tz); err == nil {
		return
	}
	m := posixTZ.FindStringSubmatch(tz)
	if m == nil {
		return
	}
	h, _ := strconv.Atoi(m[3])
	mins := 0
	if m[4] != "" {
		mins, _ = strconv.Atoi(m[4])
	}
	off := h*3600 + mins*60
	if m[2] != "-" {
		off = -off // POSIX は UTC から見た向き（JST-9 は UTC+9）
	}
	name := m[1]
	if name[0] == '<' {
		name = name[1 : len(name)-1]
	}
	time.Local = time.FixedZone(name, off)
}
