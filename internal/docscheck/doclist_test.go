package docscheck

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// doc.go のパッケージコメントの一覧と、このディレクトリに実在する *_test.go が一致することを確かめる。
//
// なぜ要るか: doc.go の一覧は「このパッケージにどんな検査があるか」を次に触る人へ伝える唯一の場所だが、
// ただのコメントなので、実ファイルとずれても何も落ちない。実際、新しい検査を足したセッションが
// 一覧のことを思い出したのは、どちらも実装が終わったあと（レビューの段階と、差分を見せ合った場面）だった。
// どちらも人が気づいて直したが、次の人が気づく保証はないので、機械で落とす。
//
// # 一覧をどこまで厳密に拾うか
//
// コメントの本文は go/parser に取らせる（"//" か "/* */" か、行頭の空白がいくつかを自分で数えない）。
// そのうえで、本文の各行を「  - <名前>: <説明>」の箇条書きとして読み、**名前が *_test.go で終わるものだけ**を
// 一覧の項目として扱う。
//
//   - 「検査 1 つ = テストファイル 1 つ」なので、一覧に載るべきものは *_test.go に限られる。
//     名前の形で絞ると、箇条書きの説明文に出てくる他のファイル名（例: deploy/public-scan.sh）を拾わない。
//   - 続きの行（"- " で始まらない字下げされた行）は項目として拾わない。既存の一覧にその形の行がある。
//   - 拾えた項目が 0 件なら Fatal にする（witness）。コメントの書式が変わって解析が空振りしたとき、
//     「一覧が空 = 差分も空」で黙って緑になるのを塞ぐため。
func TestDocCommentListsEveryTestFile(t *testing.T) {
	listed := parseDocList(t)
	// witness: 解析が空振りしたら、突き合わせる前に落とす。
	if len(listed) == 0 {
		t.Fatal("doc.go のパッケージコメントから検査の一覧を 1 件も拾えません" +
			"（箇条書きの書式「  - <ファイル名>_test.go: <説明>」が変わっていないか確かめてください）")
	}

	actual := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_test.go") {
			actual[e.Name()] = true
		}
	}
	if len(actual) == 0 {
		t.Fatal("internal/docscheck に *_test.go が 1 つもありません（走査が壊れています）")
	}

	for _, name := range sortedKeys(listed) {
		if !actual[name] {
			t.Errorf("doc.go の一覧にある %s が internal/docscheck に実在しません"+
				"（検査を消したなら doc.go の行も消してください）", name)
		}
	}
	for _, name := range sortedKeys(actual) {
		if !listed[name] {
			t.Errorf("internal/docscheck の %s が doc.go の一覧にありません"+
				"（doc.go のパッケージコメントに「  - %s: <何を確かめるか>」の行を足してください）", name, name)
		}
	}
}

// docListItem は doc.go のパッケージコメントの箇条書き「- <名前>: <説明>」の名前を取る。
// 名前に許すのは、ファイル名に実際に使う文字だけ（説明文の途中の読点や括弧で止まるようにする）。
//
// 先頭の `\s*` は、項目の下にさらに字下げして書いた入れ子の箇条書き（例:
// "//     - baz_test.go: …"）も同じ項目として拾ってしまう。いまの一覧に入れ子は無いので実害はないが、
// 誰かが入れ子で書くと実在しないファイル名として FAIL する（偽陽性）。絞るなら字下げの深さに上限を付ける。
var docListItem = regexp.MustCompile(`^\s*-\s+([A-Za-z0-9_.-]+):`)

// parseDocList は doc.go のパッケージコメントから、一覧に載っている *_test.go の名前を返す。
func parseDocList(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(".", "doc.go"), nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if f.Doc == nil {
		t.Fatal("doc.go にパッケージコメントがありません")
	}
	out := map[string]bool{}
	for _, line := range strings.Split(f.Doc.Text(), "\n") {
		m := docListItem.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.HasSuffix(m[1], "_test.go") {
			out[m[1]] = true
		}
	}
	return out
}

// sortedKeys は、失敗の出力が並び順で揺れないように名前を整列して返す。
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
