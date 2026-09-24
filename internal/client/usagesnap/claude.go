package usagesnap

import (
	"fmt"
	"sort"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// claudeFindTranscript はセッション ID から会話記録を探す（プロジェクトのスラッグの規則に依存しないよう glob で引く）。
func claudeFindTranscript(sessionID string, o Options) string {
	if sessionID == "" {
		return ""
	}
	return newest(globPaths(o.expandUser(fmt.Sprintf("~/.claude/projects/*/%s.jsonl", sessionID))))
}

// newest は更新時刻が最も新しいもの（同じ時刻は先のもの）。
func newest(hits []string) string {
	best, bt := "", 0.0
	for _, h := range hits {
		if t := mtime(h); best == "" || t > bt {
			best, bt = h, t
		}
	}
	return best
}

func newIO() *[10]int64 { return &[10]int64{} }

var ioKeys = [10]string{"reads", "rlines", "rbytes", "bins", "bbytes", "pages", "edits", "add", "del", "wbytes"}

const (
	ioReads = iota
	ioRLines
	ioRBytes
	ioBins
	ioBBytes
	ioPages
	ioEdits
	ioAdd
	ioDel
	ioWBytes
)

func countLines(s string) int64 {
	if s == "" {
		return 0
	}
	n := int64(0)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	if s[len(s)-1] != '\n' {
		n++
	}
	return n
}

// strOr は (v or "") の文字列（文字列でなければ ""）。
func strOr(v any) string {
	s, _ := v.(string)
	return s
}

// claudeAccountIO は toolUseResult から読み書きの量を数える（_claude_account_io）。
func claudeAccountIO(io *[10]int64, r map[string]any) {
	f := asMap(r["file"])
	if f != nil && has(f, "content") {
		content := strOr(f["content"])
		var n int64
		if isIntValue(f["numLines"]) {
			if b, ok := f["numLines"].(bool); ok {
				if b {
					n = 1
				}
			} else {
				n = toNum(f["numLines"])
			}
		} else {
			n = countLines(content)
		}
		io[ioReads]++
		io[ioRLines] += n
		io[ioRBytes] += runeLen(content)
		return
	}
	if f != nil && has(f, "originalSize") {
		io[ioBins]++
		io[ioBBytes] += toNum(f["originalSize"])
		io[ioPages] += toNum(f["count"])
		return
	}
	if has(r, "structuredPatch") || has(r, "oldString") {
		var add, rem int64
		hunks, _ := r["structuredPatch"].([]any)
		for _, h := range hunks {
			lines, _ := get(h, "lines").([]any)
			for _, ln := range lines {
				s, ok := ln.(string)
				if !ok || s == "" {
					continue
				}
				switch s[0] {
				case '+':
					add++
				case '-':
					rem++
				}
			}
		}
		body := ""
		if truthy(r["content"]) {
			body = strOr(r["content"])
		} else if truthy(r["newString"]) {
			body = strOr(r["newString"])
		}
		if add == 0 && rem == 0 {
			add = countLines(body)
		}
		io[ioEdits]++
		io[ioAdd] += add
		io[ioDel] += rem
		io[ioWBytes] += runeLen(body)
	}
}

// textOf は _text_of(content): 文字列ならそのまま、リストなら type=text の text を改行で連結。
func textOf(c any) string {
	switch x := c.(type) {
	case string:
		return x
	case []any:
		out := ""
		first := true
		for _, b := range x {
			m := asMap(b)
			if m == nil || m["type"] != "text" {
				continue
			}
			if !first {
				out += "\n"
			}
			first = false
			out += strOr(m["text"])
		}
		return out
	}
	return ""
}

// tsOf は d.get("timestamp") or "" の字面。
func tsOf(v any) string {
	if !truthy(v) {
		return ""
	}
	return toStr(v)
}

type claudeResp struct {
	seg   *segment
	side  bool
	u     map[string]any
	model any
}

// CollectClaudeCode は Claude Code の会話記録の累計（collect_claude_code）。応答が 1 つも無ければ nil。
func CollectClaudeCode(transcriptPath, sessionID string, o Options) (*jsonorder.Object, error) {
	lines, err := readLines(transcriptPath)
	if err != nil {
		return nil, err
	}
	var segments []*segment
	respKeys := []string{}
	resp := map[string]*claudeResp{} // message.id → 応答（同じ応答の複数行を 1 回だけ数える）
	io := newIO()
	branch, branches := "", []string{}
	firstTS, lastTS := "", ""
	version := ""
	excl := false

	segAt := func(ts string) *segment {
		if len(segments) == 0 {
			segments = append(segments, newSeg("(session start)", ts, "start"))
			return segments[0]
		}
		hit := segments[0]
		if t, ok := isoTime(ts); ok {
			for _, s := range segments {
				st, ok := isoTime(s.start)
				if ok && st.After(t) {
					break
				}
				if ok {
					hit = s
				}
			}
		}
		return hit
	}

	take := func(d map[string]any, seg *segment, side bool, fallback string) {
		msg := orMap(d["message"])
		u := asMap(msg["usage"])
		if !truthy(msg["usage"]) {
			return
		}
		if u == nil {
			u = map[string]any{}
		}
		var key string
		switch {
		case truthy(msg["id"]):
			key = hashKey(msg["id"])
		case truthy(d["uuid"]):
			key = hashKey(d["uuid"])
		default:
			key = hashKey(fallback)
		}
		if e, ok := resp[key]; !ok {
			resp[key] = &claudeResp{seg: seg, side: side, u: u, model: msg["model"]}
			respKeys = append(respKeys, key)
			if side {
				seg.subRes++
			} else {
				seg.responses++
			}
		} else {
			e.u = u
			if truthy(msg["model"]) {
				e.model = msg["model"]
			}
		}
	}

	for n, line := range lines {
		v, ok := decodeLine(line)
		if !ok {
			continue
		}
		d := asMap(v)
		if d == nil {
			continue // 以前の CLI（1.0.0 より前）は例外になる形（オブジェクトでない行）
		}
		t := d["type"]
		ts := tsOf(d["timestamp"])
		if ts != "" {
			if firstTS == "" {
				firstTS = ts
			}
			lastTS = ts
		}
		if truthy(d["gitBranch"]) {
			branch = toStr(d["gitBranch"])
			found := false
			for _, b := range branches {
				if b == branch {
					found = true
				}
			}
			if !found {
				branches = append(branches, branch)
			}
		}
		if truthy(d["version"]) && version == "" {
			version = toStr(d["version"])
		}
		switch t {
		case "user":
			if tur := asMap(d["toolUseResult"]); tur != nil && len(segments) > 0 {
				claudeAccountIO(io, tur)
			}
			origin := orMap(d["origin"])
			if origin["kind"] != "human" || truthy(d["isSidechain"]) {
				continue
			}
			content := orMap(d["message"])["content"]
			excl = excl || excluded(textOf(content))
			if s, ok := content.(string); ok {
				segments = append(segments, newSeg(s, ts, "human"))
			}
		case "attachment":
			att := orMap(d["attachment"])
			if att["type"] != "queued_command" || orMap(att["origin"])["kind"] != "human" {
				continue
			}
			var pv any = ""
			if p, ok := att["prompt"]; ok {
				pv = p
			}
			prompt := textOf(pv)
			excl = excl || excluded(prompt)
			start := ts
			if truthy(att["timestamp"]) {
				start = toStr(att["timestamp"])
			}
			segments = append(segments, newSeg(prompt, start, "intr"))
		case "assistant":
			if !truthy(orMap(d["message"])["usage"]) {
				continue
			}
			var seg *segment
			if len(segments) > 0 {
				seg = segments[len(segments)-1]
			} else {
				seg = segAt(ts)
			}
			if ts != "" {
				seg.end = ts
			}
			take(d, seg, truthy(d["isSidechain"]), fmt.Sprintf("main:%d", n))
		}
	}

	// サブエージェント（<session>/subagents/*.jsonl）
	mainIDs := map[string]bool{}
	for _, k := range respKeys {
		mainIDs[k] = true
	}
	subDir := joinPath(dirName(transcriptPath), sessionID, "subagents")
	files := globPaths(joinPath(subDir, "*.jsonl"))
	sort.Strings(files)
	for _, fp := range files {
		flines, err := readLines(fp)
		if err != nil {
			continue
		}
		started := ""
		type row struct {
			io bool
			n  int
			d  map[string]any
		}
		var rows []row
		for n, line := range flines {
			v, ok := decodeLine(line)
			if !ok {
				continue
			}
			d := asMap(v)
			if d == nil {
				continue
			}
			if started == "" {
				started = tsOf(d["timestamp"])
			}
			if d["type"] == "assistant" {
				rows = append(rows, row{false, n, d})
			} else if d["type"] == "user" {
				if tur := asMap(d["toolUseResult"]); tur != nil {
					rows = append(rows, row{true, n, tur})
				}
			}
		}
		if len(rows) == 0 {
			continue
		}
		seg := segAt(started)
		for _, r := range rows {
			if r.io {
				claudeAccountIO(io, r.d)
				continue
			}
			msg := orMap(r.d["message"])
			var id any
			switch {
			case truthy(msg["id"]):
				id = msg["id"]
			default:
				id = r.d["uuid"]
			}
			if id == nil || !mainIDs[hashKey(id)] {
				take(r.d, seg, true, fmt.Sprintf("%s:%d", fp, r.n))
			}
		}
	}

	byModel := jsonorder.NewObject()
	type modelAcc struct{ v [8]int64 }
	accs := map[string]*modelAcc{}
	var order []string
	for _, k := range respKeys {
		e := resp[k]
		u := e.u
		x := &e.seg.main
		if e.side {
			x = &e.seg.sub
		}
		x[0] += toNum(u["input_tokens"])
		x[3] += toNum(u["output_tokens"])
		x[2] += toNum(u["cache_read_input_tokens"])
		x[1] += toNum(u["cache_creation_input_tokens"])
		name := "(unknown)"
		if truthy(e.model) {
			name = toStr(e.model)
		}
		m, ok := accs[name]
		if !ok {
			m = &modelAcc{}
			accs[name] = m
			order = append(order, name)
		}
		m.v[0] += toNum(u["input_tokens"])
		m.v[3] += toNum(u["output_tokens"])
		m.v[2] += toNum(u["cache_read_input_tokens"])
		m.v[1] += toNum(u["cache_creation_input_tokens"])
		m.v[4]++
		cc := orMap(u["cache_creation"])
		m.v[5] += toNum(cc["ephemeral_1h_input_tokens"])
		m.v[6] += toNum(cc["ephemeral_5m_input_tokens"])
		m.v[7] += toNum(orMap(u["output_tokens_details"])["thinking_tokens"])
	}
	modelKeys := [8]string{"input", "cache_create", "cache_read", "output", "responses", "cache_create_1h", "cache_create_5m", "thinking"}
	for _, name := range order {
		mo := jsonorder.NewObject()
		for i, k := range modelKeys {
			mo.Set(k, accs[name].v[i])
		}
		byModel.Set(name, mo)
	}

	if len(respKeys) == 0 {
		return nil, nil
	}
	ioObj := jsonorder.NewObject()
	for i, k := range ioKeys {
		ioObj.Set(k, io[i])
	}
	return finish(finishArgs{
		client: ClientClaudeCode, version: version, sessionID: sessionID, segments: segments,
		byModel: byModel, io: ioObj, branch: branch, branches: branches,
		cwdName: baseName(dirName(transcriptPath)), firstTS: firstTS, lastTS: lastTS, excluded: excl,
	}, o), nil
}
