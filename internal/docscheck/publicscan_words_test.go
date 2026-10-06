package docscheck

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPublicScanCompanyWords は、社内固有の語の一覧（private/ の下のファイル）の読み方を、
// 使い捨ての git リポジトリで 3 つの分岐ごとに確かめる。
//
//   - 開発側（private/ がある）で一覧がある: 一覧の語と接頭辞を検査し、公開物に置くと赤になる。
//   - 公開側（private/ が無い）: 一覧を持たないので、飛ばしたことを 1 行出して続ける。
//   - 開発側で一覧が無い・空・読めない行がある: 検査を飛ばさずに止める。
//
// なぜ要るか: 一覧は社内の固有名そのものなので、公開物に入る public-scan.sh から private/ へ移した。
// 読めなかったときに黙って飛ばすと、社内の語が公開物に入っても緑のままになり、誰も気づけない。
// 公開側では一覧が無いのが正しい状態なので、そこでは止めずに、飛ばしたことだけを出力に残す。
func TestPublicScanCompanyWords(t *testing.T) {
	requirePublicScanTools(t)

	const attrs = "* text=auto eol=lf\n/private/ export-ignore\n"
	leak := "# 見本\n\n公開物に入った社内の語: " + publicScanProbeWord + "。\n"
	leakID := "# 見本\n\nほかのプロジェクトの番号: " + publicScanProbePrefix + "-0123。\n"
	const skipLine = "== 社内固有の語: 社内の語の一覧（" + publicScanWordsPath + "）が無いので、この検査は飛ばしました"

	t.Run("開発側は一覧の語を検査し、置くと赤になる", func(t *testing.T) {
		dir, env := publicScanRepo(t, map[string]string{
			".gitattributes":    attrs,
			"README.md":         "# 見本\n\n公開物に入る文書。\n",
			publicScanWordsPath: publicScanWordsFile,
		})
		// 語の無い公開物は通り、一覧を読んだことが出力に出る（読んだ数が 0 なら空振りと見分けられない）。
		out, ok := runPublicScan(t, dir, env, "--only", "company")
		if !ok || !strings.Contains(out, "== 社内の語の一覧: "+publicScanWordsPath+"（語 1 個・内部管理番号の接頭辞 1 個）") ||
			!strings.Contains(out, "== 社内固有の語: 0 行") {
			t.Fatalf("前提が崩れています。語の無い公開物で、一覧を読んで 0 行で通るはずです:\n%s", out)
		}

		// 検出が起きる側: 一覧の語を公開物に置くと赤になる。
		writeFile(t, dir, "README.md", leak)
		out, ok = runPublicScan(t, dir, env, "--only", "company")
		if ok {
			t.Errorf("一覧の語（%s）が公開物にあるのに通りました:\n%s", publicScanProbeWord, out)
		}
		if !strings.Contains(out, "README.md:3:") || !strings.Contains(out, publicScanProbeWord) {
			t.Errorf("落ちた行に README.md の該当行が出ていません:\n%s", out)
		}

		// 一覧の id で足した接頭辞の内部管理番号も赤になる。
		writeFile(t, dir, "README.md", leakID)
		out, ok = runPublicScan(t, dir, env, "--only", "ids")
		if ok || !strings.Contains(out, "README.md:3:") {
			t.Errorf("一覧の接頭辞（%s）の内部管理番号を見落としました:\n%s", publicScanProbePrefix, out)
		}
	})

	t.Run("公開側は一覧が無いので飛ばしたことを 1 行出して続ける", func(t *testing.T) {
		dir, env := publicScanRepo(t, map[string]string{
			".gitattributes": attrs,
			"README.md":      leak,
			"docs/ids.md":    leakID,
		})
		out, ok := runPublicScan(t, dir, env)
		if !ok {
			t.Errorf("private/ の無い作業ツリーで落ちました（社内の語の一覧を持たないのが正しい状態です）:\n%s", out)
		}
		if n := strings.Count(out, skipLine); n != 1 {
			t.Errorf("飛ばしたことを知らせる行が %d 行あります（1 行のはず）:\n%s", n, out)
		}
		// 飛ばすのは社内の語だけで、ほかの検査は続ける（IM の内部管理番号は公開側でも見る）。
		for _, want := range []string{"== 内部管理番号: 0 行", "== 調べた相対リンク:", "== AI ツールの設定: 0 件"} {
			if !strings.Contains(out, want) {
				t.Errorf("飛ばした後の検査（%s）が走っていません:\n%s", want, out)
			}
		}

		// 対照: 同じ木に一覧を置く（private/ ができる）と、同じ語と番号で赤になる。
		// これが無いと「検査が死んでいるから緑」と「公開側だから飛ばした」を見分けられない。
		writeFile(t, dir, publicScanWordsPath, publicScanWordsFile)
		out, ok = runPublicScan(t, dir, env)
		if ok || strings.Contains(out, skipLine) {
			t.Fatalf("前提が崩れています。一覧を置いたのに社内の語の検査に入っていません:\n%s", out)
		}
		if !strings.Contains(out, "README.md:3:") || !strings.Contains(out, "docs/ids.md:3:") {
			t.Errorf("一覧を置いた後に、語と内部管理番号の両方を見つけていません:\n%s", out)
		}
	})

	t.Run("開発側で一覧が無い・空・読めない行があれば止める", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			words *string // nil なら一覧を置かない
			want  string
		}{
			{"一覧が無い", nil, "がありません（読めません）"},
			{"コメントだけで空", wordsPtr("# 見本\n\n"), "が空です（語 0 個・接頭辞 0 個"},
			{"語だけで接頭辞が無い", wordsPtr("text " + publicScanProbeWord + "\n"), "が空です（語 1 個・接頭辞 0 個"},
			{"読めない行がある", wordsPtr(publicScanWordsFile + "txet typo\n"), "読めない行です"},
		} {
			t.Run(c.name, func(t *testing.T) {
				files := map[string]string{
					".gitattributes":   attrs,
					"README.md":        leak,
					"private/notes.md": "# 社内の記録\n",
				}
				if c.words != nil {
					files[publicScanWordsPath] = *c.words
				}
				dir, env := publicScanRepo(t, files)
				out, ok := runPublicScan(t, dir, env)
				if ok {
					t.Errorf("private/ があるのに、一覧を読めないまま通りました:\n%s", out)
				}
				if !strings.Contains(out, c.want) {
					t.Errorf("止めた理由（%s）が出ていません:\n%s", c.want, out)
				}
				// 止めずに飛ばす形（公開側の扱い）に落ちていないこと
				if strings.Contains(out, skipLine) || strings.Contains(out, "== 社内固有の語: 0 行") {
					t.Errorf("開発側なのに社内の語の検査を飛ばしています:\n%s", out)
				}
			})
		}

		// 読めない一覧（権限）。root で回すと読めてしまうので、そのときは確かめない。
		t.Run("読めない", func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root では権限で読めなくできない")
			}
			dir, env := publicScanRepo(t, map[string]string{
				".gitattributes":    attrs,
				"README.md":         leak,
				publicScanWordsPath: publicScanWordsFile,
			})
			p := filepath.Join(dir, filepath.FromSlash(publicScanWordsPath))
			if err := os.Chmod(p, 0); err != nil {
				t.Fatalf("権限を変えられません: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
			// --head: 重ね合わせ（git diff）が読めないファイルを読みに行って、別の理由で落ちないように
			out, ok := runPublicScan(t, dir, env, "--head")
			if ok || !strings.Contains(out, "がありません（読めません）") {
				t.Errorf("読めない一覧で止まりませんでした:\n%s", out)
			}
		})
	})
}

func wordsPtr(s string) *string { return &s }

// TestPublicScanPublicNameBoundary は、公開名の例外が「名前そのもの」だけに効くことを確かめる。
//
// 公開名（リポジトリ・イメージ・識別子）は、社名を含んでいても公開してよいので、検査の前に行から取り除く。
// 前方一致で取り除くと、looptrack-internal のように公開名で始まる別の名前まで、社名の部分ごと消えてしまう。
// そうなると社内固有の語に当たらず、黙って通る。だから公開名の直後が英数字・_・- のときは例外にしない。
// 一覧の語に、公開名の中に現れる社名を使うのは、取り除かれる側を直接通すため。
func TestPublicScanPublicNameBoundary(t *testing.T) {
	requirePublicScanTools(t)

	// 社名は公開名の中にしか書けない（公開物の検査が、社名を書いた行を見つける）。
	// 検査が見るのは、この断片のつなぎ目ではなく、つないだ後の文字列。
	const co = "how" + "ashoji"
	const attrs = "* text=auto eol=lf\n/private/ export-ignore\n"
	const words = "# 見本の一覧\n\ntext " + co + "\nid " + publicScanProbePrefix + "\n"

	// 3 行目から 1 行ずつ。検出される形は公開名の続きの名前で、されない形は公開名そのもの。
	detected := []string{
		"https://github.com/" + co + "/looptrack-internal",
		"ghcr.io/" + co + "/looptrackops",
		"net." + co + ".looptrack-x",
		"https://api.github.com/repos/" + co + "/looptrack_old",
		"https://raw.githubusercontent.com/" + co + "/looptrack2/main/install.sh",
	}
	passed := []string{
		"https://github.com/" + co + "/looptrack/",
		"https://github.com/" + co + "/looptrack.git",
		"ghcr.io/" + co + "/looptrack:1.0.2",
		"net." + co + ".looptrack",
		"\"github.com/" + co + "/looptrack\"",
		"https://api.github.com/repos/" + co + "/looptrack/releases/latest",
		"https://raw.githubusercontent.com/" + co + "/looptrack/main/install.sh",
		"https://" + co + ".github.io/Looptrack/guide/",
	}

	body := "# 見本\n\n"
	for _, l := range detected {
		body += l + "\n"
	}
	for _, l := range passed {
		body += l + "\n"
	}

	dir, env := publicScanRepo(t, map[string]string{
		".gitattributes":    attrs,
		"README.md":         body,
		publicScanWordsPath: words,
	})
	out, _ := runPublicScan(t, dir, env, "--only", "company")

	// 対照: 検出が生きていること。一覧の語が公開名の続きの名前の中の社名を実際に拾う。
	// 公開名の続きの名前の行は、全部が出力に出る。
	for i, l := range detected {
		want := "README.md:" + strconv.Itoa(3+i) + ":"
		if !strings.Contains(out, want) {
			t.Errorf("公開名の続きの名前が社内固有の語として検出されていません（%s）:\n%s", l, out)
		}
	}
	// 公開名そのものの行は、1 行も出ない。
	for i, l := range passed {
		bad := "README.md:" + strconv.Itoa(3+len(detected)+i) + ":"
		if strings.Contains(out, bad) {
			t.Errorf("公開名そのものが検出されています（%s）:\n%s", l, out)
		}
	}
	if want := "== 社内固有の語: " + strconv.Itoa(len(detected)) + " 行・1 ファイル"; !strings.Contains(out, want) {
		t.Fatalf("前提が崩れています。検出は公開名の続きの %d 行だけのはずです（%s）:\n%s", len(detected), want, out)
	}
}
