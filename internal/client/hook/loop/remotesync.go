package loop

// session-start-remote-sync: セッションの開始時に fetch して、いま居る作業ツリーとリモートの差を 1 行知らせる。
//
// 複数の端末・利用者で進めると、別の環境の push はセッションの一覧にも手元の参照にも現れない。
// user-prompt-stale-base は fetch を打たないので遅れの下限しか言えず、毎ターンの入力にネットワークの待ちも乗せられない。
// 書き込みを始める前の 1 回だけは待ってよいので、ここでは SessionStart で fetch する。
//
//   - 比べる相手はブランチの上流。上流が無い（作業ツリーの作業用ブランチ・detached）なら stale-base と同じ基準（origin/main など）。
//   - 遅れ・乖離・push していない先行のどれかのときだけ 1 行出す。同期しているときは何も出さない。何も止めない。
//   - fetch は間隔に上限を付ける（LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL）。fetch はリポジトリ全体に効くので、
//     印は共通の git のディレクトリにリモートごとに置く（同じリポジトリの作業ツリーで同時に開いたセッションが並べて fetch しない）。
//     間隔の内側で次のセッションが開いたら fetch せず、前回の結果の参照で比べる。
//   - fetch が失敗・時間切れのとき、次の間隔が来るまでは何も出さない（fail-open）。古い参照で「同期している」とも
//     「遅れている」とも言えないから。ネットワークの無い環境で毎回 fetch の時間切れを待たないよう、失敗も印に残す。
//   - fetch の待ちは本体の打ち切りより短い（既定 5 秒）。時間切れの git は SIGTERM で止める。SIGKILL だと参照の更新中に
//     .lock が残り、利用者の次の fetch や pull まで失敗し続けるから。孫のプロセス（ssh など）が標準エラーを握っていると
//     起動の戻りが遅れるので、猶予を過ぎたら戻りを待たずに抜ける。
//   - 上流がリモートでない（branch.<名前>.remote が "."）ブランチは比べない。
//   - fetch するのは上流のリモート（無ければ origin）だけ。LOOPTRACK_LOOP_STALE_BASE_REF で別のリモートの基準を指しても、
//     そのリモートは fetch しない。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

const (
	// DefaultRemoteSyncInterval は fetch の間隔の下限の既定（LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL で変える）。
	DefaultRemoteSyncInterval = 10 * time.Minute
	// DefaultRemoteSyncFetchTimeout は 1 回の fetch の待ちの上限の既定（LOOPTRACK_LOOP_REMOTE_SYNC_FETCH_TIMEOUT で変える）。
	DefaultRemoteSyncFetchTimeout = 5 * time.Second
	// remoteSyncFetchGrace は時間切れの git が後始末を終えるのを待つ長さ。
	remoteSyncFetchGrace = 500 * time.Millisecond

	remoteSyncMarker = "looptrack-remote-sync"
	remoteSyncOK     = "ok"
	remoteSyncFailed = "failed"
	remoteSyncActive = "fetching" // fetch の途中で hook が打ち切られると、この値のまま残る（失敗と同じに扱う）
)

// remoteSyncMarkerName は印のファイル名。リモートには URL も長い名前も書けるので、名前そのものではなくハッシュの先頭を使う。
func remoteSyncMarkerName(remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return remoteSyncMarker + "-" + hex.EncodeToString(sum[:8])
}

// SessionStartRemoteSync は fetch の後のリモートとの差を、差があるときだけ 1 行文脈に入れる（何も止めない）。
func SessionStartRemoteSync(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	switch strings.ToLower(trimSpace(e.env("LOOPTRACK_LOOP_REMOTE_SYNC_NOTICE"))) {
	case "0", "off", "false", "no":
		return hookio.Result{}, nil // 利用者が切った
	}
	dir := ev.CWD
	if dir == "" {
		dir = root(ev, e)
	}
	top := gitTop(dir)
	if top == "" {
		return hookio.Result{}, nil // git のリポジトリでない
	}
	remote := remoteSyncRemote(ctx, e, top)
	if remote == "." {
		return hookio.Result{}, nil // 上流が手元のブランチ
	}
	if !remoteSyncFetched(ctx, e, top, remote) {
		return hookio.Result{}, nil
	}
	ref := remoteSyncRef(ctx, e, top)
	if ref == "" {
		return hookio.Result{}, nil // 上流も基準のブランチも無い
	}
	out, ok := gitLine(ctx, e, top, "rev-list", "--left-right", "--count", "HEAD..."+ref)
	if !ok {
		return hookio.Result{}, nil
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return hookio.Result{}, nil
	}
	ahead, err1 := strconv.Atoi(f[0])
	behind, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return hookio.Result{}, nil
	}
	note := remoteSyncNote(e.lang(), staleBaseName(ctx, e, top), ref, ahead, behind)
	if note == "" {
		return hookio.Result{}, nil
	}
	return hookio.Result{Context: note, Kind: "remote-sync"}, nil
}

// remoteSyncFetched は、比べてよい参照があるか（間隔の内側なら前回の結果、外側なら fetch の結果）。
func remoteSyncFetched(ctx context.Context, e *Env, top, remote string) bool {
	interval := DefaultRemoteSyncInterval
	if v := trimSpace(e.env("LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	marker := ""
	if g := gitCommonDir(top); g != "" {
		marker = filepath.Join(g, remoteSyncMarkerName(remote))
	}
	now := e.Now()
	if at, state, ok := readRemoteSyncMarker(marker); ok {
		if age := now.Sub(at); age >= 0 && age < interval {
			return state == remoteSyncOK
		}
	}
	// fetch の前に印を置く。同じリポジトリの別の作業ツリーで同時に開いたセッションが fetch を重ねないように
	writeRemoteSyncMarker(marker, now, remoteSyncActive)
	state := remoteSyncFailed
	if remoteSyncFetch(ctx, e, top, remote) {
		state = remoteSyncOK
	}
	writeRemoteSyncMarker(marker, now, state)
	return state == remoteSyncOK
}

// remoteSyncFetch は 1 回 fetch する。失敗・時間切れは false。
func remoteSyncFetch(ctx context.Context, e *Env, top, remote string) bool {
	limit := DefaultRemoteSyncFetchTimeout
	if v := trimSpace(e.env("LOOPTRACK_LOOP_REMOTE_SYNC_FETCH_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			limit = d
		}
	}
	c, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		defer hookio.Recover(nil, nil)
		// 認証の入力を待たせない。hook の中の保守も走らせない（時間の読めない処理を SessionStart に乗せない）
		_, code, err := e.Run(c, Command{
			Dir:   top,
			Env:   append(e.Environ(), "GIT_TERMINAL_PROMPT=0"),
			Name:  "git",
			Args:  []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "fetch", "--quiet", remote},
			Grace: remoteSyncFetchGrace,
		})
		done <- err == nil && code == 0
	}()
	select {
	case ok := <-done:
		return ok
	case <-c.Done():
	}
	// 時間切れの結果は使わない。SIGTERM を受けた git が後始末を終えるまでは、hook のプロセスを終わらせない
	select {
	case <-done:
	case <-time.After(remoteSyncFetchGrace):
	}
	return false
}

// remoteSyncRemote は fetch するリモート（上流のリモート。無ければ origin）。
func remoteSyncRemote(ctx context.Context, e *Env, top string) string {
	branch, _ := gitLine(ctx, e, top, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "" && branch != "HEAD" {
		if r, ok := gitLine(ctx, e, top, "config", "--get", "branch."+branch+".remote"); ok && r != "" {
			return r
		}
	}
	return "origin"
}

// remoteSyncRef は比べる相手（ブランチの上流。無ければ stale-base と同じ基準。どちらも無ければ ""）。
func remoteSyncRef(ctx context.Context, e *Env, top string) string {
	if u, ok := gitLine(ctx, e, top, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); ok && u != "" {
		return u
	}
	for _, c := range staleBaseCandidates(e) {
		if sha, ok := gitLine(ctx, e, top, "rev-parse", "--verify", "--quiet", c+"^{commit}"); ok && sha != "" {
			return c
		}
	}
	return ""
}

// readRemoteSyncMarker は印（「Unix 秒 状態」の 1 行）を読む。無い・壊れているときは ok = false（fetch の間隔が過ぎたのと同じ）。
func readRemoteSyncMarker(path string) (at time.Time, state string, ok bool) {
	if path == "" {
		return time.Time{}, "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, "", false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return time.Time{}, "", false
	}
	sec, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	return time.Unix(sec, 0), f[1], true
}

// writeRemoteSyncMarker は印を書く。書けなくても止めない（次のセッションがまた fetch するだけ）。
func writeRemoteSyncMarker(path string, at time.Time, state string) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.FormatInt(at.Unix(), 10)+" "+state+"\n"), 0o666)
}

// remoteSyncNote は注入する 1 行。差が無ければ空を返す。
func remoteSyncNote(lang i18n.Lang, name, ref string, ahead, behind int) string {
	switch {
	case ahead > 0 && behind > 0:
		return i18n.T(lang, "loop.remotesync.diverged", "name", name, "ref", ref, "ahead", ahead, "behind", behind)
	case behind > 0:
		return i18n.T(lang, "loop.remotesync.behind", "name", name, "ref", ref, "n", behind)
	case ahead > 0:
		return i18n.T(lang, "loop.remotesync.ahead", "name", name, "ref", ref, "n", ahead)
	}
	return ""
}
