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

	// ── 判定に使う語（訳すと壊れる） ── 8 ファイル・合計 15 件
	{path: "internal/client/hook/core/usage.go", count: 1, reason: jaJudge, note: "createdRe が MCP の応答から ID を拾うための語（「作成: 」）。表示しない"},
	{path: "internal/client/hook/loop/memories.go", count: 2, reason: jaJudge, note: "引き継ぎの本文から節を取り出す語そのもの。`> 要約:` の行（summaryLine）と、「現在地」の節の見出しの判定（currentMarkers）。引き継ぎは書いた人の言語で書かれ、読む側の言語とは無関係なので、読む側の言語で訳すと日本語で書かれた引き継ぎから節を取り出せなくなる（英語の綴りは currentMarkers に並べてある）"},
	{path: "internal/client/report/blocks.go", count: 1, reason: jaJudge, note: "レポートに必ず設ける章の見出しの判定（本文 JSON を書く AI が読む SKILL.md は日本語のまま）"},
	{path: "internal/client/report/pdf/metrics.go", count: 2, reason: jaJudge, note: "フォントが描けるか確かめる文字と、行頭禁則の文字（訳すと組版が壊れる）"},
	{path: "internal/domain/heading.go", count: 2, reason: jaJudge, note: "「受け入れ条件」「検証コマンド」は本文から節を取り出す見出しの綴りそのもの（AcceptanceHeading・VerifyHeading）。訳して置き換えると、日本語で書かれた既存の本文から受け入れ条件・検証コマンドを取り出せなくなる。英語の別名は headingRe が同じ関数の中で足している（DESIGN §9-6）"},
	{path: "internal/domain/leadword.go", count: 3, reason: jaJudge, note: "コメントの先頭語の綴りそのもの（leadWords の表）。SQL（store/feedback.go の LIKE 'フィードバック:%'）もこの綴りで引くので、訳すと未応答のフィードバックの問い合わせが黙って 0 件になる。英語の別名は同じ表に en: true で並べてある"},
	{path: "internal/setupwiz/ask.go", count: 2, reason: jaJudge,
		note: "対話で受け付ける答え「はい」「いいえ」（checkYesNo。訳すと日本語で答えた入力を受けられなくなる）"},
	{path: "internal/store/feedback.go", count: 2, reason: jaJudge,
		note: "comments.content の先頭語「フィードバック:」を LIKE で判定する SQL。DB に入っている本文と突き合わせる語なので、訳すと一致しなくなる"},

	// ── AI しか読まない文 ── 1 ファイル・合計 93 件
	{path: "internal/client/kitinit/texts.go", count: 93, reason: jaAIOnly, note: "kit の rules・skill の本文をそのまま埋め込む（AI 向け・kit 側が原本）"},

	// ── 生成ツール・開発用（配布物に入らない / 訳せない） ── 5 ファイル・合計 96 件
	{path: "internal/client/desktop/icon/gen/main.go", count: 2, reason: jaDevTool, note: "仮アイコンを作る開発用ツール（配布物に入らない）"},
	{path: "internal/i18n/i18n.go", count: 1, reason: jaDevTool, note: "対訳表の埋め込みが壊れたときの panic（i18n 自身なので i18n.T を使えない）"},
	{path: "internal/testutil/mysql.go", count: 3, reason: jaDevTool, note: "テスト用の補助（配布物に入らない）"},
	{path: "internal/tools/notice/main.go", count: 57, reason: jaDevTool, note: "NOTICE を作る開発用ツール（配布物に入らない）"},
	{path: "private/tools/rulegap/main.go", count: 33, reason: jaDevTool, note: "private/ の規律と kit の rules を突き合わせる開発用ツール（private/ は git archive が外すので配布物に入らない）"},
	// ── DB や書き出すファイルに残る記録 ── 1 ファイル・合計 20 件
	{path: "internal/domain/report.go", count: 20, reason: jaRecord, note: "matrix.md・index.md の本文。版管理に入る生成物なので、生成した人の言語で中身が変わると内容の違わない差分が出る。closable.go の警告節（ClosableMarkdown）も同じ文書に並ぶので i18n.JA に固定してある"},

	// ── 未訳（i18n.T へ移す対象。ラチェットの残高。これが減っていくのが正しい方向） ── 41 ファイル・合計 166 件
	{path: "cmd/looptrack/repair.go", count: 1, reason: jaTodo},
	{path: "cmd/looptrack/serve.go", count: 1, reason: jaTodo},
	{path: "internal/auth/password.go", count: 3, reason: jaTodo},
	{path: "internal/auth/secret.go", count: 3, reason: jaTodo},
	{path: "internal/client/api/client.go", count: 3, reason: jaTodo},
	{path: "internal/client/cred/cred.go", count: 5, reason: jaTodo},
	{path: "internal/client/desktop/app.go", count: 5, reason: jaTodo},
	{path: "internal/client/desktop/autostart.go", count: 1, reason: jaTodo},
	{path: "internal/client/desktop/cli.go", count: 2, reason: jaTodo},
	{path: "internal/client/desktop/tray/available_linux.go", count: 1, reason: jaTodo},
	{path: "internal/client/desktop/tray/tray.go", count: 1, reason: jaTodo},
	{path: "internal/client/hook/loop/shlex.go", count: 2, reason: jaTodo},
	{path: "internal/client/hook/loop/taskmode.go", count: 4, reason: jaTodo},
	{path: "internal/client/hook/loop/worktrees.go", count: 1, reason: jaTodo},
	{path: "internal/client/jsonorder/jsonorder.go", count: 5, reason: jaTodo},
	{path: "internal/client/kitinit/init.go", count: 1, reason: jaTodo},
	{path: "internal/client/kitinit/loopkit.go", count: 5, reason: jaTodo},
	{path: "internal/client/proc/proc.go", count: 1, reason: jaTodo},
	{path: "internal/client/usagesnap/copilot.go", count: 7, reason: jaTodo},
	{path: "internal/client/usagesnap/legacycompat.go", count: 4, reason: jaTodo},
	{path: "internal/client/verify/plan.go", count: 3, reason: jaTodo},
	{path: "internal/client/verify/run.go", count: 2, reason: jaTodo},
	{path: "internal/client/verify/run_windows.go", count: 4, reason: jaTodo},
	{path: "internal/client/verify/shellrule.go", count: 2, reason: jaTodo},
	{path: "internal/hookio/event.go", count: 2, reason: jaTodo},
	{path: "internal/hookio/result.go", count: 1, reason: jaTodo},
	{path: "internal/hookio/run.go", count: 4, reason: jaTodo},
	{path: "internal/localserve/localserve.go", count: 9, reason: jaTodo},
	{path: "internal/mdformat/mdformat.go", count: 6, reason: jaTodo},
	{path: "internal/privfile/privfile.go", count: 2, reason: jaTodo},
	{path: "internal/server/mcp.go", count: 2, reason: jaTodo},
	{path: "internal/server/mcp_verify.go", count: 3, reason: jaTodo},
	{path: "internal/server/setup.go", count: 16, reason: jaTodo},
	{path: "internal/service/assignee.go", count: 10, reason: jaTodo},
	{path: "internal/service/membership.go", count: 13, reason: jaTodo},
	{path: "internal/service/repair.go", count: 2, reason: jaTodo},
	{path: "internal/service/usage_send_prompts.go", count: 6, reason: jaTodo},
	{path: "internal/service/verify.go", count: 7, reason: jaTodo},
	{path: "internal/setupwiz/files.go", count: 5, reason: jaTodo},
	{path: "internal/setupwiz/setupwiz.go", count: 1, reason: jaTodo},
	{path: "internal/transfer/transfer.go", count: 10, reason: jaTodo},

	// 合計 56 ファイル・390 件（うち未訳 166 件）
}
