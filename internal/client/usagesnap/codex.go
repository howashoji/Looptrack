package usagesnap

import (
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/client/agentdirs"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// codexHead は rollout の先頭行（session_meta）の payload。読めなければ nil。
func codexHead(path string) map[string]any {
	line, err := readFirstLine(path)
	if err != nil {
		return nil
	}
	if line == "" {
		line = "{}"
	}
	v, ok := decodeLine(line)
	if !ok {
		return nil
	}
	head := asMap(v)
	if head == nil || head["type"] != "session_meta" {
		return nil
	}
	return asMap(head["payload"])
}

// codexRolloutExt は読める rollout の拡張子。古い rollout は Codex が背景で codexCompressedExt に圧縮する。
// zstd の依存は足さないので圧縮済みは読まず、見つけて件数を知らせる（CodexUnreadRollouts）。
const (
	codexRolloutExt    = ".jsonl"
	codexCompressedExt = ".jsonl.zst"
)

// codexThreadGlob はスレッドの rollout の候補。同じスレッドに新しい rollout を作る処理（Codex の thread/revert）は
// rollout-<時刻>-<スレッド ID>_<別の ID>.jsonl という名前のファイルを作る。
func codexThreadGlob(root, threadID string) []string {
	return codexThreadGlobExt(root, threadID, codexRolloutExt)
}

// codexThreadGlobExt は codexThreadGlob の拡張子を選べる形（圧縮済みの探索にも使う）。
func codexThreadGlobExt(root, threadID, ext string) []string {
	var hits []string
	for _, suffix := range []string{"", "_*"} {
		hits = append(hits, globPaths(joinPath(root, "rollout-*"+threadID+suffix+ext))...)
	}
	return hits
}

// codexFindTranscript は作業ディレクトリが一致する直近の会話記録（session_meta.cwd）。置き場は CODEX_HOME/sessions（無ければ ~/.codex/sessions。agentdirs の 1 か所で決める）。sessionID があればそのスレッドの最新のファイル（名前だけ似ていて session_meta.id が違うものは選ばない）。
// 圧縮済み（.jsonl.zst）は見つけない（読めないため）。見つからなかったときの件数は CodexUnreadRollouts で数える。
func codexFindTranscript(cwd, sessionID string, o Options) string {
	root := agentdirs.CodexSessionsDir(o.Env.Get, o.home())
	if root == "" {
		return ""
	}
	if sessionID != "" {
		return codexFindThread(sessionID, o)
	}
	now := float64(o.now().UnixNano()) / 1e9
	var cands []string
	for _, p := range globPaths(joinPath(root, "*", "*", "*", "rollout-*.jsonl")) {
		if now-mtime(p) <= RecentSec {
			cands = append(cands, p)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return mtime(cands[i]) > mtime(cands[j]) })
	want := realpath(cwd)
	for _, p := range cands {
		meta := codexHead(p)
		if len(meta) > 0 && realpath(strOr(orStrV(meta["cwd"]))) == want {
			return p
		}
	}
	return ""
}

// orStrV は (v or "")。
func orStrV(v any) any {
	if !truthy(v) {
		return ""
	}
	return v
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// codexThreadRoot は同じスレッドの rollout を探す範囲。<CODEX_HOME>/sessions/年/月/日/ の形なら全部の日、
// そうでなければ同じディレクトリだけ。
func codexThreadRoot(transcriptPath string) string {
	abs, err := filepath.Abs(transcriptPath)
	if err != nil {
		abs = transcriptPath
	}
	day := dirName(abs)
	parts := strings.Split(day, string(filepath.Separator))
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	if len(parts) == 3 && allDigits(parts[0]) && allDigits(parts[1]) && allDigits(parts[2]) {
		return joinPath(dirName(dirName(dirName(day))), "*", "*", "*")
	}
	return day
}

// CodexUnreadRollouts は読めなかった Codex の rollout（圧縮済みの rollout-*.jsonl.zst）のうち、同じスレッドのもの。
// 会話の累計には入らないので、呼ぶ側は件数を利用者に知らせる（黙って見落とさない）。
// transcriptPath があればその置き場（<年>/<月>/<日>/ の形なら全部の日）を、無ければ CODEX_HOME/sessions（無ければ
// ~/.codex/sessions）全体を探す。スレッドは session_meta.id（読めなければ sessionID。それも無ければ CODEX_THREAD_ID）。
// CODEX_SESSION_ID は親の会話の ID でサブエージェントのスレッドと一致しないので、環境からは引かない
// （引くと、親のスレッドの圧縮済みを今の会話のものとして数える）。スレッドが分からなければ nil。
func CodexUnreadRollouts(transcriptPath, sessionID string, o Options) []string {
	threadID := ""
	if transcriptPath != "" {
		threadID = strOr(codexHead(transcriptPath)["id"])
	}
	if threadID == "" {
		threadID = sessionID
	}
	if threadID == "" {
		threadID = o.Env.Get("CODEX_THREAD_ID")
	}
	if threadID == "" {
		return nil
	}
	var where string
	if transcriptPath != "" {
		where = codexThreadRoot(transcriptPath)
	} else if root := agentdirs.CodexSessionsDir(o.Env.Get, o.home()); root != "" {
		where = joinPath(root, "*", "*", "*")
	} else {
		return nil
	}
	return codexThreadGlobExt(where, threadID, codexCompressedExt)
}

// codexThreadFiles は同じスレッド（session_meta.id が同じ）の rollout を古い順に返す（圧縮済みは読まない。CodexUnreadRollouts）。
func codexThreadFiles(transcriptPath, threadID string) []string {
	type f struct{ real, path string }
	files := []f{{realpath(transcriptPath), transcriptPath}}
	if threadID != "" {
		where := codexThreadRoot(transcriptPath)
		for _, p := range codexThreadGlob(where, threadID) {
			if strOr(codexHead(p)["id"]) != threadID {
				continue
			}
			r := realpath(p)
			dup := false
			for _, x := range files {
				if x.real == r {
					dup = true
				}
			}
			if !dup {
				files = append(files, f{r, p})
			}
		}
	}
	type keyed struct {
		ts, base, path string
	}
	ks := make([]keyed, len(files))
	for i, x := range files {
		ks[i] = keyed{orStr(codexHead(x.path)["timestamp"]), baseName(x.path), x.path}
	}
	sort.SliceStable(ks, func(i, j int) bool {
		if ks[i].ts != ks[j].ts {
			return ks[i].ts < ks[j].ts
		}
		return ks[i].base < ks[j].base
	})
	out := make([]string, len(ks))
	for i := range ks {
		out[i] = ks[i].path
	}
	return out
}

// codexKeys の順: input_tokens・cached_input_tokens・output_tokens・reasoning_output_tokens・total_tokens。
var codexKeys = [5]string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "total_tokens"}

func codexVec(u map[string]any) [5]int64 {
	var v [5]int64
	for i, k := range codexKeys {
		v[i] = toNum(u[k])
	}
	if v[4] == 0 {
		v[4] = v[0] + v[2]
	}
	return v
}

// codexHuman は人の指示なら (本文, turn_id のキー, true) を返す。
// 旧形式: event_msg の user_message（payload.message）。新形式: event_msg の item_completed で item.type が UserMessage
// （本文は item.content の type=text の text。payload.turn_id）。response_item の role=user は差し込みが混ざるので使わない。
func codexHuman(t any, p map[string]any) (string, string, bool) {
	if t != "event_msg" {
		return "", "", false
	}
	switch p["type"] {
	case "user_message":
		return strOr(p["message"]), "", true
	case "item_completed":
		item := asMap(p["item"])
		if item != nil && item["type"] == "UserMessage" {
			var parts []string
			content, _ := orListV(item["content"]).([]any)
			for _, c := range content {
				m := asMap(c)
				if m == nil || m["type"] != "text" {
					continue
				}
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
			turn := ""
			if truthy(p["turn_id"]) {
				turn = hashKey(p["turn_id"])
			}
			return strings.Join(parts, "\n"), turn, true
		}
	}
	return "", "", false
}

func orListV(v any) any {
	if !truthy(v) {
		return []any{}
	}
	return v
}

// codexTokens は token_count の 5 項目 → 共通の 4 種。内訳の無い記録は合計を入力に入れる。
func codexTokens(v [5]int64) tokens {
	inp, cached, out, total := v[0], v[1], v[2], v[4]
	m := tokens{max(inp-cached, 0), 0, cached, out}
	if m.sum() == 0 && total != 0 {
		m[0] = total
	}
	return m
}

// CollectCodex は Codex の会話（スレッド）の累計（collect_codex）。token_count が 1 つも無ければ nil。
//
// total_token_usage はファイル（rollout）ごとの累計で、同じスレッドでも新しいファイルでは 0 から数え直す。
// 同じスレッドのファイルを古い順に読み、ファイルごと（ファイルの中で減ったときはそこで区切る）の最後の値を足す。
// 別のファイルへ複製された記録（同じ時刻・同じ合計）は二度数えない。応答数は合計が増えた token_count の数。
// 人の指示ごとの区間にはその区間に増えた累計を載せる。ターンの途中に来た 2 件目の指示は割込（intr）。
func CollectCodex(transcriptPath, sessionID string, o Options) *jsonorder.Object {
	head := codexHead(transcriptPath)
	threadID := strOr(head["id"])
	if !truthy(head["id"]) {
		threadID = sessionID
	}
	var segments []*segment
	firstTS, lastTS := "", ""
	version := ""
	excl := false
	seenCounts := map[string]bool{}
	seenMsgs := map[string]bool{}
	var acc [5]int64 // 閉じた区切りの累計の和
	found := false
	running := "" // 実行中のターン
	turnsWithHuman := map[string]bool{}
	var shown [5]int64 // 直前の token_count の時点の会話全体の累計

	bank := func(cur, base [5]int64) {
		for i := range acc {
			acc[i] += max(cur[i]-base[i], 0)
		}
	}
	book := func(cur, base [5]int64) {
		seg := segments[len(segments)-1]
		if seg.codexRaw == nil {
			seg.codexRaw = &[5]int64{}
		}
		for i := range acc {
			now := acc[i] + max(cur[i]-base[i], 0)
			seg.codexRaw[i] += max(now-shown[i], 0)
			shown[i] = max(shown[i], now)
		}
	}

	for _, path := range codexThreadFiles(transcriptPath, threadID) {
		var base [5]int64
		var cur *[5]int64
		lines, err := readLines(path)
		if err != nil {
			continue
		}
		for _, line := range lines {
			v, ok := decodeLine(line)
			if !ok {
				continue
			}
			d := asMap(v)
			if d == nil {
				continue
			}
			ts := tsOf(d["timestamp"])
			if ts != "" {
				if firstTS == "" {
					firstTS = ts
				}
				lastTS = ts
			}
			t := d["type"]
			var p map[string]any
			if truthy(d["payload"]) {
				p = asMap(d["payload"])
				if p == nil {
					continue
				}
			} else {
				p = map[string]any{}
			}
			text, turn, isHuman := codexHuman(t, p)
			switch {
			case t == "session_meta":
				if truthy(p["cli_version"]) {
					version = toStr(p["cli_version"])
				}
			case isHuman:
				mk := ts + "\x00" + text
				if seenMsgs[mk] {
					continue // 別のファイルへ複製された同じ指示
				}
				seenMsgs[mk] = true
				var last *segment
				for i := len(segments) - 1; i >= 0; i-- {
					if segments[i].kind != "start" {
						last = segments[i]
						break
					}
				}
				if last != nil && last.text == text {
					if a, ok := isoTime(ts); ok {
						if b, ok := isoTime(last.start); ok && math.Abs(seconds(b, a)) <= 5 {
							continue // 同じ指示を旧形式と新形式の両方で書いた版（数えるのは 1 回）
						}
					}
				}
				excl = excl || excluded(text)
				kind := "human"
				if turn != "" && turn == running && turnsWithHuman[turn] {
					kind = "intr"
				}
				if turn != "" {
					turnsWithHuman[turn] = true
				}
				segments = append(segments, newSeg(text, ts, kind))
			case t == "event_msg" && p["type"] == "task_started":
				running = ""
				if truthy(p["turn_id"]) {
					running = hashKey(p["turn_id"])
				}
			case t == "event_msg" && (p["type"] == "task_complete" || p["type"] == "turn_aborted"):
				running = "" // 区間の終わりには使わない（移行された古い記録は次の指示の時刻で書く）
			case t == "event_msg" && p["type"] == "token_count":
				u := asMap(orMap(p["info"])["total_token_usage"])
				if len(u) == 0 {
					continue
				}
				vec := codexVec(u)
				found = true
				ck := ts + "\x00" + jsonorder.Compact(vec[4])
				if seenCounts[ck] {
					base = vec // 前のファイルから複製された記録。ここから先はその続き
					c := vec
					cur = &c
					continue
				}
				seenCounts[ck] = true
				if cur != nil && vec[4] < cur[4] {
					bank(*cur, base) // 同じファイルの中で数え直した
					base, cur = [5]int64{}, nil
				}
				if len(segments) == 0 {
					segments = append(segments, newSeg("(session start)", firstTS, "start"))
				}
				if cur == nil || vec[4] > cur[4] {
					last := segments[len(segments)-1]
					last.responses++
					if ts != "" {
						last.end = ts // 区間の終わり＝最後の応答（同じ値の繰り返しは除く）
					}
				}
				c := vec
				cur = &c
				book(*cur, base)
			case t == "response_item" && p["type"] == "message" && p["role"] == "assistant":
				if len(segments) > 0 && ts != "" {
					segments[len(segments)-1].end = ts
				}
			}
		}
		if cur != nil {
			bank(*cur, base)
		}
	}

	if !found {
		return nil
	}
	if len(segments) == 0 {
		segments = append(segments, newSeg("(session start)", firstTS, "start"))
	}
	var responses int64
	for _, s := range segments {
		responses += s.responses
	}
	// 会話全体の累計（送る値）。区間ごとの増分を同じ規則で直した値の和がこれと一致すれば区間に配り、
	// 一致しなければ最後の区間にまとめて載せる
	main := codexTokens(acc)
	reasoning := acc[3]
	perSeg := make([]tokens, len(segments))
	var sums tokens
	for i, s := range segments {
		var raw [5]int64
		if s.codexRaw != nil {
			raw = *s.codexRaw
			s.codexRaw = nil
		}
		perSeg[i] = codexTokens(raw)
		for k := range sums {
			sums[k] += perSeg[i][k]
		}
	}
	if sums == main {
		for i, s := range segments {
			s.main = perSeg[i]
		}
	} else {
		segments[len(segments)-1].main = main
	}
	if responses == 0 {
		segments[len(segments)-1].responses = 1
	}
	bm := main.object()
	r := responses
	if r == 0 {
		r = 1
	}
	bm.Set("responses", r)
	bm.Set("reasoning", reasoning)
	byModel := jsonorder.NewObject().Set("(codex)", bm)
	sid := threadID
	if sid == "" {
		b := baseName(transcriptPath)
		sid = sliceStr(b, len("rollout-"), -len(".jsonl"))
	}
	return finish(finishArgs{
		client: ClientCodex, version: version, sessionID: sid, segments: segments, byModel: byModel,
		firstTS: firstTS, lastTS: lastTS, excluded: excl, branches: nil,
	}, o)
}

// sliceStr は s[i:j]（コードポイント単位・負の j は末尾から）。
func sliceStr(s string, i, j int) string {
	r := []rune(s)
	n := len(r)
	if j < 0 {
		j += n
	}
	i = min(max(i, 0), n)
	j = min(max(j, 0), n)
	if i >= j {
		return ""
	}
	return string(r[i:j])
}
