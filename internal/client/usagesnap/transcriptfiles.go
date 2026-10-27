package usagesnap

// 会話記録の搬送（DESIGN.md §9-8）が使う、発見の公開の入口と、運ぶファイルの列挙。
// 探し方は使用量のスナップショットと同じ関数（claudeFindTranscript・codexFindTranscript・codexThreadFiles・
// CodexUnreadRollouts）を使い、置き場の根は agentdirs の 1 か所で決まる（CLAUDE_CONFIG_DIR・CODEX_HOME に従う）。
// ここが返すのは「どのファイルを運ぶか・運ばないか」と、サーバに送る相対パス（file_key）だけで、中身は読まない
// （head の session_meta を除く）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/client/agentdirs"
)

// TranscriptKind は運ぶファイルの種類（サーバの file_key の形と対応する）。
type TranscriptKind string

const (
	KindMain         TranscriptKind = "main"          // Claude: <sessionId>.jsonl
	KindSubagent     TranscriptKind = "subagent"      // Claude: <sessionId>/subagents/*.jsonl
	KindSubagentMeta TranscriptKind = "subagent_meta" // Claude: <sessionId>/subagents/*.json（*.meta.json など）
	KindToolResult   TranscriptKind = "tool_result"   // Claude: <sessionId>/tool-results/*
	KindRollout      TranscriptKind = "rollout"       // Codex: rollout-*.jsonl
)

// SkipReason は運ばないファイルの理由（--dry-run に名前つきで出す）。
type SkipReason string

const (
	SkipWorkflows      SkipReason = "workflows"                // Claude: <sessionId>/workflows/ の下（v1 は運ばない）
	SkipCompressed     SkipReason = "compressed"               // Codex: 圧縮済みの .jsonl.zst（zstd を足さないので読まない）
	SkipUnknown        SkipReason = "unknown"                  // 知らない名前・知らない置き場のファイル
	SkipNotRegular     SkipReason = "not_regular"              // シンボリックリンク・ディレクトリなど。セッション自身のファイルの外へ出さない
	SkipBadKey         SkipReason = "bad_key"                  // file_key の形に合わない名前
	SkipOtherThread    SkipReason = "other_thread"             // Codex: 名前は似ているが別のスレッド
	SkipOutsideRoot    SkipReason = "outside_root"             // Codex: 置き場の根の外
	SkipUnresolvedBase SkipReason = "unresolved_history_base"  // Codex: history_base を指すファイルが見つからない
	SkipDepthLimit     SkipReason = "history_base_depth_limit" // Codex: history_base の連鎖が深すぎる
)

// TranscriptFile は運ぶファイル 1 つ。Path はこの端末での場所（送らない）、FileKey がサーバに送る相対パス。
type TranscriptFile struct {
	FileKey string         // 端末の名前を含まない相対パス（'/' 区切り）。Claude は projects/<slug>/ から、Codex は sessions/ からの相対
	Path    string         // この端末での場所
	Kind    TranscriptKind // main / subagent / subagent_meta / tool_result / rollout
	Size    int64          // 今のバイト数
}

// SkippedFile は運ばないファイルまたはまとまり。Name は相対の名前（絶対パスは入れない）、Count はそのファイルの数。
type SkippedFile struct {
	Name   string
	Reason SkipReason
	Count  int
}

// TranscriptFiles は 1 つの記録（セッション・スレッド）の列挙結果。
type TranscriptFiles struct {
	Agent     string // ClientClaudeCode か ClientCodex
	SessionID string // Claude のセッション ID / Codex のスレッド ID
	Main      string // 本体のこの端末での場所（Claude は <sessionId>.jsonl、Codex は見つけた rollout）
	Files     []TranscriptFile
	Skipped   []SkippedFile
}

// SkippedCount は運ばないファイルの数の合計。
func (t *TranscriptFiles) SkippedCount() int {
	n := 0
	for _, s := range t.Skipped {
		n += s.Count
	}
	return n
}

// ErrTranscriptNotFound は会話記録が見つからなかったとき。
var ErrTranscriptNotFound = errors.New("transcript not found")

// 識別子として許す形。glob の文字（* ? [）と区切りを通さないための関門でもある。
var (
	safeIDRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	keyCompRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@=+-]*$`)
	rolloutRe  = regexp.MustCompile(`^rollout-[A-Za-z0-9._-]+\.jsonl$`)
	yearDirRe  = regexp.MustCompile(`^[0-9]{4}$`)
	monthDayRe = regexp.MustCompile(`^[0-9]{2}$`)
	threadIDRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]{7,63}$`)
)

const (
	maxFileKeyBytes = 255
	maxBaseDepth    = 8  // history_base の連鎖をたどる深さ
	maxBaseStrings  = 16 // history_base の値から取り出す文字列の数
)

// ValidFileKey は file_key の形を確かめる（クライアント側。サーバも agent ごとに同じ形を確かめる）。
// 絶対パス・".."・バックスラッシュ・制御文字・255 バイト超・agent ごとの形に合わないものは false。
func ValidFileKey(agent, key string) bool {
	if key == "" || len(key) > maxFileKeyBytes {
		return false
	}
	parts := strings.Split(key, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || !keyCompRe.MatchString(p) {
			return false
		}
	}
	switch agent {
	case ClientClaudeCode:
		switch len(parts) {
		case 1:
			return strings.HasSuffix(parts[0], ".jsonl")
		case 3:
			return parts[1] == "subagents" || parts[1] == "tool-results"
		}
		return false
	case ClientCodex:
		return len(parts) == 4 && yearDirRe.MatchString(parts[0]) && monthDayRe.MatchString(parts[1]) &&
			monthDayRe.MatchString(parts[2]) && rolloutRe.MatchString(parts[3])
	}
	return false
}

// FindTranscriptMain は会話記録の本体を探す公開の入口。Claude はセッション ID、Codex はスレッド ID か作業ディレクトリ（直近）で引く。
// セッション ID が glob の文字や区切りを含むときは探さない。見つからなければ ""。
func FindTranscriptMain(agent, sessionID, cwd string, o Options) string {
	if sessionID != "" && !safeIDRe.MatchString(sessionID) {
		return ""
	}
	switch agent {
	case ClientClaudeCode:
		return claudeFindTranscript(sessionID, o)
	case ClientCodex:
		if sessionID != "" {
			return codexFindThread(sessionID, o)
		}
		return codexFindTranscript(cwd, sessionID, o)
	}
	return ""
}

// codexFindThread はスレッド ID の最新の rollout。名前が似ているだけで session_meta.id が違うものは選ばない
// （thread/revert の _<別の ID> 付きの名前は別のスレッドのものも並ぶので、名前の glob だけでは本体を取り違える）。
// スレッド ID で引く経路（codexFindTranscript を含む）はすべてここを通す。
func codexFindThread(threadID string, o Options) string {
	root := agentdirs.CodexSessionsDir(o.Env.Get, o.home())
	if root == "" {
		return ""
	}
	var hits []string
	for _, p := range codexThreadGlob(joinPath(root, "*", "*", "*"), threadID) {
		if strOr(codexHead(p)["id"]) == threadID {
			hits = append(hits, p)
		}
	}
	return newestRollout(hits)
}

// newestRollout は更新時刻のいちばん新しい rollout。更新時刻が同じなら名前（rollout-<時刻>-… の時刻が先に来る）の
// 大きいほうを選ぶ。Windows の時計は刻みが粗く、続けて書いた 2 つのファイルの更新時刻がそろうことがある。
// そのとき glob の順で決めると、_<別の ID> 付きの新しい rollout より古い rollout を返してしまう。
func newestRollout(hits []string) string {
	best, bt := "", 0.0
	for _, h := range hits {
		t := mtime(h)
		if best == "" || t > bt || (t == bt && baseName(h) > baseName(best)) {
			best, bt = h, t
		}
	}
	return best
}

// DiscoverTranscript は本体を探して、運ぶファイルを列挙する。見つからなければ ErrTranscriptNotFound。
func DiscoverTranscript(agent, sessionID, cwd string, o Options) (*TranscriptFiles, error) {
	main := FindTranscriptMain(agent, sessionID, cwd, o)
	if main == "" {
		return nil, ErrTranscriptNotFound
	}
	return ListTranscriptFiles(agent, main, sessionID, o)
}

// ListTranscriptFiles は本体（FindTranscriptMain の戻り）の記録について、運ぶファイルと運ばないファイルを列挙する。
// 運ぶファイルは FileKey の順（Claude は本体が先頭）。シンボリックリンクと、記録の置き場の外のものは運ばない。
func ListTranscriptFiles(agent, mainPath, sessionID string, o Options) (*TranscriptFiles, error) {
	switch agent {
	case ClientClaudeCode:
		return listClaude(mainPath)
	case ClientCodex:
		return listCodex(mainPath, sessionID, o)
	}
	return nil, fmt.Errorf("unknown agent %q", agent)
}

// ---- Claude Code ----

func listClaude(mainPath string) (*TranscriptFiles, error) {
	base := baseName(mainPath)
	sid := strings.TrimSuffix(base, ".jsonl")
	if sid == base || !safeIDRe.MatchString(sid) {
		return nil, fmt.Errorf("not a Claude Code transcript name: %q", base)
	}
	st, err := os.Lstat(mainPath)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("transcript is not a regular file: %q", base)
	}
	out := &TranscriptFiles{Agent: ClientClaudeCode, SessionID: sid, Main: mainPath}
	out.Files = append(out.Files, TranscriptFile{FileKey: base, Path: mainPath, Kind: KindMain, Size: st.Size()})

	sessDir := filepath.Join(filepath.Dir(mainPath), sid)
	if !isRealDir(sessDir) {
		if _, err := os.Lstat(sessDir); err == nil {
			out.Skipped = append(out.Skipped, SkippedFile{Name: cleanName(sid), Reason: SkipNotRegular, Count: 1})
		}
		return out, nil
	}
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return out, nil // 付属のディレクトリが無いのはふつう
	}
	var rest []TranscriptFile
	for _, e := range entries {
		name := e.Name()
		if (name == "subagents" || name == "tool-results") && !isRealDir(filepath.Join(sessDir, name)) {
			out.Skipped = append(out.Skipped, SkippedFile{Name: cleanName(sid + "/" + name), Reason: SkipNotRegular, Count: 1})
			continue
		}
		switch name {
		case "subagents":
			claudeAddDir(out, &rest, sid, name, filepath.Join(sessDir, name), func(n string) (TranscriptKind, bool) {
				switch {
				case strings.HasSuffix(n, ".jsonl"):
					return KindSubagent, true
				case strings.HasSuffix(n, ".json"):
					return KindSubagentMeta, true
				}
				return "", false
			})
		case "tool-results":
			claudeAddDir(out, &rest, sid, name, filepath.Join(sessDir, name), func(string) (TranscriptKind, bool) {
				return KindToolResult, true
			})
		case "workflows":
			out.Skipped = append(out.Skipped, SkippedFile{Name: cleanName(sid + "/" + name), Reason: SkipWorkflows, Count: countFiles(filepath.Join(sessDir, name))})
		default:
			out.Skipped = append(out.Skipped, SkippedFile{Name: cleanName(sid + "/" + name), Reason: SkipUnknown, Count: countFiles(filepath.Join(sessDir, name))})
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].FileKey < rest[j].FileKey })
	out.Files = append(out.Files, rest...)
	sortSkipped(out.Skipped)
	return out, nil
}

// claudeAddDir は <sessionId>/<sub>/ の直下（再帰しない）を分ける。kindOf が false の名前は「知らないファイル」として運ばない。
func claudeAddDir(out *TranscriptFiles, rest *[]TranscriptFile, sid, sub, dir string, kindOf func(string) (TranscriptKind, bool)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		display := cleanName(sid + "/" + sub + "/" + name)
		full := filepath.Join(dir, name)
		info, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			out.Skipped = append(out.Skipped, SkippedFile{Name: display, Reason: SkipNotRegular, Count: 1})
			continue
		}
		kind, ok := kindOf(name)
		if !ok || strings.HasPrefix(name, ".") {
			out.Skipped = append(out.Skipped, SkippedFile{Name: display, Reason: SkipUnknown, Count: 1})
			continue
		}
		key := sid + "/" + sub + "/" + name
		if !ValidFileKey(ClientClaudeCode, key) {
			out.Skipped = append(out.Skipped, SkippedFile{Name: display, Reason: SkipBadKey, Count: 1})
			continue
		}
		*rest = append(*rest, TranscriptFile{FileKey: key, Path: full, Kind: kind, Size: info.Size()})
	}
}

// ---- Codex ----

func listCodex(mainPath, sessionID string, o Options) (*TranscriptFiles, error) {
	root := agentdirs.CodexSessionsDir(o.Env.Get, o.home())
	if root == "" {
		return nil, ErrTranscriptNotFound
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(mainPath)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("transcript is not a regular file: %q", baseName(mainPath))
	}
	threadID := strOr(codexHead(mainPath)["id"])
	if threadID == "" {
		threadID = sessionID
	} else if sessionID != "" && sessionID != threadID {
		return nil, fmt.Errorf("transcript %q belongs to another thread", baseName(mainPath))
	}
	out := &TranscriptFiles{Agent: ClientCodex, SessionID: threadID, Main: mainPath}

	w := &codexWalk{out: out, absRoot: absRoot, root: root, seen: map[string]bool{}, skipSeen: map[string]bool{}}
	w.add(mainPath, 0)
	if threadID != "" {
		for _, p := range codexThreadFiles(mainPath, threadID) {
			w.add(p, 0)
		}
		// 名前は同じスレッドに見えるが session_meta.id が違うもの（codexThreadFiles が外したもの）
		for _, p := range codexThreadGlob(codexThreadRoot(mainPath), threadID) {
			if !w.seen[realpath(p)] && !w.skipSeen[realpath(p)] {
				w.skip(p, SkipOtherThread)
			}
		}
		// 圧縮済み（運ばない。件数は CodexUnreadRollouts と同じ関数で数える）
		for _, p := range CodexUnreadRollouts(mainPath, threadID, o) {
			w.skip(p, SkipCompressed)
		}
	}
	w.follow()
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].FileKey < out.Files[j].FileKey })
	sortSkipped(out.Skipped)
	return out, nil
}

type codexWalk struct {
	out      *TranscriptFiles
	absRoot  string
	root     string
	seen     map[string]bool // 運ぶと決めたファイル（realpath）
	skipSeen map[string]bool
	queue    []baseJob
}

type baseJob struct {
	path  string
	depth int
}

// rel は置き場の根からの相対の名前（'/' 区切り）。根の外なら "" と false。
func (w *codexWalk) rel(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	r, err := filepath.Rel(w.absRoot, abs)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

func (w *codexWalk) skip(path string, reason SkipReason) {
	rp := realpath(path)
	if w.skipSeen[rp] {
		return
	}
	w.skipSeen[rp] = true
	name, ok := w.rel(path)
	if !ok {
		name, reason = baseName(path), SkipOutsideRoot
	}
	w.out.Skipped = append(w.out.Skipped, SkippedFile{Name: cleanName(name), Reason: reason, Count: 1})
}

// add は rollout を運ぶ一覧に加える（重複と、置き場の外・シンボリックリンク・形に合わない名前は運ばない）。history_base をたどる候補にも入れる。
func (w *codexWalk) add(path string, depth int) {
	rp := realpath(path)
	if w.seen[rp] {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		return
	}
	if !info.Mode().IsRegular() {
		w.skip(path, SkipNotRegular)
		return
	}
	key, ok := w.rel(path)
	if !ok {
		w.skip(path, SkipOutsideRoot)
		return
	}
	if !ValidFileKey(ClientCodex, key) {
		w.skip(path, SkipBadKey)
		return
	}
	w.seen[rp] = true
	w.out.Files = append(w.out.Files, TranscriptFile{FileKey: key, Path: path, Kind: KindRollout, Size: info.Size()})
	w.queue = append(w.queue, baseJob{path, depth})
}

// follow は運ぶ rollout の session_meta.payload.history_base がたどる元の rollout を、連鎖の深さの上限までたどって加える。
// history_base の中身は絶対パスでもファイル名でもスレッド ID でもありうるとみなし、文字列を取り出して名前の末尾だけを使い、
// 置き場の根の下で探す（値が指す絶対パスは開かない）。
func (w *codexWalk) follow() {
	for len(w.queue) > 0 {
		job := w.queue[0]
		w.queue = w.queue[1:]
		hb, ok := codexHead(job.path)["history_base"]
		if !ok || hb == nil {
			continue
		}
		if job.depth >= maxBaseDepth {
			w.out.Skipped = append(w.out.Skipped, SkippedFile{Name: "history_base", Reason: SkipDepthLimit, Count: 1})
			continue
		}
		resolved := false
		for _, s := range collectStrings(hb) {
			for _, p := range w.resolveBase(s) {
				resolved = true
				w.addBase(p, job.depth+1)
			}
		}
		if !resolved {
			w.out.Skipped = append(w.out.Skipped, SkippedFile{Name: "history_base", Reason: SkipUnresolvedBase, Count: 1})
		}
	}
}

func (w *codexWalk) addBase(path string, depth int) {
	if strings.HasSuffix(path, codexCompressedExt) {
		w.skip(path, SkipCompressed)
		return
	}
	w.add(path, depth)
}

// resolveBase は history_base の文字列 1 つを、置き場の根の下のファイルに引く。
func (w *codexWalk) resolveBase(s string) []string {
	name := s[strings.LastIndexAny(s, `/\`)+1:]
	days := joinPath(w.root, "*", "*", "*")
	switch {
	case strings.HasPrefix(name, "rollout-") && (strings.HasSuffix(name, codexRolloutExt) || strings.HasSuffix(name, codexCompressedExt)):
		if !safeIDRe.MatchString(name) {
			return nil
		}
		return globPaths(joinPath(days, name))
	case threadIDRe.MatchString(name):
		hits := codexThreadGlob(days, name)
		var out []string
		for _, p := range hits {
			if strOr(codexHead(p)["id"]) == name {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil
		}
		// 同じスレッドの rollout を全部（_<別の ID> 付きを含む）
		files := codexThreadFiles(newest(out), name)
		return append(files, codexThreadGlobExt(days, name, codexCompressedExt)...)
	}
	return nil
}

// collectStrings は JSON の値から文字列の葉を取り出す（深さ・個数に上限）。
func collectStrings(v any) []string {
	var out []string
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 3 || len(out) >= maxBaseStrings {
			return
		}
		switch x := v.(type) {
		case string:
			if x != "" {
				out = append(out, x)
			}
		case []any:
			for _, e := range x {
				walk(e, depth+1)
			}
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k], depth+1)
			}
		}
	}
	walk(v, 0)
	return out
}

// ---- 共通 ----

// isRealDir はシンボリックリンクではない実際のディレクトリか（リンクの先の記録の外へ出ないため）。
func isRealDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// countFiles はディレクトリの下のファイルの数（シンボリックリンクはたどらない）。ファイルそのものなら 1。
func countFiles(path string) int {
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return 1
	}
	n := 0
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// cleanName は表示する名前から制御文字を除く（端末に escape を出させない）。
func cleanName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

func sortSkipped(s []SkippedFile) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Name != s[j].Name {
			return s[i].Name < s[j].Name
		}
		return s[i].Reason < s[j].Reason
	})
}
