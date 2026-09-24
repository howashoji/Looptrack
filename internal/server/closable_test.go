package server

import (
	"fmt"
	"strings"
	"testing"
)

// 下位（traces でその要件を指すイシュー）の最後の 1 件を閉じたら、要件の検証と close を促す。
// close の応答（REST・CLI・MCP）・matrix（md / json）・summary の ①（CLI・MCP）に同じ判定の結果が出る。
func TestClosableRequirementNotices(t *testing.T) {
	l := newLoopsEnv(t)
	req := l.create("要件: 一覧を出す", map[string]any{"type": "requirement"})
	d := l.create("設計", map[string]any{"type": "design", "traces": []string{req}})
	a := l.create("実装 A", map[string]any{"traces": []string{req}})
	b := l.create("実装 B", map[string]any{"traces": []string{req}})
	notice := "要件 " + req + " の下位がすべて完了しました（Done 2 件・Canceled 1 件）。受け入れ条件を検証して close する: " +
		"looptrack issue show " + req + " で受け入れ条件を確かめ、looptrack issue close " + req +
		` --comment "受け入れ条件の検証結果"（MCP は get_issue → set_status で Done と comment）`

	// 下位が残っているうちは出ない
	var res struct {
		Messages []string         `json:"messages"`
		Ready    []map[string]any `json:"requirements_ready"`
	}
	l.ed.json(200, "POST", "/issues/"+d+"/status", map[string]any{"status": "Done"}, &res)
	if strings.Contains(strings.Join(res.Messages, "\n"), "下位がすべて完了") || res.Ready != nil {
		t.Errorf("下位が残っているのに案内が出た: %+v", res)
	}
	l.ed.json(200, "POST", "/issues/"+a+"/status", map[string]any{"status": "Canceled"}, &res)
	if res.Ready != nil {
		t.Errorf("下位が残っているのに requirements_ready: %+v", res)
	}

	// matrix・summary: まだ対象ではない
	_, _, md := l.ed.do("GET", "/projects/"+l.pr.Slug+"/matrix?format=md", nil)
	if !strings.Contains(string(md), "## ⚠️ 下位がすべて完了したのに開いている要件（0 件）\n\n（なし）\n") {
		t.Errorf("matrix.md（対象なし）:\n%s", md)
	}

	// 最後の 1 件を CLI で閉じる → 応答に案内と次のコマンド
	cli := newCLIEnv(t, l.e, l.pr.Slug, l.ed.token)
	r := mustCLI(t, cli.run("", "close", b), 0, "close")
	if !strings.Contains(r.stdout, b+": Todo → Done\n"+notice+"\n") {
		t.Errorf("close の応答に要件の案内が無い:\n%s", r.stdout)
	}

	// matrix（md / json）に警告節
	_, _, md = l.ed.do("GET", "/projects/"+l.pr.Slug+"/matrix?format=md", nil)
	wantLine := "- " + req + "(Todo) 要件: 一覧を出す — 下位 Done 2 件・Canceled 1 件: " + d + ", " + a + ", " + b +
		" → `looptrack issue close " + req + ` --comment "受け入れ条件の検証結果"` + "`"
	if !strings.Contains(string(md), "## ⚠️ 下位がすべて完了したのに開いている要件（1 件）\n") || !strings.Contains(string(md), wantLine+"\n") {
		t.Errorf("matrix.md に警告節が無い:\n%s", md)
	}
	r = mustCLI(t, cli.run("", "matrix"), 0, "matrix")
	if !strings.Contains(r.stdout, wantLine) {
		t.Errorf("matrix に警告節が無い:\n%s", r.stdout)
	}
	var mj struct {
		Ready []struct {
			Requirement issueRefJSON `json:"requirement"`
			Children    []string     `json:"children"`
			Done        int          `json:"done"`
			Canceled    int          `json:"canceled"`
			Command     string       `json:"command"`
		} `json:"requirements_ready"`
	}
	l.ed.json(200, "GET", "/projects/"+l.pr.Slug+"/matrix", nil, &mj)
	if len(mj.Ready) != 1 || mj.Ready[0].Requirement.ID != req || mj.Ready[0].Done != 2 || mj.Ready[0].Canceled != 1 ||
		len(mj.Ready[0].Children) != 3 || !strings.Contains(mj.Ready[0].Command, "close "+req) {
		t.Errorf("matrix json: %+v", mj)
	}
	m := l.e.mcpAs(l.ed.token, map[string]string{"X-Looptrack-Project": l.pr.Slug})
	if text, _ := m.call("get_matrix", map[string]any{}, false); !strings.Contains(text, wantLine) {
		t.Errorf("MCP get_matrix に警告節が無い:\n%s", text)
	}

	// summary の ①: CLI と MCP で同じ節
	section := "── 下位がすべて完了した要件（検証して close・1 件） ──\n" +
		fmt.Sprintf("%-9s %s（%s）→ %s\n", req, "要件: 一覧を出す", "Done 2 件・Canceled 1 件", "looptrack issue close "+req+` --comment "受け入れ条件の検証結果"`)
	r = mustCLI(t, cli.run("", "summary"), 0, "summary")
	text, data := m.call("project_summary", map[string]any{}, false)
	for name, out := range map[string]string{"CLI": r.stdout, "MCP": text} {
		i, j := strings.Index(out, "══ ① "), strings.Index(out, "══ ② ")
		if i < 0 || j < i || !strings.Contains(out[i:j], section) {
			t.Errorf("%s の summary の ① に要件の節が無い:\n%s", name, out)
		}
	}
	if rr, _ := data["requirements_ready"].([]any); len(rr) != 1 {
		t.Errorf("MCP project_summary の構造化結果: %v", data["requirements_ready"])
	}

	// MCP の set_status でも同じ案内（別の要件で確かめる）
	req2 := l.create("要件 2", map[string]any{"type": "requirement"})
	c := l.create("実装 C", map[string]any{"traces": []string{req2}})
	text, data = m.call("set_status", map[string]any{"id": c, "status": "Done"}, false)
	want2 := "要件 " + req2 + " の下位がすべて完了しました（Done 1 件）。"
	if !strings.Contains(text, c+": Todo → Done\n"+want2) {
		t.Errorf("MCP set_status に要件の案内が無い:\n%s", text)
	}
	if rr, _ := data["requirements_ready"].([]any); len(rr) != 1 {
		t.Errorf("MCP set_status の構造化結果: %v", data)
	}

	// 要件を閉じれば matrix・summary から消える
	l.status(req, "Done")
	l.status(req2, "In Review") // 人の判断待ちに回したものも ① には出さない（② に出る）
	r = mustCLI(t, cli.run("", "summary"), 0, "summary")
	if strings.Contains(r.stdout, "下位がすべて完了") {
		t.Errorf("閉じた要件が summary に残る:\n%s", r.stdout)
	}
}
