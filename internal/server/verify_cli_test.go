package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// CLI（looptrack issue verify）とサーバの往復。CLI は利用者の LOOPTRACK_* を除いた環境で起動し、テストサーバだけを指す。
func TestVerifyCLIRoundTrip(t *testing.T) {
	v := newVerifyEnv(t, "vc", `{"verify": {"require_on_close": true}}`)
	dir := t.TempDir()
	run := func(args ...string) (string, int) {
		t.Helper()
		env := append(cliAPIEnv(v.srv.URL+"/im", v.pr.Slug, v.ed.token, dir), "CLAUDE_PROJECT_DIR="+dir, "LOOPTRACK_USAGE=0")
		r := runCLI(t, dir, env, "", append([]string{"issue"}, args...)...)
		return r.stdout + r.stderr, r.code
	}
	lastEvent := func(id string) store.VerifyDetail {
		t.Helper()
		var raw []byte
		if err := v.db.QueryRow(`SELECT e.detail FROM issue_events e JOIN issues i ON i.id = e.issue_id
 WHERE i.display_id = ? AND e.kind = 'verify' ORDER BY e.id DESC LIMIT 1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d store.VerifyDetail
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	body := "説明\n\n## 検証コマンド\n\n```bash\n" +
		"test -z \"$LOOPTRACK_API_URL$LOOPTRACK_PROJECT$LOOPTRACK_TOKEN\" && test -n \"$LOOPTRACK_VERIFY_ID\"\n" +
		"echo password=hunter2; exit 3\n" +
		"```\n"
	failing := v.create("失敗する検証", body)
	passing := v.create("通る検証", "説明\n\n## 検証コマンド\n\n- `true`\n")
	none := v.create("検証なし", "説明だけ")

	// 節なし: exit 2 で何も記録しない
	if out, code := run("verify", none); code != 2 || !strings.Contains(out, "検証コマンドがありません") || v.verifyEvents(none) != 0 {
		t.Errorf("節なし: code=%d events=%d %s", code, v.verifyEvents(none), out)
	}
	// --list は実行も記録もしない
	if out, code := run("verify", failing, "--list"); code != 0 || !strings.Contains(out, "echo password=hunter2") || v.verifyEvents(failing) != 0 {
		t.Errorf("--list: code=%d %s", code, out)
	}
	// 失敗あり: 全部実行して 1 回で記録し、exit 1。LOOPTRACK_API_URL・PROJECT・TOKEN は検証コマンドに渡らない
	out, code := run("verify", failing)
	if code != 1 || v.verifyEvents(failing) != 1 {
		t.Fatalf("失敗あり: code=%d events=%d %s", code, v.verifyEvents(failing), out)
	}
	d := lastEvent(failing)
	if d.OK || d.Passed != 1 || d.Failed != 1 || len(d.Results) != 2 || d.Results[0].Status != "ok" || d.Results[1].Status != "fail" ||
		d.Results[1].ExitCode == nil || *d.Results[1].ExitCode != 3 || d.Workspace != filepath.Base(dir) {
		t.Errorf("記録: %+v", d)
	}
	if strings.Contains(d.Results[1].OutputTail, "hunter2") || !strings.Contains(d.Results[1].OutputTail, "password=***") {
		t.Errorf("マスクされていない: %q", d.Results[1].OutputTail)
	}
	// 直近が失敗なので規則で閉じられない
	if out, code := run("close", failing, "--comment", "検証"); code == 0 || !strings.Contains(out, "件が失敗しています") {
		t.Errorf("失敗後の close: code=%d %s", code, out)
	}
	// --last は出力つきで直近を出す
	if out, code := run("verify", failing, "--last"); code != 0 || !strings.Contains(out, "1/2 成功・1 失敗") || !strings.Contains(out, "| password=***") {
		t.Errorf("--last: code=%d %s", code, out)
	}

	// 全成功: exit 0、記録の後は閉じられる
	// 最後の行はサーバの文面（CLI が Accept-Language を送るので日本語で届く。rules_api_test.go と同じ事情）
	if out, code := run("verify", passing); code != 0 || !strings.Contains(out, "verify を記録: "+passing) || !lastEvent(passing).OK {
		t.Fatalf("全成功: code=%d %s", code, out)
	}
	if out, code := run("close", passing, "--comment", "検証コマンド成功"); code != 0 {
		t.Errorf("verify 後の close: code=%d %s", code, out)
	}
}
