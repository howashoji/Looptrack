package i18n

// 実行ファイルに埋め込まれて配られる .md の、日本語を含む行数の許可一覧
// （jamd_test.go のラチェットの土台）。
//
// 1 行 = 1 ファイル。lines は「いまの行数」であって「上限の目標」ではない。
// 増えても減っても TestJapaneseMarkdownDoesNotIncrease が失敗する（実測と一致させる）。
// path は os.Stat で実在を確かめる（打ち間違いのパスは黙って 0 行を返し、ラチェットが鳴らないため）。
//
// **対象は列挙ではなく //go:embed の走査で決まる**ので、この一覧は走査の結果を写したもの。
// 埋め込みのディレクトリに .md を足せば、この一覧に無いファイルとして必ず落ちる。
//
// note は全行に必ず書く（なぜこの日本語が配布物に入ってよいのか）。
// 行数の作り直し: この一覧を空にして go test ./internal/i18n/ -run TestJapaneseMarkdownDoesNotIncrease を
// 走らせると、失敗メッセージにファイルごとの実測値と、どの //go:embed から来たかが全部出る
// （節と末尾の合計の行も TestMDAllowListTotals が実測と突き合わせる）。
var jaMDAllowed = []jaMDAllow{

	// ── AI が読む共通規則 ── 1 ファイル・合計 47 行
	{path: "internal/guide/common.md", lines: 47, reason: jaAIOnly, note: "guide の共通規則の本文。REST の guide・CLI の issue guide・MCP の guide の 3 経路で AI に届く（読むのは AI だけ）"},

	// ── kit が配る rules・skill の本文（日本語が正本。en/ に対訳がある） ── 9 ファイル・合計 857 行
	{path: "kit/core/skills/issue/SKILL.md", lines: 72, reason: jaAIOnly, note: "kit が配る skill の本文（AI が読む手順）"},
	{path: "kit/core/skills/token-report/SKILL.md", lines: 97, reason: jaAIOnly, note: "kit が配る skill の本文（AI が読む手順）"},
	{path: "kit/loop/rules/background-process.md", lines: 51, reason: jaAIOnly, note: "kit が配る rules の本文（AI が読む行動規律）"},
	{path: "kit/loop/rules/iteration-discipline.md", lines: 34, reason: jaAIOnly, note: "kit が配る rules の本文（AI が読む行動規律）"},
	{path: "kit/loop/rules/output-discipline.md", lines: 42, reason: jaAIOnly, note: "kit が配る rules の本文（AI が読む行動規律）"},
	{path: "kit/loop/rules/secrets-discipline.md", lines: 195, reason: jaAIOnly, note: "kit が配る rules の本文（AI が読む行動規律）"},
	{path: "kit/loop/rules/working-discipline.md", lines: 226, reason: jaAIOnly, note: "kit が配る rules の本文（AI が読む行動規律）"},
	{path: "kit/loop/skills/iterate/SKILL.md", lines: 38, reason: jaAIOnly, note: "kit が配る skill の本文（AI が読む手順）"},
	{path: "kit/loop/skills/session-handoff/SKILL.md", lines: 102, reason: jaAIOnly, note: "kit が配る skill の本文（AI が読む手順）"},

	// ── 英訳の中に残る日本語（綴りそのものを引用している行） ── 4 ファイル・合計 17 行
	{path: "kit/core/skills/issue/en/SKILL.md", lines: 1, reason: jaJudge, note: "本文から節を取り出す見出しの綴り「## 内容」の引用。訳すと、その見出しを書けと言えなくなる"},
	{path: "kit/core/skills/token-report/en/SKILL.md", lines: 9, reason: jaJudge, note: "保存先のディレクトリ名とファイル名（トークンレポート…）の綴り。訳すと、実際に作られる名前と食い違う"},
	{path: "kit/loop/rules/en/working-discipline.md", lines: 1, reason: jaJudge, note: "タスクモードの判定に使う語「確認して」の引用（判定はこの綴りで行う）"},
	{path: "kit/loop/skills/session-handoff/en/SKILL.md", lines: 6, reason: jaJudge, note: "引き継ぎの本文から節を取り出す綴り（`> 要約:`・「現在地」）。訳すと日本語で書かれた引き継ぎを読めなくなる"},

	// ── kit 自体の説明（//go:embed * が拾うが、kit.Names が配布から除く） ── 2 ファイル・合計 186 行
	{path: "kit/README.ja.md", lines: 185, reason: jaDevTool, note: "kit の置き場の説明（何をどのプロジェクトへ配るか）。//go:embed * が実行ファイルに入れるが、配るのは core/ と loop/ の下だけ（kit.Names）なので導入先には出ない"},
	{path: "kit/README.md", lines: 1, reason: jaDevTool, note: "同上。日本語版への案内の 1 行（日英 2 本立ての対の片方）"},

	// 合計 16 ファイル・1107 行
}
