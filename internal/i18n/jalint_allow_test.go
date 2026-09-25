package i18n

// 日本語の文字列リテラルの許可一覧（jalint_test.go のラチェットの土台）。
//
// 1 行 = 1 ファイル。count は「いまの件数」であって「上限の目標」ではない。
// 増えても減っても TestJapaneseLiteralsDoNotIncrease が失敗する（実測と一致させる）。
// path は os.Stat で実在を確かめる（打ち間違いのパスは黙って 0 件を返し、ラチェットが鳴らないため）。
//
// reason の意味は jalint_test.go の jaReason を見ること。jaTodo 以外には note（なぜ訳さないか）を必ず書く。
// 件数の作り直し: この一覧を空にして go test ./internal/i18n/ -run TestJapaneseLiteralsDoNotIncrease を走らせると、
// 失敗メッセージにファイルごとの実測値が全部出る（節と末尾の合計の行も TestAllowListTotals が実測と突き合わせる）。
var jaAllowed = []jaAllow{

	// ── 判定に使う語（訳すと壊れる） ── 13 ファイル・合計 26 件
	{path: "internal/client/hook/core/usage.go", count: 1, reason: jaJudge, note: "createdRe が MCP の応答から ID を拾うための語（「作成: 」）。表示しない"},
	{path: "internal/client/hook/loop/memories.go", count: 2, reason: jaJudge, note: "引き継ぎの本文から節を取り出す語そのもの。`> 要約:` の行（summaryLine）と、「現在地」の節の見出しの判定（currentMarkers）。引き継ぎは書いた人の言語で書かれ、読む側の言語とは無関係なので、読む側の言語で訳すと日本語で書かれた引き継ぎから節を取り出せなくなる（英語の綴りは currentMarkers に並べてある）"},
	{path: "internal/client/hook/loop/taskmode.go", count: 4, reason: jaJudge, note: "タスクモードを決める日本語の語の正規表現（execWords・investWords）。利用者の指示文と突き合わせる語そのもので、指示文は書いた人の言語で来るので訳さない（英語の別名は execWordsEn・investWordsEn に並べてある）"},
	{path: "internal/client/kitinit/init.go", count: 1, reason: jaJudge, note: "loop を入れるかの問いに受け付ける答え「はい」（y / yes と並べて判定する。訳すと日本語で答えた入力を受けられなくなる）"},
	{path: "internal/client/report/blocks.go", count: 1, reason: jaJudge, note: "レポートに必ず設ける章の見出しの判定（本文 JSON を書く AI が読む SKILL.md は日本語のまま）"},
	{path: "internal/client/report/pdf/metrics.go", count: 2, reason: jaJudge, note: "フォントが描けるか確かめる文字と、行頭禁則の文字（訳すと組版が壊れる）"},
	{path: "internal/client/usagesnap/legacycompat.go", count: 4, reason: jaJudge, note: "会話記録の本文と突き合わせる語の正規表現（autoRE の利用制限の文面・excludeBodyRE の「このセッションはレポートの対象外」と、その前置きの「本」「この」）。記録は書かれた言語のまま読むので訳さない"},
	{path: "internal/domain/heading.go", count: 2, reason: jaJudge, note: "「受け入れ条件」「検証コマンド」は本文から節を取り出す見出しの綴りそのもの（AcceptanceHeading・VerifyHeading）。訳して置き換えると、日本語で書かれた既存の本文から受け入れ条件・検証コマンドを取り出せなくなる。英語の別名は headingRe が同じ関数の中で足している（DESIGN §9-6）"},
	{path: "internal/domain/leadword.go", count: 3, reason: jaJudge, note: "コメントの先頭語の綴りそのもの（leadWords の表）。SQL（store/feedback.go の LIKE 'フィードバック:%'）もこの綴りで引くので、訳すと未応答のフィードバックの問い合わせが黙って 0 件になる。英語の別名は同じ表に en: true で並べてある"},
	{path: "internal/hookio/result.go", count: 1, reason: jaJudge, note: "Copilot の Stop の差し戻しの理由に付ける印（CopilotStopMarker）。次の利用者のメッセージとして戻ってきたときに task-mode・session-scope-guard がこの印で見分ける（以前の kit の bash 版と同じ綴り）。訳すと見分けられなくなる"},
	{path: "internal/mdformat/mdformat.go", count: 1, reason: jaJudge, note: "コメント節の見出し「## コメント」（CommentSection）。本文を節に分ける判定に使う綴りそのもので、DB に入っている本文と突き合わせるので、訳すと既存の本文からコメントを取り出せなくなる"},
	{path: "internal/setupwiz/ask.go", count: 2, reason: jaJudge,
		note: "対話で受け付ける答え「はい」「いいえ」（checkYesNo。訳すと日本語で答えた入力を受けられなくなる）"},
	{path: "internal/store/feedback.go", count: 2, reason: jaJudge,
		note: "comments.content の先頭語「フィードバック:」を LIKE で判定する SQL。DB に入っている本文と突き合わせる語なので、訳すと一致しなくなる"},

	// ── AI しか読まない文 ── 3 ファイル・合計 98 件
	{path: "internal/client/kitinit/texts.go", count: 93, reason: jaAIOnly, note: "kit の rules・skill の本文をそのまま埋め込む（AI 向け・kit 側が原本）"},
	{path: "internal/server/mcp.go", count: 2, reason: jaAIOnly, note: "MCP の instructions と prompt「review」の本文の日本語の正本（mcpInstructionsJA・reviewPromptText）。英語版は同じファイルの mcpInstructionsEN・reviewPromptTextEN で、接続の言語で選ぶ。DESIGN.md の本文と突き合わせるテストと、案内文の目印の検査（aiguide_marker_test の usageAttachGuides）が Go の定数を見るので、対訳表ではなく定数の組で持つ"},
	{path: "internal/server/setup.go", count: 3, reason: jaAIOnly, note: "prompt「loop」（最小・/iterate）と「setup」の本文の日本語の正本（loopPromptText・loopIteratePromptText・setupPromptText）。英語版は同じファイルの …EN の定数で、接続の言語で選ぶ（promptText）。DESIGN.md の本文と突き合わせるテストが日本語の定数を見るので、対訳表ではなく定数の組で持つ。項目の並びの一致は TestPromptTextsHaveEnglishPairs が確かめる"},

	// ── 生成ツール・開発用（配布物に入らない / 訳せない） ── 5 ファイル・合計 96 件
	{path: "internal/client/desktop/icon/gen/main.go", count: 2, reason: jaDevTool, note: "仮アイコンを作る開発用ツール（配布物に入らない）"},
	{path: "internal/i18n/i18n.go", count: 1, reason: jaDevTool, note: "対訳表の埋め込みが壊れたときの panic（i18n 自身なので i18n.T を使えない）"},
	{path: "internal/testutil/mysql.go", count: 3, reason: jaDevTool, note: "テスト用の補助（配布物に入らない）"},
	{path: "internal/tools/notice/main.go", count: 57, reason: jaDevTool, note: "NOTICE を作る開発用ツール（配布物に入らない）"},
	{path: "private/tools/rulegap/main.go", count: 33, reason: jaDevTool, note: "private/ の規律と kit の rules を突き合わせる開発用ツール（private/ は git archive が外すので配布物に入らない）"},
	// ── DB や書き出すファイルに残る記録 ── 4 ファイル・合計 30 件
	{path: "cmd/looptrack/repair.go", count: 1, reason: jaRecord, note: "repair-lists の --reason の既定値。issue_events に残る記録なので、動かした人の言語で変えない（コードの注釈にも同じ理由を書いてある）"},
	{path: "internal/domain/report.go", count: 20, reason: jaRecord, note: "matrix.md・index.md の本文。版管理に入る生成物なので、生成した人の言語で中身が変わると内容の違わない差分が出る。closable.go の警告節（ClosableMarkdown）も同じ文書に並ぶので i18n.JA に固定してある"},
	{path: "internal/service/membership.go", count: 2, reason: jaRecord, note: "担当の付け替えの理由（ReasonMemberRemoved・ReasonMemberViewer）。assign イベントの reason として issue_events に残る記録なので、動かした人の言語で変えない"},
	{path: "internal/service/verify.go", count: 7, reason: jaRecord, note: "verify の記録としてイシューに追記するコメントの文面（verifyComment と、その中の印 SelfReportedLabel・CachedLabel）。コメントは DB に残る記録なので、記録した人の言語で変えない。画面・MCP・summary に出す印は対訳の ID（service.verify.last.self_reported・…cached）で出す"},

	// ── 未訳（i18n.T へ移す対象。ラチェットの残高。これが減っていくのが正しい方向） ── 0 ファイル・合計 0 件

	// 合計 25 ファイル・250 件（うち未訳 0 件）
}
