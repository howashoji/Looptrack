package loop

// 引き継ぎ鮮度ガードの backend issue（handoffdigest.go）: 着手中のイシューのセッション要約で Stop を判定する。
// サーバの応答はスタブの CLI（testdata/stubcli）が返す `looptrack issue summary --limit 0 --json` の形で与える。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// digestSummaryJSON は summary --json の応答（in_progress だけ）を 1 行の JSON にする。
func digestSummaryJSON(t *testing.T, items ...map[string]any) string {
	t.Helper()
	if items == nil {
		items = []map[string]any{}
	}
	b, err := json.Marshal(map[string]any{"in_progress": items, "in_review": []any{}, "ready": []any{}, "ready_total": 0, "counts": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// digestAt は latest_digest（created_at は at・セッション sess・要求のセッションと同じなら other_session は false）。
func digestAt(at time.Time, sess string, other bool) map[string]any {
	return map[string]any{"excerpt": "次の一手", "machine_label": "home", "created_at": at.UTC().Format(time.RFC3339),
		"age": "1分", "session_id": sess, "other_session": other}
}

func TestHandoffBackendIssue(t *testing.T) {
	stubCLIExe(t) // go が無ければ省略
	const sid = "d1"
	// マーカーの時刻（ファイルの更新時刻）は markerAt に固定する
	markerAt := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	pend := func(s *sandbox) string { return s.p(".claude", "handoff-pending.d", sid) }
	putMarker := func(s *sandbox) {
		s.write(pend(s), "2026-10-25 12:00:00\tissue.close\tABC-0009\n")
		s.touch(pend(s), markerAt)
	}
	// stop は backend issue の Stop。summary はスタブが返す（セッション ID を LOOPTRACK_SESSION_ID で明示して呼ばれたときだけ）
	stop := func(summary string, exit int, active bool, extra ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			cli := stubCLI(s, "issue-cli",
				stubRule{Args: "summary --limit 0 --json", Env: "LOOPTRACK_SESSION_ID=" + sid, Out: summary, Exit: exit},
				stubRule{Exit: 9, Err: "セッション ID を明示せずに呼ばれた"})
			env := map[string]string{"CLAUDE_PROJECT_DIR": s.root, "LOOPTRACK_LOOP_HANDOFF_BACKEND": "issue", "LOOPTRACK_LOOP_ISSUE_CLI": cli}
			for i := 0; i+1 < len(extra); i += 2 {
				env[extra[i]] = extra[i+1]
			}
			in := `{"session_id":"` + sid + `"}`
			if active {
				in = `{"session_id":"` + sid + `","stop_hook_active":true}`
			}
			return call{hook: "stop-handoff-freshness", env: env, input: in}
		}
	}
	own := func(id string, d map[string]any) map[string]any {
		it := map[string]any{"id": id, "status": "In Progress", "own_session": true}
		if d != nil {
			it["latest_digest"] = d
		}
		return it
	}
	wantBlockWith := func(words ...string) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, s *sandbox, g got) {
			t.Helper()
			if stopKind(g) != "BLOCK" {
				t.Fatalf("差し戻すはず: %s%s", g.out.Stdout, g.why())
			}
			for _, w := range words {
				if !strings.Contains(g.res.Block, w) {
					t.Errorf("差し戻しの文に「%s」が無い:\n%s", w, g.res.Block)
				}
			}
			if !s.exists(pend(s)) {
				t.Errorf("差し戻してもマーカーは残すはず")
			}
		}
	}
	gone := func(s *sandbox) string { return pend(s) }

	scenario{name: "差し戻す（要約が無い・完了より古い・別のセッションのもの）と通す（新しい要約）", steps: []step{
		{name: "要約が無い → 差し戻し・送るコマンドと完了のイベントの行が出る", do: putMarker,
			mk: stop(digestSummaryJSON(t, own("ABC-0001", nil)), 0, false),
			want: wantBlockWith("looptrack session digest ABC-0001 -", "ABC-0001: このセッションが書いた要約がありません",
				"ABC-0009", ".claude/handoff-pending.d/"+sid)},
		{name: "stop_hook_active は通す（差し戻しは 1 回だけ）",
			mk: stop(digestSummaryJSON(t, own("ABC-0001", nil)), 0, true), want: wantStop("PASS")},
		{name: "要約が完了より古い → 差し戻し",
			mk:   stop(digestSummaryJSON(t, own("ABC-0001", digestAt(markerAt.Add(-time.Minute), sid, false))), 0, false),
			want: wantBlockWith("looptrack session digest ABC-0001 -", "作業の完了より前のものです")},
		{name: "最新の要約が別のセッションのもの → 差し戻し",
			mk:   stop(digestSummaryJSON(t, own("ABC-0001", digestAt(markerAt.Add(time.Minute), "other", true))), 0, false),
			want: wantBlockWith("looptrack session digest ABC-0001 -", "別のセッションが書いたもの")},
		{name: "2 件のうち 1 件だけ新しい → 足りない 1 件だけを挙げて差し戻す",
			mk: stop(digestSummaryJSON(t, own("ABC-0001", digestAt(markerAt.Add(time.Minute), sid, false)), own("ABC-0002", nil)), 0, false),
			want: func(t *testing.T, s *sandbox, g got) {
				wantBlockWith("looptrack session digest ABC-0002 -")(t, s, g)
				if strings.Contains(g.res.Block, "looptrack session digest ABC-0001 -") {
					t.Errorf("要約のあるイシューまで挙げた:\n%s", g.res.Block)
				}
			}},
		{name: "完了と同じ秒の要約は新しいと見る → 通してマーカーを畳む",
			mk:   stop(digestSummaryJSON(t, own("ABC-0001", digestAt(markerAt, sid, false))), 0, false),
			want: all(wantStop("PASS"), wantExists(gone, false))},
		{name: "新しい要約がある → 通してマーカーを畳む", do: putMarker,
			mk:   stop(digestSummaryJSON(t, own("ABC-0001", digestAt(markerAt.Add(time.Minute), sid, false))), 0, false),
			want: all(wantStop("PASS"), wantExists(gone, false))},
		{name: "other_session が無くても session_id がこのセッションなら自分の要約",
			do: putMarker,
			mk: func(s *sandbox) call {
				d := digestAt(markerAt.Add(time.Minute), sid, false)
				delete(d, "other_session")
				return stop(digestSummaryJSON(t, own("ABC-0001", d)), 0, false)(s)
			},
			want: all(wantStop("PASS"), wantExists(gone, false))},
	}}.run(t)

	scenario{name: "判定の対象（このセッションが着手したものだけ）", steps: []step{
		{name: "別のセッションが着手したイシューは見ない（要約が無くても通す）", do: putMarker,
			mk:   stop(digestSummaryJSON(t, map[string]any{"id": "ABC-0003", "other_session": true}), 0, false),
			want: all(wantQuiet, wantExists(gone, false))},
		{name: "着手中のイシューが無ければ通す（close した後の Stop）", do: putMarker,
			mk: stop(digestSummaryJSON(t), 0, false), want: all(wantQuiet, wantExists(gone, false))},
		{name: "着手したセッションを判定できないもの（own_session が無い）は無いものとし、注意で知らせる", do: putMarker,
			mk:   stop(digestSummaryJSON(t, map[string]any{"id": "ABC-0004", "cross_path_session": true}), 0, false),
			want: all(wantSystemMessage("判定できないので、セッション要約を確かめていません: ABC-0004"), wantExists(gone, false))},
	}}.run(t)

	scenario{name: "サーバに届かない・応答が読めないときは止めない（fail-open）", steps: []step{
		{name: "summary が失敗（サーバに届かない）→ 注意を出して通す・マーカーは残す", do: putMarker,
			mk: stop("", 1, false),
			want: all(wantSystemMessage("サーバから着手中のイシューを読めなかったので、セッション要約を確かめずに通しました（looptrack issue summary が終了コード 1 で終わりました）"),
				wantExists(gone, true))},
		{name: "応答が JSON でない → 注意を出して通す",
			mk: stop("not json", 0, false), want: all(wantSystemMessage("summary の応答を読めません"), wantExists(gone, true))},
		{name: "summary を起動できない（実行の権限が無い）→ 理由を添えた注意を出して通す",
			mk: func(s *sandbox) call {
				c := stop("", 0, false)(s)
				p := s.p("not-executable")
				s.write(p, "x\n") // 実行の権限の無いふつうのファイル（isFile は通り、起動で失敗する）
				c.env["LOOPTRACK_LOOP_ISSUE_CLI"] = p
				return c
			},
			want: all(wantSystemMessage("looptrack issue summary を起動できなかったか、途中で止められました: "), wantExists(gone, true))},
		{name: "looptrack の実行ファイルが無い → 注意を出して通す",
			mk:   stop("", 0, false, "LOOPTRACK_LOOP_ISSUE_CLI", "/no/such/looptrack"),
			want: all(wantSystemMessage("looptrack の実行ファイルが見つかりません"), wantExists(gone, true))},
		// 対照: 同じ置き場・同じスタブで、届けば差し戻す（fail-open の経路が判定を素通りしていないこと）
		{name: "対照: 届けば差し戻す",
			mk: stop(digestSummaryJSON(t, own("ABC-0001", nil)), 0, false), want: wantBlockWith("looptrack session digest ABC-0001 -")},
	}}.run(t)

	// 対照: file バックエンドは変わらない（同じスタブが「要約が無い」と答えても、引き継ぎのファイルだけで判定する）
	H := func(s *sandbox) string { return s.p(".claude", "memories", "handoff.md") }
	fileStop := func(s *sandbox) call {
		cli := stubCLI(s, "issue-cli", stubRule{Out: digestSummaryJSON(t, own("ABC-0001", nil))})
		return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": s.root, "LOOPTRACK_LOOP_ISSUE_CLI": cli},
			input: `{"session_id":"` + sid + `"}`}
	}
	scenario{name: "対照: file バックエンドは要約を見ない", steps: []step{
		{name: "引き継ぎが新しければ通す", do: func(s *sandbox) {
			putMarker(s)
			s.write(H(s), "x\n")
			s.touch(H(s), markerAt.Add(time.Hour))
		}, mk: fileStop, want: all(wantQuiet, wantExists(gone, false))},
		{name: "引き継ぎが古ければ差し戻す（要約のコマンドは出さない）", do: func(s *sandbox) {
			putMarker(s)
			s.touch(H(s), markerAt.Add(-time.Hour))
		}, mk: fileStop, want: func(t *testing.T, _ *sandbox, g got) {
			if stopKind(g) != "BLOCK" || strings.Contains(g.res.Block, "session digest") {
				t.Errorf("file バックエンドの差し戻しのはず: %s", g.out.Stdout)
			}
		}},
	}}.run(t)
}

// 作業の完了を検知したとき、backend issue なら要約を送るコマンドを案内する（file では出さない）。
func TestHandoffBackendIssueDetectedHint(t *testing.T) {
	mk := func(backend string) func(*sandbox) call {
		return func(s *sandbox) call {
			env := map[string]string{"CLAUDE_PROJECT_DIR": s.root}
			if backend != "" {
				env["LOOPTRACK_LOOP_HANDOFF_BACKEND"] = backend
			}
			return call{hook: "post-work-complete-handoff-mark", env: env, input: jsonInput(map[string]any{"session_id": "d2", "tool_name": "Bash",
				"tool_input": map[string]any{"command": "looptrack issue close ABC-0001"}, "tool_response": map[string]any{"stdout": "ABC-0001 Done"}})}
		}
	}
	scenario{name: "完了の案内", steps: []step{
		{name: "issue", mk: mk("issue"), want: wantContext("PostToolUse", []string{"looptrack session digest <ID> -"}, nil)},
		{name: "file（対照）", mk: mk(""), want: wantContext("PostToolUse", []string{"ABC-0001"}, []string{"looptrack session digest"})},
	}}.run(t)
}

// judgeDigests の英語の文面にもコマンドが出る（文面は i18n。キーの取り違えを見る）。
func TestJudgeDigestsEnglish(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	doc := map[string]any{"in_progress": []any{map[string]any{"id": "ABC-0001", "own_session": true}}}
	d := judgeDigests(i18n.EN, doc, "s", at, "backend issue")
	if d.fresh || len(d.stale) != 1 {
		t.Fatalf("要約が無いので古いはず: %+v", d)
	}
	w := d.where(i18n.EN)
	for _, s := range []string{"  looptrack session digest ABC-0001 -", "no digest written by this session"} {
		if !strings.Contains(w, s) {
			t.Errorf("英語の文面に「%s」が無い:\n%s", s, w)
		}
	}
}

// プロジェクト別ルール session.digest_required: 入なら backend（既定の file・command）に関わらず要約の判定も行い、
// 両方が通ったときだけ通す。切なら backend の判定だけ。ルールの値は同じ summary の応答 1 本で知る。
func TestHandoffDigestRequiredRule(t *testing.T) {
	stubCLIExe(t) // go が無ければ省略
	const sid = "r1"
	markerAt := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	pend := func(s *sandbox) string { return s.p(".claude", "handoff-pending.d", sid) }
	H := func(s *sandbox) string { return s.p(".claude", "memories", "handoff.md") }
	// marker は完了のマーカーを置き、引き継ぎのファイルを handoffFresh なら完了より新しく・そうでなければ古くする
	marker := func(handoffFresh bool) func(*sandbox) {
		return func(s *sandbox) {
			s.write(pend(s), "2026-10-25 12:00:00\tissue.status\tDone\tABC-0009\n")
			s.touch(pend(s), markerAt)
			s.write(H(s), "x\n")
			if handoffFresh {
				s.touch(H(s), markerAt.Add(time.Hour))
			} else {
				s.touch(H(s), markerAt.Add(-time.Hour))
			}
		}
	}
	summary := func(rule bool, digest map[string]any) string {
		it := map[string]any{"id": "ABC-0001", "status": "In Progress", "own_session": true}
		if digest != nil {
			it["latest_digest"] = digest
		}
		b, _ := json.Marshal(map[string]any{"in_progress": []any{it}, "in_review": []any{}, "ready": []any{}, "ready_total": 0,
			"counts": map[string]any{}, "session_digest_required": rule})
		return string(b)
	}
	stop := func(out string, exit int, extra ...string) func(*sandbox) call {
		return func(s *sandbox) call {
			cli := stubCLI(s, "issue-cli", stubRule{Args: "summary --limit 0 --json", Env: "LOOPTRACK_SESSION_ID=" + sid, Out: out, Exit: exit})
			env := map[string]string{"CLAUDE_PROJECT_DIR": s.root, "LOOPTRACK_LOOP_ISSUE_CLI": cli}
			for i := 0; i+1 < len(extra); i += 2 {
				env[extra[i]] = extra[i+1]
			}
			return call{hook: "stop-handoff-freshness", env: env, input: `{"session_id":"` + sid + `"}`}
		}
	}
	blockWith := func(want []string, deny []string) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, s *sandbox, g got) {
			t.Helper()
			if stopKind(g) != "BLOCK" {
				t.Fatalf("差し戻すはず: %s%s", g.out.Stdout, g.why())
			}
			for _, w := range want {
				if !strings.Contains(g.res.Block, w) {
					t.Errorf("差し戻しの文に「%s」が無い:\n%s", w, g.res.Block)
				}
			}
			for _, w := range deny {
				if strings.Contains(g.res.Block, w) {
					t.Errorf("差し戻しの文に「%s」が出た:\n%s", w, g.res.Block)
				}
			}
		}
	}
	fresh := digestAt(markerAt.Add(time.Minute), sid, false)
	cmd := "looptrack session digest ABC-0001 -"

	scenario{name: "ルールが入: file バックエンドでも要約を確かめる", steps: []step{
		{name: "引き継ぎは新しいが要約が無い → 要約のコマンドで差し戻す", do: marker(true),
			mk: stop(summary(true, nil), 0), want: all(blockWith([]string{cmd, "ABC-0009"}, []string{"の最終更新"}), wantExists(pend, true))},
		{name: "引き継ぎも古く要約も無い → 両方の理由で差し戻す", do: marker(false),
			mk: stop(summary(true, nil), 0), want: blockWith([]string{cmd, "の最終更新"}, nil)},
		{name: "引き継ぎは古いが要約は新しい → file の理由だけで差し戻す", do: marker(false),
			mk: stop(summary(true, fresh), 0), want: blockWith([]string{"の最終更新"}, []string{cmd})},
		{name: "引き継ぎも要約も新しい → 通してマーカーを畳む", do: marker(true),
			mk: stop(summary(true, fresh), 0), want: all(wantQuiet, wantExists(pend, false))},
		{name: "command バックエンドが判定しない（終了コード 3）でも、要約が無ければ差し戻す", do: marker(true),
			mk:   stop(summary(true, nil), 0, "LOOPTRACK_LOOP_HANDOFF_BACKEND", "command", "LOOPTRACK_LOOP_HANDOFF_CHECK_CMD", "exit 3"),
			want: blockWith([]string{cmd}, nil)},
		{name: "サーバに届かない → ルールが分からないので file の判定だけ（通す）", do: marker(true),
			mk: stop("", 1), want: all(wantQuiet, wantExists(pend, false))},
	}}.run(t)

	scenario{name: "ルールが切: backend の判定だけ（要約が無くても通す）", steps: []step{
		{name: "引き継ぎが新しい・要約が無い → 通す", do: marker(true),
			mk: stop(summary(false, nil), 0), want: all(wantQuiet, wantExists(pend, false))},
		{name: "引き継ぎが古い・要約が無い → file の理由だけで差し戻す（要約のコマンドは出さない）", do: marker(false),
			mk: stop(summary(false, nil), 0), want: blockWith([]string{"の最終更新"}, []string{"session digest"})},
		{name: "command が判定しない（終了コード 3）・要約が無い → 従来どおり通してマーカーに触れない", do: marker(true),
			mk:   stop(summary(false, nil), 0, "LOOPTRACK_LOOP_HANDOFF_BACKEND", "command", "LOOPTRACK_LOOP_HANDOFF_CHECK_CMD", "exit 3"),
			want: all(wantQuiet, wantExists(pend, true))},
	}}.run(t)
}

// ルールのための summary は、backend の判定の後に hook の持ち時間（入口の ctx の締切・9 秒）の残りの中でだけ取る。
// 以前は backend の判定の前に固定の 5 秒で取っていたので、command の判定（8 秒まで）と合わせて持ち時間を越え、
// 入口の打ち切りが何も出さずに通した（ルールが切のプロジェクトでも、backend が差し戻すはずの Stop が黙って通った）。
func TestHandoffDigestRuleBudget(t *testing.T) {
	stubCLIExe(t) // go が無ければ省略
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash が無い（command バックエンドの判定コマンドは bash -c で動く）")
	}
	const sid = "b1"
	markerAt := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	pend := func(s *sandbox) string { return s.p(".claude", "handoff-pending.d", sid) }
	putMarker := func(s *sandbox) {
		s.write(pend(s), "2026-10-25 12:00:00\tissue.status\tDone\tABC-0009\n")
		s.touch(pend(s), markerAt)
	}
	summary := func(rule bool, items ...map[string]any) string {
		b, _ := json.Marshal(map[string]any{"in_progress": items, "in_review": []any{}, "ready": []any{}, "ready_total": 0,
			"counts": map[string]any{}, "session_digest_required": rule})
		return string(b)
	}
	// stop は command バックエンドの Stop。判定コマンドは checkCmd、summary のスタブは sleepMS 待ってから out を返す
	stop := func(checkCmd, out string, sleepMS int) func(*sandbox) call {
		return func(s *sandbox) call {
			cli := stubCLI(s, "issue-cli", stubRule{Args: "summary --limit 0 --json", Env: "LOOPTRACK_SESSION_ID=" + sid, Out: out, SleepMS: sleepMS})
			return call{hook: "stop-handoff-freshness", input: `{"session_id":"` + sid + `"}`, env: map[string]string{
				"CLAUDE_PROJECT_DIR": s.root, "LOOPTRACK_LOOP_ISSUE_CLI": cli,
				"LOOPTRACK_LOOP_HANDOFF_BACKEND": "command", "LOOPTRACK_LOOP_HANDOFF_CHECK_CMD": checkCmd}}
		}
	}
	// inBudget は hook が持ち時間の中で終えたこと（越えると何も出さずに通るので、判定の検査より先に見る）
	inBudget := func(t *testing.T, g got) {
		t.Helper()
		t.Logf("経過 %s（打ち切り %s）", g.elapsed.Round(10*time.Millisecond), g.limit)
		if g.limit > 0 && g.elapsed >= g.limit {
			t.Fatalf("hook の持ち時間を越えた（経過 %s・打ち切り %s）:%s", g.elapsed, g.limit, g.why())
		}
	}
	rule := "プロジェクト別ルール session.digest_required"

	scenario{name: "backend が差し戻すなら、遅い summary に持ち時間を食われずに差し戻す", steps: []step{
		{name: "ルールが切・判定コマンドが 7.5 秒後に「古い」・summary は 2 秒 → 差し戻す（文面は backend のまま）", do: putMarker,
			mk: stop("sleep 7.5; exit 1", summary(false), 2000),
			want: func(t *testing.T, s *sandbox, g got) {
				inBudget(t, g)
				if stopKind(g) != "BLOCK" {
					t.Fatalf("差し戻すはず: %s%s", g.out.Stdout, g.why())
				}
				if !strings.Contains(g.res.Block, "判定コマンド（LOOPTRACK_LOOP_HANDOFF_CHECK_CMD）が「古い」と返しました") {
					t.Errorf("command の理由が無い:\n%s", g.res.Block)
				}
				for _, w := range []string{"session digest", rule} {
					if strings.Contains(g.res.Block, w) {
						t.Errorf("差し戻しの文に「%s」が出た（backend の文面のままのはず）:\n%s", w, g.res.Block)
					}
				}
				if !s.exists(pend(s)) {
					t.Errorf("差し戻してもマーカーは残すはず")
				}
			}},
	}}.run(t)

	scenario{name: "backend が通し、summary が残りの持ち時間を越えるなら、注意を出して通す", steps: []step{
		{name: "判定コマンドが 4 秒後に「新しい」・summary は 6 秒（ルールが入で要約が無い）→ 打ち切って注意・backend の判定で通す", do: putMarker,
			mk: stop("sleep 4; exit 0", summary(true, map[string]any{"id": "ABC-0001", "status": "In Progress", "own_session": true}), 6000),
			want: func(t *testing.T, s *sandbox, g got) {
				inBudget(t, g)
				if stopKind(g) != "OUTPUT" || g.res.Block != "" {
					t.Fatalf("注意を出して通すはず: %s%s", g.out.Stdout, g.why())
				}
				for _, w := range []string{rule, "で打ち切りました", "looptrack session digest <ID> -"} {
					if !strings.Contains(g.res.SystemMessage, w) {
						t.Errorf("注意に「%s」が無い（黙って通した）:\n%q%s", w, g.res.SystemMessage, g.why())
					}
				}
				if strings.Contains(g.res.SystemMessage, "backend issue") {
					t.Errorf("ルールの注意が backend issue を名乗った:\n%s", g.res.SystemMessage)
				}
				if s.exists(pend(s)) {
					t.Errorf("backend の判定（新しい）どおりマーカーを畳むはず")
				}
			}},
		// 対照: 同じ置き場で summary が速ければ、ルールの判定が効いて差し戻す（注意の経路が判定を素通りしていないこと）
		{name: "対照: summary が速ければ、ルールで差し戻す", do: putMarker,
			mk: stop("exit 0", summary(true, map[string]any{"id": "ABC-0001", "status": "In Progress", "own_session": true}), 0),
			want: func(t *testing.T, s *sandbox, g got) {
				if stopKind(g) != "BLOCK" || !strings.Contains(g.res.Block, "looptrack session digest ABC-0001 -") {
					t.Fatalf("前提が崩れています（速い summary でルールの差し戻しにならない）: %s%s", g.out.Stdout, g.why())
				}
			}},
		{name: "ルールの判定の注意（着手したセッションが分からない）はルールを名乗る", do: putMarker,
			mk: stop("exit 0", summary(true, map[string]any{"id": "ABC-0004", "status": "In Progress", "cross_path_session": true}), 0),
			want: func(t *testing.T, s *sandbox, g got) {
				if !strings.Contains(g.res.SystemMessage, "引き継ぎの鮮度ガード（"+rule+"）") || !strings.Contains(g.res.SystemMessage, "ABC-0004") {
					t.Errorf("ルールを名乗る注意のはず:\n%q%s", g.res.SystemMessage, g.why())
				}
				if strings.Contains(g.res.SystemMessage, "backend issue") {
					t.Errorf("ルールの注意が backend issue を名乗った:\n%s", g.res.SystemMessage)
				}
			}},
	}}.run(t)
}

// digestBudget / ruleDigests: 持ち時間の残りが digestMinBudget より短ければ summary を取らず、ルールを確かめていない注意を返す。
func TestRuleDigestsSkipsWhenBudgetShort(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "looptrack")
	if err := os.WriteFile(cli, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e := &Env{
		Getenv: func(k string) string {
			return map[string]string{"LOOPTRACK_LOOP_ISSUE_CLI": cli, "LOOPTRACK_LANG": "ja"}[k]
		},
		Environ: func() []string { return nil },
		Getwd:   t.TempDir,
		Run: func(context.Context, Command) ([]byte, int, error) {
			calls++
			return []byte(`{"in_progress":[],"session_digest_required":true}`), 0, nil
		},
	}
	ev := hookio.Event{Raw: map[string]any{"session_id": "s1"}}
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	short, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok, unchecked := ruleDigests(short, ev, e, t.TempDir(), at); ok || calls != 0 ||
		!strings.Contains(unchecked, "hook の持ち時間の残り") || !strings.Contains(unchecked, "session.digest_required") {
		t.Errorf("残り 1 秒では取らずに注意を返すはず: ok=%v calls=%d unchecked=%q", ok, calls, unchecked)
	}
	// 対照: 締切の無い ctx・残りが十分な ctx では取る
	for _, c := range []context.Context{context.Background(), func() context.Context {
		c, cancel := context.WithTimeout(context.Background(), 9*time.Second)
		t.Cleanup(cancel)
		return c
	}()} {
		calls = 0
		if d, ok, unchecked := ruleDigests(c, ev, e, t.TempDir(), at); !ok || calls != 1 || unchecked != "" || !d.fresh {
			t.Errorf("前提が崩れています（残りが十分なのに summary を取らない）: ok=%v calls=%d unchecked=%q", ok, calls, unchecked)
		}
	}
	if limit, _, ok := digestBudget(context.Background()); !ok || limit != digestFetchTimeout {
		t.Errorf("締切の無い ctx では digestFetchTimeout のはず: %s %v", limit, ok)
	}
}

// digestSessionID: summary に渡すセッション ID は 明示の LOOPTRACK_SESSION_ID → hook の入力の session_id → AI の環境変数 の順。
// backend issue の判定（checkDigests）とルールの判定（ruleDigests）の両方で、子プロセスに渡す LOOPTRACK_SESSION_ID を見る。
func TestDigestSessionIDOrder(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "looptrack")
	if err := os.WriteFile(cli, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		input map[string]any
		env   map[string]string
		want  string // 子プロセスに渡る LOOPTRACK_SESSION_ID（"" なら summary を呼ばない）
	}{
		{"入力だけ", map[string]any{"session_id": "sess-office"}, nil, "sess-office"},
		{"入力（camelCase）だけ", map[string]any{"sessionId": "sess-camel"}, nil, "sess-camel"},
		{"明示の環境変数が入力に勝つ", map[string]any{"session_id": "sess-office"}, map[string]string{"LOOPTRACK_SESSION_ID": "sess-explicit"}, "sess-explicit"},
		{"入力が AI の環境変数に勝つ", map[string]any{"session_id": "sess-office"}, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-ai"}, "sess-office"},
		{"入力が無ければ AI の環境変数", map[string]any{}, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-ai"}, "sess-ai"},
		{"どれも無い（対照: 呼ばない）", map[string]any{}, nil, ""},
	}
	for _, tc := range cases {
		for _, which := range []string{"checkDigests", "ruleDigests"} {
			t.Run(tc.name+"/"+which, func(t *testing.T) {
				vars := map[string]string{"LOOPTRACK_LOOP_ISSUE_CLI": cli, "LOOPTRACK_LANG": "ja"}
				for k, v := range tc.env {
					vars[k] = v
				}
				var got []string
				e := &Env{
					Getenv:  func(k string) string { return vars[k] },
					Environ: func() []string { return nil },
					Getwd:   t.TempDir,
					Run: func(_ context.Context, c Command) ([]byte, int, error) {
						for _, kv := range c.Env {
							if strings.HasPrefix(kv, "LOOPTRACK_SESSION_ID=") {
								got = append(got, strings.TrimPrefix(kv, "LOOPTRACK_SESSION_ID="))
							}
						}
						return []byte(`{"in_progress":[],"session_digest_required":true}`), 0, nil
					},
				}
				ev := hookio.Event{Raw: tc.input}
				if which == "checkDigests" {
					checkDigests(context.Background(), ev, e, t.TempDir(), at)
				} else {
					ruleDigests(context.Background(), ev, e, t.TempDir(), at)
				}
				switch {
				case tc.want == "" && len(got) != 0:
					t.Errorf("セッション ID が無いのに summary を呼んだ: %q", got)
				case tc.want != "" && (len(got) != 1 || got[0] != tc.want):
					t.Errorf("LOOPTRACK_SESSION_ID = %q, want [%q]", got, tc.want)
				}
			})
		}
	}
}
