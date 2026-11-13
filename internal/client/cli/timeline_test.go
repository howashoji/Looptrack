package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

// タイムラインの経路が無い古いサーバ（404 unknown_api）では「このサーバは対応していない」と言って終了コード 1 で止まる（占有と同じ扱い）。
// 対照: 経路のあるサーバが返した 404（書き込みが無い）は、サーバの文面のまま出す。
func TestTimelinePostOldServerIsUnsupported(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.HasSuffix(r.URL.Path, "/projects/demo/timeline/9") {
			writeJSONResp(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "タイムラインの投稿 9 は見つかりません"}})
			return
		}
		writeJSONResp(w, 404, map[string]any{"error": map[string]any{"code": "unknown_api", "message": r.URL.Path}})
	}))
	defer srv.Close()
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": srv.URL, "LOOPTRACK_PROJECT": "demo", "LOOPTRACK_TOKEN": "lt_test", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"CLAUDE_PROJECT_DIR": t.TempDir()}
	run := func(args ...string) (int, string) {
		var out, errb strings.Builder
		code := TimelineMain(args, IO{Stdin: strings.NewReader("本文"), Stdout: &out, Stderr: &errb}, env.FromMap(vars))
		return code, errb.String()
	}
	for _, args := range [][]string{{"list"}, {"check"}, {"post", "info", "知らせ", "--all"}} {
		if code, errs := run(args...); code != 1 || !strings.Contains(errs, "このサーバは タイムラインに対応していません") {
			t.Errorf("%v: %d %q", args, code, errs)
		}
	}
	code, errs := run("show", "9")
	if hits == 0 {
		t.Fatal("前提が崩れています（要求がサーバに届いていない）")
	}
	if code != 1 || !strings.Contains(errs, "タイムラインの投稿 9 は見つかりません") || strings.Contains(errs, "対応していません") {
		t.Errorf("経路のある 404: %d %q", code, errs)
	}
}

// 名乗った値にバッククォートが入っていたら囲みを抜けないよう載せない。対照: 普通の値は囲み、空は - になる。
func TestTimelinePostClaimKeepsBackquotesInside(t *testing.T) {
	for in, want := range map[string]string{"box-a": "`box-a`", "": "-", "a`b": "?", "`": "?", "x` 地の文 `y": "?"} {
		if got := timelinePostClaim(in); got != want {
			t.Errorf("timelinePostClaim(%q) = %q, want %q", in, got, want)
		}
	}
}

// commit は --since の後から HEAD までのうち新しい 20 件を古い順に送り、超えた分を omitted で送る。ブランチは省くといまのブランチ。
// 対照として --since を省くと HEAD の 1 件だけを送る。
func TestTimelineCommitSendsRange(t *testing.T) {
	defer func(g func(string, ...string) (string, error)) { gitOutput = g }(gitOutput)
	var gitCalls []string
	gitOutput = func(dir string, args ...string) (string, error) {
		gitCalls = append(gitCalls, strings.Join(args, " "))
		switch {
		case args[0] == "rev-list":
			return "23\n", nil
		case args[0] == "rev-parse":
			return "feat-x\n", nil
		case args[0] == "log" && args[len(args)-1] == "HEAD":
			return "cccccccccc\x1f最新\n", nil
		}
		var b strings.Builder
		for i := 23; i > 3; i-- { // git log は新しい順
			b.WriteString(strings.Repeat(string(rune('a'+i%6)), 10) + "\x1f件名 " + strconv.Itoa(i) + "\n")
		}
		return b.String(), nil
	}
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/demo/timeline/commits" {
			writeJSONResp(w, 404, map[string]any{"error": map[string]any{"code": "unknown_api", "message": r.URL.Path}})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		writeJSONResp(w, 200, map[string]any{"recorded": len(body["commits"].([]any)), "omitted": body["omitted"], "withheld": 0})
	}))
	defer srv.Close()
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": srv.URL + "/api/v1", "LOOPTRACK_PROJECT": "demo", "LOOPTRACK_TOKEN": "lt_test", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"CLAUDE_PROJECT_DIR": t.TempDir()}
	run := func(args ...string) (int, string, string) {
		var out, errb strings.Builder
		code := TimelineMain(args, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(vars))
		return code, out.String(), errb.String()
	}
	if code, out, errs := run("commit", "--since", "0123456"); code != 0 || strings.TrimSpace(out) != "recorded=20 omitted=3 withheld=0" {
		t.Fatalf("--since: %d %q %q %v", code, out, errs, gitCalls)
	}
	if len(got) != 1 || got[0]["branch"] != "feat-x" || got[0]["omitted"] != float64(3) {
		t.Fatalf("送った本文: %v", got)
	}
	commits := got[0]["commits"].([]any)
	if len(commits) != 20 || commits[0].(map[string]any)["subject"] != "件名 4" || commits[19].(map[string]any)["subject"] != "件名 23" {
		t.Errorf("コミットの並び（古い順のはず）: %v", commits)
	}
	if code, out, errs := run("commit", "--branch", "main"); code != 0 || strings.TrimSpace(out) != "recorded=1 omitted=0 withheld=0" {
		t.Fatalf("対照: HEAD だけ: %d %q %q", code, out, errs)
	}
	if c := got[1]["commits"].([]any); got[1]["branch"] != "main" || len(c) != 1 || c[0].(map[string]any)["sha"] != "cccccccccc" {
		t.Errorf("HEAD だけの本文: %v", got[1])
	}
}

// commit の範囲を一時ディレクトリの実際の git リポジトリで確かめる（git を差し替えない）。
// 自分の線（--first-parent）だけを送り、merge で取り込んだほかの人のコミットを載せない。since が HEAD の先祖でない（rebase や amend で
// 書き換わった・無い）なら HEAD の 1 件だけ。初回（since 無し・コミットが 1 つ）も HEAD の 1 件。20 件を超えたら新しい 20 件と全体の件数。
func TestTimelineCommitLogRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git が無い")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string) string {
		git("commit", "-q", "--allow-empty", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	subjects := func(cs [][2]string) []string {
		out := []string{}
		for _, c := range cs {
			out = append(out, c[1])
		}
		return out
	}
	git("init", "-q", "-b", "main")
	c1 := commit("c1")
	// 初回: since 無し・コミットが 1 つ
	if cs, total, err := timelineCommitLog(dir, "", "", maxTimelineCommits); err != nil || total != 1 || strings.Join(subjects(cs), ",") != "c1" {
		t.Fatalf("初回: %v %d %v", subjects(cs), total, err)
	}
	// merge で取り込んだほかの人のコミット（o1・o2）は載せない。自分の c2 とマージのコミットだけ
	git("checkout", "-q", "-b", "other")
	commit("o1")
	commit("o2")
	git("checkout", "-q", "main")
	commit("c2")
	git("merge", "-q", "--no-ff", "-m", "merge other", "other")
	cs, total, err := timelineCommitLog(dir, c1, "", maxTimelineCommits)
	if err != nil || total != 2 || strings.Join(subjects(cs), ",") != "c2,merge other" {
		t.Fatalf("merge の後: %v %d %v", subjects(cs), total, err)
	}
	// 対照: --first-parent が無ければ o1・o2 も入る範囲であること（前提）
	if n := git("rev-list", "--count", c1+"..HEAD"); n != "4" {
		t.Fatalf("前提が崩れています: 取り込んだコミットを含む件数 %s", n)
	}
	// since が書き換わって先祖でなくなった（amend）・無い SHA は HEAD の 1 件だけ
	before := commit("c3")
	git("commit", "-q", "--allow-empty", "--amend", "-m", "c3 amended")
	commit("c4")
	if cs, total, err := timelineCommitLog(dir, before, "", maxTimelineCommits); err != nil || total != 1 || strings.Join(subjects(cs), ",") != "c4" {
		t.Fatalf("先祖でない since: %v %d %v", subjects(cs), total, err)
	}
	if cs, total, err := timelineCommitLog(dir, "deadbeefdeadbeef", "", maxTimelineCommits); err != nil || total != 1 || strings.Join(subjects(cs), ",") != "c4" {
		t.Fatalf("無い since: %v %d %v", subjects(cs), total, err)
	}
	// 20 件を超えたら新しい 20 件を古い順に、全体の件数も返す
	base := git("rev-parse", "HEAD")
	for i := 1; i <= 25; i++ {
		commit("n" + strconv.Itoa(i))
	}
	cs, total, err = timelineCommitLog(dir, base, "", maxTimelineCommits)
	if err != nil || total != 25 || len(cs) != 20 || cs[0][1] != "n6" || cs[19][1] != "n25" {
		t.Fatalf("20 件を超えた: %v %d %v", subjects(cs), total, err)
	}
}

// --since は SHA の形だけを受け取る（「-」で始まる値を git の選択肢として渡さない）。対照として SHA の形は通る。
func TestTimelineCommitSinceMustBeSHA(t *testing.T) {
	for in, want := range map[string]bool{"0123456": true, "ABCDEF0123": true, "-n1": false, "--output=/tmp/x": false, "HEAD~1": false, "012345": false} {
		if got := timelineSinceRe.MatchString(in); got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": "http://127.0.0.1:1/api/v1", "LOOPTRACK_PROJECT": "demo", "LOOPTRACK_TOKEN": "lt_test", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"CLAUDE_PROJECT_DIR": t.TempDir()}
	var out, errb strings.Builder
	if code := TimelineMain([]string{"commit", "--since=-n1"}, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(vars)); code != 1 ||
		!strings.Contains(errb.String(), "--since にはコミットの SHA") {
		t.Errorf("-n1: %d %q", code, errb.String())
	}
}

// unread の後の seen を、テスト用のサーバで数える。session はヘッダに載せるセッション ID（空なら載せない）。
func timelineUnreadRun(t *testing.T, session string, stdout io.Writer) (code int, seen int, sessions []string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions = append(sessions, r.Header.Get("X-Looptrack-Session"))
		switch r.URL.Path {
		case "/api/v1/projects/demo/timeline/unread":
			writeJSONResp(w, 200, map[string]any{"text": "未読の文", "advance_to": 7, "head_seq": 9})
		case "/api/v1/projects/demo/timeline/seen":
			seen++
			writeJSONResp(w, 200, map[string]any{"seq": 7, "advanced": true})
		default:
			writeJSONResp(w, 404, map[string]any{"error": map[string]any{"code": "unknown_api", "message": r.URL.Path}})
		}
	}))
	defer srv.Close()
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": srv.URL + "/api/v1", "LOOPTRACK_PROJECT": "demo", "LOOPTRACK_TOKEN": "lt_test", "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"CLAUDE_PROJECT_DIR": t.TempDir()}
	if session != "" {
		vars["LOOPTRACK_SESSION_ID"] = session
	}
	var errb strings.Builder
	code = TimelineMain([]string{"unread"}, IO{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: &errb}, env.FromMap(vars))
	return code, seen, sessions
}

// セッションの無い読み手は unread の後に seen を送らない（サーバも拒む）。対照として、セッションのある読み手は送る。
// 判定は要求のヘッダに実際に載る値で行うので、unread の要求のヘッダも併せて確かめる。
func TestTimelineUnreadSeenNeedsSession(t *testing.T) {
	var out strings.Builder
	code, seen, sessions := timelineUnreadRun(t, "", &out)
	if code != 0 || seen != 0 || len(sessions) != 1 || sessions[0] != "" || !strings.Contains(out.String(), "未読の文") {
		t.Errorf("セッションの無い unread: code=%d seen=%d sessions=%q out=%q", code, seen, sessions, out.String())
	}
	out.Reset()
	code, seen, sessions = timelineUnreadRun(t, "cli-x", &out)
	if code != 0 || seen != 1 || len(sessions) != 2 || sessions[1] != "cli-x" {
		t.Errorf("対照: セッションのある unread: code=%d seen=%d sessions=%q", code, seen, sessions)
	}
}

// failWriter は書き込みをいつも失敗させる。
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("書き込めません") }

// 出力の書き込みに失敗したら seen を送らない（出せなかった分を読んだことにしない）。対照として、書けたら送る。
func TestTimelineUnreadSeenNeedsWrite(t *testing.T) {
	if code, seen, _ := timelineUnreadRun(t, "cli-x", failWriter{}); code == 0 || seen != 0 {
		t.Errorf("書き込みの失敗: code=%d seen=%d", code, seen)
	}
	var out strings.Builder
	if code, seen, _ := timelineUnreadRun(t, "cli-x", &out); code != 0 || seen != 1 {
		t.Errorf("対照: 書き込みの成功: code=%d seen=%d", code, seen)
	}
}
