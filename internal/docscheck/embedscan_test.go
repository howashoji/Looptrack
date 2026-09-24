package docscheck

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// 実行ファイルに埋め込むもの（//go:embed）に、配るつもりのないものが混ざっていないことを確かめる。
//
// きっかけ: //go:embed static/*（ディレクトリの中身を丸ごと）が、node --test で回すテストコード
// internal/server/static/render_test.mjs まで実行ファイルに入れ、GET <base>/static/ から
// 認証なしで配信していた（internal/server/embed_assets_test.go が 404 を実測する）。
// 同じく //go:embed fonts が fonts/README.md（置き場の説明）を入れていた。
//
// どちらも「ディレクトリ丸ごと」「* 」のパターンで起きる。パターンを絞っても、あとから同じ
// ディレクトリにファイルを置けば黙って戻るので、ここで**リポジトリの //go:embed を実際に走査して**
// 落とす。個別の列挙にすると、足した人が一覧を更新しない限り気づけない。
//
// 落とすもの:
//   - *_test.* （テストコード。配布物ではない）
//   - README* （置き場の説明。配布物ではない）
//   - 想定外の拡張子（下の embedOKExt 以外）
func TestEmbeddedFilesAreDistributable(t *testing.T) {
	// embedOKExt は、いま実際に埋め込まれているものから決めた拡張子。
	// 増やすときは「それを配布物に入れる理由」を一緒に書くこと。
	embedOKExt := map[string]string{
		".css":  "Web 画面の様式（internal/server/static）",
		".html": "Web 画面の雛形（internal/server/templates）",
		".ico":  "デスクトップ版のトレイのアイコン（Windows）",
		".js":   "Web 画面の script（internal/server/static）",
		".json": "対訳表（internal/i18n）・ルールの例（deploy/rules）・kit の manifest",
		".md":   "AI が読む本文（internal/guide/common.md・kit の rules と skill）",
		".png":  "デスクトップ版のトレイのアイコン（macOS・Linux）",
		".sql":  "マイグレーション（migrations）・権限（deploy/grants.sql）",
		".ttf":  "PDF に使う日本語フォント本体（internal/client/report/pdf/fonts）",
		".txt":  "フォントのライセンス文 OFL.txt（再頒布の条件）",
	}
	// 拡張子の無いもの（拡張子では見分けられないので名前で許す）。
	embedOKName := map[string]string{
		"NOTICE": "依存のライセンス表示（looptrack licenses が出す。生成物）",
	}
	// embedKnown は、いま埋め込まれていると分かっているが、この検査の対象から外すもの。
	// **新しく同じものが増えたら落ちる**ように、パスを 1 つずつ書く（種類でまとめて許さない）。
	embedKnown := map[string]string{
		"kit/embed.go":      "kit/embed.go の //go:embed * が自分自身を含む（* は「.」「_」で始まらないものを全部取る）。配るのは core/ と loop/ の下だけ（kit.Names）なので配布物としては出ない",
		"kit/embed_test.go": "同上（* が拾う）。配布物としては出ないが、実行ファイルには入っている",
		"kit/README.md":     "同上（* が拾う）。kit 自体の説明で、配らない（internal/docscheck/kit_test.go が対象外にしている）",
		"kit/README.ja.md":  "同上（* が拾う）",
	}

	got := scanEmbeddedFiles(t)

	// 走査が空振りでないことを確かめる（0 件なら、以下の検査は何も見ずに緑になる）。
	if len(got) < 40 {
		t.Fatalf("//go:embed の走査で %d 件しか見つかりません（少なすぎます。走査が壊れている可能性）", len(got))
	}
	// 解決が実際に効いていることを、分かっている 3 つで確かめる。
	for _, want := range []string{
		"internal/client/report/pdf/fonts/BIZUDGothic-Regular.ttf", // パターン fonts/*.ttf
		"internal/server/static/render.js",                         // パターン static/*.js
		"kit/loop/rules/working-discipline.md",                     // パターン *（ディレクトリを下まで歩く）
	} {
		if !slices.Contains(got, want) {
			t.Errorf("走査に %s がありません（パターンの解決が壊れています）", want)
		}
	}
	// 絞ったものが本当に外れていることを確かめる（この検査自身のラチェット）。
	for _, ng := range []string{
		"internal/server/static/render_test.mjs",
		"internal/client/report/pdf/fonts/README.md",
	} {
		if slices.Contains(got, ng) {
			t.Errorf("%s が埋め込まれています（//go:embed のパターンを配信・配布するものだけに絞ってください）", ng)
		}
	}

	for _, rel := range got {
		if _, ok := embedKnown[rel]; ok {
			continue
		}
		name := path.Base(rel)
		switch {
		case isTestFileName(name):
			t.Errorf("%s: テストコードが実行ファイルに埋め込まれています。//go:embed のパターンを絞ってください（置き場は動かさなくて済みます）", rel)
		case strings.HasPrefix(name, "README"):
			t.Errorf("%s: 置き場の説明（README）が実行ファイルに埋め込まれています。//go:embed のパターンを絞ってください", rel)
		default:
			ext := path.Ext(name)
			if _, ok := embedOKExt[ext]; ok {
				continue
			}
			if _, ok := embedOKName[name]; ok {
				continue
			}
			t.Errorf("%s: 想定外の拡張子 %q が埋め込まれています。配布物に入れてよいものなら embedOKExt に理由つきで足し、"+
				"そうでなければ //go:embed のパターンを絞ってください", rel, ext)
		}
	}
}

// isTestFileName は *_test.* の形か（render_test.mjs・embed_test.go・foo_test.js）。
func isTestFileName(name string) bool {
	ext := path.Ext(name)
	return strings.HasSuffix(strings.TrimSuffix(name, ext), "_test")
}

// scanEmbeddedFiles は、リポジトリの .go にある //go:embed を全部読み、埋め込まれるファイルを
// リポジトリからの相対パス（スラッシュ区切り・重複なし・整列済み）で返す。
//
// Go の規則に合わせる:
//   - パターンは、そのディレクティブを書いた .go のあるディレクトリからの相対
//   - ディレクトリを指したときは中身を全部（「.」「_」で始まる名前を除く）
//   - all: を付けたときは「.」「_」で始まるものも含む
func scanEmbeddedFiles(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	directives := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			n := d.Name()
			// .git・.claude/worktrees（別の作業ツリーの複製）は見ない。
			if p != root && (strings.HasPrefix(n, ".") || n == "vendor" || n == "node_modules" || n == "testdata") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dir := filepath.Dir(p)
		// 字下げされていても directive として効く（grouped var 宣言の中など）。どこまで拾うかは embedDirectives。
		for _, rest := range embedDirectives(p, string(b)) {
			directives++
			for _, pat := range strings.Fields(rest) {
				pat = strings.Trim(pat, `"`+"`")
				all := false
				if s, ok := strings.CutPrefix(pat, "all:"); ok {
					pat, all = s, true
				}
				matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat)))
				if err != nil {
					t.Errorf("%s: パターン %q が壊れています（%v）", p, pat, err)
					continue
				}
				if len(matches) == 0 {
					t.Errorf("%s: パターン %q が 1 つも当たりません（ビルドが通らないはずです）", p, pat)
					continue
				}
				for _, m := range matches {
					addEmbedded(t, root, m, all, set)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if directives == 0 {
		t.Fatal("//go:embed のディレクティブが 1 つも見つかりません（走査が壊れています）")
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// embedDirectives は Go のソース src から、directive として実際に効く //go:embed の
// 「パターンの並び」を書かれた順に返す（先頭の "//go:embed " を除いた残り）。
//
// どこまで拾うかは、コンパイラ（cmd/compile/internal/syntax の scanner と noder）の規則に合わせた:
//   - 行コメントは、**字下げされていても効く**。grouped var 宣言（var ( … ) ）の中に字下げして
//     書いたものは、実際にファイルを埋め込む（実測した）。だから行頭に限ってはいけない。
//   - ブロックコメント（/* … */）の中は効かない（scanner は /* では go: を見ない）。
//   - 同じ行にコードが先にあると効かない（directive は行に単独で置かれていること）。
//   - 文字列リテラル（生文字列を含む）の中は、そもそもコメントではないので効かない。
//
// 自前で行を切ると後ろの 3 つを取りこぼすので、go/scanner にコメントだけを拾わせる
// （go/build の readGoInfo が //go:embed を集めるのと同じやり方）。
//
// 関数の中や var 以外に付けたものは「効かない」のではなくコンパイルエラーになる
// （go:embed cannot apply to var inside func・misplaced go:embed directive）。
// ビルドの通るリポジトリには存在しえないので、拾っても偽陽性にならない。ここでは除かない。
func embedDirectives(name, src string) []string {
	fset := token.NewFileSet()
	f := fset.AddFile(name, -1, len(src))
	var sc scanner.Scanner
	sc.Init(f, []byte(src), nil, scanner.ScanComments)
	var out []string
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		rest, ok := strings.CutPrefix(strings.TrimRight(lit, "\r"), "//go:embed ")
		if !ok {
			continue
		}
		// 行に単独で置かれているか（前にコードがあると directive にならない）。
		off := fset.Position(pos).Offset
		if strings.TrimSpace(src[strings.LastIndexByte(src[:off], '\n')+1:off]) != "" {
			continue
		}
		out = append(out, rest)
	}
	return out
}

// addEmbedded は m（ファイルかディレクトリ）を set に入れる。ディレクトリは中身を全部歩く。
func addEmbedded(t *testing.T, root, m string, all bool, set map[string]bool) {
	t.Helper()
	fi, err := os.Stat(m)
	if err != nil {
		t.Errorf("%s: %v", m, err)
		return
	}
	rel := func(p string) string {
		r, err := filepath.Rel(root, p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return ""
		}
		return filepath.ToSlash(r)
	}
	if !fi.IsDir() {
		if r := rel(m); r != "" {
			set[r] = true
		}
		return
	}
	filepath.WalkDir(m, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		n := d.Name()
		if p != m && !all && strings.HasPrefix(n, ".") || p != m && !all && strings.HasPrefix(n, "_") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			if r := rel(p); r != "" {
				set[r] = true
			}
		}
		return nil
	})
}

// TestEmbedDirectiveScanPicksUpIndented は、grouped var 宣言の中に字下げして書いた //go:embed を
// 走査が拾うことを確かめる。Go はこれを directive として扱って実際に埋め込むので、走査が
// 「行頭だけ」に戻ると、配ってはいけないものが黙って通る。
func TestEmbedDirectiveScanPicksUpIndented(t *testing.T) {
	const rel = "embedscan/indented_var.go.txt"
	got := embedDirectives(rel, readEmbedScanTestdata(t, rel))
	want := []string{"probe_test.mjs", "sub/*.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("%s: 走査した directive が %q でした（期待 %q）。字下げされた //go:embed を取りこぼしています", rel, got, want)
	}
}

// TestEmbedDirectiveScanIgnoresInertPositions は、Go が directive として扱わない位置に書かれた
// //go:embed を走査が拾わないことを確かめる。拾いすぎも穴で、実在しないものを咎める検査は
// 次に読む人に信用されなくなる。
func TestEmbedDirectiveScanIgnoresInertPositions(t *testing.T) {
	const rel = "embedscan/inert_positions.go.txt"
	if got := embedDirectives(rel, readEmbedScanTestdata(t, rel)); len(got) != 0 {
		t.Errorf("%s: directive として効かない位置の //go:embed を %q と拾っています", rel, got)
	}
}

// readEmbedScanTestdata は testdata の固定データを読む（拡張子を .go.txt にしてあるのは、
// Go のツールチェインにソースとして拾わせないため）。
func readEmbedScanTestdata(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
