package cli

// looptrack session transcript push（DESIGN.md §9-8「送る」「送った続きの判断」）。
//
// 順:
//  1. 記録を探す（usagesnap.DiscoverTranscript。セッション ID は引数か --current（X-Looptrack-Session と同じ決め方））。
//  2. 運ぶファイルを全部読み、状態のファイルの長さから先に変換する（transcript.Convert）。止め（RejectError）が 1 つでも
//     あれば、要求を 1 本も送らずに件数と理由だけを出して終了コード 1。--dry-run はここで件数だけを出して終える（要求を送らない）。
//  3. 記録を登録する（同じものがあれば今の連番とファイルごとの送った長さが返る）。サーバの長さが変換に使った長さと違う
//     ファイルは、サーバの長さから変換し直す。
//  4. チャンクを順に追記する（If-Match: "<今の seq>"・位置と印は X-Looptrack-Transcript-*・本文は gzip）。
//     409（transcript_seq・transcript_offset）は GET /transcripts/{id} で今の連番と長さを取り直し、続きから送り直す（上限つき）。
//  5. チャンクを 1 つ送るたびに、状態のファイル（usagesnap.StateDir の下の transcript-push/<ProjectKey>/<agent>-<セッション ID>.json）に
//     ファイルごとの送った長さと、その直前 4KiB の sha256 を書く。
//
// 送った続きの判断（ファイルごと）: 状態の長さがサーバの長さと同じなら、元のファイルがその長さ以上あり直前 4KiB が同じときだけ
// 続きを送る。状態が無い（別の端末から送った・消えた）・食い違うときは、サーバの長さの直前の 1 バイトが改行であることだけを確かめる。
// 短くなった・中身が変わったファイルは送らず、件数で知らせる（purge してから送り直せば最初から送れる）。json（*.meta.json など）と
// text（tool-results）はファイル全体を 1 回だけ送り、後から変わったら同じく「書き換えられた」として数える。

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/agentdirs"
	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/client/transcript"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

const (
	// trTailBytes は送った続きの判断で比べる、送った長さの直前のバイト数。
	trTailBytes = 4096
	// trMaxResync は 409 の後に取り直して送り直す回数の上限。
	trMaxResync = 3
	// trMinChunkBytes は 413 で上限を半分にしていくときの下限（ここより小さくはしない。32MiB から 10 回まで）。
	trMinChunkBytes = 64 << 10
	// trStateVersion は状態のファイルの形の版。
	trStateVersion = 1
	// trStateDir は状態のファイルの置き場（usagesnap.StateDir の下）。
	trStateDir = "transcript-push"
)

// trSent はファイル 1 つの送った長さと、その直前 trTailBytes の sha256。
type trSent struct {
	Length int64  `json:"length"`
	Tail   string `json:"tail_sha256"`
}

// trPushState は状態のファイルの中身（値は送った長さと sha256 だけ。中身もパスも入れない）。
type trPushState struct {
	Version      int               `json:"version"`
	TranscriptID int64             `json:"transcript_id"`
	Sent         map[string]trSent `json:"sent"`
}

// trStatePath は状態のファイル。API の URL・プロジェクト・置き場が決まらなければ ""。
func trStatePath(e env.Env, agent, sid string) string {
	base, _ := e.Setting(env.APIURL)
	project, _ := e.Setting(env.Project)
	key := usagesnap.ProjectKey(base, project)
	dir := usagesnap.StateDir(e)
	if key == "" || dir == "" {
		return ""
	}
	return filepath.Join(dir, trStateDir, key, agent+"-"+sid+".json")
}

func readPushState(path string) *trPushState {
	st := &trPushState{Version: trStateVersion, Sent: map[string]trSent{}}
	if path == "" {
		return st
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	var got trPushState
	if json.Unmarshal(b, &got) != nil || got.Version != trStateVersion || got.Sent == nil {
		return st
	}
	return &got
}

// writePushState は状態のファイルを書く（一時ファイル → rename。書けなくても送信は止めない）。
func writePushState(path string, st *trPushState) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
}

// tailSum は data[:n] の末尾 trTailBytes の sha256。
func tailSum(data []byte, n int64) string {
	from := n - trTailBytes
	if from < 0 {
		from = 0
	}
	sum := sha256.Sum256(data[from:n])
	return hex.EncodeToString(sum[:])
}

// trKindOf はファイルの読み方（サーバの service.TranscriptFileKind と同じ対応）。
func trKindOf(k usagesnap.TranscriptKind) transcript.Kind {
	switch k {
	case usagesnap.KindSubagentMeta:
		return transcript.KindJSON
	case usagesnap.KindToolResult:
		return transcript.KindText
	}
	return transcript.KindJSONL
}

// trSource は運ぶファイル 1 つと、読んだ時点の中身。
type trSource struct {
	usagesnap.TranscriptFile
	kind transcript.Kind
	data []byte
}

// trPlan はファイル 1 つについて今回送るもの。
type trPlan struct {
	src       *trSource
	offset    int64
	res       *transcript.Result
	rewritten bool // 元のファイルが短くなった・送った部分が変わった（送らない）
}

// trPlanAll は offsets（ファイルごとの送った長さ）から各ファイルの送る分を決めて変換する。
// state は状態のファイル（送った長さが offsets と同じときだけ直前 4KiB を比べる）。止めはファイルごとに rejects に入れる。
func trPlanAll(srcs []*trSource, offsets map[string]int64, state *trPushState, opt transcript.Options) (plans []*trPlan, rejects []trReject) {
	for _, s := range srcs {
		p := &trPlan{src: s, offset: offsets[s.FileKey]}
		plans = append(plans, p)
		size := int64(len(s.data))
		if p.offset > 0 {
			if size < p.offset {
				p.rewritten = true
				continue
			}
			if sent, ok := state.Sent[s.FileKey]; ok && sent.Length == p.offset {
				if tailSum(s.data, p.offset) != sent.Tail {
					p.rewritten = true
					continue
				}
			} else if s.kind == transcript.KindJSONL && s.data[p.offset-1] != '\n' {
				p.rewritten = true
				continue
			}
			if s.kind != transcript.KindJSONL {
				// json と text はファイル全体を 1 回だけ送る
				if size != p.offset {
					p.rewritten = true
				}
				continue
			}
		}
		data := s.data[p.offset:]
		if s.kind == transcript.KindJSON && len(bytes.TrimSpace(data)) == 0 {
			continue // 書きかけ（空）の json は次の回に送る
		}
		o := opt
		o.Kind = s.kind
		o.BaseOffset = p.offset
		res, err := transcript.Convert(data, o)
		if err != nil {
			rejects = append(rejects, trReject{key: s.FileKey, err: err})
			continue
		}
		p.res = res
	}
	return plans, rejects
}

// trReject は変換が送るのを止めたファイル（理由は件数だけ）。
type trReject struct {
	key string
	err error
}

// trTotals は件数の合計（出力用。値は持たない）。
type trTotals struct {
	files, chunks, rewritten, pending int
	stats                             transcript.Stats
}

func trSum(plans []*trPlan) trTotals {
	var t trTotals
	for _, p := range plans {
		if p.rewritten {
			t.rewritten++
			continue
		}
		if p.res == nil {
			continue
		}
		t.pending += p.res.Pending
		if len(p.res.Chunks) == 0 {
			continue
		}
		t.files++
		t.chunks += len(p.res.Chunks)
		addStats(&t.stats, p.res.Stats)
	}
	return t
}

// addStats は件数を足す（transcript.Stats の足し算は非公開なので、出力に要る欄だけ）。
func addStats(dst *transcript.Stats, s transcript.Stats) {
	dst.Lines += s.Lines
	dst.Rewritten += s.Rewritten
	dst.MaskedLines += s.MaskedLines
	dst.UnrewrittenStructural += s.UnrewrittenStructural
	dst.UnrewrittenProse += s.UnrewrittenProse
	dst.UnrewrittenKeys += s.UnrewrittenKeys
	for k, n := range s.Masked {
		if dst.Masked == nil {
			dst.Masked = map[string]int{}
		}
		dst.Masked[k] += n
	}
	for k, n := range s.UnrewrittenFields {
		if dst.UnrewrittenFields == nil {
			dst.UnrewrittenFields = map[string]int{}
		}
		dst.UnrewrittenFields[k] += n
	}
}

// countList は「語 件数」を並べた文（語の順）。
func countList(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+strconv.Itoa(m[k]))
	}
	return strings.Join(parts, ", ")
}

// trTarget は push する記録（agent とセッション ID）。
type trTarget struct {
	agent, sid string
}

// pushTarget は引数と環境から agent とセッション ID を決める。agent が決まらなければ空（両方で探す）。
func pushTarget(c *Ctx, v *Values) (trTarget, error) {
	sid := strings.TrimSpace(v.Str("session"))
	agent := v.Str("agent")
	if sid != "" && v.Bool("current") {
		return trTarget{}, i18n.Errorf("cli.err.transcript.session_and_current")
	}
	if sid == "" {
		m := session.Detect(c.Env)
		if m.Session == "" || m.Kind == session.KindHost {
			return trTarget{}, i18n.Errorf("cli.err.transcript.no_current")
		}
		sid = m.Session
	}
	if agent == "" {
		switch session.AIKind(c.Env) {
		case trAgentClaude:
			if v.Str("session") == "" {
				agent = trAgentClaude
			}
		case trAgentCodex:
			if v.Str("session") == "" {
				agent = trAgentCodex
			}
		}
	}
	return trTarget{agent: agent, sid: sid}, nil
}

// discoverTranscriptFiles は記録の探索（テストで列挙の失敗を作るために差し替える）。
var discoverTranscriptFiles = usagesnap.DiscoverTranscript

// discoverTranscript は記録を探す（agent が空なら Claude Code → Codex の順）。
func discoverTranscript(c *Ctx, tg trTarget) (*usagesnap.TranscriptFiles, error) {
	wd, _ := os.Getwd()
	o := usagesnap.Options{Env: c.Env}
	agents := []string{tg.agent}
	if tg.agent == "" {
		agents = []string{trAgentClaude, trAgentCodex}
	}
	for _, a := range agents {
		files, err := discoverTranscriptFiles(a, tg.sid, wd, o)
		if err == nil {
			return files, nil
		}
		if !errors.Is(err, usagesnap.ErrTranscriptNotFound) {
			// 探してから列挙するまでにファイルが消えると、OS の誤りが絶対パスを持つ。パスは文面に出さない
			return nil, i18n.Errorf("cli.err.transcript.list_files", "reason", errReason(err))
		}
	}
	return nil, i18n.Errorf("cli.err.transcript.not_found", "session", tg.sid)
}

// pushHints は送る端末の書き換えの元（ルートは pathhint.Collect と同じ 1 か所。§9-8 の種類の表）。
func (c *Ctx) pushHints(files *usagesnap.TranscriptFiles) transcript.Hints {
	ph := c.pathHints()
	home := agentdirs.Home(c.Env.Get)
	h := transcript.Hints{Roots: ph.Roots, Home: ph.Home}
	if files.Agent == trAgentClaude {
		h.SessionDir = filepath.Dir(files.Main)
		h.VendorDir = agentdirs.ClaudeConfigDir(c.Env.Get, home)
	} else {
		h.VendorDir = agentdirs.CodexHome(c.Env.Get, home)
	}
	return h
}

// readSources は運ぶファイルを全部読む（読んだ時点の中身で変換し、送った長さもこの中身で数える）。
func readSources(files *usagesnap.TranscriptFiles) ([]*trSource, error) {
	var out []*trSource
	for _, f := range files.Files {
		b, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, i18n.Errorf("cli.err.transcript.read", "file", f.FileKey, "reason", errReason(err))
		}
		out = append(out, &trSource{TranscriptFile: f, kind: trKindOf(f.Kind), data: b})
	}
	return out, nil
}

// versionRe は記録の版として受け取る形（版らしくない値は送らない）。
var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)

// agentVersion は記録の中の版（Claude は行の version、Codex は session_meta.payload.cli_version）。分からなければ ""。
func agentVersion(agent string, main []byte) string {
	lines := bytes.SplitN(main, []byte("\n"), 201)
	for i, l := range lines {
		if i == 200 {
			break
		}
		var row struct {
			Version string `json:"version"`
			Type    string `json:"type"`
			Payload struct {
				CLIVersion string `json:"cli_version"`
			} `json:"payload"`
		}
		if json.Unmarshal(l, &row) != nil {
			continue
		}
		v := row.Version
		if agent == trAgentCodex {
			if row.Type != "session_meta" {
				continue
			}
			v = row.Payload.CLIVersion
		}
		if versionRe.MatchString(v) {
			return v
		}
	}
	return ""
}

// originOS は送る端末の OS（サーバが受け取る値）。
func originOS() (string, error) {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		return runtime.GOOS, nil
	}
	return "", i18n.Errorf("cli.err.transcript.os", "os", runtime.GOOS)
}

// machineLabel は端末のラベル（LOOPTRACK_MACHINE_LABEL、無ければホスト名の最初の「.」より前。session digest と同じ）。
func (c *Ctx) machineLabel() string {
	l, err := c.digestValues("")("label")
	if err != nil {
		return ""
	}
	if r := []rune(l); len(r) > 64 {
		l = string(r[:64])
	}
	return l
}

// cmdTranscriptPush は会話記録を送る。
func cmdTranscriptPush(c *Ctx, v *Values) error {
	tg, err := pushTarget(c, v)
	if err != nil {
		return err
	}
	files, err := discoverTranscript(c, tg)
	if err != nil {
		return err
	}
	srcs, err := readSources(files)
	if err != nil {
		return err
	}
	opt := transcript.Options{Agent: transcript.Agent(files.Agent), Hints: c.pushHints(files)}
	statePath := trStatePath(c.Env, files.Agent, files.SessionID)
	state := readPushState(statePath)
	local := map[string]int64{}
	for k, s := range state.Sent {
		local[k] = s.Length
	}
	plans, rejects := trPlanAll(srcs, local, state, opt)
	if v.Bool("dry_run") {
		c.printPushCounts(files, trSum(plans), true)
		return c.reportRejects(rejects, 0)
	}
	if err := c.reportRejects(rejects, 0); err != nil {
		return err
	}
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/transcripts")
	if err != nil {
		return err
	}
	osName, err := originOS()
	if err != nil {
		return err
	}
	ver := ""
	if len(srcs) > 0 {
		ver = agentVersion(files.Agent, srcs[0].data)
	}
	reg := jsonorder.NewObject().Set("agent", files.Agent).Set("vendor_session_id", files.SessionID).
		Set("machine_label", c.machineLabel()).Set("origin_os", osName).Set("agent_version", ver)
	var regRes struct {
		Transcript trState `json:"transcript"`
	}
	if _, err := trDo(cl, api.Request{Method: http.MethodPost, Path: path, Body: reg}, &regRes); err != nil {
		return c.pushError(err)
	}
	cur := regRes.Transcript
	usagesnap.RememberTranscriptsRule(cur.TranscriptsEnabled, usagesnap.Options{Env: c.Env})
	if state.TranscriptID != cur.ID {
		if state.TranscriptID != 0 {
			local = nil // 別の記録の状態で決めた長さは使わない（サーバの長さで変換し直す）
		}
		state = &trPushState{Version: trStateVersion, TranscriptID: cur.ID, Sent: map[string]trSent{}}
	}
	sent, total, err := c.sendPlans(cl, srcs, plans, local, state, statePath, opt, cur)
	if err != nil {
		return err
	}
	return c.printPushResult(files, cur.ID, sent, total)
}

// sendPlans はチャンクを順に送る。サーバの長さが変換に使った長さと違えば変換し直し、409 は取り直して続きから送る。
// 送ったチャンクの数と、最後の計画の件数を返す。
func (c *Ctx) sendPlans(cl *api.Client, srcs []*trSource, plans []*trPlan, used map[string]int64, state *trPushState, statePath string,
	opt transcript.Options, cur trState) (int, trTotals, error) {
	sent, resyncs := 0, 0
	var sentStats transcript.Stats
	sentFiles := map[string]bool{}
	for {
		server := cur.lengths()
		if !sameOffsets(srcs, used, server) {
			var rejects []trReject
			plans, rejects = trPlanAll(srcs, server, state, opt)
			if err := c.reportRejects(rejects, sent); err != nil {
				return sent, trTotals{}, err
			}
			used = server
		}
		conflict, tooLarge := false, false
		seq := cur.Seq
	loop:
		for _, p := range plans {
			if p.rewritten || p.res == nil {
				continue
			}
			for _, ch := range p.res.Chunks {
				next, err := appendChunk(cl, cur.ID, seq, p.src, ch)
				if err != nil {
					var ae *api.Error
					if errors.As(err, &ae) && ae.Status == http.StatusConflict && (ae.Code == "transcript_seq" || ae.Code == "transcript_offset") {
						conflict = true
						break loop
					}
					// サーバの上限（transcript_max_chunk_bytes）は応答に載らないので、413 なら上限を半分にして、送った続きから分け直す
					if errors.As(err, &ae) && ae.Status == http.StatusRequestEntityTooLarge && ae.Code == "transcript_too_large" &&
						chunkMax(opt) > trMinChunkBytes {
						tooLarge = true
						break loop
					}
					if errors.As(err, &ae) && ae.Status == http.StatusRequestEntityTooLarge && ae.Code == "transcript_too_large" {
						// 下限まで小さくしても拒まれた。送り済みの数を添える（続きは次の push が送る）。
						// api.Error のまま文面だけを替えるので、hook の分け方（4xx は試し直さない）は変わらない
						// （Message は api.Error を先に拾うため、包む形では文面が替わらない）
						refused := *ae
						refused.Msg = i18n.M("cli.err.transcript.too_large", "min", trMinChunkBytes, "sent", sent, "reason", ae.Text(c.Lang))
						refused.Message = refused.Msg.In(i18n.JA)
						return sent, trTotals{}, &refused
					}
					return sent, trTotals{}, c.pushError(err)
				}
				sent++
				sentFiles[p.src.FileKey] = true
				addStats(&sentStats, ch.Stats)
				seq = next.Seq
				usagesnap.RememberTranscriptsRule(next.TranscriptsEnabled, usagesnap.Options{Env: c.Env})
				state.Sent[p.src.FileKey] = trSent{Length: ch.End, Tail: tailSum(p.src.data, ch.End)}
				writePushState(statePath, state)
			}
		}
		if !conflict && !tooLarge {
			t := trSum(plans)
			t.files, t.chunks, t.stats = len(sentFiles), sent, sentStats
			return sent, t, nil
		}
		if tooLarge {
			opt.MaxChunkBytes = chunkMax(opt) / 2
		} else if resyncs++; resyncs > trMaxResync {
			return sent, trTotals{}, i18n.Errorf("cli.err.transcript.resync", "count", trMaxResync)
		}
		var got struct {
			Transcript trState `json:"transcript"`
		}
		if _, err := trDo(cl, api.Request{Method: http.MethodGet, Path: trPath(cur.ID, "")}, &got); err != nil {
			return sent, trTotals{}, c.pushError(err)
		}
		cur = got.Transcript
		used = nil // 取り直した長さで必ず変換し直す
	}
}

// chunkMax はチャンクの大きさの上限（指定が無ければ transcript.DefaultMaxChunkBytes）。
func chunkMax(opt transcript.Options) int {
	if opt.MaxChunkBytes > 0 {
		return opt.MaxChunkBytes
	}
	return transcript.DefaultMaxChunkBytes
}

// sameOffsets は、変換に使った長さ（used）がサーバの長さと、運ぶファイルすべてで同じか。
func sameOffsets(srcs []*trSource, used, server map[string]int64) bool {
	if used == nil {
		return false
	}
	for _, s := range srcs {
		if used[s.FileKey] != server[s.FileKey] {
			return false
		}
	}
	return true
}

// appendChunk はチャンク 1 つを追記し、応答の今の状態を返す。
func appendChunk(cl *api.Client, id, seq int64, src *trSource, ch transcript.Chunk) (trState, error) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(ch.Data); err != nil {
		return trState{}, err
	}
	if err := zw.Close(); err != nil {
		return trState{}, err
	}
	placeholders := ch.Placeholders
	if placeholders == nil {
		placeholders = []string{}
	}
	ph, _ := json.Marshal(placeholders)
	stats, _ := json.Marshal(headerStats(ch.Stats))
	headers := map[string]string{
		"Content-Type":                        "application/octet-stream",
		"Content-Encoding":                    "gzip",
		"If-Match":                            `"` + strconv.FormatInt(seq, 10) + `"`,
		"X-Looptrack-Transcript-File":         url.PathEscape(src.FileKey),
		"X-Looptrack-Transcript-Offset":       strconv.FormatInt(ch.Offset, 10),
		"X-Looptrack-Transcript-Length":       strconv.FormatInt(ch.End-ch.Offset, 10),
		"X-Looptrack-Transcript-Placeholders": string(ph),
		"X-Looptrack-Transcript-Stats":        string(stats),
	}
	if ch.Nonce != "" {
		headers["X-Looptrack-Transcript-Nonce"] = ch.Nonce
	}
	var res struct {
		Transcript trState `json:"transcript"`
	}
	if _, err := trDo(cl, api.Request{Method: http.MethodPost, Path: trPath(id, "/chunks"), RawBody: gz.Bytes(), Headers: headers}, &res); err != nil {
		return trState{}, err
	}
	return res.Transcript, nil
}

// headerStats は X-Looptrack-Transcript-Stats に載せる件数。マスクした数のキー（規則の語。例「password=…」「Bearer …」）は
// それ自体が秘密の規則に当たる形なので、サーバの stats の検査（値ごとの domain.FindSecret）に拒まれる。キーに domain.MaskSecrets を
// かけた形（「password=***」。マスクした形は FindSecret に当たらない）にして送る。同じ形になったキーの数は足す。
func headerStats(s transcript.Stats) transcript.Stats {
	if len(s.Masked) == 0 {
		return s
	}
	m := make(map[string]int, len(s.Masked))
	for k, n := range s.Masked {
		m[domain.MaskSecrets(k)] += n
	}
	s.Masked = m
	return s
}

// pushError は送信の誤り。ルールが切（422 transcript_rule_off）なら切を覚える。文面はサーバのもの（値は含まない）。
func (c *Ctx) pushError(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Code == "transcript_rule_off" {
		usagesnap.RememberTranscriptsRule(false, usagesnap.Options{Env: c.Env})
	}
	return err
}

// reportRejects は変換の止めを「<ファイル>: <理由>」で標準エラーに出し、1 つでもあれば終了コード 1 にする（値は出さない）。
func (c *Ctx) reportRejects(rejects []trReject, sent int) error {
	if len(rejects) == 0 {
		return nil
	}
	for _, r := range rejects {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.transcript.push.reject", "file", r.key, "reason", i18n.Text(c.Lang, r.err)))
	}
	if sent > 0 {
		// 半分にして分け直した後に出る止めは、送り済みのチャンクがあっても起きる（「何も送らなかった」は偽になる）
		return i18n.Errorf("cli.err.transcript.rejected_sent", "count", len(rejects), "sent", sent)
	}
	return i18n.Errorf("cli.err.transcript.rejected", "count", len(rejects))
}

// printPushCounts は件数を出す（dry-run と送った後で共通。値は出さない）。
func (c *Ctx) printPushCounts(files *usagesnap.TranscriptFiles, t trTotals, dry bool) {
	if dry {
		c.Println(i18n.T(c.Lang, "cli.transcript.push.dry_title", "agent", files.Agent, "session", files.SessionID))
		c.Println(i18n.T(c.Lang, "cli.transcript.push.files", "send", t.files, "skipped", files.SkippedCount(), "rewritten", t.rewritten))
		c.Println(i18n.T(c.Lang, "cli.transcript.push.lines", "lines", t.stats.Lines, "chunks", t.chunks, "pending", t.pending))
	}
	c.Println(i18n.T(c.Lang, "cli.transcript.push.rewrites", "count", t.stats.Rewritten))
	masked := 0
	for _, n := range t.stats.Masked {
		masked += n
	}
	if masked > 0 {
		c.Println(i18n.T(c.Lang, "cli.transcript.push.masked_labels", "count", masked, "labels", countList(t.stats.Masked)))
	} else {
		c.Println(i18n.T(c.Lang, "cli.transcript.push.masked", "count", masked))
	}
	c.Println(i18n.T(c.Lang, "cli.transcript.push.unrewritten", "structural", t.stats.UnrewrittenStructural,
		"prose", t.stats.UnrewrittenProse, "keys", t.stats.UnrewrittenKeys))
	for _, s := range files.Skipped {
		c.Println(i18n.T(c.Lang, "cli.transcript.push.skipped", "name", s.Name, "reason", string(s.Reason), "count", s.Count))
	}
	c.pushNotices(t)
}

// pushNotices は注意（構造の欄に書き換えなかった絶対パス・書き換えられたファイル）を標準エラーに出す。
func (c *Ctx) pushNotices(t trTotals) {
	if t.stats.UnrewrittenStructural > 0 {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.transcript.push.structural_notice", "count", t.stats.UnrewrittenStructural,
			"fields", countList(t.stats.UnrewrittenFields)))
	}
	if t.rewritten > 0 {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.transcript.push.rewritten_notice", "count", t.rewritten))
	}
}

// printPushResult は送った結果を出す。書き換えられたファイルがあれば終了コード 1（purge してから送り直すまで続きを送れない）。
func (c *Ctx) printPushResult(files *usagesnap.TranscriptFiles, id int64, sent int, t trTotals) error {
	if sent == 0 {
		c.Println(i18n.T(c.Lang, "cli.transcript.push.nothing", "id", id, "agent", files.Agent, "session", files.SessionID))
	} else {
		c.Println(i18n.T(c.Lang, "cli.transcript.push.sent", "id", id, "agent", files.Agent, "session", files.SessionID,
			"chunks", sent, "files", t.files, "lines", t.stats.Lines))
	}
	c.printPushCounts(files, t, false)
	if t.rewritten > 0 {
		return &Exit{Code: 1}
	}
	return nil
}
