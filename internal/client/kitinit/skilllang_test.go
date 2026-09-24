package kitinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/kit"
)

// skill は AI のハーネスが .claude/skills/<名前>/SKILL.md という固定のパスで読むので、init は導入時の言語の
// 本文 1 本だけをそこに置き、en/SKILL.md は置かない。

var skillNames = []string{"issue", "token-report", "iterate", "session-handoff"}

func kitSkillPath(name string) string {
	if name == "iterate" || name == "session-handoff" {
		return "kit/loop/skills/" + name + "/SKILL.md"
	}
	return "kit/core/skills/" + name + "/SKILL.md"
}

func kitBody(t *testing.T, name string) string {
	t.Helper()
	b, err := kit.ReadFile(name)
	if err != nil {
		t.Fatalf("kit に %s が無い（前提が崩れている）: %v", name, err)
	}
	return string(b)
}

func initLang(t *testing.T, root, lang string, args ...string) result {
	t.Helper()
	stubs(t, true)
	r := runInitIn(t, root, "", map[string]string{"LOOPTRACK_LANG": lang},
		append([]string{"--url", fakeURL, "--project", "demo", "--agent", "claude-code"}, args...)...)
	if r.code != 0 {
		t.Fatalf("init（%s）が失敗: %d\n%s%s", lang, r.code, r.stdout, r.stderr)
	}
	return r
}

func skillDest(root, name string) string {
	return filepath.Join(root, "ws", ".claude", "skills", name, "SKILL.md")
}

func trDest(root, name string) string {
	return filepath.Join(root, "ws", ".claude", "skills", name, kit.LangDir, "SKILL.md")
}

// 英語で導入すると、正本の位置に英訳の本文（description も英語）が置かれ、en/SKILL.md は置かれない。
// 対照として、日本語で導入したときは正本の日本語が置かれる（同じテストの中で、言語で結果が変わることを確かめる）。
func TestSkillPlacedInInstallLanguage(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			root := newRoot(t)
			initLang(t, root, lang, "--loop")
			for _, n := range skillNames {
				src := kitSkillPath(n)
				want := kitBody(t, src)
				if lang == "en" {
					want = kitBody(t, kit.TranslatedName(src))
				}
				got := read(t, skillDest(root, n))
				if got != want {
					t.Errorf("%s: 置かれた SKILL.md が %s の本文と違う", n, lang)
				}
				if _, err := os.Lstat(trDest(root, n)); !os.IsNotExist(err) {
					t.Errorf("%s: en/SKILL.md が置かれている（%v）", n, err)
				}
			}
			desc := frontmatterValue(read(t, skillDest(root, "issue")), "description")
			wantDesc := frontmatterValue(kitBody(t, "kit/core/skills/issue/SKILL.md"), "description")
			if lang == "en" {
				wantDesc = frontmatterValue(kitBody(t, "kit/core/skills/issue/en/SKILL.md"), "description")
				if !strings.HasPrefix(desc, "Create, start (next), comment on, close and edit issues on the issue management server.") {
					t.Errorf("英語の環境で description が英語でない: %q", desc)
				}
			}
			if desc != wantDesc {
				t.Errorf("description = %q, want %q", desc, wantDesc)
			}
		})
	}
}

// 以前の init が置いた en/SKILL.md（init の置いたまま）は、次の init で片付く。手で変えたもの（core は印の無いもの、
// loop は記録のハッシュと違うもの）は残す。
func TestSkillTranslationCleanedOnReinit(t *testing.T) {
	root := newRoot(t)
	initLang(t, root, "ja", "--loop")
	// 以前の init の形を再現する: kit の en/SKILL.md をそのまま置く
	for _, n := range skillNames {
		write(t, trDest(root, n), kitBody(t, kit.TranslatedName(kitSkillPath(n))))
	}
	// 手で変えたもの（残る側の対照）: core は印を外す・loop は本文を変える
	handCore := "---\nname: token-report\n---\n# by hand\n"
	write(t, trDest(root, "token-report"), handCore)
	handLoop := kitBody(t, "kit/loop/skills/iterate/en/SKILL.md") + "\nlocal note\n"
	write(t, trDest(root, "iterate"), handLoop)

	initLang(t, root, "ja", "--loop")
	for _, n := range []string{"issue", "session-handoff"} {
		if _, err := os.Lstat(trDest(root, n)); !os.IsNotExist(err) {
			t.Errorf("%s: 以前の init の en/SKILL.md が残っている（%v）", n, err)
		}
		if _, err := os.Lstat(filepath.Dir(trDest(root, n))); !os.IsNotExist(err) {
			t.Errorf("%s: 空の en/ が残っている（%v）", n, err)
		}
	}
	if got := read(t, trDest(root, "token-report")); got != handCore {
		t.Errorf("手で置いた core の en/SKILL.md が変わった: %q", got)
	}
	if got := read(t, trDest(root, "iterate")); got != handLoop {
		t.Errorf("手で変えた loop の en/SKILL.md が変わった: %q", got)
	}
}

// 言語を切り替えて打ち直すと、正本の位置の本文が新しい言語に入れ替わる（前の言語の本文を「手で変えたもの」と取り違えない）。
func TestSkillLanguageSwitchOnReinit(t *testing.T) {
	root := newRoot(t)
	initLang(t, root, "ja", "--loop")
	initLang(t, root, "en", "--loop")
	for _, n := range skillNames {
		want := kitBody(t, kit.TranslatedName(kitSkillPath(n)))
		if got := read(t, skillDest(root, n)); got != want {
			t.Errorf("%s: ja → en の打ち直しで英語に入れ替わらない", n)
		}
	}
	initLang(t, root, "ja", "--loop")
	for _, n := range skillNames {
		want := kitBody(t, kitSkillPath(n))
		if got := read(t, skillDest(root, n)); got != want {
			t.Errorf("%s: en → ja の打ち直しで日本語に戻らない", n)
		}
	}
}

// --remove-loop は、英語で置いた loop の skill（正本の位置）も、以前の init が置いた en/SKILL.md も消す。
func TestRemoveLoopClearsSkillsInEitherLanguage(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			root := newRoot(t)
			initLang(t, root, lang, "--loop")
			// 以前の init の残骸（kit の en/SKILL.md そのまま）
			write(t, trDest(root, "session-handoff"), kitBody(t, "kit/loop/skills/session-handoff/en/SKILL.md"))
			stubs(t, true)
			r := runInitIn(t, root, "", map[string]string{"LOOPTRACK_LANG": lang}, "--url", fakeURL, "--remove-loop")
			if r.code != 0 {
				t.Fatalf("--remove-loop が失敗: %d\n%s%s", r.code, r.stdout, r.stderr)
			}
			for _, n := range []string{"iterate", "session-handoff"} {
				if _, err := os.Lstat(skillDest(root, n)); !os.IsNotExist(err) {
					t.Errorf("%s: --remove-loop の後に SKILL.md が残っている（%v）\n%s", n, err, r.stdout)
				}
				if _, err := os.Lstat(trDest(root, n)); !os.IsNotExist(err) {
					t.Errorf("%s: --remove-loop の後に en/SKILL.md が残っている（%v）", n, err)
				}
			}
			// core の skill は --remove-loop の対象ではない（対照）
			if _, err := os.Lstat(skillDest(root, "issue")); err != nil {
				t.Errorf("core の skill issue が消えた: %v", err)
			}
		})
	}
}
