package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TestSummaryTranscriptState は、会話記録の自動送信の鍵の状態と、この PC の hook の失敗・退避が要約に出ること。
// 鍵: 覚えたルールが入で端末のオプトインも入なら「入」の 1 行、オプトインが切なら環境変数の名前を挙げた 1 行。
// ルールが切・覚えていない・失敗も退避も無いときは何も出さない（対照として同じテストで見る）。
func TestSummaryTranscriptState(t *testing.T) {
	home := t.TempDir()
	vars := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "cfg"), "APPDATA": filepath.Join(home, "appdata"),
		"LOOPTRACK_API_URL": "https://issues.example.invalid/im"}
	jst := time.FixedZone("JST", 9*3600)
	lines := func() []string {
		return transcriptStateText(i18n.JA, localTranscriptState(env.FromMap(vars), "", "demo", ""), jst)
	}
	if got := lines(); len(got) != 0 {
		t.Fatalf("ルールを覚えていなければ何も出さない: %q", got)
	}
	ruleEnv := env.Overlay(env.FromMap(vars), map[string]string{"LOOPTRACK_PROJECT": "demo"})
	usagesnap.RememberTranscriptsRule(false, usagesnap.Options{Env: ruleEnv})
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	if got := lines(); len(got) != 0 {
		t.Fatalf("ルールが切なら何も出さない: %q", got)
	}
	usagesnap.RememberTranscriptsRule(true, usagesnap.Options{Env: ruleEnv})
	if got := lines(); len(got) != 1 || !strings.Contains(got[0], "会話記録の自動送信: 入") {
		t.Fatalf("両方が入なら「入」の 1 行: %q", got)
	}
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "0"
	if got := lines(); len(got) != 1 || !strings.Contains(got[0], "LOOPTRACK_SESSION_TRANSCRIPTS=1 を設定") {
		t.Fatalf("ルールが入でオプトインが切なら、環境変数の名前を挙げた 1 行: %q", got)
	}
	// 失敗（変換の止め）と退避
	dir := usagesnap.StateDir(env.FromMap(vars))
	usagesnap.RecordTranscriptFailure(dir, time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), usagesnap.FailTranscriptRejected, 0, 2)
	spool := usagesnap.TranscriptSpoolDir(dir, vars["LOOPTRACK_API_URL"], "demo")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "claude-code-s1.json"), []byte(`{"agent":"claude-code","session_id":"s1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := lines()
	if len(got) != 2 || !strings.Contains(got[1], "会話記録を送れていません") || !strings.Contains(got[1], "ファイルが 2 個") ||
		!strings.Contains(got[1], "未送信 1 件") || !strings.Contains(got[1], "2026-01-02 12:04") {
		t.Fatalf("失敗の 1 行（件数・時刻・理由）: %q", got)
	}
	usagesnap.ClearTranscriptFailure(dir)
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	got = lines()
	if len(got) != 2 || !strings.Contains(got[1], "未送信の会話記録が 1 件") {
		t.Fatalf("退避だけの 1 行: %q", got)
	}
	// 英語の文面も同じ数だけ出る
	if en := transcriptStateText(i18n.EN, localTranscriptState(env.FromMap(vars), "", "demo", ""), jst); len(en) != 2 || !strings.Contains(en[0], "Automatic transcript sending: on") {
		t.Fatalf("英語: %q", en)
	}
}

// TestSummaryTranscriptUnwired は、自動送信の鍵が両方とも入なのに送る hook（transcript の Stop・SessionEnd）が
// 配線されていないとき、要約が「入」の行の代わりに init のやり直しを案内すること。
// 送る hook の無い版の init で入れたプロジェクトで CLI だけを上げると、この形になる。
// 案内が出る側（Claude Code・Codex それぞれの配線の欠け）と出ない側（配線あり・鍵が切・別の AI の欠け・looptrack の
// hook が 1 つも無い）を同じテストで見る。出ない側だけだと、判定の経路が死んでいても通るため。
func TestSummaryTranscriptUnwired(t *testing.T) {
	home := t.TempDir()
	vars := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "cfg"), "APPDATA": filepath.Join(home, "appdata"),
		"LOOPTRACK_API_URL": "https://issues.example.invalid/im", "LOOPTRACK_SESSION_TRANSCRIPTS": "1"}
	ruleEnv := env.Overlay(env.FromMap(vars), map[string]string{"LOOPTRACK_PROJECT": "demo"})
	usagesnap.RememberTranscriptsRule(true, usagesnap.Options{Env: ruleEnv})
	jst := time.FixedZone("JST", 9*3600)
	lines := func(lang i18n.Lang, root, agent string) []string {
		return transcriptStateText(lang, localTranscriptState(env.FromMap(vars), root, "demo", agent), jst)
	}
	write := func(root, rel, body string) {
		t.Helper()
		fp := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	group := func(cmds ...string) string {
		var hs []string
		for _, c := range cmds {
			hs = append(hs, `{"type": "command", "command": "`+strings.ReplaceAll(c, `"`, `\"`)+`", "timeout": 10}`)
		}
		return `[{"hooks": [` + strings.Join(hs, ", ") + `]}]`
	}
	// 1.0.2 の init が書いた形（summary・usage はあるが transcript が無い）
	claudeOld := `{"hooks": {"SessionStart": ` + group("looptrack hook summary --agent claude-code") +
		`, "Stop": ` + group("looptrack hook usage --agent claude-code") + `, "SessionEnd": ` + group("looptrack hook usage --agent claude-code") + `}}`
	claudeNew := `{"hooks": {"SessionStart": ` + group("looptrack hook summary --agent claude-code") +
		`, "Stop": ` + group("looptrack hook usage --agent claude-code", "looptrack hook transcript --agent claude-code") +
		`, "SessionEnd": ` + group("looptrack hook usage --agent claude-code", "looptrack hook transcript --agent claude-code") + `}}`
	pre := "LOOPTRACK_API_URL=https://issues.example.invalid/im LOOPTRACK_PROJECT=demo "
	codexOld := `{"hooks": {"SessionStart": ` + group(pre+"looptrack hook summary --agent codex") +
		`, "Stop": ` + group(pre+"looptrack hook usage --agent codex") + `}}`
	codexNew := `{"hooks": {"SessionStart": ` + group(pre+"looptrack hook summary --agent codex") +
		`, "Stop": ` + group(pre+"looptrack hook usage --agent codex", pre+"looptrack hook transcript --agent codex") +
		`, "SessionEnd": ` + group(pre+"looptrack hook transcript --agent codex") + `}}`
	const unwired = "送る hook（Stop・SessionEnd の looptrack hook transcript）がありません"
	const on = "会話記録の自動送信: 入"

	// 案内が出る: Claude Code の配線に transcript が無い
	old := t.TempDir()
	write(old, ".claude/settings.json", claudeOld)
	for _, agent := range []string{"", "claude-code"} {
		got := lines(i18n.JA, old, agent)
		if len(got) != 1 || !strings.Contains(got[0], unwired) || !strings.Contains(got[0], "claude-code の設定") ||
			!strings.Contains(got[0], "looptrack issue init --project demo --agent claude-code") || strings.Contains(got[0], on) {
			t.Fatalf("Claude Code の配線の欠けを案内していない（agent=%q）: %q", agent, got)
		}
	}
	if en := lines(i18n.EN, old, ""); len(en) != 1 || !strings.Contains(en[0], "no hook that sends it") {
		t.Fatalf("英語: %q", en)
	}
	// 対照: 鍵が切なら配線が無くても案内しない（hook は配線があっても送らない）
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "0"
	if got := lines(i18n.JA, old, ""); len(got) != 1 || strings.Contains(got[0], unwired) {
		t.Fatalf("鍵が切なら配線の案内は出さない: %q", got)
	}
	vars["LOOPTRACK_SESSION_TRANSCRIPTS"] = "1"
	// 対照: Copilot から呼ばれた要約は Claude Code の設定を見ない
	if got := lines(i18n.JA, old, "copilot"); len(got) != 1 || !strings.Contains(got[0], on) {
		t.Fatalf("Copilot の要約に Claude Code の配線の案内を出した: %q", got)
	}

	// 対照: transcript が Stop・SessionEnd の両方にあれば「入」の行だけ
	cur := t.TempDir()
	write(cur, ".claude/settings.json", claudeNew)
	if got := lines(i18n.JA, cur, ""); len(got) != 1 || !strings.Contains(got[0], on) || strings.Contains(got[0], unwired) {
		t.Fatalf("配線があるのに案内した: %q", got)
	}
	// 対照: PATH に無い端末で settings.local.json に書いた配線も数える
	local := t.TempDir()
	write(local, ".claude/settings.json", `{"hooks": {}}`)
	write(local, ".claude/settings.local.json", strings.ReplaceAll(claudeNew, "looptrack hook", `\"/opt/lt/looptrack\" hook`))
	if got := lines(i18n.JA, local, ""); len(got) != 1 || !strings.Contains(got[0], on) {
		t.Fatalf("settings.local.json の配線を数えていない: %q", got)
	}
	// 対照: 配線が 2 つの設定ファイルに分かれていても合わせて見る。settings.json には transcript の無い古い配線、
	// settings.local.json には transcript だけがあるときは揃っているので「入」の行（settings.local.json を見なければ案内が出る）
	split := t.TempDir()
	write(split, ".claude/settings.json", claudeOld)
	write(split, ".claude/settings.local.json", `{"hooks": {"Stop": `+group(`"/opt/lt/looptrack" hook transcript --agent claude-code`)+
		`, "SessionEnd": `+group(`"/opt/lt/looptrack" hook transcript --agent claude-code`)+`}}`)
	if got := lines(i18n.JA, split, ""); len(got) != 1 || !strings.Contains(got[0], on) || strings.Contains(got[0], unwired) {
		t.Fatalf("2 つの設定ファイルの配線を合わせて数えていない: %q", got)
	}
	// 案内が出る: looptrack の hook が settings.local.json にしか無く、そこに transcript が無い（settings.local.json を見なければ未導入として案内しない）
	localOld := t.TempDir()
	write(localOld, ".claude/settings.json", `{"hooks": {}}`)
	write(localOld, ".claude/settings.local.json", strings.ReplaceAll(claudeOld, "looptrack hook", `\"/opt/lt/looptrack\" hook`))
	if got := lines(i18n.JA, localOld, ""); len(got) != 1 || !strings.Contains(got[0], unwired) || !strings.Contains(got[0], "claude-code の設定") {
		t.Fatalf("settings.local.json だけにある古い配線を案内していない: %q", got)
	}
	// 対照: looptrack の hook が 1 つも無いプロジェクト（未導入）には案内しない
	none := t.TempDir()
	write(none, ".claude/settings.json", `{"hooks": {"Stop": `+group("./scripts/own-hook.sh")+`}}`)
	if got := lines(i18n.JA, none, ""); len(got) != 1 || !strings.Contains(got[0], on) {
		t.Fatalf("looptrack の hook の無いプロジェクトに案内した: %q", got)
	}

	// Codex: 前置の環境変数つきの配線。SessionEnd に無いだけでも案内する
	cx := t.TempDir()
	write(cx, ".claude/settings.json", claudeNew)
	write(cx, ".codex/hooks.json", codexOld)
	got := lines(i18n.JA, cx, "")
	if len(got) != 1 || !strings.Contains(got[0], "codex の設定") || !strings.Contains(got[0], "--agent codex") || strings.Contains(got[0], "claude-code") {
		t.Fatalf("Codex の配線の欠けだけを案内していない: %q", got)
	}
	if got := lines(i18n.JA, cx, "claude-code"); len(got) != 1 || !strings.Contains(got[0], on) {
		t.Fatalf("Claude Code から呼ばれた要約に Codex の欠けを出した: %q", got)
	}
	// Stop には transcript があり SessionEnd だけに無い形も案内する（Stop だけを見る判定だと通ってしまう）
	codexStopOnly := `{"hooks": {"SessionStart": ` + group(pre+"looptrack hook summary --agent codex") +
		`, "Stop": ` + group(pre+"looptrack hook usage --agent codex", pre+"looptrack hook transcript --agent codex") +
		`, "SessionEnd": ` + group(pre+"looptrack hook usage --agent codex") + `}}`
	write(cx, ".codex/hooks.json", codexStopOnly)
	if got := lines(i18n.JA, cx, "codex"); len(got) != 1 || !strings.Contains(got[0], "codex の設定") || !strings.Contains(got[0], unwired) {
		t.Fatalf("SessionEnd だけに transcript が無い Codex の配線を案内していない: %q", got)
	}
	write(cx, ".codex/hooks.json", codexNew)
	if got := lines(i18n.JA, cx, "codex"); len(got) != 1 || !strings.Contains(got[0], on) {
		t.Fatalf("Codex の配線があるのに案内した: %q", got)
	}

	// サーバ版の導入（控えが server）では、実行中の looptrack の絶対パスに --url と --source server を付ける
	oldExe := executableFn
	t.Cleanup(func() { executableFn = oldExe })
	executableFn = func() (string, error) { return "/opt/lt/looptrack", nil }
	srv := t.TempDir()
	write(srv, ".claude/settings.json", claudeOld)
	write(srv, ".claude/.looptrack-kit.json", `{"project": "demo", "source": "server"}`)
	// Windows では PowerShell に貼る形（& '…'）で囲む
	wantBin := `"/opt/lt/looptrack"`
	if runtime.GOOS == "windows" {
		wantBin = `& '/opt/lt/looptrack'`
	}
	if got := lines(i18n.JA, srv, "claude-code"); len(got) != 1 ||
		!strings.Contains(got[0], wantBin+` issue init --project demo --url https://issues.example.invalid/im --agent claude-code --source server`) {
		t.Fatalf("サーバ版のやり直しのコマンド: %q", got)
	}
	// looptrack 自身のリポジトリ（正本）では init を案内しない
	self := t.TempDir()
	write(self, ".claude/settings.json", claudeOld)
	write(self, "kit/embed.go", "package kit\n")
	if err := os.MkdirAll(filepath.Join(self, "cmd", "looptrack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lines(i18n.JA, self, ""); len(got) != 1 || !strings.Contains(got[0], "init はしません") || strings.Contains(got[0], "issue init") {
		t.Fatalf("正本の案内: %q", got)
	}
}
