package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// sessionCLI は looptrack issue をテストのサーバ（プロジェクト req）に向けて起動する関数を返す（利用者の AI のセッションの
// 変数は持ち込まない。extraEnv で AI の変数を足す）。失敗（exit 0 以外）はテストの失敗。
func sessionCLI(t *testing.T, e *env, token, home string, tick bool) func(extraEnv []string, args ...string) string {
	return func(extraEnv []string, args ...string) string {
		t.Helper()
		if tick {
			e.clock.Add(time.Second)
		}
		env := append(append(cliAPIEnv(e.srv.URL+"/im", "req", token, home), "CLAUDE_PROJECT_DIR="+home), extraEnv...)
		r := runCLI(t, home, env, "", append([]string{"issue"}, args...)...)
		if r.code != 0 {
			t.Fatalf("looptrack issue %v: exit %d %s%s", args, r.code, r.stdout, r.stderr)
		}
		return r.stdout + r.stderr
	}
}

// Claude Code の Bash から looptrack issue で変更すると、イベントにそのセッション ID が入る。
// Claude Code が渡す環境変数は CLAUDE_CODE_SESSION_ID（LOOPTRACK_SESSION_ID を明示したテストでは気づけなかった）。
func TestCLISessionID(t *testing.T) {
	e := newEnv(t)
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	token := e.apiAs(u).token
	run := sessionCLI(t, e, token, t.TempDir(), false)
	sessionOf := func(kind string) string {
		t.Helper()
		var sid *string
		if err := e.db.QueryRow("SELECT session_id FROM issue_events WHERE kind = ? ORDER BY id DESC LIMIT 1", kind).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		if sid == nil {
			return ""
		}
		return *sid
	}

	if out := run([]string{"CLAUDE_CODE_SESSION_ID=cc-session-1"}, "new", "起票"); !strings.Contains(out, "REQ-0001") {
		t.Fatalf("new: %s", out)
	}
	if got := sessionOf("create"); got != "cc-session-1" {
		t.Errorf("CLAUDE_CODE_SESSION_ID のとき create の session_id = %q", got)
	}
	run([]string{"CLAUDE_SESSION_ID=legacy-session"}, "comment", "REQ-0001", "旧い変数名")
	if got := sessionOf("comment"); got != "legacy-session" {
		t.Errorf("CLAUDE_SESSION_ID のとき comment の session_id = %q", got)
	}
	// 明示した LOOPTRACK_SESSION_ID が最優先
	run([]string{"LOOPTRACK_SESSION_ID=explicit", "CLAUDE_CODE_SESSION_ID=cc-session-2"}, "status", "REQ-0001", "In Progress")
	if got := sessionOf("status"); got != "explicit" {
		t.Errorf("LOOPTRACK_SESSION_ID を優先するはず: %q", got)
	}
	// どれも無ければ空（人がターミナルから打った操作）
	run(nil, "comment", "REQ-0001", "端末から")
	if got := sessionOf("comment"); got != "" {
		t.Errorf("変数が無いとき session_id = %q", got)
	}
}

// Codex のシェルから looptrack issue で変更すると、イベントに Codex のスレッド ID が入り、
// 同じ ID の会話記録（rollout）からトークン情報が付いて、付与漏れの検知で AI の操作として数えられる。
// Codex がシェルに渡すのは CODEX_THREAD_ID（rollout のファイル名・session_meta.id と同じ）と、新しい版では
// CODEX_SESSION_ID（サブエージェントも共有する親の会話の ID）。
func TestCLISessionIDCodex(t *testing.T) {
	e := newEnv(t)
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	token := e.apiAs(u).token
	home := t.TempDir()
	const thread = "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0001"
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "18")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := strings.Join([]string{
		`{"timestamp":"2026-09-18T01:00:00.000Z","type":"session_meta","payload":{"id":"` + thread + `","cwd":"/nowhere","cli_version":"0.140.0","originator":"codex_cli_rs"}}`,
		`{"timestamp":"2026-09-18T01:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"直して"}}`,
		`{"timestamp":"2026-09-18T01:00:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[]}}`,
		`{"timestamp":"2026-09-18T01:00:05.500Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":80,"reasoning_output_tokens":30,"total_tokens":1080}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-09-18T01-00-00-"+thread+".jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatal(err)
	}
	run := sessionCLI(t, e, token, home, true)
	sessionOf := func(kind string) string {
		t.Helper()
		var sid *string
		if err := e.db.QueryRow("SELECT session_id FROM issue_events WHERE kind = ? ORDER BY id DESC LIMIT 1", kind).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		if sid == nil {
			return ""
		}
		return *sid
	}

	// 修正前は session_id が空で「人の操作」になり、トークン情報も付かなかった
	inCodex := []string{"CODEX_THREAD_ID=" + thread, "CODEX_SESSION_ID=" + thread}
	if out := run(inCodex, "new", "Codex から起票"); !strings.Contains(out, "REQ-0001") {
		t.Fatalf("new: %s", out)
	}
	if got := sessionOf("create"); got != thread {
		t.Errorf("CODEX_THREAD_ID のとき create の session_id = %q", got)
	}
	var snapSession, client string
	if err := e.db.QueryRow("SELECT session_id, client FROM usage_snapshots ORDER BY id DESC LIMIT 1").Scan(&snapSession, &client); err != nil {
		t.Fatalf("トークン情報が付いていない: %v", err)
	}
	if snapSession != thread || client != "codex" {
		t.Errorf("スナップショット = %q %q（イベントと同じ ID の codex のはず）", snapSession, client)
	}
	// サブエージェントのスレッド: 自分の THREAD を親の SESSION より優先する
	run([]string{"CODEX_THREAD_ID=sub-thread", "CODEX_SESSION_ID=" + thread}, "comment", "REQ-0001", "サブエージェントから")
	if got := sessionOf("comment"); got != "sub-thread" {
		t.Errorf("CODEX_THREAD_ID を優先するはず: %q", got)
	}
	// CODEX_SESSION_ID だけ（THREAD を渡さない実行経路）でも AI の操作として扱う
	run([]string{"CODEX_SESSION_ID=root-only"}, "status", "REQ-0001", "In Progress")
	if got := sessionOf("status"); got != "root-only" {
		t.Errorf("CODEX_SESSION_ID のとき status の session_id = %q", got)
	}
	// Claude Code の変数が同時にあれば、従来どおり Claude Code を優先する（以前からの挙動を変えない）
	run([]string{"CLAUDE_CODE_SESSION_ID=cc-1", "CODEX_THREAD_ID=" + thread}, "comment", "REQ-0001", "両方")
	if got := sessionOf("comment"); got != "cc-1" {
		t.Errorf("CLAUDE_CODE_SESSION_ID を優先するはず: %q", got)
	}
	// 付与漏れの検知: Codex の操作は人の操作ではなく AI の操作として数える
	var cov coverageResp
	out := run(nil, "usage", "missing", "--json", "--all-users")
	if err := json.Unmarshal([]byte(out), &cov); err != nil {
		t.Fatalf("usage missing --json: %v %s", err, out)
	}
	// 対象は 5 件（create / comment 2 件 / status / assign）。担当者が動く status の操作は
	// kind assign のイベントも書き、未付与の検知はその assign も数える（数えないと、担当だけを
	// 変えた操作が未付与の一覧にすら出ない）。
	if cov.Target != 5 || cov.Humans != 0 {
		t.Errorf("Codex の操作が AI の操作として数えられていない: target=%d humans=%d %s", cov.Target, cov.Humans, out)
	}
	sawAssign := false
	for _, it := range cov.Items {
		if it.Issue == "REQ-0001" && it.Kind == "create" {
			t.Errorf("Codex の起票は同じ会話のトークン情報が付いているので未付与にならないはず: %s", out)
		}
		if it.Issue == "REQ-0001" && it.Kind == "assign" {
			sawAssign = true
		}
	}
	if !sawAssign {
		t.Errorf("担当者が動く status の assign が未付与の一覧に出ていない（assign を数えていない）: %s", out)
	}
}

// GitHub Copilot のシェルから looptrack issue で変更したときの記録。
//   - Copilot CLI: COPILOT_CLI=1 と COPILOT_AGENT_SESSION_ID → session_id に載り、detail.agent = copilot
//   - VS Code のエージェント用ターミナル: AI_AGENT=github_copilot_vscode_agent / COPILOT_AGENT=1 → session_id なし・detail.agent = copilot
//   - 人が VS Code の端末で打った操作（TERM_PROGRAM=vscode だけ）・COPILOT_CLI=1 だけ → 従来どおり人の操作
//
// Copilot は計測が任意の AI（store.UsageOptInAgents）で、計測を有効にしていない利用者の操作は付与漏れの検知の対象にも「人の操作」にも数えない。
func TestCLISessionIDCopilot(t *testing.T) {
	e := newEnv(t)
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	run := sessionCLI(t, e, e.apiAs(u).token, t.TempDir(), true)
	last := func(kind string) (session, agent string) {
		t.Helper()
		var sid, ag *string
		if err := e.db.QueryRow(`SELECT session_id, JSON_UNQUOTE(JSON_EXTRACT(detail, '$.agent')) FROM issue_events
WHERE kind = ? ORDER BY id DESC LIMIT 1`, kind).Scan(&sid, &ag); err != nil {
			t.Fatal(err)
		}
		if sid != nil {
			session = *sid
		}
		if ag != nil {
			agent = *ag
		}
		return
	}

	const cliSession = "3f0c0b8e-1111-4222-8333-444455556666"
	inCLI := []string{"COPILOT_CLI=1", "COPILOT_CLI_BINARY_VERSION=1.0.29", "COPILOT_AGENT_SESSION_ID=" + cliSession}
	if out := run(inCLI, "new", "Copilot CLI から起票"); !strings.Contains(out, "REQ-0001") {
		t.Fatalf("new: %s", out)
	}
	if s, a := last("create"); s != cliSession || a != "copilot" {
		t.Errorf("Copilot CLI: session_id=%q agent=%q", s, a)
	}
	if out := run(inCLI, "comment", "REQ-0001", "CLI"); strings.Contains(out, "usage attach") {
		t.Errorf("Copilot（測れない AI）に付与の指示を出さないはず: %s", out)
	}
	// COPILOT_CLI=1 と組み合わせないとき（COPILOT_AGENT_SESSION_ID だけ）は読まない。COPILOT_CLI=1 だけ（ID の無い旧い版・人の ! コマンド）も人の操作
	run([]string{"COPILOT_AGENT_SESSION_ID=" + cliSession}, "comment", "REQ-0001", "ID だけ")
	if s, a := last("comment"); s != "" || a != "" {
		t.Errorf("COPILOT_CLI が無ければ人の操作のはず: session_id=%q agent=%q", s, a)
	}
	run([]string{"COPILOT_CLI=1"}, "comment", "REQ-0001", "CLI の印だけ")
	if s, a := last("comment"); s != "" || a != "" {
		t.Errorf("COPILOT_AGENT_SESSION_ID が無ければ人の操作のはず: session_id=%q agent=%q", s, a)
	}
	// VS Code のエージェント用ターミナル: セッション ID なしの AI の操作
	for _, env := range [][]string{
		{"TERM_PROGRAM=vscode", "AI_AGENT=github_copilot_vscode_agent", "COPILOT_AGENT=1"},
		{"TERM_PROGRAM=vscode", "AI_AGENT=github_copilot_vscode_agent"},
		{"TERM_PROGRAM=vscode", "COPILOT_AGENT=1"},
	} {
		run(env, "comment", "REQ-0001", "VS Code の Copilot")
		if s, a := last("comment"); s != "" || a != "copilot" {
			t.Errorf("VS Code の Copilot %v: session_id=%q agent=%q", env, s, a)
		}
	}
	// 人が VS Code の端末で打った操作（エージェントの印が無い）・別の AI の値は人の操作のまま
	for _, env := range [][]string{{"TERM_PROGRAM=vscode"}, {"TERM_PROGRAM=vscode", "AI_AGENT=other_agent", "COPILOT_AGENT=0"}} {
		run(env, "comment", "REQ-0001", "人が VS Code の端末から")
		if s, a := last("comment"); s != "" || a != "" {
			t.Errorf("人の操作 %v: session_id=%q agent=%q", env, s, a)
		}
	}
	// 明示の LOOPTRACK_SESSION_ID は従来どおり最優先。Copilot の下なら agent も付く
	run([]string{"LOOPTRACK_SESSION_ID=explicit-1", "AI_AGENT=github_copilot_vscode_agent"}, "comment", "REQ-0001", "ID を明示")
	if s, a := last("comment"); s != "explicit-1" || a != "copilot" {
		t.Errorf("LOOPTRACK_SESSION_ID + VS Code の Copilot: session_id=%q agent=%q", s, a)
	}
	// Claude Code の変数があれば従来どおり Claude Code（agent は付けない。Claude Code を Copilot の端末から起動した場合など）
	run(append([]string{"CLAUDE_CODE_SESSION_ID=cc-1"}, inCLI...), "comment", "REQ-0001", "Claude Code")
	if s, a := last("comment"); s != "cc-1" || a != "" {
		t.Errorf("Claude Code を優先するはず: session_id=%q agent=%q", s, a)
	}

	// 付与漏れの検知: Copilot の操作は対象（AI・トークン付与を求める）にも人の操作にも数えない。
	// 対象は Claude Code の 1 件、人は「ID だけ」「CLI の印だけ」「VS Code の端末 2 件」の 4 件
	var cov coverageResp
	out := run(nil, "usage", "missing", "--json", "--all-users")
	if err := json.Unmarshal([]byte(out), &cov); err != nil {
		t.Fatalf("usage missing --json: %v %s", err, out)
	}
	if cov.Target != 1 || cov.Humans != 4 {
		t.Errorf("Copilot の操作の数え方: target=%d humans=%d %s", cov.Target, cov.Humans, out)
	}
	// agent が付くのは Copilot の印があるときだけ（人の操作・Claude Code には付かない）
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM issue_events WHERE JSON_EXTRACT(detail, '$.agent') IS NOT NULL").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Errorf("agent の付いたイベント = %d（Copilot CLI 2・VS Code 3・LOOPTRACK_SESSION_ID 1 の計 6 のはず）", n)
	}
}
