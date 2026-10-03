package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/guide"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 担当者の規則（DESIGN.md §9-2「AI への案内」）が guide（common.md）と MCP の instructions に入っていて、
// 設計の文面と一字一句同じ。guide ツール・initialize の instructions からも読める。
func TestAssigneeGuideMatchesDesign(t *testing.T) {
	q := aiGuideQuotes(t)
	if !strings.Contains(guide.Common(i18n.JA), "\n### 担当者（重複作業の防止）\n\n"+q[0]+"\n") {
		t.Errorf("common.md に担当者の段落が無い（DESIGN §9-2 と違う）:\n%s", q[0])
	}
	if !strings.Contains(mcpInstructions(i18n.JA), "\n"+q[1]+"\n") {
		t.Errorf("instructions に担当者の 1 行が無い（DESIGN §9-2 と違う）:\n%s", q[1])
	}
	for _, want := range []string{"In Progress にしない", "--override \"理由\"", "override_reason", "担当を替えずに", "自分が担当か未設定"} {
		if !strings.Contains(q[0], want) {
			t.Errorf("guide の段落に %q が無い", want)
		}
	}

	// 経路: guide ツール（REST・CLI と同じ Markdown）と MCP の initialize の instructions
	e, _, ed := newAPIEnv(t)
	g := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "req"})
	if text, _ := g.call("guide", map[string]any{}, false); !strings.Contains(text, q[0]) {
		t.Error("guide ツールの出力に担当者の段落が無い")
	}
	m := e.mcpAsClient(ed.token, map[string]string{"X-Looptrack-Project": "req"}, "claude-code", "1", "2025-06-18")
	if ins := m.cs.InitializeResult().Instructions; !strings.Contains(ins, q[1]) {
		t.Error("initialize の instructions に担当者の 1 行が無い")
	}
}

// aiGuideQuotes は DESIGN.md §9-2「AI への案内」の引用を順に返す。
// 並びは 担当者の guide の段落・担当者の instructions の 1 行・エビデンスの guide の本文・エビデンスの instructions の 1 行。
func aiGuideQuotes(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "server", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "#### AI への案内（guide・MCP instructions）")
	if i < 0 {
		t.Fatal("DESIGN.md §9-2 に「AI への案内」の節が無い")
	}
	s = s[i:]
	if j := strings.Index(s[1:], "\n#### "); j >= 0 {
		s = s[:j+1]
	}
	q := quoteBlocks(s)
	if len(q) != 4 {
		t.Fatalf("§9-2「AI への案内」の引用が 4 つでない: %q", q)
	}
	return q
}

// エビデンスの規則（DESIGN.md §9-2「AI への案内」）が guide（common.md）と MCP の instructions に入っていて、
// 設計の文面と一字一句同じ。英語版は同じ規則の対訳で、コマンド・引数・キーの名前を持つ（DB は使わない）。
func TestEvidenceGuideMatchesDesign(t *testing.T) {
	q := aiGuideQuotes(t)
	if !strings.Contains(guide.Common(i18n.JA), "\n### エビデンス（検証の証跡を添付する）\n\n"+q[2]+"\n") {
		t.Errorf("common.md にエビデンスの節が無い（DESIGN §9-2 と違う）:\n%s", q[2])
	}
	if !strings.Contains(mcpInstructions(i18n.JA), "\n"+q[3]+"\n") {
		t.Errorf("instructions にエビデンスの 1 行が無い（DESIGN §9-2 と違う）:\n%s", q[3])
	}
	names := []string{"looptrack issue attach <ID>", "--attach-output", "report_verify", "attachments", "verify.require_evidence"}
	mcpNames := []string{"looptrack issue attach <ID>", "report_verify", "attachments", "verify.require_evidence"} // instructions は 1 行なので --attach-output までは書かない
	for _, c := range []struct {
		name, text string
		want       []string
	}{
		// 対象は「本文に検証コマンドが 1 つ以上あるイシュー」（判定の domain.VerifyCheck.HasCommands と同じ。節があってもコマンドが 0 件なら対象外）
		{"DESIGN の guide の本文", q[2], append([]string{"looptrack issue verify <ID> --attach <ファイル>", "override_reason", "スクリーンショット", "本文に検証コマンドが 1 つ以上あるイシュー"}, names...)},
		{"DESIGN の instructions の 1 行", q[3], mcpNames},
		{"en/common.md", guide.Common(i18n.EN), append([]string{"### Evidence (attaching proof of verification)", "looptrack issue verify <ID> --attach <file>", "override_reason", "holds at least one verify command"}, names...)},
		{"mcpInstructionsEN", mcpInstructions(i18n.EN), append([]string{"\nEvidence: "}, mcpNames...)},
		// next の text は verifyText を通らないので、close の段の文面に同じ指示を入れる
		{"next の close の段（日本語）", i18n.T(i18n.JA, "server.api.next.step_close", "id", "ABC-0001"), []string{"looptrack issue verify ABC-0001 --attach-output", "--attach <ファイル>", "looptrack issue attach ABC-0001 <ファイル>", "report_verify", "attachments", "looptrack issue close ABC-0001"}},
		{"next の close の段（英語）", i18n.T(i18n.EN, "server.api.next.step_close", "id", "ABC-0001"), []string{"looptrack issue verify ABC-0001 --attach-output", "--attach <file>", "looptrack issue attach ABC-0001 <file>", "report_verify", "attachments", "looptrack issue close ABC-0001"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.text, w) {
				t.Errorf("%s に %q が無い", c.name, w)
			}
		}
	}
}
