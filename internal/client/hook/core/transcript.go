package core

// 会話記録を自動で送る hook（DESIGN.md §9-8「hook」）。`looptrack hook transcript --agent <AI>` を Stop と SessionEnd に配線する
// （Claude Code と Codex。internal/client/kitinit の coreWants）。作りは使用量の hook（usage.go）と同じ。
//
//   - 送るのは 2 つの鍵がどちらも入のときだけ: 端末ごとのオプトイン LOOPTRACK_SESSION_TRANSCRIPTS=1（usagesnap.TranscriptsOptIn）と、
//     覚えたプロジェクトのルール session.transcripts（usagesnap.TranscriptsRule）。覚えた切が 1 日より新しければ何もしない。
//     覚えた値が無いか 1 日より古ければ、子プロセスの push の登録の要求がルールを確かめる（切なら 422 で、push が切を覚える）。
//   - Stop は前回から LOOPTRACK_TRANSCRIPT_THROTTLE_MIN 分（既定 10）未満なら何もしない（usage の throttled と同じ。記録は
//     transcript-last/<セッション ID>）。SessionEnd は間引かない。
//   - 送るのは切り離した子プロセス（`looptrack hook transcript --transcript-job`。仕事は標準入力の JSON）で、hook はすぐ返る
//     （LOOPTRACK_TRANSCRIPT_FOREGROUND=1 なら同じプロセスで送る。調べるときだけ使う）。子は手で打つ push と同じ本体
//     （cli.PushTranscriptForHook → cmdTranscriptPush）を呼ぶ。
//   - 送れなかったときは「どれを送るか」だけを退避し（usagesnap.TranscriptSpoolDir。中身は入れない）、次の子プロセスが元のファイルから
//     続きを送る。7 日で捨てる。変換が止めた記録（読めない行・キーの秘密・長すぎる行）は退避せず、印（TranscriptRejectedDir）を
//     置いて、失敗を 1 回だけ記録する（同じ記録は何度試しても同じ結果になるので、自動では試し直さない）。
//   - 失敗は usagesnap.RecordTranscriptFailure に残り、SessionStart の要約（summary）が 1 行出す。
//
// hook は何も出さない（差し戻さない・知らせない）。出力は LOOPTRACK_USAGE_DEBUG のときの標準エラーだけ（使用量の hook と同じ変数・同じ文面）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

const (
	// transcriptJobFlag は切り離した子プロセスの印（利用者は使わない）。
	transcriptJobFlag = "--transcript-job"
	transcriptLastDir = "transcript-last"
	// transcriptRuleRecheck は覚えた「切」を信じる長さ（これより古ければ子プロセスが登録で確かめ直す）。
	transcriptRuleRecheck = 24 * time.Hour
	// transcriptThrottleMin は Stop の間引きの既定（分）。
	transcriptThrottleMin = 10
)

// TranscriptChildArgs は会話記録の送信を切り離した子プロセスの引数（実行ファイルの後ろ）。
var TranscriptChildArgs = []string{"hook", "transcript", transcriptJobFlag}

// transcriptJob は切り離した子プロセスに渡す仕事（標準入力の JSON）。退避のファイルも同じ形（中身は入れない）。
type transcriptJob struct {
	Trigger        string `json:"trigger"`
	Agent          string `json:"agent"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	CWD            string `json:"cwd,omitempty"`
}

// safeSessionRe はファイル名に使うセッション ID の形（状態のファイル・退避・印の名前になるので、区切りを含むものは使わない）。
var safeSessionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validTranscriptJob は仕事の agent とセッション ID が、ファイル名に使ってよい形か（agent は Claude Code と Codex だけ）。
func validTranscriptJob(j transcriptJob) bool {
	return (j.Agent == usagesnap.ClientClaudeCode || j.Agent == usagesnap.ClientCodex) && safeSessionRe.MatchString(j.SessionID)
}

// transcriptVars は鍵と置き場を決める環境（LOOPTRACK_PROJECT が無く .claude/issues から決めたときも、同じプロジェクトの鍵になるよう足す）。
func (c *Call) transcriptVars() (env.Env, bool) {
	slug, err := c.project()
	if err != nil || c.apiBase() == "" {
		return env.Env{}, false
	}
	return env.Overlay(*c.Vars, map[string]string{env.Name(env.Project): slug}), true
}

// transcriptAgent は送る AI（配線の --agent。推測したときは usage と同じ推測）。対象は Claude Code と Codex だけ。
func (c *Call) transcriptAgent(ev hookio.Event) string {
	a := string(ev.Agent)
	if ev.AgentGuessed || a == "" {
		a = c.client(ev)
	}
	switch a {
	case usagesnap.ClientClaudeCode, usagesnap.ClientCodex:
		return a
	}
	return ""
}

// transcriptGates は 2 つの鍵（オプトインと覚えたルール）。覚えた切が新しいときだけ false（覚えた値が無い・古いときは子が確かめる）。
func transcriptGates(vars env.Env, now time.Time) bool {
	if !usagesnap.TranscriptsOptIn(vars) {
		return false
	}
	on, at, ok := usagesnap.TranscriptsRule(usagesnap.Options{Env: vars})
	if ok && !on && now.Sub(at) < transcriptRuleRecheck {
		return false
	}
	return true
}

// transcriptLastFile は Stop の間引きの記録（置き場が分からなければ ""）。
func (c *Call) transcriptLastFile(sid string) string {
	d := c.stateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, transcriptLastDir, sid)
}

// transcriptThrottled は Stop を間引くか（前回から LOOPTRACK_TRANSCRIPT_THROTTLE_MIN 分未満）。SessionEnd は間引かない。
func (c *Call) transcriptThrottled(sid, trigger string) bool {
	if trigger != "stop" {
		return false
	}
	minutes := transcriptThrottleMin
	if v := c.setting("TRANSCRIPT_THROTTLE_MIN"); v != "" && isDigits(v) {
		minutes, _ = strconv.Atoi(v)
	}
	if minutes <= 0 {
		return false
	}
	p := c.transcriptLastFile(sid)
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	return c.Now().Sub(fi.ModTime()) < time.Duration(minutes)*time.Minute
}

// markTranscriptTried は間引きの起点を書く（子を起こした時点。送れなかった分は退避が受け持つ）。
func (c *Call) markTranscriptTried(sid string) {
	p := c.transcriptLastFile(sid)
	if p == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	now := c.Now()
	if os.WriteFile(p, []byte(strconv.FormatInt(now.Unix(), 10)), 0o644) == nil {
		_ = os.Chtimes(p, now, now) // テストの時計（Env.Now）でも間引けるように
	}
}

// Transcript は `looptrack hook transcript`。出力は無い（送信は切り離した子プロセスで行う）。
func Transcript(ctx context.Context, c *Call, ev hookio.Event) (hookio.Result, error) {
	var trigger string
	switch ev.Name {
	case hookio.Stop:
		trigger = "stop"
	case hookio.SessionEnd:
		trigger = "session_end"
	default:
		return hookio.Result{}, nil
	}
	// サーバの URL かプロジェクトが無ければ何もしない（usage と同じ。looptrack を使っていないリポジトリでも起動しうる）
	vars, ok := c.transcriptVars()
	if !ok || !transcriptGates(vars, c.Now()) {
		return hookio.Result{}, nil
	}
	agent := c.transcriptAgent(ev)
	sid := ev.SessionID
	if sid == "" {
		sid = rawString(ev.Raw, "session_id", "sessionId")
	}
	if agent == "" || !safeSessionRe.MatchString(sid) {
		return hookio.Result{}, nil
	}
	if c.transcriptThrottled(sid, trigger) {
		return hookio.Result{}, nil
	}
	cwd := ev.CWD
	if cwd == "" {
		cwd = c.Getwd()
	}
	job := transcriptJob{Trigger: trigger, Agent: agent, SessionID: sid, TranscriptPath: ev.TranscriptPath, CWD: cwd}
	c.markTranscriptTried(sid)
	if c.setting("TRANSCRIPT_FOREGROUND") != "1" {
		b, _ := json.Marshal(job)
		err := c.SpawnTranscript(b, transcriptChildDir(cwd, c.Getwd()))
		if err == nil {
			return hookio.Result{}, nil
		}
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.detach_failed", "reason", err))
	}
	c.transcriptWork(ctx, job)
	return hookio.Result{}, nil
}

// transcriptChildDir は子プロセスの作業ディレクトリ（入力の cwd があればそこ。push は作業ディレクトリから git のルートを決める）。
func transcriptChildDir(cwd, fallback string) string {
	if fi, err := os.Stat(cwd); cwd != "" && err == nil && fi.IsDir() {
		return cwd
	}
	return fallback
}

// runTranscriptJob は切り離した子プロセスの本体（標準入力の transcriptJob を送る）。
func runTranscriptJob(ctx context.Context, c *Call, stdin io.Reader) {
	defer hookio.Recover(nil, nil)
	b, err := io.ReadAll(io.LimitReader(stdin, hookio.MaxInput))
	if err != nil {
		return
	}
	var job transcriptJob
	if json.Unmarshal(b, &job) != nil || !validTranscriptJob(job) {
		return
	}
	c.transcriptWork(ctx, job)
}

// transcriptWork は退避してある記録と今回の記録を順に送る。届かなければ残りを退避してやめる。
func (c *Call) transcriptWork(_ context.Context, job transcriptJob) {
	defer hookio.Recover(nil, func(p any, _ []byte) {
		c.debug("%s", i18n.T(c.lang(), "core.usage.debug.failed", "reason", fmt.Sprint(p)))
	})
	vars, ok := c.transcriptVars()
	if !ok {
		return
	}
	state := c.stateDir()
	apiURL, slug := vars.Value(env.APIURL), vars.Value(env.Project)
	spoolDir := usagesnap.TranscriptSpoolDir(state, apiURL, slug)
	rejectDir := usagesnap.TranscriptRejectedDir(state, apiURL, slug)
	now := c.Now()
	expireTranscriptFiles(state, now)

	jobs := append(readTranscriptSpool(spoolDir), job)
	seen := map[string]bool{}
	for i, j := range jobs {
		name := j.Agent + "-" + j.SessionID + ".json"
		if seen[name] || !validTranscriptJob(j) {
			continue
		}
		seen[name] = true
		if rejectDir != "" && fileExists(filepath.Join(rejectDir, name)) {
			removeIn(spoolDir, name) // 変換が止めた記録は自動では試し直さない
			continue
		}
		r := cli.PushTranscriptForHook(vars, j.Agent, j.SessionID)
		switch r.Outcome {
		case cli.HookPushSent:
			usagesnap.ClearTranscriptFailure(state)
			removeIn(spoolDir, name)
		case cli.HookPushRuleOff:
			// ルールが切（push が切を覚えた）。送れるものは無いので、ここでやめる（退避は 7 日で消える）
			usagesnap.ClearTranscriptFailure(state)
			return
		case cli.HookPushRejected:
			removeIn(spoolDir, name)
			if writeOnce(rejectDir, name, now) {
				usagesnap.RecordTranscriptFailure(state, now, usagesnap.FailTranscriptRejected, 0, r.Files)
			}
		case cli.HookPushRewritten:
			removeIn(spoolDir, name)
			usagesnap.RecordTranscriptFailure(state, now, usagesnap.FailTranscriptRewritten, 0, 0)
		case cli.HookPushRefused:
			removeIn(spoolDir, name)
			usagesnap.RecordTranscriptFailure(state, now, usagesnap.FailRejected, r.Status, 0)
		case cli.HookPushNotFound:
			removeIn(spoolDir, name) // 記録が無い（消えた・別の AI のセッション）。失敗としては残さない
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.no_transcript", "client", j.Agent, "path", j.SessionID))
		default: // HookPushRetry
			reason := usagesnap.FailUnreachable
			switch {
			case r.NoToken:
				reason = usagesnap.FailNoToken
			case r.Status == 0 && !r.Conn:
				reason = usagesnap.FailClient
			}
			c.debug("%s", i18n.T(c.lang(), "core.usage.debug.send_failed", "reason", i18n.Text(c.lang(), r.Err)))
			usagesnap.RecordTranscriptFailure(state, now, reason, 0, 0)
			for _, rest := range jobs[i:] {
				if !c.spoolTranscript(spoolDir, rest) {
					usagesnap.RecordTranscriptFailure(state, now, usagesnap.FailSpool, 0, 0)
				}
			}
			return
		}
	}
}

// spoolTranscript は「どれを送るか」だけを退避する（中身は入れない。同じ記録は 1 つのファイルにまとめ、最初に退避した時刻を残す）。
func (c *Call) spoolTranscript(dir string, j transcriptJob) bool {
	if dir == "" || !validTranscriptJob(j) {
		return false
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return false
	}
	p := filepath.Join(dir, j.Agent+"-"+j.SessionID+".json")
	if fileExists(p) {
		return true // 退避した時刻（更新時刻）を延ばさない（7 日で捨てる起点）
	}
	b, _ := json.Marshal(j)
	return os.WriteFile(p, b, 0o600) == nil
}

// readTranscriptSpool は退避してある記録（古い順）。
func readTranscriptSpool(dir string) []transcriptJob {
	var out []transcriptJob
	paths := usagesnap.SpoolFiles(dir)
	sort.Slice(paths, func(i, j int) bool {
		a, _ := os.Stat(paths[i])
		b, _ := os.Stat(paths[j])
		if a == nil || b == nil {
			return paths[i] < paths[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var j transcriptJob
		if json.Unmarshal(b, &j) != nil || !validTranscriptJob(j) || filepath.Base(p) != j.Agent+"-"+j.SessionID+".json" {
			_ = os.Remove(p) // 読めない・名前と中身が食い違うものは送り先が分からないので捨てる
			continue
		}
		out = append(out, j)
	}
	return out
}

// expireTranscriptFiles は、すべてのプロジェクトの退避と印から 7 日より古いものを捨てる
// （そのプロジェクトのセッションが来ないと送り直しも掃除もされないので、ここで捨てる）。
func expireTranscriptFiles(state string, now time.Time) {
	if state == "" {
		return
	}
	for _, d := range []string{usagesnap.TranscriptSpoolDirName, usagesnap.TranscriptRejectedDirName} {
		paths, _ := filepath.Glob(filepath.Join(state, d, "*", "*.json"))
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && now.Sub(fi.ModTime()).Seconds() > spoolKeepSec {
				_ = os.Remove(p)
			}
		}
	}
	paths, _ := filepath.Glob(filepath.Join(state, transcriptLastDir, "*"))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && now.Sub(fi.ModTime()).Seconds() > spoolKeepSec {
			_ = os.Remove(p)
		}
	}
}

// writeOnce は dir/name に印を置く（既にあれば何もしないで false。置いたら true）。中身は印を置いた時刻だけ。
func writeOnce(dir, name string, now time.Time) bool {
	if dir == "" {
		return true // 置き場が無くても失敗は記録する（印が無いので次も試すが、黙って失うよりよい）
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return true
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return !os.IsExist(err)
	}
	_, _ = f.WriteString(`{"at":` + strconv.FormatInt(now.Unix(), 10) + "}")
	_ = f.Close()
	_ = os.Chtimes(filepath.Join(dir, name), now, now)
	return true
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func removeIn(dir, name string) {
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
