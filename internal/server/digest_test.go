package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/pathhint"
	"github.com/howashoji/looptrack/internal/hookio"
)

// セッション要約（DESIGN.md §9-7）を REST・MCP・CLI の 3 経路から同じ本文・同じ手がかりで送ると、保存された本文がバイト一致する。
// CLI は手がかりを自分で集める（pathhint.Collect）。REST と MCP には同じ関数で集めた値を渡す。
// 秘密の当たる要約は 3 経路とも拒まれ、拒否の文面には行番号と規則の語だけが出て、値は出ない。
func TestDigestSameBytesFromRESTMCPCLI(t *testing.T) {
	a := newAttachAPIEnv(t)
	home := t.TempDir()
	projDir := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": a.srv.URL + "/im", "LOOPTRACK_PROJECT": a.pr.Slug, "LOOPTRACK_TOKEN": a.ed.token,
		"LOOPTRACK_LANG": "ja", "HOME": home, "CLAUDE_PROJECT_DIR": projDir}
	runCLI := func(args ...string) (int, string) {
		t.Helper()
		var out, errb bytes.Buffer
		code := cli.Main(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
		return code, errb.String()
	}
	// CLI と同じ集め方（作業ディレクトリとプロジェクトのディレクトリ）
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	hints := pathhint.Collect(clienv.FromMap(vars), []string{wd, projDir}, nil)
	if hints.Home != home {
		t.Fatalf("前提が崩れています: 手がかりの home %q", hints.Home)
	}
	text := "セッション要約: test-pc\nbranch: main\nhead: ce768dc\nai: claude-code\n\n## 現在地\n- 設定は " + filepath.Join(home, ".config", "x") + " にある"
	want := "セッション要約: test-pc\nbranch: main\nhead: ce768dc\nai: claude-code\n\n## 現在地\n- 設定は ~" + string(filepath.Separator) + filepath.Join(".config", "x") + " にある"
	if top := hookio.GitRoot(wd); top != "" { // git の作業ツリーの中で走るとき（ふつうはこちら）は、ルートの置き換えも確かめる
		text += "\n- 直した: " + filepath.Join(top, "internal", "server", "digest_test.go")
		want += "\n- 直した: ." + string(filepath.Separator) + filepath.Join("internal", "server", "digest_test.go")
	} else {
		t.Log("git の作業ツリーの外で走っているので、ルートの置き換えは確かめない（home だけ）")
	}

	stored := func() string {
		t.Helper()
		var d issueDetailJSON
		a.ed.json(200, "GET", "/issues/"+a.issue, nil, &d)
		return d.Comments[len(d.Comments)-1].Content
	}
	var got []string
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": text, "path_hints": map[string]any{
		"roots": hints.Roots, "home": hints.Home, "machine_local": hints.MachineLocal}}, nil)
	got = append(got, stored())
	m := a.mcpAs(a.ed.token, map[string]string{"X-Looptrack-Project": a.pr.Slug})
	m.call("add_comment", map[string]any{"id": a.issue, "text": text, "path_hints": map[string]any{
		"roots": hints.Roots, "home": hints.Home, "machine_local": hints.MachineLocal}}, false)
	got = append(got, stored())
	if code, stderr := runCLI("comment", a.issue, text); code != 0 {
		t.Fatalf("CLI: exit %d・%s", code, stderr)
	}
	got = append(got, stored())
	for i, s := range got {
		if s != want {
			t.Errorf("経路 %d（REST・MCP・CLI の順）の本文 %q, want %q", i, s, want)
		}
	}
	n := a.count("SELECT COUNT(*) FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = '" + a.issue +
		"' AND e.kind = 'comment' AND JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.kind')) = 'digest'")
	if n != 3 {
		t.Errorf("記録の kind: digest が %d 件、3 件のはず", n)
	}

	// 手がかりを付けなければ何も置き換えない（REST）
	a.ed.json(201, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": text}, nil)
	if s := stored(); s != text {
		t.Errorf("手がかりなしで置き換わった: %q", s)
	}
	// 形の違う手がかりは 400
	if e := a.ed.fail(400, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": text, "path_hints": map[string]any{"roots": []string{"/"}}}); e.Error.Code != "invalid_argument" {
		t.Errorf("ルートの手がかり: %+v", e.Error)
	}

	// 秘密: 3 経路とも拒み、行番号と規則の語だけを出す
	const value = "s3cr3t-value-xyz"
	bad := "セッション要約: test-pc\nbranch: main\nhead: ce768dc\nai: claude-code\npassword=" + value
	before, _ := commentCounts(t, a, a.issue)
	e := a.ed.fail(422, "POST", "/issues/"+a.issue+"/comments", map[string]any{"text": bad}, "Accept-Language", "en")
	if e.Error.Code != "comment_secret" || strings.Contains(e.Error.Message, value) || !strings.Contains(e.Error.Message, "line 5") || !strings.Contains(e.Error.Message, "password=…") {
		t.Errorf("REST: %+v", e.Error)
	}
	if text, _ := m.call("add_comment", map[string]any{"id": a.issue, "text": bad}, true); strings.Contains(text, value) || !strings.Contains(text, "5 行目") {
		t.Errorf("MCP: %q", text)
	}
	if code, stderr := runCLI("comment", a.issue, bad); code == 0 || strings.Contains(stderr, value) || !strings.Contains(stderr, "5 行目") {
		t.Errorf("CLI: exit %d・%q", code, stderr)
	}
	if after, _ := commentCounts(t, a, a.issue); after != before {
		t.Errorf("拒んだ要約が残った: %d → %d", before, after)
	}
}
