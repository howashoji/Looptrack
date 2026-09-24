package docscheck

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// publicScanProbeID は「公開物に残してはいけない内部管理番号」の見本。
//
// この _test.go も公開物として配られるので、実在しうる番号を 1 つの文字列としては書かずに組み立てる
// （検査は _test.go の説明コメントに書いた ID を見る。1 つの文字列にしなければ、この見本そのものが
// 検査に当たって基準を動かすこともない）。組み立てた結果は検査の正規表現には当たるので、
// 使い捨てのリポジトリに書けば検出の見本として働く。
const publicScanProbeID = "IM" + "-9999"

// publicScanProbeWord は「公開物に残してはいけない社内固有の語」の見本。
//
// publicScanProbeID と同じ理由で 1 つの文字列としては書かない（この _test.go 自身は公開物として配られるので、
// そのまま書くと検査が自分自身を見つけて常時赤になる）。組み立てた結果は検査の一覧に載っている語になる。
const publicScanProbeWord = "req" + "weave"

// TestPublicScanScope は公開物の検査（deploy/public-scan.sh）が
// 「公開物に入るものを調べ、公開物に入らないものは調べない」ことを、使い捨ての git リポジトリで確かめる。
//
// なぜ要るか（両方向とも実際に壊れた）:
//
//   - 見るべきでないものを見た: 検査は未コミットの変更を HEAD の書き出しに重ねるが、重ねるかどうかを
//     git check-attr export-ignore で決めていた。.gitattributes の「/private/」はディレクトリに付く指定なので、
//     その配下のファイルに対する check-attr は unspecified を返す（git archive はディレクトリごと外す）。
//     このずれのため、公開物に入らない社内の記録を未コミットのまま編集すると、検査が必ず赤になった。
//   - 見るべきものを見落とすと気づけない: 逆に重ね合わせが効かなくなると、これから入る混入
//     （コード中のコメントに書いた内部管理番号など）はコミットされるまで誰にも見えない。
//     0 件で通ったことが「公開してよい」の根拠にならなくなるが、検査は緑のままなので気づけない。
//
// Windows では skip する（bash のスクリプト）。git・bash が無いときも skip する。
func TestPublicScanScope(t *testing.T) {
	requirePublicScanTools(t)

	base := map[string]string{
		".gitattributes":   "* text=auto eol=lf\n/private/ export-ignore\n",
		".gitignore":       "/scratch/\n",
		"README.md":        "# 見本\n\n公開物に入る文書。\n",
		"private/notes.md": "# 社内の記録\n\n公開物には入らない。\n",
	}

	t.Run("公開物に入らない場所の未コミットの変更は調べない", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// 既存のファイルを編集し、新しいファイルを git add する（どちらも公開物には入らない場所）。
		appendFile(t, dir, "private/notes.md", "追記（"+publicScanProbeID+"）。\n")
		writeFile(t, dir, "private/added.md", "新しい社内の記録（"+publicScanProbeID+"）。\n")
		gitIn(t, dir, env, "add", "--", "private/added.md")

		out, ok := runPublicScan(t, dir, env)
		// 重ね合わせの対象としては数えたうえで外していることまで確かめる
		//（「差分を見ていないから緑」では、見落としと見分けが付かない）。
		if !strings.Contains(out, "作業ツリーの変更 2 件") {
			t.Errorf("未コミットの変更 2 件を重ね合わせの対象として数えていません:\n%s", out)
		}
		if !ok {
			t.Errorf("公開物に入らない場所の未コミットの変更で落ちました:\n%s", out)
		}
	})

	t.Run("公開物の未コミットの変更は調べる", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		appendFile(t, dir, "README.md", "\n未コミットの追記（"+publicScanProbeID+"）。\n")

		out, ok := runPublicScan(t, dir, env)
		if ok {
			t.Errorf("公開物の未コミットの変更に内部管理番号があるのに通りました:\n%s", out)
		}
		if !strings.Contains(out, "README.md:") || !strings.Contains(out, publicScanProbeID) {
			t.Errorf("落ちた行に README.md の該当行が出ていません:\n%s", out)
		}
	})

	t.Run("公開物に加えた新しいファイルも調べる", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// コードのコメントに内部管理番号を書いて git add しただけの状態（コミットの前に捕まえたい形）。
		writeFile(t, dir, "internal/sample/render.go",
			"package sample\n\n// すり替える理由は、"+publicScanProbeID+" の経路を通せないため。\nconst Name = \"sample\"\n")
		gitIn(t, dir, env, "add", "--", "internal/sample/render.go")

		out, ok := runPublicScan(t, dir, env)
		if ok {
			t.Errorf("git add しただけの新しいファイルの内部管理番号を見落としました:\n%s", out)
		}
		if !strings.Contains(out, "internal/sample/render.go:") {
			t.Errorf("落ちた行に新しいファイルの該当行が出ていません:\n%s", out)
		}
	})

	t.Run("公開物に加えた未追跡のファイルも調べる", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// git add すらしていない状態（新しく足したファイルの混入を、版管理へ入れる前に捕まえたい形）。
		writeFile(t, dir, "internal/sample/render.go",
			"package sample\n\n// すり替える理由は、"+publicScanProbeID+" の経路を通せないため。\nconst Name = \"sample\"\n")

		out, ok := runPublicScan(t, dir, env)
		if ok {
			t.Errorf("未追跡のファイルの内部管理番号を見落としました:\n%s", out)
		}
		if !strings.Contains(out, "internal/sample/render.go:") {
			t.Errorf("落ちた行に未追跡のファイルの該当行が出ていません:\n%s", out)
		}
		// 何を重ねたかが出力から分かること（赤の原因が未追跡のファイルかを、その場で切り分けられるように）。
		if !strings.Contains(out, "未追跡 1 件を含む") || !strings.Contains(out, "未追跡 1 件を含む: internal/sample/render.go") {
			t.Errorf("調べた対象の行に未追跡のファイルの件数と名前が出ていません:\n%s", out)
		}
	})

	t.Run("公開物に入らない場所の未追跡のファイルは調べない", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// 先に対照を取る。未追跡がそもそも検査の対象に入っていなければ private/ のものも当然赤にならないので、
		// この subtest は「未追跡を重ねる実装が無くても緑」になり、退行を捕まえられなくなる。
		requireUntrackedIsScanned(t, dir, env)

		// private/ は git archive が export-ignore でディレクトリごと外すので、重ねても公開物には入らない。
		writeFile(t, dir, "private/added.md", "新しい社内の記録（"+publicScanProbeID+"）。\n")

		out, ok := runPublicScan(t, dir, env)
		// 重ね合わせの対象としては数えたうえで外していることまで確かめる
		//（「未追跡を見ていないから緑」では、見落としと見分けが付かない）。
		if !strings.Contains(out, "未追跡 1 件を含む") {
			t.Errorf("未追跡のファイルを重ね合わせの対象として数えていません:\n%s", out)
		}
		if !ok {
			t.Errorf("公開物に入らない場所の未追跡のファイルで落ちました:\n%s", out)
		}
	})

	t.Run("gitignore で無視される未追跡のファイルは調べない", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// 上と同じ理由で、先に「未追跡が検査の対象に入っている」ことを対照として確かめる。
		requireUntrackedIsScanned(t, dir, env)

		// 無視されるものは追跡されない限り git archive に入らないので、調べれば純粋な偽陽性になる
		//（実測で、無視されるものまで含めると他のセッションの作業ツリーが丸ごと入り、検査が常時赤になった）。
		writeFile(t, dir, "scratch/memo.md", "手元の作業用のメモ（"+publicScanProbeID+"）。\n")

		out, ok := runPublicScan(t, dir, env)
		if strings.Contains(out, "未追跡") {
			t.Errorf("無視されるファイルを重ね合わせの対象にしています:\n%s", out)
		}
		if !ok {
			t.Errorf("gitignore で無視される未追跡のファイルで落ちました:\n%s", out)
		}
	})

	t.Run("作業ツリーで消したファイルは調べない", func(t *testing.T) {
		withLegacy := map[string]string{}
		for k, v := range base {
			withLegacy[k] = v
		}
		withLegacy["legacy.go"] = "package legacy\n\n// 古いコメント（" + publicScanProbeID + "）。\n"
		dir, env := publicScanRepo(t, withLegacy)

		// 消す前は HEAD だけを見ても赤（この subtest が「消したから緑になった」ことを示すための対照）。
		if out, ok := runPublicScan(t, dir, env, "--head"); ok {
			t.Fatalf("前提が崩れています。HEAD に内部管理番号があるのに通りました:\n%s", out)
		}
		if err := os.Remove(filepath.Join(dir, "legacy.go")); err != nil {
			t.Fatalf("消せません: %v", err)
		}
		out, ok := runPublicScan(t, dir, env)
		if !ok {
			t.Errorf("作業ツリーで消したファイルを公開物として調べています:\n%s", out)
		}
	})

	// _test.go も公開物として配られるので、検査は説明のコメントに書いた内部管理番号を見る。
	// 入力・期待値としてデータに使う ID は見ない（テストは合成のイシューの ID をデータに使う）。
	sampleComment := "package sample\n\nimport \"testing\"\n\n" +
		"// すり替える理由は、" + publicScanProbeID + " の経路を通せないため。\n" +
		"func TestSample(t *testing.T) { _ = t }\n"
	sampleData := "package sample\n\nimport \"testing\"\n\n" +
		"// want は下で作る合成のイシュー " + publicScanProbeID + " を指す（このファイルがデータに使う ID）。\n" +
		"func TestSample(t *testing.T) {\n\twant := \"" + publicScanProbeID + "\"\n" +
		"\tif want == \"\" {\n\t\tt.Fatal(want)\n\t}\n}\n"

	t.Run("テストの説明コメントに書いた内部管理番号は調べる", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		writeFile(t, dir, "internal/sample/sample_test.go", sampleComment)
		gitIn(t, dir, env, "add", "--", "internal/sample/sample_test.go")

		out, ok := runPublicScan(t, dir, env)
		if ok {
			t.Errorf("_test.go の説明コメントに書いた内部管理番号を見落としました:\n%s", out)
		}
		if !strings.Contains(out, "internal/sample/sample_test.go:") {
			t.Errorf("落ちた行に該当の _test.go が出ていません:\n%s", out)
		}
	})

	t.Run("テストが入力・期待値として使う内部管理番号は調べない", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// 対照（前提の確認）: 同じ場所の説明コメントに書けば赤くなる。これを先に確かめないと、
		// 「検査が _test.go を見ていないから緑」を「誤検知しないから緑」と取り違える
		//（「X は起きない」のテストは、その経路が死んでいても通る）。
		writeFile(t, dir, "internal/sample/sample_test.go", sampleComment)
		gitIn(t, dir, env, "add", "--", "internal/sample/sample_test.go")
		if out, ok := runPublicScan(t, dir, env); ok {
			t.Fatalf("前提が崩れています。_test.go の説明コメントの内部管理番号が検査の対象に入っていません:\n%s", out)
		}

		// データとしての ID と、そのデータを指す説明だけにすると通る。
		writeFile(t, dir, "internal/sample/sample_test.go", sampleData)
		out, ok := runPublicScan(t, dir, env)
		if !ok {
			t.Errorf("テストが入力・期待値として使う内部管理番号で落ちました（誤検知）:\n%s", out)
		}
	})

	// 未追跡のファイルは、禁止語の検査（内容を守る）には流し、ラチェット（総量の増加を守る）の母数からは外す。
	// 1 本の対象集合を両方が読むと、同じ版を調べても作業ツリーに置いてあるものしだいでラチェットの結果が変わる
	//（実際に、未追跡の _test.go を 1 つ置くだけで「増えました（基準 0 → 実測 1）」で落ちた）。

	t.Run("未追跡のテストはラチェットの母数に入れない", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// 「赤にならないこと」を見る subtest なので、先に対照を取る
		//（未追跡を重ねる実装ごと失われても、この subtest だけなら緑のまま通ってしまう）。
		requireUntrackedIsScanned(t, dir, env)

		// git add していない _test.go。説明コメントの内部管理番号は、まだ公開物の総量に入っていない。
		writeFile(t, dir, "internal/sample/sample_test.go", sampleComment)

		out, ok := runPublicScan(t, dir, env)
		// 重ね合わせの対象としては数えたうえで、ラチェットの母数からだけ外していることを確かめる。
		if !strings.Contains(out, "未追跡 1 件を含む") {
			t.Errorf("未追跡のファイルを重ね合わせの対象として数えていません:\n%s", out)
		}
		if strings.Contains(out, "増えました") {
			t.Errorf("未追跡の _test.go をラチェットの母数に入れています:\n%s", out)
		}
		if !strings.Contains(out, "内部管理番号（テストの説明コメント）: 0 行") {
			t.Errorf("ラチェットの実測に未追跡の _test.go が入っています:\n%s", out)
		}
		if !ok {
			t.Errorf("未追跡の _test.go の説明コメントの内部管理番号で落ちました:\n%s", out)
		}
	})

	t.Run("未追跡のテストの社内固有の語は禁止語の検査で見つける", func(t *testing.T) {
		dir, env := publicScanRepo(t, base)
		// 上の subtest で母数から外したせいで、未追跡の _test.go の「内容」まで見なくなっていないこと
		//（未追跡を重ねるのは、版管理へ入れる前に混入を捕まえるためなので、ここが死ぬと目的が失われる）。
		writeFile(t, dir, "internal/sample/sample_test.go",
			"package sample\n\nimport \"testing\"\n\n"+
				"// 置き換える先は "+publicScanProbeWord+"（"+publicScanProbeID+" の経路を通せないため）。\n"+
				"func TestSample(t *testing.T) { _ = t }\n")

		out, ok := runPublicScan(t, dir, env)
		if ok {
			t.Errorf("未追跡の _test.go の社内固有の語を見落としました:\n%s", out)
		}
		if !strings.Contains(out, "internal/sample/sample_test.go:") || !strings.Contains(out, publicScanProbeWord) {
			t.Errorf("落ちた行に未追跡の _test.go の該当行が出ていません:\n%s", out)
		}
	})

	t.Run("--help がヘッダのコメント塊を最後まで出す", func(t *testing.T) {
		// sed の行範囲を手で持っているので、ヘッダに行を足すと --help が末尾を黙って落とす（実際に落とした）。
		// 行数ではなく実物どうしを突き合わせて、ずれた時点で落とす。
		want := publicScanHeader(t)

		script, err := filepath.Abs(filepath.Join(repoRoot, "deploy", "public-scan.sh"))
		if err != nil {
			t.Fatalf("スクリプトのパスを解決できません: %v", err)
		}
		cmd := exec.Command("bash", script, "--help")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--help を回せません（%v）:\n%s", err, out)
		}
		got := strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
		if got != want {
			t.Errorf("--help の出力がヘッダのコメント塊と一致しません（sed の行範囲がずれています）。\n--- --help（%d 行）---\n%s\n--- ヘッダ（%d 行）---\n%s",
				len(strings.Split(got, "\n")), got, len(strings.Split(want, "\n")), want)
		}
	})
}

// aiToolConfigPaths は、このリポジトリで作業する AI ツールの設定・指示の置き場の見本（公開物に入れないもの）。
var aiToolConfigPaths = []string{
	".claude/settings.json",
	".claude/skills/issue/SKILL.md",
	".codex/config.toml",
	"CLAUDE.md",
	"AGENTS.md",
	".github/copilot-instructions.md",
	".cursor/rules/dev.mdc",
	".mcp.json",
	"docs/superpowers/specs/design.md",
}

// TestPublicScanAIToolConfig は、公開物の検査が「このリポジトリで作業する AI ツールの設定・指示が
// 公開物に入っていること」を見つけ、export-ignore で外せば通ることを、使い捨ての git リポジトリで確かめる。
//
// なぜ要るか: 置き場を変えられない設定（.claude/・.codex/・CLAUDE.md など）は、.gitattributes で
// 1 つずつ外していた。外し忘れたもの（skills の写し・AI への指示の文書・設計のメモ）は語の検査に掛からず、
// そのまま公開物に入っていた。
func TestPublicScanAIToolConfig(t *testing.T) {
	requirePublicScanTools(t)

	withConfigs := func(attrs string) map[string]string {
		files := map[string]string{
			".gitattributes": "* text=auto eol=lf\n/private/ export-ignore\n" + attrs,
			"README.md":      "# 見本\n\n公開物に入る文書。\n",
		}
		for _, p := range aiToolConfigPaths {
			files[p] = "開発用の設定の見本\n"
		}
		return files
	}

	t.Run("公開物に入った AI ツールの設定を見つける", func(t *testing.T) {
		dir, env := publicScanRepo(t, withConfigs(""))
		out, ok := runPublicScan(t, dir, env, "--head", "--only", "aiconf")
		if ok {
			t.Errorf("AI ツールの設定が公開物に入っているのに通りました:\n%s", out)
		}
		// 見つけたのはディレクトリか、ファイルそのもの（最上位の名前で 1 行）
		for _, want := range []string{".claude:", ".codex:", "CLAUDE.md:", "AGENTS.md:", ".github/copilot-instructions.md:",
			".cursor:", ".mcp.json:", "docs/superpowers:"} {
			if !strings.Contains(out, "\n"+want) {
				t.Errorf("%s を見つけていません:\n%s", strings.TrimSuffix(want, ":"), out)
			}
		}
	})

	t.Run("export-ignore で外せば通る", func(t *testing.T) {
		dir, env := publicScanRepo(t, withConfigs(
			"/.claude/ export-ignore\n/.codex/ export-ignore\n/.cursor/ export-ignore\n/CLAUDE.md export-ignore\n"+
				"/AGENTS.md export-ignore\n/.mcp.json export-ignore\n/.github/copilot-instructions.md export-ignore\n"+
				"/docs/superpowers/ export-ignore\n"))
		out, ok := runPublicScan(t, dir, env, "--head", "--only", "aiconf")
		if !ok || !strings.Contains(out, "AI ツールの設定: 0 件") {
			t.Errorf("export-ignore で外した AI ツールの設定で落ちました:\n%s", out)
		}
	})

	t.Run("配る kit・雛形・testdata の中は見ない", func(t *testing.T) {
		// 対照: 同じリポジトリで、ルートの設定は見つかること（見つける経路が死んでいたら、この subtest は無意味に緑になる）。
		files := map[string]string{
			".gitattributes":                            "* text=auto eol=lf\n",
			"CLAUDE.md":                                 "開発用の指示の見本\n",
			"kit/core/skills/issue/SKILL.md":            "配る skill の見本\n",
			"docs/templates/CLAUDE-snippet.md":          "導入先の CLAUDE.md に入る節の見本\n",
			"internal/sample/testdata/ws/CLAUDE.md":     "導入を確かめるテストの入力\n",
			"internal/sample/testdata/ws/.claude/x.txt": "導入を確かめるテストの入力\n",
		}
		dir, env := publicScanRepo(t, files)
		out, ok := runPublicScan(t, dir, env, "--head", "--only", "aiconf")
		if ok || !strings.Contains(out, "AI ツールの設定: 1 件") || !strings.Contains(out, "\nCLAUDE.md:") {
			t.Fatalf("前提が崩れています。ルートの CLAUDE.md を 1 件だけ見つけるはずです:\n%s", out)
		}
		for _, p := range []string{"kit/", "docs/templates/", "testdata/"} {
			if strings.Contains(out, p) {
				t.Errorf("%s の中を AI ツールの設定として数えました:\n%s", p, out)
			}
		}
	})

	t.Run("改名で公開物の外へ移したパスは調べない", func(t *testing.T) {
		// 未コミットの git mv（改名）は、元のパスの削除として重ねる。改名の検出が効くと新しいパスしか
		// 重ね合わせの対象に出ず、外したつもりの元のパスが HEAD の中身のまま調べられて赤になった。
		dir, env := publicScanRepo(t, map[string]string{
			".gitattributes": "* text=auto eol=lf\n/private/ export-ignore\n",
			"docs/notes.md":  "# 社内の記録\n\n" + publicScanProbeID + " の経緯。\n",
		})
		// 対照: 移す前は赤（中身が検査に当たることの確認）。
		if out, ok := runPublicScan(t, dir, env); ok || !strings.Contains(out, "docs/notes.md:") {
			t.Fatalf("前提が崩れています。移す前の docs/notes.md が検査に当たりません:\n%s", out)
		}
		if err := os.MkdirAll(filepath.Join(dir, "private"), 0o755); err != nil {
			t.Fatalf("作れません: %v", err)
		}
		gitIn(t, dir, env, "mv", "docs/notes.md", "private/notes.md")
		out, ok := runPublicScan(t, dir, env)
		if !ok {
			t.Errorf("private/ へ移した（未コミットの改名）のに、元のパスで落ちました:\n%s", out)
		}
	})
}

// publicScanHeader は deploy/public-scan.sh の先頭のコメント塊（shebang の次から、# で始まる連続した行）を返す。
// --help はこの塊を出すことになっているので、期待値は行数ではなく実物から数える。
func publicScanHeader(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "public-scan.sh"))
	if err != nil {
		t.Fatalf("読めません: %v", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "#!") {
		t.Fatalf("1 行目が shebang ではありません: %q", lines[0])
	}
	var header []string
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "#") {
			break
		}
		header = append(header, line)
	}
	if len(header) == 0 {
		t.Fatal("先頭にコメント塊がありません")
	}
	return strings.Join(header, "\n")
}

// requireUntrackedIsScanned は、公開物のパスに未追跡のファイルを 1 つ置いて検査が赤になることを確かめ、
// そのファイルを消して元の状態に戻す（偽陽性を見る subtest の対照）。
//
// 偽陽性を見る subtest は「赤にならないこと」を見るので、未追跡を重ねる実装ごと失われても緑のまま通る。
// 先にこの対照を通しておけば、実装が落ちた時点でその subtest も落ちる。
func requireUntrackedIsScanned(t *testing.T, dir string, env []string) {
	t.Helper()
	rel := "internal/sample/render.go"
	writeFile(t, dir, rel,
		"package sample\n\n// すり替える理由は、"+publicScanProbeID+" の経路を通せないため。\nconst Name = \"sample\"\n")
	out, ok := runPublicScan(t, dir, env)
	if ok || !strings.Contains(out, rel+":") {
		t.Fatalf("前提が崩れています。公開物のパスに置いた未追跡のファイルが検査の対象に入っていません:\n%s", out)
	}
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("消せません: %v", err)
	}
}

// requirePublicScanTools は public-scan.sh を回せない環境で skip する。
func requirePublicScanTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("public-scan.sh は bash のスクリプト（Windows のジョブでは走らせない）")
	}
	for _, bin := range []string{"bash", "git", "tar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s がありません: %v", bin, err)
		}
	}
}

// publicScanRepo は使い捨ての git リポジトリを作り、files（リポジトリからの相対パス → 中身）を
// 1 つのコミットに入れて、その作業ツリーのパスと git に渡す環境変数を返す。
//
// 利用者の設定（~/.gitconfig・システムの設定）に左右されないように HOME を temp に向け、
// 著者・コミッタは環境変数で決める。
func publicScanRepo(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	home := filepath.Join(base, "home")
	for _, d := range []string{dir, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("作れません: %v", err)
		}
	}
	env := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=docscheck", "GIT_AUTHOR_EMAIL=docscheck@example.invalid",
		"GIT_COMMITTER_NAME=docscheck", "GIT_COMMITTER_EMAIL=docscheck@example.invalid",
	)

	gitIn(t, dir, env, "-c", "init.defaultBranch=main", "init", "-q")
	paths := make([]string, 0, len(files))
	for rel, body := range files {
		writeFile(t, dir, rel, body)
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	gitIn(t, dir, env, append([]string{"add", "--"}, paths...)...)
	gitIn(t, dir, env, "commit", "-q", "-m", "見本")
	return dir, env
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("作れません: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("書けません: %v", err)
	}
}

func appendFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("読めません: %v", err)
	}
	writeFile(t, dir, rel, string(b)+body)
}

func gitIn(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// runPublicScan は public-scan.sh を dir で回し、出力と「0 件で通ったか」を返す。
// 終了コード 1 は「見つかった」なので失敗として扱わない（それ以外は回せていないので t.Fatal）。
func runPublicScan(t *testing.T, dir string, env []string, args ...string) (string, bool) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(repoRoot, "deploy", "public-scan.sh"))
	if err != nil {
		t.Fatalf("スクリプトのパスを解決できません: %v", err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return string(out), false
	}
	t.Fatalf("public-scan.sh を回せません（%v）:\n%s", err, out)
	return "", false
}
