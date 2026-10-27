package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/client/agentdirs"
	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/transcript"
)

// looptrack session transcript push / pull（DESIGN.md §9-8）を実サーバに対して動かす。
// 2 つの合成の端末（別の HOME・別の CLAUDE_CONFIG_DIR / CODEX_HOME・別の git の作業ツリーのルート）を一時ディレクトリに作り、
// CLI はこのプロセスの中で cli.SessionMain として呼ぶ。サーバへの要求は数える中継（trProxy）を通す。
// 会話記録の中身はすべて合成の行（秘密は trSecret の偽物）。

const (
	trCLISID  = "00000000-0000-4000-8000-0000000000c1"
	trCodexID = "019a0000-0000-7000-8000-0000000000d1"
	// trSecretValue は trSecret のうち、出力にも置いたファイルにも出てはいけない部分
	trSecretValue = "Synthetic0Value9"
)

// trMachine は合成の端末 1 つ。
type trMachine struct {
	home, claude, codex, root string
	vars                      map[string]string
}

// newTrMachine は端末を作る（root は .git を置いた作業ツリーのルート。realpath にそろえる）。
func newTrMachine(t *testing.T, a *transcriptAPIEnv, apiURL, label string) *trMachine {
	t.Helper()
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	m := &trMachine{home: real(t.TempDir()), root: real(t.TempDir())}
	m.claude = filepath.Join(m.home, "cfg-claude")
	m.codex = filepath.Join(m.home, "cfg-codex")
	if err := os.MkdirAll(filepath.Join(m.root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.vars = map[string]string{"LOOPTRACK_API_URL": apiURL, "LOOPTRACK_PROJECT": a.pr.Slug, "LOOPTRACK_TOKEN": a.owner.token,
		"LOOPTRACK_LANG": "ja", "HOME": m.home, "USERPROFILE": m.home, "XDG_CONFIG_HOME": filepath.Join(m.home, ".config"),
		"APPDATA": filepath.Join(m.home, "AppData"), "CLAUDE_PROJECT_DIR": m.root, "CLAUDE_CONFIG_DIR": m.claude, "CODEX_HOME": m.codex,
		"LOOPTRACK_MACHINE_LABEL": label}
	return m
}

func (m *trMachine) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.SessionMain(append([]string{"transcript"}, args...), cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb},
		clienv.FromMap(m.vars))
	return code, out.String(), errb.String()
}

// sessionDir は Claude Code の記録のディレクトリ（この端末のルートの slug）。
func (m *trMachine) sessionDir() string {
	return filepath.Join(m.claude, "projects", agentdirs.ClaudeSlug(m.root))
}

// trProxy はサーバへの要求を記録して通す中継。
type trProxy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []trSeen
}

type trSeen struct {
	method, path, file, offset string
}

func newTrProxy(t *testing.T, a *transcriptAPIEnv) *trProxy {
	p := &trProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.reqs = append(p.reqs, trSeen{r.Method, r.URL.Path, r.Header.Get(transcriptFileHeader), r.Header.Get(transcriptOffsetHeader)})
		p.mu.Unlock()
		a.srv.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *trProxy) take() []trSeen {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.reqs
	p.reqs = nil
	return out
}

func chunkPosts(reqs []trSeen) []trSeen {
	var out []trSeen
	for _, r := range reqs {
		if r.method == "POST" && strings.HasSuffix(r.path, "/chunks") {
			out = append(out, r)
		}
	}
	return out
}

func jsonLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// writeClaudeSession は端末 m に合成の Claude Code の記録（本体・サブエージェント・付属の json・tool-results）を置く。本体のパスを返す。
func writeClaudeSession(t *testing.T, m *trMachine) string {
	t.Helper()
	dir := filepath.Join(m.claude, "projects", agentdirs.ClaudeSlug(m.root))
	file := filepath.Join(m.root, "internal", "x.go")
	main := jsonLine(t, map[string]any{"type": "user", "sessionId": trCLISID, "cwd": m.root, "version": "2.1.0",
		"message": map[string]any{"role": "user", "content": "x.go を直して"}}) +
		jsonLine(t, map[string]any{"type": "assistant", "sessionId": trCLISID, "cwd": m.root, "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "tool_use", "id": "tu1", "name": "Edit", "input": map[string]any{"file_path": file, "old_string": "a", "new_string": "b"}}}}}) +
		jsonLine(t, map[string]any{"type": "user", "sessionId": trCLISID, "cwd": m.root, "message": map[string]any{"role": "user",
			"content": "設定は " + trSecret + " にある。メモは " + filepath.Join(m.home, "notes.md") + " にある"}}) +
		jsonLine(t, map[string]any{"type": "user", "sessionId": trCLISID, "cwd": m.root,
			"toolUseResult": map[string]any{"persistedOutputPath": filepath.Join(dir, trCLISID, "tool-results", "t1.txt")}})
	files := map[string]string{
		trCLISID + ".jsonl": main,
		filepath.Join(trCLISID, "subagents", "agent-a1.jsonl"): jsonLine(t, map[string]any{"type": "user", "sessionId": trCLISID, "isSidechain": true,
			"cwd": m.root, "message": map[string]any{"role": "user", "content": "調べて"}}),
		filepath.Join(trCLISID, "subagents", "agent-a1.meta.json"): `{"agentType":"general"}` + "\n",
		filepath.Join(trCLISID, "tool-results", "t1.txt"):          "出力の 1 行目\n2 行目\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, trCLISID+".jsonl")
}

// noValues は出力に秘密の値・端末のホーム・ルートが出ていないことを確かめる。
func noValues(t *testing.T, what, out string, m *trMachine) {
	t.Helper()
	for _, bad := range []string{trSecretValue, m.home, m.root} {
		if strings.Contains(out, bad) {
			t.Errorf("%s に値（%q）が出ています:\n%s", what, bad, out)
		}
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// 受け入れ条件: 2 回目の push は続きのチャンクだけを送る・元のファイルが短くなったら送らず件数で知らせる・--dry-run は要求を 1 本も送らない・
// 出力とエラーに値（パス・秘密）が出ない。
func TestTranscriptCLIPushIncremental(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	p := newTrProxy(t, a)
	m := newTrMachine(t, a, p.srv.URL+"/im", "box-a")
	main := writeClaudeSession(t, m)

	// --dry-run: 件数だけで、要求は 0 本
	code, out, errOut := m.run(t, "push", trCLISID, "--dry-run")
	if code != 0 || len(p.take()) != 0 {
		t.Fatalf("dry-run: exit %d・要求あり\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "マスクした値: 1") || !strings.Contains(out, "送る 4") {
		t.Errorf("dry-run の件数が違います:\n%s", out)
	}
	noValues(t, "dry-run の出力", out+errOut, m)
	if n := a.count("SELECT COUNT(*) FROM transcripts"); n != 0 {
		t.Fatalf("dry-run で記録が %d 件できています", n)
	}

	// 1 回目: 登録 + 4 ファイルのチャンク
	code, out, errOut = m.run(t, "push", trCLISID)
	reqs := p.take()
	if code != 0 {
		t.Fatalf("push: exit %d\n%s%s", code, out, errOut)
	}
	if len(reqs) == 0 || reqs[0].method != "POST" || !strings.HasSuffix(reqs[0].path, "/projects/"+a.pr.Slug+"/transcripts") {
		t.Fatalf("最初の要求が登録ではありません: %+v", reqs)
	}
	if posts := chunkPosts(reqs); len(posts) != 4 {
		t.Fatalf("チャンクの追記が %d 本（4 本のはず）: %+v", len(posts), reqs)
	}
	noValues(t, "push の出力", out+errOut, m)
	before, err := os.Stat(main)
	if err != nil {
		t.Fatal(err)
	}
	// サーバに置いた本体にも値が無い（変換を通った）
	_, _, raw := a.req(a.owner, "GET", "/transcripts/1/chunks/1", nil)
	if zr, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
		plain, _ := io.ReadAll(zr)
		if bytes.Contains(plain, []byte(trSecretValue)) || bytes.Contains(plain, []byte(m.root)) || !bytes.Contains(plain, []byte(":ROOT")) {
			t.Errorf("サーバの本体に値が残っている・プレースホルダが無い:\n%s", plain)
		}
	} else {
		t.Fatalf("前提が崩れています: 本体が gzip ではありません: %v", err)
	}

	// 2 回目: 本体に 1 行足すと、本体の続きのチャンク 1 本だけ（位置は 1 回目の長さ）
	f, err := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(jsonLine(t, map[string]any{"type": "user", "sessionId": trCLISID, "cwd": m.root, "message": map[string]any{"role": "user", "content": "続き"}}))
	f.Close()
	code, out, errOut = m.run(t, "push", trCLISID)
	posts := chunkPosts(p.take())
	if code != 0 || len(posts) != 1 || posts[0].file != trCLISID+".jsonl" || posts[0].offset != strconv.FormatInt(before.Size(), 10) {
		t.Fatalf("2 回目: exit %d・追記 %+v（本体の続き 1 本・位置 %d のはず）\n%s%s", code, posts, before.Size(), out, errOut)
	}
	// 3 回目: 何も変わっていなければ追記は 0 本
	code, out, _ = m.run(t, "push", trCLISID)
	if posts := chunkPosts(p.take()); code != 0 || len(posts) != 0 || !strings.Contains(out, "送る続きはありません") {
		t.Fatalf("3 回目: exit %d・追記 %d 本\n%s", code, len(posts), out)
	}
	// 元が短くなった: 送らず、件数で知らせて終了コード 1
	if err := os.Truncate(main, before.Size()-10); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = m.run(t, "push", trCLISID)
	if posts := chunkPosts(p.take()); code != 1 || len(posts) != 0 || !strings.Contains(errOut, "1 個あるので、その続きは送っていません") ||
		!strings.Contains(out, "送る続きはありません") {
		t.Fatalf("短くなった: exit %d・追記 %d 本\n%s%s", code, len(posts), out, errOut)
	}
	noValues(t, "短くなったときの出力", out+errOut, m)
	if n := a.count("SELECT COUNT(*) FROM transcript_chunks"); n != 5 {
		t.Fatalf("チャンクが %d 本（5 本のはず）", n)
	}
}

// 変換が止めた記録（キーに秘密）は、要求を 1 本も送らずに件数と理由だけを出して終了コード 1。値は出ない。
func TestTranscriptCLIPushRejectNoValue(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	p := newTrProxy(t, a)
	m := newTrMachine(t, a, p.srv.URL+"/im", "box-a")
	main := writeClaudeSession(t, m)
	line := `{"type":"user","sessionId":"` + trCLISID + `","` + trSecret + `":1}` + "\n"
	f, _ := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line)
	f.Close()
	for _, args := range [][]string{{"push", trCLISID}, {"push", trCLISID, "--dry-run"}} {
		code, out, errOut := m.run(t, args...)
		if code != 1 || len(p.take()) != 0 {
			t.Fatalf("%v: exit %d（1 のはず）・要求あり\n%s%s", args, code, out, errOut)
		}
		if !strings.Contains(errOut, "キーに秘密らしい文字列がある行が 1 件") || !strings.Contains(errOut, trCLISID+".jsonl") {
			t.Errorf("%v: 理由（件数とファイル）が出ていません:\n%s", args, errOut)
		}
		noValues(t, strings.Join(args, " "), out+errOut, m)
	}
}

// list は自分の記録を出し、purge は本体を消す。消去の後の push は最初（位置 0）から送り直す（送った長さは最後の消去より後のチャンクから数える）。
func TestTranscriptCLIListPurgeResend(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	p := newTrProxy(t, a)
	m := newTrMachine(t, a, p.srv.URL+"/im", "box-a")
	writeClaudeSession(t, m)
	if code, out, errOut := m.run(t, "push", trCLISID); code != 0 {
		t.Fatalf("push: exit %d\n%s%s", code, out, errOut)
	}
	code, out, _ := m.run(t, "list")
	if code != 0 || !strings.Contains(out, trCLISID) || !strings.Contains(out, "box-a") || strings.Contains(out, "消去済み") {
		t.Fatalf("list: exit %d\n%s", code, out)
	}
	if code, out, errOut := m.run(t, "purge", "1", "--reason", "test"); code != 0 || !strings.Contains(out, "チャンク 4 個") {
		t.Fatalf("purge: exit %d\n%s%s", code, out, errOut)
	}
	if code, out, _ := m.run(t, "list"); code != 0 || !strings.Contains(out, "消去済み") {
		t.Fatalf("消去の後の list: exit %d\n%s", code, out)
	}
	p.take()
	code, out, errOut := m.run(t, "push", trCLISID)
	posts := chunkPosts(p.take())
	if code != 0 || len(posts) != 4 {
		t.Fatalf("消去の後の push: exit %d・追記 %d 本（4 本のはず）\n%s%s", code, len(posts), out, errOut)
	}
	for _, q := range posts {
		if q.offset != "0" {
			t.Errorf("消去の後の追記の位置 %s（0 のはず）: %+v", q.offset, q)
		}
	}
	// 他人の記録は無いものと同じ（404 の文面が出て終了コード 1）
	other := newTrMachine(t, a, p.srv.URL+"/im", "box-x")
	other.vars["LOOPTRACK_TOKEN"] = a.peer.token
	if code, _, errOut := other.run(t, "purge", "1"); code != 1 || errOut == "" {
		t.Fatalf("他人の purge: exit %d\n%s", code, errOut)
	}
}

// claudeExpected は、端末 a の本体を送る前の変換にかけ、端末 b の値で戻し、セッション ID を newID にしたもの（pull が置くはずのバイト列）。
func claudeExpected(t *testing.T, src []byte, kind transcript.Kind, from, to *trMachine, newID string) []byte {
	t.Helper()
	res, err := transcript.Convert(src, transcript.Options{Agent: transcript.AgentClaude, Kind: kind, Hints: transcript.Hints{
		Roots: []string{from.root}, SessionDir: from.sessionDir(), VendorDir: from.claude, Home: from.home}})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, c := range res.Chunks {
		got, err := transcript.Restore(c.Data, kind, c.Nonce, transcript.Target{Root: to.root,
			SessionDir: filepath.Join(to.claude, "projects", agentdirs.ClaudeSlug(to.root)), VendorDir: to.claude, Home: to.home})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(got)
	}
	if kind == transcript.KindText {
		return b.Bytes()
	}
	return bytes.ReplaceAll(b.Bytes(), []byte(trCLISID), []byte(newID))
}

// 取り込む端末の設定の置き場が ' を含む（利用者名が O'Brien の Windows のホームなど）とき、再開のコマンドは
// Windows では新しいセッション ID（PowerShell と Git Bash のどちらでも引用が要らない）、ほかの OS では
// ' を '"'"' にした引用のパスになる。2 回目の pull（置き済み）も同じ 1 行を出す。
func TestTranscriptCLIPullResumeQuote(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	p := newTrProxy(t, a)
	from := newTrMachine(t, a, p.srv.URL+"/im", "box-a")
	writeClaudeSession(t, from)
	if code, out, errOut := from.run(t, "push", trCLISID); code != 0 {
		t.Fatalf("push: exit %d\n%s%s", code, out, errOut)
	}
	to := newTrMachine(t, a, p.srv.URL+"/im", "box-b")
	to.claude = filepath.Join(to.home, "O'Brien", "cfg-claude")
	to.vars["CLAUDE_CONFIG_DIR"] = to.claude
	for i := 1; i <= 2; i++ {
		code, out, errOut := to.run(t, "pull", "--print-resume", "1")
		if code != 0 {
			t.Fatalf("pull %d 回目: exit %d\n%s%s", i, code, out, errOut)
		}
		entries, err := filepath.Glob(filepath.Join(to.sessionDir(), "*.jsonl"))
		if err != nil || len(entries) != 1 {
			t.Fatalf("置いた本体: %v %v（1 つのはず）\n%s%s", entries, err, out, errOut)
		}
		placed := entries[0]
		if !strings.Contains(placed, "'") {
			t.Fatalf("前提が崩れています: 置いた本体のパスに ' がありません: %s", placed)
		}
		newID := strings.TrimSuffix(filepath.Base(placed), ".jsonl")
		want := "claude --resume '" + strings.ReplaceAll(placed, "'", `'"'"'`) + "' --fork-session\n"
		if runtime.GOOS == "windows" {
			want = "claude --resume " + newID + " --fork-session\n"
		}
		if out != want {
			t.Errorf("pull %d 回目の再開のコマンド:\n%q\n期待:\n%q", i, out, want)
		}
	}
}

// 受け入れ条件: 別の CLAUDE_CONFIG_DIR と別の git のルートの 2 つの端末で push → pull すると、置いた本体の各行の cwd と
// tool_use の input.file_path が取り込む端末のルートを指す。同じ sessionId のファイルが別の slug にあると置かずに止まり、
// ファイルの数が増えない。2 回目の pull は何も変えない。--print-resume の標準出力はコマンドの 1 行だけ。消去済みは置かずに終了コード 1。
func TestTranscriptCLIPullAcrossMachines(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	p := newTrProxy(t, a)
	from := newTrMachine(t, a, p.srv.URL+"/im", "box-a")
	main := writeClaudeSession(t, from)
	if code, out, errOut := from.run(t, "push", trCLISID); code != 0 {
		t.Fatalf("push: exit %d\n%s%s", code, out, errOut)
	}
	to := newTrMachine(t, a, p.srv.URL+"/im", "box-b")
	code, out, errOut := to.run(t, "pull", "1")
	if code != 0 {
		t.Fatalf("pull: exit %d\n%s%s", code, out, errOut)
	}
	entries, err := filepath.Glob(filepath.Join(to.sessionDir(), "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("置いた本体: %v %v（1 つのはず）\n%s%s", entries, err, out, errOut)
	}
	placed := entries[0]
	newID := strings.TrimSuffix(filepath.Base(placed), ".jsonl")
	if newID == trCLISID || len(newID) != 36 {
		t.Fatalf("新しいセッション ID になっていません: %s", newID)
	}
	// CLI はパスを shellQuote で囲む。Windows のパスは \ を含むので ' で囲まれる（中の ' は '"'"'）。
	// POSIX の一時ディレクトリは引用の要らない文字だけなので、そのまま
	quoted := placed
	if runtime.GOOS == "windows" {
		quoted = "'" + strings.ReplaceAll(placed, "'", `'"'"'`) + "'"
	}
	wantResume := "claude --resume " + quoted + " --fork-session"
	if !strings.Contains(out, wantResume) {
		t.Errorf("再開のコマンド（%s）が出ていません:\n%s", wantResume, out)
	}
	// 各行の cwd・tool_use の file_path・sessionId・tool-results を指すパスが取り込む端末の値
	body, err := os.ReadFile(placed)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	lines, tools := 0, 0
	for sc.Scan() {
		var row struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			ToolUseResult struct {
				Persisted string `json:"persistedOutputPath"`
			} `json:"toolUseResult"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		lines++
		if row.Cwd != to.root || row.SessionID != newID {
			t.Errorf("%d 行目: cwd %q・sessionId %q（%q・%q のはず）", lines, row.Cwd, row.SessionID, to.root, newID)
		}
		var parts []struct {
			Type  string `json:"type"`
			Input struct {
				FilePath string `json:"file_path"`
			} `json:"input"`
		}
		if json.Unmarshal(row.Message.Content, &parts) == nil {
			for _, pt := range parts {
				if pt.Type == "tool_use" {
					tools++
					if pt.Input.FilePath != filepath.Join(to.root, "internal", "x.go") {
						t.Errorf("tool_use の file_path = %q（取り込む端末のルートの下のはず）", pt.Input.FilePath)
					}
				}
			}
		}
		if row.ToolUseResult.Persisted != "" && row.ToolUseResult.Persisted != filepath.Join(to.sessionDir(), newID, "tool-results", "t1.txt") {
			t.Errorf("persistedOutputPath = %q", row.ToolUseResult.Persisted)
		}
	}
	if lines != 4 || tools != 1 {
		t.Fatalf("前提が崩れています: 行 %d・tool_use %d（4・1 のはず）", lines, tools)
	}
	if bytes.Contains(body, []byte(from.root)) || bytes.Contains(body, []byte(trSecretValue)) {
		t.Errorf("置いた本体に送った端末のルートか秘密の値が残っています")
	}
	// 置いた各ファイルは、送る前の変換を取り込む端末の値で戻したものとバイト一致（セッション ID だけ新しい値）
	for rel, kind := range map[string]transcript.Kind{
		trCLISID + ".jsonl": transcript.KindJSONL,
		filepath.Join(trCLISID, "subagents", "agent-a1.jsonl"):     transcript.KindJSONL,
		filepath.Join(trCLISID, "subagents", "agent-a1.meta.json"): transcript.KindJSON,
		filepath.Join(trCLISID, "tool-results", "t1.txt"):          transcript.KindText,
	} {
		src, err := os.ReadFile(filepath.Join(filepath.Dir(main), rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(to.sessionDir(), strings.Replace(rel, trCLISID, newID, 1)))
		if err != nil {
			t.Fatalf("%s が置かれていません: %v", rel, err)
		}
		if want := claudeExpected(t, src, kind, from, to, newID); !bytes.Equal(got, want) {
			t.Errorf("%s が変換と戻しの結果と違います:\n got %q\nwant %q", rel, got, want)
		}
	}
	files := countFiles(t, to.claude)
	if files != 4 {
		t.Fatalf("置いたファイルが %d 個（4 個のはず）", files)
	}

	// 2 回目の pull は何も変えない（同じ中身なので再開のコマンドだけ）。--print-resume は標準出力がコマンドの 1 行だけ
	st1, _ := os.Stat(placed)
	code, out, errOut = to.run(t, "pull", "1", "--print-resume")
	st2, _ := os.Stat(placed)
	if code != 0 || out != wantResume+"\n" || !strings.Contains(errOut, "置き済み") {
		t.Fatalf("2 回目（--print-resume）: exit %d\nstdout %q\nstderr %s", code, out, errOut)
	}
	if countFiles(t, to.claude) != files || !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("2 回目の pull でファイルが変わりました")
	}

	// 同じ写し（新しい ID）が別の slug（別の作業ツリー）にあれば置かない。ファイルの数は増えない
	other := filepath.Join(to.root, "sub")
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	to.vars["CLAUDE_PROJECT_DIR"] = other
	code, _, errOut = to.run(t, "pull", "1")
	to.vars["CLAUDE_PROJECT_DIR"] = to.root
	if code != 1 || !strings.Contains(errOut, "別の作業ツリーの置き場にある") || countFiles(t, to.claude) != files {
		t.Fatalf("別の slug: exit %d・ファイル %d 個\n%s", code, countFiles(t, to.claude), errOut)
	}

	// 元のセッション ID のファイルがこの端末にある（送った端末自身・別の slug に同じ sessionId）と置かない
	code, _, errOut = from.run(t, "pull", "1")
	if code != 1 || !strings.Contains(errOut, "同じセッションの記録がこの端末にある") || countFiles(t, from.claude) != 4 {
		t.Fatalf("送った端末で pull: exit %d・ファイル %d 個\n%s", code, countFiles(t, from.claude), errOut)
	}
	third := newTrMachine(t, a, p.srv.URL+"/im", "box-c")
	dup := filepath.Join(third.claude, "projects", "-elsewhere", trCLISID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(dup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dup, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = third.run(t, "pull", "1")
	if code != 1 || !strings.Contains(errOut, dup) || countFiles(t, third.claude) != 1 {
		t.Fatalf("別の slug に同じ sessionId: exit %d・ファイル %d 個\n%s", code, countFiles(t, third.claude), errOut)
	}

	// --out は AI の置き場ではなく指定のディレクトリに書き、再開のコマンドを出さない
	outDir := filepath.Join(third.home, "inspect")
	code, out, errOut = third.run(t, "pull", "1", "--out", outDir)
	if code != 0 || strings.Contains(out, "--fork-session") || countFiles(t, outDir) != 4 || countFiles(t, third.claude) != 1 {
		t.Fatalf("--out: exit %d・書いた %d 個\n%s%s", code, countFiles(t, outDir), out, errOut)
	}

	// 消去済みの記録は置かずに「消去済み」で終了コード 1
	if code, out, errOut := from.run(t, "purge", "1", "--reason", "test"); code != 0 || !strings.Contains(out, "チャンク 4 個") {
		t.Fatalf("purge: exit %d\n%s%s", code, out, errOut)
	}
	fourth := newTrMachine(t, a, p.srv.URL+"/im", "box-d")
	code, _, errOut = fourth.run(t, "pull", "1")
	if code != 1 || !strings.Contains(errOut, "消去済み") || countFiles(t, fourth.home) != 0 {
		t.Fatalf("消去済み: exit %d・ファイル %d 個\n%s", code, countFiles(t, fourth.home), errOut)
	}
	// list は消去済みを示す
	if code, out, _ := from.run(t, "list"); code != 0 || !strings.Contains(out, trCLISID) || !strings.Contains(out, "消去済み") {
		t.Fatalf("list: exit %d\n%s", code, out)
	}
}

// OS の系統（Windows とそれ以外）が違う端末で作られた記録は置かない（v1 の対象外）。ファイルは増えない。
func TestTranscriptCLIPullCrossOS(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "linux"
	}
	body, _ := json.Marshal(map[string]string{"agent": "claude-code", "vendor_session_id": trCLISID, "origin_os": other})
	if code, _, b := a.req(a.owner, "POST", "/projects/"+a.pr.Slug+"/transcripts", body, "Content-Type", "application/json"); code != http.StatusCreated {
		t.Fatalf("登録: %d %s", code, b)
	}
	a.mustAppend(a.owner, 1, `"0"`, trCLISID+".jsonl", 0, []byte(trLine("hi")))
	m := newTrMachine(t, a, a.srv.URL+"/im", "box-b")
	code, _, errOut := m.run(t, "pull", "1")
	if code != 1 || !strings.Contains(errOut, "OS の系統") || countFiles(t, m.home) != 0 {
		t.Fatalf("OS の系統が違う: exit %d・ファイル %d 個\n%s", code, countFiles(t, m.home), errOut)
	}
	// 対照: 同じ記録を同じ系統の OS として登録し直した別の記録なら置ける（経路が生きている）
	body, _ = json.Marshal(map[string]string{"agent": "claude-code", "vendor_session_id": trSID2, "origin_os": runtime.GOOS})
	if code, _, b := a.req(a.owner, "POST", "/projects/"+a.pr.Slug+"/transcripts", body, "Content-Type", "application/json"); code != http.StatusCreated {
		t.Fatalf("登録: %d %s", code, b)
	}
	a.mustAppend(a.owner, 2, `"0"`, trSID2+".jsonl", 0, []byte(trLine("hi")))
	if code, out, errOut := m.run(t, "pull", "2"); code != 0 || countFiles(t, m.claude) != 1 {
		t.Fatalf("前提が崩れています: 同じ系統の記録を置けません: exit %d\n%s%s", code, out, errOut)
	}
}

// Codex（要実測: 置いた rollout を codex fork が見つけるかは利用者の端末で確かめる）。<CODEX_HOME>/sessions/<年>/<月>/<日>/ に
// 新しいスレッド ID の名前で置き、session_meta.payload.id と cwd が取り込む端末の値になり、再開のコマンドは codex fork <新しい ID>。
func TestTranscriptCLICodexRoundTrip(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	from := newTrMachine(t, a, a.srv.URL+"/im", "box-a")
	name := "rollout-2026-10-25T10-00-00-" + trCodexID + ".jsonl"
	src := filepath.Join(from.codex, "sessions", "2026", "10", "25", name)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	content := jsonLine(t, map[string]any{"type": "session_meta", "payload": map[string]any{"id": trCodexID, "cwd": from.root, "cli_version": "0.50.0"}}) +
		jsonLine(t, map[string]any{"type": "turn_context", "payload": map[string]any{"cwd": from.root}})
	if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := from.run(t, "push", trCodexID, "--agent", "codex"); code != 0 {
		t.Fatalf("push: exit %d\n%s%s", code, out, errOut)
	}
	to := newTrMachine(t, a, a.srv.URL+"/im", "box-b")
	code, out, errOut := to.run(t, "pull", "1")
	if code != 0 {
		t.Fatalf("pull: exit %d\n%s%s", code, out, errOut)
	}
	hits, _ := filepath.Glob(filepath.Join(to.codex, "sessions", "2026", "10", "25", "rollout-2026-10-25T10-00-00-*.jsonl"))
	if len(hits) != 1 || strings.Contains(hits[0], trCodexID) {
		t.Fatalf("置いた rollout: %v", hits)
	}
	newID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(hits[0]), "rollout-2026-10-25T10-00-00-"), ".jsonl")
	if !strings.Contains(out, "codex fork "+newID) {
		t.Errorf("再開のコマンドが出ていません:\n%s", out)
	}
	b, _ := os.ReadFile(hits[0])
	var meta struct {
		Payload struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	first, _, _ := bytes.Cut(b, []byte("\n"))
	if err := json.Unmarshal(first, &meta); err != nil || meta.Payload.ID != newID || meta.Payload.Cwd != to.root {
		t.Fatalf("session_meta: %+v %v（id %s・cwd %s のはず）", meta, err, newID, to.root)
	}
	if bytes.Contains(b, []byte(from.root)) || bytes.Contains(b, []byte(trCodexID)) {
		t.Errorf("送った端末のルートか元のスレッド ID が残っています:\n%s", b)
	}
}

// 合成の材料（internal/client/transcript/testdata。実際の記録の値を置き換えたもの）を push → pull すると、置いた各ファイルは
// 送る前の変換を取り込む端末の値で戻したものとバイト一致する（セッション ID だけ新しい値）。チャンクを複数に分けても同じ。
func TestTranscriptCLIPullFixtureBytes(t *testing.T) {
	const fixtureSID = "00000000-0000-4000-8000-000000000001"
	srcDir := filepath.Join("..", "client", "transcript", "testdata", "claude")
	rels := []string{fixtureSID + ".jsonl", filepath.Join(fixtureSID, "subagents", "agent-s002251.jsonl")}
	a := newTranscriptAPIEnv(t)
	a.setLimit("transcript_max_chunk_bytes", 128<<10) // 本体を複数のチャンクに分ける
	from := newTrMachine(t, a, a.srv.URL+"/im", "box-a")
	dir := from.sessionDir()
	srcs := map[string][]byte{}
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(srcDir, rel))
		if err != nil {
			t.Fatalf("前提が崩れています: 材料がありません: %v", err)
		}
		srcs[rel] = b
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, errOut := from.run(t, "push", fixtureSID); code != 0 {
		t.Fatalf("push: exit %d\n%s%s", code, out, errOut)
	}
	if n := a.count("SELECT COUNT(*) FROM transcript_chunks"); n < 3 {
		t.Fatalf("前提が崩れています: チャンクが %d 本（本体を分けていない）", n)
	}
	to := newTrMachine(t, a, a.srv.URL+"/im", "box-b")
	if code, out, errOut := to.run(t, "pull", "1"); code != 0 {
		t.Fatalf("pull: exit %d\n%s%s", code, out, errOut)
	}
	hits, _ := filepath.Glob(filepath.Join(to.sessionDir(), "*.jsonl"))
	if len(hits) != 1 {
		t.Fatalf("置いた本体: %v", hits)
	}
	newID := strings.TrimSuffix(filepath.Base(hits[0]), ".jsonl")
	for _, rel := range rels {
		got, err := os.ReadFile(filepath.Join(to.sessionDir(), strings.Replace(rel, fixtureSID, newID, 1)))
		if err != nil {
			t.Fatal(err)
		}
		want := bytes.ReplaceAll(claudeExpected(t, srcs[rel], transcript.KindJSONL, from, to, newID), []byte(fixtureSID), []byte(newID))
		if !bytes.Equal(got, want) {
			t.Errorf("%s が変換と戻しの結果と違います（%d / %d バイト）", rel, len(got), len(want))
		}
		if bytes.Contains(got, []byte(fixtureSID)) {
			t.Errorf("%s に元のセッション ID が残っています", rel)
		}
	}
}
