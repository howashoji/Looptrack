package i18n

// OneSuffix は、件数が 1 のときだけ使う単数の文面の ID に付ける接尾辞。
// 英語の "{n} issues" に対する "{n} issue" を、<ID>_one に置く。
const OneSuffix = "_one"

// TN は件数 n で単数と複数を分けて、id の文面を lang で返す。
//
// n が 1 で、lang の対訳表に id + OneSuffix があればそれを使い、それ以外（n が 0・2 以上・
// 単数の文面が無い言語）は id をそのまま使う。単数と複数で文面が変わらない日本語は、
// 両方に同じ文面を置く（表示は変わらない）。単数の文面を日本語から借りることはしない
// （英語の画面に日本語が出るため）。
//
// n は選ぶためだけの値で、文面の中の {名前} は kv で埋める（件数の置き場の名前は文面ごとに違う）。
// Web の画面の側は同じ規則を render.js の countText に持つ。
func TN(lang Lang, id string, n int, kv ...any) string {
	return T(lang, pickCount(lang, id, n), kv...)
}

// pickCount は TN の選び方。件数が 1 で単数の文面があれば id + OneSuffix を、無ければ id を返す。
func pickCount(lang Lang, id string, n int) string {
	if n == 1 && Has(lang, id+OneSuffix) {
		return id + OneSuffix
	}
	return id
}

// TNFunc は HTML テンプレートの関数 TN の実装（{{TN .Lang "id" (len .Items) "slug" .Slug}}）。
// 言語の読み方は TFunc と同じ。件数は整数ならそのまま、読めない値は複数として扱う。
func TNFunc(lang any, id string, n any, kv ...any) string {
	var l Lang
	switch v := lang.(type) {
	case Lang:
		l = v
	case string:
		if p, ok := Parse(v); ok {
			l = p
		}
	}
	count := -1
	switch v := n.(type) {
	case int:
		count = v
	case int32:
		count = int(v)
	case int64:
		count = int(v)
	case uint:
		count = int(v)
	case uint32:
		count = int(v)
	case uint64:
		count = int(v)
	}
	return TN(l, id, count, kv...)
}

// MN は、件数 n で単数と複数を分ける Msg を作る（言語は In で決める）。
// 別の文面の {名前} に埋める「件数つきの語句」（"1 issue"・"2 issues"）を作るときにも使える
// （置き換えの値の Msg は、その言語の文面になる）。
func MN(id string, n int, kv ...any) Msg { return Msg{ID: id, KV: kv, n: n, counted: true} }

// ErrorfN は、件数で単数と複数を分ける利用者向けの理由を ID で持つ error を作る。
func ErrorfN(id string, n int, kv ...any) error { return &Error{Msg: MN(id, n, kv...)} }

// WrapfN は Wrapf の、件数で単数と複数を分ける版。
func WrapfN(err error, id string, n int, kv ...any) error {
	return &wrapped{msg: MN(id, n, append(kv, "reason", err)...), err: err}
}
