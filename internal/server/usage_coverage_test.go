package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/store"
)

// 付与漏れの検知（イベントとの突き合わせ）・変更の応答での指示・summary の件数・クローズ時の必須化。

type coverageResp struct {
	Target   int      `json:"target"`
	Attached int      `json:"attached"`
	Missing  int      `json:"missing_count"`
	Rate     *float64 `json:"rate"`
	Humans   int      `json:"humans"`
	Mine     bool     `json:"mine"`
	Issues   []string `json:"issues"`
	Items    []struct {
		Issue string `json:"issue"`
		Kind  string `json:"kind"`
		Via   string `json:"via"`
		User  string `json:"user"`
	} `json:"missing"`
	Command string `json:"command"`
}

type noticeResp struct {
	UsageNotice string `json:"usage_notice"`
}

func TestUsageCoverage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	other := e.user("other", "other-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	store.SetMember(ctx, e.db, pr.ID, other.ID, "editor")
	api := e.apiAs(u)
	cli := e.apiAs(u)
	cli.header["X-Looptrack-Client"] = "cli"
	ai := e.apiAs(u) // Claude Code の Bash から（セッション ID あり）
	ai.header["X-Looptrack-Client"], ai.header["X-Looptrack-Session"] = "cli", "sess-a"
	ai2 := e.apiAs(other)
	ai2.header["X-Looptrack-Client"], ai2.header["X-Looptrack-Session"] = "cli", "sess-b"
	snap := func(c *apiClient, conv, session, trigger, issue, op string, total int64) {
		t.Helper()
		defer e.clock.Add(time.Second) // 時計は止めてあるので、次の操作をスナップショットより後にする
		c.json(201, "POST", "/projects/req/usage", usageBody(conv, session, trigger, issue, op, total, 1, total), nil)
	}

	// AI の起票（応答に付与の指示）→ 直後にスナップショットが届く＝付与済み
	var n noticeResp
	ai.json(201, "POST", "/projects/req/issues", map[string]any{"title": "AI の起票"}, &n)
	if n.UsageNotice != "トークン情報が未付与です。次を実行してください: looptrack issue usage attach REQ-0001" {
		t.Errorf("起票の応答の指示: %q", n.UsageNotice)
	}
	snap(ai, "c1", "sess-a", "issue_op", "REQ-0001", "create", 100)
	// AI のコメント（スナップショットが届かない＝未付与）
	ai.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "送れなかった"}, &n)
	if !strings.Contains(n.UsageNotice, "usage attach REQ-0001") {
		t.Errorf("コメントの応答の指示: %q", n.UsageNotice)
	}
	// 人がターミナルから（セッション ID なし）・API 直接は対象外で、指示も出さない
	n = noticeResp{}
	cli.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "人の操作"}, &n)
	if n.UsageNotice != "" {
		t.Errorf("人の操作に指示が付いた: %q", n.UsageNotice)
	}
	api.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "API"}, &n)
	// 別の利用者の AI 操作（未付与）
	ai2.json(201, "POST", "/projects/req/issues", map[string]any{"title": "他人の起票"}, nil)
	// 10 分より後に届いたスナップショットは、その操作の付与とはみなさない
	ai.json(201, "POST", "/projects/req/issues", map[string]any{"title": "遅れて届く"}, nil)
	e.clock.Add(11 * time.Minute)
	snap(ai, "c1", "sess-a", "issue_op", "REQ-0003", "comment", 200)
	// 本文の更新（版つき）も対象
	var upd noticeResp
	ai.json(200, "PATCH", "/issues/REQ-0001", map[string]any{"priority": "P1"}, &upd, "If-Match", `"4"`)
	if !strings.Contains(upd.UsageNotice, "usage attach REQ-0001") {
		t.Errorf("更新の応答の指示: %q", upd.UsageNotice)
	}
	snap(ai, "c1", "sess-a", "issue_op", "REQ-0001", "update", 300)

	var cov coverageResp
	api.json(200, "GET", "/projects/req/usage/coverage?mine=1", nil, &cov)
	// 自分の AI 操作: 起票 REQ-0001（付与）・コメント（未付与）・起票 REQ-0003（遅れ＝未付与）・更新（付与）
	if cov.Target != 4 || cov.Attached != 2 || cov.Missing != 2 || cov.Humans != 1 || !cov.Mine || cov.Rate == nil || *cov.Rate != 0.5 {
		t.Errorf("充足率（自分）: %+v", cov)
	}
	if strings.Join(cov.Issues, ",") != "REQ-0003,REQ-0001" || cov.Items[0].Kind != "create" || cov.Items[1].Kind != "comment" || cov.Items[1].User != "editor" {
		t.Errorf("未付与の一覧: %+v", cov)
	}
	api.json(200, "GET", "/projects/req/usage/coverage", nil, &cov)
	if cov.Target != 5 || cov.Missing != 3 || cov.Mine || !strings.Contains(strings.Join(cov.Issues, ","), "REQ-0002") {
		t.Errorf("充足率（全員）: %+v", cov)
	}
	api.fail(400, "GET", "/projects/req/usage/coverage?days=0", nil)

	// summary: 呼び出した利用者の分だけ、件数と回収のコマンド
	var sum struct {
		UsageMissing struct {
			Count   int      `json:"count"`
			Issues  []string `json:"issues"`
			Message string   `json:"message"`
		} `json:"usage_missing"`
	}
	api.json(200, "GET", "/projects/req/summary", nil, &sum)
	if sum.UsageMissing.Count != 2 || !strings.Contains(sum.UsageMissing.Message, "トークン情報の未付与 2 件（直近 7 日・あなたの AI 操作・REQ-0003 REQ-0001）") ||
		!strings.Contains(sum.UsageMissing.Message, "looptrack issue usage attach <ID>") {
		t.Errorf("summary: %+v", sum.UsageMissing)
	}
	// 回収: 後から手動で付けた（manual）分は、10 分を過ぎていても付与済みになる
	e.clock.Add(time.Hour)
	ai.json(201, "POST", "/projects/req/usage", func() map[string]any {
		b := usageBody("c9", "sess-new", "manual", "", "", 10, 1, 1)
		b["issue"] = "REQ-0001"
		return b
	}(), nil)
	api.json(200, "GET", "/projects/req/summary", nil, &sum)
	if sum.UsageMissing.Count != 1 || strings.Join(sum.UsageMissing.Issues, ",") != "REQ-0003" {
		t.Errorf("回収後の summary: %+v", sum.UsageMissing)
	}

	// MCP: フックが働いていない利用者には毎回指示を出し、フック（via=mcp のスナップショット）が届いた後は出さない
	m := e.mcpAs(ai.token, map[string]string{"X-Looptrack-Project": "req"})
	text, _ := m.call("add_comment", map[string]any{"id": "REQ-0003", "text": "MCP から"}, false)
	if !strings.HasSuffix(text, "\nトークン情報が未付与です。次を実行してください: looptrack issue usage attach REQ-0003") {
		t.Errorf("MCP の指示: %s", text)
	}
	hook := usageBody("c1", "sess-a", "issue_op", "REQ-0003", "comment", 400, 1, 400)
	hook["via"], hook["tool_use_id"] = "mcp", "toolu_1"
	ai.json(201, "POST", "/projects/req/usage", hook, nil)
	if text, _ = m.call("add_comment", map[string]any{"id": "REQ-0003", "text": "フックあり"}, false); text != "コメント追記: REQ-0003" {
		t.Errorf("フックがある利用者への指示: %s", text)
	}
	text, data := m.call("usage_missing", map[string]any{}, false)
	if !strings.Contains(text, "トークン情報の充足率") || !strings.Contains(text, "回収: イシューごとに looptrack issue usage attach <ID>") || data["missing_count"] == nil {
		t.Errorf("usage_missing: %s %v", text, data)
	}
	text, _ = m.call("project_summary", map[string]any{}, false)
	if !strings.Contains(text, "トークン情報の未付与 ") {
		t.Errorf("project_summary に未付与が無い: %s", text)
	}
	// クローズで、その会話のトークン情報がイシューに無いときは（既定は）警告だけ
	ai2.json(201, "POST", "/projects/req/issues", map[string]any{"title": "付与なしで閉じる"}, nil) // REQ-0004
	var st noticeResp
	ai2.json(200, "POST", "/issues/REQ-0004/status", map[string]any{"status": "Done"}, &st)
	if !strings.HasPrefix(st.UsageNotice, "警告: REQ-0004 をクローズしましたが、この会話のトークン情報が REQ-0004 にまだありません。次を実行してください: looptrack issue usage attach REQ-0004") {
		t.Errorf("クローズの警告: %q", st.UsageNotice)
	}
}

func TestUsageRequiredOnClose(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"require_on_close": true}}`)); err != nil {
		t.Fatal(err)
	}
	ai := e.apiAs(u)
	ai.header["X-Looptrack-Client"], ai.header["X-Looptrack-Session"] = "cli", "sess-a"
	human := e.apiAs(u)
	human.header["X-Looptrack-Client"] = "cli"
	for i := 0; i < 4; i++ {
		ai.json(201, "POST", "/projects/req/issues", map[string]any{"title": "課題"}, nil)
	}

	// その会話のスナップショットが無いクローズは 422。メッセージに次のコマンド
	f := ai.fail(422, "POST", "/issues/REQ-0001/status", map[string]any{"status": "Done", "comment": "完了"})
	if f.Error.Rule != "usage_required_on_close" || !f.Error.Overridable ||
		!strings.Contains(f.Error.Message, "looptrack issue usage attach REQ-0001") || !strings.Contains(f.Error.Message, "--override") {
		t.Errorf("拒否: %+v", f.Error)
	}
	ai.fail(422, "POST", "/issues/REQ-0001/status", map[string]any{"status": "Canceled"})
	// 途中の状態は対象外
	ai.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "In Progress"}, nil)
	// 付けた後は通る
	ai.json(201, "POST", "/projects/req/usage", usageBody("c1", "sess-a", "manual", "", "", 0, 0, 0), nil)
	b := usageBody("c1", "sess-a", "manual", "", "", 10, 1, 1)
	b["issue"] = "REQ-0001"
	ai.json(201, "POST", "/projects/req/usage", b, nil)
	var st noticeResp
	ai.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "Done"}, &st)
	if strings.HasPrefix(st.UsageNotice, "警告") {
		t.Errorf("付与済みのクローズに警告: %q", st.UsageNotice)
	}
	// 再開でセッション ID が変わっても、同じ会話（conversation_id）の分を数える
	resumed := e.apiAs(u)
	resumed.header["X-Looptrack-Client"], resumed.header["X-Looptrack-Session"] = "cli", "sess-a2"
	b = usageBody("c1", "sess-a2", "stop", "", "", 20, 2, 2)
	resumed.json(201, "POST", "/projects/req/usage", b, nil)
	b = usageBody("c1", "sess-a", "manual", "", "", 15, 1, 1)
	b["issue"] = "REQ-0002"
	ai.json(201, "POST", "/projects/req/usage", b, nil)
	resumed.json(200, "POST", "/issues/REQ-0002/status", map[string]any{"status": "Done"}, nil)
	// 別の会話の分は数えない
	other := e.apiAs(u)
	other.header["X-Looptrack-Client"], other.header["X-Looptrack-Session"] = "cli", "sess-z"
	other.fail(422, "POST", "/issues/REQ-0002/status", map[string]any{"status": "Canceled"})

	// 理由つきの上書きは通り、rule_override に残る
	ai.json(200, "POST", "/issues/REQ-0003/status", map[string]any{"status": "Done", "override_reason": "利用者が端末で確認済み"}, nil)
	var detail string
	if err := e.db.QueryRow(`SELECT CAST(detail AS CHAR) FROM issue_events WHERE kind = 'rule_override'`).Scan(&detail); err != nil ||
		!strings.Contains(detail, "usage_required_on_close") || !strings.Contains(detail, "利用者が端末で確認済み") {
		t.Errorf("上書きの記録: %v %s", err, detail)
	}
	// 人がターミナルから閉じる（セッション ID なし）のは対象外
	human.json(200, "POST", "/issues/REQ-0004/status", map[string]any{"status": "Done"}, nil)

	// MCP の set_status も同じ規則（セッション ID が無いので、その利用者のスナップショットで判定）
	ai.json(201, "POST", "/projects/req/issues", map[string]any{"title": "MCP で閉じる"}, nil) // REQ-0005
	m := e.mcpAs(ai.token, map[string]string{"X-Looptrack-Project": "req"})
	text, _ := m.call("set_status", map[string]any{"id": "REQ-0005", "status": "Done"}, true)
	if !strings.Contains(text, "looptrack issue usage attach REQ-0005") {
		t.Errorf("MCP の拒否: %s", text)
	}
	b = usageBody("c3", "sess-c", "issue_op", "REQ-0005", "create", 30, 3, 3)
	b["via"] = "mcp"
	ai.json(201, "POST", "/projects/req/usage", b, nil)
	if text, _ = m.call("set_status", map[string]any{"id": "REQ-0005", "status": "Done"}, false); text != "REQ-0005: Todo → Done" {
		t.Errorf("MCP の付与後のクローズ: %s", text)
	}

	// 設定の検査: 状態でない値は拒否
	if _, err := domain.ParseRules([]byte(`{"usage": {"require_on_close": true, "statuses": ["Closed"]}}`)); err == nil {
		t.Error("不正な statuses を受け付けた")
	}
}

// CLI: クローズ時の必須化で拒否されたら先に付けて 1 回だけやり直す・付けられなければ拒否のメッセージ・
// 付与に失敗したときだけ指示を表示・usage missing・summary の未付与の行。
func TestUsageCLIRecovery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"require_on_close": true}}`)); err != nil {
		t.Fatal(err)
	}
	token := e.apiAs(u).token
	home := t.TempDir()
	claudeTranscript(t, home, "sess-cli")
	run := func(env []string, args ...string) (int, string, string) {
		t.Helper()
		e.clock.Add(time.Second)
		base := append(cliAPIEnv(e.srv.URL+"/im", "req", token, home), "XDG_CONFIG_HOME="+filepath.Join(home, "cfg"), "CLAUDE_PROJECT_DIR="+home)
		r := runCLI(t, home, append(base, env...), "", append([]string{"issue"}, args...)...)
		return r.code, r.stdout, r.stderr
	}
	inClaude := []string{"CLAUDE_CODE_SESSION_ID=sess-cli", "CLAUDECODE=1"}
	noUsage := append([]string{"LOOPTRACK_USAGE=0"}, inClaude...)

	// 付与を切って起票（トークン情報なし）→ close は拒否されるが、CLI が先に付けてやり直すので通る
	if code, out, errOut := run(noUsage, "new", "付与なしの起票"); code != 0 || !strings.Contains(out, "REQ-0001") {
		t.Fatalf("new: %d %s %s", code, out, errOut)
	}
	// 2 行目はサーバの文面（CLI が Accept-Language を送るので日本語で届く。rules_api_test.go と同じ事情）
	if code, out, errOut := run(inClaude, "close", "REQ-0001", "--comment", "完了"); code != 0 || out != "REQ-0001: Todo → Done\nコメント追記: REQ-0001\n" || errOut != "" {
		t.Errorf("close（自動で付与してやり直す）: %d %q %q", code, out, errOut)
	}
	var manual int
	e.db.QueryRow("SELECT COUNT(*) FROM usage_snapshots WHERE trigger_kind = 'manual'").Scan(&manual)
	if manual != 1 {
		t.Errorf("やり直しの前の手動付与 = %d", manual)
	}

	// 会話記録が無い（付けられない）ときは拒否のメッセージのまま exit 1。--override なら通る
	run(noUsage, "new", "記録なし")
	noTranscript := []string{"CLAUDE_CODE_SESSION_ID=no-such-session", "CLAUDECODE=1"}
	code, _, errOut := run(noTranscript, "status", "REQ-0002", "Done")
	if code != 1 || !strings.Contains(errOut, "エラー: REQ-0002 を「Done」にするには") || !strings.Contains(errOut, "looptrack issue usage attach REQ-0002") {
		t.Errorf("付けられないときの拒否: %d %s", code, errOut)
	}
	if code, out, errOut := run(noTranscript, "close", "REQ-0002", "--override", "端末で確認済み"); code != 0 || !strings.Contains(out, "REQ-0002: Todo → Done") || errOut != "" {
		t.Errorf("--override: %d %s %s", code, out, errOut)
	}

	// 付与に失敗したとき（サーバが受け付けない記録）だけ、サーバの指示を標準エラーに出す
	claudeTranscript(t, home, "sess-cli",
		`{"type":"assistant","timestamp":"2099-01-01T00:00:00.000Z","message":{"id":"m9","model":"claude-opus-5","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`)
	code, out, errOut := run(inClaude, "new", "送れない起票")
	if code != 0 || !strings.Contains(out, "作成: REQ-0003") || !strings.Contains(errOut, "トークン情報が未付与です。次を実行してください: looptrack issue usage attach REQ-0003") {
		t.Errorf("付与の失敗時の指示: %d %s %s", code, out, errOut)
	}
	claudeTranscript(t, home, "sess-cli")

	// usage missing（自分の AI 操作・充足率）と summary の未付与の行。REQ-0001 の起票は付与を切っていたが、
	// 10 分以内にクローズ前の付与が届いたので付与済み。REQ-0002 は起票とクローズ、REQ-0003 は起票が未付与
	code, out, _ = run(inClaude, "usage", "missing")
	if code != 0 || !strings.Contains(out, "未付与 3 件") || !strings.Contains(out, "REQ-0003") || !strings.Contains(out, "回収: イシューごとに looptrack issue usage attach <ID> を実行（対象 REQ-0003 REQ-0002）") ||
		!strings.Contains(out, "トークン情報の充足率 40.0%（直近 30 日・自分の AI 操作 5 件中 2 件に付与") {
		t.Errorf("usage missing: %d %s", code, out)
	}
	var cov coverageResp
	_, out, _ = run(nil, "usage", "missing", "--json", "--all-users", "--days", "3")
	if err := json.Unmarshal([]byte(out), &cov); err != nil || cov.Missing != 3 || cov.Mine {
		t.Errorf("usage missing --json: %v %s", err, out)
	}
	// 付与漏れの行はサーバの文面（同上で日本語）
	if code, out, _ = run(inClaude, "summary"); code != 0 || !strings.Contains(out, "トークン情報の未付与 3 件（直近 7 日・あなたの AI 操作・REQ-0003 REQ-0002）") {
		t.Errorf("summary: %d %s", code, out)
	}
	// 回収すると消える
	for _, id := range []string{"REQ-0002", "REQ-0003"} {
		if code, out, errOut := run(inClaude, "usage", "attach", id); code != 0 || !strings.Contains(out, "トークン情報:") {
			t.Fatalf("attach %s: %d %s %s", id, code, out, errOut)
		}
	}
	if _, out, _ = run(inClaude, "summary"); strings.Contains(out, "未付与") {
		t.Errorf("回収後の summary: %s", out)
	}
	if code, _, errOut := run(nil, "usage", "show"); code != 1 || !strings.Contains(errOut, "イシュー ID を指定してください") {
		t.Errorf("usage show（ID なし）: %d %s", code, errOut)
	}
}

// TestUsageHostSessionNotTarget は、器のセッション ID（X-Looptrack-Session-Kind: host）で来た CLI の操作が
// 付与の対象にならないことを、応答の指示・充足率・summary・usage.require_on_close の 4 つで確かめる。
// 判定の表（環境変数 4 通り × (a) ヘッダ (b) 付与の対象 (c) 会話記録）は TestHostSessionKindTable。
func TestUsageHostSessionNotTarget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("req")
	u := e.user("editor", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"require_on_close": true}}`)); err != nil {
		t.Fatal(err)
	}
	api := e.apiAs(u)
	// ② 器のセッション ID だけが渡る窓（会話記録を引けないので、トークン情報を付けようがない）
	host := e.apiAs(u)
	host.header["X-Looptrack-Client"] = "cli"
	host.header["X-Looptrack-Session"] = "host-1"
	host.header["X-Looptrack-Session-Kind"] = "host"
	// ① 会話のセッション ID（従来どおり付与の対象）
	ai := e.apiAs(u)
	ai.header["X-Looptrack-Client"], ai.header["X-Looptrack-Session"] = "cli", "sess-a"
	// 印を送らない古い CLI（同じ器の ID を、種類を伝えずに送る）→ 本件より前と同じ判定（対象）
	old := e.apiAs(u)
	old.header["X-Looptrack-Client"], old.header["X-Looptrack-Session"] = "cli", "host-old"
	// サーバが知らない種類 → 読み捨てて、今までどおり会話のセッション ID として扱う
	unknown := e.apiAs(u)
	unknown.header["X-Looptrack-Client"], unknown.header["X-Looptrack-Session"] = "cli", "sess-u"
	unknown.header["X-Looptrack-Session-Kind"] = "something-new"

	// 付与の指示は出ない（付けるものが無い）
	var n noticeResp
	host.json(201, "POST", "/projects/req/issues", map[string]any{"title": "器の窓からの起票"}, &n) // REQ-0001
	if n.UsageNotice != "" {
		t.Errorf("器のセッション ID の操作に付与の指示が付いた: %q", n.UsageNotice)
	}
	host.json(201, "POST", "/issues/REQ-0001/comments", map[string]any{"text": "器の窓からのコメント"}, &n)
	if n.UsageNotice != "" {
		t.Errorf("器のセッション ID のコメントに付与の指示が付いた: %q", n.UsageNotice)
	}
	// 従来どおりの経路は指示が出る
	n = noticeResp{}
	ai.json(201, "POST", "/projects/req/issues", map[string]any{"title": "会話のセッション ID"}, &n) // REQ-0002
	if !strings.Contains(n.UsageNotice, "usage attach REQ-0002") {
		t.Errorf("会話のセッション ID の起票に指示が無い: %q", n.UsageNotice)
	}
	n = noticeResp{}
	old.json(201, "POST", "/projects/req/issues", map[string]any{"title": "印を送らない古い CLI"}, &n) // REQ-0003
	if !strings.Contains(n.UsageNotice, "usage attach REQ-0003") {
		t.Errorf("古い CLI の起票に指示が無い（本件より前と同じ判定になっていない）: %q", n.UsageNotice)
	}
	n = noticeResp{}
	unknown.json(201, "POST", "/projects/req/issues", map[string]any{"title": "知らない種類"}, &n) // REQ-0004
	if !strings.Contains(n.UsageNotice, "usage attach REQ-0004") {
		t.Errorf("知らない種類を読み捨てていない: %q", n.UsageNotice)
	}

	// 充足率: 器の窓の 2 件（起票・コメント）は対象にも humans にも数えない
	var cov coverageResp
	api.json(200, "GET", "/projects/req/usage/coverage?mine=1", nil, &cov)
	if cov.Target != 3 || cov.Humans != 0 {
		t.Errorf("充足率: target=%d humans=%d（期待 target=3・器の窓の 2 件は数えない）: %+v", cov.Target, cov.Humans, cov)
	}
	for _, it := range cov.Items {
		if it.Issue == "REQ-0001" {
			t.Errorf("未付与の一覧に器の窓の操作が出た: %+v", it)
		}
	}
	// summary の usage_missing も同じ
	var sum struct {
		UsageMissing struct {
			Count  int      `json:"count"`
			Issues []string `json:"issues"`
		} `json:"usage_missing"`
	}
	api.json(200, "GET", "/projects/req/summary", nil, &sum)
	if sum.UsageMissing.Count != 3 || strings.Contains(strings.Join(sum.UsageMissing.Issues, ","), "REQ-0001") {
		t.Errorf("summary の未付与に器の窓の操作が入った: %+v", sum.UsageMissing)
	}
	// 実測値を記録に残す（器の窓の 2 件を足しても数が増えないこと）
	t.Logf("器の窓の操作 2 件（REQ-0001 の起票・コメント）を含む全 5 件のうち、coverage の target=%d humans=%d・summary の usage_missing=%d %v",
		cov.Target, cov.Humans, sum.UsageMissing.Count, sum.UsageMissing.Issues)

	// usage.require_on_close: 器の窓の Done は通る（付けようがないものを必須にしない）
	n = noticeResp{}
	host.json(200, "POST", "/issues/REQ-0001/status", map[string]any{"status": "Done", "comment": "完了"}, &n)
	if strings.HasPrefix(n.UsageNotice, "警告") {
		t.Errorf("器の窓のクローズに警告が付いた: %q", n.UsageNotice)
	}
	t.Logf("usage.require_on_close のプロジェクトで、器の窓からの REQ-0001 の Done = 200（usage_notice=%q）", n.UsageNotice)
	// 会話のセッション ID と、印を送らない古い CLI は今までどおり 422
	if f := ai.fail(422, "POST", "/issues/REQ-0002/status", map[string]any{"status": "Done"}); f.Error.Rule != "usage_required_on_close" {
		t.Errorf("会話のセッション ID のクローズ: %+v", f.Error)
	}
	if f := old.fail(422, "POST", "/issues/REQ-0003/status", map[string]any{"status": "Done"}); f.Error.Rule != "usage_required_on_close" {
		t.Errorf("古い CLI のクローズ（本件より前と同じ判定になっていない）: %+v", f.Error)
	}

	// 器の窓の操作も session_id は記録する（どのセッションが持っているかは見分けられる）
	var sid, detail string
	if err := e.db.QueryRow(`SELECT COALESCE(e.session_id, ''), COALESCE(CAST(e.detail AS CHAR), '') FROM issue_events e
JOIN issues i ON i.id = e.issue_id WHERE i.display_id = 'REQ-0001' AND e.kind = 'create'`).Scan(&sid, &detail); err != nil {
		t.Fatal(err)
	}
	// detail は JSON 列。MySQL は正規化して返す（「": "」の空白が入る）ので、文字列の一致ではなく JSON として読んで値を見る。
	var det map[string]any
	if err := json.Unmarshal([]byte(detail), &det); err != nil {
		t.Fatalf("detail を JSON として読めません: %v: %s", err, detail)
	}
	if sid != "host-1" || det["session_kind"] != store.SessionKindHost {
		t.Errorf("記録: session_id=%q detail=%q", sid, detail)
	}
}
