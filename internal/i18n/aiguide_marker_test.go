package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// AI 向けの案内が、**2 言語化された文面のリテラルを目印にしていない**ことを確かめる（設計 DESIGN.md §9-6）。
//
// 背景: AI に行動を促す通知（トークン情報の付与・要件の close・導入状態）は利用者の言語で出る。
// 案内文が「『…』と出たら〜しなさい」と日本語の文面を目印にしていると、英語で動く AI にはその語が
// 現れないので促しが届かない。**壊れてもテストは緑のまま**で、英語環境の AI が黙って従わなくなるだけ。
//
// 次に案内文を書く人へ（目印の作り方）:
//   - **言語に依らない目印を指す**。いちばん確実なのは通知が示す looptrack のコマンド
//     （`looptrack issue usage attach <ID>` など）。ASCII で、どの言語の文面にも {command} として入る。
//   - コマンドを示さない通知は、**条件を言葉で説明する**（「ツールの結果とは別の行で、setup ツールか
//     更新のコマンドを示す注記が付いていたら」）。文面の引用で代用しない。
//   - AI が**書く**側の取り決めの語（コメント先頭の「フィードバック:」など）は目印にしてよい。
//     利用者の言語で変わらず、サーバも同じ語で判定する。

// aiGuideMDs は AI 向けの案内のうち Markdown のもの（リポジトリからの相対パス）。
// .go は下の walk でリポジトリ全体の文字列リテラルを見るので、ここには挙げない。
var aiGuideMDs = []string{
	"internal/guide/common.md",    // guide の共通規則（CLI・REST・MCP が返す）
	"internal/guide/en/common.md", // 同・英語版（日本語が正本）
	"docs/AI-GUIDE.md",            // 別プロジェクトのセッション向けの案内
}

// jaOnlyMarkers は、案内文が目印にしてはいけない日本語の文面。
// id はその文面を持つ対訳表の ID で、literal はそのうち案内文に写されがちな部分。
// 「ja.json にあり、en.json には無い」ことをテスト自身が確かめるので、一覧が実態から離れない。
var jaOnlyMarkers = []struct{ id, literal string }{
	{"service.usage.notice.attach", "トークン情報が未付与です"},
	{"service.usage.notice.closed_without", "警告:"},
	{"server.api.usage.missing", "トークン情報の未付与"},
	{"domain.closable.notice", "の下位がすべて完了しました"},
	{"server.mcp.setup.state.missing", "【導入が未完了】"},
	{"server.mcp.setup.state.stale", "【配布スクリプトの更新】"},
}

// langNeutralMarkers は、AI に行動を促す通知が持つべき「言語に依らない目印」。
// どの言語の文面にも同じ形で入る（= 案内文はこれを目印にできる）。
var langNeutralMarkers = []struct{ id, marker string }{
	{"service.usage.notice.attach", "{command}"},
	{"service.usage.notice.closed_without", "{command}"},
	{"server.api.usage.missing", "{command}"},
	{"domain.rules.usage_required_on_close", "{command}"},
	{"domain.closable.notice", "looptrack issue show {id}"},
}

// usageAttachGuides は、トークン情報の付与を促している案内文。言語に依らない目印
// （looptrack issue usage attach）を指していることを確かめる。
var usageAttachGuides = []string{
	"internal/guide/common.md",
	"internal/guide/en/common.md",
	"docs/AI-GUIDE.md",
	"internal/server/mcp.go",
	"internal/client/kitinit/texts.go",
}

// usageAttachCommand は付与のコマンド（domain.UsageAttachCommand が 1 か所で組み立てる形）。
// internal/domain は internal/i18n を import するので、ここでは綴りで持つ。
const usageAttachCommand = "looptrack issue usage attach"

func TestActionNoticesCarryLanguageNeutralMarker(t *testing.T) {
	for _, m := range langNeutralMarkers {
		for lang := range catalogs {
			s, ok := catalogs[lang][m.id]
			if !ok {
				t.Errorf("%s.json に %q がありません", lang, m.id)
				continue
			}
			if !strings.Contains(s, m.marker) {
				t.Errorf("%s.json の %q に言語に依らない目印 %q がありません（案内文がこれを目印にしています）:\n%s",
					lang, m.id, m.marker, s)
			}
		}
	}
	// 付与のコマンドは対訳表では {command} で、実際の値は 1 か所（domain.UsageAttachCommand）で組む。
	// 案内文がその綴りを指していること（= 英語で動く AI にも同じ条件で届くこと）を確かめる。
	for _, rel := range usageAttachGuides {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if !strings.Contains(string(b), usageAttachCommand) {
			t.Errorf("%s に %q がありません（トークン情報の付与の目印はこのコマンド）", rel, usageAttachCommand)
		}
	}
}

func TestAIGuidesDoNotKeyOnLocalizedText(t *testing.T) {
	for _, m := range jaOnlyMarkers {
		ja, ok := catalogs[JA][m.id]
		if !ok {
			t.Errorf("ja.json に %q がありません（jaOnlyMarkers の一覧が古くなっています）", m.id)
			continue
		}
		if !strings.Contains(ja, m.literal) {
			t.Errorf("ja.json の %q に %q がありません（jaOnlyMarkers の一覧が古くなっています）:\n%s", m.id, m.literal, ja)
			continue
		}
		if en := catalogs[EN][m.id]; strings.Contains(en, m.literal) {
			// 英語でも同じ語が出るなら、それは言語に依らない目印。一覧から外してよい。
			t.Errorf("en.json の %q にも %q があります（jaOnlyMarkers から外してください）:\n%s", m.id, m.literal, en)
		}
	}

	for _, text := range aiGuideTexts(t) {
		for _, m := range jaOnlyMarkers {
			if !strings.Contains(text.body, m.literal) {
				continue
			}
			t.Errorf(`%s が 2 言語化された文面 %q（対訳表の %s）を目印にしています。
英語で動く AI にはこの語が現れないので、促しが届きません。
言語に依らない目印（通知が示す looptrack のコマンド）を指すか、条件を言葉で説明してください（本ファイル冒頭の説明）。`,
				text.where, m.literal, m.id)
		}
	}
}

// aiGuideText は検査する案内文 1 つ（where は場所の説明、body は本文）。
type aiGuideText struct{ where, body string }

// aiGuideTexts は、AI 向けの案内の本文を集める。
//   - Markdown は aiGuideMDs のファイルの全文。
//   - Go はリポジトリ全体の**文字列リテラル**（instructions・prompts・init が置く案内節はすべてここに入る）。
//     コメントは読み飛ばす（go/ast で文字列リテラルだけを取る。仕様を説明するコメントは案内文ではない）。
//     案内文の置き場が増えても一覧に足し忘れないよう、走査の起点はリポジトリ全体にする。
func aiGuideTexts(t *testing.T) []aiGuideText {
	t.Helper()
	var out []aiGuideText
	for _, rel := range aiGuideMDs {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		out = append(out, aiGuideText{where: rel, body: string(b)})
	}
	root := mustAbs(t, repoRoot)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("%s: %v", path, perr)
		}
		rel := filepath.ToSlash(mustRel(t, path))
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				v = lit.Value
			}
			out = append(out, aiGuideText{where: rel + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line), body: v})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
