package transfer_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/testdata"
)

// フィクスチャ（internal/testdata/fixtures）自体の検査。go test ./... は testdata という名前のディレクトリを
// パッケージとして対象にしないため、internal/testdata ではなくここに置く。
// フィクスチャは以前の生成器で作ったものをそのまま置いている（生成の仕掛けは消した）。
// 直すと internal/domain の以前の CLI との比較の記録（testdata/golden.json）と合わなくなる。

// tree は root 以下のファイル（相対パス → 内容）。
func tree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		b, err := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 公開してはいけない社内の名前・ID・URL（フィクスチャは架空の内容だけにする）。
// 語は分けて書く（このファイル自身が公開物の検査（deploy/public-scan.sh）に掛からないように）
var forbidden = regexp.MustCompile(`(?i)` + "how" + "ashoji|宝" + "和|153\\.126|dev" + "new|req" + "weave|h" + "pc|dev" + "-infra|iss" + "ui" + `|\bIM-\d|\bREQ-\d|\bINFRA-\d`)

func TestFixturesHaveNoInternalNames(t *testing.T) {
	root := testdata.Root(t)
	for rel, b := range tree(t, root) {
		if m := forbidden.Find([]byte(rel)); m != nil {
			t.Errorf("%s: ファイル名に %q", rel, m)
		}
		if m := forbidden.Find(b); m != nil {
			t.Errorf("%s: 内容に %q", rel, m)
		}
	}
}

// pseudoHeading はコメントの中の疑似見出し（日付だけの ### と ## の見出し）。
var pseudoHeading = regexp.MustCompile(`(?m)^(### \d{4}-\d{2}-\d{2}|## \S)`)

var lowerID = regexp.MustCompile(`\b[a-z]+-\d`)

// TestFixturesCoverage は、テストが頼りにしている形がフィクスチャに入っていることを確かめる。
// フィクスチャを直して形が消えると、比較テストが黙って確かめる範囲を狭めるため。
func TestFixturesCoverage(t *testing.T) {
	root := testdata.Root(t)
	files, err := mdformat.CollectIssueFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	perProject := map[string]int{}
	var stats mdformat.Stats
	statuses, types := map[string]bool{}, map[string]bool{}
	var parent, blocked, traces, refs, spacedRefs, spacedLabels, lowerTrace, longBody, pseudo, zeroComments, sameMinute, image, html, fence, tab, zenkaku int
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := mdformat.Parse(string(raw))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		stats.Add(doc)
		rel, _ := filepath.Rel(root, path)
		perProject[strings.Split(filepath.ToSlash(rel), "/")[0]]++
		it := domain.FromDocument(doc)
		statuses[it.Status], types[it.Type] = true, true
		count := func(c *int, ok bool) {
			if ok {
				*c++
			}
		}
		count(&parent, it.Parent != "")
		count(&blocked, len(it.BlockedBy) > 0)
		count(&traces, len(it.Traces) > 0)
		count(&refs, len(it.Refs) > 0)
		count(&spacedRefs, len(it.Refs) > 0 && strings.Contains(strings.Join(it.Refs, ","), " "))
		count(&spacedLabels, strings.Contains(strings.Join(it.Labels, ","), " "))
		count(&lowerTrace, lowerID.MatchString(strings.Join(append(append([]string{}, it.Traces...), it.BlockedBy...), ",")))
		count(&longBody, len(raw) > 65535)
		for i, c := range doc.Comments {
			count(&pseudo, pseudoHeading.MatchString(c.Content))
			count(&sameMinute, i > 0 && doc.Comments[i-1].TS == c.TS)
		}
		count(&zeroComments, doc.HasCommentSection && len(doc.Comments) == 0)
		count(&image, strings.Contains(string(raw), "!["))
		count(&html, strings.Contains(string(raw), "<details>"))
		count(&fence, strings.Contains(string(raw), "```"))
		count(&tab, strings.Contains(string(raw), "\t"))
		count(&zenkaku, strings.Contains(it.Title, "　"))
	}
	t.Logf("files=%d %v %s", len(files), perProject, stats)
	for _, slug := range testdata.Slugs {
		if n := perProject[slug]; n < 20 || n > 50 {
			t.Errorf("%s: %d 件（各プロジェクト 20〜50 件）", slug, n)
		}
	}
	for _, s := range domain.Statuses {
		if !statuses[s] {
			t.Errorf("状態 %s のイシューが無い", s)
		}
	}
	for _, ty := range domain.Types {
		if !types[ty] {
			t.Errorf("型 %s のイシューが無い", ty)
		}
	}
	for name, n := range map[string]int{
		"parent": parent, "blocked_by": blocked, "traces": traces, "refs": refs, "空白区切りの refs": spacedRefs,
		"空白を含むラベル": spacedLabels, "小文字の ID を指す traces / blocked_by": lowerTrace, "64 KiB 超のファイル": longBody,
		"添付の無い画像参照": image, "HTML タグ": html, "コードブロック": fence, "タブ": tab, "全角空白のタイトル": zenkaku,
		"コメントの中の疑似見出し": pseudo, "コメント 0 件のコメント節": zeroComments, "同じ分のコメントの連続": sameMinute, "コメント": stats.Comments, "空のコメント": stats.EmptyComments, "コメント節の前置き": stats.Preambles,
		"コメント節の無いファイル": stats.NoCommentSection, "コメント節の前の空行 2 行": stats.GapNL[3], "末尾の改行 2 つ": stats.TrailNL[2],
		"未知のキー origin": stats.Keys["origin"], "refs キーの無いファイル": stats.Files - stats.Keys["refs"],
	} {
		if n == 0 {
			t.Errorf("フィクスチャに %s が無い", name)
		}
	}
}
