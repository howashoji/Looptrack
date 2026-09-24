package cli

// 読み取り系のサブコマンドが共通に使う、応答の値の取り出し・桁区切り・クエリの組み立て・選択肢の検査。

import (
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// get はキーの値（キーが無ければ def。値が null なら nil）。
func get(o *jsonorder.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

// getStr はキーの値を表示用の文字列にする（キーが無ければ def）。
func getStr(o *jsonorder.Object, key, def string) string { return jsonorder.Str(get(o, key, def)) }

// orStr はキーの値を表示用の文字列にする（キーが無い・値が偽なら def）。
func orStr(o *jsonorder.Object, key, def string) string {
	v, _ := o.Get(key)
	if !jsonorder.Truthy(v) {
		return def
	}
	return jsonorder.Str(v)
}

// truthy はキーの値が真とみなせるか。
func truthy(o *jsonorder.Object, key string) bool {
	v, _ := o.Get(key)
	return jsonorder.Truthy(v)
}

// must はキーの値。無ければ「応答に要るキーが無い」誤り
// （以前の CLI はここで異常終了し、終了コード 1 になった）。
func must(o *jsonorder.Object, key string) (any, error) {
	v, ok := o.Get(key)
	if !ok {
		return nil, i18n.Errorf("cli.err.missing_key", "key", key)
	}
	return v, nil
}

// asObject は応答の値をオブジェクトとして見る（オブジェクトでなければ空）。
func asObject(v any) *jsonorder.Object {
	if o, ok := v.(*jsonorder.Object); ok && o != nil {
		return o
	}
	return jsonorder.NewObject()
}

// objects は配列の要素をオブジェクトとして並べる（配列でなければ空）。
func objects(v any) []*jsonorder.Object {
	l, _ := v.([]any)
	out := make([]*jsonorder.Object, 0, len(l))
	for _, x := range l {
		out = append(out, asObject(x))
	}
	return out
}

// mustObjects はキーの値（オブジェクトの配列）。無ければ must と同じ誤り。
func mustObjects(o *jsonorder.Object, key string) ([]*jsonorder.Object, error) {
	v, err := must(o, key)
	if err != nil {
		return nil, err
	}
	return objects(v), nil
}

// strList は文字列の配列（キーが無い・配列でなければ空）。
func strList(o *jsonorder.Object, key string) []string {
	v, _ := o.Get(key)
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		out = append(out, jsonorder.Str(x))
	}
	return out
}

// intOf は数を整数にする（数でなければ 0）。
func intOf(v any) int64 {
	i, _ := jsonorder.Int(v)
	return i
}

// comma は 3 桁ごとにカンマを入れる（小数・指数は表示の形のまま、整数部だけを区切る）。
func comma(v any) string {
	s := jsonorder.Str(v)
	if n, ok := v.(jsonorder.Number); ok {
		s = jsonorder.FormatNumber(n)
	} else if i, ok := v.(int64); ok {
		s = strconv.FormatInt(i, 10)
	}
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	head, tail := s, ""
	if i := strings.IndexAny(s, ".eE"); i >= 0 {
		head, tail = s[:i], s[i:]
	}
	if _, ok := new(big.Int).SetString(head, 10); !ok {
		return sign + s
	}
	var b strings.Builder
	for i, ch := range head {
		if i > 0 && (len(head)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return sign + b.String() + tail
}

// validate は値が選択肢に無ければ「エラー: …」で止める。
func validate(field, value string, allowed []string) error {
	for _, a := range allowed {
		if a == value {
			return nil
		}
	}
	return i18n.Errorf("cli.err.choice", "field", field, "allowed", strings.Join(allowed, " / "), "value", value)
}

// query はクエリ文字列（並べた順を保つ。キーと値は %XX に逃がす）。
type query []struct{ k, v string }

func (q *query) add(k, v string) { *q = append(*q, struct{ k, v string }{k, v}) }

// addIf は v が空でなければ足す。
func (q *query) addIf(k, v string) {
	if v != "" {
		q.add(k, v)
	}
}

func (q query) encode() string {
	parts := make([]string, len(q))
	for i, p := range q {
		parts[i] = url.QueryEscape(p.k) + "=" + url.QueryEscape(p.v)
	}
	return strings.Join(parts, "&")
}

// listQuery は一覧のクエリ（sort・reverse と、空でない絞り込み）。
func listQuery(v *Values, extra ...[2]string) string {
	q := query{}
	q.add("sort", v.Str("sort"))
	if v.Bool("reverse") {
		q.add("reverse", "1")
	}
	for _, e := range extra {
		q.addIf(e[0], e[1])
	}
	return q.encode()
}

// flag1 は真なら "1"、偽なら ""。
func flag1(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// issuePath は /issues/<id><suffix>?<params>&project=<slug>。
func (c *Ctx) issuePath(id, suffix string, params ...[2]string) (string, error) {
	slug, err := c.Project()
	if err != nil {
		return "", err
	}
	q := query{}
	for _, p := range params {
		q.add(p[0], p[1])
	}
	q.add("project", slug)
	return "/issues/" + api.PathEscape(id) + suffix + "?" + q.encode(), nil
}

// getObject は GET して応答をオブジェクトとして返す。
func (c *Ctx) getObject(cl *api.Client, path string) (*jsonorder.Object, error) {
	v, err := cl.Get(path)
	if err != nil {
		return nil, err
	}
	return asObject(v), nil
}

// plainAPIError は API の誤りをサーバのメッセージだけにする（401 の案内・5xx の前置きを付けない）。
func plainAPIError(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) {
		return &Fail{ae.Message}
	}
	return err
}
