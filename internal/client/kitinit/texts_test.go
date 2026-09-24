package kitinit

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mdLines は ws の md のうち、sub を含む行を「相対パス: 行」で返す。
func mdLines(t *testing.T, ws, sub string) (hits []string, files int) {
	t.Helper()
	filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		files++
		rel, _ := filepath.Rel(ws, p)
		b, _ := os.ReadFile(p)
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, sub) {
				hits = append(hits, filepath.ToSlash(rel)+": "+l)
			}
		}
		return nil
	})
	return hits, files
}

// TestCLINotationPerInstall は、CLAUDE.md・AGENTS.md・skill・rules の CLI の表記がすべて looptrack issue になることを
// AI 4 種 × loop の有無で確かめる。
func TestCLINotationPerInstall(t *testing.T) {
	for _, agent := range goldenAgents {
		for _, loop := range []bool{false, true} {
			if agent == "other" && loop {
				continue // --loop は other に入れない（エラー。golden の other/loop が確かめる）
			}
			name := agent
			if loop {
				name += "/loop"
			}
			t.Run(name, func(t *testing.T) {
				stubs(t, true)
				root := newRoot(t)
				ws := filepath.Join(root, "ws")
				args := []string{"--project", "demo", "--agent", agent}
				if loop {
					args = append(args, "--loop")
				}
				if r := runInit(t, root, "", args...); r.code != 0 {
					t.Fatalf("init %v: %d %s", args, r.code, r.stderr)
				}
				goHits, files := mdLines(t, ws, "`"+GoCLI)
				if agent == "other" {
					if files != 0 {
						t.Errorf("--agent other は md を書かない: %d 件", files)
					}
					return
				}
				if len(goHits) == 0 {
					t.Errorf("looptrack issue の表記がありません")
				}
			})
		}
	}
}

// oldNames は、同梱の kit に残っていてはいけない以前の名前（1.0.0 より前の CLI・改名前の Go 版のもの）。
var oldNames = []string{"bash .claude/scripts/im-loop/", "IM_API_URL", "IM_PROJECT", ".claude/.im-work/", ".claude/rules/im-loop"}

// TestKitUsesLooptrackCLI は、同梱の kit の本文が looptrack の呼び方だけで書かれていることを確かめる
// （init は kit の本文をそのまま置くので、ここに旧名が入ると導入先にそのまま出る）。
func TestKitUsesLooptrackCLI(t *testing.T) {
	n := 0
	for name, body := range embedded() {
		n++
		for _, old := range oldNames {
			if strings.Contains(body, old) {
				t.Errorf("%s: 以前の名前 %q が残っています", name, old)
			}
		}
	}
	if n == 0 {
		t.Fatal("kit のファイルがありません")
	}
}

// snippetDoc は docs/templates/CLAUDE-snippet.md（init が CLAUDE.md に入れる節の写し）。
const snippetDoc = "../../../docs/templates/CLAUDE-snippet.md"

const snippetDocHead = `<!-- プロジェクト側の CLAUDE.md に入る節（<slug> はプロジェクト名、<URL> はサーバの URL）。looptrack issue init が
     <!-- looptrack:begin … --> / <!-- looptrack:end --> で囲んで入れる。正本は internal/client/kitinit/texts.go の claudeSnippet。
     この写しと一致することを internal/client/kitinit の TestClaudeSnippetDoc が確かめる（-update で書き直す）。手で貼らない。 -->

`

// TestClaudeSnippetDoc は文書の写しが init の入れる節と同じことを確かめる。
func TestClaudeSnippetDoc(t *testing.T) {
	want := snippetDocHead + fill(claudeSnippet, "<slug>", "<URL>")
	if *update {
		if err := os.WriteFile(snippetDoc, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(snippetDoc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s が init の節と違います（go test ./internal/client/kitinit -run TestClaudeSnippetDoc -update で書き直す）:%s", snippetDoc, firstDiff(want, string(got)))
	}
}
