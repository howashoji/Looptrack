// Package i18n は利用者に見える文面を日本語と英語で出し分ける。
//
// 文面は ID で参照し、対訳は ja.json と en.json に置いて実行ファイルに埋め込む。
// 日本語が正本で、英語はそこに追いつく形（英語が無い ID は日本語のまま出る）。抜けは
// テストが見つけるので、埋まっていない間も動く。
//
// 言語は呼び出しの経路ごとに決めてから渡す（CLI は環境変数、HTTP は Accept-Language）。
// グローバル変数に現在の言語を置かない。サーバは同時に別々の利用者を相手にしており、
// 要求ごとに言語が違うため。
//
// 対象は「人の目に触れる文面」と、AI が読む案内（MCP の instructions・guide の本文）。
// 日本語のままにするのは、MCP の Tool.Description と入力項目の jsonschema タグだけ（DESIGN.md §9-6 の表）。
//
// 長い本文（kit の rules と skill・guide の共通規則）は対訳表ではなくファイルで持つ。日本語を今の場所
// （kit/loop/rules/x.md・internal/guide/common.md）に、英語を同じディレクトリの en/（kit/loop/rules/en/x.md・
// internal/guide/en/common.md）に置く（kit/README.ja.md「本文の言語」）。どちらを読むかは実行時に決め
// （kit は hook が FromEnv、guide はサーバが要求の言語）、訳が無ければ日本語に戻る。
package i18n

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Lang は出し分ける言語。v1.0.0 は日本語と英語の 2 つだけ。
type Lang string

const (
	// JA は日本語。対訳表の正本。
	JA Lang = "ja"
	// EN は英語。設定が無い利用者に出る既定。
	EN Lang = "en"
)

//go:embed ja.json
var jaJSON []byte

//go:embed en.json
var enJSON []byte

// catalogs は言語ごとの対訳表（ID → 文面）。init で一度だけ組む。
var catalogs map[Lang]map[string]string

func init() {
	catalogs = map[Lang]map[string]string{}
	for l, b := range map[Lang][]byte{JA: jaJSON, EN: enJSON} {
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			// 埋め込んだ対訳表が壊れているのは組み立ての誤りなので、起動時に落とす
			panic(fmt.Sprintf("i18n: %s.json を読めません: %v", l, err))
		}
		catalogs[l] = m
	}
}

// T は id の文面を lang で返す。
//
// kv は文面の中の {名前} を置き換える組（名前, 値, 名前, 値…）。語順は言語で変わるので、
// 位置で埋める %s ではなく名前で埋める。
//
// lang に文面が無ければ日本語に戻し、日本語にも無ければ id をそのまま返す（動いている
// 最中に落とさない）。ID の抜けは TestCatalogHasEveryUsedID が組み立ての時点で見つける。
func T(lang Lang, id string, kv ...any) string {
	s, ok := catalogs[lang][id]
	if !ok {
		if s, ok = catalogs[JA][id]; !ok {
			return id
		}
	}
	return expand(lang, s, kv)
}

// TFunc は HTML テンプレートの関数 T の実装（{{T .Lang "id"}}）。
//
// 言語を any で受けるのは、テンプレートのデータが map で、まだ .Lang を詰めていない画面では
// 何も渡らない（nil になる）ため。読めない値・空のときは T の既定に従い、対訳表の正本（日本語）で出す。
// テンプレートの中で落とさない（ID の抜けは TestCatalogHasEveryUsedID が組み立ての時点で見つける）。
func TFunc(lang any, id string, kv ...any) string {
	var l Lang
	switch v := lang.(type) {
	case Lang:
		l = v
	case string:
		if p, ok := Parse(v); ok {
			l = p
		}
	}
	return T(l, id, kv...)
}

// Has は id が lang の対訳表にあるかを返す（訳の埋まり具合を調べるテスト用）。
func Has(lang Lang, id string) bool {
	_, ok := catalogs[lang][id]
	return ok
}

// IDs は lang の対訳表にあるすべての ID を返す（テスト用。並び順は決まっていない）。
func IDs(lang Lang) []string {
	out := make([]string, 0, len(catalogs[lang]))
	for id := range catalogs[lang] {
		out = append(out, id)
	}
	return out
}

// expand は文面の {名前} を値で置き換える。
//
// lang を持ち回るのは、値が error や Msg のときに「その言語の文面」へ直すため
// （error の Error() は ID を返す設計なので、言語を知らずに文字列にすると {reason} に
// ID が出てしまう）。
func expand(lang Lang, s string, kv []any) string {
	if len(kv) == 0 {
		return s
	}
	rep := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		rep = append(rep, "{"+asString(lang, kv[i])+"}", asString(lang, kv[i+1]))
	}
	return strings.NewReplacer(rep...).Replace(s)
}

// asString は置き換えの値を lang の文字列にする。
//
// error と Msg は lang の文面にする（Text → In → T → expand と再帰するが、包みの深さは
// 有限なので止まる）。それ以外は、これまでどおりそのままの文字列にする。
func asString(lang Lang, v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case Msg:
		return x.In(lang)
	case error:
		return Text(lang, x)
	default:
		return fmt.Sprint(v)
	}
}

// Parse は言語の指定を Lang にする。日本語を指す形（ja・ja-JP・ja_JP.UTF-8）だけを JA と
// 見なし、それ以外は EN にする（v1.0.0 は 2 言語なので、日本語でなければ英語）。
// ok は日本語とも英語とも読めたかを返す（読めない値を無視して次の手がかりへ進むため）。
func Parse(s string) (lang Lang, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return EN, false
	}
	// ja_JP.UTF-8 や ja-JP から言語の部分だけを取る
	if i := strings.IndexAny(s, "_.-@"); i > 0 {
		s = s[:i]
	}
	switch s {
	case "ja", "jpn":
		return JA, true
	case "en", "eng", "c", "posix":
		return EN, true
	}
	return EN, false
}

// FromEnv は CLI・hook の言語を環境変数から決める。
//
// 優先順は LOOPTRACK_LANG（この製品の指定）→ LC_ALL → LC_MESSAGES → LANG（POSIX の慣習）。
// どれも日本語とも英語とも読めなければ EN。
func FromEnv(getenv func(string) string) Lang {
	for _, k := range []string{"LOOPTRACK_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if lang, ok := Parse(getenv(k)); ok {
			return lang
		}
	}
	return EN
}

// FromAcceptLanguage は HTTP・MCP の言語を Accept-Language から決める。
//
// 日本語と英語の品質値（q。既定 1.0）を比べ、日本語のほうが高ければ JA。同じなら先に
// 現れたほうを採る。どちらも無ければ EN。
func FromAcceptLanguage(header string) Lang {
	best := map[Lang]float64{}
	order := map[Lang]int{}
	for i, part := range strings.Split(header, ",") {
		tag, q := part, 1.0
		if k := strings.Index(part, ";"); k >= 0 {
			tag = part[:k]
			for _, p := range strings.Split(part[k+1:], ";") {
				if name, v, found := strings.Cut(p, "="); found && strings.TrimSpace(name) == "q" {
					if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
						q = f
					}
				}
			}
		}
		lang, ok := Parse(tag)
		if !ok {
			continue
		}
		if cur, seen := best[lang]; !seen || q > cur {
			best[lang] = q
			if !seen {
				order[lang] = i
			}
		}
	}
	ja, hasJA := best[JA]
	en, hasEN := best[EN]
	switch {
	case hasJA && hasEN:
		if ja > en || (ja == en && order[JA] < order[EN]) {
			return JA
		}
		return EN
	case hasJA:
		return JA
	default:
		return EN
	}
}

// Msg は「まだ言語を決めていない文面」。ID と置き換えの組だけを持つ。
//
// 文面を作る場所と、それを見せる場所が離れているときに使う。たとえば入力を検査する関数は
// 誤りの理由を返すが、その時点では相手の言語を知らない（知る必要もない）。Msg で返し、
// 画面に出す側が In で言語を決める。
type Msg struct {
	ID string
	KV []any
}

// M は Msg を作る。
func M(id string, kv ...any) Msg { return Msg{ID: id, KV: kv} }

// In は Msg を lang の文面にする。
func (m Msg) In(lang Lang) string { return T(lang, m.ID, m.KV...) }

// Error は Msg を持つ error。利用者に見せる理由を、言語を決めずに返すために使う。
type Error struct{ Msg Msg }

// Errorf は利用者に見せる理由を ID で持つ error を作る。
func Errorf(id string, kv ...any) error { return &Error{Msg: M(id, kv...)} }

// Error は ID を返す（ログと %v 用。利用者に見せる文面は Text で作る）。
func (e *Error) Error() string { return e.Msg.ID }

// Text は err を lang の文面にする。i18n.Error でなければ、そのままの文言を返す
// （内部の失敗や OS からのエラーは訳さずに出す）。
func Text(lang Lang, err error) string {
	if err == nil {
		return ""
	}
	var h msgHolder
	if errors.As(err, &h) {
		return h.i18nMsg().In(lang)
	}
	return err.Error()
}

// Wrapf は、もとの error を包んだまま、利用者に見せる理由を ID で足す。
//
// errors.Is / errors.As は包んだ先まで届くので、os.ErrNotExist などの判定は壊れない。
// Text は ID の文面に {reason} として、もとの error の文言を埋める。もとの error が
// i18n の error なら、その ID ではなく**同じ言語の文面**が入る。
func Wrapf(err error, id string, kv ...any) error {
	return &wrapped{msg: M(id, append(kv, "reason", err)...), err: err}
}

type wrapped struct {
	msg Msg
	err error
}

func (w *wrapped) Error() string { return w.msg.ID }
func (w *wrapped) Unwrap() error { return w.err }
func (w *wrapped) i18nMsg() Msg  { return w.msg }
func (e *Error) i18nMsg() Msg    { return e.Msg }

// msgHolder は ID を持つ error（Error と wrapped）。Text がこれを探す。
type msgHolder interface{ i18nMsg() Msg }
