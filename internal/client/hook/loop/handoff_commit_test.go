package loop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestHandoffMarkCommitWhere は「コミットの記録に短い SHA・ブランチ・作業ツリーを残し、差し戻しの文面に出す」ことの回帰。
//
// 記録は本体の <状態>/handoff-pending.d/<セッション> に集まる（サブエージェントは親と同じ session_id を使う）。
// 別の作業ツリーの、main に未マージのブランチでコミットすると、本体の git log にはそのコミットが出ない。
// 記録が時刻と `commit` の 1 語だけだと、正当な記録が「無いはずのコミット」に見え、ガードの誤判定と読まれる。
// handoff.go の commitFields を外して `addEvent("commit")` だけに戻すと、下の段が軒並み red になる。
func TestHandoffMarkCommitWhere(t *testing.T) {
	needGit(t)
	R := func(s *sandbox, parts ...string) string { return s.p(append([]string{"repo"}, parts...)...) }
	W := func(s *sandbox) string { return s.p("wt") } // 別の作業ツリー（ブランチ feat）
	// Wsh はコマンドの文字列に書く形。引用符の無い `\` はシェルが打ち消しとして食う（`D:\a\wt` は `D:awt` になる）ので、
	// Windows では git が受け取れる `/` 区切りにする（利用者が Git Bash で打つ形。POSIX では W と同じ）。
	Wsh := func(s *sandbox) string { return filepath.ToSlash(W(s)) }
	pend := func(s *sandbox) string { return R(s, ".claude", "handoff-pending.d", "s1") }
	var sha, detachedSHA, commitOut string
	setup := func(s *sandbox) {
		s.git("init", "-q", "-b", "main", R(s))
		s.mkdir("repo", ".claude", "memories")
		s.write(R(s, "src", "a.txt"), "a\n")
		s.git("-C", R(s), "add", "src/a.txt")
		s.git("-C", R(s), "commit", "-qm", "base")
		s.git("-C", R(s), "worktree", "add", "-q", "-b", "feat", W(s))
		s.write(s.p("wt", "b.txt"), "b\n")
		s.git("-C", W(s), "add", "b.txt")
		commitOut = s.git("-C", W(s), "commit", "-m", "feat の作業")
		sha = s.git("-C", W(s), "rev-parse", "--short", "HEAD")
		// 前提: そのコミットは main に未マージで、本体の作業ツリーの履歴には出ない（出るなら、この検査は意味を失う）
		if err := exec.Command("git", "-C", R(s), "merge-base", "--is-ancestor", sha, "main").Run(); err == nil {
			s.t.Fatalf("前提が崩れている: %s が main に入っている", sha)
		}
	}
	mark := func(cmd string, resp func() string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "post-work-complete-handoff-mark", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s)}, cwd: R(s),
				input: jsonInput(map[string]any{"session_id": "s1", "tool_name": "Bash", "cwd": R(s),
					"tool_input": map[string]any{"command": cmd}, "tool_response": resp()})}
		}
	}
	stop := func(lang string) func(*sandbox) call {
		return func(s *sandbox) call {
			return call{hook: "stop-handoff-freshness", env: map[string]string{"CLAUDE_PROJECT_DIR": R(s), "LOOPTRACK_LANG": lang},
				input: jsonInput(map[string]any{"session_id": "s1"})}
		}
	}
	clear := func(s *sandbox) { os.RemoveAll(R(s, ".claude", "handoff-pending.d")) }
	// wantRecord は記録の 1 行（時刻を除く）を、実行時に決まる値（SHA・作業ツリーの実パス）で突き合わせる
	wantRecord := func(want func(s *sandbox) string) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, s *sandbox, g got) {
			t.Helper()
			wantRecordLines(pend, want(s))(t, s, g)
		}
	}
	wtTop := func(s *sandbox) string { return s.git("-C", W(s), "rev-parse", "--show-toplevel") }
	featLine := func(s *sandbox) string { return "commit\t" + sha + "\tfeat\t" + wtTop(s) }
	wantBlockHas := func(parts ...func(*sandbox) string) func(*testing.T, *sandbox, got) {
		return func(t *testing.T, s *sandbox, g got) {
			t.Helper()
			for _, p := range parts {
				if w := p(s); !strings.Contains(g.res.Block, w) {
					t.Errorf("差し戻しの文面に %q が無い: %s", w, g.res.Block)
				}
			}
		}
	}
	lit := func(v string) func(*sandbox) string { return func(*sandbox) string { return v } }
	shaOf := func(*sandbox) string { return sha }
	out := func() string { return commitOut }
	none := func() string { return "" }

	scenario{name: "別の作業ツリー（main に未マージ）でのコミット", setup: setup, steps: []step{
		{name: "git -C <作業ツリー> commit: 出力の [ブランチ SHA] と作業ツリーを記録する", do: clear,
			mk:   func(s *sandbox) call { return mark("git -C "+Wsh(s)+" commit -m x", out)(s) },
			want: all(wantMark("OUT"), wantRecord(featLine))},
		{name: "出力が無ければ、コミットした作業ツリーに問うて SHA とブランチを埋める", do: clear,
			mk:   func(s *sandbox) call { return mark("git -C "+Wsh(s)+" commit -m x", none)(s) },
			want: all(wantMark("OUT"), wantRecord(featLine))},
		{name: "cd <作業ツリー> && git commit も同じ", do: clear,
			mk:   func(s *sandbox) call { return mark("cd "+Wsh(s)+" && git add b.txt && git commit -m x", none)(s) },
			want: all(wantMark("OUT"), wantRecord(featLine))},
		{name: "相対の -C は cwd（本体）から継ぐ", do: clear,
			mk: mark("git -C ../wt commit -m x", none), want: all(wantMark("OUT"), wantRecord(featLine))},
		{name: "知らせの文面にも SHA とブランチが出る", do: clear,
			mk: func(s *sandbox) call { return mark("git -C "+Wsh(s)+" commit -m x", out)(s) },
			want: func(t *testing.T, s *sandbox, g got) {
				t.Helper()
				if !strings.Contains(g.ctx(), sha) || !strings.Contains(g.ctx(), "feat") {
					t.Errorf("SHA %s とブランチ feat が知らせに無い: %s", sha, g.ctx())
				}
			}},
		{name: "差し戻し（日本語）に SHA・ブランチ・作業ツリーが出る", mk: stop("ja"),
			want: all(wantStop("BLOCK"), wantBlockHas(lit("コミット "), shaOf, lit("ブランチ feat"), wtTop))},
		{name: "差し戻し（英語）にも出る", mk: stop("en"),
			want: all(wantStop("BLOCK"), wantBlockHas(lit("commit "), shaOf, lit("branch feat"), wtTop))},
		{name: "場所が読めない（~ や変数）なら作業ツリーは ? で、記録は落とさない", do: clear,
			mk:   mark("git -C $WT commit -m x", out),
			want: all(wantMark("OUT"), wantRecord(func(*sandbox) string { return "commit\t" + sha + "\tfeat\t?" }))},
		{name: "git の作業ツリーでない場所でも記録は落とさない（欄は ?）", do: clear,
			mk:   func(s *sandbox) call { return mark("git -C "+filepath.ToSlash(s.root)+" commit -m x", none)(s) },
			want: all(wantMark("OUT"), wantRecord(lit("commit\t?\t?\t?")))},
		{name: "失敗したコミット（nothing to commit）は従来どおり積まない", do: clear,
			mk: func(s *sandbox) call {
				return mark("git -C "+Wsh(s)+" commit -m x", func() string { return "nothing to commit, working tree clean" })(s)
			},
			want: all(wantMark("QUIET"), wantExists(pend, false))},
	}}.run(t)

	scenario{name: "detached HEAD でのコミット", setup: func(s *sandbox) {
		setup(s)
		s.git("-C", W(s), "checkout", "-q", "--detach")
		s.write(s.p("wt", "c.txt"), "c\n")
		s.git("-C", W(s), "add", "c.txt")
		commitOut = s.git("-C", W(s), "commit", "-m", "detached の作業")
		detachedSHA = s.git("-C", W(s), "rev-parse", "--short", "HEAD")
	}, steps: []step{
		{name: "出力の [detached HEAD SHA] はブランチ HEAD として記録する", do: clear,
			mk:   func(s *sandbox) call { return mark("git -C "+Wsh(s)+" commit -m x", out)(s) },
			want: all(wantMark("OUT"), wantRecord(func(s *sandbox) string { return "commit\t" + detachedSHA + "\tHEAD\t" + wtTop(s) }))},
		{name: "出力が無くても git に問うて HEAD になる", do: clear,
			mk:   func(s *sandbox) call { return mark("git -C "+Wsh(s)+" commit -m x", none)(s) },
			want: all(wantMark("OUT"), wantRecord(func(s *sandbox) string { return "commit\t" + detachedSHA + "\tHEAD\t" + wtTop(s) }))},
		{name: "差し戻しには detached HEAD と出る", mk: stop("ja"),
			want: all(wantStop("BLOCK"), wantBlockHas(func(*sandbox) string { return detachedSHA }, lit("detached HEAD"), wtTop))},
	}}.run(t)

	scenario{name: "古い形（時刻と commit の 1 語）と新しい形が混ざった記録も読める", setup: setup, steps: []step{
		{name: "両方の行を差し戻しに出す",
			do: func(s *sandbox) {
				clear(s)
				s.write(pend(s), "2026-09-21 02:00:00\tcommit\n2026-09-21 02:01:00\tcommit\t"+sha+"\tfeat\t"+wtTop(s)+"\n")
				s.touch(pend(s), newTime)
			},
			mk: stop("ja"),
			want: all(wantStop("BLOCK"),
				wantBlockHas(lit("2026-09-21 02:00:00 コミット\n"),
					func(*sandbox) string { return "2026-09-21 02:01:00 コミット " + sha + "（ブランチ feat" }))},
	}}.run(t)
}

// TestHandoffEventCommitForms は handoffEvent が古い形と新しい形の commit の行を、両方の言語で訳すことを見る。
func TestHandoffEventCommitForms(t *testing.T) {
	cases := []struct {
		lang   i18n.Lang
		fields []string
		want   string
	}{
		{i18n.JA, []string{"commit"}, "コミット"},
		{i18n.EN, []string{"commit"}, "commit"},
		{i18n.JA, []string{"commit", "abc1234", "feat", "/w/t"}, "コミット abc1234（ブランチ feat・作業ツリー /w/t）"},
		{i18n.EN, []string{"commit", "abc1234", "feat", "/w/t"}, "commit abc1234 (branch feat, worktree /w/t)"},
		{i18n.JA, []string{"commit", "abc1234", "HEAD", "/w/t"}, "コミット abc1234（detached HEAD・作業ツリー /w/t）"},
		{i18n.EN, []string{"commit", "abc1234", "HEAD", "/w/t"}, "commit abc1234 (detached HEAD, worktree /w/t)"},
		{i18n.JA, []string{"commit", "?", "?", "?"}, "コミット ?（ブランチ ?・作業ツリー ?）"},
	}
	for _, c := range cases {
		if got := handoffEvent(c.lang, c.fields); got != c.want {
			t.Errorf("handoffEvent(%s, %q) = %q, want %q", c.lang, c.fields, got, c.want)
		}
	}
}

// TestCommitFieldsWithoutGit は git が起動できない（無い・時間切れ）ときも欄を ? で埋めて返すことを見る。
// 記録そのものを落とすと、本当の完了の引き継ぎが要求されなくなる。
func TestCommitFieldsWithoutGit(t *testing.T) {
	calls := 0
	e := (&Env{Getenv: func(string) string { return "" }, Getwd: func() string { return t.TempDir() },
		Run: func(context.Context, Command) ([]byte, int, error) {
			calls++
			return nil, -1, errors.New("git が無い")
		}}).withDefaults()
	got := commitFields(context.Background(), e, "/somewhere", "")
	if strings.Join(got, "\t") != "?\t?\t?" {
		t.Errorf("git が無いとき: %q", got)
	}
	if calls == 0 { // 対照: 実際に git を呼ぼうとして失敗した経路を通っていること
		t.Error("前提が崩れている: git を呼んでいない")
	}
	got = commitFields(context.Background(), e, "/somewhere", "[main (root-commit) 0123abcd] x\n 1 file changed")
	if strings.Join(got, "\t") != "0123abcd\tmain\t?" {
		t.Errorf("出力から取れる欄は埋める: %q", got)
	}
}
