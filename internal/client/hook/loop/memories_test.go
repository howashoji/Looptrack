package loop

// kit/loop/verify/verify-memories.sh（9 ケース）と verify-iteration.sh（9 ケース）の移植。

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func ctxHas(want ...string) func(*testing.T, *sandbox, got) {
	return wantContext("SessionStart", want, nil)
}

// afterNoticeIsHeading は、省略の知らせの次の行が `## ` の見出しであること（節の途中から始めない）。
func afterNoticeIsHeading(t *testing.T, _ *sandbox, g got) {
	t.Helper()
	lines := strings.Split(g.ctx(), "\n")
	for i, l := range lines {
		if !strings.Contains(l, "末尾（新しい節）だけ") {
			continue
		}
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "## ") {
			next := ""
			if i+1 < len(lines) {
				next = lines[i+1]
			}
			t.Errorf("省略の知らせの次の行は `## ` の見出しのはず / 実際 %q%s", next, g.why())
		}
		return
	}
	t.Errorf("省略の知らせが見つかりません:\n%s%s", g.ctx(), g.why())
}

func countLines(text, want string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if l == want {
			n++
		}
	}
	return n
}

func TestMemories(t *testing.T) {
	proj := func(s *sandbox) map[string]string { return map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")} }
	mem := func(s *sandbox, name string) string { return s.p("proj", ".claude", "memories", name) }
	scenario{name: "ファイル版の記憶の注入",
		setup: func(s *sandbox) {
			s.write(mem(s, "handoff.md"), "# 引き継ぎ\n\n> 要約: 検証まで完了\n\n次の一手: レビュー待ち\n")
			s.write(mem(s, "decision-numbering.md"), "# 決定\n\n> 要約: 採番はサーバが行う\n")
			s.write(mem(s, "caveat-x.md"), "# 注意\n\n本文だけ\n")
			s.write(mem(s, "README.md"), "# README\n\n> 要約: 出してはいけない\n")
			s.write(s.p("other", "latest.md"), "別の置き場\n")
		},
		steps: []step{
			{name: "引き継ぎの全文・最終更新時刻・他の記憶の要約",
				mk: func(s *sandbox) call { return call{hook: "session-start-memories", env: proj(s), input: "{}"} },
				want: all(
					ctxHas("次の一手: レビュー待ち"),                                 // 引き継ぎの全文を入れる
					ctxHas("ファイルの最終更新:"),                                   // 引き継ぎの最終更新時刻を出す
					ctxHas("decision-numbering.md — 採番はサーバが行う"),            // 他の記憶は要約の行だけ一覧する
					ctxHas("caveat-x.md — （要約の行なし）"),                       // 要約の行が無いものはそう出す
					wantContext("SessionStart", nil, []string{"出してはいけない"}), // README.md は一覧に出さない
				)},
			{name: "引き継ぎが無ければ作るよう促す",
				do:   func(s *sandbox) { _ = removeFile(mem(s, "handoff.md")) },
				mk:   func(s *sandbox) call { return call{hook: "session-start-memories", env: proj(s), input: "{}"} },
				want: ctxHas("handoff.md がまだありません")},
			{name: "記憶のディレクトリが無ければ何も出さない",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-memories", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("none")}, input: "{}"}
				},
				want: wantQuiet},
			{name: "第 1 引数のディレクトリと LOOPTRACK_LOOP_HANDOFF_FILE の名前を使う",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-memories", args: []string{s.p("other")},
						env: map[string]string{"LOOPTRACK_LOOP_HANDOFF_FILE": "latest.md", "CLAUDE_PROJECT_DIR": s.p("none")}, input: "{}"}
				},
				want: ctxHas("別の置き場")},
			{name: "LOOPTRACK_LOOP_MEMORIES_DIR でディレクトリを渡せる",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-memories", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("none"),
						"LOOPTRACK_LOOP_MEMORIES_DIR": s.p("other"), "LOOPTRACK_LOOP_HANDOFF_FILE": "latest.md"}, input: "{}"}
				},
				want: ctxHas("別の置き場")},
		}}.run(t)
}

// iterState は session-start-iteration の出力を quiet / zero / bugs:<件数>[:listed] / unknown / invalid / other に分類する（verify の state）。
func iterState(g got) string {
	if g.quiet() {
		return "quiet"
	}
	if g.hookEventName() != "SessionStart" || g.ctx() == "" {
		return "invalid"
	}
	c := g.ctx()
	m := regexp.MustCompile(`未解決の bug: (\d+) 件`).FindStringSubmatch(c)
	switch {
	case strings.Contains(c, "取得できませんでした"):
		return "unknown"
	case m != nil && m[1] == "0":
		return "zero"
	case m != nil:
		s := "bugs:" + m[1]
		if strings.Contains(c, "TST-0007 一覧が空") {
			s += ":listed"
		}
		return s
	}
	return "other"
}

func wantIter(want string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if s := iterState(g); s != want {
			t.Errorf("期待 %s / 実際 %s: %s%s", want, s, g.out.Stdout, g.why())
		}
	}
}

// スタブの CLI: STUB_MODE が bugs / fail / それ以外（none）で返事を変える。list --type bug --json 以外は失敗する（規則に無い＝終了コード 2）。
var iterStubRules = []stubRule{
	{Args: "list --type bug --json", Env: "STUB_MODE=fail", Err: "接続できません", Exit: 1},
	{Args: "list --type bug --json", Env: "STUB_MODE=bugs",
		Out: `{"items": [{"id": "TST-0003", "title": "保存に失敗する"}, {"id": "TST-0007", "title": "一覧が空"}]}`},
	{Args: "list --type bug --json", Out: `{"items": []}`},
}

func TestIteration(t *testing.T) {
	stub := ""
	state := func(mode, gdir string) func(s *sandbox) call {
		return func(s *sandbox) call {
			if gdir == "" {
				gdir = "."
			}
			return call{hook: "session-start-iteration", input: "{}", env: map[string]string{"STUB_MODE": mode,
				"CLAUDE_PROJECT_DIR": s.p("proj"), "LOOPTRACK_LOOP_ISSUE_CLI": stub, "LOOPTRACK_LOOP_GATES_DIR": gdir}}
		}
	}
	makefile := "test:\n\t@true\n"
	scenario{name: "実装イテレーションの状態（未解決の bug）",
		setup: func(s *sandbox) {
			stub = stubCLI(s, "stub", iterStubRules...)
			s.mkdir("proj")
		},
		steps: []step{
			// ゲートの Makefile が無い（実装フェーズの前）
			{name: "bug 0 件なら黙る", mk: state("none", ""), want: wantIter("quiet")},
			{name: "取得できなくても黙る（文書作業の雑音を避ける）", mk: state("fail", ""), want: wantIter("quiet")},
			{name: "bug があれば件数と一覧を出す", mk: state("bugs", ""), want: wantIter("bugs:2:listed")},
			// ゲートの Makefile がある
			{name: "bug 0 件なら「着手できる」", do: func(s *sandbox) { s.write(s.p("proj", "Makefile"), makefile) },
				mk: state("none", ""), want: all(wantIter("zero"), wantContext("SessionStart", []string{"looptrack gates"}, []string{"gates.sh", "bash "}))},
			{name: "取得できなければ「件数不明」（0 件と見なさない）", mk: state("fail", ""), want: wantIter("unknown")},
			{name: "bug があれば件数と一覧", mk: state("bugs", ""), want: wantIter("bugs:2:listed")},
			// LOOPTRACK_LOOP_GATES_DIR
			{name: "既定（.）には Makefile が無いので黙る",
				do: func(s *sandbox) {
					_ = removeFile(s.p("proj", "Makefile"))
					s.write(s.p("proj", "app", "Makefile"), makefile)
				},
				mk: state("none", ""), want: wantIter("quiet")},
			{name: "LOOPTRACK_LOOP_GATES_DIR=app の Makefile を見る", mk: state("none", "app"), want: wantIter("zero")},
			// CLI が無い
			{name: "CLI も Makefile も無ければ黙る",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-iteration", input: "{}", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("none")}}
				},
				want: wantQuiet},
		}}.run(t)
}

// TestMemoriesInjectionLimit は SessionStart の注入が上限（既定 6000 文字）を超えないことの回帰。
//
// 引き継ぎは in-place 更新の建前に反して追記で育ち、実測 107,311 文字になっていた。
// memories.go を元の「全文を入れる」実装に戻すと、最初のケースが red になる。
func TestMemoriesInjectionLimit(t *testing.T) {
	proj := func(s *sandbox) map[string]string { return map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")} }
	mem := func(s *sandbox, name string) string { return s.p("proj", ".claude", "memories", name) }
	// 追記で育った引き継ぎ（節が 30・全体で 1 万字超）。末尾が最新の節
	big := func() string {
		var b strings.Builder
		b.WriteString("# 引き継ぎ\n\n> 要約: 途中\n")
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(&b, "\n## セッション %02d の申し送り\n\n%s\n", i, strings.Repeat("古い経緯の行。", 40))
		}
		b.WriteString("\n## セッション 31 の申し送り\n\n次の一手: レビュー待ち\n")
		return b.String()
	}()
	// 末尾の 1 節だけが予算より長い引き継ぎ（「現在地」は無い）。切り出しがその節の途中から始まる
	bigTail := func() string {
		var b strings.Builder
		b.WriteString("# 引き継ぎ\n\n> 要約: 途中\n")
		for i := 1; i <= 5; i++ {
			fmt.Fprintf(&b, "\n## セッション %02d の申し送り\n\n%s\n", i, strings.Repeat("古い経緯の行。", 40))
		}
		b.WriteString("\n## セッション 31 の申し送り\n\n" + strings.Repeat("末尾の節の行。\n", 2500))
		return b.String()
	}()
	within := func(limit int) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, _ *sandbox, g got) {
			t.Helper()
			if n := len([]rune(g.ctx())); n > limit {
				t.Errorf("注入は %d 文字以内のはず / 実際 %d 文字%s", limit, n, g.why())
			}
		}
	}
	atLeast := func(limit int) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, _ *sandbox, g got) {
			t.Helper()
			if n := len([]rune(g.ctx())); n < limit {
				t.Errorf("注入は %d 文字以上のはず / 実際 %d 文字%s", limit, n, g.why())
			}
		}
	}
	scenario{name: "注入の上限", setup: func(s *sandbox) { s.write(mem(s, "handoff.md"), big) }, steps: []step{
		{name: "上限を超える引き継ぎは末尾だけを入れる（既定 6000 文字）",
			mk: func(s *sandbox) call { return call{hook: "session-start-memories", env: proj(s), input: "{}"} },
			want: all(within(6000),
				atLeast(3000), // 既定が 1500 のままなら、ここまで入らない
				ctxHas("次の一手: レビュー待ち"),                                       // 末尾（最新の節）は入る
				ctxHas("末尾（新しい節）だけ"),                                         // 省いたことを知らせる
				wantContext("SessionStart", nil, []string{"セッション 01 の申し送り"}), // 古い節は入れない
				afterNoticeIsHeading, // 切り出しは `## ` から始まる
				// 「現在地」の見出しが 1 つも無い入力では、枠を取らずに既定（末尾だけ）に落ちる。何も警告しない
				wantContext("SessionStart", nil, []string{"現在地"}),
			)},
		{name: "LOOPTRACK_LOOP_MEMORIES_MAX_CHARS で上限を変えられる",
			mk: func(s *sandbox) call {
				return call{hook: "session-start-memories", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj"),
					"LOOPTRACK_LOOP_MEMORIES_MAX_CHARS": "600"}, input: "{}"}
			},
			want: all(within(600), ctxHas("次の一手: レビュー待ち"))},
		{name: "0 なら今までどおり全文を入れる",
			mk: func(s *sandbox) call {
				return call{hook: "session-start-memories", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj"),
					"LOOPTRACK_LOOP_MEMORIES_MAX_CHARS": "0"}, input: "{}"}
			},
			want: all(atLeast(len([]rune(big))), ctxHas("セッション 01 の申し送り"))},
		{name: "上限に収まる引き継ぎは全文をそのまま入れる（省略の知らせも出さない）",
			do: func(s *sandbox) {
				s.write(mem(s, "handoff.md"), "# 引き継ぎ\n\n> 要約: 検証まで完了\n\n次の一手: レビュー待ち\n")
			},
			mk: func(s *sandbox) call { return call{hook: "session-start-memories", env: proj(s), input: "{}"} },
			want: all(within(1500), ctxHas("> 要約: 検証まで完了"),
				wantContext("SessionStart", nil, []string{"末尾（新しい節）だけ"}))},
		{name: "節が予算より長くても、切り出しは見出しから始まる（1 つ手前の見出しまで戻る）",
			do:   func(s *sandbox) { s.write(mem(s, "handoff.md"), bigTail) },
			mk:   func(s *sandbox) call { return call{hook: "session-start-memories", env: proj(s), input: "{}"} },
			want: all(within(6000), afterNoticeIsHeading, ctxHas("## セッション 31 の申し送り"), ctxHas("この節は"))},
	}}.run(t)
}

// TestMemoriesCurrentSection は、上限を超える引き継ぎで「現在地」の節に別の枠を取ることの回帰。
//
// 末尾だけを入れる実装では、先頭寄りに置かれがちな「現在地」が必ず落ちる。落ちた分は読み手に
// 「無い」ではなく「変わっていない」と読まれるので、古い値が黙って生き延びる。
// 合成の入力で確かめる（実物の引き継ぎは写さない）。節が予算より長い場合を作るため、末尾の 1 節を 2 万字にしてある。
func TestMemoriesCurrentSection(t *testing.T) {
	proj := func(s *sandbox) map[string]string { return map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")} }
	mem := func(s *sandbox, name string) string { return s.p("proj", ".claude", "memories", name) }
	middle := func(b *strings.Builder) {
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(b, "\n## セッション %02d の申し送り\n\n%s\n", i, strings.Repeat("古い経緯の行。", 40))
		}
	}
	// 「現在地」が 2 つ（古いほうが先頭・新しいほうが末尾寄り）あり、末尾の 1 節が 2 万字
	two := func() string {
		var b strings.Builder
		b.WriteString("# 引き継ぎ\n\n> 要約: 途中\n")
		fmt.Fprintf(&b, "\n## 現在地（live で確認した値）\n\n古い値: main は 0000000 で 6 先行。%s\n", strings.Repeat("古い現在地の行。", 30))
		middle(&b)
		fmt.Fprintf(&b, "\n## 現在地（最新）\n\n新しい値: main は 1111111 で同期済み。%s\n", strings.Repeat("新しい現在地の行。", 25))
		b.WriteString("\n## セッション 31 の申し送り\n\n" + strings.Repeat("末尾の節の行。\n", 2500))
		return b.String()
	}()
	// 「現在地」が末尾の節そのもの（末尾の切り出しに既に入っている）
	tailIsCurrent := func() string {
		var b strings.Builder
		b.WriteString("# 引き継ぎ\n\n> 要約: 途中\n")
		middle(&b)
		b.WriteString("\n## 現在地（最新）\n\n新しい値: main は 1111111 で同期済み。\n")
		return b.String()
	}()
	within := func(limit int) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, _ *sandbox, g got) {
			t.Helper()
			if n := len([]rune(g.ctx())); n > limit {
				t.Errorf("注入は %d 文字以内のはず / 実際 %d 文字%s", limit, n, g.why())
			}
		}
	}
	once := func(line string) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, _ *sandbox, g got) {
			t.Helper()
			if n := countLines(g.ctx(), line); n != 1 {
				t.Errorf("「%s」の行はちょうど 1 回のはず / 実際 %d 回:\n%s%s", line, n, g.ctx(), g.why())
			}
		}
	}
	scenario{name: "現在地の枠", setup: func(s *sandbox) { s.write(mem(s, "handoff.md"), two) }, steps: []step{
		{name: "末尾から最も近い「現在地」の節だけを別の枠で入れる",
			mk: func(s *sandbox) call { return call{hook: "session-start-memories", env: proj(s), input: "{}"} },
			want: all(within(6000),
				afterNoticeIsHeading,           // 切り出しは `## ` から始まる
				ctxHas("## 現在地（最新）"),           // 見出しと
				ctxHas("新しい値: main は 1111111"), // その本文が入る
				ctxHas("末尾の節の行。"),              // 末尾の枠も残る
				ctxHas("## セッション 31 の申し送り"),    // 予算より長い節でも見出しは入る
				ctxHas("この節は"),                 // 途中からであることを断る
				wantContext("SessionStart", nil, []string{
					"## 現在地（live で確認した値）", // 先頭の（古い）ほうは入れない
					"古い値: main は 0000000",
				}),
			)},
		{name: "末尾の切り出しに既に入っているときは枠を取らない（同じ節を 2 回入れない）",
			do:   func(s *sandbox) { s.write(mem(s, "handoff.md"), tailIsCurrent) },
			mk:   func(s *sandbox) call { return call{hook: "session-start-memories", env: proj(s), input: "{}"} },
			want: all(within(6000), ctxHas("新しい値: main は 1111111"), once("## 現在地（最新）"))},
	}}.run(t)
}
