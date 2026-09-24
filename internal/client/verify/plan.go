package verify

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// Fetch は検証の節を取る（GET /issues/{id}/verify。path は ?project= まで付けたもの）。
func Fetch(cl *api.Client, path string) (*jsonorder.Object, error) {
	v, err := cl.Get(path)
	if err != nil {
		return nil, err
	}
	if o, ok := v.(*jsonorder.Object); ok {
		return o, nil
	}
	return jsonorder.NewObject(), nil
}

// Commands は計画の commands（文字列だけ）。
func Commands(plan *jsonorder.Object) []string {
	v, _ := plan.Get("commands")
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, jsonorder.Str(x))
	}
	return out
}

// Options は RunAll の設定。
type Options struct {
	Dir          string        // 実行場所（Root）
	Env          []string      // 環境（Env）
	Timeout      time.Duration // 1 コマンドの上限
	TotalTimeout time.Duration // 全体の上限（超えた残りは skipped）
	// Before はコマンドを始める前、After は終わった後に呼ぶ（表示用。nil なら呼ばない）。skipped は After だけ。
	Before func(i, n int, command string)
	After  func(i, n int, r Result)
}

// RunAll は commands を順にすべて実行する（失敗しても続ける）。
func RunAll(commands []string, o Options) []Result {
	n := len(commands)
	deadline := time.Now().Add(o.TotalTimeout)
	out := make([]Result, 0, n)
	for i, command := range commands {
		var r Result
		if remaining := time.Until(deadline); remaining <= 0 {
			r = Skipped(command)
		} else {
			if o.Before != nil {
				o.Before(i+1, n, command)
			}
			r = RunCommand(command, o.Dir, o.Env, min(o.Timeout, remaining))
		}
		out = append(out, r)
		if o.After != nil {
			o.After(i+1, n, r)
		}
	}
	return out
}

// JSON は POST の results の 1 件（キーの順は以前の CLI と同じ）。
func (r Result) JSON() *jsonorder.Object {
	var code any
	if r.ExitCode != nil {
		code = *r.ExitCode
	}
	o := jsonorder.NewObject().Set("command", r.Command).Set("status", r.Status).Set("exit_code", code).
		Set("duration_ms", r.DurationMS).Set("output_tail", r.OutputTail)
	if r.Cached { // 注記は付いたときだけ送る（以前の CLI と同じ並びを崩さない）
		o.Set("cached", true)
	}
	return o
}

// ResultsJSON は results の配列。
func ResultsJSON(rs []Result) []any {
	out := make([]any, len(rs))
	for i, r := range rs {
		out[i] = r.JSON()
	}
	return out
}

// Count は (成功, 失敗) の件数（skipped と timeout は失敗に数える）。
func Count(rs []Result) (passed, failed int) {
	for _, r := range rs {
		if r.Status == StatusOK {
			passed++
		}
	}
	return passed, len(rs) - passed
}

// Body は POST の本文（body_sha256・results・host・workspace）。
func Body(sha string, rs []Result, dir string) *jsonorder.Object {
	host, _ := os.Hostname()
	ws := dir
	if abs, err := filepath.Abs(dir); err == nil {
		ws = abs
	}
	if real, err := filepath.EvalSymlinks(ws); err == nil {
		ws = real
	}
	return jsonorder.NewObject().Set("body_sha256", sha).Set("results", ResultsJSON(rs)).
		Set("host", cut(host, 128)).Set("workspace", cut(filepath.Base(ws), 128))
}

// StripCached は POST の本文から results[].cached の欄を落とす（1 件でも落としたら true）。
//
// cached は後から足した欄で、サーバは知らない欄のある本文を丸ごと 400（invalid_json）で拒む。
// CLI は更新済み・サーバはまだという組み合わせが普通に起きるので、そのままだと
// 「結果キャッシュを見つけた回だけ、検証の記録そのものが残らない」という、元の欠陥より悪い形になる。
// 注記だけを捨てて記録は残し、注記が落ちたことは手元に出す。
func StripCached(body *jsonorder.Object) bool {
	v, _ := body.Get("results")
	list, _ := v.([]any)
	stripped := false
	for _, x := range list {
		if o, ok := x.(*jsonorder.Object); ok && o.Has("cached") {
			o.Delete("cached")
			stripped = true
		}
	}
	return stripped
}

// Send は結果を 1 回で送る（POST /issues/{id}/verify）。
func Send(cl *api.Client, path string, body *jsonorder.Object) (any, error) {
	res, err := cl.Do(api.Request{Method: http.MethodPost, Path: path, Body: body})
	if err != nil {
		return nil, err
	}
	return res.Value, nil
}

// Line は 1 件の結果の行。
func Line(i, n int, r Result) string {
	secs := fmt.Sprintf("%.1f 秒", float64(r.DurationMS)/1000)
	var detail string
	switch r.Status {
	case StatusOK:
		detail = secs
	case StatusFail:
		code := "None"
		if r.ExitCode != nil {
			code = strconv.Itoa(*r.ExitCode)
		}
		detail = "exit " + code + "・" + secs
	case StatusTimeout:
		detail = "時間切れ・" + secs
	case StatusSkipped:
		detail = "全体の上限を超えたため実行しない"
	}
	return fmt.Sprintf("[%d/%d] %-7s %s（%s）", i, n, r.Status, r.Command, detail)
}

// ResultFromJSON はサーバの記録（last.results の 1 件）を Result にする。
func ResultFromJSON(v any) Result {
	o, _ := v.(*jsonorder.Object)
	if o == nil {
		o = jsonorder.NewObject()
	}
	r := Result{Command: o.String("command"), Status: o.String("status"), OutputTail: o.String("output_tail")}
	if x, ok := o.Get("cached"); ok {
		r.Cached = jsonorder.Truthy(x)
	}
	if x, ok := o.Get("exit_code"); ok && x != nil {
		if c, ok := jsonorder.Int(x); ok {
			ci := int(c)
			r.ExitCode = &ci
		}
	}
	if x, ok := o.Get("duration_ms"); ok {
		if f, ok := jsonorder.Float(x); ok {
			r.DurationMS = int64(f)
		}
	}
	return r
}

// IndentTail は出力の末尾 lines 行を「      | 」を付けて並べる（空なら ""）。
func IndentTail(text string, lines int) string {
	if strings.TrimFunc(text, unicode.IsSpace) == "" {
		return ""
	}
	rows := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	for i, row := range rows {
		rows[i] = "      | " + row
	}
	return strings.Join(rows, "\n")
}

// cut は先頭 n 文字（短ければそのまま）。
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
