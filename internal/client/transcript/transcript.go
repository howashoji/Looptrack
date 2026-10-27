// Package transcript は、AI の会話記録（Claude Code の JSONL と付属のファイル・Codex の rollout）を
// サーバへ送る前に変換し、取り込む端末で元に戻す（DESIGN.md §9-8「送る前の変換」「パスの書き換え」）。
//
// 送る前の変換（Convert）の順:
//  1. 行を読む（jsonl は 1 行ずつ、json はファイル全体、text はそのまま）。最後の改行より後ろは返さない。
//  2. JSON として読む（jsonorder.Decode）。読めない行が 1 つでもあれば、そのファイルは返さない。
//     キー（オブジェクトの名前）に domain.FindSecret が当たる行があっても返さない（キーは秘密をマスクしない）。
//  3. パスを書き換える（欄は fields.go。境界の規則は domain.ReplacePathPrefixes）。キーは、パスをキーにした
//     オブジェクト（Codex の changes）だけ頭を書き換える。
//  4. 文字列の値ごとに domain.MaskSecretsCount をかけ、当たった数を規則の語ごとに数える。
//  5. 書き換えもマスクも当たらなかった行は元のバイトのまま。変わった行は、変わった文字列の字面だけを書き直す
//     （jsonorder.Marshal で行ごと書き直すと区切りと数の字面が変わり、戻しても元のバイトに戻らないため。jsontok.go）。
//  6. 全行をもう一度 jsonorder.Decode で読み直す。読めない行があれば返さない。
//  7. 行の境目でチャンクに分ける（上限以内。1 行が上限を超えれば返さない）。
//
// 送れない理由は件数だけで返し、行の中身は返さない（RejectError）。
package transcript

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Agent は会話記録を書いた AI。
type Agent string

const (
	AgentClaude Agent = "claude-code"
	AgentCodex  Agent = "codex"
)

// Kind はファイルの読み方。
type Kind string

const (
	KindJSONL Kind = "jsonl" // 1 行 1 つの JSON（本体・サブエージェント・rollout）
	KindJSON  Kind = "json"  // ファイル全体で 1 つの JSON（*.meta.json など）
	KindText  Kind = "text"  // JSON でないもの（tool-results）。マスクだけをかける
)

// DefaultMaxChunkBytes はチャンクの大きさの既定の上限（transcript_max_chunk_bytes の既定と同じ 32MiB）。
const DefaultMaxChunkBytes = 32 << 20

// nonceAttempts は nonce を作り直す回数の上限。
const nonceAttempts = 8

// Hints は送る端末の値（書き換える元）。空の値は使わない。
type Hints struct {
	// Roots は git の作業ツリーのルートの形（literal・realpath・macOS の /private の形）。並びの順に ROOT0・ROOT1… になる。
	Roots []string
	// SessionDir は記録のディレクトリ（<設定の置き場>/projects/<slug>。Codex は空）。SDIR になる。
	SessionDir string
	// VendorDir は AI の設定の置き場（CLAUDE_CONFIG_DIR か ~/.claude・CODEX_HOME か ~/.codex）。VDIR になる。
	VendorDir string
	// Home はホームディレクトリ。HOME になる。
	Home string
}

// Options は Convert の指定。
type Options struct {
	Agent Agent
	Kind  Kind
	Hints Hints
	// MaxChunkBytes はチャンク 1 つの上限（0 なら DefaultMaxChunkBytes）。
	MaxChunkBytes int
	// BaseOffset は data の先頭の、元のファイルの中の位置（前回の続きから送るとき）。
	BaseOffset int64
	// Rand は nonce の乱数（nil なら crypto/rand）。テストが差し替える。
	Rand io.Reader
}

// Stats は変換で数えた件数。値（パス・秘密）は持たない。
type Stats struct {
	Lines        int `json:"lines"`
	ChangedLines int `json:"changed_lines"`
	// Rewritten は書き換えたパスの数（プレースホルダにした数）。
	Rewritten int `json:"rewritten"`
	// Masked はマスクした数（規則の語ごと）。MaskedLines はマスクに当たった行の数。
	Masked      map[string]int `json:"masked,omitempty"`
	MaskedLines int            `json:"masked_lines"`
	// 書き換えなかった絶対パス（送る端末のルート・記録の置き場・設定の置き場・ホームに当たるもの）。
	// Structural は値の頭がパスの欄（散文でない欄。新しい欄が増えた兆し）、Prose は値の中に混ざったもの、Keys はキーの中のもの。
	UnrewrittenStructural int            `json:"unrewritten_structural"`
	UnrewrittenProse      int            `json:"unrewritten_prose"`
	UnrewrittenKeys       int            `json:"unrewritten_keys"`
	UnrewrittenFields     map[string]int `json:"unrewritten_fields,omitempty"`
}

func (s *Stats) add(o Stats) {
	s.Lines += o.Lines
	s.ChangedLines += o.ChangedLines
	s.Rewritten += o.Rewritten
	s.MaskedLines += o.MaskedLines
	s.UnrewrittenStructural += o.UnrewrittenStructural
	s.UnrewrittenProse += o.UnrewrittenProse
	s.UnrewrittenKeys += o.UnrewrittenKeys
	for k, n := range o.Masked {
		if s.Masked == nil {
			s.Masked = map[string]int{}
		}
		s.Masked[k] += n
	}
	for k, n := range o.UnrewrittenFields {
		if s.UnrewrittenFields == nil {
			s.UnrewrittenFields = map[string]int{}
		}
		s.UnrewrittenFields[k] += n
	}
}

// Chunk は送るチャンク 1 つ。
type Chunk struct {
	// Offset・End は元のファイルの中の位置（Offset が src_offset、End が次のチャンクの Offset）。
	Offset, End int64
	// Data は変換した中身（行の境目で終わる）。
	Data []byte
	// Nonce はプレースホルダの印（text は空）。Placeholders は Data に現れる種類（ROOT0・SDIR など）。
	Nonce        string
	Placeholders []string
	Stats        Stats
}

// Result は Convert の結果。
type Result struct {
	Chunks []Chunk
	Stats  Stats
	// Pending は最後の改行より後ろのバイト数（書きかけの行。次の回に送る）。
	Pending int
}

// RejectError は変換が送るのを止めた理由。理由は件数だけで、行の中身は持たない。
// 同じ記録は何度試しても同じ結果になるので、呼ぶ側は退避（spool）せずに失敗として残す。
type RejectError struct{ err error }

func (e *RejectError) Error() string { return e.err.Error() }
func (e *RejectError) Unwrap() error { return e.err }

func reject(err error) error { return &RejectError{err: err} }

// IsReject は err が変換の止め（RejectError）か。
func IsReject(err error) bool {
	var r *RejectError
	return errors.As(err, &r)
}

// prefix は書き換えの手がかり 1 つ（種類と値）。
type prefix struct {
	kind, value string
}

// prefixes は手がかりを長い値から順に並べる。同じ値は最初の種類だけ。区切り（/ \）を含まない値（/ や C:）は使わない。
// 値に \ か " があれば、JSON の文字列の中に JSON を書いた欄（Codex の function_call.arguments）のための逃がした形を
// 種類の後ろに J を付けて足す（戻すときも逃がした形に戻す）。
func (h Hints) prefixes() []prefix {
	var out []prefix
	seen := map[string]bool{}
	addOne := func(kind, v string) {
		v = strings.TrimRight(v, `/\`)
		if v == "" || !strings.ContainsAny(v, `/\`) || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, prefix{kind, v})
		if j := jsonInner(v); j != v && !seen[j] {
			seen[j] = true
			out = append(out, prefix{kind + "J", j})
		}
	}
	n := 0
	for _, r := range h.Roots {
		before := len(out)
		addOne("ROOT"+strconv.Itoa(n), r)
		if len(out) > before {
			n++
		}
	}
	addOne("SDIR", h.SessionDir)
	addOne("VDIR", h.VendorDir)
	addOne("HOME", h.Home)
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].value) > len(out[j].value) })
	return out
}

func placeholderHead(nonce string) string { return "{{LT:" + nonce + ":" }

func placeholder(nonce, kind string) string { return placeholderHead(nonce) + kind + "}}" }

func pathPrefixes(ps []prefix, nonce string) []domain.PathPrefix {
	out := make([]domain.PathPrefix, len(ps))
	for i, p := range ps {
		out[i] = domain.PathPrefix{Path: p.value, Repl: placeholder(nonce, p.kind)}
	}
	return out
}

// countMarker は書き換えなかったパスを数えるときの置き換え先（数えるだけで、結果は使わない）。
const countMarker = "\x00"

func countPaths(s string, ps []domain.PathPrefix) int {
	return strings.Count(domain.ReplacePathPrefixes(s, ps), countMarker) - strings.Count(s, countMarker)
}

// replaceHead は値の頭が手がかりに当たるときだけ、頭を置き換える（境界の規則は ReplacePathPrefixes のもの）。
func replaceHead(s string, ps []domain.PathPrefix) (string, bool) {
	for _, p := range ps {
		if !strings.HasPrefix(s, p.Path) {
			continue
		}
		if strings.HasPrefix(domain.ReplacePathPrefixes(s, []domain.PathPrefix{p}), p.Repl) {
			return p.Repl + s[len(p.Path):], true
		}
	}
	return s, false
}

// replaceCommandHead はコマンド行の先頭の語（最初の空白まで）が手がかりに当たるときだけ、その語の頭を置き換える。
// 引数の中のパスは置き換えない（引数が散文かパスかは語の形からは分からないため）。
func replaceCommandHead(s string, ps []domain.PathPrefix) (string, bool) {
	end := strings.IndexAny(s, " \t\r\n")
	if end < 0 {
		end = len(s)
	}
	w, ok := replaceHead(s[:end], ps)
	if !ok {
		return s, false
	}
	return w + s[end:], true
}

// headRepl は値の頭が当たる手がかりの置き換え先（当たらなければ空）。
func headRepl(s string, ps []domain.PathPrefix) string {
	for _, p := range ps {
		if strings.HasPrefix(s, p.Path) && strings.HasPrefix(domain.ReplacePathPrefixes(s, []domain.PathPrefix{p}), p.Repl) {
			return p.Repl
		}
	}
	return ""
}

// keyRewritable は、キーの頭を書き換えるオブジェクト（keyRule に当たるもの）ごとに、書き換えてよいかを返す
// （オブジェクトの値の字面の先頭の位置で引く）。書き換えてよいのは、キーがすべて同じ 1 つの種類に当たるときだけ。
// 種類が混ざると、取り込む端末で別の種類が同じ値に戻り（ROOT0 と ROOT1 はどちらもルートに戻る。
// ルートがホームの下にあれば HOME と ROOT0 も重なりうる）、同じキーが 2 つできて値が黙って失われる。
// 書き換えないキーが混ざるときも、戻したキーがそれと重なりうるので書き換えない。どちらもキーは元のまま残し、数える。
func keyRewritable(doc []byte, rule func([]seg) bool, ps []domain.PathPrefix) map[int]bool {
	kinds := map[int]string{}
	mixed := map[int]bool{}
	_ = scanJSON(doc, func(path []seg, key bool, s, e int) {
		if !key || !rule(path) {
			return
		}
		obj := path[len(path)-1].at
		k, _, _ := decodeString(doc[s:e])
		r := headRepl(k, ps)
		if prev, ok := kinds[obj]; ok && prev != r {
			mixed[obj] = true
		}
		kinds[obj] = r
	})
	out := map[int]bool{}
	for obj, r := range kinds {
		out[obj] = r != "" && !mixed[obj]
	}
	return out
}

// breakAfter はテストだけが差す（3〜5 の後の行を壊し、6 の読み直しが止めることを確かめる）。
var breakAfter func([]byte) []byte

// proseKeys は本文の欄の名前（値がパスで始まっても構造の欄としては数えない。tool_result の content・コマンドの出力など）。
var proseKeys = map[string]bool{"text": true, "content": true, "stdout": true, "stderr": true, "output": true, "thinking": true}

// structural は書き換えなかった値が「値の頭がパスの欄」か（新しい欄が増えた兆し。散文と分けて数える）。
// 値の頭が手がかりに当たり、空白を含まず（パスだけの値）、本文の欄の名前でないもの。
func structural(path []seg, v string, counts []domain.PathPrefix) bool {
	if strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	if k, _ := fieldKey(path); proseKeys[k] {
		return false
	}
	_, head := replaceHead(v, counts)
	return head
}

// countUnrewritten は書き換えなかった値に残る、送る端末のパスを数える（構造の欄と散文に分ける）。
func countUnrewritten(st *Stats, path []seg, v string, counts []domain.PathPrefix) {
	n := countPaths(v, counts)
	if n == 0 {
		return
	}
	if structural(path, v, counts) {
		st.UnrewrittenStructural += n
		if st.UnrewrittenFields == nil {
			st.UnrewrittenFields = map[string]int{}
		}
		st.UnrewrittenFields[fieldName(path)] += n
		return
	}
	st.UnrewrittenProse += n
}

// converter は 1 ファイルの変換の状態。
type converter struct {
	opt  Options
	rule func([]seg) fieldRule
	// keyRule はキーの頭を書き換えるオブジェクトか（nil なら、キーは書き換えない）。
	keyRule func([]seg) bool
	ps      []prefix
	counts  []domain.PathPrefix // 書き換えなかったパスを数える手がかり（置き換え先は countMarker）
}

func newConverter(opt Options) (*converter, error) {
	c := &converter{opt: opt, ps: opt.Hints.prefixes()}
	switch opt.Agent {
	case AgentClaude:
		c.rule = claudeRule
	case AgentCodex:
		c.rule = codexRule
		c.keyRule = codexKeyRule
	default:
		return nil, i18n.Errorf("transcript.err.agent", "agent", string(opt.Agent))
	}
	for _, p := range c.ps {
		c.counts = append(c.counts, domain.PathPrefix{Path: p.value, Repl: countMarker})
	}
	return c, nil
}

// precheck は 2 の段（読めるか・キーに秘密が無いか）。値は返さない。
func precheck(doc []byte) (readable, secretKey bool) {
	if _, err := jsonorder.Decode(doc); err != nil {
		return false, false
	}
	err := scanJSON(doc, func(_ []seg, key bool, s, e int) {
		if !key || secretKey {
			return
		}
		k, _, _ := decodeString(doc[s:e])
		if _, hit := domain.FindSecret(k); hit {
			secretKey = true
		}
	})
	if err != nil {
		return false, false
	}
	return true, secretKey
}

// convertDoc は JSON の値 1 つ（jsonl の 1 行・json のファイル全体）に 3〜5 をかける。変わらなければ doc をそのまま返す。
func (c *converter) convertDoc(doc []byte, nonce string) ([]byte, Stats, error) {
	var st Stats
	st.Lines = 1
	pps := pathPrefixes(c.ps, nonce)
	type repl struct {
		s, e int
		b    []byte
	}
	var repls []repl
	masked := false
	var keyOK map[int]bool
	if c.keyRule != nil {
		keyOK = keyRewritable(doc, c.keyRule, pps)
	}
	err := scanJSON(doc, func(path []seg, key bool, s, e int) {
		tok := doc[s:e]
		old, decOff, rawOff := decodeString(tok)
		if key {
			k := old
			if c.keyRule != nil && c.keyRule(path) && keyOK[path[len(path)-1].at] {
				if nk, ok := replaceHead(old, pps); ok {
					k = nk
					st.Rewritten += strings.Count(nk, placeholderHead(nonce))
					repls = append(repls, repl{s, e, spliceString(tok, old, decOff, rawOff, nk)})
				}
			}
			if n := countPaths(k, c.counts); n > 0 {
				st.UnrewrittenKeys += n
			}
			return
		}
		v := old
		switch c.rule(path) {
		case ruleHead:
			v, _ = replaceHead(v, pps)
		case ruleAnywhere:
			v = domain.ReplacePathPrefixes(v, pps)
		case ruleCommandHead:
			// 先頭の語の後ろ（引数）に残ったパスは、書き換えない欄と同じく数える
			v, _ = replaceCommandHead(v, pps)
			countUnrewritten(&st, path, v, c.counts)
		default:
			countUnrewritten(&st, path, v, c.counts)
		}
		if v != old {
			st.Rewritten += strings.Count(v, placeholderHead(nonce))
		}
		mv, mc := domain.MaskSecretsCount(v)
		for label, n := range mc {
			if st.Masked == nil {
				st.Masked = map[string]int{}
			}
			st.Masked[label] += n
			masked = true
		}
		if mv != old {
			repls = append(repls, repl{s, e, spliceString(tok, old, decOff, rawOff, mv)})
		}
	})
	if err != nil {
		return nil, Stats{}, err
	}
	if masked {
		st.MaskedLines = 1
	}
	if len(repls) == 0 {
		if breakAfter != nil {
			return breakAfter(doc), st, nil
		}
		return doc, st, nil
	}
	st.ChangedLines = 1
	out := make([]byte, 0, len(doc)+64)
	last := 0
	for _, r := range repls {
		out = append(out, doc[last:r.s]...)
		out = append(out, r.b...)
		last = r.e
	}
	out = append(out, doc[last:]...)
	if breakAfter != nil {
		out = breakAfter(out)
	}
	return out, st, nil
}

func (c *converter) newNonce() (string, error) {
	r := c.opt.Rand
	if r == nil {
		r = rand.Reader
	}
	var b [6]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", i18n.Wrapf(err, "transcript.err.random")
	}
	return hex.EncodeToString(b[:]), nil
}

// usedKinds は data に現れるプレースホルダの種類（重なりなし・並べたもの）。
func usedKinds(data []byte, nonce string) []string {
	head := []byte(placeholderHead(nonce))
	seen := map[string]bool{}
	for i := 0; ; {
		k := bytes.Index(data[i:], head)
		if k < 0 {
			break
		}
		i += k + len(head)
		end := bytes.Index(data[i:], []byte("}}"))
		if end < 0 {
			break
		}
		seen[string(data[i:i+end])] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Convert は 1 ファイルの中身を送るチャンクにする（§9-8 の 1〜7）。送れないときは *RejectError（件数だけ）。
func Convert(data []byte, opt Options) (*Result, error) {
	if opt.MaxChunkBytes <= 0 {
		opt.MaxChunkBytes = DefaultMaxChunkBytes
	}
	switch opt.Kind {
	case KindText:
		return convertText(data, opt)
	case KindJSON:
		return convertWhole(data, opt)
	case KindJSONL:
		return convertLines(data, opt)
	}
	return nil, i18n.Errorf("transcript.err.kind", "kind", string(opt.Kind))
}

func convertText(data []byte, opt Options) (*Result, error) {
	res := &Result{}
	if len(data) == 0 {
		return res, nil
	}
	out, mc := domain.MaskSecretsCount(string(data))
	if len(out) > opt.MaxChunkBytes {
		return nil, reject(i18n.Errorf("transcript.err.too_long", "count", 1, "max", opt.MaxChunkBytes))
	}
	st := Stats{Lines: 1, Masked: mc}
	if len(mc) > 0 {
		st.MaskedLines = 1
	}
	if out != string(data) {
		st.ChangedLines = 1
	}
	res.Chunks = []Chunk{{Offset: opt.BaseOffset, End: opt.BaseOffset + int64(len(data)), Data: []byte(out), Stats: st}}
	res.Stats = st
	return res, nil
}

func convertWhole(data []byte, opt Options) (*Result, error) {
	c, err := newConverter(opt)
	if err != nil {
		return nil, err
	}
	res := &Result{}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, reject(i18n.Errorf("transcript.err.unreadable", "count", 1))
	}
	readable, secretKey := precheck(data)
	if !readable {
		return nil, reject(i18n.Errorf("transcript.err.unreadable", "count", 1))
	}
	if secretKey {
		return nil, reject(i18n.Errorf("transcript.err.secret_key", "count", 1))
	}
	for attempt := 0; ; attempt++ {
		if attempt == nonceAttempts {
			return nil, reject(i18n.Errorf("transcript.err.nonce", "count", nonceAttempts))
		}
		nonce, err := c.newNonce()
		if err != nil {
			return nil, err
		}
		if bytes.Contains(data, []byte(placeholderHead(nonce))) {
			continue
		}
		out, st, err := c.convertDoc(data, nonce)
		if err != nil {
			return nil, reject(i18n.Errorf("transcript.err.unreadable", "count", 1))
		}
		if _, err := jsonorder.Decode(out); err != nil {
			return nil, reject(i18n.Errorf("transcript.err.unreadable_after", "count", 1))
		}
		if len(out) > opt.MaxChunkBytes {
			return nil, reject(i18n.Errorf("transcript.err.too_long", "count", 1, "max", opt.MaxChunkBytes))
		}
		res.Chunks = []Chunk{{Offset: opt.BaseOffset, End: opt.BaseOffset + int64(len(data)), Data: out, Nonce: nonce,
			Placeholders: usedKinds(out, nonce), Stats: st}}
		res.Stats = st
		return res, nil
	}
}

// line は元のファイルの 1 行（改行を含まない中身と、改行を含む範囲）。
type line struct {
	body       []byte
	start, end int
}

func splitLines(data []byte) ([]line, int) {
	var ls []line
	start := 0
	for {
		k := bytes.IndexByte(data[start:], '\n')
		if k < 0 {
			break
		}
		ls = append(ls, line{body: data[start : start+k], start: start, end: start + k + 1})
		start += k + 1
	}
	return ls, len(data) - start
}

func convertLines(data []byte, opt Options) (*Result, error) {
	c, err := newConverter(opt)
	if err != nil {
		return nil, err
	}
	ls, pending := splitLines(data)
	res := &Result{Pending: pending}
	// 2: 全行を先に確かめる（1 行でも外れれば何も返さない）
	unreadable, secretKeys := 0, 0
	for _, l := range ls {
		readable, secretKey := precheck(l.body)
		switch {
		case !readable:
			unreadable++
		case secretKey:
			secretKeys++
		}
	}
	if unreadable > 0 {
		return nil, reject(i18n.Errorf("transcript.err.unreadable", "count", unreadable))
	}
	if secretKeys > 0 {
		return nil, reject(i18n.Errorf("transcript.err.secret_key", "count", secretKeys))
	}
	// 3〜7: チャンクごとに nonce を決めて変換する
	unreadableAfter := 0
	for i := 0; i < len(ls); {
		var chunk Chunk
		var j, bad int
		for attempt := 0; ; attempt++ {
			if attempt == nonceAttempts {
				return nil, reject(i18n.Errorf("transcript.err.nonce", "count", nonceAttempts))
			}
			nonce, err := c.newNonce()
			if err != nil {
				return nil, err
			}
			head := []byte(placeholderHead(nonce))
			chunk = Chunk{Offset: opt.BaseOffset + int64(ls[i].start), Nonce: nonce}
			var buf bytes.Buffer
			collided := false
			bad = 0
			for j = i; j < len(ls); j++ {
				out, st, err := c.convertDoc(ls[j].body, nonce)
				if err != nil {
					return nil, reject(i18n.Errorf("transcript.err.unreadable", "count", 1))
				}
				if len(out)+1 > opt.MaxChunkBytes {
					return nil, reject(i18n.Errorf("transcript.err.too_long", "count", c.countTooLong(ls[j:], nonce), "max", opt.MaxChunkBytes))
				}
				if buf.Len()+len(out)+1 > opt.MaxChunkBytes {
					break
				}
				if bytes.Contains(ls[j].body, head) {
					collided = true
					break
				}
				if _, err := jsonorder.Decode(out); err != nil {
					bad++
				}
				buf.Write(out)
				buf.WriteByte('\n')
				chunk.Stats.add(st)
			}
			if collided {
				continue
			}
			chunk.Data = buf.Bytes()
			break
		}
		unreadableAfter += bad
		chunk.End = opt.BaseOffset + int64(ls[j-1].end)
		chunk.Placeholders = usedKinds(chunk.Data, chunk.Nonce)
		res.Chunks = append(res.Chunks, chunk)
		res.Stats.add(chunk.Stats)
		i = j
	}
	if unreadableAfter > 0 {
		return nil, reject(i18n.Errorf("transcript.err.unreadable_after", "count", unreadableAfter))
	}
	return res, nil
}

// countTooLong は上限を超える行の数（エラーの件数のため。最初の 1 行は超えていると分かっている）。
func (c *converter) countTooLong(ls []line, nonce string) int {
	n := 0
	for _, l := range ls {
		out, _, err := c.convertDoc(l.body, nonce)
		if err != nil || len(out)+1 > c.opt.MaxChunkBytes {
			n++
		}
	}
	return n
}
