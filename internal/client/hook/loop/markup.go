package loop

// stop-tool-markup-guard（kit/loop/hooks/stop-tool-markup-guard.sh の Go 版）: ツール呼び出しの書式ミス（未パースの markup）が
// 残った返答を差し戻す。
//
// 会話記録の最後の assistant メッセージに tool_use が 1 件も無く、テキストに素の invoke / parameter の開始タグ（または先頭の
// count 等の 1 語の直後の invoke）が残っていれば、Stop を差し戻して正しい書式での再送を促す。`<` を伴わない言及は通す。
// 差し戻しの理由文にも `<` の形を入れない（自己再発火しない）。

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 「<」は正規表現の文字クラスで書く（このファイルを読んだ AI の出力に素のタグの形が紛れないように）。
var markupRes = []interface{ MatchString(string) bool }{
	compatRe(`(?i)[<]\s*(?:antml:)?invoke\s+name\s*=`),
	compatRe(`(?i)[<]\s*(?:antml:)?parameter\s+name\s*=`),
	compatRe(`(?im)^\s*[A-Za-z]{1,12}\s*\r?\n\s*[<]\s*(?:antml:)?invoke\b`),
}

// StopToolMarkupGuard は未パースの markup が残った返答を差し戻す。
func StopToolMarkupGuard(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	if ev.StopHookActive {
		return hookio.Result{}, nil
	}
	path := rawTranscript(ev)
	if path == "" || !isFile(path) {
		return hookio.Result{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return hookio.Result{}, nil
	}
	var last map[string]any
	for _, line := range textLines(decodeReplace(b)) {
		if !strings.Contains(line, `"assistant"`) {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) != nil {
			continue
		}
		if obj["type"] == "assistant" {
			last = obj
		}
	}
	if last == nil {
		return hookio.Result{}, nil
	}
	msg, _ := last["message"].(map[string]any)
	var content []any
	switch c := msg["content"].(type) {
	case string:
		if c != "" {
			content = []any{map[string]any{"type": "text", "text": c}}
		}
	case []any:
		content = c
	}
	var texts []string
	for _, blk := range content {
		m, ok := blk.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "tool_use":
			return hookio.Result{}, nil
		case "text":
			t, _ := m["text"].(string)
			texts = append(texts, t)
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" {
		return hookio.Result{}, nil
	}
	for _, rx := range markupRes {
		if rx.MatchString(text) {
			return hookio.Result{Block: i18n.T(envFrom(ctx).lang(), "loop.markup.reason")}, nil
		}
	}
	return hookio.Result{}, nil
}
