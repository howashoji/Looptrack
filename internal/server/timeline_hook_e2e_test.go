package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/cred"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/core"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/kitinit"
	"github.com/howashoji/looptrack/internal/store"
)

// タイムラインの hook の e2e。実物のサーバに対して、実物の hook（loop.Main・core.Main）と実物の CLI を動かす。
// 切り離した子プロセス（コミットの送信・在席）だけを同じプロセスの呼び出しに置き換え、時計（間引き）を差し替える。

type tlTerm struct {
	t     *testing.T
	e     *env
	dir   string
	sess  string
	agent string
	vars  map[string]string
	now   time.Time
	args  []string // 配線が足す引数（--no-block など）
	// held が真なら、切り離した子を動かさずに jobs に溜める（runJob で順を決めて動かす）
	held bool
	jobs []tlJob
}

// tlJob は溜めた子 1 本（引数と、足す環境）。
type tlJob struct {
	args []string
	add  map[string]string
}

type tlUsers struct {
	e            *env
	slug         string
	alice, bob   string // トークン
	aliceT, bobT *tlTerm
}

func newTimelineUsers(t *testing.T) *tlUsers {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("tlh")
	tok := func(login string) string {
		u := e.user(login, login+"-password-1", "member")
		if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
			t.Fatal(err)
		}
		return e.apiAs(u).token
	}
	u := &tlUsers{e: e, slug: pr.Slug, alice: tok("tl-alice"), bob: tok("tl-bob")}
	u.aliceT = e.tlTerm(u.alice, u.slug, "box-a", "sess-a")
	u.bobT = e.tlTerm(u.bob, u.slug, "box-b", "sess-b")
	return u
}

func (e *env) tlTerm(token, slug, label, sess string) *tlTerm {
	e.t.Helper()
	home := e.t.TempDir()
	dir := filepath.Join(e.t.TempDir(), "repo-"+label)
	for _, args := range [][]string{{"init", "-q", "-b", "main", dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "最初"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			e.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": slug, "LOOPTRACK_TOKEN": token, "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"LOOPTRACK_MACHINE_LABEL": label, "CLAUDE_PROJECT_DIR": dir, "PATH": os.Getenv("PATH"),
		// 引き継ぎの判定はこのテストの外（command で判定コマンドが無い＝判定しない）
		"LOOPTRACK_LOOP_HANDOFF_BACKEND": "command"}
	return &tlTerm{t: e.t, e: e, dir: dir, sess: sess, agent: "claude-code", vars: vars, now: time.Now()}
}

// cli は CLI を同じ作業ツリーとセッションから動かす。
func (p *tlTerm) cli(args ...string) (int, string, string) {
	vars := clienv.Overlay(clienv.FromMap(p.vars), map[string]string{"LOOPTRACK_SESSION_ID": p.sess})
	var out, errb bytes.Buffer
	code := cli.TimelineMain(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, vars)
	return code, out.String(), errb.String()
}

func (p *tlTerm) post(kind, body string, args ...string) int64 {
	p.t.Helper()
	code, out, errs := p.cli(append([]string{"post", kind, body}, args...)...)
	if code != 0 {
		p.t.Fatalf("post %s: %d %q %q", kind, code, out, errs)
	}
	m := regexp.MustCompile(`#(\d+)`).FindStringSubmatch(out)
	if m == nil {
		p.t.Fatalf("post の出力に ID が無い: %q", out)
	}
	var id int64
	fmt.Sscan(m[1], &id)
	return id
}

// spawned はこの端末の hook が切り離して起動した子の引数（同じプロセスで動かした順）。
var tlSpawnLog = map[*tlTerm][][]string{}

func (p *tlTerm) loopEnv() *loop.Env {
	return &loop.Env{Getenv: func(k string) string { return p.vars[k] }, Environ: func() []string { return nil },
		Getwd: func() string { return p.dir }, Now: func() time.Time { return p.now },
		Spawn: func(args []string, dir string, extra []string) error {
			tlSpawnLog[p] = append(tlSpawnLog[p], args)
			add := map[string]string{}
			for _, kv := range extra {
				k, v, _ := strings.Cut(kv, "=")
				add[k] = v
			}
			if len(args) == 0 || args[0] != "timeline" {
				return fmt.Errorf("知らない子: %v", args)
			}
			if p.held {
				p.jobs = append(p.jobs, tlJob{args: args, add: add})
				return nil
			}
			var out, errb bytes.Buffer
			if code := cli.TimelineMain(args[1:], cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb},
				clienv.Overlay(clienv.FromMap(p.vars), add)); code != 0 {
				p.t.Logf("子 %v: %d %s", args, code, errb.String()) // 届かない試験では失敗する。数は呼ぶ側が確かめる
			}
			return nil
		}}
}

// hook は loop の hook を 1 回動かして出力の JSON を返す（何も出なければ空）。
func (p *tlTerm) hook(name, event string, extra map[string]any) map[string]any {
	p.t.Helper()
	in := map[string]any{"session_id": p.sess, "cwd": p.dir, "hook_event_name": event}
	for k, v := range extra {
		in[k] = v
	}
	raw, _ := json.Marshal(in)
	var out, errb bytes.Buffer
	if code := loop.Main(context.Background(), name, append([]string{"--agent", p.agent}, p.args...), bytes.NewReader(raw), &out, &errb, p.loopEnv()); code != 0 {
		p.t.Fatalf("%s: exit %d: %s", name, code, errb.String())
	}
	got := map[string]any{}
	if s := strings.TrimSpace(out.String()); s != "" {
		if err := json.Unmarshal([]byte(s), &got); err != nil {
			p.t.Fatalf("%s の出力を読めない: %v: %s", name, err, s)
		}
	}
	return got
}

func contextOf(g map[string]any) string {
	spec, _ := g["hookSpecificOutput"].(map[string]any)
	s, _ := spec["additionalContext"].(string)
	return s
}

// start は SessionStart（core の summary が在席に開始の印を付けて送り、loop の session-start-messages がタイムラインを読む）。
func (p *tlTerm) start() string {
	p.t.Helper()
	vars := clienv.FromMap(p.vars)
	var jobs [][]byte
	ce := &core.Env{Vars: &vars, Getwd: func() string { return p.dir }, Spawn: func(job []byte, _ string) error {
		jobs = append(jobs, job)
		return nil
	}}
	in := `{"hook_event_name":"SessionStart","session_id":"` + p.sess + `","cwd":"` + filepath.ToSlash(p.dir) + `"}`
	var out bytes.Buffer
	if code := core.Main(context.Background(), "summary", []string{"--agent", p.agent}, strings.NewReader(in), &out, &out, ce); code != 0 {
		p.t.Fatalf("summary: %d %s", code, out.String())
	}
	for _, j := range jobs {
		core.Main(context.Background(), "usage", []string{"--usage-job"}, bytes.NewReader(j), io.Discard, io.Discard, ce)
	}
	return contextOf(p.hook("session-start-messages", "SessionStart", map[string]any{"source": "startup"}))
}

func (p *tlTerm) prompt() string {
	return contextOf(p.hook("user-prompt-messages", "UserPromptSubmit", map[string]any{"prompt": "次の作業"}))
}

func (p *tlTerm) preTool() string {
	return contextOf(p.hook("pre-tool-scope-guard", "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}))
}

// stop はターンの終わり。差し戻しの理由（無ければ空）と知らせを返す。
func (p *tlTerm) stop(active bool) (block, msg string) {
	g := p.hook("stop-handoff-freshness", "Stop", map[string]any{"stop_hook_active": active})
	if d, _ := g["decision"].(string); d == "block" {
		block, _ = g["reason"].(string)
	}
	msg, _ = g["systemMessage"].(string)
	return block, msg
}

// commit は実物の git で n 件コミットし、1 回の Bash の呼び出しとして PostToolUse を動かして注入の文を返す。
func (p *tlTerm) commit(subjects ...string) string {
	p.t.Helper()
	return p.commitWith(nil, subjects...)
}

// commitWith は git commit に flags（-q など）を足して commit と同じことをする。
func (p *tlTerm) commitWith(flags []string, subjects ...string) string {
	p.t.Helper()
	var cmds []string
	var resp strings.Builder
	for _, s := range subjects {
		args := append([]string{"-C", p.dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "--allow-empty", "-m", s}, flags...)
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			p.t.Fatalf("git commit: %v %s", err, out)
		}
		resp.Write(out)
		cmds = append(cmds, "git commit --allow-empty -m '"+s+"'")
	}
	return contextOf(p.hook("post-work-complete-handoff-mark", "PostToolUse", map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": strings.Join(cmds, " && ")}, "tool_response": map[string]any{"stdout": resp.String(), "stderr": "", "interrupted": false}}))
}

func (u *tlUsers) count(kind string) int {
	u.e.t.Helper()
	var n int
	if err := u.e.db.QueryRow("SELECT COUNT(*) FROM timeline_events WHERE kind = ?", kind).Scan(&n); err != nil {
		u.e.t.Fatal(err)
	}
	return n
}

// 受け入れ条件: 新しいセッションの SessionStart に「いま」と直近の出来事が出る。同じセッション ID の再開では前の位置からの未読が出る。
// 在席の送信に開始の印が付き、サーバにセッションの開始の出来事が載る。
func TestTimelineHookSessionStart(t *testing.T) {
	u := newTimelineUsers(t)
	if got := u.bobT.start(); strings.Contains(got, "[いま]") {
		t.Errorf("誰も動いていないのに「いま」が出た: %q", got)
	}
	if n := u.count("session_start"); n != 1 {
		t.Fatalf("開始の印を付けた在席で開始の出来事が載らない: %d", n)
	}
	u.bobT.post("update", "B は検索を直しています")
	got := u.aliceT.start()
	for _, want := range []string{"タイムライン:", "[いま]", "`box-b`", "> B は検索を直しています", "[未読の出来事と近況"} {
		if !strings.Contains(got, want) {
			t.Errorf("新しいセッションの SessionStart に %q が無い: %q", want, got)
		}
	}
	u.bobT.post("update", "B は二つ目の作業に移りました")
	// 同じセッション ID の再開: 前の位置からの未読だけ（「いま」は出さない・前に出した近況も出さない）
	again := contextOf(u.aliceT.hook("session-start-messages", "SessionStart", map[string]any{"source": "resume"}))
	if !strings.Contains(again, "> B は二つ目の作業に移りました") || strings.Contains(again, "[いま]") || strings.Contains(again, "検索を直しています") {
		t.Errorf("再開の SessionStart: %q", again)
	}
	if third := u.aliceT.prompt(); strings.Contains(third, "二つ目") {
		t.Errorf("読んだ分が次の場面にもう一度出た: %q", third)
	}
}

// 受け入れ条件: コミットを検知した PostToolUse の出力に未読が入り、サーバにコミットの出来事が載る。1 回の呼び出しで 2 件コミットしたとき 2 件とも載る（実 git）。
func TestTimelineHookBoundaryCommits(t *testing.T) {
	u := newTimelineUsers(t)
	u.aliceT.start()
	u.bobT.post("update", "B は API を触っています")
	got := u.aliceT.commit("検索の索引を足す", "検索の試験を足す")
	if !strings.Contains(got, "> B は API を触っています") || !strings.Contains(got, "looptrack timeline post update") {
		t.Errorf("区切りの注入に未読と近況の促しが無い: %q", got)
	}
	if n := u.count("commit"); n != 2 {
		t.Fatalf("2 件のコミットが載らない: %d（子: %v）", n, tlSpawnLog[u.aliceT])
	}
	seen := u.bobT.prompt()
	for _, s := range []string{"検索の索引を足す", "検索の試験を足す"} {
		if !strings.Contains(seen, "> "+s) {
			t.Errorf("相手の作業開始にコミットの件名 %q が無い: %q", s, seen)
		}
	}
	// 次の区切りは前に送った HEAD から先だけ
	u.aliceT.commit("索引の名前を直す")
	if n := u.count("commit"); n != 3 {
		t.Errorf("2 回目の区切りで送ったコミットの数: %d（want 3）", n)
	}
}

// 受け入れ条件: 区切りの後に読み残しがあると Stop がやり直しを求める。読み終えた後は求めない。区切りの後にほかのセッションが書いた分では求めない。
// 21 件以上の未読では 20 行までが出て、残りが次の場面で出る（黙って消えるものが無い）。
func TestTimelineHookStopAndOverflow(t *testing.T) {
	u := newTimelineUsers(t)
	u.aliceT.start()
	for i := 1; i <= 25; i++ {
		u.bobT.post("update", fmt.Sprintf("近況 %02d です", i))
	}
	got := u.aliceT.commit("区切りのコミット")
	shown := regexp.MustCompile(`> 近況 (\d\d) です`).FindAllStringSubmatch(got, -1)
	if len(shown) != 20 || shown[0][1] != "01" || shown[19][1] != "20" || !strings.Contains(got, "ほかに 5 行が未読のまま残っています") {
		t.Fatalf("区切りで出た行: %d 行 %v: %q", len(shown), shown, got)
	}
	block, _ := u.aliceT.stop(false)
	for _, want := range []string{"作業区切りの後の確認が済んでいません", "> 近況 21 です", "> 近況 25 です"} {
		if !strings.Contains(block, want) {
			t.Errorf("読み残しがあるのに Stop が %q を求めない: %q", want, block)
		}
	}
	if strings.Contains(block, "近況 20 です") {
		t.Errorf("区切りで出した行をもう一度出した: %q", block)
	}
	if again, _ := u.aliceT.stop(true); again != "" {
		t.Errorf("やり直しの後の停止でまた求めた: %q", again)
	}
	if done, _ := u.aliceT.stop(false); done != "" {
		t.Errorf("読み終えた後も求めた: %q", done)
	}
	// 区切りの後にほかのセッションが書いた分では求めない（次の場面に回す）
	u.aliceT.commit("二つ目の区切り")
	u.bobT.post("update", "区切りの後の近況")
	if b, _ := u.aliceT.stop(false); b != "" {
		t.Errorf("区切りの後に書かれた分で求めた: %q", b)
	}
	if p := u.aliceT.prompt(); !strings.Contains(p, "> 区切りの後の近況") {
		t.Errorf("区切りの後の分が次の場面に出ない: %q", p)
	}
	// 作業開始でも 21 件以上は 20 行と残りの件数。次の場面で残りが出る
	for i := 1; i <= 22; i++ {
		u.bobT.post("update", fmt.Sprintf("続き %02d です", i))
	}
	first, second := u.aliceT.prompt(), u.aliceT.prompt()
	all := regexp.MustCompile(`> 続き (\d\d) です`).FindAllStringSubmatch(first+second, -1)
	if len(all) != 22 || !strings.Contains(first, "ほかに 2 行") || strings.Contains(second, "続き 20 です") {
		t.Errorf("作業開始の続き: 1 回目 %q 2 回目 %q", first, second)
	}
}

// Codex は区切りの文が AI に届くと実機で確かめたので位置を進める。区切りで出した未読は次の Stop と次の作業開始で重ねて出ない。
// 対照は Copilot（区切りで位置を進めない）で、同じ未読が次の Stop にもう一度出ること。
func TestTimelineHookCodexBoundaryAdvances(t *testing.T) {
	for _, agent := range []string{"codex", "copilot"} {
		t.Run(agent, func(t *testing.T) {
			u := newTimelineUsers(t)
			u.aliceT.agent = agent
			if agent == "codex" {
				u.aliceT.vars["LOOPTRACK_LOOP_STATE_DIR"] = filepath.Join(u.aliceT.dir, ".codex")
			}
			u.aliceT.start()
			const line = "> 区切りで渡した近況"
			u.bobT.post("update", "区切りで渡した近況")
			if got := u.aliceT.commit("区切りのコミット"); !strings.Contains(got, line) {
				t.Fatalf("前提が崩れています: 区切りの注入に未読が無い: %q", got)
			}
			block, msg := u.aliceT.stop(false)
			again := strings.Contains(block+msg, line)
			if agent == "copilot" {
				if !again {
					t.Errorf("前提が崩れています: 区切りで進めない Copilot で次の Stop に未読がもう一度出ない: block %q msg %q", block, msg)
				}
				return
			}
			if again {
				t.Errorf("Codex の区切りで出した未読が次の Stop にもう一度出た: block %q msg %q", block, msg)
			}
			if p := u.aliceT.prompt(); strings.Contains(p, line) {
				t.Errorf("Codex の区切りで出した未読が次の作業開始にもう一度出た: %q", p)
			}
		})
	}
}

// countingFront は要求の数を数える前段（タイムラインの経路だけを数える）。
func countingFront(t *testing.T, target string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	u, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(u)
	var n atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/timeline/") {
			n.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	return front, &n
}

// 受け入れ条件: 利用者の発言もコミットも挟まない 1 ターンの途中で置かれた待機が、次の書き込みの前に文脈に入る。出来事と近況はそこでは出ない。
// 1 分以内の 2 回目ではサーバに問い合わせない（要求の数を数える）。了解していない待機は何度確かめても毎回出て、了解した後は出ない。
func TestTimelineHookPreToolHolds(t *testing.T) {
	u := newTimelineUsers(t)
	u.aliceT.start()
	front, n := countingFront(t, u.e.srv.URL)
	u.aliceT.vars["LOOPTRACK_API_URL"] = front.URL + "/im"
	if got := u.aliceT.preTool(); got != "" {
		t.Errorf("何も無いのに出した: %q", got)
	}
	u.bobT.post("update", "途中の近況は出さない")
	hold := u.bobT.post("hold", "main への push を止めてください", "--to", "tl-alice")
	u.aliceT.now = u.aliceT.now.Add(61 * time.Second)
	got := u.aliceT.preTool()
	if !strings.Contains(got, "> main への push を止めてください") || !strings.Contains(got, "hold") {
		t.Fatalf("待機が書き込みの前に入らない: %q", got)
	}
	if strings.Contains(got, "途中の近況") || strings.Contains(got, "[未読の出来事と近況") {
		t.Errorf("書き込みの前に近況・出来事が出た: %q", got)
	}
	before := n.Load()
	if again := u.aliceT.preTool(); again != "" {
		t.Errorf("1 分以内の 2 回目に出した: %q", again)
	}
	if d := n.Load() - before; d != 0 {
		t.Errorf("1 分以内の 2 回目にサーバへ %d 回問い合わせた", d)
	}
	for i := 0; i < 2; i++ { // 了解していない待機は確かめるたびに出る
		u.aliceT.now = u.aliceT.now.Add(61 * time.Second)
		if again := u.aliceT.preTool(); !strings.Contains(again, "main への push を止めてください") {
			t.Errorf("%d 回目の確認で未了解の待機が出ない: %q", i+2, again)
		}
	}
	if code, out, errs := u.aliceT.cli("ack", fmt.Sprint(hold)); code != 0 {
		t.Fatalf("ack: %q %q", out, errs)
	}
	u.aliceT.now = u.aliceT.now.Add(61 * time.Second)
	if after := u.aliceT.preTool(); after != "" {
		t.Errorf("了解した後も出た: %q", after)
	}
	// 作業開始では出来事と近況も出る（書き込みの前に出さなかった分が抜けていない）
	if p := u.aliceT.prompt(); !strings.Contains(p, "> 途中の近況は出さない") {
		t.Errorf("書き込みの前に出さなかった近況が作業開始に出ない: %q", p)
	}
}

// preToolHolds は書き込みの前の確認が手元に残した待機の ID の並び（サーバの軽い確認が返した値）。
func (p *tlTerm) preToolHolds() []int64 {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join(p.dir, ".claude", ".looptrack-freshness", "timeline", p.sess+".json"))
	if err != nil {
		p.t.Fatal(err)
	}
	var st struct {
		Holds []int64 `json:"holds"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		p.t.Fatal(err)
	}
	return st.Holds
}

// 受け入れ条件: 書き込みの前の確認で、前の確認で手元に持っていた待機が取り下げ・期限で解けたら、それぞれ 1 行で知らせる。
// 取り下げには理由を添える。了解しただけの待機は並びから外れないので「解けた」と出さない（対照）。同じ待機の「解けた」は 1 度だけ出る。
func TestTimelineHookPreToolLiftedHolds(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	withdrawn := u.bobT.post("hold", "取り下げる待機です", "--to", "tl-alice")
	expired := u.bobT.post("hold", "期限で解ける待機です", "--to", "tl-alice", "--ttl", "30m")
	acked := u.bobT.post("hold", "了解するだけの待機です", "--to", "tl-alice")
	lifted := func(id int64) string { return fmt.Sprintf("待機 #%d は", id) }
	a.now = a.now.Add(61 * time.Second)
	if got := a.preTool(); !strings.Contains(got, "> 取り下げる待機です") || !strings.Contains(got, "> 了解するだけの待機です") {
		t.Fatalf("待機が書き込みの前に入らない: %q", got)
	}
	beforeAck := a.preToolHolds()
	if code, out, errs := a.cli("ack", fmt.Sprint(acked)); code != 0 {
		t.Fatalf("ack: %q %q", out, errs)
	}
	a.now = a.now.Add(61 * time.Second)
	got := a.preTool()
	afterAck := a.preToolHolds()
	t.Logf("了解の前の並び %v・後の並び %v", beforeAck, afterAck)
	if !slices.Contains(beforeAck, acked) || !slices.Contains(afterAck, acked) {
		t.Fatalf("前提が崩れています: 了解した待機が並びから外れた（前 %v・後 %v）", beforeAck, afterAck)
	}
	if strings.Contains(got, lifted(acked)) || strings.Contains(got, "解けました") {
		t.Errorf("了解しただけの待機を「解けた」と出した: %q", got)
	}
	if code, out, errs := u.bobT.cli("withdraw", fmt.Sprint(withdrawn), "--reason", "push は終わりました"); code != 0 {
		t.Fatalf("withdraw: %q %q", out, errs)
	}
	u.e.clock.Add(31 * time.Minute) // サーバの時計で期限を越える（ほかの 2 件の待機は既定の 2 時間で効いたまま）
	a.now = a.now.Add(61 * time.Second)
	got = a.preTool()
	if !strings.Contains(got, lifted(withdrawn)+"取り下げで解けました") || !strings.Contains(got, "> push は終わりました") {
		t.Errorf("取り下げで解けた待機が出ない: %q", got)
	}
	if !strings.Contains(got, lifted(expired)+"このセッションには効かなくなりました") || strings.Contains(got, lifted(expired)+"取り下げ") {
		t.Errorf("期限で解けた待機が出ない: %q", got)
	}
	if strings.Contains(got, lifted(acked)) {
		t.Errorf("了解しただけの待機を「解けた」と出した: %q", got)
	}
	a.now = a.now.Add(61 * time.Second)
	if again := a.preTool(); strings.Contains(again, "解けました") || strings.Contains(again, "効かなくなりました") {
		t.Errorf("同じ待機の「解けた」を 2 度出した: %q", again)
	}
	// 通し番号が進んで取りに行く回（軽い確認で止まらない経路）でも重ねて出さない
	u.bobT.post("update", "通し番号を進める近況")
	a.now = a.now.Add(61 * time.Second)
	if again := a.preTool(); strings.Contains(again, "解けました") || strings.Contains(again, "効かなくなりました") {
		t.Errorf("取りに行く回に同じ待機の「解けた」を 2 度出した: %q", again)
	}
	// 取り下げの知らせは受け取りを記録したので、作業開始で重ねて出さない
	if p := a.prompt(); strings.Contains(p, "push は終わりました") {
		t.Errorf("書き込みの前に知らせた取り下げを作業開始でも出した: %q", p)
	}
}

// 書き込みの前より先に作業開始が取り下げを知らせたら、書き込みの前の確認は同じ待機を別の文面で重ねて知らせない。
func TestTimelineHookPreToolLiftedAfterPrompt(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	hold := u.bobT.post("hold", "先に作業開始が知らせる待機です", "--to", "tl-alice")
	a.now = a.now.Add(61 * time.Second)
	if got := a.preTool(); !strings.Contains(got, "> 先に作業開始が知らせる待機です") {
		t.Fatalf("待機が書き込みの前に入らない: %q", got)
	}
	if code, out, errs := u.bobT.cli("withdraw", fmt.Sprint(hold), "--reason", "反映は終わりました"); code != 0 {
		t.Fatalf("withdraw: %q %q", out, errs)
	}
	if p := a.prompt(); !strings.Contains(p, "反映は終わりました") {
		t.Fatalf("前提が崩れています: 作業開始が取り下げを知らせない: %q", p)
	}
	a.now = a.now.Add(61 * time.Second)
	if got := a.preTool(); strings.Contains(got, fmt.Sprintf("待機 #%d は", hold)) {
		t.Errorf("作業開始が知らせた取り下げを書き込みの前でも出した: %q", got)
	}
}

// 宛先の変化（端末のラベルを名乗り替えた）で並びから外れた待機は、ほかのセッションには効いたままなので「解けた」とは出さない。
func TestTimelineHookPreToolTargetChanged(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start() // 在席に box-a を名乗る
	hold := u.bobT.post("hold", "box-a の push を止めてください", "--to-machine", "box-a")
	a.now = a.now.Add(61 * time.Second)
	if got := a.preTool(); !strings.Contains(got, "> box-a の push を止めてください") {
		t.Fatalf("待機が書き込みの前に入らない: %q", got)
	}
	a.vars["LOOPTRACK_MACHINE_LABEL"] = "box-c"
	a.start() // 在席に box-c を名乗り直す
	a.now = a.now.Add(61 * time.Second)
	got := a.preTool()
	if !strings.Contains(got, fmt.Sprintf("待機 #%d はこのセッションには効かなくなりました", hold)) {
		t.Errorf("宛先から外れた待機を知らせない: %q", got)
	}
	if strings.Contains(got, "解けました") {
		t.Errorf("宛先から外れただけの待機を「解けた」と出した: %q", got)
	}
}

// 受け入れ条件: サーバに届かないとき hook は作業を止めずに「確かめられていない」を知らせる。届いたときは知らせが出ない。知らせは 10 分に 1 回まで。
func TestTimelineHookUnreachable(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	if got := a.start(); strings.Contains(got, "確かめられていません") {
		t.Errorf("届いたのに知らせた: %q", got)
	}
	good := a.vars["LOOPTRACK_API_URL"]
	a.vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
	a.now = a.now.Add(time.Minute)
	got := a.prompt()
	if !strings.Contains(got, "確かめられていません") || !strings.Contains(got, "最後に確かめた時刻は") {
		t.Errorf("届かないのに知らせない: %q", got)
	}
	a.now = a.now.Add(5 * time.Minute)
	if again := a.prompt(); again != "" {
		t.Errorf("10 分以内にまた知らせた: %q", again)
	}
	if pt := a.preTool(); pt != "" {
		t.Errorf("10 分以内の書き込みの前に知らせた: %q", pt)
	}
	a.now = a.now.Add(6 * time.Minute)
	if again := a.prompt(); !strings.Contains(again, "確かめられていません") {
		t.Errorf("10 分を過ぎても知らせない: %q", again)
	}
	a.vars["LOOPTRACK_API_URL"] = good
	a.now = a.now.Add(11 * time.Minute)
	if back := a.prompt(); strings.Contains(back, "確かめられていません") {
		t.Errorf("届いたのに知らせた: %q", back)
	}
}

// LOOPTRACK_LOOP_MESSAGES=0 で切ると、どの場面でも読まず、コミットも送らず、Stop も求めない。
func TestTimelineHookOff(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	u.bobT.post("update", "切ったら出ない近況")
	u.bobT.post("hold", "切っても効く待機", "--to", "tl-alice")
	a.vars["LOOPTRACK_LOOP_MESSAGES"] = "0"
	front, n := countingFront(t, u.e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = front.URL + "/im"
	a.now = a.now.Add(2 * time.Minute)
	if got := a.prompt() + a.preTool(); got != "" {
		t.Errorf("切ったのに出した: %q", got)
	}
	if got := a.commit("切ったときのコミット"); strings.Contains(got, "切ったら出ない") || strings.Contains(got, "timeline post update") {
		t.Errorf("切ったのに区切りで出した: %q", got)
	}
	if b, _ := a.stop(false); b != "" {
		t.Errorf("切ったのに Stop で求めた: %q", b)
	}
	if d := n.Load(); d != 0 || u.count("commit") != 0 {
		t.Errorf("切ったのにタイムラインへ %d 回問い合わせた・コミット %d 件", d, u.count("commit"))
	}
	// 対照: 戻すと出る
	delete(a.vars, "LOOPTRACK_LOOP_MESSAGES")
	if got := a.prompt(); !strings.Contains(got, "> 切ったら出ない近況") || !strings.Contains(got, "> 切っても効く待機") {
		t.Errorf("戻した後に出ない: %q", got)
	}
}

// 区切りの後に置かれた未了解の待機では Stop は求めない（次の場面に回す）。対照: 区切りの前に置かれた未了解の待機では求める。
func TestTimelineHookStopHoldAfterBoundary(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	a.commit("区切り 1")
	u.bobT.post("hold", "区切りの後の待機", "--to", "tl-alice")
	if b, _ := a.stop(false); b != "" {
		t.Errorf("区切りの後に置かれた待機で求めた: %q", b)
	}
	// 対照: 待機を置いてから区切る（区切りの文で出るが、了解していないので Stop が求める）
	u.bobT.post("hold", "区切りの前の待機", "--to", "tl-alice")
	if got := a.commit("区切り 2"); !strings.Contains(got, "> 区切りの前の待機") {
		t.Fatalf("前提が崩れています: 区切りに待機が出ない: %q", got)
	}
	if b, _ := a.stop(false); !strings.Contains(b, "区切りの前の待機") {
		t.Errorf("区切りの前の未了解の待機で求めない: %q", b)
	}
}

// 区切りで 21 件以上を残し、区切りの後にも載ったとき、印の番号までの読み残しで求める（区切りの後の分だけでは求めない対照は下の Codex）。
// 区切りで位置を進めない Codex では、自分の出来事だけで位置が印より後ろに残っても、区切りの後の分だけなら求めない。
func TestTimelineHookStopOnlyUpToMark(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	for i := 1; i <= 23; i++ {
		u.bobT.post("update", fmt.Sprintf("前 %02d", i))
	}
	a.commit("残す区切り")
	u.bobT.post("update", "区切りの後 01")
	if b, _ := a.stop(false); !strings.Contains(b, "> 前 21") || !strings.Contains(b, "> 前 23") {
		t.Errorf("印までの読み残しで求めない: %q", b)
	}

	c := newTimelineUsers(t)
	x := c.aliceT
	x.agent = "codex"
	x.vars["LOOPTRACK_LOOP_STATE_DIR"] = filepath.Join(x.dir, ".codex")
	x.start()
	x.commit("自分の出来事だけの区切り") // 区切りでは位置を進めないので、読んだ位置は自分のコミットの出来事より前に残る
	c.bobT.post("update", "区切りの後だけの近況")
	g := x.hook("stop-handoff-freshness", "Stop", map[string]any{})
	if m, _ := g["systemMessage"].(string); strings.Contains(m, "作業区切りの後の確認") {
		t.Errorf("区切りの後の分だけで求めた: %q", m)
	}
	if p := x.prompt(); !strings.Contains(p, "> 区切りの後だけの近況") {
		t.Errorf("前提が崩れています: 区切りの後の近況が未読にない: %q", p)
	}
}

// 書き込みの前に出す依頼は新しく届いたものだけ。対照: 届いた後にもう一度確かめても出ない・新しい依頼は出る。
func TestTimelineHookPreToolRequestOnlyNew(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	u.bobT.post("request", "最初の依頼", "--to", "tl-alice")
	a.now = a.now.Add(61 * time.Second)
	if got := a.preTool(); !strings.Contains(got, "> 最初の依頼") {
		t.Fatalf("新しく届いた依頼が出ない: %q", got)
	}
	u.bobT.post("update", "通し番号を進める近況")
	a.now = a.now.Add(61 * time.Second)
	if got := a.preTool(); strings.Contains(got, "最初の依頼") {
		t.Errorf("既に届いた依頼をまた出した: %q", got)
	}
	u.bobT.post("request", "二つ目の依頼", "--to", "tl-alice")
	a.now = a.now.Add(61 * time.Second)
	if got := a.preTool(); !strings.Contains(got, "> 二つ目の依頼") || strings.Contains(got, "最初の依頼") {
		t.Errorf("新しい依頼だけが出ない: %q", got)
	}
}

// 子（looptrack timeline commit）が送れなかった範囲は記録されず、次の区切りで送り直す。
func TestTimelineHookCommitRetryAfterFailure(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	good := a.vars["LOOPTRACK_API_URL"]
	a.vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
	a.commit("届かなかったコミット")
	if n := u.count("commit"); n != 0 {
		t.Fatalf("前提が崩れています: 届かないのに載った: %d", n)
	}
	a.vars["LOOPTRACK_API_URL"] = good
	a.commit("届いたコミット")
	if n := u.count("commit"); n != 2 {
		t.Errorf("届かなかった分を送り直さない: %d（want 2）", n)
	}
	a.commit("次のコミット")
	if n := u.count("commit"); n != 3 {
		t.Errorf("送れた後は先だけを送る: %d（want 3）", n)
	}
}

// 初めての区切りはセッション開始の HEAD から送る（成功の行を出さない git commit -q でも、浅い履歴でも全部載る）。
func TestTimelineHookCommitQuietAndShallow(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	a.commitWith([]string{"-q"}, "静かなコミット 1", "静かなコミット 2")
	if n := u.count("commit"); n != 2 {
		t.Errorf("-q の 2 件が載らない: %d", n)
	}
	// 浅い履歴（--depth 1）の作業ツリーで新しいセッション
	b := u.bobT
	shallow := filepath.Join(t.TempDir(), "shallow")
	if out, err := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+a.dir, shallow).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	b.dir, b.vars["CLAUDE_PROJECT_DIR"] = shallow, shallow
	b.start()
	b.commitWith([]string{"-q"}, "浅い 1", "浅い 2", "浅い 3")
	if n := u.count("commit"); n != 5 {
		t.Errorf("浅い履歴の 3 件が載らない: %d（want 5）", n)
	}
}

// --no-block の配線の Stop は理由が利用者への知らせに落ちるので位置を進めない（次の場面でもう一度出る）。対照: 引数が無ければ進める。
func TestTimelineHookNoBlockArgKeepsUnread(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	for i := 1; i <= 21; i++ {
		u.bobT.post("update", fmt.Sprintf("引数 %02d", i))
	}
	a.commit("区切り")
	a.args = []string{"--no-block"}
	if _, m := a.stop(false); !strings.Contains(m, "> 引数 21") {
		t.Fatalf("前提が崩れています: --no-block の Stop に読み残しが無い: %q", m)
	}
	a.args = nil
	if b, _ := a.stop(false); !strings.Contains(b, "> 引数 21") {
		t.Errorf("--no-block の Stop で位置を進めた: %q", b)
	}
	if b, _ := a.stop(false); b != "" {
		t.Errorf("対照: 引数の無い Stop で位置を進めない: %q", b)
	}
}

// 7 日より古い手元の記録はセッション開始で消す（新しい記録は残す）。
func TestTimelineHookExpiresOldState(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	dir := filepath.Join(a.dir, ".claude", ".looptrack-freshness", "timeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "old-sess.json"), filepath.Join(dir, "new-sess.mark")
	for _, f := range []string{old, fresh, filepath.Join(dir, "old-sess.mark")} {
		if err := os.WriteFile(f, []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := a.now.Add(-8 * 24 * time.Hour)
	for _, f := range []string{old, filepath.Join(dir, "old-sess.mark")} {
		if err := os.Chtimes(f, past, past); err != nil {
			t.Fatal(err)
		}
	}
	a.start()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("古い記録が残った: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old-sess.mark")); !os.IsNotExist(err) {
		t.Errorf("古い印が残った: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("新しい記録まで消した: %v", err)
	}
}

// initInto は実物の init（looptrack issue init）を p の作業ツリーに走らせる（接続先は url と slug）。
func (p *tlTerm) initInto(agent, url, slug string) {
	p.t.Helper()
	kitinit.Register() // 実行ファイルの main と同じ登録（cli から kitinit を import すると循環する）
	vars := clienv.FromMap(map[string]string{"HOME": p.vars["HOME"], "USERPROFILE": p.vars["HOME"], "XDG_CONFIG_HOME": p.vars["XDG_CONFIG_HOME"],
		"APPDATA": p.vars["APPDATA"], "LOOPTRACK_LANG": "ja", "PATH": os.Getenv("PATH")})
	var out, errb bytes.Buffer
	if code := cli.Main([]string{"init", "--agent", agent, "--loop", "--project", slug, "--url", url, "--no-verify", "--dir", p.dir},
		cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, vars); code != 0 {
		p.t.Fatalf("init %s: %d %s %s", agent, code, out.String(), errb.String())
	}
}

// Codex と Copilot の hook は、環境に接続先が無ければ init が書いた導入先の設定から得る（Codex は .codex/config.toml、
// Copilot は .claude/.looptrack-kit.json）。対照: 設定が無ければ従来どおり何も出さない・環境にあれば環境を優先する。
func TestTimelineHookConnectionFromInstall(t *testing.T) {
	for _, tc := range []struct{ agent, file string }{{"codex", ".codex/config.toml"}, {"copilot", ".claude/.looptrack-kit.json"}} {
		t.Run(tc.agent, func(t *testing.T) {
			u := newTimelineUsers(t)
			a := u.aliceT
			a.agent = tc.agent
			url := a.vars["LOOPTRACK_API_URL"]
			a.initInto(tc.agent, url, u.slug)
			if b, err := os.ReadFile(filepath.Join(a.dir, tc.file)); err != nil || !strings.Contains(string(b), url) {
				t.Fatalf("前提が崩れています: init が %s に接続先を書いていない: %v %s", tc.file, err, b)
			}
			delete(a.vars, "LOOPTRACK_API_URL")
			delete(a.vars, "LOOPTRACK_PROJECT")
			delete(a.vars, "CLAUDE_PROJECT_DIR")
			a.saveCred(url, u.alice) // 補った URL には資格情報のファイルの項目だけが使われる（環境のトークンは外す）
			a.start()
			u.bobT.post("update", "設定ファイルから届く近況 "+tc.agent)
			if got := a.prompt(); !strings.Contains(got, "> 設定ファイルから届く近況 "+tc.agent) {
				t.Errorf("導入先の設定から接続先を得られない: %q", got)
			}
			// 環境にあれば環境を優先する（届かない URL を置くと、設定ファイルの URL ではなくそちらに問うて知らせが出る）
			a.vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
			if got := a.prompt(); !strings.Contains(got, "確かめられていません") {
				t.Errorf("環境の接続先を優先しない: %q", got)
			}
			delete(a.vars, "LOOPTRACK_API_URL")
			// 設定ファイルが無ければ従来どおり何も出さない
			if err := os.Remove(filepath.Join(a.dir, tc.file)); err != nil {
				t.Fatal(err)
			}
			u.bobT.post("update", "設定が無いと届かない近況")
			if got := a.prompt(); got != "" {
				t.Errorf("設定が無いのに出した: %q", got)
			}
		})
	}
}

// handCommit は hook を通さずにコミットする（ほかの人のコミットを取り込んだ形の代わり）。
func (p *tlTerm) handCommit(dir string, subjects ...string) {
	p.t.Helper()
	for _, s := range subjects {
		if out, err := exec.Command("git", "-C", dir, "-c", "user.name=o", "-c", "user.email=o@example.invalid", "commit", "-q", "--allow-empty", "-m", s).CombinedOutput(); err != nil {
			p.t.Fatalf("git commit: %v %s", err, out)
		}
	}
}

// セッション開始の後に入ったほかのコミットを、自分のコミットとして送らない。
// (1) hook を通らずに入ったもの: 自分の呼び出しの成功の行の数で範囲を上限する。
// (2) シェルの呼び出し（pull など）で入ったもの: その呼び出しで観測した HEAD から先だけを送る（-q で成功の行が無くても）。
func TestTimelineHookCommitExcludesOthers(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	a.handCommit(a.dir, "ほか 1", "ほか 2", "ほか 3")
	a.commit("自分の 1 件")
	if n := u.count("commit"); n != 1 {
		t.Errorf("hook を通らずに入ったコミットまで送った: %d（want 1）", n)
	}
	a.handCommit(a.dir, "取り込み 1", "取り込み 2")
	a.hook("post-work-complete-handoff-mark", "PostToolUse", map[string]any{"tool_name": "Bash", // 取り込んだ呼び出し（完了ではない）
		"tool_input": map[string]any{"command": "git pull"}, "tool_response": map[string]any{"stdout": "Fast-forward", "stderr": ""}})
	a.commitWith([]string{"-q"}, "静かな自分の 1 件")
	if n := u.count("commit"); n != 2 {
		t.Errorf("観測の後の分だけを送らない: %d（want 2）", n)
	}
}

// 開始と違う作業ツリー（観測の記録が無い）で 1 回に 2 件コミットすると 2 件とも送る。
func TestTimelineHookCommitOtherWorktree(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	other := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", a.dir, "worktree", "add", "-q", "-b", "side", other).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v %s", err, out)
	}
	var resp strings.Builder
	for _, s := range []string{"別の作業ツリー 1", "別の作業ツリー 2"} {
		out, err := exec.Command("git", "-C", other, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "--allow-empty", "-m", s).CombinedOutput()
		if err != nil {
			t.Fatalf("git commit: %v %s", err, out)
		}
		resp.Write(out)
	}
	a.hook("post-work-complete-handoff-mark", "PostToolUse", map[string]any{"tool_name": "Bash",
		"tool_input":    map[string]any{"command": "cd " + other + " && git commit -m a && git commit -m b"},
		"tool_response": map[string]any{"stdout": resp.String(), "stderr": ""}})
	if n := u.count("commit"); n != 2 {
		t.Errorf("別の作業ツリーの 2 件が載らない: %d", n)
	}
}

// サーバは同じプロジェクトの同じ SHA のコミットを重ねて載せない。送りかけの記録は、送れたときと恒久の拒否（4xx）では消え、
// 届かないときは残る。
func TestTimelineCommitDedupAndPending(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.handCommit(a.dir, "一つ目", "二つ目")
	since := strings.TrimSpace(func() string {
		out, _ := exec.Command("git", "-C", a.dir, "rev-parse", "HEAD~2").Output()
		return string(out)
	}())
	pend := filepath.Join(t.TempDir(), "pending")
	write := func() {
		if err := os.WriteFile(pend, []byte(since+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exists := func() bool { _, err := os.Stat(pend); return err == nil }
	write()
	for i := 0; i < 2; i++ {
		if code, out, errs := a.cli("commit", "--dir", a.dir, "--since", since, "--pending", pend); code != 0 {
			t.Fatalf("commit: %d %q %q", code, out, errs)
		}
	}
	if n := u.count("commit"); n != 2 {
		t.Errorf("同じ SHA を重ねて載せた: %d（want 2）", n)
	}
	if exists() {
		t.Errorf("送れたのに送りかけが残った")
	}
	// 届かない: 残す
	write()
	good := a.vars["LOOPTRACK_API_URL"]
	a.vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
	if code, _, _ := a.cli("commit", "--dir", a.dir, "--since", since, "--pending", pend); code == 0 || !exists() {
		t.Errorf("届かないのに送りかけを消した（code %d）", code)
	}
	// 恒久の拒否（無いプロジェクト）: 消す
	a.vars["LOOPTRACK_API_URL"], a.vars["LOOPTRACK_PROJECT"] = good, "no-such-project"
	if code, _, _ := a.cli("commit", "--dir", a.dir, "--since", since, "--pending", pend); code == 0 || exists() {
		t.Errorf("恒久の拒否で送りかけが残った（code %d）", code)
	}
}

// saveCred は資格情報のファイルに url のトークンを置く（login と同じ置き場）。
func (p *tlTerm) saveCred(url, token string) {
	p.t.Helper()
	st, err := cred.Open(clienv.FromMap(p.vars))
	if err != nil {
		p.t.Fatal(err)
	}
	if err := st.SaveEntry(url, jsonorder.NewObject().Set("token", token)); err != nil {
		p.t.Fatal(err)
	}
}

// 導入先の設定から URL を補ったときは、環境の LOOPTRACK_TOKEN をその URL へ送らない（資格情報のファイルのその URL の項目を使う）。
// 対照: 環境に URL もあるときは、従来どおり環境のトークンを使う。値は比べるだけで出さない。
func TestTimelineHookInstallURLDropsEnvToken(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.agent = "codex"
	target, _ := url.Parse(u.e.srv.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var envTok, fileTok atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer " + u.bob:
			envTok.Add(1)
		case "Bearer " + u.alice:
			fileTok.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	installURL := front.URL + "/im"
	a.initInto("codex", installURL, u.slug)
	delete(a.vars, "LOOPTRACK_API_URL")
	delete(a.vars, "LOOPTRACK_PROJECT")
	delete(a.vars, "CLAUDE_PROJECT_DIR")
	a.vars["LOOPTRACK_TOKEN"] = u.bob // 環境のトークン（別の利用者）
	a.saveCred(installURL, u.alice)
	a.start()
	u.bobT.post("update", "トークンの近況")
	got := a.prompt()
	if envTok.Load() != 0 {
		t.Errorf("補った URL へ環境のトークンを %d 回送った", envTok.Load())
	}
	if fileTok.Load() == 0 || !strings.Contains(got, "> トークンの近況") {
		t.Errorf("資格情報のファイルの項目で読めない（%d 回）: %q", fileTok.Load(), got)
	}
	// 対照: 環境に URL もあれば環境のトークンを使う
	a.vars["LOOPTRACK_API_URL"], a.vars["LOOPTRACK_PROJECT"] = installURL, u.slug
	a.prompt()
	if envTok.Load() == 0 {
		t.Errorf("環境に URL があるのに環境のトークンを使わない")
	}
}

// runJob は溜めた子の i 本目を動かす（url が空でなければその接続先で）。
func (p *tlTerm) runJob(i int, url string) {
	p.t.Helper()
	j := p.jobs[i]
	vars := clienv.Overlay(clienv.FromMap(p.vars), j.add)
	if url != "" {
		vars = clienv.Overlay(vars, map[string]string{"LOOPTRACK_API_URL": url})
	}
	var out, errb bytes.Buffer
	cli.TimelineMain(j.args[1:], cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, vars)
}

// 送りかけの範囲は始点と終点のまま送り直し、その間に取り込んだほかの人のコミットを入れない。
func TestTimelineHookRetryKeepsRange(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	good := a.vars["LOOPTRACK_API_URL"]
	a.vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
	a.commit("届かなかった自分のコミット")
	a.vars["LOOPTRACK_API_URL"] = good
	a.handCommit(a.dir, "取り込み 1", "取り込み 2")
	a.hook("post-work-complete-handoff-mark", "PostToolUse", map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "git pull"}, "tool_response": map[string]any{"stdout": "Fast-forward", "stderr": ""}})
	a.commitWith([]string{"-q"}, "次の自分のコミット")
	if n := u.count("commit"); n != 2 {
		t.Errorf("送り直しの範囲に取り込んだコミットが入った・自分の分が抜けた: %d（want 2）", n)
	}
}

// 範囲ごとの送りかけは、ほかの範囲の子が送り終えても消えない（後の範囲が届かなかったら次の区切りで送り直す）。
func TestTimelineHookRetryPerRange(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	a.held = true
	a.commit("一つ目")                                  // 子 1（一つ目の範囲）
	a.commit("二つ目")                                  // 子 1 の送り直しと子 2（二つ目の範囲）
	a.runJob(len(a.jobs)-1, "http://127.0.0.1:1/im") // 子 2 は届かない
	a.runJob(0, "")                                  // 子 1 は送れる
	a.held = false
	a.commit("三つ目") // 残っている二つ目の範囲を送り直す
	if n := u.count("commit"); n != 3 {
		t.Errorf("届かなかった範囲が送り直されない: %d（want 3）", n)
	}
}

// 観測の記録が無い作業ツリーで送れなかった 1 件も、次の区切りで送り直す（送りかけを観測より先に書く）。
func TestTimelineHookRetryWithoutObservation(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	other := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", a.dir, "worktree", "add", "-q", "-b", "side", other).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v %s", err, out)
	}
	commitIn := func(subject string) {
		out, err := exec.Command("git", "-C", other, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "--allow-empty", "-m", subject).CombinedOutput()
		if err != nil {
			t.Fatalf("git commit: %v %s", err, out)
		}
		a.hook("post-work-complete-handoff-mark", "PostToolUse", map[string]any{"tool_name": "Bash",
			"tool_input": map[string]any{"command": "cd " + other + " && git commit -m x"}, "tool_response": map[string]any{"stdout": string(out), "stderr": ""}})
	}
	good := a.vars["LOOPTRACK_API_URL"]
	a.vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
	commitIn("観測の無い作業ツリーの 1 件")
	a.vars["LOOPTRACK_API_URL"] = good
	commitIn("その次の 1 件")
	if n := u.count("commit"); n != 2 {
		t.Errorf("観測の無い作業ツリーで送れなかった 1 件が送り直されない: %d（want 2）", n)
	}
}

// 届かない間に溜まった送りかけは、1 回の区切りで古い順に 3 本までしか送り直さない（子を何本も起こさない）。残りは次の区切りで送る。
func TestTimelineHookRetryCapped(t *testing.T) {
	u := newTimelineUsers(t)
	a := u.aliceT
	a.start()
	good := a.vars["LOOPTRACK_API_URL"]
	a.vars["LOOPTRACK_API_URL"] = "http://127.0.0.1:1/im"
	for i := 1; i <= 5; i++ {
		before := len(tlSpawnLog[a])
		a.commit(fmt.Sprintf("届かない %d", i))
		if got, want := len(tlSpawnLog[a])-before, min(i-1, 3)+1; got != want {
			t.Errorf("%d 回目の区切りで起こした子 %d 本（want %d）", i, got, want)
		}
	}
	a.vars["LOOPTRACK_API_URL"] = good
	for i := 1; i <= 3; i++ {
		a.commit(fmt.Sprintf("届く %d", i))
	}
	if n := u.count("commit"); n != 8 {
		t.Errorf("溜まった分が次の区切りで送られない: %d（want 8）", n)
	}
}

// 受け入れ条件: 読む時点で解決する宛先の待機が hook の注入に出る。端末のラベルを宛先にした待機は、そのラベルを在席で名乗るセッションの
// 作業開始とセッション開始に出て、別のラベルを宛てた待機（対照）は出ない。自分以外を宛てた待機は出したセッションには出ない。
func TestTimelineHookResolvedTargetHolds(t *testing.T) {
	u := newTimelineUsers(t)
	u.aliceT.start() // 在席に box-a を名乗る
	u.bobT.start()
	u.bobT.post("hold", "box-a の push を止めてください", "--to-machine", "box-a")
	u.bobT.post("hold", "box-z だけ止めてください", "--to-machine", "box-z")
	u.bobT.post("hold", "B 以外は全員待ってください", "--others")
	got := u.aliceT.prompt()
	for _, want := range []string{"> box-a の push を止めてください", "> B 以外は全員待ってください", "端末 `box-a` のセッション"} {
		if !strings.Contains(got, want) {
			t.Errorf("作業開始の注入に %q が無い: %q", want, got)
		}
	}
	if strings.Contains(got, "box-z だけ") {
		t.Errorf("別の端末を宛てた待機が出た: %q", got)
	}
	if p := u.bobT.prompt(); strings.Contains(p, "B 以外は全員") || strings.Contains(p, "box-a の push") {
		t.Errorf("出したセッションに自分の待機が出た: %q", p)
	}
	// 未了解の待機は続きの SessionStart にも出る
	again := contextOf(u.aliceT.hook("session-start-messages", "SessionStart", map[string]any{"source": "resume"}))
	if !strings.Contains(again, "box-a の push を止めてください") || strings.Contains(again, "box-z だけ") {
		t.Errorf("再開の SessionStart: %q", again)
	}
}

// 上限（注入の 20 行・一覧の行数）を超える数の依頼と待機を置いても、了解していない投稿は確かめるたびにすべて出る。
// 対照として、了解した投稿は出なくなり、了解していない投稿は出続ける。
func TestTimelineHookUnackedSurviveOverflow(t *testing.T) {
	u := newTimelineUsers(t)
	u.aliceT.start()
	const nHold, nReq = 14, 14 // 合わせて 28 件。注入の 20 行も一覧の上限も超える
	label := map[int64]string{}
	var holds, reqs []int64
	for i := 1; i <= nHold; i++ {
		body := fmt.Sprintf("待機 %02d の本文", i)
		id := u.bobT.post("hold", body, "--to", "tl-alice")
		label[id] = body
		holds = append(holds, id)
	}
	for i := 1; i <= nReq; i++ {
		body := fmt.Sprintf("依頼 %02d の本文", i)
		id := u.bobT.post("request", body, "--to", "tl-alice")
		label[id] = body
		reqs = append(reqs, id)
	}
	if len(label) != nHold+nReq {
		t.Fatalf("置いた投稿の ID が重なった: %d 件", len(label))
	}
	// shown は text に本文が出ている投稿の ID を数える（本文の行頭に「> 」が付く）
	shown := func(text string, ids []int64) (got, missing []int64) {
		for _, id := range ids {
			if strings.Contains(text, "> "+label[id]) {
				got = append(got, id)
			} else {
				missing = append(missing, id)
			}
		}
		return got, missing
	}
	all := append(append([]int64{}, holds...), reqs...)
	// 確認を 3 回くり返す。作業開始（待機と依頼）と書き込みの前（待機）の両方で、未了解がすべて出る
	for round := 1; round <= 3; round++ {
		text := u.aliceT.prompt()
		if _, missing := shown(text, all); len(missing) != 0 {
			t.Fatalf("%d 回目の作業開始で未了解の投稿が %d 件消えた（置いた %d 件）: 欠けた ID %v\n%s", round, len(missing), len(all), missing, text)
		}
		u.aliceT.now = u.aliceT.now.Add(61 * time.Second)
		pre := u.aliceT.preTool()
		if _, missing := shown(pre, holds); len(missing) != 0 {
			t.Fatalf("%d 回目の書き込みの前の確認で未了解の待機が %d 件消えた（置いた %d 件）: 欠けた ID %v\n%s", round, len(missing), nHold, missing, pre)
		}
	}
	// 対照: 待機と依頼を 5 件ずつ了解する
	acked := append(append([]int64{}, holds[:5]...), reqs[:5]...)
	for _, id := range acked {
		if code, out, errs := u.aliceT.cli("ack", fmt.Sprint(id)); code != 0 {
			t.Fatalf("ack %d: %q %q", id, out, errs)
		}
	}
	rest := append(append([]int64{}, holds[5:]...), reqs[5:]...)
	for round := 1; round <= 2; round++ {
		text := u.aliceT.prompt()
		if got, _ := shown(text, acked); len(got) != 0 {
			t.Errorf("了解した投稿が %d 回目の作業開始にまた出た: ID %v\n%s", round, got, text)
		}
		if _, missing := shown(text, rest); len(missing) != 0 {
			t.Fatalf("了解していない投稿が %d 回目の作業開始から %d 件消えた（残り %d 件）: 欠けた ID %v\n%s", round, len(missing), len(rest), missing, text)
		}
		u.aliceT.now = u.aliceT.now.Add(61 * time.Second)
		pre := u.aliceT.preTool()
		if got, _ := shown(pre, acked); len(got) != 0 {
			t.Errorf("了解した待機が %d 回目の書き込みの前にまた出た: ID %v\n%s", round, got, pre)
		}
		if _, missing := shown(pre, holds[5:]); len(missing) != 0 {
			t.Fatalf("了解していない待機が %d 回目の書き込みの前から %d 件消えた: 欠けた ID %v\n%s", round, len(missing), missing, pre)
		}
	}
}
