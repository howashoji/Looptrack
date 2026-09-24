package docscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// unbracedBeforeNonASCII は、波かっこの無い変数展開の直後に ASCII 以外の文字が続く形（`"$rev（…）"`）。
//
// bash は変数名の続きかどうかを 1 バイトずつ isalnum で決める。macOS の libc は UTF-8 のロケールで
// 0x80〜0xFF を Latin-1 の文字として分類する（0xEF は `ï` なので英字）。そのため `（`（EF BC 88）の
// 先頭のバイトが名前に食われ、`$rev（` は `rev\xEF` という別の変数になる。`set -u` なら
// 「unbound variable」で止まり、`set -u` が無ければ黙って空に展開される。Linux の glibc は 0x80 以上を
// 英字に数えないので、Linux だけで回していると見つからない。`${rev}（` と書けば OS に依らない。
var unbracedBeforeNonASCII = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7F]`)

// TestShellVarsBracedBeforeNonASCII は、リポジトリのシェルスクリプトに上の形が無いことを見る。
func TestShellVarsBracedBeforeNonASCII(t *testing.T) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".sh") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(b), "\n") {
			if m := unbracedBeforeNonASCII.FindString(line); m != "" {
				t.Errorf("%s:%d: %q の変数名に続く文字が macOS の bash では名前の一部として読まれます。${…} で囲んでください: %s",
					filepath.ToSlash(rel), i+1, m, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 { // 対照: 調べる対象が見つかっていること（場所を取り違えると何も見ずに通る）
		t.Fatalf("前提が崩れています。%s の下に *.sh が 1 つも見つかりません", root)
	}
	// 対照: この検査の式が、実際に落ちた形に当たること
	if !unbracedBeforeNonASCII.MatchString(`scanned="$rev（$(git rev-parse --short "$rev")）だけ"`) {
		t.Error("前提が崩れています。検査の式が `$rev（` に当たりません")
	}
	if unbracedBeforeNonASCII.MatchString(`scanned="${rev}（x）・$1（y）"`) {
		t.Error("波かっこで囲んだ形と位置パラメータに当たっています")
	}
}
