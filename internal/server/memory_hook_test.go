package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	clientenv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/store"
)

// kit の引き継ぎの置き場 server を、本物のサーバと hook・CLI の入口で通す。別のディレクトリは別の端末を模す。

type memHookBox struct {
	t    *testing.T
	vars map[string]string
}

func newMemHookEnv(t *testing.T) (*env, string, string) {
	t.Helper()
	return newMemHookEnvSkew(t, 0)
}

// newMemHookEnvSkew はサーバの時計を skew だけずらす。
func newMemHookEnvSkew(t *testing.T, skew time.Duration) (*env, string, string) {
	t.Helper()
	e := newEnvWith(t, func(cfg *Config) {
		cfg.AllowNoAdmin = true
		cfg.Now = func() time.Time { return time.Now().Add(skew) }
	})
	pr := e.project("memhook")
	u := e.user("memhook-ed", "memhook-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	return e, pr.Slug, e.apiAs(u).token
}

func (e *env) memHookBox(slug, token string) *memHookBox {
	e.t.Helper()
	dir, home := e.t.TempDir(), e.t.TempDir()
	return &memHookBox{t: e.t, vars: map[string]string{"HOME": home, "USERPROFILE": home, "APPDATA": filepath.Join(home, ".config"), "CLAUDE_PROJECT_DIR": dir, "LOOPTRACK_LANG": "ja",
		"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": slug, "LOOPTRACK_TOKEN": token,
		"LOOPTRACK_LOOP_HANDOFF_BACKEND": "server", "LOOPTRACK_MACHINE_LABEL": "box"}}
}

func (b *memHookBox) loopEnv() *loop.Env {
	return &loop.Env{Getenv: func(k string) string { return b.vars[k] }, Environ: func() []string { return nil },
		Getwd: func() string { return b.vars["CLAUDE_PROJECT_DIR"] }}
}

// hook は hook を 1 回動かして出力の JSON を返す（何も出なければ空）。
func (b *memHookBox) hook(name, event, sid string, extra map[string]any) map[string]any {
	b.t.Helper()
	in := map[string]any{"session_id": sid, "cwd": b.vars["CLAUDE_PROJECT_DIR"], "hook_event_name": event}
	for k, v := range extra {
		in[k] = v
	}
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	if code := loop.Main(context.Background(), name, []string{"--agent", "claude-code"}, bytes.NewReader(raw), &out, &errb, b.loopEnv()); code != 0 {
		b.t.Fatalf("%s: exit %d: %s", name, code, errb.String())
	}
	got := map[string]any{}
	if s := strings.TrimSpace(out.String()); s != "" {
		if err := json.Unmarshal([]byte(s), &got); err != nil {
			b.t.Fatalf("%s の出力を読めない: %v: %s", name, err, s)
		}
	}
	return got
}

func (b *memHookBox) sessionStart(sid string) string {
	b.t.Helper()
	g := b.hook("session-start-memories", "SessionStart", sid, nil)
	spec, _ := g["hookSpecificOutput"].(map[string]any)
	s, _ := spec["additionalContext"].(string)
	return s
}

// finish は作業の完了（commit）を積む。
func (b *memHookBox) finish(sid string) {
	b.t.Helper()
	g := b.hook("post-work-complete-handoff-mark", "PostToolUse", sid, map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "git commit -m x"}, "tool_response": "[main abc1234] x\n 1 file changed"})
	spec, _ := g["hookSpecificOutput"].(map[string]any)
	if s, _ := spec["additionalContext"].(string); !strings.Contains(s, "LOOPTRACK_LOOP_HANDOFF_BACKEND=server") {
		b.t.Fatalf("完了の検知に置き場の案内が無い: %v", g)
	}
}

func (b *memHookBox) handoff(body string, args ...string) (int, string, string) {
	b.t.Helper()
	var out, errb bytes.Buffer
	code := loop.Handoff(context.Background(), args, strings.NewReader(body), &out, &errb, b.loopEnv())
	return code, out.String(), errb.String()
}

func (b *memHookBox) memory(args ...string) (int, string, string) {
	b.t.Helper()
	var out, errb bytes.Buffer
	code := cli.MemoryMain(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, clientenv.FromMap(b.vars))
	return code, out.String(), errb.String()
}

func TestServerHandoffAcrossMachines(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a, other := e.memHookBox(slug, token), e.memHookBox(slug, token)

	// 対照: 書く前は別の端末の SessionStart に引き継ぎが入らない
	if s := other.sessionStart("sess-b"); !strings.Contains(s, "サーバに引き継ぎがまだありません") || strings.Contains(s, "再開の手がかり") {
		t.Fatalf("書く前の注入: %q", s)
	}

	// 鮮度ガード: 引き継ぎが無いと差し戻す
	a.finish("sess-a")
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); g["decision"] != "block" || !strings.Contains(g["reason"].(string), "handoff/main") {
		t.Fatalf("引き継ぎが無いのに通した: %v", g)
	}
	// ほかのセッションの書き込みでは通らない
	if code, _, msg := other.handoff("別の端末の話", "append", "--title", "別", "--session", "sess-b"); code != 0 {
		t.Fatalf("別のセッションの追記: exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); g["decision"] != "block" || !strings.Contains(g["reason"].(string), "このセッションが書いた版がありません") {
		t.Fatalf("別のセッションの書き込みで通った: %v", g)
	}
	// 自分が書いたら通し、マーカーを畳む
	if code, out, msg := a.handoff("再開の手がかり", "append", "--title", "現在地", "--session", "sess-a"); code != 0 || !strings.Contains(out, "版 2") {
		t.Fatalf("追記: exit %d %s %s", code, out, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("書いた後も止めた: %v", g)
	}
	marks, _ := filepath.Glob(filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "handoff-pending.d", "*"))
	if len(marks) != 0 {
		t.Fatalf("マーカーが畳まれていない: %v", marks)
	}

	// 決定と注意は要約だけが入る（本文は入らない）
	if code, _, msg := a.memoryWrite("decision", "use-mysql", "MySQL を使う\n\n本文の詳細"); code != 0 {
		t.Fatalf("決定の追記: exit %d %s", code, msg)
	}
	if code, _, msg := a.memoryWrite("caveat", "slow-ci", "CI は遅い"); code != 0 {
		t.Fatalf("注意の追記: exit %d %s", code, msg)
	}
	s := other.sessionStart("sess-c")
	for _, want := range []string{"再開の手がかり", "別の端末の話", "decision/use-mysql — MySQL を使う", "caveat/slow-ci — CI は遅い", "版 2"} {
		if !strings.Contains(s, want) {
			t.Errorf("別の端末の注入に「%s」が無い:\n%s", want, s)
		}
	}
	if strings.Contains(s, "本文の詳細") {
		t.Errorf("決定の本文まで入れた:\n%s", s)
	}

	// 上限を超えたら file の置き場と同じく末尾の節から入れ、省いたことを断る
	if code, _, msg := a.handoff(strings.Repeat("古い行\n", 500), "append", "--title", "長い節", "--session", "sess-a"); code != 0 {
		t.Fatalf("長い節: exit %d %s", code, msg)
	}
	if code, _, msg := a.handoff("新しい行", "append", "--title", "最後の節", "--session", "sess-a"); code != 0 {
		t.Fatalf("最後の節: exit %d %s", code, msg)
	}
	other.vars["LOOPTRACK_LOOP_MEMORIES_MAX_CHARS"] = "900"
	s = other.sessionStart("sess-c")
	if len([]rune(s)) > 900 || !strings.Contains(s, "末尾（新しい節）だけ") || !strings.Contains(s, "## 最後の節") || !strings.Contains(s, "## 現在地") {
		t.Errorf("上限の切り出し（%d 字）:\n%s", len([]rune(s)), s)
	}
	delete(other.vars, "LOOPTRACK_LOOP_MEMORIES_MAX_CHARS")

	// compact は読んだ版を渡す。古ければ何も書かない
	if code, _, msg := a.handoff("## 圧縮\n短く", "compact", "--version", "2", "--session", "sess-a"); code != 3 || !strings.Contains(msg, "版") {
		t.Fatalf("古い版の compact: exit %d %s", code, msg)
	}
	_, shown, _ := a.memory("show", "handoff")
	if !strings.Contains(shown, "再開の手がかり") {
		t.Fatalf("拒んだ compact が書いた: %s", shown)
	}
	if code, _, msg := a.handoff("## 圧縮", "compact", "--sha", strings.Repeat("0", 64)); code != 2 || !strings.Contains(msg, "--version") {
		t.Fatalf("--sha の compact: exit %d %s", code, msg)
	}
	if code, out, msg := a.handoff("## 圧縮\n短く", "compact", "--version", "4", "--session", "sess-a"); code != 0 || !strings.Contains(out, "版 5") {
		t.Fatalf("今の版の compact: exit %d %s %s", code, out, msg)
	}
	_, shown, _ = a.memory("show", "handoff")
	if !strings.Contains(shown, "## 圧縮") || strings.Contains(shown, "再開の手がかり") {
		t.Fatalf("compact の本文: %s", shown)
	}
}

func (b *memHookBox) memoryWrite(kind, name, text string) (int, string, string) {
	b.t.Helper()
	var out, errb bytes.Buffer
	code := cli.MemoryMain([]string{"append", kind, name}, cli.IO{Stdin: strings.NewReader(text), Stdout: &out, Stderr: &errb}, clientenv.FromMap(b.vars))
	return code, out.String(), errb.String()
}

// サーバに届かないときは Stop を止めず、注意を 1 つ出し、マーカーは残す。つながったら判定が戻る。
func TestServerHandoffUnreachable(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	a.finish("sess-a")
	// 対照: 届けば差し戻す
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); g["decision"] != "block" {
		t.Fatalf("届くのに差し戻さない（前提が崩れています）: %v", g)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()
	live := a.vars["LOOPTRACK_API_URL"]
	a.vars["LOOPTRACK_API_URL"] = dead

	g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
	msg, _ := g["systemMessage"].(string)
	if _, blocked := g["decision"]; blocked || !strings.Contains(msg, "backend server") {
		t.Fatalf("届かないのに止めた、か注意が無い: %v", g)
	}
	if s := a.sessionStart("sess-a"); !strings.Contains(s, "サーバの記憶を読めませんでした") {
		t.Fatalf("SessionStart の注意: %q", s)
	}
	marks, _ := filepath.Glob(filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "handoff-pending.d", "*"))
	if len(marks) != 1 {
		t.Fatalf("マーカーを畳んだ: %v", marks)
	}
	a.vars["LOOPTRACK_API_URL"] = live
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); g["decision"] != "block" {
		t.Fatalf("つながった後に判定が戻らない: %v", g)
	}
}

// 手元の記憶の取り込み: 要約の行・既にある名前・対象外・資格情報・上限。
func TestMemoryImport(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	dir := filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const fakeSecret = "password=Sup3rS3cretValue9"
	files := map[string]string{
		"handoff.md":           "# 引き継ぎ\n\n> 要約: 今の状況\n\n## 現在地\n進行中\n",
		"decision-db.md":       "# DB\n> 要約: MySQL を使う\n本文\n",
		"caveat-no-summary.md": "最初の行が要約\n本文\n",
		"caveat-exists.md":     "> 要約: 新しい本文\n",
		"decision-secret.md":   "> 要約: 秘密\n1 行目\n" + fakeSecret + "\n",
		"decision-large.md":    "> 要約: 大きい\n" + strings.Repeat("あ", 30000) + "\n",
		"decision-Bad_Name.md": "> 要約: 名前が違う\n",
		"feedback-x.md":        "> 要約: 対象外\n",
		"README.md":            "説明\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, msg := a.memoryWrite("caveat", "exists", "前からある本文"); code != 0 {
		t.Fatalf("準備: exit %d %s", code, msg)
	}
	code, out, errb := a.memory("import")
	if code != 1 {
		t.Fatalf("拒否があるのに exit %d: %s%s", code, out, errb)
	}
	if strings.Contains(out+errb, "Sup3rS3cretValue9") {
		t.Fatalf("資格情報の値を出した: %s%s", out, errb)
	}
	for _, want := range []string{"取り込みました: handoff.md → handoff/main", "取り込みました: decision-db.md → decision/db",
		"飛ばしました（既にあります）: caveat-exists.md → caveat/exists", "拒みました: decision-secret.md: 3 行目が資格情報の形です",
		"拒みました: decision-large.md", "拒みました: decision-Bad_Name.md", "対象外なので読みませんでした: feedback-x.md", "取り込み 3 件・飛ばした 1 件・拒否 3 件"} {
		if !strings.Contains(out, want) {
			t.Errorf("「%s」が無い:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("README を対象外に数えた:\n%s", out)
	}
	var got struct {
		Memories []memoryJSON `json:"memories"`
	}
	_, listed, _ := a.memory("list", "--json")
	if err := json.Unmarshal([]byte(listed), &got); err != nil {
		t.Fatal(err)
	}
	sum := map[string]string{}
	for _, m := range got.Memories {
		sum[m.Kind+"/"+m.Name] = m.Summary + "#" + strconv.FormatInt(m.Version, 10)
	}
	for k, want := range map[string]string{"handoff/main": "今の状況#1", "decision/db": "MySQL を使う#1", "caveat/no-summary": "最初の行が要約#1", "caveat/exists": "前からある本文#1"} {
		if sum[k] != want {
			t.Errorf("%s の要約: %q（期待 %q）", k, sum[k], want)
		}
	}
	if len(sum) != 4 {
		t.Errorf("拒んだものまで入った: %v", sum)
	}
	_, shown, _ := a.memory("show", "handoff")
	if !strings.HasPrefix(shown, "今の状況\n\n# 引き継ぎ\n") || strings.Contains(shown, "要約:") || !strings.Contains(shown, "## 現在地") {
		t.Errorf("取り込んだ本文: %q", shown)
	}
	// もう 1 回回しても上書きしない
	if _, out, _ = a.memory("import"); !strings.Contains(out, "飛ばしました（既にあります）: handoff.md") {
		t.Errorf("2 回目: %s", out)
	}
}

// 置き場が server なら、手元のファイルが消えても消失として差し戻さない（取り込んだ後に消す運用を止めないため）。
// 控えを更新しない順にする: server の Stop の後でも file の Stop が消失を見つけることが、server の Stop が控えに触らなかった証拠になる。
func TestServerHandoffIgnoresLocalRecordLoss(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	file := filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "memories", "handoff.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("## x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.vars["LOOPTRACK_LOOP_HANDOFF_BACKEND"] = "file"
	a.hook("stop-handoff-freshness", "Stop", "sess-a", nil) // 控えを作る
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	a.vars["LOOPTRACK_LOOP_HANDOFF_BACKEND"] = "server"
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("server の置き場で手元の消失を知らせた: %v", g)
	}
	// 対照: file の置き場は同じ状態を消失として知らせる
	a.vars["LOOPTRACK_LOOP_HANDOFF_BACKEND"] = "file"
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) == 0 {
		t.Fatal("file の置き場で消失を検知しない（前提が崩れています）")
	}
}

// --version はファイルの置き場では使えない（--sha の照合を黙って飛ばさない）。
func TestHandoffVersionFlagNeedsServer(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	a.vars["LOOPTRACK_LOOP_HANDOFF_BACKEND"] = "file"
	if code, _, msg := a.handoff("## x", "compact", "--version", "1", "--sha", strings.Repeat("0", 64)); code != 2 || !strings.Contains(msg, "--version") {
		t.Fatalf("exit %d %s", code, msg)
	}
}

// bash は Bash のツールの中のシェルを模す（hook とは別の環境を持てる）。
func (b *memHookBox) bash(extra map[string]string) *memHookBox {
	vars := map[string]string{}
	for k, v := range b.vars {
		vars[k] = v
	}
	for k, v := range extra {
		vars[k] = v
	}
	return &memHookBox{t: b.t, vars: vars}
}

// sawHandoff は Bash で走った handoff を PostToolUse に見せる。
func (b *memHookBox) sawHandoff(sid, output string) {
	b.t.Helper()
	b.hook("post-work-complete-handoff-mark", "PostToolUse", sid, map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "looptrack handoff append -"}, "tool_response": output})
}

func stopBlocked(g map[string]any) bool { return g["decision"] == "block" }

// シェルの中の ID と hook の ID が違う器（器の窓の ID・ID の無い器）でも、書いたら通る。
func TestServerHandoffShellSessionDiffers(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		block bool // 手元の記録が無いときに差し戻すか（ID が無い版は書き手を比べられないので、注意を出して通す）
	}{
		{"器の窓の ID だけ", map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "host-window-1"}, true},
		{"ID が取れない器", map[string]string{"AI_AGENT": "github_copilot_vscode_agent", "COPILOT_AGENT": "1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, slug, token := newMemHookEnv(t)
			a := e.memHookBox(slug, token)
			a.finish("sess-a")
			code, out, msg := a.bash(tc.env).handoff("再開の手がかり", "append", "--title", "現在地")
			if code != 0 {
				t.Fatalf("追記: exit %d %s", code, msg)
			}
			// 対照: 失敗の出力を見せても記録は残らない
			a.sawHandoff("sess-a", "エラー: 置き場が server のときは --file を使えません（書き先はサーバの handoff/main です）。")
			if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); stopBlocked(g) != tc.block {
				t.Fatalf("失敗の出力の判定が違う（前提が崩れています）: %v", g)
			} else if !tc.block && !strings.Contains(fmt.Sprint(g), "判定できない") {
				t.Fatalf("比べられない版の注意が無い: %v", g)
			}
			a.finish("sess-a") // 上の Stop が畳んだ分を積み直す
			a.sawHandoff("sess-a", out)
			if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
				t.Fatalf("書いたのに止めた: %v", g)
			}
		})
	}
	t.Run("hook の環境にも器の ID がある", func(t *testing.T) {
		e, slug, token := newMemHookEnv(t)
		a := e.memHookBox(slug, token).bash(map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "host-window-1"})
		a.finish("sess-a")
		if code, _, msg := a.handoff("手がかり", "append", "--title", "現在地"); code != 0 {
			t.Fatalf("追記: exit %d %s", code, msg)
		}
		if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
			t.Fatalf("書いたのに止めた: %v", g)
		}
	})
}

// 書けない者（非参加者・viewer）には書き直しを迫らない。
func TestServerHandoffCannotWrite(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	pr, err := store.ProjectBySlug(context.Background(), e.db, slug)
	if err != nil {
		t.Fatal(err)
	}
	viewer := e.user("memhook-vi", "memhook-password-2", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	stranger := e.user("memhook-st", "memhook-password-3", "member")
	for _, tc := range []struct {
		name, token, want string
		block             bool
	}{
		{"editor は差し戻す（対照）", token, "", true},
		{"viewer", e.apiAs(viewer).token, "書ける役割ではない", false},
		{"非参加者", e.apiAs(stranger).token, "参加していない", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := e.memHookBox(slug, tc.token)
			a.finish("sess-a")
			g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
			if stopBlocked(g) != tc.block {
				t.Fatalf("差し戻し %v のはず: %v", tc.block, g)
			}
			if msg, _ := g["systemMessage"].(string); tc.want != "" && !strings.Contains(msg, tc.want) {
				t.Fatalf("注意に「%s」が無い: %v", tc.want, g)
			}
		})
	}
}

// サーバの時計が手元とずれていても、手元の記録で正しく判定する。記録が無いときだけサーバの時刻に落とし、ずれの可能性を示す。
func TestServerHandoffClockSkew(t *testing.T) {
	for _, skew := range []time.Duration{-60 * time.Second, 60 * time.Second} {
		t.Run(skew.String(), func(t *testing.T) {
			e, slug, token := newMemHookEnvSkew(t, skew)
			a := e.memHookBox(slug, token)
			// 完了の前に書いただけでは通らない（版に残る書いた時刻は手元の時計なので、サーバの時計に左右されない）
			if code, _, msg := a.handoff("前の話", "append", "--title", "前", "--session", "sess-a"); code != 0 {
				t.Fatalf("追記: exit %d %s", code, msg)
			}
			// 記録の時刻を完了より前にする（秒に丸めて比べるので、同じ秒だと通る）
			past := time.Now().Add(-5 * time.Second)
			rec := filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", ".looptrack-freshness", "handoff-written", "sess-a")
			if err := os.Chtimes(rec, past, past); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1100 * time.Millisecond)
			a.finish("sess-a")
			if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); !stopBlocked(g) {
				t.Fatalf("完了の前の書き込みで通した（%v）: %v", skew, g)
			}
			// 完了の後に書けば通る（サーバが遅れていても）
			if code, _, msg := a.handoff("後の話", "append", "--title", "後", "--session", "sess-a"); code != 0 {
				t.Fatalf("追記: exit %d %s", code, msg)
			}
			if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
				t.Fatalf("書いたのに止めた: %v", g)
			}
			// 手元の記録が無くても、版の書いた時刻が完了より前なら通らない。差し戻しの文面でずれの可能性を示す
			if err := os.RemoveAll(filepath.Dir(rec)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1100 * time.Millisecond)
			a.finish("sess-a")
			g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
			if !stopBlocked(g) || !strings.Contains(g["reason"].(string), "時計のずれ") {
				t.Fatalf("サーバの時刻の差し戻しに、ずれの案内が無い: %v", g)
			}
		})
	}
}

// セッション ID が分からないときは、手元の共有の記録か最新の版の時刻で見る。
func TestServerHandoffNoSessionID(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	if code, _, msg := a.memoryWrite("handoff", "main", "## 前\n"); code != 0 {
		t.Fatalf("準備: exit %d %s", code, msg)
	}
	time.Sleep(1100 * time.Millisecond) // 版の時刻を完了の秒より前にする
	a.hook("post-work-complete-handoff-mark", "PostToolUse", "", map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "git commit -m x"}, "tool_response": "[main abc1234] x\n 1 file changed"})
	if g := a.hook("stop-handoff-freshness", "Stop", "", nil); !stopBlocked(g) {
		t.Fatalf("完了より古い版で通した: %v", g)
	}
	if code, _, msg := a.memoryWrite("handoff", "main", "## 後\n"); code != 0 {
		t.Fatalf("追記: exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "", nil); len(g) != 0 {
		t.Fatalf("新しい版があるのに止めた: %v", g)
	}
	if a.exists(".claude", "handoff-pending") {
		t.Fatal("通したのにマーカーを畳んでいない（hook が途中で落ちて通っただけかもしれません）")
	}
}

func (b *memHookBox) exists(parts ...string) bool {
	_, err := os.Stat(filepath.Join(append([]string{b.vars["CLAUDE_PROJECT_DIR"]}, parts...)...))
	return err == nil
}

// 自分だけの handoff/main をプロジェクトの引き継ぎと取り違えない。
func TestServerHandoffPersonalIsNotProject(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	var out, errb bytes.Buffer
	code := cli.MemoryMain([]string{"append", "handoff", "main", "--personal"}, cli.IO{Stdin: strings.NewReader("個人の本文\n続き"), Stdout: &out, Stderr: &errb}, clientenv.FromMap(a.vars))
	if code != 0 {
		t.Fatalf("exit %d %s", code, errb.String())
	}
	s := a.sessionStart("sess-a")
	if !strings.Contains(s, "サーバに引き継ぎがまだありません") || !strings.Contains(s, "handoff/main (personal) — 個人の本文") || strings.Contains(s, "続き") {
		t.Fatalf("注入:\n%s", s)
	}
	a.finish("sess-a")
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); !stopBlocked(g) || !strings.Contains(g["reason"].(string), "まだありません") {
		t.Fatalf("個人の記憶で通した: %v", g)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// 本体と作業ツリーを作る。
func gitWorktree(t *testing.T) (mainDir, wt string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainDir, wt = filepath.Join(dir, "main"), filepath.Join(dir, "wt")
	if err := os.Mkdir(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainDir, "init", "-q")
	runGit(t, mainDir, "commit", "-q", "--allow-empty", "-m", "x")
	runGit(t, mainDir, "worktree", "add", "-q", wt)
	return mainDir, wt
}

// 取り残しの知らせは手元のファイルの話なので、server の置き場では出さない。
func TestServerHandoffNoStrayNotice(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	mainDir, wt := gitWorktree(t)
	stray := filepath.Join(wt, ".claude", "memories", "handoff.md")
	if err := os.MkdirAll(filepath.Dir(stray), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("## x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.vars["CLAUDE_PROJECT_DIR"] = mainDir
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("server の置き場で知らせた: %v", g)
	}
	a.vars["LOOPTRACK_LOOP_HANDOFF_BACKEND"] = "file"
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); !strings.Contains(fmt.Sprint(g), "handoff.md") {
		t.Fatalf("file の置き場で知らせない（前提が崩れています）: %v", g)
	}
}

// 作業ツリーの中で回しても本体の記憶を読む。要約の読み方の境目も確かめる。
func TestMemoryImportFromWorktree(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	mainDir, wt := gitWorktree(t)
	a.vars["CLAUDE_PROJECT_DIR"] = wt
	dir := filepath.Join(mainDir, ".claude", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"decision-bom.md":   "\ufeff> 要約: BOM つき\n本文\n",
		"decision-wide.md":  ">　要約: 全角空白\n本文\n",
		"decision-cr.md":    "a\r> 要約: CR の後\n",
		"decision-empty.md": "> 要約:\n本文の行\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errb := a.memory("import")
	if code != 0 || !strings.Contains(out, "取り込み 4 件") {
		t.Fatalf("exit %d %s%s", code, out, errb)
	}
	_, listed, _ := a.memory("list")
	for _, want := range []string{"BOM つき", "全角空白", "CR の後", "本文の行"} {
		if !strings.Contains(listed, want) {
			t.Errorf("要約「%s」が無い:\n%s", want, listed)
		}
	}
	if strings.Contains(listed, "要約:") || strings.Contains(listed, "\ufeff") {
		t.Errorf("要約の印か BOM が残った:\n%s", listed)
	}
}

// file の置き場の要約の読み方は以前のまま（全角空白・CR の行）。空の要約は要約なし。
func TestFileBackendSummaryReading(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	a.vars["LOOPTRACK_LOOP_HANDOFF_BACKEND"] = "file"
	dir := filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, c := range map[string]string{"handoff.md": "## x\n", "a.md": ">　要約: x\n", "b.md": "a\r> 要約: y\n", "c.md": "> 要約:\n"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := a.sessionStart("sess-a")
	for _, want := range []string{"a.md — x", "b.md — y", "c.md — （要約の行なし）"} {
		if !strings.Contains(s, want) {
			t.Errorf("「%s」が無い:\n%s", want, s)
		}
	}
}

// import の出力に、資格情報の形のファイル名を出さない。
func TestMemoryImportSecretFileName(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	dir := filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "password=Sup3rS3cretValue9.md"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feedback-ok.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, errb := a.memory("import")
	if strings.Contains(out+errb, "Sup3rS3cretValue9") || !strings.Contains(out, "feedback-ok.md") {
		t.Fatalf("%s%s", out, errb)
	}
}

func TestHandoffAppendRefusesVersion(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	if code, _, msg := a.handoff("x", "append", "--version", "99", "--session", "sess-a"); code != 2 || !strings.Contains(msg, "--version") {
		t.Fatalf("exit %d %s", code, msg)
	}
}

// memFlakyProxy は API の URL を変えずに届かなくする（退避の置き場の鍵は URL から決まるので、URL を変えると別の置き場になる）。
type memFlakyProxy struct {
	srv       *httptest.Server
	down      atomic.Bool
	lost      atomic.Bool // サーバまで届けて、応答だけを捨てる
	fail500   atomic.Bool
	delay     atomic.Int64 // 応答の前に待つ（ナノ秒）
	postDelay atomic.Int64 // POST の応答の前に待つ（ナノ秒）
	posts     atomic.Int64 // 追記の要求の数
}

func newMemFlakyProxy(t *testing.T, target string) *memFlakyProxy {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	p := &memFlakyProxy{}
	rp := httputil.NewSingleHostReverseProxy(u)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			p.posts.Add(1)
			if d := p.postDelay.Load(); d > 0 {
				select {
				case <-time.After(time.Duration(d)):
				case <-r.Context().Done():
				}
			}
		}
		if p.down.Load() {
			panic(http.ErrAbortHandler) // 接続を切る
		}
		if p.lost.Load() {
			rp.ServeHTTP(httptest.NewRecorder(), r)
			panic(http.ErrAbortHandler)
		}
		if p.fail500.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if d := p.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// サーバに届かない間の引き継ぎを失わず、別のセッションが送れて、2 回送らない。
func TestHandoffSpool(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	p := newMemFlakyProxy(t, e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = p.srv.URL + "/im"
	a.finish("sess-a")
	p.down.Store(true)
	code, out, msg := a.handoff("届かない間の話", "append", "--title", "退避", "--session", "sess-a")
	if code != 0 || !strings.Contains(out, "退避しました") {
		t.Fatalf("退避: exit %d %s %s", code, out, msg)
	}
	// 退避の置き場は作業ツリーの外
	if found, _ := filepath.Glob(filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "*spool*")); len(found) != 0 {
		t.Fatalf("作業ツリーに置いた: %v", found)
	}
	g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
	if m, _ := g["systemMessage"].(string); stopBlocked(g) || !strings.Contains(m, "未送信") {
		t.Fatalf("Stop の注意: %v", g)
	}
	marks, _ := filepath.Glob(filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "handoff-pending.d", "*"))
	if len(marks) != 1 {
		t.Fatalf("送る前にマーカーを畳んだ: %v", marks)
	}
	// つながったら別のセッションの SessionStart が送る。書き手は書いたセッションのまま
	p.down.Store(false)
	s := a.sessionStart("sess-other")
	if !strings.Contains(s, "届かない間の話") || strings.Contains(s, "未送信") {
		t.Fatalf("注入:\n%s", s)
	}
	var d struct {
		Versions []memoryVersionJSON `json:"versions"`
	}
	_, listed, _ := a.memory("show", "handoff", "--json")
	if err := json.Unmarshal([]byte(listed), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Versions) != 1 || d.Versions[0].SessionID != "sess-a" {
		t.Fatalf("版: %+v", d.Versions)
	}
	// 2 回目の SessionStart は送らない。Stop はマーカーを畳む
	a.sessionStart("sess-other")
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("送った後の Stop: %v", g)
	}
	marks, _ = filepath.Glob(filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", "handoff-pending.d", "*"))
	if len(marks) != 0 {
		t.Fatalf("送った後もマーカーが残る: %v", marks)
	}
	_, listed, _ = a.memory("show", "handoff", "--history")
	if n := strings.Count(listed, "append"); n != 1 {
		t.Fatalf("%d 回送った:\n%s", n, listed)
	}
}

// 同時に送っても 1 回だけ。応答の前に切れた追記も 2 重にしない。
func TestHandoffSpoolOnce(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	p := newMemFlakyProxy(t, e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = p.srv.URL + "/im"
	p.down.Store(true)
	for i := 0; i < 5; i++ {
		if code, _, msg := a.handoff("本文 "+strconv.Itoa(i), "append", "--title", "節"+strconv.Itoa(i), "--session", "sess-a"); code != 0 {
			t.Fatalf("退避: exit %d %s", code, msg)
		}
	}
	p.down.Store(false)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errb bytes.Buffer
			raw := []byte(`{"session_id":"s","hook_event_name":"SessionStart"}`)
			loop.Main(context.Background(), "session-start-memories", []string{"--agent", "claude-code"}, bytes.NewReader(raw), &out, &errb, a.loopEnv())
		}()
	}
	wg.Wait()
	a.sessionStart("sess-b") // 持ち時間で残った分があれば送り切る
	_, shown, _ := a.memory("show", "handoff")
	for i := 0; i < 5; i++ {
		if n := strings.Count(shown, "本文 "+strconv.Itoa(i)); n != 1 {
			t.Errorf("本文 %d が %d 回:\n%s", i, n, shown)
		}
	}
}

// 応答の前に切れた追記はサーバに届いている。退避から送り直しても 2 重にしない。
func TestHandoffSpoolLostResponse(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	p := newMemFlakyProxy(t, e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = p.srv.URL + "/im"
	if code, _, msg := a.handoff("先の節", "append", "--title", "先", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	p.lost.Store(true)
	if code, out, msg := a.handoff("切れた節", "append", "--title", "切れた", "--session", "sess-a"); code != 0 || !strings.Contains(out, "退避しました") {
		t.Fatalf("exit %d %s %s", code, out, msg)
	}
	p.lost.Store(false)
	a.sessionStart("sess-a")
	_, shown, _ := a.memory("show", "handoff")
	if n := strings.Count(shown, "切れた節"); n != 1 {
		t.Fatalf("%d 回入った:\n%s", n, shown)
	}
	if _, list, _ := a.handoff("", "spool"); !strings.Contains(list, "ありません") {
		t.Fatalf("退避が残った: %s", list)
	}
}

// 次の書き込みが先に送る。圧縮の退避は版が同じなら送り、古ければ残して注意を出す。拒まれたものも残し、値は出さない。
func TestHandoffSpoolCompactAndRejected(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	p := newMemFlakyProxy(t, e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = p.srv.URL + "/im"
	if code, _, msg := a.handoff("元の本文", "append", "--title", "元", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	// 版が同じ圧縮の退避は、次の書き込みの前に送る
	p.down.Store(true)
	if code, out, msg := a.handoff("## 圧縮\n短い", "compact", "--version", "1", "--session", "sess-a"); code != 0 || !strings.Contains(out, "退避しました") {
		t.Fatalf("exit %d %s %s", code, out, msg)
	}
	p.down.Store(false)
	if code, out, msg := a.handoff("次の話", "append", "--title", "次", "--session", "sess-a"); code != 0 || !strings.Contains(out, "版 3") {
		t.Fatalf("exit %d %s %s", code, out, msg)
	}
	_, shown, _ := a.memory("show", "handoff")
	if !strings.Contains(shown, "## 圧縮") || strings.Contains(shown, "元の本文") || !strings.Contains(shown, "次の話") {
		t.Fatalf("本文: %s", shown)
	}
	// 古い版の圧縮は残す
	p.down.Store(true)
	if code, _, msg := a.handoff("## 古い圧縮\n上書きしない", "compact", "--version", "1", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	p.down.Store(false)
	s := a.sessionStart("sess-b")
	if !strings.Contains(s, "圧縮の元にした版が古い") || strings.Contains(s, "上書きしない") {
		t.Fatalf("注入:\n%s", s)
	}
	// サーバが拒むもの（追記の上限を超える本文）も残して注意を出す
	p.down.Store(true)
	if code, _, msg := a.handoff(strings.Repeat("長い", 9000), "append", "--title", "長い節", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	// 資格情報の形は退避しない。値は出さない
	const value = "password=Sup3rS3cretValue9"
	code, out, msg := a.handoff(value, "append", "--title", "秘密", "--session", "sess-a")
	if code != 1 || !strings.Contains(msg, "退避しませんでした") || strings.Contains(out+msg, "Sup3rS3cretValue9") {
		t.Fatalf("資格情報の退避: exit %d %s %s", code, out, msg)
	}
	p.down.Store(false)
	s = a.sessionStart("sess-b")
	if !strings.Contains(s, "未送信の退避が 2 件") {
		t.Fatalf("注入:\n%s", s)
	}
	code, list, _ := a.handoff("", "spool")
	if code != 0 || strings.Count(list, " compact ")+strings.Count(list, " append ") != 2 {
		t.Fatalf("一覧: exit %d %s", code, list)
	}
	names := strings.Fields(strings.ReplaceAll(list, "\n", " "))
	var files []string
	for _, n := range names {
		if strings.HasSuffix(n, ".json") {
			files = append(files, n)
		}
	}
	for _, f := range files {
		if code, out, msg := a.handoff("", "spool", "--drop", f); code != 0 || !strings.Contains(out, "取り消せません") {
			t.Fatalf("--drop: exit %d %s %s", code, out, msg)
		}
	}
	if _, list, _ := a.handoff("", "spool"); !strings.Contains(list, "ありません") {
		t.Fatalf("捨てた後の一覧: %s", list)
	}
	if code, _, _ := a.handoff("", "spool", "--drop", "../x.json"); code != 2 {
		t.Fatalf("外を指す名前: exit %d", code)
	}
}

// spoolDirOf は箱の退避の置き場（1 つだけあるはず）。
func spoolDirOf(t *testing.T, b *memHookBox) string {
	t.Helper()
	dirs, _ := filepath.Glob(filepath.Join(b.vars["HOME"], ".config", "looptrack", "handoff-spool", "*"))
	if len(dirs) != 1 {
		t.Fatalf("退避の置き場: %v", dirs)
	}
	return dirs[0]
}

func newSpoolBox(t *testing.T) (*memHookBox, *memFlakyProxy) {
	t.Helper()
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	p := newMemFlakyProxy(t, e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = p.srv.URL + "/im"
	return a, p
}

func (b *memHookBox) historyCount() int {
	b.t.Helper()
	_, out, _ := b.memory("show", "handoff", "--history")
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

// 冪等はサーバで持つ: 前方一致の本文も同じ節の再掲も届き、同じ鍵の再送だけが畳まれる。
func TestHandoffAppendIdempotency(t *testing.T) {
	a, p := newSpoolBox(t)
	for _, body := range []string{"本文 12", "本文 1"} { // 2 つ目は 1 つ目の前方一致
		if code, _, msg := a.handoff(body, "append", "--title", "節", "--session", "sess-a"); code != 0 {
			t.Fatalf("exit %d %s", code, msg)
		}
	}
	// 同じ節の再掲を退避から送っても届く
	p.down.Store(true)
	if code, _, msg := a.handoff("本文 1", "append", "--title", "節", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	p.down.Store(false)
	a.sessionStart("sess-a")
	_, shown, _ := a.memory("show", "handoff")
	if n := strings.Count(shown, "本文 1\n"); n != 2 {
		t.Fatalf("前方一致か再掲の節が消えた（%d 件）:\n%s", n, shown)
	}
	if n := a.historyCount(); n != 3 {
		t.Fatalf("版が %d", n)
	}
	// 同じ鍵の再送は版を増やさず今の版を返す
	spool := spoolDirForServer(t, a)
	en := []byte(`{"op":"append","key":"fixed-key","content":"## 鍵\n","at":"x"}`)
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(spool, strconv.Itoa(1000+i)+"-0000000"+strconv.Itoa(i)+".json"), en, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a.sessionStart("sess-a")
	if n := a.historyCount(); n != 4 {
		t.Fatalf("同じ鍵で版が増えた: %d", n)
	}
}

// spoolDirForServer は、まだ退避が無くても置き場を作って返す。
func spoolDirForServer(t *testing.T, b *memHookBox) string {
	t.Helper()
	m, err := cli.NewMemoryAccess(clientenv.FromMap(b.vars), b.vars["CLAUDE_PROJECT_DIR"], "ja", 0)
	if err != nil {
		t.Fatal(err)
	}
	d := m.SpoolDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

// 取得の時刻で見るので、古い退避を送信中に別のセッションが来ても奪わない。落ちて残ったものは 10 分で戻す。
func TestHandoffSpoolSendingRecovery(t *testing.T) {
	a, _ := newSpoolBox(t)
	dir := spoolDirForServer(t, a)
	old := time.Now().Add(-11 * time.Minute)
	write := func(name, body string, mtime time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(`{"op":"append","key":"k`+strconv.Itoa(len(name))+strconv.Itoa(len(body))+`","content":"## `+body+`\n","session":"sess-a","at":"x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	// 取ったばかりの .sending（中身は 11 分前の退避）は戻さず、残っているものとして数える
	write("1000-0000000a.json.sending", "取得したばかり", time.Now())
	s := a.sessionStart("sess-b")
	if !strings.Contains(s, "未送信の退避が 1 件") {
		t.Fatalf("送信中を数えていない:\n%s", s)
	}
	a.finish("sess-a")
	g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
	if stopBlocked(g) || !strings.Contains(fmt.Sprint(g), "未送信") {
		t.Fatalf("Stop: %v", g)
	}
	if _, shown, _ := a.memory("show", "handoff"); strings.Contains(shown, "取得したばかり") {
		t.Fatalf("送信中のものを別のセッションが送った: %s", shown)
	}
	if _, list, _ := a.handoff("", "spool"); !strings.Contains(list, ".sending") {
		t.Fatalf("spool の一覧に出ない: %s", list)
	}
	// 落ちて残ったものは戻して送る
	if err := os.Chtimes(filepath.Join(dir, "1000-0000000a.json.sending"), old, old); err != nil {
		t.Fatal(err)
	}
	a.sessionStart("sess-b")
	if _, shown, _ := a.memory("show", "handoff"); !strings.Contains(shown, "取得したばかり") {
		t.Fatalf("回収していない: %q", shown)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*")); len(m) != 0 {
		t.Fatalf("残った: %v", m)
	}
	// 取るときに時刻を更新する（古い退避を送っている間に別のプロセスが戻さない）
	write("2000-0000000b.json", "古い退避", old)
	a.sessionStart("sess-b")
	if n := strings.Count(func() string { _, x, _ := a.memory("show", "handoff"); return x }(), "古い退避"); n != 1 {
		t.Fatalf("%d 回送った", n)
	}
}

// 2 回送らない: 送信中に別のセッションの SessionStart が来ても版は 1 つ。鍵の無い退避なので、取得の時刻の判定そのものを見ている。
func TestHandoffSpoolOldEntryDuringSend(t *testing.T) {
	a, p := newSpoolBox(t)
	dir := spoolDirForServer(t, a)
	path := filepath.Join(dir, "1000-0000000c.json")
	if err := os.WriteFile(path, []byte(`{"op":"append","content":"## 古い\n","at":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	a.finish("sess-b") // Stop はマーカーが無いと何もしない
	p.postDelay.Store(int64(600 * time.Millisecond))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var out, errb bytes.Buffer
		loop.Main(context.Background(), "session-start-memories", []string{"--agent", "claude-code"},
			strings.NewReader(`{"session_id":"a","hook_event_name":"SessionStart"}`), &out, &errb, a.loopEnv())
	}()
	for i := 0; i < 400; i++ { // A が取るのを待つ（締切つき）
		if _, err := os.Stat(path + ".sending"); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.hook("stop-handoff-freshness", "Stop", "sess-b", nil) // 退避の回収が最初に走る
	wg.Wait()
	p.postDelay.Store(0)
	a.sessionStart("sess-b")
	if n := a.historyCount(); n != 1 {
		t.Fatalf("版が %d", n)
	}
}

// 退避しただけでは Stop を畳まない。--drop でも畳まない。
func TestHandoffSpoolDropDoesNotFold(t *testing.T) {
	a, p := newSpoolBox(t)
	a.finish("sess-a")
	p.down.Store(true)
	code, out, msg := a.handoff("本文", "append", "--title", "節", "--session", "sess-a")
	if code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	a.sawHandoff("sess-a", out) // 退避の行を見ても書いた記録にしない
	p.down.Store(false)
	dir := spoolDirOf(t, a)
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("退避: %v", files)
	}
	if code, _, msg := a.handoff("", "spool", "--drop", filepath.Base(files[0])); code != 0 {
		t.Fatalf("--drop: exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); !stopBlocked(g) {
		t.Fatalf("書かれていないのに通した: %v", g)
	}
}

// シェルの中の ID が hook と違っても、送れた後の Stop は通る（PostToolUse が hook の ID を退避に書き足す）。
func TestHandoffSpoolHookSession(t *testing.T) {
	a, p := newSpoolBox(t)
	a.finish("sess-a")
	p.down.Store(true)
	code, out, msg := a.bash(map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "host-window-1"}).handoff("本文", "append", "--title", "節")
	if code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	a.sawHandoff("sess-a", out)
	p.down.Store(false)
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("送れた後に止めた: %v", g)
	}
	if a.exists(".claude", "handoff-pending.d", "sess-a") {
		t.Fatal("マーカーが残った")
	}
}

// 比べられない版は自分の版のときだけ、畳まずに通す。ほかの利用者の版では通さない。
func TestServerHandoffUnknownWriter(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	pr, err := store.ProjectBySlug(context.Background(), e.db, slug)
	if err != nil {
		t.Fatal(err)
	}
	other := e.user("memhook-ot", "memhook-password-4", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, other.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	a := e.memHookBox(slug, token)
	b := e.memHookBox(slug, e.apiAs(other).token)
	a.finish("sess-a")
	if code, _, msg := b.memoryWrite("handoff", "main", "## ほかの利用者\n"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); !stopBlocked(g) {
		t.Fatalf("ほかの利用者の版で通した: %v", g)
	}
	if code, _, msg := a.memoryWrite("handoff", "main", "## 自分\n"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
	if stopBlocked(g) || !strings.Contains(fmt.Sprint(g), "判定できない") {
		t.Fatalf("自分の版: %v", g)
	}
	if !a.exists(".claude", "handoff-pending.d", "sess-a") {
		t.Fatal("セッションを特定できないのに畳んだ")
	}
}

// 手元の記録があるセッションがサーバへ直接書いても通る。
func TestServerHandoffDirectWriteAfterLocalRecord(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	if code, _, msg := a.handoff("前", "append", "--title", "前", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	rec := filepath.Join(a.vars["CLAUDE_PROJECT_DIR"], ".claude", ".looptrack-freshness", "handoff-written", "sess-a")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(rec, past, past); err != nil {
		t.Fatal(err)
	}
	a.finish("sess-a")
	a.vars["LOOPTRACK_SESSION_ID"] = "sess-a"
	defer delete(a.vars, "LOOPTRACK_SESSION_ID")
	if code, _, msg := a.memoryWrite("handoff", "main", "## 直接\n"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("直接書いたのに止めた: %v", g)
	}
}

// 遅いサーバでも注入を確保し、退避は次の機会に回す。
func TestSessionStartSlowServerKeepsInjection(t *testing.T) {
	a, p := newSpoolBox(t)
	if code, _, msg := a.handoff("注入する本文", "append", "--title", "現在地", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	dir := spoolDirForServer(t, a)
	if err := os.WriteFile(filepath.Join(dir, "1000-0000000d.json"), []byte(`{"op":"append","key":"kd","content":"## 退避\n","at":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p.delay.Store(int64(1200 * time.Millisecond))
	s := a.sessionStart("sess-b")
	p.delay.Store(0)
	if !strings.Contains(s, "注入する本文") || !strings.Contains(s, "未送信の退避が 1 件") {
		t.Fatalf("注入:\n%s", s)
	}
	if s := a.sessionStart("sess-b"); !strings.Contains(s, "## 退避") {
		t.Fatalf("速くなった後に送っていない:\n%s", s)
	}
}

// 適用済みの圧縮は古いと見なさず片付ける。
func TestHandoffSpoolCompactAlreadyApplied(t *testing.T) {
	a, p := newSpoolBox(t)
	if code, _, msg := a.handoff("元", "append", "--title", "元", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	p.lost.Store(true)
	if code, out, msg := a.handoff("## 圧縮\n短い", "compact", "--version", "1", "--session", "sess-a"); code != 0 || !strings.Contains(out, "退避しました") {
		t.Fatalf("exit %d %s %s", code, out, msg)
	}
	p.lost.Store(false)
	if s := a.sessionStart("sess-a"); strings.Contains(s, "未送信") || !strings.Contains(s, "## 圧縮") {
		t.Fatalf("注入:\n%s", s)
	}
	if n := a.historyCount(); n != 2 {
		t.Fatalf("版が %d", n)
	}
}

// 5xx は退避し、届かないときと文面を分ける。順序を守るので先頭を飛ばさない。退避は 0600。
func TestHandoffSpoolServerError(t *testing.T) {
	a, p := newSpoolBox(t)
	p.fail500.Store(true)
	for _, title := range []string{"一", "二"} {
		if code, out, msg := a.handoff("本文"+title, "append", "--title", title, "--session", "sess-a"); code != 0 || !strings.Contains(out, "退避しました") {
			t.Fatalf("exit %d %s %s", code, out, msg)
		}
	}
	dir := spoolDirOf(t, a)
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		t.Fatalf("退避: %v", files)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(files[0])
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("権限: %v %v", st.Mode(), err)
		}
	}
	a.finish("sess-a")
	g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
	if m := fmt.Sprint(g); stopBlocked(g) || !strings.Contains(m, "502") || strings.Contains(m, "届きません") {
		t.Fatalf("文面: %v", g)
	}
	p.fail500.Store(false)
	a.sessionStart("sess-a")
	_, shown, _ := a.memory("show", "handoff")
	if i, j := strings.Index(shown, "本文一"), strings.Index(shown, "本文二"); i < 0 || j < i {
		left, _ := os.ReadDir(dir)
		t.Fatalf("順序:\n%s\n残り: %v", shown, left)
	}
}

// --show は秘密の形の値を出さず、人が開くパスを出す。
func TestHandoffSpoolShowSecret(t *testing.T) {
	a, _ := newSpoolBox(t)
	dir := spoolDirForServer(t, a)
	name := "1000-0000000e.json"
	raw, _ := json.Marshal(map[string]string{"op": "append", "key": "ke", "content": "password=Sup3rS3cretValue9\n", "at": "x"})
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, _ := a.handoff("", "spool", "--show", name)
	if strings.Contains(out, "Sup3rS3cretValue9") || !strings.Contains(out, filepath.Join(dir, name)) {
		t.Fatalf("%s", out)
	}
}

// 手元の記録は kit の .gitignore の行で版管理の外になり、古いものは消える。
func TestHandoffWrittenIgnoredAndPruned(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	root := a.vars["CLAUDE_PROJECT_DIR"]
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/.looptrack-freshness/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(".claude", ".looptrack-freshness", "handoff-written")
	if err := os.MkdirAll(filepath.Join(root, rec), 0o755); err != nil {
		t.Fatal(err)
	}
	oldRec := filepath.Join(root, rec, "old-session")
	if err := os.WriteFile(oldRec, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldRec, old, old); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := a.handoff("本文", "append", "--title", "節", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	runGit(t, root, "check-ignore", "-q", filepath.Join(rec, "sess-a"))
	if _, err := os.Stat(oldRec); err == nil {
		t.Fatal("古い記録が残った")
	}
}

// 同じ時刻に並行に書いても退避は消えない。
func TestWriteSpoolConcurrent(t *testing.T) {
	a, p := newSpoolBox(t)
	p.down.Store(true)
	env := a.loopEnv()
	fixed := time.Date(2026, 10, 30, 12, 0, 0, 123, time.UTC)
	env.Now = func() time.Time { return fixed }
	const workers, each = 8, 25
	var wg sync.WaitGroup
	var failed atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				var out, errb bytes.Buffer
				if loop.Handoff(context.Background(), []string{"append", "--title", "t", "--session", "s"}, strings.NewReader("本文"), &out, &errb, env) != 0 {
					failed.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	dir := spoolDirOf(t, a)
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sending, _ := filepath.Glob(filepath.Join(dir, "*.sending"))
	files = append(files, sending...) // 送信中のまま残ったものも退避で、消えてはいない
	if failed.Load() != 0 || len(files) != workers*each {
		t.Fatalf("失敗 %d・退避 %d 件（期待 %d）", failed.Load(), len(files), workers*each)
	}
}

// POST が遅くて持ち時間に収まらなくても注入を出す。打ち切った退避は残り、送信中のまま残らない。
func TestSessionStartSlowPostKeepsInjection(t *testing.T) {
	a, p := newSpoolBox(t)
	if code, _, msg := a.handoff("注入する本文", "append", "--title", "現在地", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	dir := spoolDirForServer(t, a)
	for i := 0; i < 6; i++ {
		name := "1000-0000000" + strconv.Itoa(i) + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"op":"append","key":"s`+strconv.Itoa(i)+`","content":"## 退避`+strconv.Itoa(i)+`\n","at":"x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p.postDelay.Store(int64(1400 * time.Millisecond))
	s := a.sessionStart("sess-b")
	p.postDelay.Store(0)
	if !strings.Contains(s, "注入する本文") || !strings.Contains(s, "未送信の退避が") {
		t.Fatalf("注入:\n%s", s)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.sending")); len(m) != 0 {
		t.Fatalf("送信中のまま残った: %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(m) == 0 || len(m) == 6 {
		t.Fatalf("残りの件数: %d", len(m))
	}
}

// 5xx を受けたらそこで止め、後ろを送らない。
func TestHandoffSpoolStopsAtServerError(t *testing.T) {
	a, p := newSpoolBox(t)
	p.fail500.Store(true)
	for _, title := range []string{"一", "二"} {
		if code, _, msg := a.handoff("本文", "append", "--title", title, "--session", "sess-a"); code != 0 {
			t.Fatalf("exit %d %s", code, msg)
		}
	}
	a.finish("sess-a")
	before := p.posts.Load()
	a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
	if n := p.posts.Load() - before; n != 1 {
		t.Fatalf("POST が %d 回", n)
	}
}

// 残った退避で通すのは持ち主だけ。何も書いていない別のセッションは差し戻す。
func TestHandoffSpoolOwnerOnlyPasses(t *testing.T) {
	a, p := newSpoolBox(t)
	p.down.Store(true)
	if code, _, msg := a.handoff(strings.Repeat("長い", 9000), "append", "--title", "長い節", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	p.down.Store(false)
	if code, _, msg := a.memoryWrite("handoff", "main", "## 土台\n"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	time.Sleep(1100 * time.Millisecond)
	a.finish("sess-b")
	g := a.hook("stop-handoff-freshness", "Stop", "sess-b", nil)
	if !stopBlocked(g) || !strings.Contains(fmt.Sprint(g), "未送信") {
		t.Fatalf("別のセッションを通した: %v", g)
	}
	a.finish("sess-a")
	g = a.hook("stop-handoff-freshness", "Stop", "sess-a", nil)
	if stopBlocked(g) || !strings.Contains(fmt.Sprint(g), "未送信") {
		t.Fatalf("持ち主を止めた: %v", g)
	}
}

// 完了より前の退避が完了の後に送れても通らない。書き直せば通る。
func TestHandoffSpoolBeforeCompletion(t *testing.T) {
	a, p := newSpoolBox(t)
	p.down.Store(true)
	if code, _, msg := a.handoff("前の話", "append", "--title", "前", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	time.Sleep(1100 * time.Millisecond)
	a.finish("sess-a")
	p.down.Store(false)
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); !stopBlocked(g) {
		t.Fatalf("完了より前の本文で通した: %v", g)
	}
	if code, _, msg := a.handoff("後の話", "append", "--title", "後", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("書き直したのに止めた: %v", g)
	}
}

// ID の無い器は、自分の版でだけ通す。
func TestServerHandoffNoSessionOtherUser(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	pr, err := store.ProjectBySlug(context.Background(), e.db, slug)
	if err != nil {
		t.Fatal(err)
	}
	other := e.user("memhook-ns", "memhook-password-5", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, other.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	a, b := e.memHookBox(slug, token), e.memHookBox(slug, e.apiAs(other).token)
	a.hook("post-work-complete-handoff-mark", "PostToolUse", "", map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "git commit -m x"}, "tool_response": "[main abc1234] x\n 1 file changed"})
	if code, _, msg := b.memoryWrite("handoff", "main", "## ほかの利用者\n"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "", nil); !stopBlocked(g) {
		t.Fatalf("ほかの利用者の版で通した: %v", g)
	}
	if code, _, msg := a.memoryWrite("handoff", "main", "## 自分\n"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "", nil); len(g) != 0 {
		t.Fatalf("自分の版で止めた: %v", g)
	}
}

// 同じ鍵は同じ利用者の同じ本文の再送だけを畳む。応答の書き手は書き換えない。
func TestMemoryIdempotencyKeyConflict(t *testing.T) {
	e, slug, _ := newMemHookEnv(t)
	pr, err := store.ProjectBySlug(context.Background(), e.db, slug)
	if err != nil {
		t.Fatal(err)
	}
	u1 := e.user("memkey-a", "memhook-password-6", "member")
	u2 := e.user("memkey-b", "memhook-password-7", "member")
	for _, u := range []store.User{u1, u2} {
		if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
			t.Fatal(err)
		}
	}
	ca, cb := e.apiAs(u1), e.apiAs(u2)
	ca.header["Idempotency-Key"], cb.header["Idempotency-Key"] = "k1", "k1"
	path := "/projects/" + slug + "/memory/handoff/main/append"
	post := func(c *apiClient, content string) (int, map[string]any) {
		t.Helper()
		st, _, raw := c.do("POST", path, map[string]any{"content": content})
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		return st, got
	}
	if st, _ := post(ca, "## 節\n"); st != 201 {
		t.Fatalf("最初: %d", st)
	}
	delete(cb.header, "Idempotency-Key")
	if st, _ := post(cb, "## 別の人の節\n"); st != 200 {
		t.Fatalf("鍵なしの追記: %d", st)
	}
	st, got := post(ca, "## 節\n")
	mem, _ := got["memory"].(map[string]any)
	if st != 200 || mem["version"] != float64(1) || mem["updated_by"] != "memkey-a" {
		t.Fatalf("再送: %d %v", st, got)
	}
	if st, got := post(ca, "## 違う節\n"); st != 409 || !strings.Contains(fmt.Sprint(got), "memory_idempotency_conflict") {
		t.Fatalf("別の本文: %d %v", st, got)
	}
	cb.header["Idempotency-Key"] = "k1"
	if st, _ := post(cb, "## 節\n"); st != 409 {
		t.Fatalf("別の利用者: %d", st)
	}
}

// PostToolUse が hook の ID を添えても、消した退避や送った退避は生き返らない。
func TestSpoolHookNoteDoesNotRevive(t *testing.T) {
	a, p := newSpoolBox(t)
	p.down.Store(true)
	_, out, _ := a.handoff("本文", "append", "--title", "節", "--session", "sess-a")
	dir := spoolDirOf(t, a)
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("退避: %v", files)
	}
	if code, _, msg := a.handoff("", "spool", "--drop", filepath.Base(files[0])); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	a.sawHandoff("sess-a", out) // 消した後に届いた
	if m, _ := filepath.Glob(filepath.Join(dir, "*")); len(m) != 0 {
		t.Fatalf("生き返った: %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".*")); len(m) != 0 {
		t.Fatalf("横のファイルが残った: %v", m)
	}
}

// directWrite は手元の記録を残さない経路で引き継ぎを書く。
func (b *memHookBox) directWrite(t *testing.T, sid, text string) {
	t.Helper()
	b.vars["LOOPTRACK_SESSION_ID"] = sid
	defer delete(b.vars, "LOOPTRACK_SESSION_ID")
	if code, _, msg := b.memoryWrite("handoff", "main", text); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
}

// 別の作業ツリーのセッションが送っても、完了前の退避は書いた時刻で判定する。
func TestHandoffSpoolSentFromAnotherWorktree(t *testing.T) {
	for _, tc := range []struct {
		name        string
		spoolBefore bool
		wantBlocked bool
	}{
		{"完了の前の退避は通らない", true, true},
		{"完了の後の退避は通る（対照）", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, p := newSpoolBox(t)
			b := a.bash(map[string]string{"CLAUDE_PROJECT_DIR": t.TempDir()})
			spool := func() {
				p.down.Store(true)
				if code, _, msg := a.handoff("退避の話", "append", "--title", "退避", "--session", "sess-a"); code != 0 {
					t.Fatalf("exit %d %s", code, msg)
				}
				p.down.Store(false)
			}
			if tc.spoolBefore {
				spool()
				time.Sleep(1100 * time.Millisecond)
				a.finish("sess-a")
			} else {
				a.finish("sess-a")
				spool()
			}
			if s := b.sessionStart("sess-b"); !strings.Contains(s, "退避の話") {
				t.Fatalf("別の作業ツリーが送っていない:\n%s", s)
			}
			if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); stopBlocked(g) != tc.wantBlocked {
				t.Fatalf("差し戻し %v のはず: %v", tc.wantBlocked, g)
			}
		})
	}
}

// 応答の切れた追記が残っていても、完了の後に自分が書いた版は数える。
func TestHandoffSpoolResendKeepsOwnVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		write   bool
		blocked bool
	}{
		{"完了の後に書いた", true, false},
		{"書いていない（対照）", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, p := newSpoolBox(t)
			p.lost.Store(true)
			if code, out, msg := a.handoff("切れた節", "append", "--title", "切れた", "--session", "sess-a"); code != 0 || !strings.Contains(out, "退避しました") {
				t.Fatalf("exit %d %s %s", code, out, msg)
			}
			p.lost.Store(false)
			time.Sleep(1100 * time.Millisecond)
			a.finish("sess-a")
			if tc.write {
				a.directWrite(t, "sess-a", "## 完了の後\n")
			}
			if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); stopBlocked(g) != tc.blocked {
				t.Fatalf("差し戻し %v のはず: %v", tc.blocked, g)
			}
		})
	}
}

// 別のプロジェクトの同じ番号の版を取り違えない。
func TestHandoffSpoolOtherProjectSameVersion(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	p := newMemFlakyProxy(t, e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = p.srv.URL + "/im"
	pr2 := e.project("memhook2")
	u := e.user("memhook-pp", "memhook-password-8", "member")
	for _, id := range []int64{pr2.ID, e.projectID(slug)} {
		if err := store.SetMember(context.Background(), e.db, id, u.ID, "editor"); err != nil {
			t.Fatal(err)
		}
	}
	a.vars["LOOPTRACK_TOKEN"] = e.apiAs(u).token
	p.down.Store(true)
	if code, _, msg := a.handoff("一つ目のプロジェクト", "append", "--title", "節", "--session", "sess-a"); code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	p.down.Store(false)
	a.sessionStart("sess-a") // 版 1 を退避から送る
	time.Sleep(1100 * time.Millisecond)
	a.vars["LOOPTRACK_PROJECT"] = pr2.Slug
	a.finish("sess-a")
	// 対照: まだ書いていないので差し戻す
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); !stopBlocked(g) {
		t.Fatalf("書いていないのに通した: %v", g)
	}
	a.directWrite(t, "sess-a", "## 二つ目のプロジェクト\n")
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("このプロジェクトの版 1 で止めた: %v", g)
	}
}

// ID の無い器でも、完了の後の退避は自分の版として数える。
func TestHandoffSpoolNoSessionNoPostToolUse(t *testing.T) {
	noID := map[string]string{"AI_AGENT": "github_copilot_vscode_agent", "COPILOT_AGENT": "1"}
	for _, tc := range []struct {
		name    string
		before  bool
		blocked bool
	}{
		{"完了の後の退避", false, false},
		{"完了の前の退避（対照）", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, p := newSpoolBox(t)
			spool := func() {
				p.down.Store(true)
				if code, _, msg := a.bash(noID).handoff("本文", "append", "--title", "節"); code != 0 {
					t.Fatalf("exit %d %s", code, msg)
				}
				p.down.Store(false)
			}
			if tc.before {
				spool()
				time.Sleep(1100 * time.Millisecond)
				a.finish("sess-a")
			} else {
				a.finish("sess-a")
				spool()
			}
			if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); stopBlocked(g) != tc.blocked {
				t.Fatalf("差し戻し %v のはず: %v", tc.blocked, g)
			}
		})
	}
}

// 書いた時刻は版に残り、未来は今に丸め、読めない値は拒む。
func TestMemoryWrittenAt(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	c := e.memClient(token)
	path := "/projects/" + slug + "/memory/handoff/main/append"
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if st, _, _ := c.do("POST", path, map[string]any{"content": "## 過去\n", "written_at": past.Format(time.RFC3339)}); st != 201 {
		t.Fatalf("過去: %d", st)
	}
	if st, _, _ := c.do("POST", path, map[string]any{"content": "## 未来\n", "written_at": time.Now().Add(time.Hour).Format(time.RFC3339)}); st != 200 {
		t.Fatalf("未来: %d", st)
	}
	if st, _, _ := c.do("POST", path, map[string]any{"content": "## 読めない\n", "written_at": "昨日"}); st != 400 && st != 422 {
		t.Fatalf("読めない値: %d", st)
	}
	var d struct {
		Versions []memoryVersionJSON `json:"versions"`
	}
	_, _, raw := c.do("GET", "/projects/"+slug+"/memory/handoff/main", nil)
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Versions) != 2 || d.Versions[1].WrittenAt != past.Format(time.RFC3339) {
		t.Fatalf("過去の版: %+v", d.Versions)
	}
	if at, err := time.Parse(time.RFC3339, d.Versions[0].WrittenAt); err != nil || at.After(time.Now().Add(time.Minute)) {
		t.Fatalf("未来を丸めていない: %+v", d.Versions[0])
	}
}

func (e *env) memClient(token string) *apiClient {
	return &apiClient{e: e, c: e.client(), token: token, header: map[string]string{}}
}
