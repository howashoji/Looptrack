package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// 英語の見出し（## Acceptance criteria / ## Verify commands。大小を問わない・DESIGN.md §5-13）の本文で、
// next の受け入れ条件・verify の一覧（GET・MCP verify_issue）・verify.require_on_close の判定が日本語の見出しと同じ結果になる。
// コードブロックの中の見出しは、どちらの言語でも見出しとしない。
func TestEnglishHeadingsSameAsJapanese(t *testing.T) {
	v := newVerifyEnv(t, "ve", `{"verify": {"require_on_close": true}}`)
	m := v.mcpAs(v.ed.token, map[string]string{"X-Looptrack-Project": "ve"})
	const tmpl = "説明\n\n```md\n{A}\n- [ ] コードブロックの中\n{V}\necho no\n```\n\n{A}\n\n- [ ] 動く\n- [ ] テストが通る\n\n" +
		"{V}\n\n```bash\ngo test ./...\n# コメント\nmake lint\n```\n\n## 関連\n\n- `false`\n"
	type result struct {
		acceptance string
		commands   string
		verifyNull bool
		rule       string
		mcpCmds    string
	}
	run := func(acc, ver string) result {
		t.Helper()
		body := strings.NewReplacer("{A}", acc, "{V}", ver).Replace(tmpl)
		id := v.create("見出し "+acc, body)
		var r result
		var n nextJSON
		v.ed.json(200, "POST", "/projects/ve/next", map[string]any{}, &n)
		if n.Issue == nil || n.Issue.ID != id {
			t.Fatalf("%s: next が %s を返さない: %+v", acc, id, n.Issue)
		}
		r.acceptance = n.Acceptance
		r.verifyNull = n.Verify == nil
		if n.Verify != nil {
			r.commands = strings.Join(n.Verify.Commands, "|")
		}
		if p := v.plan(id); strings.Join(p.Commands, "|") != r.commands {
			t.Errorf("%s: GET verify の一覧 %q と next の一覧 %q が違う", acc, p.Commands, r.commands)
		}
		_, data := m.call("verify_issue", map[string]any{"id": id}, false)
		b, _ := json.Marshal(data["commands"])
		r.mcpCmds = string(b)
		// 記録が無ければ閉じられない → 記録すれば閉じられる
		r.rule = v.ed.rule("verify_required_on_close", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"}).Error.Rule
		v.record(id)
		v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done", "comment": "検証結果: verify 2/2"}, nil)
		return r
	}
	ja := run("## 受け入れ条件", "## 検証コマンド")
	if ja.acceptance != "- [ ] 動く\n- [ ] テストが通る" || ja.commands != "go test ./...|make lint" || ja.verifyNull ||
		ja.rule != "verify_required_on_close" || ja.mcpCmds != `["go test ./...","make lint"]` {
		t.Fatalf("日本語の見出し: %+v", ja)
	}
	for _, h := range [][2]string{
		{"## Acceptance criteria", "## Verify commands"},
		{"## ACCEPTANCE CRITERIA", "## verify Commands"},
		{"##  acceptance criteria ", "##\tVerify commands"},
	} {
		if en := run(h[0], h[1]); en != ja {
			t.Errorf("%q / %q: %+v, 日本語 %+v", h[0], h[1], en, ja)
		}
	}
	// 節がコードブロックの中にしか無ければ、どちらの言語でも節なし（起票の雛形の受け入れ条件節が付く・next の verify は null・
	// 記録なしで閉じられる）
	for _, h := range [][2]string{{"## 受け入れ条件", "## 検証コマンド"}, {"## Acceptance criteria", "## Verify commands"}} {
		body := "説明\n\n```md\n" + h[0] + "\n- [ ] x\n" + h[1] + "\necho x\n```\n"
		id := v.create("コードブロックだけ "+h[0], body)
		var n nextJSON
		v.ed.json(200, "POST", "/projects/ve/next", map[string]any{}, &n)
		if n.Issue == nil || n.Issue.ID != id || n.Acceptance != "- [ ] （テスト可能な形で書く。曖昧語を使わない）" || n.Verify != nil {
			t.Errorf("%s: コードブロックの中の見出しを読んだ: acceptance=%q verify=%+v", h[0], n.Acceptance, n.Verify)
		}
		v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"}, nil)
	}
}
