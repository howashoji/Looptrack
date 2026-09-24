package loop

// 本体以外の作業ツリーに取り残された引き継ぎの知らせ（strayhandoff.go）。
//
// 確かめること: 止めずに systemMessage で知らせる / 同じ顔ぶれでは 1 回だけ / 取り残しが消えたら黙る。

import (
	"path/filepath"
	"strings"
	"testing"
)

// wantStrayNotice は systemMessage に want の語をすべて含むこと（want が空なら systemMessage が無いこと）。
//
// 期待の語は filepath.ToSlash を通す。知らせに並ぶパスは strayHandoffNotice が**意図して** / 区切りで出す
// （relpath と同じ理由: AI に示すコマンドに埋めるため。Windows の Claude Code の Bash は Git Bash で \ は
// エスケープとして消える）。期待を filepath.Join の形のまま（Windows では \ 区切り）置くと、実装が正しくても
// Windows でだけ食い違う。ここは Windows で期待を緩めるのではなく、**実装が出す形に期待をそろえる**もの
// （macOS・linux では ToSlash は何もしないので、確かめる内容は変わらない）。
func wantStrayNotice(want ...string) func(*testing.T, *sandbox, got) {
	return func(t *testing.T, _ *sandbox, g got) {
		t.Helper()
		if len(want) == 0 {
			if g.res.SystemMessage != "" {
				t.Errorf("systemMessage が出た: %q", g.res.SystemMessage)
			}
			return
		}
		for _, w := range want {
			if !strings.Contains(g.res.SystemMessage, filepath.ToSlash(w)) {
				t.Errorf("systemMessage に「%s」が無い:\n%s", filepath.ToSlash(w), g.res.SystemMessage)
			}
		}
	}
}

func TestStrayHandoffNotice(t *testing.T) {
	needGit(t)
	scenario{name: "本体以外に取り残された引き継ぎを知らせる",
		setup: func(s *sandbox) {
			setupWorktreeRepo(s)
			s.write(s.repoHandoff(), "# 引き継ぎ\n\n本体の引き継ぎの本文\n")
			s.write(s.p("wt", ".claude", "memories", "handoff.md"), "# 引き継ぎ\n\n取り残された本文\n")
		},
		steps: []step{
			{name: "本体の Stop で取り残しを知らせる（止めない）",
				mk: func(s *sandbox) call {
					return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("repo")},
						input: `{"session_id":"m1"}`}
				},
				want: func(t *testing.T, s *sandbox, g got) {
					t.Helper()
					if g.res.Block != "" {
						t.Errorf("止めてはいけない: %s", g.res.Block)
					}
					wantStrayNotice(s.p("wt", ".claude", "memories", "handoff.md"), s.repoHandoff())(t, s, g)
				}},
			{name: "同じ顔ぶれでは 2 回目は出さない",
				mk: func(s *sandbox) call {
					return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("repo")},
						input: `{"session_id":"m1"}`}
				},
				want: wantStrayNotice()},
			{name: "取り残しが消えたら何も言わない",
				do: func(s *sandbox) { _ = removeFile(s.p("wt", ".claude", "memories")) },
				mk: func(s *sandbox) call {
					return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("repo")},
						input: `{"session_id":"m1"}`}
				},
				want: wantStrayNotice()},
		}}.run(t)
}
