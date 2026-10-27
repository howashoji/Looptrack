package usagesnap

// 会話記録の置き場が CLAUDE_CONFIG_DIR・CODEX_HOME で変わっても探せること（置き場の解決は agentdirs の 1 か所）。
// 以前の CLI の記録（golden）は環境変数を知らないので、ここは snap を通さず直接呼ぶ。

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
)

func optsFor(home string, vars map[string]string) Options {
	m := map[string]string{"HOME": home}
	for k, v := range vars {
		m[k] = v
	}
	return Options{Env: env.FromMap(m), Home: home}
}

func TestClaudeFindTranscriptHonoursConfigDir(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "other-claude")
	defaultPath := writeClaudeTranscript(t, home, "sess-d", claudeSample())
	otherPath := writeLines(t, filepath.Join(cfg, "projects", "-Users-x-proj", "sess-c.jsonl"), claudeSample())

	// 設定なし: ~/.claude/projects を見る（CLAUDE_CONFIG_DIR 側の記録は見えない）
	if got := claudeFindTranscript("sess-d", optsFor(home, nil)); got != defaultPath {
		t.Errorf("設定なし: got %q, want %q", got, defaultPath)
	}
	if got := claudeFindTranscript("sess-c", optsFor(home, nil)); got != "" {
		t.Errorf("設定なしで CLAUDE_CONFIG_DIR 側を拾った: %q", got)
	}
	// 設定あり: その下の projects を見る（~/.claude 側の記録は見えない）
	o := optsFor(home, map[string]string{"CLAUDE_CONFIG_DIR": cfg})
	if got := claudeFindTranscript("sess-c", o); got != otherPath {
		t.Errorf("設定あり: got %q, want %q", got, otherPath)
	}
	if got := claudeFindTranscript("sess-d", o); got != "" {
		t.Errorf("設定ありで ~/.claude 側を拾った: %q", got)
	}
	// 空の値は設定なしと同じ
	if got := claudeFindTranscript("sess-d", optsFor(home, map[string]string{"CLAUDE_CONFIG_DIR": " "})); got != defaultPath {
		t.Errorf("空白だけの設定: got %q", got)
	}
}

func TestClaudeDetectAndCollectUnderConfigDir(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "other-claude")
	path := writeLines(t, filepath.Join(cfg, "projects", "-Users-x-proj", "sess-1.jsonl"), claudeSample())
	// サブエージェントの記録は会話記録のディレクトリの下（<session>/subagents）。置き場が違っても同じ規則で数える
	writeLines(t, filepath.Join(filepath.Dir(path), "sess-1", "subagents", "agent-1.jsonl"), []string{
		claudeLine("assistant", "2026-09-18T01:00:15.000Z", M{"message": withID("sub1", claudeUsage(5, 0, 500, 7, ""))}),
	})
	o := optsFor(home, map[string]string{"CLAUDE_CONFIG_DIR": cfg, "CLAUDE_CODE_SESSION_ID": "sess-1"})
	client, got, sid := Detect("/nowhere", o)
	if client != "claude-code" || got != path || sid != "sess-1" {
		t.Fatalf("Detect = %q %q %q, want claude-code %q sess-1", client, got, sid, path)
	}
	p, err := Collect(client, got, sid, "/nowhere", o)
	if err != nil || p == nil {
		t.Fatalf("Collect: %v %v", p, err)
	}
	eq(t, p, `1`, "sub_responses")
	eq(t, p, `7`, "tokens", "sub", "output")
	eq(t, p, `650`, "tokens", "main", "output")

	// 設定なしなら見つからない（~/.claude には無い）
	o = optsFor(home, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1"})
	if c, p, _ := Detect("/nowhere", o); c == "claude-code" && p != "" {
		t.Errorf("設定なしで見つかった: %q", p)
	}
}

func TestCodexFindTranscriptHonoursCodexHome(t *testing.T) {
	home, cwd, defaultPath := codexSetup(t)
	codexHome := filepath.Join(t.TempDir(), "other-codex")
	const tidOther = "0199aaaa-1111-7222-8333-444455556666"
	otherPath := filepath.Join(codexHome, "sessions", "2026", "09", "18", "rollout-2026-09-18T02-00-00-"+tidOther+".jsonl")
	otherCWD := filepath.Join(home, "proj-other")
	if err := os.MkdirAll(otherCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRows(t, otherPath, []any{
		M{"timestamp": "2026-09-18T02:00:00.000Z", "type": "session_meta", "payload": M{"id": tidOther, "cwd": otherCWD, "cli_version": "0.140.0"}},
	})

	none := optsFor(home, nil)
	set := optsFor(home, map[string]string{"CODEX_HOME": codexHome})

	// スレッド ID で引く
	if got := codexFindTranscript("", codexTID1, none); got != defaultPath {
		t.Errorf("ID・設定なし: got %q, want %q", got, defaultPath)
	}
	if got := codexFindTranscript("", tidOther, none); got != "" {
		t.Errorf("ID・設定なしで CODEX_HOME 側を拾った: %q", got)
	}
	if got := codexFindTranscript("", tidOther, set); got != otherPath {
		t.Errorf("ID・設定あり: got %q, want %q", got, otherPath)
	}
	if got := codexFindTranscript("", codexTID1, set); got != "" {
		t.Errorf("ID・設定ありで ~/.codex 側を拾った: %q", got)
	}
	// 作業ディレクトリで引く
	if got := codexFindTranscript(cwd, "", none); got != defaultPath {
		t.Errorf("cwd・設定なし: got %q, want %q", got, defaultPath)
	}
	if got := codexFindTranscript(otherCWD, "", none); got != "" {
		t.Errorf("cwd・設定なしで CODEX_HOME 側を拾った: %q", got)
	}
	now := time.Now()
	if err := os.Chtimes(otherPath, now, now); err != nil {
		t.Fatal(err)
	}
	if got := codexFindTranscript(otherCWD, "", set); got != otherPath {
		t.Errorf("cwd・設定あり: got %q, want %q", got, otherPath)
	}
	if got := codexFindTranscript(cwd, "", set); got != "" {
		t.Errorf("cwd・設定ありで ~/.codex 側を拾った: %q", got)
	}
}

func TestCodexThreadFilesUnderCodexHome(t *testing.T) {
	// 同じスレッドの分割ファイル（codexThreadFiles）は渡された会話記録のディレクトリから辿るので、置き場が違っても同じに集まる
	codexHome := filepath.Join(t.TempDir(), "other-codex")
	const tid = "0199bbbb-1111-7222-8333-444455556666"
	day := filepath.Join(codexHome, "sessions", "2026", "09", "18")
	first := filepath.Join(day, "rollout-2026-09-18T01-00-00-"+tid+".jsonl")
	second := filepath.Join(day, "rollout-2026-09-18T03-00-00-"+tid+"_0199cccc-0000-7000-8000-000000000000.jsonl")
	for i, p := range []string{first, second} {
		ts := []string{"2026-09-18T01:00:00.000Z", "2026-09-18T03:00:00.000Z"}[i]
		writeRows(t, p, []any{M{"timestamp": ts, "type": "session_meta", "payload": M{"id": tid, "cwd": "/x"}}})
	}
	files := codexThreadFiles(second, tid)
	if len(files) != 2 || files[0] != first || files[1] != second {
		t.Errorf("codexThreadFiles = %v, want [%s %s]", files, first, second)
	}
}

func TestCodexCompressedRolloutsAreCountedNotRead(t *testing.T) {
	// 圧縮済みの rollout（.jsonl.zst）は zstd を足さないので読まない。ただし黙って見落とさず、件数を返す。
	codexHome := filepath.Join(t.TempDir(), "other-codex")
	home := t.TempDir()
	const tid = "0199dddd-1111-7222-8333-444455556666"
	const tidOther = "0199eeee-1111-7222-8333-444455556666"
	const tidOnlyZst = "0199ffff-1111-7222-8333-444455556666"
	day := filepath.Join(codexHome, "sessions", "2026", "09", "18")
	older := filepath.Join(day, "rollout-2026-09-18T01-00-00-"+tid+".jsonl.zst")
	olderRevert := filepath.Join(codexHome, "sessions", "2026", "09", "17", "rollout-2026-09-17T01-00-00-"+tid+"_0199cccc-0000-7000-8000-000000000000.jsonl.zst")
	otherThread := filepath.Join(day, "rollout-2026-09-18T01-00-00-"+tidOther+".jsonl.zst")
	onlyZst := filepath.Join(day, "rollout-2026-09-18T01-00-00-"+tidOnlyZst+".jsonl.zst")
	for _, p := range []string{older, olderRevert, otherThread, onlyZst} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("not-really-zstd"), 0o644); err != nil { // 中身は読まない。名前だけが合成の対象
			t.Fatal(err)
		}
	}
	live := filepath.Join(day, "rollout-2026-09-18T03-00-00-"+tid+".jsonl")
	writeRows(t, live, []any{
		M{"timestamp": "2026-09-18T03:00:00.000Z", "type": "session_meta", "payload": M{"id": tid, "cwd": "/x"}},
	})
	o := optsFor(home, map[string]string{"CODEX_HOME": codexHome})

	// 探索は圧縮済みを会話記録として拾わない（対照: 同じスレッドの .jsonl は拾う）
	if got := codexFindTranscript("", tid, o); got != live {
		t.Fatalf("前提が崩れています: 同じスレッドの .jsonl を拾えない: %q", got)
	}
	if got := codexFindTranscript("", tidOnlyZst, o); got != "" {
		t.Errorf("圧縮済みだけのスレッドを会話記録として拾った: %q", got)
	}
	if files := codexThreadFiles(live, tid); len(files) != 1 || files[0] != live {
		t.Errorf("codexThreadFiles は圧縮済みを含めない: %v", files)
	}
	// 圧縮済みは件数として返る（同じスレッドの 2 件。別のスレッドは数えない）
	got := CodexUnreadRollouts(live, "", o)
	sort.Strings(got)
	want := []string{older, olderRevert}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CodexUnreadRollouts(会話記録あり) = %v, want %v", got, want)
	}
	// 会話記録が見つからないスレッドでも、スレッド ID（引数か CODEX_THREAD_ID）で圧縮済みを数える
	if got := CodexUnreadRollouts("", tidOnlyZst, o); len(got) != 1 || got[0] != onlyZst {
		t.Errorf("CodexUnreadRollouts(ID 引数) = %v, want [%s]", got, onlyZst)
	}
	oEnv := optsFor(home, map[string]string{"CODEX_HOME": codexHome, "CODEX_THREAD_ID": tidOnlyZst})
	if got := CodexUnreadRollouts("", "", oEnv); len(got) != 1 || got[0] != onlyZst {
		t.Errorf("CodexUnreadRollouts(CODEX_THREAD_ID) = %v, want [%s]", got, onlyZst)
	}
	if got := CodexUnreadRollouts("", "", o); got != nil {
		t.Errorf("スレッドが分からないのに数えた: %v", got)
	}
	// CODEX_SESSION_ID は親の会話の ID。サブエージェントのスレッドとは別なので、それだけがあっても親のスレッドの圧縮済みを数えない
	// （対照: 同じ ID を CODEX_THREAD_ID に入れると数える。上の oEnv と同じ経路）
	oParent := optsFor(home, map[string]string{"CODEX_HOME": codexHome, "CODEX_SESSION_ID": tid})
	if got := CodexUnreadRollouts("", "", oParent); got != nil {
		t.Errorf("CODEX_SESSION_ID だけで親のスレッドの圧縮済みを数えた: %v", got)
	}
	oThread := optsFor(home, map[string]string{"CODEX_HOME": codexHome, "CODEX_THREAD_ID": tid})
	if got := CodexUnreadRollouts("", "", oThread); len(got) != 2 {
		t.Errorf("前提が崩れています: CODEX_THREAD_ID で同じスレッドの圧縮済みを数えられません: %v", got)
	}
	// 置き場が違えば数えない（~/.codex 側には無い）
	if got := CodexUnreadRollouts("", tid, optsFor(home, nil)); len(got) != 0 {
		t.Errorf("CODEX_HOME 側の圧縮済みを ~/.codex から数えた: %v", got)
	}
}
