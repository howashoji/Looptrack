package usagesnap

// GitHub Copilot（OpenTelemetry のファイル出力）。以前の CLI（1.0.0 より前）と同じ規則:
//   - gen_ai.conversation.id がそのセッションのスパンだけ（ID の無い chat は同じトレースの他のスパンから引く）
//   - 同じスパン（traceId + spanId、無ければ response.id、無ければ行の中身）は 1 回
//   - トレースごとに「chat の合計」と「invoke_agent の最大（根）」の項目ごとの大きい方（github/copilot-cli#4860 の欠けを根で埋める）
//   - 入力は input_tokens からキャッシュの読み・作成を引いた値（input_tokens がその和より小さい出力元は引かない）
//   - cache の属性が無い根（Copilot CLI の invoke_agent）の input_tokens は、同じトレースの chat の入力から引いたキャッシュの和を引いてから比べる

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// CopilotOtelEnv は OTel の JSONL の場所を足す環境変数の設定名（LOOPTRACK_USAGE_COPILOT_OTEL）。
const CopilotOtelEnv = "USAGE_COPILOT_OTEL"

var copilotSessionAttrs = []string{"gen_ai.conversation.id", "session.id", "copilot_chat.session_id", "copilot_chat.chat_session_id"}

var copilotCacheAttrs = []string{"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_creation.input_tokens", "gen_ai.usage.cache_write.input_tokens"}

// copilotKeys の順: input・cache_create・cache_read・output・reasoning。
var copilotKeys = [5]string{"input", "cache_create", "cache_read", "output", "reasoning"}

type ctok [5]int64

func (t ctok) sum() int64 { return t[0] + t[1] + t[2] + t[3] + t[4] }

// CopilotOtelDir は OTel の JSONL の既定の置き場 ${COPILOT_HOME:-~/.copilot}/otel。
// Copilot CLI（1.0.86 で確認・2026-09-19）は COPILOT_OTEL_FILE_EXPORTER_PATH を hook にもエージェントのシェルにも渡さない
// （渡るのは COPILOT_CLI・COPILOT_CLI_BINARY_VERSION・COPILOT_HOME・COPILOT_PROJECT_DIR・COPILOT_TRACEPARENT）。
// COPILOT_HOME は渡るので、hook と usage attach が必ず見つけられるのはこの下だけ。
func CopilotOtelDir(o Options) string {
	ch := o.Env.Get("COPILOT_HOME")
	if ch == "" {
		ch = o.expandUser("~/.copilot")
	}
	return joinPath(ch, "otel")
}

// CopilotMissHint は Copilot のセッションのスパンが見つからなかったときの案内（usage attach のエラー文・hook のログ用）。
// 探した結果（ファイルが無い／あるがそのセッションのスパンが無い）と、出力先を既定の置き場の下にする理由を 1 文にする。
func CopilotMissHint(lang i18n.Lang, sessionID string, o Options) string {
	dir := CopilotOtelDir(o)
	files := CopilotOtelFiles(o)
	found := i18n.M("usagesnap.copilot.miss.no_files", "dir", dir)
	if len(files) > 0 {
		found = i18n.M("usagesnap.copilot.miss.no_span", "count", len(files), "session", sessionID)
	}
	return i18n.T(lang, "usagesnap.copilot.miss.hint", "found", found, "example", joinPath(dir, "copilot-otel.jsonl"))
}

// CopilotOtelFiles は読む OTel の JSONL の一覧（存在するものだけ・重複なし）。
//  1. LOOPTRACK_USAGE_COPILOT_OTEL（ファイルかディレクトリ。PATH と同じ区切り文字で複数）
//  2. COPILOT_OTEL_FILE_EXPORTER_PATH（この変数が見える環境だけ。Copilot CLI は hook・シェルに渡さない）
//  3. CopilotOtelDir（${COPILOT_HOME:-~/.copilot}/otel/）の下の *.jsonl（サブディレクトリも）。環境変数が無くても必ず見る
func CopilotOtelFiles(o Options) []string {
	var cands []string
	raw, _ := o.Env.Setting(CopilotOtelEnv)
	for _, x := range strings.Split(raw, string(os.PathListSeparator)) {
		if s := trimSpace(x); s != "" {
			cands = append(cands, o.expandUser(s))
		}
	}
	if v := o.Env.Get("COPILOT_OTEL_FILE_EXPORTER_PATH"); v != "" {
		cands = append(cands, o.expandUser(v))
	}
	cands = append(cands, CopilotOtelDir(o))
	var out []string
	seen := map[string]bool{}
	for _, c := range cands {
		var files []string
		switch {
		case isDir(c):
			files = globRecursiveJSONL(c)
		case isFile(c):
			files = []string{c}
		}
		for _, f := range files {
			r := realpath(f)
			if !seen[r] {
				seen[r] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// otelValue は OTLP の JSON の AnyValue（{"stringValue": …} など）なら中身を、そうでなければそのまま返す。
func otelValue(v any) any {
	m := asMap(v)
	if len(m) != 1 {
		return v
	}
	for k, x := range m {
		switch k {
		case "stringValue", "boolValue", "doubleValue":
			return x
		case "intValue":
			if i, ok := toIntNumber(x); ok {
				return i
			}
			return nil
		}
	}
	return v
}

// toIntNumber は int(x)（文字列は前後の空白を除いた 10 進・小数は 0 の側へ切り捨て）。値は json.Number の整数にする。
func toIntNumber(x any) (json.Number, bool) {
	switch v := x.(type) {
	case bool:
		if v {
			return "1", true
		}
		return "0", true
	case json.Number:
		if isIntLiteral(string(v)) {
			return v, true
		}
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return "", false
		}
		bf := new(big.Float).SetFloat64(math.Trunc(f))
		i, _ := bf.Int(nil)
		return json.Number(i.String()), true
	case string:
		s := strings.ReplaceAll(trimSpace(v), "_", "")
		if s == "" || strings.Contains(trimSpace(v), "__") || strings.HasPrefix(trimSpace(v), "_") || strings.HasSuffix(trimSpace(v), "_") {
			return "", false
		}
		i, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return "", false
		}
		return json.Number(i.String()), true
	}
	return "", false
}

func otelAttrs(a any) map[string]any {
	out := map[string]any{}
	switch x := a.(type) {
	case map[string]any:
		for k, v := range x {
			out[k] = otelValue(v)
		}
	case []any: // OTLP の JSON: [{"key": …, "value": {…}}]
		for _, e := range x {
			m := asMap(e)
			if m == nil || !truthy(m["key"]) {
				continue
			}
			k, ok := m["key"].(string)
			if !ok {
				continue
			}
			out[k] = otelValue(m["value"])
		}
	}
	return out
}

// otelInt は _otel_int: 真偽値は 0、数は max(int(v), 0)、数字だけの文字列はその値、他は 0。
func otelInt(v any) int64 {
	switch x := v.(type) {
	case bool:
		return 0
	case json.Number:
		n, ok := toIntNumber(x)
		if !ok {
			return 0
		}
		i, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil {
			if strings.HasPrefix(string(n), "-") {
				return 0
			}
			return math.MaxInt64
		}
		return max(i, 0)
	case string:
		s := trimSpace(x)
		if s == "" || !allDigits(s) {
			return 0
		}
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return math.MaxInt64
		}
		return i
	}
	return 0
}

// toFloat は float(v)（数・数の文字列・真偽値）。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil && !math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case string:
		s := strings.ToLower(trimSpace(x))
		switch s {
		case "inf", "+inf", "infinity", "+infinity":
			return math.Inf(1), true
		case "-inf", "-infinity":
			return math.Inf(-1), true
		case "nan", "+nan", "-nan":
			return math.NaN(), true
		}
		if strings.Contains(s, "_") {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil && !math.IsInf(f, 0) {
			return 0, false
		}
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "-0x") || strings.HasPrefix(s, "+0x") {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// otelTime は [秒, ナノ秒]・数値（秒 / ミリ秒 / マイクロ秒 / ナノ秒を桁で判定）・ISO 8601 の文字列を時刻にする。
func otelTime(v any) (time.Time, bool) {
	if l, ok := v.([]any); ok && len(l) == 2 {
		a, ok1 := toFloat(l[0])
		b, ok2 := toFloat(l[1])
		if !ok1 || !ok2 {
			return time.Time{}, false
		}
		return fromTimestamp(a + b/1e9)
	}
	if s, ok := v.(string); ok {
		if st := trimSpace(s); st == "" || !allDigits(st) {
			return isoTime(s)
		}
	}
	n, ok := toFloat(v)
	if !ok || math.IsNaN(n) {
		return time.Time{}, false
	}
	if n <= 0 {
		return time.Time{}, false
	}
	switch {
	case n >= 1e17:
		n /= 1e9
	case n >= 1e14:
		n /= 1e6
	case n >= 1e11:
		n /= 1e3
	}
	return fromTimestamp(n)
}

func otelIDs(d map[string]any) (string, string) {
	for _, holder := range []any{d, d["spanContext"], d["_spanContext"], d["context"]} {
		h := asMap(holder)
		if h == nil {
			continue
		}
		t := h["traceId"]
		if !truthy(t) {
			t = h["trace_id"]
		}
		s := h["spanId"]
		if !truthy(s) {
			s = h["span_id"]
		}
		if truthy(t) || truthy(s) {
			return orStr(t), orStr(s)
		}
	}
	return "", ""
}

type otelSpan struct {
	op, conv, trace, key, model string
	tok                         ctok
	hasUsage                    bool
	hasCache                    bool  // cache の属性（cache_read / cache_creation / cache_write）があるか
	cacheInInput                int64 // input_tokens から引いたキャッシュ（引かなかったときは 0）
	turns                       int64
	start, end                  time.Time
	hasStart, hasEnd            bool
	version                     string
}

func firstTruthy(d map[string]any, keys ...string) any {
	for _, k := range keys {
		if truthy(d[k]) {
			return d[k]
		}
	}
	if len(keys) > 0 {
		return d[keys[len(keys)-1]]
	}
	return nil
}

// otelSpanOf は 1 行を使用量のスパンにする。使用量のスパン（chat / invoke_agent）でなければ nil。
func otelSpanOf(line string) *otelSpan {
	if !strings.Contains(line, "gen_ai.") {
		return nil
	}
	v, ok := decodeLine(line)
	if !ok {
		return nil
	}
	d := asMap(v)
	if d == nil {
		return nil
	}
	attrs := otelAttrs(d["attributes"])
	name := orStr(d["name"])
	op := orStr(attrs["gen_ai.operation.name"])
	if op == "" {
		op, _, _ = strings.Cut(name, " ")
	}
	if op != "chat" && op != "invoke_agent" {
		return nil
	}
	inp := otelInt(attrs["gen_ai.usage.input_tokens"])
	out := otelInt(attrs["gen_ai.usage.output_tokens"])
	cr := otelInt(attrs["gen_ai.usage.cache_read.input_tokens"])
	ccv := attrs["gen_ai.usage.cache_creation.input_tokens"]
	if !truthy(ccv) {
		ccv = attrs["gen_ai.usage.cache_write.input_tokens"]
	}
	cc := otelInt(ccv)
	reasoning := otelInt(attrs["gen_ai.usage.reasoning.output_tokens"])
	trace, span := otelIDs(d)
	conv := ""
	for _, k := range copilotSessionAttrs {
		if truthy(attrs[k]) {
			conv = toStr(attrs[k])
			break
		}
	}
	rid := orStr(attrs["gen_ai.response.id"])
	var key string
	switch {
	case span != "":
		key = "span\x00" + trace + "\x00" + span
	case rid != "":
		key = "resp\x00" + rid
	default:
		sum := sha256.Sum256([]byte(trimSpace(line)))
		key = "line\x00" + hex.EncodeToString(sum[:])
	}
	var rattrs map[string]any
	if res := asMap(d["resource"]); res != nil {
		rattrs = otelAttrs(firstTruthy(res, "attributes", "_attributes", "_rawAttributes"))
	}
	model := attrs["gen_ai.response.model"]
	if !truthy(model) {
		model = attrs["gen_ai.request.model"]
	}
	input := inp
	if inp >= cr+cc {
		input = inp - cr - cc
	}
	hasCache := false
	for _, k := range copilotCacheAttrs {
		if _, ok := attrs[k]; ok {
			hasCache = true
		}
	}
	s := &otelSpan{
		op: op, conv: conv, trace: trace, key: key, model: orStr(model),
		tok:          ctok{input, cc, cr, out, reasoning},
		hasUsage:     inp != 0 || out != 0 || cr != 0 || cc != 0,
		hasCache:     hasCache,
		cacheInInput: inp - input,
		turns:        otelInt(attrs["github.copilot.turn_count"]),
		version:      orStr(rattrs["service.version"]),
	}
	s.start, s.hasStart = otelTime(firstTruthy(d, "startTime", "start_time", "startTimeUnixNano"))
	s.end, s.hasEnd = otelTime(firstTruthy(d, "endTime", "end_time", "endTimeUnixNano", "timestamp"))
	return s
}

type modelTok struct {
	v         ctok
	responses int64
}

type orderedModels struct {
	keys []string
	m    map[string]*modelTok
}

func (om *orderedModels) get(name string) *modelTok {
	if om.m == nil {
		om.m = map[string]*modelTok{}
	}
	x, ok := om.m[name]
	if !ok {
		x = &modelTok{}
		om.m[name] = x
		om.keys = append(om.keys, name)
	}
	return x
}

type otelTrace struct {
	chat       ctok
	chatCache  int64 // chat の input_tokens から引いたキャッシュの和（根の input_tokens を同じ基準にそろえる）
	chats      int64
	byModel    orderedModels
	root       *otelSpan
	start, end time.Time
	hasStart   bool
	hasEnd     bool
	version    string
}

// CollectCopilot は Copilot の会話（セッション）の累計を OTel の JSONL から作る。そのセッションのスパンが無ければ nil（未計測）。
// files が nil なら CopilotOtelFiles で探す。
func CollectCopilot(sessionID string, files []string, cwd string, o Options) *jsonorder.Object {
	if sessionID == "" {
		return nil
	}
	if files == nil {
		files = CopilotOtelFiles(o)
	}
	var spans []*otelSpan
	traceConv := map[string]string{}
	for _, path := range files {
		lines, err := readLines(path)
		if err != nil {
			continue
		}
		for _, line := range lines {
			s := otelSpanOf(line)
			if s == nil {
				continue
			}
			spans = append(spans, s)
			if s.conv != "" && s.trace != "" {
				if _, ok := traceConv[s.trace]; !ok {
					traceConv[s.trace] = s.conv
				}
			}
		}
	}
	var order []string
	traces := map[string]*otelTrace{}
	seen := map[string]bool{}
	for _, s := range spans {
		conv := s.conv
		if conv == "" {
			conv = traceConv[s.trace]
		}
		if conv != sessionID || seen[s.key] {
			continue
		}
		seen[s.key] = true
		tk := "t\x00" + s.trace
		if s.trace == "" {
			tk = "k\x00" + s.key
		}
		t, ok := traces[tk]
		if !ok {
			t = &otelTrace{}
			traces[tk] = t
			order = append(order, tk)
		}
		for _, w := range []struct {
			t  time.Time
			ok bool
		}{{s.start, s.hasStart}, {s.end, s.hasEnd}} {
			if !w.ok {
				continue
			}
			if !t.hasStart || w.t.Before(t.start) {
				t.start, t.hasStart = w.t, true
			}
			if !t.hasEnd || w.t.After(t.end) {
				t.end, t.hasEnd = w.t, true
			}
		}
		if t.version == "" {
			t.version = s.version
		}
		if s.op == "chat" {
			if !s.hasUsage {
				continue // 使用量の欠けた chat（copilot-cli#4860）は根で埋める
			}
			t.chats++
			t.chatCache += s.cacheInInput
			name := s.model
			if name == "" {
				name = "(copilot)"
			}
			m := t.byModel.get(name)
			m.responses++
			for k := range copilotKeys {
				t.chat[k] += s.tok[k]
				m.v[k] += s.tok[k]
			}
		} else if s.hasUsage && (t.root == nil || s.tok.sum() > t.root.tok.sum()) {
			t.root = s
		}
	}
	if len(order) == 0 {
		return nil
	}
	list := make([]*otelTrace, len(order))
	for i, k := range order {
		list[i] = traces[k]
	}
	minTime := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	sort.SliceStable(list, func(i, j int) bool {
		a, b := minTime, minTime
		if list[i].hasStart {
			a = list[i].start
		}
		if list[j].hasStart {
			b = list[j].start
		}
		return a.Before(b)
	})

	var segments []*segment
	var byModel orderedModels
	var firstTS, lastTS time.Time
	hasFirst, hasLast := false, false
	version := ""
	for _, t := range list {
		root := t.root
		var rootTok ctok
		if root != nil {
			rootTok = root.tok
			if !root.hasCache {
				// Copilot CLI の根は input_tokens（chat の和＝キャッシュを含む）と output_tokens だけを持つ（1.0.86 の実データ）。
				// chat の入力から引いたのと同じだけ引く（キャッシュを含まない出力元の chat からは何も引いていないので、根からも引かない）
				rootTok[0] = max(rootTok[0]-t.chatCache, 0)
			}
		}
		var val ctok
		for k := range copilotKeys {
			val[k] = max(t.chat[k], rootTok[k])
		}
		responses := t.chats
		if root != nil {
			responses = max(responses, root.turns)
		}
		if responses == 0 && root != nil {
			responses = 1
		}
		for _, name := range t.byModel.keys {
			m := t.byModel.m[name]
			bm := byModel.get(name)
			for k := range copilotKeys {
				bm.v[k] += m.v[k]
			}
			bm.responses += m.responses
		}
		var extra ctok
		anyExtra := false
		for k := range copilotKeys {
			extra[k] = val[k] - t.chat[k]
			if extra[k] != 0 {
				anyExtra = true
			}
		}
		if anyExtra || responses > t.chats {
			name := "(copilot)"
			if root != nil && root.model != "" {
				name = root.model
			}
			bm := byModel.get(name)
			for k := range copilotKeys {
				bm.v[k] += extra[k]
			}
			bm.responses += responses - t.chats
		}
		start := ""
		if t.hasStart {
			start = utcString(t.start)
		}
		seg := newSeg("", start, "human")
		if t.hasEnd {
			seg.end = utcString(t.end)
		} else {
			seg.end = start
		}
		seg.main = tokens{val[0], val[1], val[2], val[3]}
		seg.responses = responses
		segments = append(segments, seg)
		if !hasFirst && t.hasStart {
			firstTS, hasFirst = t.start, true
		}
		if hasLast && t.hasEnd {
			if t.end.After(lastTS) {
				lastTS = t.end
			}
		} else if t.hasEnd {
			lastTS, hasLast = t.end, true
		}
		if version == "" {
			version = t.version
		}
	}
	bmObj := jsonorder.NewObject()
	for _, name := range byModel.keys {
		m := byModel.m[name]
		mo := jsonorder.NewObject()
		for k, key := range copilotKeys {
			mo.Set(key, m.v[k])
		}
		mo.Set("responses", m.responses)
		bmObj.Set(name, mo)
	}
	cwdName := ""
	if cwd != "" {
		cwdName = baseName(realpath(cwd))
	}
	ft, lt := "", ""
	if hasFirst {
		ft = utcString(firstTS)
	}
	if hasLast {
		lt = utcString(lastTS)
	}
	p := finish(finishArgs{
		client: ClientCopilot, version: version, sessionID: sessionID, segments: segments, byModel: bmObj,
		cwdName: cwdName, firstTS: ft, lastTS: lt,
	}, o)
	// 会話 ID はセッション ID（指示文が記録に無いので「最初の指示の時刻 + 内容」を作れない。Copilot は再開しても同じ ID）
	p.Set("conversation_id", runePrefix(sessionID, 64))
	return p
}
