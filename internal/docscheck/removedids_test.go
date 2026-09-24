package docscheck

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// 撤去した ID（ルールのキー・エラーコード・対訳キー・サブコマンド名）が、撤去したあとの木に
// 残っていないかを確かめる。
//
// なぜ要るか: golden は「期待値そのもの」なので、古い期待値が残ること＝仕様が巻き戻ることになる。
// しかも git は行単位で自動マージするので、撤去した側と自分の側が同じファイルの違う行を触っていると
// 衝突にならず、両方が採用される（実測: 撤去を取り込む rebase が衝突なしで成功したのに、
// golden 2 ファイルに撤去済みの文言が残った）。テストは「実装と golden が一致するか」しか見ないので、
// 実装に古い文言を出す経路が残っていれば緑のまま通り、-update で golden を作り直すと残骸は黙って
// 正当化される。だから「実装との一致」とは別に、撤去した ID の残骸だけを見る検査を独立に置く。
//
// # 一覧（removedIDs）に何を載せてよいか
//
//   - 載せるのは機械的な ID だけ。ルールのキー・エラーコード・対訳キー・サブコマンド名・API の経路など、
//     綴りが 1 つに決まっていて、書き手の言い回しで揺れないもの。
//   - 自然文の言い回しは載せない。「ゼロバグゲート」のような説明の語は、撤去の経緯・訳・引用など
//     正当に残る場所が多く、載せると誤検知だらけになって検査ごと信用されなくなる。
//   - 1 行に「いつ・何を撤去したか」を書く（removed）。後から一覧を読む人が、その行を消してよいか
//     （＝撤去が取り消されたのか、単に古くなったのか）を判断できるようにするため。
//   - 社内のイシューの番号は書かない。internal/docscheck は公開物に入るので、経緯はイシュー管理に残し、
//     ここには日付と理由だけを書く（publicscan_test.go の失敗時の案内と同じ扱い）。
//   - 残してよい場所は allow にパス単位で書き、1 つずつ理由を添える。allow は ID ごとなので、
//     ある ID を許した場所でも、ほかの ID は引き続き検査される。
//
// # deploy/public-scan.sh との棲み分け
//
// あちらは「公開物に、社内固有の語・内部の番号・旧名・切れたリンクが無いか」を見る（公開してよいかの検査）。
// こちらは「撤去した ID の残骸が無いか」を見る（仕様が巻き戻っていないかの検査）。目的が違うので、
// 同じ語を両方に書かない。撤去した ID をここへ足すとき、public-scan.sh 側には足さないこと。

// removedID は撤去した ID を 1 つ表す。
type removedID struct {
	id      string            // 残っていてはいけない文字列（そのまま部分一致で探す）
	removed string            // いつ・何を撤去したか（1 行）
	allow   map[string]string // 残してよいパス（repoRoot からの相対）→ その理由
}

// removedIDDesignDoc は撤去の経緯を書く場所。
// 撤去した機能は「いつ・何を・なぜ撤去したか」を仕様書に残すので、そこでは ID に触れざるを得ない。
const removedIDDesignDoc = "docs/server/DESIGN.md"

// removedIDGateDoc は、ゼロバグゲートの撤去の経緯（DESIGN.md §9-1 とそこへの参照）を指す理由。
const removedIDGateDoc = "撤去の経緯を書く場所（§9-1「ゼロバグゲート（撤去済み・2026-09-20）」と、そこを指す参照）"

// removedIDs は撤去した ID の一覧。足すときは上の「一覧に何を載せてよいか」を読むこと。
var removedIDs = []removedID{
	// プロジェクト別ルールのゼロバグゲート（未解決の bug がある間、bug / test 以外の着手を止める機能）を
	// 製品ごと撤去した。サーバ・CLI・MCP・Web・配布ルール・kit・文書のすべてから消えている。
	{id: "zero_bug_gate", removed: "2026-09-20 撤去: プロジェクト別ルールのキー（deploy/rules/*.json）と設定の記録の名前",
		allow: map[string]string{removedIDDesignDoc: removedIDGateDoc}},
	{id: "set_zero_bug_gate", removed: "2026-09-20 撤去: MCP のツール名",
		allow: map[string]string{removedIDDesignDoc: removedIDGateDoc}},
	{id: "zero-bug-gate", removed: "2026-09-20 撤去: CLI のサブコマンド名（looptrack issue zero-bug-gate）と REST の経路",
		allow: map[string]string{removedIDDesignDoc: removedIDGateDoc}},

	// 上と同じ撤去で消えた対訳キー（internal/i18n/{ja,en}.json から 17 件）。
	// 対訳キーは片方の言語にだけ残ると気づかれにくいので、1 件ずつ並べる。
	{id: "cli.cmd.zero_bug_gate", removed: "2026-09-20 撤去: 対訳キー（サブコマンドの説明）"},
	{id: "cli.arg.zero_bug_gate.action", removed: "2026-09-20 撤去: 対訳キー（サブコマンドの引数の説明）"},
	{id: "cli.err.zero_bug_gate_api_only", removed: "2026-09-20 撤去: 対訳キー（エラーの文面）"},
	{id: "cli.err.zero_bug_gate_unsupported", removed: "2026-09-20 撤去: 対訳キー（エラーの文面）"},
	{id: "cli.zero_bug_gate.admin_off", removed: "2026-09-20 撤去: 対訳キー（CLI の出力）"},
	{id: "cli.zero_bug_gate.admin_steps", removed: "2026-09-20 撤去: 対訳キー（CLI の出力）"},
	{id: "cli.zero_bug_gate.disabled", removed: "2026-09-20 撤去: 対訳キー（CLI の出力）"},
	{id: "cli.zero_bug_gate.enabled", removed: "2026-09-20 撤去: 対訳キー（CLI の出力）"},
	{id: "cli.zero_bug_gate.header", removed: "2026-09-20 撤去: 対訳キー（CLI の出力）"},
	{id: "cli.zero_bug_gate.question", removed: "2026-09-20 撤去: 対訳キー（CLI の出力）"},
	{id: "cli.zero_bug_gate.request", removed: "2026-09-20 撤去: 対訳キー（CLI の出力）"},
	{id: "cli.zero_bug_gate.step.head", removed: "2026-09-20 撤去: 対訳キー（導入の次の一歩）"},
	{id: "cli.zero_bug_gate.step.unknown", removed: "2026-09-20 撤去: 対訳キー（導入の次の一歩）"},
	{id: "cli.zero_bug_gate.summary", removed: "2026-09-20 撤去: 対訳キー（機能の要約）"},
	{id: "domain.rules.zero_bug_gate", removed: "2026-09-20 撤去: 対訳キー（ルールが拒否したときの文面）"},
	{id: "domain.rules.zero_bug_gate.create", removed: "2026-09-20 撤去: 対訳キー（ルールが起票を拒否したときの文面）"},
	{id: "service.err.forbidden.zero_bug_gate", removed: "2026-09-20 撤去: エラーコードの対訳キー（403 の文面）"},
}

// removedIDRoots は調べる場所（repoRoot からの相対。ファイルでもディレクトリでもよい）。
//
//   - golden: 期待値そのもの。古い期待値が残ると仕様が巻き戻る（この検査を作った理由）。
//   - i18n の json: 対訳キーが片方の言語にだけ残っても、実装からは呼ばれないので誰も気づかない。
//   - docs・kit: 配る文書。撤去した機能の手順が残ると、読んだ人がその通りに操作して失敗する。
//
// ここに無い場所（パス単位で調べない場所）と、その理由:
//   - CHANGELOG.md: 撤去そのものを記録する場所（「1.0.0 の一覧からゼロバグゲートを外した」と書く）。
//   - Go のソース（internal/ の *.go）: 後方互換のために解釈しない無視用のフィールドを残してあり
//     （domain.Rules の zero_bug_gate）、その動きを確かめるテストも ID を書くのが仕事なので調べない。
//   - private/: 公開物ではない社内の記録（撤去の経緯と引き継ぎを書く場所）。
var removedIDRoots = []string{
	"internal/clitest/testdata/golden",
	"internal/client/kitinit/testdata/golden",
	"internal/client/usagesnap/testdata/golden",
	"internal/i18n/ja.json",
	"internal/i18n/en.json",
	"docs",
	"kit",
}

// removedIDHit は 1 件の残骸。
type removedIDHit struct {
	path string // repoRoot からの相対
	line int    // 1 始まり
	id   string
	note string
}

// removedIDsByLength は一覧を長い順に並べて返す。
// 長い ID（cli.cmd.zero_bug_gate）は短い ID（zero_bug_gate）を含むので、長い方を先に当てて、
// 重なった位置は短い方で二重に数えない（1 行に 1 件だけ、いちばん具体的な ID を出す）。
func removedIDsByLength() []removedID {
	ids := slices.Clone(removedIDs)
	sort.SliceStable(ids, func(i, j int) bool { return len(ids[i].id) > len(ids[j].id) })
	return ids
}

// scanRemovedIDs は 1 行の中の残骸を返す（allow に当たるパスの ID は飛ばす）。
func scanRemovedIDs(ids []removedID, rel, line string, lineNo int) []removedIDHit {
	type span struct{ from, to int }
	var taken []span
	var hits []removedIDHit
	for _, e := range ids {
		if _, ok := e.allow[rel]; ok {
			continue
		}
		for at := 0; ; {
			i := strings.Index(line[at:], e.id)
			if i < 0 {
				break
			}
			from, to := at+i, at+i+len(e.id)
			at = from + 1
			overlap := false
			for _, s := range taken {
				if from < s.to && s.from < to {
					overlap = true
					break
				}
			}
			if overlap {
				continue
			}
			taken = append(taken, span{from, to})
			hits = append(hits, removedIDHit{path: rel, line: lineNo, id: e.id, note: e.removed})
		}
	}
	return hits
}

// removedIDFiles は調べる場所の下のファイルを repoRoot からの相対で返す（中身が本文でないものは外す）。
func removedIDFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, root := range removedIDRoots {
		abs := filepath.Join(repoRoot, root)
		info, err := os.Stat(abs)
		if err != nil {
			t.Fatalf("調べる場所がありません: %s（%v）", root, err)
		}
		if !info.IsDir() {
			out = append(out, root)
			continue
		}
		err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, rerr := filepath.Rel(repoRoot, p)
			if rerr != nil {
				return rerr
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(out)
	return out
}

// TestNoRemovedIDs は、撤去した ID が golden・対訳・文書・kit に残っていないことを確かめる。
//
// golden と実装の一致を見るテスト（./internal/clitest/）とは独立に動く。golden を -update で作り直しても、
// ここは実装の出力ではなく一覧と照らすので緑にならない。
func TestNoRemovedIDs(t *testing.T) {
	ids := removedIDsByLength()
	var hits []removedIDHit
	for _, rel := range removedIDFiles(t) {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue // 本文ではない（画像などの入力）
		}
		for i, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			hits = append(hits, scanRemovedIDs(ids, rel, line, i+1)...)
		}
	}
	for _, h := range hits {
		t.Errorf("%s:%d: 撤去した ID が残っています: %q（%s）", h.path, h.line, h.id, h.note)
	}
	if len(hits) > 0 {
		t.Log("撤去した ID の残骸です。その場所を今の仕様に直すか、" +
			"残してよい場所なら removedids_test.go の allow にパスと理由を足してください")
	}
	t.Logf("撤去した ID %d 件を %d ファイルで調べました", len(removedIDs), len(removedIDFiles(t)))
}

// TestRemovedIDsList は一覧そのものの形を確かめる（一覧が壊れると検査が黙って効かなくなる）。
//
//   - 空でない・ID が重複しない・いつ何を撤去したかが書いてある
//   - allow のパスが実在し、いまもその ID を含む（含まなくなった許可は穴になるだけなので落とす）
func TestRemovedIDsList(t *testing.T) {
	if len(removedIDs) == 0 {
		t.Fatal("一覧が空です（撤去した ID を足すこと）")
	}
	seen := map[string]bool{}
	for _, e := range removedIDs {
		if e.id == "" {
			t.Error("空の ID があります")
			continue
		}
		if seen[e.id] {
			t.Errorf("%q が一覧に 2 回あります", e.id)
		}
		seen[e.id] = true
		if !strings.Contains(e.removed, "撤去") {
			t.Errorf("%q: いつ・何を撤去したかが書かれていません: %q", e.id, e.removed)
		}
		for p, why := range e.allow {
			if why == "" {
				t.Errorf("%q: 許可したパス %s に理由がありません", e.id, p)
			}
			b, err := os.ReadFile(filepath.Join(repoRoot, p))
			if err != nil {
				t.Errorf("%q: 許可したパスがありません: %s（%v）", e.id, p, err)
				continue
			}
			if !strings.Contains(string(b), e.id) {
				t.Errorf("%q: 許可したパス %s にもう現れません（許可を消すこと。残すと検査の穴になります）", e.id, p)
			}
		}
	}
}
