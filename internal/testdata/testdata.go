// Package testdata は旧形式（ファイルモードの Markdown イシュー）の合成フィクスチャをテストに渡す。
//
// フィクスチャは fixtures/ にコミットしてある（以前の生成器の出力。内容はすべて架空。生成の仕掛けは消した）。
// 直下に <slug>/config.json・counter・open/・closed/ を持つ 3 プロジェクト（demo・sample・mini）:
//
//   - demo（DEMO・幅 4）: 全状態・全型・親子 / blocked_by / traces / refs・コメント・クローズ済み・旧データで見つかった形
//   - sample（SMP-DEV・幅 4）: ハイフンを含む接頭辞・未知の frontmatter キー（origin）・空白を含むラベル・In Review が無い
//   - mini（MINI・幅 3）: config.json が prefix / width だけ・コメント節の無いファイル
//
// 以前は git タグ archive/file-mode から社内の実データを展開していたが、公開版に実データを持ち込まないため
// 合成フィクスチャに置き換えた。タグや LOOPTRACK_DATA_ROOT が無くてもテストは省略されずに走る。
//
// フィクスチャ自体の検査（生成物の一致・社内の名前が無いこと・形の網羅）は internal/transfer/fixtures_test.go
// （go test ./... は testdata という名前のディレクトリを対象にしない）。
//
// テストはこのディレクトリを読むだけにし、書き込むとき（ファイルモードの再現等）は一時ディレクトリへ写してから使う。
package testdata

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Slugs はフィクスチャのプロジェクト（ディレクトリ名の昇順）。
var Slugs = []string{"demo", "mini", "sample"}

// Root は合成フィクスチャのルート（直下に <slug>/config.json・open/・closed/）を返す。無ければテストを失敗させる。
func Root(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "fixtures")
	if _, err := os.Stat(filepath.Join(root, Slugs[0], "config.json")); err != nil {
		t.Fatalf("合成フィクスチャがありません（%s）: %v", root, err)
	}
	return root
}

// FileModeEnv は IM_*・LOOPTRACK_* と AI のセッションの変数を除いた環境変数を返す。CLI（looptrack）を子プロセスで動かすテストは
// 必ずこれを使う（名前は以前のファイルモードのテストの名残り）。
//
// CLI は LOOPTRACK_API_URL があるとイシュー管理サーバへ接続し、利用者の資格情報で本番に書き込む。Claude Code のセッションは
// .claude/settings.json の env で LOOPTRACK_API_URL を持つため、os.Environ() をそのまま渡すとテストが本番にイシューを作る
// （2026-09-17 に作られたもの）。
func FileModeEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		// IM_*・LOOPTRACK_*（API モードの設定）と、テストを回している Claude Code・Codex 自身のセッション（トークン計測が拾ってしまう）は渡さない
		if strings.HasPrefix(kv, "IM_") || strings.HasPrefix(kv, "LOOPTRACK_") || strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=") ||
			strings.HasPrefix(kv, "CLAUDE_SESSION_ID=") || strings.HasPrefix(kv, "CLAUDECODE=") ||
			strings.HasPrefix(kv, "CODEX_THREAD_ID=") || strings.HasPrefix(kv, "CODEX_SESSION_ID=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}
