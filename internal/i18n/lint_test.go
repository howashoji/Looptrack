package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 受け入れ条件「訳の抜けを検出するテストがある」の 2 つ目と 3 つ目。
//
// ソースの中の i18n.T(…) が渡す ID を集めて、対訳表と突き合わせる:
//   - コードが使っているのに対訳表に無い ID があれば失敗（訳の抜け）
//   - 対訳表にあるのにコードのどこからも使われない ID があれば失敗（消し忘れ）
//   - ID が文字列リテラルでなければ失敗（組み立てられると抜けを機械で見つけられない）
//   - 構造体の jsonschema タグの値（MCP の入力項目の説明の ID）も、使っている ID として数える

const repoRoot = "../.."

// 走査の起点はリポジトリ全体にしてある（「見る場所の一覧」を持たない）。
// 一覧に挙げ忘れたパッケージが黙って検査の外に落ちるのを防ぐため
// （以前は cmd・internal だけを見ていて、deploy/embed.go・kit/embed.go・migrations/embed.go・
// notice.go が検査の外にあった）。除外の規則は jalint_test.go の走査と同じにする。

// scanSkipNames は、走査で入らないディレクトリの名前。
//   - 先頭が . のもの（.git・.claude/worktrees には別の作業ツリーの複製が入る）
//   - testdata（記録・合成データ。Go として読めないものが混ざる）
//   - vendor・node_modules（自分で書いたコードではない）
func scanSkipName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules"
}

// repoFiles は、リポジトリ全体から拡張子が suffix のファイルを集める（リポジトリからの相対パス・昇順）。
// skipDir は、名前の規則に加えて読み飛ばすディレクトリ（リポジトリからの相対パス）を選ぶ。
func repoFiles(t *testing.T, suffix string, skipDir func(rel string) bool) []string {
	t.Helper()
	root := mustAbs(t, repoRoot)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("走査の起点 %s: パスが存在しません（%v）", repoRoot, err)
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(mustRel(t, path))
		if d.IsDir() {
			if path == root {
				return nil
			}
			if scanSkipName(d.Name()) || (skipDir != nil && skipDir(rel)) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, suffix) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// goSourceFiles は走査の対象になる .go を集める。
// テストは検査用の対訳表に差し替えて使うので対象外（対訳表に無い ID が出てくるのが正常）。
// i18n 自身も対象外（T・M・Errorf の実装が互いを呼ぶため）。
func goSourceFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, rel := range repoFiles(t, ".go", func(rel string) bool { return rel == "internal/i18n" }) {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// scanMustInclude は、走査が cmd・internal の外まで届いていることを確かめるための実在のファイル。
// 改名・移動したらこの一覧も直す（打ち間違いのパスは黙って検査の外に落ちるので、実在も確かめる）。
var scanMustInclude = []string{
	"notice.go",
	"deploy/embed.go",
	"kit/embed.go",
	"migrations/embed.go",
}

// TestScanCoversWholeRepo は、ID の走査が cmd・internal の外にも届くことを確かめる。
func TestScanCoversWholeRepo(t *testing.T) {
	got := map[string]bool{}
	for _, rel := range goSourceFiles(t) {
		got[rel] = true
	}
	for _, want := range scanMustInclude {
		if _, err := os.Stat(filepath.Join(repoRoot, want)); err != nil {
			t.Errorf("scanMustInclude の %s: パスが存在しません。改名・移動した可能性があります（%v）", want, err)
			continue
		}
		if !got[want] {
			t.Errorf("%s が走査の対象に入っていません（cmd・internal の外が検査の外に落ちています）", want)
		}
	}
	for _, ng := range []string{"internal/i18n/i18n.go", "internal/i18n/lint_test.go", "notice_test.go"} {
		if got[ng] {
			t.Errorf("%s は走査の対象外のはずです", ng)
		}
	}
}

func TestCatalogHasEveryUsedID(t *testing.T) {
	used := collectUsedIDs(t)
	for id, where := range collectTemplateIDs(t) {
		if _, seen := used[id]; !seen {
			used[id] = where
		}
	}
	for id, where := range used {
		if !Has(JA, id) {
			t.Errorf("ja.json に %q がありません（%s で使っています）", id, where)
		}
		if !Has(EN, id) {
			t.Errorf("en.json に %q がありません（%s で使っています）", id, where)
		}
	}
}

func TestCatalogHasNoUnusedID(t *testing.T) {
	used := collectUsedIDs(t)
	for id, where := range collectTemplateIDs(t) {
		if _, seen := used[id]; !seen {
			used[id] = where
		}
	}
	for _, id := range IDs(JA) {
		if _, ok := used[id]; !ok {
			t.Errorf("対訳表の %q は、どこからも使われていません（消し忘れ。使う場所を足すか、ja.json と en.json から消す）", id)
		}
	}
}

// collectUsedIDs は i18n.T の第 2 引数の ID を集める（ID → 最初に見つけた場所）。
func collectUsedIDs(t *testing.T) map[string]string {
	t.Helper()
	used := map[string]string{}
	fset := token.NewFileSet()
	for _, rel := range goSourceFiles(t) {
		f, err := parser.ParseFile(fset, filepath.Join(repoRoot, rel), nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			// MCP の入力項目の説明は、構造体の jsonschema タグに ID を書き、登録のときに引く
			// （internal/server/mcp_tooldef.go）。タグの値も「使っている ID」として集めるので、
			// 表に無い ID・日本語の文面を直に書いたタグは TestCatalogHasEveryUsedID が落とす。
			if field, ok := n.(*ast.Field); ok && field.Tag != nil {
				if tag, err := strconv.Unquote(field.Tag.Value); err == nil {
					if id, ok := reflect.StructTag(tag).Lookup("jsonschema"); ok {
						if _, seen := used[id]; !seen {
							used[id] = fmt.Sprintf("%s:%d（jsonschema タグ）", rel, fset.Position(field.Pos()).Line)
						}
					}
				}
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			at, ok := idArgIndex(call.Fun)
			if !ok || len(call.Args) <= at {
				return true
			}
			where := fmt.Sprintf("%s:%d", rel, fset.Position(call.Pos()).Line)
			lit, ok := call.Args[at].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: 文面の ID が文字列リテラルではありません（組み立てると訳の抜けを機械で見つけられません）", where)
				return true
			}
			id, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("%s: ID を読めません: %v", where, err)
				return true
			}
			if _, seen := used[id]; !seen {
				used[id] = where
			}
			return true
		})
	}
	return used
}

// idArgIndex は、その呼び出しが文面の ID を取るものなら、ID が何番目の引数かを返す。
//
// 文面の入口は 3 つある。どれも ID を文字列リテラルで受けるので、ここで一緒に集める。
//   - T(lang, id, …)     その場で言語を決めて文面にする
//   - M(id, …)           言語を決めずに持ち回る（Msg）
//   - Errorf(id, …)      言語を決めずに error として返す
//   - Wrapf(err, id, …)  もとの error を包んだまま理由を足す
func idArgIndex(fun ast.Expr) (int, bool) {
	name := ""
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		if !ok || pkg.Name != "i18n" {
			return 0, false
		}
		name = f.Sel.Name
	case *ast.Ident:
		name = f.Name
	default:
		return 0, false
	}
	switch name {
	case "T", "Wrapf":
		return 1, true
	case "M", "Errorf":
		return 0, true
	}
	return 0, false
}

func mustRel(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(abs, path)
	if err != nil {
		return path
	}
	return rel
}

// templateCall は HTML テンプレートの {{T .Lang "id"}} を拾う。
// 画面の文面はテンプレートの中にあり、Go のソースの走査では見つからないため、こちらも集める。
// ID は必ず文字列リテラルで書く（printf などで組み立てると、ここで拾えず抜けに気づけない）。
//
// 括弧の中（{{template "head" (T .Lang "id")}}）も拾う。訳した文字列を別のテンプレートへ渡すときの
// 書き方で、ここで拾えないと「使っていない ID」と誤判定される。
var templateCall = regexp.MustCompile(`(?:\{\{|\()-?\s*T\s+[^\s})]+\s+"([^"]+)"`)

// TestTemplateScanIsNotEmpty は、テンプレートの走査が空振りしていないことを確かめる。
//
// collectTemplateIDs を使う 2 つの検査（対訳表に無い ID・どこからも使われない ID）は、集まる ID が
// 0 件でも緑になる。テンプレートが {{T …}} を 1 つも使っていなかった間は、実際にその状態だった
// （走査対象の .html は 20 ファイルあるのに、集まる ID は 0 件で、本体が一度も走らないまま両方 PASS していた）。
// 書き方を変えて正規表現が当たらなくなったときも同じ形で静かに戻るので、下限をここで押さえる。
func TestTemplateScanIsNotEmpty(t *testing.T) {
	files := repoFiles(t, ".html", nil)
	if len(files) < 10 {
		t.Fatalf("走査対象の .html が %d ファイルしかありません（走査が壊れている可能性）", len(files))
	}
	ids := collectTemplateIDs(t)
	t.Logf("走査対象の .html: %d ファイル / 集めた ID: %d 件", len(files), len(ids))
	if len(ids) < 100 {
		t.Fatalf("テンプレートから集めた ID が %d 件しかありません（0 件でも上の 2 つの検査は緑になります）", len(ids))
	}
	// 画面ごとの枠と、括弧の中で別のテンプレートへ渡す形の両方が拾えていること
	// （どちらかの書き方が拾えなくなると、その ID が「どこからも使われない」と誤判定される）。
	for _, want := range []string{
		"server.web.common.html_lang",        // layout.html の <html lang>（括弧の中ではない素の {{T .Lang …}}）
		"server.web.account.lang_h2",         // account.html（画面ごとの文面）
		"server.web.common.account_settings", // (headData .Lang (T .Lang "…")) の括弧の中
	} {
		if _, ok := ids[want]; !ok {
			t.Errorf("%s が走査に入っていません（テンプレートの書き方と templateCall がずれています）", want)
		}
	}
}

// collectTemplateIDs は .html の中で使っている ID を集める（ID → 最初に見つけた場所）。
func collectTemplateIDs(t *testing.T) map[string]string {
	t.Helper()
	used := map[string]string{}
	for _, rel := range repoFiles(t, ".html", nil) {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range templateCall.FindAllStringSubmatch(line, -1) {
				if _, seen := used[m[1]]; !seen {
					used[m[1]] = fmt.Sprintf("%s:%d", rel, i+1)
				}
			}
		}
	}
	return used
}
