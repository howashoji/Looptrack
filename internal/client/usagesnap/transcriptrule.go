package usagesnap

// 会話記録の自動送信（DESIGN.md §9-8「hook」）の 2 つの鍵のうち、クライアントが覚えるもの。
//   - プロジェクト別ルール session.transcripts の値: 登録とチャンクの追記の応答の transcripts_enabled から覚える
//     （<looptrack の置き場>/session-transcripts/<ProjectKey の鍵>。中身は "1" / "0"、覚えた時刻はファイルの更新時刻）。
//   - 端末ごとのオプトイン LOOPTRACK_SESSION_TRANSCRIPTS（"1" で入。既定は切）。
//
// 手で打つ looptrack session transcript push はオプトインを見ない（打つこと自体が本人の同意なので）。読むのは hook。

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
)

const transcriptsRuleDir = "session-transcripts"

// transcriptsRuleFile は覚えた値のファイル。API の URL かプロジェクトが無ければ ""。
func transcriptsRuleFile(o Options) string {
	base, _ := o.Env.Setting(env.APIURL)
	project, _ := o.Env.Setting(env.Project)
	key := ProjectKey(base, project)
	if key == "" {
		return ""
	}
	return filepath.Join(o.configDir(), transcriptsRuleDir, key)
}

// RememberTranscriptsRule はプロジェクト別ルール session.transcripts の値を覚える（書けなくてもエラーにしない）。
func RememberTranscriptsRule(enabled bool, o Options) {
	p := transcriptsRuleFile(o)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	content := "0"
	if enabled {
		content = "1"
	}
	_ = os.WriteFile(p, []byte(content), 0o644)
}

// TranscriptsRule は覚えたルールの値と、覚えた時刻。覚えた値が無ければ ok が false。
func TranscriptsRule(o Options) (enabled bool, at time.Time, ok bool) {
	p := transcriptsRuleFile(o)
	if p == "" {
		return false, time.Time{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false, time.Time{}, false
	}
	st, err := os.Stat(p)
	if err != nil {
		return false, time.Time{}, false
	}
	switch strings.TrimSpace(string(b)) {
	case "1":
		return true, st.ModTime(), true
	case "0":
		return false, st.ModTime(), true
	}
	return false, time.Time{}, false
}

// TranscriptsOptIn は端末ごとのオプトイン（LOOPTRACK_SESSION_TRANSCRIPTS が前後の空白を除いて "1"）。
func TranscriptsOptIn(e env.Env) bool {
	return strings.TrimSpace(e.Get(env.Name(env.SessionTranscripts))) == "1"
}
