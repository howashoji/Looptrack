package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
)

// TestParityMutations は new / comment / status / close が書くファイルを以前の CLI（記録）と比較する。
// 時刻は以前の CLI が書いた updated（分単位）を読み取り、Go 側にも同じ値を与える。
func TestParityMutations(t *testing.T) {
	_, projects := loadProjects(t)
	p := projects[0]
	dir := sandbox(t, p.dir)
	issues := filepath.Join(dir, ".claude", "issues")

	cfg := readConfig(t, issues)
	counter := readCounter(t, issues)
	id := FormatID(cfg.prefix, cfg.width, counter+1)

	body := "本文 `code` と 🔴\n\n- 箇条書き"
	out := goldenRun(t, "new", "比較用: タイトル / 記号*?", "--type", "bug", "--priority", "P1",
		"--labels", "a,b c", "--blocked-by", p.set.Items[0].ID, "--traces", p.set.Items[0].ID, "--refs", "FR-X-001", "--body", body)
	if !strings.Contains(out, id) {
		t.Fatalf("new の出力に %s が無い: %s", id, out)
	}
	path := filepath.Join(issues, "open", goldenValue(t, "new name"))
	if got, want := filepath.Base(path), FileName(id, "比較用: タイトル / 記号*?"); got != want {
		t.Errorf("ファイル名: got=%s want=%s", got, want)
	}
	rec := goldenFile(t, "new file", path)
	now := updatedOf(t, rec)
	doc := NewDocument(id, NewIssueInput{Title: "比較用: タイトル / 記号*?", Type: "bug", Status: "Todo", Priority: "P1",
		Labels: SplitList("a,b c"), BlockedBy: []string{p.set.Items[0].ID}, Traces: []string{p.set.Items[0].ID}, Refs: []string{"FR-X-001"}, Body: body}, now, i18n.JA)
	compare(t, "new", rec, mdformat.Render(doc))

	goldenRun(t, "comment", id, "原因: 1 行目\n2 行目")
	rec = goldenFile(t, "comment file", path)
	AppendComment(doc, updatedOf(t, rec), "原因: 1 行目\n2 行目")
	compare(t, "comment", rec, mdformat.Render(doc))

	goldenRun(t, "status", id, "In Progress", "--comment", "着手")
	rec = goldenFile(t, "status file", path)
	ts := updatedOf(t, rec)
	SetStatus(doc, "In Progress", ts)
	AppendComment(doc, ts, "着手")
	compare(t, "status --comment", rec, mdformat.Render(doc))

	goldenRun(t, "close", id, "--comment", "検証済み")
	rec = goldenValue(t, "close file")
	ts = updatedOf(t, rec)
	SetStatus(doc, "Done", ts)
	AppendComment(doc, ts, "検証済み")
	compare(t, "close --comment", rec, mdformat.Render(doc))

	// --body に受け入れ条件の見出しがあれば、テンプレートの受け入れ条件節を付けない
	for i, body := range []string{
		"要件の説明\n\n## 受け入れ条件\n\n- [ ] 単体テスト PASS",
		"要件の説明\n\n##  受け入れ条件 \n- [ ] 行末の空白",
		"見出しではない: ## 受け入れ条件 - [ ] 1 行",
	} {
		id := FormatID(cfg.prefix, cfg.width, counter+2+i)
		title := fmt.Sprintf("受け入れ条件つき %d", i)
		goldenRun(t, "new", title, "--body", body)
		rec := goldenValue(t, "file "+title)
		doc := NewDocument(id, NewIssueInput{Title: title, Type: "task", Status: "Todo", Priority: "P2",
			Labels: []string{}, BlockedBy: []string{}, Traces: []string{}, Refs: []string{}, Body: body}, updatedOf(t, rec), i18n.JA)
		compare(t, "new --body "+title, rec, mdformat.Render(doc))
		if got := len(regexp.MustCompile(`(?m)^##[ \t]+受け入れ条件[ \t]*$`).FindAllString(rec, -1)); got != 1 {
			t.Errorf("%s: 受け入れ条件の見出しが %d 個", title, got)
		}
		if tmpl := strings.Contains(rec, "（テスト可能な形で書く。曖昧語を使わない）"); tmpl != (i == 2) {
			t.Errorf("%s: テンプレートの受け入れ条件節の有無=%v", title, tmpl)
		}
	}
}

type config struct {
	prefix string
	width  int
}

func readConfig(t *testing.T, issues string) config {
	raw := read(t, filepath.Join(issues, "config.json"))
	prefix := regexp.MustCompile(`"prefix":\s*"([^"]+)"`).FindStringSubmatch(raw)
	width := regexp.MustCompile(`"width":\s*(\d+)`).FindStringSubmatch(raw)
	if prefix == nil || width == nil {
		t.Fatalf("config.json を読めない: %s", raw)
	}
	w := 0
	for _, c := range width[1] {
		w = w*10 + int(c-'0')
	}
	return config{prefix[1], w}
}

func readCounter(t *testing.T, issues string) int {
	n := 0
	for _, c := range strings.TrimSpace(read(t, filepath.Join(issues, "counter"))) {
		n = n*10 + int(c-'0')
	}
	return n
}

func onlyFile(t *testing.T, pattern string) string {
	m, _ := filepath.Glob(pattern)
	if len(m) != 1 {
		t.Fatalf("%s に一致するファイルが %d 件", pattern, len(m))
	}
	return m[0]
}

// goldenFile は以前の CLI が書いたファイルの中身（記録）。
func goldenFile(t *testing.T, key, _ string) string {
	t.Helper()
	return goldenValue(t, key)
}

func read(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var updatedLine = regexp.MustCompile(`(?m)^updated: (.*)$`)

func updatedOf(t *testing.T, text string) string {
	m := updatedLine.FindStringSubmatch(text)
	if m == nil {
		t.Fatal("updated が無い")
	}
	return m[1]
}

// compare は created（new の時刻）を記録に合わせてから比較する。
func compare(t *testing.T, step, rec, got string) {
	t.Helper()
	created := regexp.MustCompile(`(?m)^created: .*$`)
	if m := created.FindString(rec); m != "" {
		got = created.ReplaceAllString(got, m)
	}
	if rec != got {
		t.Errorf("%s の結果が一致しません:%s", step, firstDiff(rec, got))
	}
}
