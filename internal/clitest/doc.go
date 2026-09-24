// Package clitest は CLI（looptrack issue）の golden テストの仕掛け。
// golden は以前の CLI の出力から作り、Go 版に移した後は Go 版の出力を正とする（以前の実装は撤去済み）。
//
// 仕組み:
//
//   - 偽 API（FakeAPI）: httptest のサーバがケースごとのフィクスチャの応答を返し、受けた要求
//     （method・path・クエリ・本文・Authorization / Content-Type / If-Match / X-Looptrack-* ヘッダ）を記録する。
//   - 実行（Run）: 一時ディレクトリの中に作業ディレクトリ（ws。CLI のカレント）と HOME（home。資格情報の置き場
//     XDG_CONFIG_HOME=home/.config）を作り、CLI を子プロセスで動かす。子プロセスの環境変数は利用者の環境を引き継がず、
//     PATH だけを持ち込んで残りを明示的に組み立てる（ChildEnv）。LOOPTRACK_API_URL は必ず偽 API を指し、IM_*・LOOPTRACK_*・CLAUDE_*・CODEX_*・
//     COPILOT_* は持ち込まない（テストが本番に書き込むのを防ぐ。2026-09-17 に実際に起きたことの再発防止）。
//   - golden: 終了コード・stdout・stderr・要求の列・書いたファイル（ws と home の中）を正規化（Normalizer）して
//     1 つのテキストにし、testdata/golden/<ケース名>.golden と比べる。
//   - 正規化: 一時パス → $TMP、偽 API の URL → $API、ほかの 127.0.0.1 のポート → $PORT、実行中の現在時刻 → $NOW、
//     経過時間 → $SEC、ホスト名 → $HOST、乱数（OAuth の state・PKCE）→ $STATE / $PKCE_*、配布物のハッシュ → $SHA256。
//     クエリ・フォーム・JSON の本文と書いたファイルの JSON はキーの順をそろえる（意味の無い順序の違いを落とす）。
//     規則そのもののテストは normalize_test.go。
//
// 実行する実装: "looptrack issue …"。実行ファイルは環境変数 LOOPTRACK_BIN、無ければテストの中で 1 回だけ go build する。
// golden の記録（-update）も Go 版の出力から作る。
//
// golden の更新: go test ./internal/clitest -update（または CLITEST_UPDATE=1）。
//
// ファイルの分け方: 仕掛け（harness_*_test.go）もケース（cases・special・init_cases・fixtures）もすべて _test.go に置く。
// 仕掛けは子プロセスを起動する（os/exec）ので、本番のバイナリに入り得る形にしない（internal/server の
// TestServerNeverExecutes が internal/ の _test.go 以外の os/exec を禁じている）。Go 版の CLI はこのパッケージを import せず、
// 実行ファイルとして流す。
package clitest
