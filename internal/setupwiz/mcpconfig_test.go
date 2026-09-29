package setupwiz

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// MCPConfigs が配る接続設定に、セッション系のヘッダ（X-Looptrack-Session・X-Looptrack-Session-Kind）を入れないことを固定する。
//
// 接続設定の headers に書いた ${CLAUDE_CODE_SESSION_ID} は Claude Code が展開して送るが、Claude Code の本体は
// 自分のセッション ID を環境に持たず、展開されるのは起動元から継承した値（端末からなら空、入れ子なら親の ID）になる。
// 入れると別のセッションの ID を名乗ることになる。器の印（Session-Kind: host）も要らない。MCP の操作の付与は
// PostToolUse の hook が入力の session_id と transcript_path で行い、器の窓からの操作も付与されるので、
// 印を付けると付与できている操作まで対象から外してしまう（docs/server/DESIGN.md §9-5「器のセッション ID」）。
//
// 対照として X-Looptrack-Project が入っていることを同じテストで確かめる。対照が無いと、Text を 1 つも
// 見ていなくても（ヘッダの書き方が変わって文字列で当たらなくなっても）このテストは通ってしまう。
func TestMCPConfigsOmitSessionHeaders(t *testing.T) {
	// 小文字で比べる。"x-looptrack-session" は X-Looptrack-Session-Kind の頭でもあるが、どちらに当たったかを出すため両方を並べる。
	forbidden := []string{"x-looptrack-session-kind", "x-looptrack-session"}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		cfgs := MCPConfigs(lang, "http://127.0.0.1:8090/looptrack", "demo")
		if len(cfgs) == 0 {
			t.Fatalf("前提が崩れています: MCPConfigs(%s) が接続設定を 1 つも返しません", lang)
		}
		for _, c := range cfgs {
			text := strings.ToLower(c.Text) // HTTP のヘッダ名は大小を区別しない
			// 対照: 既定のプロジェクトのヘッダは入っている（ヘッダを実際に見ていることの確かめ）。
			if !strings.Contains(c.Text, "X-Looptrack-Project") || !strings.Contains(c.Text, "demo") {
				t.Errorf("前提が崩れています: %s（%s）の接続設定に X-Looptrack-Project: demo が見当たりません。"+
					"ヘッダの書き方が変わったなら、このテストの見方も合わせて直してください:\n%s", c.Client, c.Where, c.Text)
			}
			for _, f := range forbidden {
				if strings.Contains(text, f) {
					t.Errorf("%s（%s）の接続設定にセッション系のヘッダ（%q）が入っています。"+
						"接続設定のヘッダでは会話ごとのセッション ID を送れません（DESIGN.md §9-5「器のセッション ID」）:\n%s",
						c.Client, c.Where, f, c.Text)
				}
			}
		}
	}
}
