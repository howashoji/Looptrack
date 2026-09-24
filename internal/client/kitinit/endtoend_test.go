package kitinit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/i18n"
)

// endToEnd は looptrack をビルドし、配線のコマンドを sh -c で起動する（導入先に置くものは looptrack の実行ファイルだけで、
// スクリプトの類は要らない）。
func endToEnd(t *testing.T, root string) {
	if testing.Short() {
		return
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Log("go が無いため配線の実行は省略")
		return
	}
	bin := filepath.Join(root, "bin", "looptrack")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/looptrack")
	build.Dir, _ = filepath.Abs(filepath.Join("..", "..", ".."))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("looptrack のビルド: %v\n%s", err, out)
	}
	ws := filepath.Join(root, "ws")
	home := filepath.Join(root, "home")
	base := []string{"PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
	run := func(extra []string, stdin string, name string, args ...string) (string, string, int) {
		cmd := exec.Command(name, args...)
		cmd.Dir = filepath.Join(ws, ".claude") // サブディレクトリからでもルートを見つける
		cmd.Env = append(append([]string(nil), base...), extra...)
		cmd.Stdin = strings.NewReader(stdin)
		var o, e strings.Builder
		cmd.Stdout, cmd.Stderr = &o, &e
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return o.String(), e.String(), code
	}
	// 配線: Claude Code（CLAUDECODE）なら rules を注入し、Copilot から CLAUDE_PROJECT_DIR なしで起動されたら何も出さずに 0
	// cwd は JSON の文字列として埋める（Windows のパスの「\」を連結で埋めると不正な JSON になる）
	evb, _ := json.Marshal(map[string]string{"session_id": "e2e", "hook_event_name": "SessionStart", "source": "startup", "cwd": ws})
	ev := string(evb)
	out, errs, code := run([]string{"CLAUDECODE=1", "CLAUDE_PROJECT_DIR=" + ws}, ev, "sh", "-c", "looptrack hook session-start-rules --agent claude-code")
	if code != 0 || !strings.Contains(out, "additionalContext") {
		t.Errorf("Claude Code からの session-start-rules: %d %q %q", code, out, errs)
	}
	out, errs, code = run([]string{"COPILOT_AGENT=1"}, ev, "sh", "-c", "looptrack hook session-start-rules --agent claude-code")
	if code != 0 || out != "" || errs != "" {
		t.Errorf("Copilot からの .claude/settings.json の hook: %d %q %q", code, out, errs)
	}
}

// TestVerifyFailureRollsBack は、導入の後の verify が失敗したら、書いたものを元に戻して止めることを確かめる。
func TestVerifyFailureRollsBack(t *testing.T) {
	fr := stubs(t, true)
	root := newRoot(t)
	ws := filepath.Join(root, "ws")
	write(t, filepath.Join(ws, ".claude", "settings.json"), `{"env": {"OTHER": "1"}}`+"\n")
	write(t, filepath.Join(ws, "CLAUDE.md"), "# 既存\n")
	if r := runInit(t, root, "", "--project", "demo", "--no-loop"); r.code != 0 {
		t.Fatalf("準備: %s", r.stderr)
	}
	os.RemoveAll(filepath.Join(ws, ".claude", ".looptrack-init-backup"))
	beforeTree := tree(t, ws)
	fr.version = func() (string, int) { return "", 127 } // looptrack が動かない（壊れた実行ファイル）
	r := runInit(t, root, "", "--project", "demo", "--loop")
	if r.code != 1 || !strings.Contains(r.stderr, "verify に失敗したため導入を止めました（書いたものは元に戻しました）") ||
		!strings.Contains(r.stderr, "version が looptrack の版を出しません") {
		t.Fatalf("verify の失敗で止まらない: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	after := tree(t, ws)
	if lexists(filepath.Join(ws, ".claude", ".looptrack-init-backup")) {
		t.Errorf("控えのディレクトリが残っている")
	}
	for n, b := range beforeTree {
		if after[n] != b {
			t.Errorf("%s が元に戻っていない", n)
		}
	}
	for n := range after {
		if _, ok := beforeTree[n]; !ok {
			t.Errorf("%s が残っている（作ったものが消えていない）", n)
		}
	}
	if lexists(filepath.Join(ws, ".claude", "rules")) {
		t.Errorf("作ったディレクトリ .claude/rules が残っている")
	}
	// --no-verify なら止めない
	r = runInit(t, root, "", "--project", "demo", "--loop", "--no-verify")
	if r.code != 0 || !strings.Contains(r.stdout, "verify: 省きました（--no-verify）") {
		t.Errorf("--no-verify: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

// TestVerifyDetectsBrokenWiring は verify の配線の検査が、manifest と違う配線（手で消した loop の hook）で失敗することを確かめる。
func TestVerifyDetectsBrokenWiring(t *testing.T) {
	stubs(t, true)
	root := newRoot(t)
	if r := runInit(t, root, "", "--project", "demo", "--loop"); r.code != 0 {
		t.Fatalf("準備: %s", r.stderr)
	}
	in := &installer{c: &cli.Ctx{Lang: i18n.JA}, o: &Options{Project: "demo", agents: []string{"claude-code"}}, url: fakeURL, target: filepath.Join(root, "ws"),
		plan: &Plan{Root: filepath.Join(root, "ws"), Lang: i18n.JA}, bin: binRef{sh: "looptrack", ps: "looptrack"}}
	in.files = embedded()
	loopFiles := map[string]string{}
	for n, b := range in.files {
		if strings.HasPrefix(n, "kit/loop/") {
			loopFiles[n] = b
		}
	}
	_, in.loop, _ = parseManifest(loopFiles)
	in.loopOn = true
	if rep := in.verify(); len(rep.fails) != 0 {
		t.Fatalf("導入直後の verify が失敗: %v", rep.fails)
	}
	sp := filepath.Join(root, "ws", ".claude", "settings.json")
	write(t, sp, strings.Replace(read(t, sp), "looptrack hook stop-handoff-freshness --agent claude-code", "looptrack hook stop-handoff-freshnes --agent claude-code", 1))
	rep := in.verify()
	if len(rep.fails) == 0 || !strings.Contains(strings.Join(rep.fails, "\n"), "looptrack に無い hook") {
		t.Errorf("壊れた配線を見つけない: %v", rep.fails)
	}
}

// TestNotOnPathUsesLocalSettings は PATH に looptrack が無い端末: 絶対パスで手元専用の settings.local.json に配線する。
func TestNotOnPathUsesLocalSettings(t *testing.T) {
	fr := stubs(t, false)
	root := newRoot(t)
	ws := filepath.Join(root, "ws")
	r := runInit(t, root, "", "--project", "demo", "--loop")
	if r.code != 0 {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	shared := read(t, filepath.Join(ws, ".claude", "settings.json"))
	local := read(t, filepath.Join(ws, ".claude", "settings.local.json"))
	if strings.Contains(shared, `"hooks"`) || !strings.Contains(local, `"\"/opt/lt/looptrack\" hook summary --agent claude-code"`) {
		t.Errorf("共有の設定に配線せず、手元の設定に絶対パスで配線する:\nshared=%s\nlocal=%s", shared, local)
	}
	if !strings.Contains(strings.Join(fr.calls, "\n"), "/opt/lt/looptrack version") {
		t.Errorf("verify が絶対パスの looptrack を起動していない: %v", fr.calls)
	}
	// PATH に置いた後の再実行で、共有の設定に名前で配線し直し、手元の設定から外す
	stubs(t, true)
	r = runInit(t, root, "", "--project", "demo")
	if r.code != 0 {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	shared = read(t, filepath.Join(ws, ".claude", "settings.json"))
	local = read(t, filepath.Join(ws, ".claude", "settings.local.json"))
	if !strings.Contains(shared, `"looptrack hook summary --agent claude-code"`) || strings.Contains(local, "looptrack") {
		t.Errorf("PATH に置いた後の配線:\nshared=%s\nlocal=%s", shared, local)
	}
}

// tree は ws のファイル（symlink は "-> 先"）。
func tree(t *testing.T, ws string) map[string]string {
	out := map[string]string{}
	filepath.Walk(ws, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == ws {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".claude/.looptrack-init-backup") {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			tgt, _ := os.Readlink(p)
			out[rel] = "-> " + tgt
			return nil
		}
		if !info.IsDir() {
			out[rel] = read(t, p)
		}
		return nil
	})
	return out
}

// TestTokenReportLinkReplaced は、以前の CLI（1.0.0 より前）の init が置いた skill token-report のディレクトリの symlink
// （リポジトリの skills/token-report へ。その撤去で先が無くなる）を消して、kit の SKILL.md の実体を置くことを確かめる。
// 手で置いた（init の印の無い）SKILL.md は変えない。
func TestTokenReportLinkReplaced(t *testing.T) {
	stubs(t, true)
	root := newRoot(t)
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(filepath.Join(ws, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gone", "skills", "token-report"), filepath.Join(ws, ".claude", "skills", "token-report")); err != nil {
		t.Skip("symlink を作れない: ", err)
	}
	if r := runInit(t, root, "", "--project", "demo"); r.code != 0 {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	rel := filepath.Join(ws, ".claude", "skills", "token-report", "SKILL.md")
	if isLink(filepath.Dir(rel)) || !strings.Contains(read(t, rel), "looptrack report pdf") || !strings.Contains(read(t, rel), "looptrack issue") {
		t.Errorf("symlink を実体の SKILL.md（looptrack の形）に置き換えていない")
	}
	write(t, rel, "# 手で置いた\n")
	if r := runInit(t, root, "", "--project", "demo"); r.code != 0 || !strings.Contains(r.stdout, "init が作ったものではないため変えていません") {
		t.Fatalf("手で置いた SKILL.md: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if read(t, rel) != "# 手で置いた\n" {
		t.Errorf("手で置いた SKILL.md を変えた")
	}
}
