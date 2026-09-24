package docscheck

import (
	"path/filepath"
	"slices"
	"testing"
)

// README（英語の README.md と日本語の README.ja.md）の構成がそろっているかを確かめる。
// 規則は利用者ガイド（guide_test.go）と同じで、見出しの階層の並び・コードブロックの数・表の数を比べる。
// 片方だけを直すと失敗するので、日英の 2 本立てが黙って崩れない。

const repoRoot = "../.."

func TestReadmeStructure(t *testing.T) {
	en := shapeOf(read(t, filepath.Join(repoRoot, "README.md")))
	ja := shapeOf(read(t, filepath.Join(repoRoot, "README.ja.md")))
	if len(en.headings) == 0 {
		t.Fatal("README.md に見出しがありません")
	}
	if !slices.Equal(en.headings, ja.headings) {
		t.Errorf("見出しの並びが違います（英 %v / 日 %v）", en.headings, ja.headings)
	}
	if en.fences != ja.fences {
		t.Errorf("コードブロックの数が違います（英 %d / 日 %d）", en.fences, ja.fences)
	}
	if en.tables != ja.tables {
		t.Errorf("表の数が違います（英 %d / 日 %d）", en.tables, ja.tables)
	}
}
