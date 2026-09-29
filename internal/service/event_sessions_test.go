package service

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// ToolUseHash は検査を通る ID だけをハッシュにし、生の値を含まない 32 字を返す。検査に落ちる値は捨てる（対照: 通る値）。
func TestToolUseHash(t *testing.T) {
	h, ok := ToolUseHash("toolu_01AbC-xyz")
	if !ok || len(h) != 32 || strings.Contains(h, "toolu") {
		t.Fatalf("検査を通る値（対照）: %q %v", h, ok)
	}
	if h2, _ := ToolUseHash("toolu_01AbC-xyz"); h2 != h {
		t.Errorf("同じ値が同じハッシュにならない: %q %q", h, h2)
	}
	if h3, _ := ToolUseHash(strings.Repeat("a", 128)); h3 == "" {
		t.Errorf("128 字は受け付ける")
	}
	for _, bad := range []string{"", strings.Repeat("a", 129), "a b", "a;b", "ａ", "a/b", "a\nb"} {
		if h, ok := ToolUseHash(bad); ok || h != "" {
			t.Errorf("%q を受け付けた: %q", bad, h)
		}
	}
}

// StarterSession は、会話のセッション ID を名乗る見る側にだけ結んだ値を返す。器の ID（host）・MCP の接続 ID・
// セッション ID なしの見る側には記録した値を返す（結んだ値とは比べない）。対照: 結びが無ければ誰に対しても記録した値。
func TestStarterSession(t *testing.T) {
	linked := store.Starter{SessionID: MCPSessionPrefix + "c1", LinkedSessionID: "s-A"}
	plain := store.Starter{SessionID: MCPSessionPrefix + "c1"}
	cases := []struct {
		name   string
		viewer Actor
		st     store.Starter
		want   string
	}{
		{"会話のセッション ID（結んだ値を使う）", Actor{SessionID: "s-A"}, linked, "s-A"},
		{"器の ID（比べない）", Actor{SessionID: "s-A", SessionKind: SessionKindHost}, linked, MCPSessionPrefix + "c1"},
		{"MCP の接続 ID（記録した値のまま）", Actor{SessionID: MCPSessionPrefix + "c1"}, linked, MCPSessionPrefix + "c1"},
		{"セッション ID なし", Actor{}, linked, MCPSessionPrefix + "c1"},
		{"結び無し", Actor{SessionID: "s-A"}, plain, MCPSessionPrefix + "c1"},
	}
	for _, c := range cases {
		if got := StarterSession(c.viewer, c.st); got != c.want {
			t.Errorf("%s: %q（%q のはず）", c.name, got, c.want)
		}
	}
}
