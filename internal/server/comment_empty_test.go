package server

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
)

// 本文（前後の空白を除く）も添付も無いコメントは、REST・MCP・CLI のどれからも拒まれ、記録が残らない。
// 判定は service の 1 か所にある（ここでは 3 経路が同じ文面で拒まれることを確かめる）。
// 対照として、本文だけ・添付だけのコメントは同じ経路で通る。

func commentCounts(t *testing.T, a *attachAPIEnv, issue string) (comments, events int) {
	t.Helper()
	var d issueDetailJSON
	a.ed.json(200, "GET", "/issues/"+issue, nil, &d)
	n := a.count("SELECT COUNT(*) FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = '" + issue + "' AND e.kind = 'comment'")
	return len(d.Comments), n
}

func TestCommentWithoutTextOrAttachmentIsRejected(t *testing.T) {
	a := newAttachAPIEnv(t)
	at := a.mustUpload(a.issue, "log.txt", "text/plain", []byte("log"))
	baseC, baseE := commentCounts(t, a, a.issue)
	wantJA, wantEN := "に追記するコメントには", "A comment on "

	// REST: 空・空白だけは 400。Accept-Language で文面が変わる。text が無い（null）は従来どおりの別の 400
	for _, c := range []struct{ name, text string }{{"空", ""}, {"空白だけ", "   "}, {"改行とタブだけ", "\n\t\n"}} {
		e := a.ed.fail(400, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": c.text}, "Accept-Language", "ja")
		if e.Error.Code != "comment_empty" || !strings.Contains(e.Error.Message, wantJA) || !strings.Contains(e.Error.Message, a.issue) {
			t.Errorf("REST（%s・ja）: %+v", c.name, e.Error)
		}
		e = a.ed.fail(400, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": c.text}, "Accept-Language", "en")
		if e.Error.Code != "comment_empty" || !strings.Contains(e.Error.Message, wantEN) {
			t.Errorf("REST（%s・en）: %+v", c.name, e.Error)
		}
	}
	e := a.ed.fail(400, "POST", "/issues/"+a.issue+"/comments", map[string]any{"attachments": []int64{}}, "Accept-Language", "ja")
	if e.Error.Code != "invalid_argument" {
		t.Errorf("REST（text が無い）: %+v", e.Error)
	}

	// MCP: 空・空白だけ。REST と同じ文面（service の 1 か所）。text の省略は、ツールの入力の検査（text は必須）が先に拒む
	hdr := map[string]string{"X-Looptrack-Project": a.pr.Slug}
	mja := a.mcpAs(a.ed.token, hdr)
	men := a.mcpAs(a.ed.token, map[string]string{"X-Looptrack-Project": a.pr.Slug, "Accept-Language": "en"})
	for _, args := range []map[string]any{{"id": a.issue, "text": ""}, {"id": a.issue, "text": "  "}} {
		if text, _ := mja.call("add_comment", args, true); !strings.Contains(text, wantJA) {
			t.Errorf("MCP（ja）%v: %q", args, text)
		}
		if text, _ := men.call("add_comment", args, true); !strings.Contains(text, wantEN) {
			t.Errorf("MCP（en）%v: %q", args, text)
		}
	}

	mja.call("add_comment", map[string]any{"id": a.issue}, true)

	// CLI: looptrack issue comment <ID> ""（本物のサーバに当てる）。言語は LOOPTRACK_LANG で固定する
	runCLI := func(lang string, args ...string) (int, string, string) {
		t.Helper()
		var out, errb bytes.Buffer
		vars := clienv.FromMap(map[string]string{"LOOPTRACK_API_URL": a.srv.URL + "/im", "LOOPTRACK_PROJECT": a.pr.Slug, "LOOPTRACK_TOKEN": a.ed.token,
			"LOOPTRACK_LANG": lang, "HOME": t.TempDir(), "CLAUDE_PROJECT_DIR": t.TempDir()})
		code := cli.Main(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, vars)
		return code, out.String(), errb.String()
	}
	for _, text := range []string{"", "   "} {
		if code, _, stderr := runCLI("ja", "comment", a.issue, text); code == 0 || !strings.Contains(stderr, wantJA) {
			t.Errorf("CLI（ja）%q: exit %d・stderr %q", text, code, stderr)
		}
		if code, _, stderr := runCLI("en", "comment", a.issue, text); code == 0 || !strings.Contains(stderr, wantEN) {
			t.Errorf("CLI（en）%q: exit %d・stderr %q", text, code, stderr)
		}
	}

	// どの経路も、拒んだコメントとその記録を残さない
	if c, ev := commentCounts(t, a, a.issue); c != baseC || ev != baseE {
		t.Fatalf("拒んだコメントが残った: コメント %d → %d・記録 %d → %d", baseC, c, baseE, ev)
	}

	// 対照: 同じ経路で、本文だけ・添付だけのコメントは通る（3 経路で 1 件ずつ増える）
	want := baseC
	step := func(name string) {
		t.Helper()
		want++
		if c, _ := commentCounts(t, a, a.issue); c != want {
			t.Fatalf("対照（%s）: コメントが %d 件で、%d 件のはず（前提が崩れています）", name, c, want)
		}
	}
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": "本文だけ"}, nil)
	step("REST の本文だけ")
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": "", "attachments": []int64{at.ID}}, nil)
	step("REST の添付だけ")
	mja.call("add_comment", map[string]any{"id": a.issue, "text": "本文だけ"}, false)
	step("MCP の本文だけ")
	mja.call("add_comment", map[string]any{"id": a.issue, "text": "", "attachments": []int64{at.ID}}, false)
	step("MCP の添付だけ")
	if code, _, stderr := runCLI("ja", "comment", a.issue, "本文だけ"); code != 0 {
		t.Fatalf("対照（CLI の本文だけ）: exit %d・%s", code, stderr)
	}
	step("CLI の本文だけ")
	file := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(file, []byte("evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("ja", "comment", a.issue, "", "--attach", file); code != 0 {
		t.Fatalf("対照（CLI の添付だけ）: exit %d・%s", code, stderr)
	}
	step("CLI の添付だけ")

	// 状態の変更と同時のコメントが空白だけのときは、コメントを作らず状態の変更は通す。
	// 応答にも「コメント追記」の行を出さない（行は service の結果で決まり、REST・MCP・CLI で同じ）。
	// 対照として、本文のあるコメントは同じ経路で作られ、行も出る
	const added = "コメント追記"
	statusStep := func(name, status, comment string, wantComment bool, do func() string) {
		t.Helper()
		before, _ := commentCounts(t, a, a.issue)
		out := do()
		after, _ := commentCounts(t, a, a.issue)
		wantN := before
		if wantComment {
			wantN++
		}
		if after != wantN {
			t.Errorf("%s: コメントが %d → %d 件（%d 件のはず）", name, before, after, wantN)
		}
		if got := strings.Contains(out, added); got != wantComment {
			t.Errorf("%s: 応答に「%s」が出る = %v、%v のはず（comment=%q）: %q", name, added, got, wantComment, comment, out)
		}
		var d issueDetailJSON
		a.ed.json(200, "GET", "/issues/"+a.issue, nil, &d)
		if d.Status != status {
			t.Errorf("%s: 状態 = %q、%q のはず", name, d.Status, status)
		}
	}
	restStatus := func(status, comment string) func() string {
		return func() string {
			var res struct {
				Messages []string `json:"messages"`
			}
			a.ed.json(http.StatusOK, "POST", "/issues/"+a.issue+"/status", map[string]any{"status": status, "comment": comment}, &res, "Accept-Language", "ja")
			return strings.Join(res.Messages, "\n")
		}
	}
	mcpStatus := func(status, comment string) func() string {
		return func() string {
			text, _ := mja.call("set_status", map[string]any{"id": a.issue, "status": status, "comment": comment}, false)
			return text
		}
	}
	cliStatus := func(status, comment string) func() string {
		return func() string {
			code, stdout, stderr := runCLI("ja", "status", a.issue, status, "--comment", comment)
			if code != 0 {
				t.Fatalf("CLI status %q: exit %d・%s", status, code, stderr)
			}
			return stdout
		}
	}
	statusStep("REST・空白だけ", "In Progress", "  ", false, restStatus("In Progress", "  "))
	statusStep("MCP・空白だけ", "Todo", "  ", false, mcpStatus("Todo", "  "))
	statusStep("CLI・空白だけ", "In Progress", "  ", false, cliStatus("In Progress", "  "))
	statusStep("REST・本文あり（対照）", "Todo", "戻します", true, restStatus("Todo", "戻します"))
	statusStep("MCP・本文あり（対照）", "In Progress", "再開します", true, mcpStatus("In Progress", "再開します"))
	statusStep("CLI・本文あり（対照）", "Todo", "もう一度戻します", true, cliStatus("Todo", "もう一度戻します"))
}
