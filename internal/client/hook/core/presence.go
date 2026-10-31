package core

// 在席を送る（DESIGN.md §9-9）。既にある配線に乗せる: SessionStart は summary、Stop と SessionEnd は usage の hook が呼ぶ。
// 別の配線を足さないので、init をやり直していないプロジェクトでも動く。
// 長いターンの間も一覧から消えないよう、PostToolUse と UserPromptSubmit でも presenceBeat に 1 回まで送る
// （Claude Code は issue-freshness-mark、ほかの AI は usage の配線。どの本数も増えない）。
//
// 送るのは usage の切り離した子プロセスで、hook はすぐ返る（サーバに届かなくても AI を待たせない）。届かない・古いサーバ・拒まれたときも
// 何も残さず、次の Stop でまた送る（15 分で消える情報なので退避しない）。
// 作業ツリーの絶対パスは送らない（ホームディレクトリの利用者名がほかの参加者に見えるため）。送るのはディレクトリの名前だけ。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/hookio"
)

const (
	presenceTimeout    = 3 * time.Second // 1 回の要求の打ち切り
	presenceGitTimeout = 2 * time.Second
	maxPresenceLabel   = 64
	// presenceBeat はターンの途中で送る間隔（一覧から消える 15 分より十分短く）。
	presenceBeat    = 5 * time.Minute
	presenceLastDir = "presence-last"
)

// presenceJob は切り離した子プロセスに渡す仕事（usageJob.Presence）。
type presenceJob struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	// Ended はセッションの終わり（在席を外す）
	Ended bool `json:"ended,omitempty"`
}

// reportPresence は在席の送信を切り離す。どこで失敗しても呼んだ hook の本体には影響させない（panic も含む）。
func (c *Call) reportPresence(ev hookio.Event) {
	defer hookio.Recover(nil, func(p any, _ []byte) { c.debug("presence: %v", p) })
	beat := false
	switch ev.Name {
	case hookio.SessionStart, hookio.Stop, hookio.SessionEnd:
	case hookio.PostToolUse, hookio.UserPromptSubmit:
		beat = true
	default:
		return
	}
	sid := ev.SessionID
	if sid == "" {
		sid = rawString(ev.Raw, "session_id", "sessionId")
	}
	if sid == "" || c.apiBase() == "" || c.Spawn == nil {
		return
	}
	last := c.presenceLastFile(sid)
	if beat {
		if last == "" {
			return // 間引きの記録を置けないなら、ツールの呼び出しごとに子を起こさない
		}
		if fi, err := os.Stat(last); err == nil && c.Now().Sub(fi.ModTime()) < presenceBeat {
			return
		}
	}
	if _, err := c.project(); err != nil {
		return
	}
	agent := string(ev.Agent)
	if ev.AgentGuessed || agent == "" {
		agent = c.client(ev)
	}
	cwd := ev.CWD
	if cwd == "" {
		cwd = c.Getwd()
	}
	b, err := json.Marshal(usageJob{Presence: &presenceJob{Agent: agent, SessionID: sid, CWD: cwd, Ended: ev.Name == hookio.SessionEnd}})
	if err != nil {
		return
	}
	c.markPresence(last)
	if err := c.Spawn(b, c.Getwd()); err != nil {
		c.debug("presence: %v", err)
	}
}

// presenceLastFile は間引きの記録の置き場（置き場が分からない・ファイル名に使えないセッション ID なら ""）。
func (c *Call) presenceLastFile(sid string) string {
	d := c.stateDir()
	if d == "" || !safeSessionRe.MatchString(sid) {
		return ""
	}
	return filepath.Join(d, presenceLastDir, sid)
}

// markPresence は間引きの起点を書く（子を起こした時点。届かなくても次の Stop で送る）。
func (c *Call) markPresence(p string) {
	if p == "" || os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	now := c.Now()
	if os.WriteFile(p, nil, 0o644) == nil {
		_ = os.Chtimes(p, now, now) // テストの時計（Env.Now）でも間引けるように
	}
}

// expirePresenceMarks は古い間引きの記録を捨てる。
func (c *Call) expirePresenceMarks() {
	d := c.stateDir()
	if d == "" {
		return
	}
	paths, _ := filepath.Glob(filepath.Join(d, presenceLastDir, "*"))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && c.Now().Sub(fi.ModTime()).Seconds() > spoolKeepSec {
			_ = os.Remove(p)
		}
	}
}

// sendPresence は切り離した子プロセスの本体。結果は誰にも見せない（次の Stop でまた送る）。
func (c *Call) sendPresence(j presenceJob) {
	if strings.TrimSpace(j.SessionID) == "" {
		return
	}
	c.expirePresenceMarks()
	slug, err := c.project()
	if err != nil {
		return
	}
	vars := session.WithHookSession(*c.Vars, j.SessionID)
	cl, err := api.NewWithTimeout(vars, presenceTimeout)
	if err != nil {
		return
	}
	path := "/projects/" + api.PathEscape(slug) + "/presence"
	req := api.Request{Method: http.MethodDelete, Path: path}
	if !j.Ended {
		branch, head, dir := presenceGit(j.CWD, presenceHomes(vars))
		req = api.Request{Method: http.MethodPost, Path: path, Body: map[string]string{"agent": j.Agent, "machine_label": presenceWord(presenceLabel(vars)),
			"branch": presenceWord(branch), "head_sha": head, "directory": presenceWord(dir)}}
	}
	if _, err := cl.Do(req); err != nil {
		c.debug("presence: %s", fmt.Sprint(err))
	}
}

// presenceLabel は端末のラベル（LOOPTRACK_MACHINE_LABEL、無ければホスト名の最初の「.」より前）。
func presenceLabel(vars env.Env) string {
	label := strings.TrimSpace(vars.Value(env.MachineLabel))
	if label == "" {
		host, _ := os.Hostname()
		label, _, _ = strings.Cut(host, ".")
	}
	if r := []rune(label); len(r) > maxPresenceLabel {
		label = string(r[:maxPresenceLabel])
	}
	return label
}

// presenceWord はサーバが受け取れない形の値を空にする（空白を含むディレクトリの名前や長いブランチのせいで、セッションごと一覧から消えないように）。
func presenceWord(v string) string {
	if domain.PresenceText(v) {
		return v
	}
	return ""
}

// presenceHomes はホームディレクトリの候補。
func presenceHomes(vars env.Env) []string {
	var out []string
	for _, k := range []string{"HOME", "USERPROFILE"} {
		if v := vars.Get(k); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// samePath は 2 つのパスが同じ場所か（git は実パスを返すので、シンボリックリンクを解いてから比べる）。
func samePath(a, b string) bool {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Clean(r)
		}
		return filepath.Clean(p)
	}
	return real(a) == real(b)
}

// presenceGit はブランチ・HEAD の SHA・作業ツリーのディレクトリの名前を返す。git の外では 3 つとも空。
// 作業ツリーがホームやルートならディレクトリの名前も空にする（最後の要素が利用者名になるため）。
func presenceGit(cwd string, homes []string) (branch, head, dir string) {
	ask := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), presenceGitTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = cwd
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	top := ask("rev-parse", "--show-toplevel")
	if top == "" {
		return "", "", ""
	}
	dir = filepath.Base(top)
	if filepath.Dir(top) == top {
		dir = ""
	}
	for _, h := range homes {
		if samePath(top, h) {
			dir = ""
		}
	}
	return ask("rev-parse", "--abbrev-ref", "HEAD"), ask("rev-parse", "HEAD"), dir
}
