package server

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 検証コマンド（A-1。DESIGN.md §5-8）の API・規則・next・MCP。

const verifyBody = "説明\n\n## 検証コマンド\n\n```bash\ngo test ./...\n# コメント行は読まない\nmake lint\n```\n"

type verifyEnv struct {
	*env
	pr     store.Project
	ed     *apiClient
	viewer *apiClient
}

func newVerifyEnv(t *testing.T, slug, rules string) *verifyEnv {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project(slug)
	if rules != "" {
		if err := store.SetRules(ctx, e.db, pr.ID, []byte(rules)); err != nil {
			t.Fatal(err)
		}
	}
	ed := e.user(slug+"-ed", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, ed.ID, "editor")
	vi := e.user(slug+"-vi", "viewer-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, vi.ID, "viewer")
	return &verifyEnv{env: e, pr: pr, ed: e.apiAs(ed), viewer: e.apiAs(vi)}
}

func (v *verifyEnv) create(title, body string) string {
	v.t.Helper()
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	v.ed.json(201, "POST", "/projects/"+v.pr.Slug+"/issues", map[string]any{"title": title, "body": body}, &created)
	return created.Issue.ID
}

func (v *verifyEnv) plan(id string) verifyPlanJSON {
	v.t.Helper()
	var p verifyPlanJSON
	v.ed.json(200, "GET", "/issues/"+id+"/verify", nil, &p)
	return p
}

func results(cmds []string, statuses ...string) []map[string]any {
	var out []map[string]any
	for i, c := range cmds {
		st := "ok"
		if i < len(statuses) {
			st = statuses[i]
		}
		r := map[string]any{"command": c, "status": st, "duration_ms": 1200 + i, "output_tail": "PASS token=s3cr3t-value imp_AbCdEf123"}
		if st == "ok" {
			r["exit_code"] = 0
		} else if st == "fail" {
			r["exit_code"] = 2
		} else {
			r["exit_code"] = nil
		}
		out = append(out, r)
	}
	return out
}

func (v *verifyEnv) record(id string, statuses ...string) map[string]any {
	v.t.Helper()
	p := v.plan(id)
	var out map[string]any
	v.ed.json(201, "POST", "/issues/"+id+"/verify", map[string]any{"body_sha256": p.BodySHA256, "results": results(p.Commands, statuses...),
		"host": "mac.local", "workspace": "/Users/x/im-wt"}, &out)
	return out
}

func (v *verifyEnv) verifyEvents(id string) int {
	var n int
	v.db.QueryRow("SELECT COUNT(*) FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = ? AND e.kind = 'verify'", id).Scan(&n)
	return n
}

func TestVerifyAPI(t *testing.T) {
	v := newVerifyEnv(t, "vf", "")
	id := v.create("検証あり", verifyBody)
	none := v.create("検証なし", "説明だけ")

	// GET: 節が無ければ commands: [] と message（404 ではない）
	p := v.plan(none)
	if len(p.Commands) != 0 || p.Message != domain.NoVerifyCommandsMsg(none).In(i18n.JA) || p.Last != nil {
		t.Errorf("節なし: %+v", p)
	}
	// 閲覧は viewer でもできる
	v.viewer.json(200, "GET", "/issues/"+id+"/verify", nil, &p)
	if strings.Join(p.Commands, "|") != "go test ./...|make lint" || len(p.BodySHA256) != 64 || p.Last != nil || p.Current {
		t.Fatalf("GET: %+v", p)
	}
	if !strings.Contains(p.Text, domain.VerifyCommand(id)) {
		t.Errorf("text に CLI の指示が無い: %s", p.Text)
	}

	// 拒否: 本文の版の不一致 409・コマンドの並びの不一致 400・viewer 403・節なし 400。いずれも何も記録しない
	before := v.ed.state(id)
	bad := v.ed.fail(409, "POST", "/issues/"+id+"/verify", map[string]any{"body_sha256": strings.Repeat("0", 64), "results": results(p.Commands)})
	if bad.Error.Code != "body_changed" {
		t.Errorf("409: %+v", bad)
	}
	rev := []string{p.Commands[1], p.Commands[0]}
	if e := v.ed.fail(400, "POST", "/issues/"+id+"/verify", map[string]any{"body_sha256": p.BodySHA256, "results": results(rev)}); e.Error.Code != "verify_commands_mismatch" {
		t.Errorf("400: %+v", e)
	}
	v.ed.fail(400, "POST", "/issues/"+id+"/verify", map[string]any{"body_sha256": p.BodySHA256, "results": results(p.Commands[:1])})
	v.ed.fail(400, "POST", "/issues/"+id+"/verify", map[string]any{"body_sha256": p.BodySHA256, "results": results(p.Commands, "maybe")})
	if e := v.viewer.fail(403, "POST", "/issues/"+id+"/verify", map[string]any{"body_sha256": p.BodySHA256, "results": results(p.Commands)}); !strings.Contains(e.Error.Message, "閲覧のみ") {
		t.Errorf("403: %+v", e)
	}
	np := v.plan(none)
	v.ed.fail(400, "POST", "/issues/"+none+"/verify", map[string]any{"body_sha256": np.BodySHA256, "results": results([]string{"true"})})
	if after := v.ed.state(id); after != before || v.verifyEvents(id) != 0 || v.verifyEvents(none) != 0 {
		t.Errorf("拒否した記録が残った: %+v → %+v", before, after)
	}

	// 成功: コメント（出力を含まない）と issue_events kind verify が残る
	out := v.record(id, "ok", "fail")
	if out["ok"] != false || out["passed"] != float64(1) || out["failed"] != float64(1) {
		t.Errorf("POST: %v", out)
	}
	var d issueDetailJSON
	v.ed.json(200, "GET", "/issues/"+id, nil, &d)
	c := d.Comments[len(d.Comments)-1].Content
	want := "検証コマンド: 1/2 成功・1 失敗（2.4 秒・本文 " + p.BodySHA256[:8] + "）\n- ok `go test ./...`（1.2 秒）\n- fail `make lint`（exit 2・1.2 秒）"
	if c != want {
		t.Errorf("コメント:\n%s\nwant\n%s", c, want)
	}
	if strings.Contains(c, "PASS") || strings.Contains(c, "s3cr3t") {
		t.Errorf("コメントに出力が入った: %s", c)
	}
	var raw []byte
	var via string
	v.db.QueryRow("SELECT e.detail, e.via FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = ? AND e.kind = 'verify'", id).Scan(&raw, &via)
	var det store.VerifyDetail
	if err := json.Unmarshal(raw, &det); err != nil {
		t.Fatal(err)
	}
	if det.BodySHA256 != p.BodySHA256 || det.OK || len(det.Results) != 2 || det.Host != "mac.local" || det.Workspace != "im-wt" || det.CommentSeq != len(d.Comments) || via != "api" {
		t.Errorf("detail: %+v via=%s", det, via)
	}
	if o := det.Results[0].OutputTail; strings.Contains(o, "s3cr3t") || strings.Contains(o, "AbCdEf") || !strings.Contains(o, "token=***") {
		t.Errorf("出力のマスク: %q", o)
	}
	// GET は直近の記録（出力つき）を返す
	p = v.plan(id)
	if p.Last == nil || !p.Current || p.Last.OK || p.Last.Failed != 1 || len(p.Last.Results) != 2 || p.Last.Results[1].ExitCode == nil || *p.Last.Results[1].ExitCode != 2 {
		t.Errorf("GET last: %+v", p.Last)
	}
	// クローズ済みにも記録できる
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"}, nil)
	v.record(id)
	if v.verifyEvents(id) != 2 {
		t.Errorf("クローズ済みへの記録: %d", v.verifyEvents(id))
	}

	// 上限（21 コマンド）は GET も POST も 400
	var many strings.Builder
	many.WriteString("## 検証コマンド\n\n```\n")
	for i := 0; i < 21; i++ {
		many.WriteString("echo " + strconv.Itoa(i) + "\n")
	}
	many.WriteString("```\n")
	big := v.create("多すぎる", many.String())
	if e := v.ed.fail(400, "GET", "/issues/"+big+"/verify", nil); e.Error.Code != "verify_commands_too_many" {
		t.Errorf("上限: %+v", e)
	}
	var bd issueDetailJSON
	v.ed.json(200, "GET", "/issues/"+big, nil, &bd)
	var cmds []string
	for i := 0; i < 21; i++ {
		cmds = append(cmds, "echo "+strconv.Itoa(i))
	}
	if e := v.ed.fail(400, "POST", "/issues/"+big+"/verify", map[string]any{"body_sha256": domain.BodySHA256(bd.Body), "results": results(cmds)}); e.Error.Code != "verify_commands_too_many" {
		t.Errorf("上限（POST）: %+v", e)
	}
}

func TestVerifyRequireOnClose(t *testing.T) {
	v := newVerifyEnv(t, "vr", `{"verify": {"require_on_close": true}}`)
	id := v.create("検証あり", verifyBody)
	cmd := domain.VerifyCommand(id)

	// 記録なし
	e := v.ed.rule("verify_required_on_close", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done", "comment": "終わり"})
	if e.Error.Message != id+" は検証コマンドを持っていますが、verify の記録がありません。次を実行してから閉じてください: "+cmd || !e.Error.Overridable {
		t.Errorf("記録なし: %+v", e.Error)
	}
	// 直近が失敗（前に成功していても直近だけを見る）
	v.record(id)
	v.record(id, "ok", "fail")
	e = v.ed.rule("verify_required_on_close", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"})
	if e.Error.Message != id+" の最後の verify で 1 件が失敗しています。直して次を実行してから閉じてください: "+cmd {
		t.Errorf("失敗: %s", e.Error.Message)
	}
	// 成功の後に本文を編集すると無効になる
	v.record(id)
	var d issueDetailJSON
	v.ed.json(200, "GET", "/issues/"+id, nil, &d)
	v.ed.json(200, "PATCH", "/issues/"+id, map[string]any{"markdown": strings.Replace(d.Markdown, "説明", "説明（直した）", 1)}, nil, "If-Match", strconv.Itoa(d.Version))
	e = v.ed.rule("verify_required_on_close", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"})
	if e.Error.Message != id+" の本文が最後の verify の後に変わりました。次を実行してから閉じてください: "+cmd {
		t.Errorf("本文の変更: %s", e.Error.Message)
	}
	if st := v.ed.state(id); st.status != "Todo" {
		t.Errorf("拒否で状態が変わった: %+v", st)
	}
	// 項目（タイトル）の変更は本文の版を変えない。verify 自身のコメント・通常のコメント・close --comment でも無効にならない
	v.record(id)
	v.ed.json(200, "PATCH", "/issues/"+id, map[string]any{"title": "検証あり（改題）"}, nil, "If-Match", strconv.Itoa(v.ed.state(id).version))
	v.ed.json(201, "POST", "/issues/"+id+"/comments", map[string]any{"text": "確認した"}, nil)
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Review", "comment": "判断してほしい点: なし"}, nil)
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done", "comment": "検証結果: verify 2/2"}, nil)

	// 上書き: rule_override（rule verify_required_on_close）が残る
	id2 := v.create("上書き", verifyBody)
	v.ed.json(200, "POST", "/issues/"+id2+"/status", map[string]any{"status": "Done", "override_reason": "CI で確認済み（利用者の指示）"}, nil)
	var rule, reason string
	v.db.QueryRow(`SELECT JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.rule')), JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.reason')) FROM issue_events e
JOIN issues i ON i.id = e.issue_id WHERE i.display_id = ? AND e.kind = 'rule_override'`, id2).Scan(&rule, &reason)
	if rule != "verify_required_on_close" || reason != "CI で確認済み（利用者の指示）" {
		t.Errorf("rule_override: %s %s", rule, reason)
	}

	// 節の無いイシューと Canceled（既定の statuses は Done だけ）には効かない
	none := v.create("検証なし", "説明だけ")
	v.ed.json(200, "POST", "/issues/"+none+"/status", map[string]any{"status": "Done"}, nil)
	id3 := v.create("取りやめ", verifyBody)
	v.ed.json(200, "POST", "/issues/"+id3+"/status", map[string]any{"status": "Canceled"}, nil)

	// 起票で Done にする場合も同じ判定（記録が無いので拒否。上書き可）
	e = v.ed.rule("verify_required_on_close", "POST", "/projects/vr/issues", map[string]any{"title": "x", "status": "Done", "body": verifyBody})
	if !strings.Contains(e.Error.Message, "起票することはできません") {
		t.Errorf("起票: %s", e.Error.Message)
	}
	v.ed.json(201, "POST", "/projects/vr/issues", map[string]any{"title": "x", "status": "Done", "body": verifyBody, "override_reason": "移行分"}, nil)
	v.ed.json(201, "POST", "/projects/vr/issues", map[string]any{"title": "y", "status": "Done", "body": "説明だけ"}, nil)

	// statuses の誤りは設定時に拒否
	if _, err := domain.ParseRules([]byte(`{"verify": {"require_on_close": true, "statuses": ["Closed"]}}`)); err == nil {
		t.Error("不正な statuses を受け付けた")
	}
}

func TestVerifyNextAndMCP(t *testing.T) {
	v := newVerifyEnv(t, "vn", "")
	id := v.create("検証あり", verifyBody)

	// next: verify に一覧、text に節
	var n nextJSON
	v.ed.json(200, "POST", "/projects/vn/next", map[string]any{}, &n)
	if n.Issue == nil || n.Issue.ID != id || n.Verify == nil || strings.Join(n.Verify.Commands, "|") != "go test ./...|make lint" || n.Verify.Last != nil {
		t.Fatalf("next: %+v", n.Verify)
	}
	if !strings.Contains(n.Text, "── 検証コマンド（"+domain.VerifyCommand(id)+" で実行して記録する） ──\n- go test ./...\n- make lint\n直近の verify: 記録なし") {
		t.Errorf("next の text:\n%s", n.Text)
	}
	v.record(id)
	v.ed.json(200, "POST", "/projects/vn/next", map[string]any{}, &n) // 着手中を返す
	if n.Verify == nil || n.Verify.Last == nil || !n.Verify.Last.OK || !n.Verify.Last.Current {
		t.Errorf("next の last: %+v", n.Verify)
	}
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"}, nil)
	none := v.create("検証なし", "説明だけ")
	var raw map[string]any
	v.ed.json(200, "POST", "/projects/vn/next", map[string]any{}, &raw)
	if vv, ok := raw["verify"]; !ok || vv != nil {
		t.Errorf("節なしの next の verify は null: %v", raw["verify"])
	}

	// MCP verify_issue: GET と同じ文言 + report_verify の使い方。節なしは isError で GET の message と同じ
	m := v.mcpAs(v.ed.token, map[string]string{"X-Looptrack-Project": "vn"})
	text, data := m.call("verify_issue", map[string]any{"id": id}, false)
	if p := v.plan(id); text != p.Text+"\n"+reportVerifyHint(i18n.JA, p.BodySHA256) || data["body_sha256"] != p.BodySHA256 {
		t.Errorf("verify_issue:\n%s\nGET:\n%s", text, p.Text)
	}
	text, _ = m.call("verify_issue", map[string]any{"id": none}, true)
	if p := v.plan(none); text != p.Message {
		t.Errorf("節なし: %q, GET message %q", text, p.Message)
	}
	// viewer も一覧は見られる。MCP からの呼び出しでは何も記録しない
	vm := v.mcpAs(v.viewer.token, map[string]string{"X-Looptrack-Project": "vn"})
	vm.call("verify_issue", map[string]any{"id": id}, false)
	var events int
	v.db.QueryRow("SELECT COUNT(*) FROM issue_events WHERE via = 'mcp'").Scan(&events)
	if events != 0 {
		t.Errorf("verify_issue が記録した: %d", events)
	}
}

// サーバがコマンドを実行する経路が無いこと（プロセスを起動する関数を本番のコードから呼ばない。DESIGN.md §5-8-2）。
func TestServerNeverExecutes(t *testing.T) {
	root := filepath.Join("..", "..")
	// プロセスを起動する import と関数（syscall 自体はシグナルの受け取りに使うので、起動系の関数だけを禁じる）
	bannedImports := map[string]bool{"os/exec": true, "golang.org/x/sys/unix": true, "golang.org/x/sys/execabs": true}
	bannedCalls := map[string]bool{"os.StartProcess": true, "syscall.Exec": true, "syscall.ForkExec": true, "syscall.StartProcess": true}
	const clientPkg = "github.com/howashoji/looptrack/internal/client"
	checked := 0
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// テスト用の補助（git からテストデータを展開する）は本番のバイナリに入らない
				if name := d.Name(); name == "testdata" || name == "testutil" {
					return filepath.SkipDir
				}
				// クライアント側（CLI・hook。git や ps を起動する）は internal/client に置く。
				// サーバ側のパッケージがそれを import しないことを下で確かめる（DESIGN.md §5-11）
				if filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "internal", "client")) {
					return filepath.SkipDir
				}
				// ビルドの道具（go run で使う main パッケージ。NOTICE の生成は go list を起動する）は
				// main なので import できず、looptrack に入らない
				if filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "internal", "tools")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			checked++
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if bannedImports[p] {
					t.Errorf("%s が %s を import している（サーバはコマンドを実行しない）", path, p)
				}
				// internal/ のサーバ側からクライアント側（コマンドを起動しうる）への依存を禁じる。cmd/ の入口は両方を束ねてよい
				if dir == "internal" && (p == clientPkg || strings.HasPrefix(p, clientPkg+"/")) {
					t.Errorf("%s が %s を import している（サーバ側はクライアント側に依存しない）", path, p)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && bannedCalls[x.Name+"."+sel.Sel.Name] {
						t.Errorf("%s が %s.%s を使っている（サーバはコマンドを実行しない）", path, x.Name, sel.Sel.Name)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 20 {
		t.Fatalf("調べたファイルが少なすぎる: %d", checked)
	}
}

// MCP の report_verify。POST /verify と同じ検査・拒否、自己申告の印（detail・コメント・next・verify_issue・summary・CLI）、
// verify.require_on_close は自己申告も数える。
func TestReportVerifyMCP(t *testing.T) {
	v := newVerifyEnv(t, "vm", `{"verify": {"require_on_close": true}}`)
	id := v.create("検証あり", verifyBody)
	none := v.create("検証なし", "説明だけ")
	p := v.plan(id)
	m := v.mcpAs(v.ed.token, map[string]string{"X-Looptrack-Project": "vm"})
	vm := v.mcpAs(v.viewer.token, map[string]string{"X-Looptrack-Project": "vm"})
	send := func(c *mcpClient, iid, sha string, res []map[string]any, wantErr bool) (string, map[string]any) {
		t.Helper()
		return c.call("report_verify", map[string]any{"id": iid, "body_sha256": sha, "results": res, "host": "mac.local", "workspace": "/Users/x/im-wt"}, wantErr)
	}

	// 拒否は POST /verify と同じ文言（409 body_changed・400 並び違い・viewer 403・節なし 400）。何も記録しない
	rev := []string{p.Commands[1], p.Commands[0]}
	np := v.plan(none)
	for _, c := range []struct {
		name   string
		client *mcpClient
		rest   *apiClient
		code   int
		iid    string
		sha    string
		res    []map[string]any
	}{
		{"body_changed", m, v.ed, 409, id, strings.Repeat("0", 64), results(p.Commands)},
		{"mismatch", m, v.ed, 400, id, p.BodySHA256, results(rev)},
		{"件数", m, v.ed, 400, id, p.BodySHA256, results(p.Commands[:1])},
		{"viewer", vm, v.viewer, 403, id, p.BodySHA256, results(p.Commands)},
		{"節なし", m, v.ed, 400, none, np.BodySHA256, results([]string{"true"})},
	} {
		text, _ := send(c.client, c.iid, c.sha, c.res, true)
		e := c.rest.fail(c.code, "POST", "/issues/"+c.iid+"/verify", map[string]any{"body_sha256": c.sha, "results": c.res})
		if text != e.Error.Message {
			t.Errorf("%s: MCP %q / REST %q", c.name, text, e.Error.Message)
		}
	}
	if v.verifyEvents(id) != 0 || v.verifyEvents(none) != 0 {
		t.Fatalf("拒否した記録が残った")
	}

	// 記録: 経路 mcp・detail の self_reported・コメントの印
	text, data := send(m, id, p.BodySHA256, results(p.Commands, "ok", "fail"), false)
	wantComment := "検証コマンド（MCP の自己申告）: 1/2 成功・1 失敗（2.4 秒・本文 " + p.BodySHA256[:8] + "）\n- ok `go test ./...`（1.2 秒）\n- fail `make lint`（exit 2・1.2 秒）"
	if !strings.HasPrefix(text, "verify を記録（MCP の自己申告）: "+id+"\n"+wantComment) || data["self_reported"] != true || data["ok"] != false {
		t.Errorf("report_verify:\n%s\n%v", text, data)
	}
	var raw []byte
	var via string
	v.db.QueryRow("SELECT e.detail, e.via FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = ? AND e.kind = 'verify'", id).Scan(&raw, &via)
	var det store.VerifyDetail
	if err := json.Unmarshal(raw, &det); err != nil {
		t.Fatal(err)
	}
	if via != "mcp" || !det.SelfReported || det.Workspace != "im-wt" || strings.Contains(det.Results[0].OutputTail, "s3cr3t") {
		t.Errorf("detail: %+v via=%s", det, via)
	}
	// show（コメント）に印
	var d issueDetailJSON
	v.ed.json(200, "GET", "/issues/"+id, nil, &d)
	if c := d.Comments[len(d.Comments)-1].Content; c != wantComment || !strings.Contains(d.Markdown, "検証コマンド（MCP の自己申告）: ") {
		t.Errorf("show のコメント: %q", c)
	}

	// 失敗の自己申告は require_on_close で拒否（直近の 1 件を見る）
	v.ed.rule("verify_required_on_close", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"})

	// 全件成功の自己申告 → GET・verify_issue・next に印
	send(m, id, p.BodySHA256, results(p.Commands), false)
	p = v.plan(id)
	if p.Last == nil || !p.Last.SelfReported || p.Last.Via != "mcp" || !p.Current || !p.Last.OK || !strings.Contains(p.Text, "（現在の本文に対する記録・MCP の自己申告）") {
		t.Errorf("GET: %+v\n%s", p.Last, p.Text)
	}
	vt, _ := m.call("verify_issue", map[string]any{"id": id}, false)
	if !strings.Contains(vt, "（現在の本文に対する記録・MCP の自己申告）") || !strings.Contains(vt, "report_verify で送れる") || !strings.Contains(vt, p.BodySHA256) {
		t.Errorf("verify_issue:\n%s", vt)
	}
	var n nextJSON
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress"}, nil)
	v.ed.json(200, "POST", "/projects/vm/next", map[string]any{}, &n)
	if n.Verify == nil || n.Verify.Last == nil || !n.Verify.Last.SelfReported || !strings.Contains(n.Text, "・MCP の自己申告）") {
		t.Errorf("next: %+v\n%s", n.Verify, n.Text)
	}

	// summary の ②（In Review）に印。REST・MCP・CLI で同じ文言
	other := v.create("CLI で検証", verifyBody)
	v.record(other) // CLI（経路 api）の記録には印が付かない
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Review"}, nil)
	v.ed.json(200, "POST", "/issues/"+other+"/status", map[string]any{"status": "In Review"}, nil)
	var sum summaryRes
	v.ed.json(200, "GET", "/projects/vm/summary", nil, &sum)
	marks := map[string]bool{}
	for _, it := range sum.InReview {
		marks[it.ID] = it.VerifySelfReported
	}
	if len(marks) != 2 || !marks[id] || marks[other] {
		t.Errorf("summary の verify_self_reported: %v", marks)
	}
	st, _ := m.call("project_summary", map[string]any{}, false)
	line := func(s, iid string) string { // ② の行（① の ready にも同じ ID が出るので ② の見出しの後から探す）
		if i := strings.Index(s, "══ ② "); i >= 0 {
			s = s[i:]
		}
		for _, l := range strings.Split(s, "\n") {
			if strings.HasPrefix(l, iid+" ") && strings.Contains(l, "In Review") {
				return l
			}
		}
		return ""
	}
	if !strings.HasSuffix(line(st, id), "検証あり  （直近の verify は MCP の自己申告）") || strings.Contains(line(st, other), "自己申告") {
		t.Errorf("project_summary:\n%s", st)
	}
	cli := newCLIEnv(t, v.env, "vm", v.ed.token)
	r := mustCLI(t, cli.run("", "summary"), 0, "summary")
	if line(r.stdout, id) != line(st, id) || line(r.stdout, other) != line(st, other) {
		t.Errorf("CLI と MCP の ② が違う:\n--- CLI\n%s\n--- MCP\n%s", r.stdout, st)
	}
	r = mustCLI(t, cli.run("", "verify", id, "--last"), 0, "verify --last")
	if !strings.Contains(r.stdout, "現在の本文に対する記録・MCP の自己申告・mac.local im-wt") {
		t.Errorf("verify --last:\n%s", r.stdout)
	}

	// 全件成功の自己申告で require_on_close の Done が通る（MCP の set_status でも）
	if _, data := m.call("set_status", map[string]any{"id": id, "status": "Done", "comment": "検証結果: report_verify 2/2"}, false); data == nil {
		t.Errorf("set_status Done")
	}
	if st := v.ed.state(id); st.status != "Done" {
		t.Errorf("Done にならない: %+v", st)
	}
}
