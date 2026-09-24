package core

// 鮮度ガードのケース（以前のテスト（1.0.0 より前）の全ケースを API モードで。ファイルモードのケースは偽 API の更新時刻で同じことを確かめる）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// freshSetup は偽 API のイシュー（TST-0001 Todo・TST-0002 / 0003 Todo・TST-0009 Done。どれも 1 時間前に更新）。
func freshSetup(s *sandbox) {
	old := now() - 3600
	s.api.issues["TST-0001"] = &fakeIssue{"Todo", "参照されるイシュー", old}
	s.api.issues["TST-0002"] = &fakeIssue{"Todo", "子イシュー", old}
	s.api.issues["TST-0003"] = &fakeIssue{"Todo", "子イシュー3", old}
	s.api.issues["TST-0009"] = &fakeIssue{"Done", "閉じたイシュー", old}
}

func markCall(input map[string]any) func(*sandbox) call {
	return func(*sandbox) call { return call{hook: "issue-freshness-mark", input: input} }
}

func bashIn(session, command string) map[string]any {
	in := map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": command}}
	if session != "" {
		in["session_id"] = session
	}
	return in
}

// engage は CLI の show <ID> で参照する手順。
func engage(id, session string) step {
	return step{name: "engage " + id + " " + session, mk: markCall(bashIn(session, "looptrack issue show "+id)), want: wantQuiet}
}

func bash(session, command string) step {
	return step{name: "bash " + command, mk: markCall(bashIn(session, command)), want: wantQuiet}
}

func subBash(session, command string) step {
	in := bashIn(session, command)
	in["agent_id"], in["agent_type"] = "a123", "general-purpose"
	return step{name: "subagent " + command, mk: markCall(in), want: wantQuiet}
}

// edit は <ルート>/<path> の Edit（tool = Edit・Write など）。
func edit(session, tool, path string) step {
	return step{name: tool + " " + path, mk: func(s *sandbox) call {
		fp := path
		if !filepath.IsAbs(fp) {
			fp = s.p(path)
		}
		in := map[string]any{"hook_event_name": "PostToolUse", "tool_name": tool, "tool_input": map[string]any{"file_path": fp}}
		if session != "" {
			in["session_id"] = session
		}
		return call{hook: "issue-freshness-mark", input: in}
	}, want: wantQuiet}
}

func doWork(session string) step { return edit(session, "Edit", "src/main.go") }

func stopStep(name, session string, active bool, want func(*testing.T, *sandbox, got)) step {
	return step{name: name, mk: func(*sandbox) call {
		in := map[string]any{"hook_event_name": "Stop", "stop_hook_active": active}
		if session != "" {
			in["session_id"] = session
		}
		return call{hook: "issue-freshness-check", input: in}
	}, want: want}
}

func cliStep(name string, env map[string]string, args ...string) step {
	return step{name: "cli " + strings.Join(args, " "), mk: func(*sandbox) call { return call{cli: args, env: env} }}
}

func updateIssue(id string) step {
	return step{name: "更新 " + id, do: func(s *sandbox) { s.api.setUpdated(id, now()+1) }}
}

func TestFreshness(t *testing.T) {
	sid := "s1"
	pass := wantQuiet
	cases := []scenario{
		{name: "参照のみ・実作業なしは素通り", steps: []step{engage("TST-0001", sid), stopStep("check", sid, false, pass)}},
		{name: "実作業のみ・参照なしは素通り", steps: []step{doWork(sid), stopStep("check", sid, false, pass)}},
		{name: "参照＋実作業で未更新なら差し戻す・更新すれば通り記録を畳む", steps: []step{
			engage("TST-0001", sid), doWork(sid),
			stopStep("check", sid, false, wantBlock("TST-0001 [Todo] 参照されるイシュー", "ファイル変更: src/main.go")),
			updateIssue("TST-0001"),
			stopStep("更新すれば通る", sid, false, pass),
			{name: "通ったら記録を畳む", want: nil, do: func(s *sandbox) {
				if exists(filepath.Join(s.scope(sid), "engaged")) || exists(s.p(".claude", ".looptrack-freshness", "project.json")) {
					t.Error("記録（engaged・project.json）が残っている")
				}
			}},
		}},
		{name: "クローズ済みのイシューは対象外", steps: []step{engage("TST-0009", sid), doWork(sid), stopStep("check", sid, false, pass)}},
		{name: "存在しない ID は無視", steps: []step{engage("TST-9999", sid), doWork(sid), stopStep("check", sid, false, pass)}},
		{name: "stop_hook_active なら素通り", steps: []step{engage("TST-0001", sid), doWork(sid), stopStep("check", sid, true, pass)}},
		{name: "ack で対象から外れる", steps: []step{engage("TST-0001", sid), doWork(sid),
			{name: "ack", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました: TST-0001", true)},
			stopStep("check", sid, false, pass)}},
		{name: "ack した ID は次のターンのプロンプトに出ても戻らない", steps: []step{
			{name: "prompt", mk: markCall(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": sid, "prompt": "TST-0001 をやって"}), want: wantQuiet},
			doWork(sid),
			{name: "ack", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました: TST-0001", true)},
			{name: "次のターンのプロンプト", mk: markCall(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": sid, "prompt": "TST-0001 はどうなった"}), want: wantQuiet},
			stopStep("戻らない", sid, false, pass)}},
		{name: "ack した ID は次のターンのコマンドに出ても戻らない", steps: []step{engage("TST-0001", sid), doWork(sid),
			{name: "ack", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました: TST-0001", true)},
			engage("TST-0001", sid),
			stopStep("戻らない", sid, false, pass)}},
		{name: "同じ ID を二度 ack しても控えは増えない", steps: []step{engage("TST-0001", sid),
			{name: "ack 1 回目", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました", true)},
			{name: "ack 2 回目", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました", true)},
			{name: "控えは 1 行", want: nil, do: func(s *sandbox) {
				b, err := os.ReadFile(filepath.Join(s.scope(sid), "acked"))
				if err != nil {
					t.Fatalf("acked が読めない: %v", err)
				}
				if n := len(strings.Fields(string(b))); n != 1 {
					t.Errorf("控えが %d 行（1 行のはず）: %q", n, string(b))
				}
			}}}},
		{name: "ack した ID は show に出る", steps: []step{engage("TST-0001", sid),
			{name: "ack", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました", true)},
			{name: "show", mk: func(*sandbox) call { return call{cli: []string{"show"}} }, want: wantStdout("acked   : TST-0001", true)}}},
		{name: "ack はセッションをまたいで効かない", steps: []step{engage("TST-0001", "s1"),
			{name: "ack", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました", true)},
			engage("TST-0001", "s2"), doWork("s2"), stopStep("別のセッションでは挙がる", "s2", false, wantBlock("TST-0001"))}},
		{name: "reset の後は ack の記録も消える", steps: []step{engage("TST-0001", sid),
			{name: "ack", mk: func(*sandbox) call { return call{cli: []string{"ack", "TST-0001"}} }, want: wantStdout("検査対象から外しました", true)},
			{name: "reset", mk: func(*sandbox) call { return call{cli: []string{"reset"}} }, want: wantStdout("記録を消しました", true)},
			{name: "show に acked が残らない", mk: func(*sandbox) call { return call{cli: []string{"show"}} },
				want: func(t *testing.T, _ *sandbox, g got) {
					if strings.Contains(g.out.Stdout, "acked") {
						t.Errorf("reset の後も ack の記録が残っている:\n%s", g.out.Stdout)
					}
				}}}},
		{name: "reset で対象から外れる", steps: []step{engage("TST-0001", sid), doWork(sid),
			{name: "reset", mk: func(*sandbox) call { return call{cli: []string{"reset"}} }, want: wantStdout("記録を消しました", true)},
			stopStep("check", sid, false, pass)}},
		{name: "セッションが変われば記録を引き継がない", steps: []step{engage("TST-0001", "s1"), doWork("s2"), stopStep("check", "s2", false, pass)}},
		{name: ".claude の下の編集は実作業ではない", steps: []step{engage("TST-0001", sid), edit(sid, "Edit", ".claude/memories/handoff.md"),
			stopStep("check", sid, false, pass)}},
		{name: "利用者の発話の ID も参照", steps: []step{
			{name: "prompt", mk: markCall(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": sid, "prompt": "TST-0001 の続きをやって"}), want: wantQuiet},
			doWork(sid), stopStep("check", sid, false, wantBlock("TST-0001"))}},
		{name: "他セッションからの連絡の ID は参照にしない", steps: []step{
			{name: "prompt", mk: markCall(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": sid,
				"prompt": "<cross-session-message from=\"uds:/tmp/x.sock\" from-name=\"22\">TST-0001 は私が見ています</cross-session-message>"}), want: wantQuiet},
			doWork(sid), stopStep("check", sid, false, pass)}},
		{name: "system の注意書きの ID は参照にしない（差し戻し文の再掲を含む）", steps: []step{
			{name: "prompt", mk: markCall(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": sid,
				"prompt": "<system-reminder>\n参照したのに一度も更新されていないイシュー:\n   • TST-0001 [Todo] 参照されるイシュー\n</system-reminder>"}), want: wantQuiet},
			doWork(sid), stopStep("check", sid, false, pass)}},
		{name: "連絡に混ざっていても利用者が書いた ID は参照", steps: []step{
			{name: "prompt", mk: markCall(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": sid,
				"prompt": "<cross-session-message from=\"uds:/tmp/x.sock\">TST-0002 を引き取ります</cross-session-message>\nTST-0001 の続きをやって"}), want: wantQuiet},
			doWork(sid), stopStep("check", sid, false, func(t *testing.T, _ *sandbox, g got) {
				if !strings.Contains(g.res.Block, "TST-0001") {
					t.Errorf("利用者が書いた TST-0001 が挙がっていない\n%s", g.res.Block)
				}
				if strings.Contains(g.res.Block, "TST-0002") {
					t.Errorf("連絡の中の TST-0002 が挙がっている\n%s", g.res.Block)
				}
			})}},
		{name: "閉じタグが無ければ取り除かない（取りすぎない）", steps: []step{
			{name: "prompt", mk: markCall(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": sid,
				"prompt": "<cross-session-message from=\"x\">途中で切れた TST-0001"}), want: wantQuiet},
			doWork(sid), stopStep("check", sid, false, wantBlock("TST-0001"))}},
		{name: "git commit も実作業", steps: []step{engage("TST-0001", sid), bash(sid, "git commit -m 'x'"),
			stopStep("check", sid, false, wantBlock("git 操作: git commit -m"))}},
		{name: "コミットメッセージ先頭行の ID は参照", steps: []step{bash(sid, `git commit -m "TST-0001: 実装した"`),
			stopStep("check", sid, false, wantBlock("TST-0001"))}},
		{name: "コミットメッセージ本文の ID は参照にしない", steps: []step{
			bash(sid, "git commit -m \"$(cat <<'EOF'\n引き継ぎ更新\nTST-0001 と TST-0002 に触れた話\nEOF\n)\""),
			stopStep("check", sid, false, pass)}},
		{name: "引用符の中の git commit は実作業ではない", steps: []step{engage("TST-0001", sid),
			bash(sid, `echo '{"command":"git commit -m x"}'`), stopStep("check", sid, false, pass)}},
		{name: "work のラベルは実際の git コマンド", steps: []step{bash(sid, "export FOO=1\ncd /tmp && git push origin master"),
			{name: "show", mk: func(*sandbox) call { return call{cli: []string{"show"}} }, want: wantStdout("git 操作: git push origin master", true)}}},
		{name: "engaged が重複しない", steps: []step{
			bash(sid, "looptrack issue comment TST-0001 'TST-0001 は TST-0001 のことである TST-0001'"), doWork(sid),
			stopStep("check", sid, false, func(t *testing.T, _ *sandbox, g got) {
				if n := strings.Count(g.res.Block, "TST-0001 [Todo]"); n != 1 {
					t.Errorf("1 回のはず: %d\n%s", n, g.res.Block)
				}
			})}},
		{name: "別セッションの参照で ack 済みが戻らない・別セッションは別に検査される", steps: []step{
			engage("TST-0001", "s1"), doWork("s1"),
			cliStep("ack", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"}, "ack", "TST-0001"),
			engage("TST-0001", "s2"),
			stopStep("s1 は通る", "s1", false, pass),
			doWork("s2"),
			stopStep("s2 は止まる", "s2", false, wantBlock("TST-0001"))}},
		{name: "並行セッションが記録を消し合わない・他セッションの ack は効かない", steps: []step{
			engage("TST-0001", "s1"), doWork("s1"), engage("TST-0001", "s2"),
			stopStep("s1 は止まる", "s1", false, wantBlock()),
			cliStep("ack s2", map[string]string{"CLAUDE_CODE_SESSION_ID": "s2"}, "ack", "TST-0001"),
			stopStep("s1 はまだ止まる", "s1", false, wantBlock())}},
		{name: "サブエージェントの参照で ack 済みが戻らない", steps: []step{
			engage("TST-0001", sid), doWork(sid),
			cliStep("ack", map[string]string{"CLAUDE_CODE_SESSION_ID": sid}, "ack", "TST-0001"),
			subBash(sid, "looptrack issue show TST-0001"),
			stopStep("check", sid, false, pass)}},
		{name: "サブエージェントの作業は親の作業に数えない", steps: []step{
			subBash(sid, "looptrack issue show TST-0001"),
			{name: "subagent edit", mk: func(s *sandbox) call {
				return call{hook: "issue-freshness-mark", input: map[string]any{"hook_event_name": "PostToolUse", "session_id": sid, "agent_id": "a123",
					"tool_name": "Edit", "tool_input": map[string]any{"file_path": s.p("src", "x.go")}}}
			}, want: wantQuiet},
			engage("TST-0001", sid), stopStep("check", sid, false, pass)}},
		{name: "ルートの外のファイル変更は実作業ではない", steps: []step{
			engage("TST-0001", sid),
			{name: "外の Write", mk: func(s *sandbox) call {
				return call{hook: "issue-freshness-mark", input: map[string]any{"hook_event_name": "PostToolUse", "session_id": sid,
					"tool_name": "Write", "tool_input": map[string]any{"file_path": filepath.Join(s.dir, "proj-wt-0001", "src", "main.go")}}}
			}, want: wantQuiet},
			stopStep("外は通る", sid, false, pass),
			edit(sid, "Write", "src/main.go"),
			stopStep("中は止まる", sid, false, wantBlock("ファイル変更: src/main.go"))}},
		{name: "session_id が無い入力も 1 つの器で動く", steps: []step{
			engage("TST-0001", ""), doWork(""),
			stopStep("止まる", "", false, wantBlock()),
			cliStep("ack", nil, "ack", "TST-0001"),
			stopStep("ack が効く", "", false, pass)}},
		{name: "古い器は掃除される", steps: []step{
			engage("TST-0001", "old"),
			{name: "30 日前にする", do: func(s *sandbox) {
				old := time.Now().Add(-30 * 24 * time.Hour)
				_ = os.Chtimes(s.scope("old"), old, old)
			}},
			engage("TST-0001", "new"),
			{name: "old が無い", do: func(s *sandbox) {
				if exists(s.scope("old")) {
					t.Error("古い器が残っている")
				}
			}}}},
		{name: "ack と comment を同じコマンドにしても ack が効く", steps: []step{
			engage("TST-0001", sid), doWork(sid), cliStep("ack", nil, "ack", "TST-0001"),
			bash(sid, `looptrack issue comment TST-0001 "確認のみ" && looptrack issue-freshness ack TST-0001`),
			stopStep("check", sid, false, pass)}},
		{name: "reset を含むコマンドも参照を登録しない", steps: []step{doWork(sid),
			bash(sid, "looptrack issue-freshness reset; looptrack issue show TST-0001"),
			stopStep("check", sid, false, pass)}},
		{name: "引用符の中の ack では登録を止めない", steps: []step{doWork(sid),
			bash(sid, `echo "looptrack issue-freshness ack" && looptrack issue show TST-0001`),
			stopStep("check", sid, false, wantBlock("TST-0001"))}},
		{name: "mark は毎回サーバに聞かない", steps: []step{
			engage("TST-0001", sid), engage("TST-0001", sid), engage("TST-0001", sid),
			{name: "問い合わせは 1 回", do: func(s *sandbox) {
				n := 0
				for _, r := range s.api.requests() {
					if r.Path == basePath+"/projects/tst" {
						n++
					}
				}
				if n != 1 {
					t.Errorf("GET /projects/tst は 1 回のはず: %d", n)
				}
			}}}},
		{name: "サーバに繋がらなければ素通り（fail-open）", steps: []step{
			{name: "engage", mk: func(*sandbox) call {
				return call{hook: "issue-freshness-mark", downAPI: true, input: bashIn(sid, "looptrack issue show TST-0001")}
			}, want: wantQuiet},
			{name: "work", mk: func(s *sandbox) call {
				return call{hook: "issue-freshness-mark", downAPI: true, input: map[string]any{"hook_event_name": "PostToolUse", "session_id": sid,
					"tool_name": "Edit", "tool_input": map[string]any{"file_path": s.p("src/main.go")}}}
			}, want: wantQuiet},
			{name: "check", mk: func(*sandbox) call {
				return call{hook: "issue-freshness-check", downAPI: true, input: map[string]any{"hook_event_name": "Stop", "session_id": sid}}
			}, want: wantQuiet}}},
		{name: "活動の取得に失敗したら素通り（記録は残す）", steps: []step{
			engage("TST-0001", sid), doWork(sid),
			{name: "壊れた応答", do: func(s *sandbox) { s.api.activityBody = `{"items":[{"title":"id が無い"}]}` }},
			stopStep("check", sid, false, pass)}},
		{name: "プロジェクトが取れなければ何もしない", setup: func(s *sandbox) { s.api.projectStatus = 404 }, steps: []step{
			engage("TST-0001", sid), doWork(sid), stopStep("check", sid, false, pass)}},
		{name: "イシュー管理を使っていないなら何もしない", steps: []step{
			{name: "mark", mk: func(*sandbox) call {
				return call{hook: "issue-freshness-mark", noAPI: true, input: bashIn(sid, "looptrack issue show TST-0001")}
			}, want: wantQuiet},
			{name: "check", mk: func(*sandbox) call {
				return call{hook: "issue-freshness-check", noAPI: true, input: map[string]any{"hook_event_name": "Stop", "session_id": sid}}
			}, want: wantQuiet}}},
		{name: "トークンが無ければ何もしない（以前は exit 1）", steps: []step{
			{name: "mark", mk: func(*sandbox) call {
				return call{hook: "issue-freshness-mark", noToken: true, input: bashIn(sid, "looptrack issue show TST-0001")}
			}, want: wantQuiet}}},
		{name: "LOOPTRACK_LOOP_NO_BLOCK なら差し戻さず知らせる", steps: []step{engage("TST-0001", sid), doWork(sid),
			{name: "check", mk: func(*sandbox) call {
				return call{hook: "issue-freshness-check", env: map[string]string{"LOOPTRACK_LOOP_NO_BLOCK": "1"},
					input: map[string]any{"hook_event_name": "Stop", "session_id": sid}}
			}, want: func(t *testing.T, _ *sandbox, g got) {
				if g.res.Block != "" || !strings.Contains(g.res.SystemMessage, "TST-0001") {
					t.Errorf("systemMessage で知らせるはず: %q", g.out.Stdout)
				}
			}}}},
		{name: "show", steps: []step{engage("TST-0001", sid), doWork(sid),
			{name: "show", mk: func(*sandbox) call { return call{cli: []string{"show"}} }, want: func(t *testing.T, _ *sandbox, g got) {
				for _, w := range []string{"mode    : API（", "・tst）", "session : s1", "engaged : TST-0001", "work    : 1 件", "stale   : TST-0001"} {
					if !strings.Contains(g.out.Stdout, w) {
						t.Errorf("「%s」が無い:\n%s", w, g.out.Stdout)
					}
				}
			}},
			{name: "ack の引数なし", mk: func(*sandbox) call { return call{cli: []string{"ack"}} }, want: func(t *testing.T, _ *sandbox, g got) {
				if g.code != 1 {
					t.Errorf("exit 1 のはず: %d", g.code)
				}
			}},
		}},
	}
	// CLI の本文に書いた ID は参照に数えない（対象の ID 引数だけ数える）
	body := []struct{ name, cmd string }{
		{"comment の本文（引用符）", `looptrack issue comment TST-0001 "子 TST-0002 と TST-0003 を終えた"`},
		{"comment の本文（1 語の ID）", "looptrack issue comment TST-0001 TST-0002"},
		{"comment の本文（ヒアドキュメント）", "looptrack issue comment TST-0001 \"$(cat <<'EOF'\nTST-0002 済み\nTST-0003 済み\nEOF\n)\""},
		{"status --comment", `looptrack issue status TST-0001 "In Review" --comment "TST-0002 待ち"`},
		{"close --comment", `looptrack issue close TST-0001 --comment "TST-0002・TST-0003 で検証"`},
		{"new のタイトル・本文と --parent", `looptrack issue new "TST-0002 の続き" --body "TST-0003 参照" --parent TST-0001`},
		{"looptrack issue（Go 版の入口）", `looptrack issue comment TST-0001 "TST-0002 済み"`},
	}
	for _, b := range body {
		cmd := b.cmd
		sc := scenario{name: "本文の ID は数えない: " + b.name, steps: []step{bash(sid, cmd),
			{name: "show", mk: func(*sandbox) call { return call{cli: []string{"show"}} }, want: func(t *testing.T, _ *sandbox, g got) {
				if !strings.Contains(g.out.Stdout, "engaged : TST-0001\n") {
					t.Errorf("TST-0001 だけのはず:\n%s", g.out.Stdout)
				}
			}}}}
		cases = append(cases, sc)
	}
	engaged := []struct {
		name, cmd string
		want      string
	}{
		{"show / activity の対象 ID は数える", "cd /x && LOOPTRACK_PROJECT=tst looptrack issue show TST-0002 | head; looptrack issue activity TST-0001 TST-0003", "engaged : TST-0002, TST-0001, TST-0003\n"},
		{"イシューファイルの直接の読み取りは参照", "cat .claude/issues/open/TST-0002-子イシュー.md", "engaged : TST-0002\n"},
		{"引用符の中に書いただけの CLI の呼び出しは参照にしない", `echo "looptrack issue show TST-0002"`, "engaged : (なし)\n"},
		{"コミットメッセージ先頭行の ID は参照", `git commit -m "TST-0002: 実装した"`, "engaged : TST-0002\n"},
		{"--blocked-by の ID は数える", `looptrack issue edit TST-0001 --blocked-by TST-0002,TST-0003 --title "TST-0009"`, "engaged : TST-0001, TST-0002, TST-0003\n"},
		{"閉じていない引用符は実行される語から拾う", `looptrack issue show TST-0002 "TST-0003`, "engaged : TST-0002, TST-0003\n"},
		{"-m の後の ID", "git commit -am TST-0003\ngit push", "engaged : TST-0003\n"},
		{"--message= の形", "git commit --message=\"TST-0002 x\" && git push", "engaged : TST-0002\n"},
	}
	for _, e := range engaged {
		want := e.want
		cases = append(cases, scenario{name: "参照: " + e.name, steps: []step{bash(sid, e.cmd),
			{name: "show", mk: func(*sandbox) call { return call{cli: []string{"show"}} }, want: wantStdout(want, true)}}})
	}
	// merge-* などは実作業ではない
	for _, c := range []struct {
		cmd  string
		work bool
	}{
		{"git merge-base --is-ancestor server-app main", false}, {"git merge-file a b c", false},
		{"git merge-tree --write-tree a b", false}, {"git commit-tree HEAD^{tree}", false},
		{"git merge --no-edit feature", true}, {"git merge feature", true}, {"cd x && git merge --ff-only main", true},
		{"git  push", true}, {"xgit push", false}, {"git pushx", false}, {"git\tcommit", true},
	} {
		w := pass
		if c.work {
			w = wantBlock("git 操作: ")
		}
		cases = append(cases, scenario{name: "実作業: " + c.cmd, steps: []step{engage("TST-0001", sid), bash(sid, c.cmd), stopStep("check", sid, false, w)}})
	}
	for _, sc := range cases {
		sc.setup = chain(freshSetup, sc.setup)
		sc.run(t)
	}
}

func chain(fs ...func(*sandbox)) func(*sandbox) {
	return func(s *sandbox) {
		for _, f := range fs {
			if f != nil {
				f(s)
			}
		}
	}
}

// TestFreshnessForeign は Claude Code 以外（Copilot）が .claude/settings.json の配線から起動したとき。
func TestFreshnessForeign(t *testing.T) {
	sid := "s1"
	stopIn := map[string]any{"hook_event_name": "Stop", "session_id": sid, "stop_hook_active": false}
	steps := []step{engage("TST-0001", sid), doWork(sid), stopStep("対照: Claude Code なら差し戻す", sid, false, wantBlock())}
	for _, marks := range []map[string]string{{"COPILOT_AGENT": "1"}, {"COPILOT_CLI": "1"}, {"AI_AGENT": "github-copilot"}, {"VSCODE_PID": "4242"}} {
		m := marks
		steps = append(steps, step{name: "Copilot から", mk: func(*sandbox) call {
			return call{hook: "issue-freshness-check", bare: true, env: m, input: stopIn}
		}, want: wantQuiet})
	}
	steps = append(steps,
		step{name: "camelCase の入力", mk: func(s *sandbox) call {
			return call{hook: "issue-freshness-check", bare: true, input: map[string]any{"timestamp": 1, "cwd": s.proj, "sessionId": sid}}
		}, want: wantQuiet},
		// Copilot CLI 1.0.86 は .claude/settings.json の hook に CLAUDE_PROJECT_DIR・COPILOT_CLI=1・COPILOT_PROJECT_DIR を渡す
		step{name: "Copilot CLI から（CLAUDE_PROJECT_DIR + COPILOT_CLI）", mk: func(s *sandbox) call {
			return call{hook: "issue-freshness-check", bare: true, env: map[string]string{"CLAUDE_PROJECT_DIR": s.proj, "COPILOT_CLI": "1"}, input: stopIn}
		}, want: wantQuiet},
		step{name: "Claude Code から起動した Copilot CLI から（CLAUDECODE + COPILOT_PROJECT_DIR）", mk: func(s *sandbox) call {
			return call{hook: "issue-freshness-check", bare: true, env: map[string]string{"CLAUDECODE": "1", "CLAUDE_PROJECT_DIR": s.proj,
				"COPILOT_CLI": "1", "COPILOT_PROJECT_DIR": s.proj}, input: stopIn}
		}, want: wantQuiet},
		step{name: "Copilot CLI のシェルから起動した Claude Code なら差し戻す（CLAUDECODE + COPILOT_CLI）", mk: func(s *sandbox) call {
			return call{hook: "issue-freshness-check", bare: true, env: map[string]string{"CLAUDECODE": "1", "CLAUDE_PROJECT_DIR": s.proj, "COPILOT_CLI": "1"},
				input: stopIn}
		}, want: wantBlock()},
		step{name: "CLAUDECODE なら印があっても差し戻す", mk: func(*sandbox) call {
			return call{hook: "issue-freshness-check", bare: true, env: map[string]string{"CLAUDECODE": "1", "COPILOT_AGENT": "1", "CLAUDE_PROJECT_DIR": ""},
				input: stopIn}
		}, want: wantBlock()},
	)
	scenario{name: "check", setup: freshSetup, steps: steps}.run(t)

	scenario{name: "Copilot からの mark は記録しない", setup: freshSetup, steps: []step{
		{name: "mark", mk: func(*sandbox) call {
			return call{hook: "issue-freshness-mark", bare: true, env: map[string]string{"COPILOT_AGENT": "1"}, input: bashIn(sid, "looptrack issue show TST-0001")}
		}, want: wantQuiet},
		{name: "edit", mk: func(s *sandbox) call {
			return call{hook: "issue-freshness-mark", bare: true, env: map[string]string{"COPILOT_AGENT": "1"},
				input: map[string]any{"hook_event_name": "PostToolUse", "session_id": sid, "tool_name": "Edit", "tool_input": map[string]any{"file_path": s.p("src/main.go")}}}
		}, want: wantQuiet},
		stopStep("check", sid, false, wantQuiet),
	}}.run(t)
}

// TestFreshnessAgents は Claude Code 以外のツール名（hookio の種類）でも記録すること（Go 版だけ）。
func TestFreshnessAgents(t *testing.T) {
	sid := "s1"
	scenario{name: "Copilot のツール名", setup: freshSetup, steps: []step{
		{name: "bash", mk: func(*sandbox) call {
			return call{hook: "issue-freshness-mark", agent: hookio.Copilot, input: map[string]any{"hook_event_name": "PostToolUse", "session_id": sid,
				"tool_name": "bash", "tool_input": map[string]any{"command": "looptrack issue show TST-0001"}}}
		}, want: wantQuiet},
		{name: "create", mk: func(s *sandbox) call {
			return call{hook: "issue-freshness-mark", agent: hookio.Copilot, input: map[string]any{"hook_event_name": "PostToolUse", "session_id": sid,
				"tool_name": "create", "tool_input": map[string]any{"path": s.p("src/a.go")}}}
		}, want: wantQuiet},
		{name: "check", mk: func(*sandbox) call {
			return call{hook: "issue-freshness-check", agent: hookio.Copilot, input: map[string]any{"hook_event_name": "Stop", "session_id": sid}}
		}, want: func(t *testing.T, _ *sandbox, g got) {
			// Copilot の Stop は差し戻す（CLI 1.0.86 で確認。hookio の capabilities。理由の先頭に印を付ける）
			if !strings.HasPrefix(g.res.Block, hookio.CopilotStopMarker) || !strings.Contains(g.res.Block, "TST-0001") ||
				!strings.Contains(g.res.Block, "ファイル変更: src/a.go") {
				t.Errorf("差し戻すはず: %q", g.out.Stdout)
			}
		}},
	}}.run(t)
	scenario{name: "looptrack issue-freshness ack は逃げ道", setup: freshSetup, steps: []step{
		engage("TST-0001", sid), doWork(sid),
		bash(sid, "looptrack issue-freshness ack TST-0001 && looptrack issue show TST-0001"),
		cliStep("ack", nil, "ack", "TST-0001"),
		stopStep("check", sid, false, wantQuiet),
	}}.run(t)
}
