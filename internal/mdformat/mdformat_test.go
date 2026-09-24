package mdformat

import (
	"errors"
	"strings"
	"testing"
)

const fm = "---\nid: IM-0001\ntitle: テスト\nlabels: [a, b c]\nparent: \nrefs: []\n---\n\n"

func TestRoundTripCases(t *testing.T) {
	cases := map[string]string{
		"標準（コメント 1 件）":     fm + "# IM-0001 テスト\n\n## 背景\n\n本文\n\n## コメント\n\n### 2026-09-17 12:00\n\nコメント\n",
		"コメント 0 件":         fm + "# IM-0001 テスト\n\n## コメント\n",
		"空のコメントが連続":        fm + "# x\n\n## コメント\n\n### 2026-09-01 11:30\n\n### 2026-09-01 11:30\n\n本文\n",
		"コメント節の前が空行 2 行":   fm + "# x\n\n\n## コメント\n\n### 2026-09-17 12:00\n\nc\n",
		"末尾が改行 2 つ":        fm + "# x\n\n## コメント\n\n### 2026-09-17 12:00\n\nc\n\n",
		"preamble あり":      fm + "# x\n\n## コメント\n\n- 移設メモ\n\n### 2026-09-17 12:00\n\nc\n",
		"日付のみの疑似コメントは本文扱い": fm + "# x\n\n## コメント\n\n### 2026-09-01\n\n疑似\n\n### 2026-09-17 12:00\n\nc\n",
		"コメント節が無い":         fm + "# x\n\n本文\n",
		"4 バイト文字":          fm + "# x 🔴\n\n## コメント\n\n### 2026-09-17 12:00\n\n💩\n",
		"タブと行末空白":          fm + "# x\n\n\tタブ \n\n## コメント\n",
		"frontmatter 後の空行が 2 行（本文先頭の改行として保持）": "---\nid: IM-0001\n---\n\n\n# x\n",
		"frontmatter キー無し参照":                  "---\nid: IM-0002\norigin: OLD-1\n---\n\n# x\n\n## コメント\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(text); err != nil {
				t.Fatalf("Parse: %v", err)
			}
		})
	}
}

func TestCodeFenceHeadingsAreNotSplit(t *testing.T) {
	text := fm + "# x\n\n```\n## コメント\n### 2026-09-17 12:00\n```\n\n## コメント\n\n### 2026-09-17 12:01\n\n```md\n### 2026-09-17 12:02\n```\n"
	doc, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.BodyMain, "```\n## コメント") {
		t.Errorf("コードブロック内の見出しでコメント節を始めてしまった: %q", doc.BodyMain)
	}
	if len(doc.Comments) != 1 {
		t.Errorf("コメント件数 = %d, want 1（コードブロック内の時刻見出しを数えない）", len(doc.Comments))
	}
}

func TestSplitValues(t *testing.T) {
	doc, err := Parse(fm + "# x\n\n本文\n\n## コメント\n\n- 前置き\n\n### 2026-09-17 12:00\n\n### 2026-09-17 12:00\n\nb\n")
	if err != nil {
		t.Fatal(err)
	}
	if l := doc.Field("labels"); l == nil || !l.IsList || len(l.List) != 2 || l.List[1] != "b c" {
		t.Errorf("labels = %+v（空白を含む値を分割しない）", l)
	}
	if p := doc.Field("parent"); p == nil || p.IsList || p.Value != "" {
		t.Errorf("parent = %+v（空文字のスカラー）", p)
	}
	if r := doc.Field("refs"); r == nil || !r.IsList || len(r.List) != 0 {
		t.Errorf("refs = %+v（空リスト）", r)
	}
	if doc.BodyMain != "# x\n\n本文" || doc.GapNL != 2 || doc.TrailNL != 1 {
		t.Errorf("BodyMain=%q GapNL=%d TrailNL=%d", doc.BodyMain, doc.GapNL, doc.TrailNL)
	}
	if doc.Preamble == nil || *doc.Preamble != "- 前置き" {
		t.Errorf("Preamble = %v", doc.Preamble)
	}
	if len(doc.Comments) != 2 || doc.Comments[0].Content != "" || doc.Comments[1].Content != "b" {
		t.Errorf("Comments = %+v", doc.Comments)
	}
}

func TestNonCanonicalIsRejected(t *testing.T) {
	cases := map[string]string{
		"コロンの後に空白が無い": "---\nid:IM-0001\n---\n\n# x\n",
		"リストの区切りが不揃い": "---\nlabels: [a,b]\n---\n\n# x\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(text)
			var rt *RoundTripError
			if err == nil || !(errors.As(err, &rt) || errors.Is(err, ErrFormat)) {
				t.Fatalf("err = %v, want RoundTripError または ErrFormat（黙って正規化しない）", err)
			}
		})
	}
}

func TestFormatErrors(t *testing.T) {
	for name, text := range map[string]string{
		"frontmatter 無し":   "# x\n",
		"frontmatter 閉じ無し": "---\nid: x\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(text); !errors.Is(err, ErrFormat) {
				t.Fatalf("err = %v, want ErrFormat", err)
			}
		})
	}
}
