package loop

// 記憶・引き継ぎのパスを本体（メイン）の作業ツリーで解決する。
//
// 確かめること:
//   - 作業ツリー（git worktree add で作った場所）の中から呼んでも、本体の引き継ぎを読む（作業ツリーには .gitignore された symlink が無く、以前は何も読めなかった）
//   - 本体で呼んだときの挙動は変わらない
//   - git でない場所では今までどおり（その場所の .claude/memories を見る）
//   - 作業ツリーの中で完了を積んでも、本体の引き継ぎが新しければ差し戻さない（「書いたのに差し戻される」を起こさない）

import (
	"testing"
)

// setupWorktreeRepo は本体（repo）と作業ツリー（wt）を本物の git で作る。
func setupWorktreeRepo(s *sandbox) {
	s.mkdir("repo")
	s.git("-C", s.p("repo"), "init", "-q", "-b", "main")
	s.write(s.p("repo", "f.txt"), "x\n")
	s.git("-C", s.p("repo"), "add", "f.txt")
	s.git("-C", s.p("repo"), "commit", "-q", "-m", "初回")
	s.git("-C", s.p("repo"), "worktree", "add", "-q", s.p("wt"))
}

func TestMemoriesResolveToMainWorktree(t *testing.T) {
	needGit(t)
	proj := func(d string) map[string]string { return map[string]string{"CLAUDE_PROJECT_DIR": d} }
	scenario{name: "作業ツリーの中でも本体の引き継ぎを読む",
		setup: func(s *sandbox) {
			setupWorktreeRepo(s)
			s.write(s.p("repo", ".claude", "memories", "handoff.md"), "# 引き継ぎ\n\n本体の引き継ぎの本文\n")
			// git でない場所（従来どおりの解決を確かめる）
			s.write(s.p("plain", ".claude", "memories", "handoff.md"), "# 引き継ぎ\n\ngit でない場所の本文\n")
		},
		steps: []step{
			{name: "作業ツリーから呼んでも本体の引き継ぎが出る",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-memories", env: proj(s.p("wt")), input: "{}"}
				},
				want: func(t *testing.T, s *sandbox, g got) {
					t.Helper()
					// 見出しに出るパスが本体のものであること（作業ツリー側の複製を読んでいない証拠）
					wantContext("SessionStart", []string{"本体の引き継ぎの本文", s.repoHandoff()}, nil)(t, s, g)
				}},
			{name: "本体から呼んだときは今までどおり",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-memories", env: proj(s.p("repo")), input: "{}"}
				},
				want: wantContext("SessionStart", []string{"本体の引き継ぎの本文"}, nil)},
			{name: "git でない場所では今までどおりその場所を見る",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-memories", env: proj(s.p("plain")), input: "{}"}
				},
				want: wantContext("SessionStart", []string{"git でない場所の本文"}, []string{"本体の引き継ぎの本文"})},
			{name: "LOOPTRACK_LOOP_MEMORIES_DIR は今までどおり優先する",
				mk: func(s *sandbox) call {
					return call{hook: "session-start-memories", input: "{}", env: map[string]string{
						"CLAUDE_PROJECT_DIR": s.p("wt"), "LOOPTRACK_LOOP_MEMORIES_DIR": s.p("plain", ".claude", "memories")}}
				},
				want: wantContext("SessionStart", []string{"git でない場所の本文"}, []string{"本体の引き継ぎの本文"})},
		}}.run(t)
}

// repoHandoff は本体の引き継ぎのパス（見出しに出るので、作業ツリー側でないことの証拠になる）。
func (s *sandbox) repoHandoff() string { return s.p("repo", ".claude", "memories", "handoff.md") }

func TestFreshnessUsesMainWorktreeHandoff(t *testing.T) {
	needGit(t)
	scenario{name: "作業ツリーで積んだ完了は本体の引き継ぎで満たされる",
		setup: func(s *sandbox) {
			setupWorktreeRepo(s)
			s.write(s.repoHandoff(), "# 引き継ぎ\n\n本体の引き継ぎの本文\n")
		},
		steps: []step{
			{name: "本体の引き継ぎが古ければ差し戻す",
				do: func(s *sandbox) {
					s.write(s.p("wt", ".claude", "handoff-pending.d", "w1"), "2026-09-21 00:00:00\tcommit\n")
					s.touch(s.repoHandoff(), oldTime)
					s.touch(s.p("wt", ".claude", "handoff-pending.d", "w1"), newTime)
				},
				mk: func(s *sandbox) call {
					return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("wt")},
						input: `{"session_id":"w1"}`}
				},
				want: func(t *testing.T, s *sandbox, g got) {
					t.Helper()
					blockHas([]string{s.repoHandoff()}, nil)(t, s, g)
				}},
			{name: "本体の引き継ぎを更新すれば通る（作業ツリーの中でも差し戻されない）",
				do: func(s *sandbox) {
					// 「このセッションが書いた」ことは本文の印で判定する（handoff.go の freshByWriterMark）
					s.write(s.repoHandoff(), "# 引き継ぎ\n\n本体の引き継ぎの本文\n"+writerMark("w1")+"\n")
					s.touch(s.repoHandoff(), newTime)
				},
				mk: func(s *sandbox) call {
					return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("wt")},
						input: `{"session_id":"w1"}`}
				},
				want: wantQuiet},
		}}.run(t)
}

func TestMainWorktreeRootFallsBack(t *testing.T) {
	s := newSandbox(t)
	if got := mainWorktreeRoot(s.mkdir("nogit")); got != "" {
		t.Errorf("git でない場所は \"\" のはず: %q", got)
	}
}
