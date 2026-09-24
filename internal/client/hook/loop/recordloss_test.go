package loop

// 記録の消失の検知（recordloss.go）。
//
// 確かめること:
//   - 前回あった記録が無くなったら、「更新が遅れている」とは別の文言で知らせる（1 回だけ）
//   - 控えが 1 つも無いとき（＝この仕掛けを入れる前と同じ状態）は何も出さない
//   - 控えの片方（鮮度の状態の側）が記録もろとも消えても、もう片方（git のディレクトリの側）から検知する
//   - 記憶のディレクトリが symlink でも働き、symlink への置き換え（移設）を消失と誤って読まない

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// blockHas は差し戻しの理由に want の語をすべて含み、deny の語を含まないこと。
func blockHas(want []string, deny []string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if g.json["decision"] != "block" || g.res.Block == "" {
			t.Fatalf("decision: block のはず: %q", g.out.Stdout)
		}
		for _, w := range want {
			if !strings.Contains(g.res.Block, w) {
				t.Errorf("「%s」が無い:\n%s", w, g.res.Block)
			}
		}
		for _, w := range deny {
			if strings.Contains(g.res.Block, w) {
				t.Errorf("「%s」が出た:\n%s", w, g.res.Block)
			}
		}
	}
}

func TestRecordLoss(t *testing.T) {
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	memDir := func(s *sandbox) string { return R(s, ".claude", "memories") }
	mem := func(s *sandbox, name string) string { return filepath.Join(memDir(s), name) }
	primary := func(s *sandbox) string { return R(s, ".claude", ".looptrack-freshness", "memories-seen.json") }
	mirror := func(s *sandbox) string { return R(s, ".git", "looptrack", "memories-seen.json") }
	stop := func(sid string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)},
				input: `{"session_id":"` + sid + `"}`}
		}
	}
	// setup は git の作業ツリー（.git はディレクトリがあれば足りる）と記憶の 2 ファイル。
	setup := func(s *sandbox) {
		s.mkdir("repo", ".git")
		s.write(mem(s, "handoff.md"), "# 引き継ぎ\n\n> 要約: 途中\n")
		s.write(mem(s, "caveat-i18n.md"), "# 注意\n\n> 要約: 落とし穴\n")
	}

	scenario{name: "消失の検知", setup: setup, steps: []step{
		{name: "控えが無い初回は何も出さず、2 か所に控えを取る", mk: stop("r1"),
			want: all(wantQuiet, wantExists(primary, true), wantExists(mirror, true))},
		{name: "記憶が丸ごと消えたら、更新遅延とは別の文言で知らせる",
			do: func(s *sandbox) { _ = removeFile(memDir(s)) },
			mk: stop("r1"),
			want: blockHas(
				[]string{".claude/memories/handoff.md", ".claude/memories/caveat-i18n.md", "更新の遅れではなく消失です"},
				[]string{"作業の完了に追いついていません"})},
		{name: "同じ消失では 2 回目は出さない（控えを取り直している）", mk: stop("r1"), want: wantQuiet},
	}}.run(t)

	scenario{name: "控えが 1 つも無ければ知らせない（この仕掛けを入れる前と同じ）", setup: setup, steps: []step{
		{name: "控えを取る前に消しても何も出さない",
			do:   func(s *sandbox) { _ = removeFile(memDir(s)) },
			mk:   stop("r2"),
			want: wantQuiet},
	}}.run(t)

	scenario{name: "控えの片方が記録もろとも消えても検知する", setup: setup, steps: []step{
		{name: "まず控えを取る", mk: stop("r3"), want: wantQuiet},
		{name: ".claude 配下（記憶と鮮度の状態）が丸ごと消えても、git の側の控えから知らせる",
			do:   func(s *sandbox) { _ = removeFile(R(s, ".claude")) },
			mk:   stop("r3"),
			want: blockHas([]string{".claude/memories/handoff.md", "控え", "残っていた"}, nil)},
		{name: "控えは 2 か所にそろえ直す", mk: stop("r3"),
			want: all(wantQuiet, wantExists(primary, true), wantExists(mirror, true))},
	}}.run(t)

	scenario{name: "更新遅延は従来どおりの文言", setup: setup, steps: []step{
		{name: "まず控えを取る", mk: stop("r4"), want: wantQuiet},
		{name: "記録は在るがマーカーが新しい",
			do: func(s *sandbox) {
				s.write(R(s, ".claude", "handoff-pending.d", "r4"), "2026-09-18 10:00:00\tcommit\n")
				s.touch(mem(s, "handoff.md"), oldTime)
			},
			mk:   stop("r4"),
			want: blockHas([]string{"作業の完了に追いついていません"}, []string{"更新の遅れではなく消失です"})},
	}}.run(t)

	scenario{name: "見る場所（設定）が変わった控えとは突き合わせない", setup: setup, steps: []step{
		{name: "既定の場所で控えを取る", mk: stop("r7"), want: wantQuiet},
		{name: "LOOPTRACK_LOOP_MEMORIES_DIR を別の場所に変えても消失とは言わない",
			do: func(s *sandbox) { s.write(R(s, "other", "handoff.md"), "# 別の置き場\n") },
			mk: func(s *sandbox) call {
				return call{hook: "stop-handoff-freshness", input: `{"session_id":"r7"}`,
					env: map[string]string{"CLAUDE_PROJECT_DIR": R(s), "LOOPTRACK_LOOP_MEMORIES_DIR": R(s, "other"),
						"LOOPTRACK_LOOP_HANDOFF_FILE": R(s, "other", "handoff.md")}}
			},
			want: wantQuiet},
	}}.run(t)

	if runtime.GOOS == "windows" {
		return // symlink を作れないことがある
	}
	link := func(s *sandbox) {
		s.t.Helper()
		if err := os.MkdirAll(R(s, ".claude"), 0o755); err != nil {
			s.t.Fatal(err)
		}
		// .claude/memories → ../private/memories（版管理下への移設と同じ形）
		if err := os.Symlink(filepath.Join("..", "private", "memories"), memDir(s)); err != nil {
			s.t.Skipf("symlink を作れないため省略: %v", err)
		}
	}
	scenario{name: "記憶のディレクトリが symlink でも働く",
		setup: func(s *sandbox) {
			s.mkdir("repo", ".git")
			s.write(R(s, "private", "memories", "handoff.md"), "# 引き継ぎ\n")
			link(s)
		}, steps: []step{
			{name: "symlink 越しに控えを取る", mk: stop("r5"),
				want: func(t *testing.T, s *sandbox, g got) {
					wantQuiet(t, s, g)
					// 控えには辿る前のパスを書く（移設で名前が変わらないように）
					if c := s.read(primary(s)); !strings.Contains(c, ".claude/memories/handoff.md") {
						t.Errorf("控えの中身: %s", c)
					}
				}},
			{name: "実体（private/memories）を消したら symlink 越しでも知らせる",
				do:   func(s *sandbox) { _ = removeFile(R(s, "private", "memories", "handoff.md")) },
				mk:   stop("r5"),
				want: blockHas([]string{".claude/memories/handoff.md", "更新の遅れではなく消失です"}, nil)},
		}}.run(t)

	scenario{name: "symlink への置き換え（移設）を消失と読まない", setup: setup, steps: []step{
		{name: "実体のディレクトリで控えを取る", mk: stop("r6"), want: wantQuiet},
		{name: "private/memories へ移して symlink にしても何も出さない",
			do: func(s *sandbox) {
				if err := os.MkdirAll(R(s, "private"), 0o755); err != nil {
					s.t.Fatal(err)
				}
				if err := os.Rename(memDir(s), R(s, "private", "memories")); err != nil {
					s.t.Fatal(err)
				}
				link(s)
			},
			mk:   stop("r6"),
			want: wantQuiet},
	}}.run(t)
}
