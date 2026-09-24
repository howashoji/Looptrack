package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/mdformat"

	"github.com/howashoji/looptrack/internal/testdata"
)

// 以前の CLI（1.0.0 より前・ファイルモード）を合成フィクスチャ（のコピー）に対して実行した結果と、Go の結果を
// 比較する。フィクスチャは internal/testdata/fixtures（実データは合成フィクスチャに置き換えてある）。
// 以前の CLI は撤去した。その前に記録した出力（testdata/golden.json。goldenValue）と比べる。

type project struct {
	slug string
	dir  string
	set  *Set
	docs []*mdformat.Document
}

func loadProjects(t *testing.T) (root string, projects []project) {
	t.Helper()
	root = testdata.Root(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(root, e.Name(), "config.json")); err != nil || !e.IsDir() || e.Name() == "scripts" {
			continue
		}
		p := project{slug: e.Name(), dir: filepath.Join(root, e.Name())}
		var items []Issue
		for _, sub := range []string{"open", "closed"} {
			files, _ := filepath.Glob(filepath.Join(p.dir, sub, "*.md"))
			for _, f := range files {
				raw, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				doc, err := mdformat.Parse(string(raw))
				if err != nil {
					t.Fatalf("%s: %v", f, err)
				}
				p.docs = append(p.docs, doc)
				items = append(items, FromDocument(doc))
			}
		}
		if len(items) == 0 {
			continue
		}
		p.set = NewSet(items)
		projects = append(projects, p)
	}
	if len(projects) != len(testdata.Slugs) {
		t.Fatalf("フィクスチャのプロジェクトが %d 件（want %d）", len(projects), len(testdata.Slugs))
	}
	return root, projects
}

// sandbox はプロジェクトディレクトリを一時ディレクトリへコピーし、CLAUDE_PROJECT_DIR として使える場所を返す
// （index が書き出すため、フィクスチャに触れない）。
func sandbox(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	issues := filepath.Join(dst, ".claude", "issues")
	if err := os.CopyFS(issues, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

// goldenRun は以前の CLI に args を渡したときの出力（記録）。
func goldenRun(t *testing.T, args ...string) string {
	t.Helper()
	return goldenValue(t, strings.Join(args, " "))
}

// idsFromTable は list / ready の出力から ID 列を取り出す。
func idsFromTable(out string) []string {
	var ids []string
	lines := strings.Split(out, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "ID ") {
		return ids
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			break
		}
		ids = append(ids, strings.Fields(l)[0])
	}
	return ids
}

func ids(items []Issue) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParityListAndReady(t *testing.T) {
	_, projects := loadProjects(t)
	for _, p := range projects {
		t.Run(p.slug, func(t *testing.T) {
			check := func(name string, want []string, got []Issue) {
				if !equal(want, ids(got)) {
					t.Errorf("%s: 件数 want=%d got=%d\nwant=%v\ngot=%v", name, len(want), len(got), head(want), head(ids(got)))
				}
			}
			for _, key := range SortKeys {
				for _, rev := range []bool{false, true} {
					args := []string{"list", "--all", "--sort", key}
					if rev {
						args = append(args, "--reverse")
					}
					check(strings.Join(args, " "), idsFromTable(goldenRun(t, args...)), p.set.List(Filter{All: true}, key, rev))
				}
			}
			check("list（既定）", idsFromTable(goldenRun(t, "list")), p.set.List(Filter{}, "priority", false))
			check("ready", idsFromTable(goldenRun(t, "ready")), p.set.Ready())
			check("ready --sort updated", idsFromTable(goldenRun(t, "ready", "--sort", "updated")), SortIssues(p.set.Ready(), "updated", false))
			for _, st := range Statuses {
				check("list --status "+st, idsFromTable(goldenRun(t, "list", "--status", st)), p.set.List(Filter{Status: st}, "priority", false))
			}
			check("list --type bug", idsFromTable(goldenRun(t, "list", "--type", "bug")), p.set.List(Filter{Type: "bug"}, "priority", false))
			if label, ref := firstValues(p.set); label != "" || ref != "" {
				if label != "" {
					check("list --all --label "+label, idsFromTable(goldenRun(t, "list", "--all", "--label", label)), p.set.List(Filter{All: true, Label: label}, "priority", false))
				}
				if ref != "" {
					lower := strings.ToLower(ref)
					check("list --all --ref "+lower, idsFromTable(goldenRun(t, "list", "--all", "--ref", lower)), p.set.List(Filter{All: true, Ref: lower}, "priority", false))
				}
			}
		})
	}
}

func firstValues(s *Set) (label, ref string) {
	for _, it := range s.Items {
		if label == "" && len(it.Labels) > 0 {
			label = it.Labels[0]
		}
		if ref == "" && len(it.Refs) > 0 {
			ref = it.Refs[0]
		}
	}
	return
}

func head(v []string) []string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}

var generatedAt = regexp.MustCompile(`(?m)^> 生成日時: .*$`)

// regenNote は記録（以前の CLI の出力・凍結）の「再生成」の案内を今の呼び方にそろえる。
// 以前の入口を廃止し、案内を looptrack issue にしたので、この 1 行だけは記録と違ってよい。
var regenNote = regexp.MustCompile("(?m)^(> \\*\\*自動生成\\*\\* — )`[^`]+ (index|matrix)`( で再生成。)")

func TestParityIndexAndMatrix(t *testing.T) {
	_, projects := loadProjects(t)
	for _, p := range projects {
		t.Run(p.slug, func(t *testing.T) {
			goldenRun(t, "index")
			for name, got := range map[string]string{
				"index.md":  p.set.BuildIndexMarkdown("X"),
				"matrix.md": p.set.BuildMatrixMarkdown("X"),
			} {
				// 生成日時は X にしてある
				want := regenNote.ReplaceAllString(goldenValue(t, "file "+name), "${1}`looptrack issue ${2}`${3}")
				if want != got {
					t.Errorf("%s が一致しません: %v", name, (&mdformat.RoundTripError{}).Error()+firstDiff(want, got))
				}
			}
		})
	}
}

func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := max(0, i-60)
	return "\nwant: " + a[lo:min(len(a), i+60)] + "\ngot:  " + b[lo:min(len(b), i+60)]
}

// TestParitySlugify はフィクスチャの全タイトル（と境界例）で以前の CLI の記録と比較する。
func TestParitySlugify(t *testing.T) {
	_, projects := loadProjects(t)
	titles := []string{"", "  ", "a / b", "---x---", "全角　スペース", "x:y*z?\"<>|w", strings.Repeat("あ", 50), "ab", "tab\there"}
	for _, p := range projects {
		for _, it := range p.set.Items {
			titles = append(titles, it.Title)
		}
	}
	in, _ := json.Marshal(titles)
	out := goldenValue(t, "slugify "+string(in))
	var want []string
	if err := json.Unmarshal([]byte(out), &want); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for i, title := range titles {
		if got := Slugify(title); got != want[i] {
			bad++
			if bad <= 10 {
				t.Errorf("Slugify(%q) = %q, want = %q", title, got, want[i])
			}
		}
	}
	t.Logf("titles=%d mismatches=%d", len(titles), bad)
}
