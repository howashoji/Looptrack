package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// OpenTelemetry のファイル出力を有効にしていない（client = copilot のスナップショットが無い）利用者の GitHub Copilot の MCP の操作は、
// 付与の指示・クローズ時の必須（usage.require_on_close）・未付与の検知（coverage・summary）の対象にしない。判定は MCP の clientInfo（setup と同じ agentOf）。記録は issue_events.detail の agent。
func TestUsageCopilotUnmeasured(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"require_on_close": true}}`)); err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	agentOfEvent := func(issue, kind string) string {
		t.Helper()
		var agent *string
		if err := e.db.QueryRow(`SELECT JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.agent')) FROM issue_events e JOIN issues i ON i.id = e.issue_id
WHERE i.display_id = ? AND e.kind = ? ORDER BY e.id DESC LIMIT 1`, issue, kind).Scan(&agent); err != nil {
			t.Fatal(err)
		}
		if agent == nil {
			return ""
		}
		return *agent
	}
	coverage := func() coverageResp {
		t.Helper()
		var cov coverageResp
		ed.json(200, "GET", "/projects/req/usage/coverage?mine=1", nil, &cov)
		return cov
	}

	// 新しいプロトコル（要求ごとの clientInfo）と旧プロトコル（接続の記録）の両方で、Copilot と判定する
	for i, c := range []struct{ name, protocol string }{{"github-copilot-developer", ""}, {"Visual Studio Code", "2025-06-18"}} {
		m := e.mcpAsClient(ed.token, hdr, c.name, "1.0.0", c.protocol)
		text, _ := m.call("create_issue", map[string]any{"title": "Copilot から起票 " + c.name}, false)
		id := "REQ-000" + string(rune('1'+i))
		if !strings.Contains(text, "作成: "+id) || strings.Contains(text, "トークン情報が未付与") {
			t.Errorf("%s: 起票に付与の指示が出た: %s", c.name, text)
		}
		if text, _ = m.call("add_comment", map[string]any{"id": id, "text": "経過"}, false); strings.Contains(text, "トークン情報が未付与") {
			t.Errorf("%s: コメントに付与の指示が出た: %s", c.name, text)
		}
		// usage.require_on_close でも拒否されない（付けようがない）。警告も出ない
		text, _ = m.call("set_status", map[string]any{"id": id, "status": "Done", "comment": "検証済み"}, false)
		if !strings.Contains(text, "Done") || strings.Contains(text, "警告") || strings.Contains(text, "usage attach") {
			t.Errorf("%s: Done: %s", c.name, text)
		}
		if got := agentOfEvent(id, "create"); got != "copilot" {
			t.Errorf("%s: create の detail.agent = %q", c.name, got)
		}
		if got := agentOfEvent(id, "status"); got != "copilot" {
			t.Errorf("%s: status の detail.agent = %q", c.name, got)
		}
	}
	if cov := coverage(); cov.Target != 0 || cov.Missing != 0 {
		t.Errorf("Copilot の操作が未付与の検知の対象になった: %+v", cov)
	}

	// 対照: Claude Code の MCP の操作は従来どおり対象（指示が出る・Done は拒否・未付与に数える）
	cc := e.mcpAsClient(ed.token, hdr, "claude-code", "2.1.0", "")
	text, _ := cc.call("create_issue", map[string]any{"title": "Claude Code から起票"}, false)
	if !strings.Contains(text, "作成: REQ-0003") || !strings.Contains(text, "トークン情報が未付与です。次を実行してください: looptrack issue usage attach REQ-0003") {
		t.Errorf("Claude Code の起票に付与の指示が無い: %s", text)
	}
	if text, _ = cc.call("set_status", map[string]any{"id": "REQ-0003", "status": "Done"}, true); !strings.Contains(text, "usage attach REQ-0003") {
		t.Errorf("Claude Code の Done が規則 usage で拒否されない: %s", text)
	}
	if got := agentOfEvent("REQ-0003", "create"); got != "claude-code" {
		t.Errorf("Claude Code の create の detail.agent = %q", got)
	}
	if cov := coverage(); cov.Target != 1 || cov.Missing != 1 || len(cov.Items) != 1 || cov.Items[0].Issue != "REQ-0003" {
		t.Errorf("Claude Code の操作だけが対象のはず: %+v", cov)
	}
	// summary（SessionStart）の未付与の行も Claude Code の分だけ
	var sum struct {
		UsageMissing struct {
			Count int `json:"count"`
		} `json:"usage_missing"`
	}
	ed.json(200, "GET", "/projects/req/summary", nil, &sum)
	if sum.UsageMissing.Count != 1 {
		t.Errorf("summary の未付与 = %d（Copilot の操作を数えない）", sum.UsageMissing.Count)
	}
}

// OpenTelemetry のファイル出力を有効にした利用者の Copilot は、hook が client = copilot の
// スナップショットを送る。その利用者の Copilot の操作は、直近 7 日にそのスナップショットがあれば計測している AI として扱う
// （付与の指示・クローズ時の必須・未付与の検知の対象に戻る）。有効にする前の操作は、後から有効にしても対象に戻らない。
func TestUsageCopilotMeasured(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"require_on_close": true}}`)); err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"X-Looptrack-Project": "req"}
	coverage := func() coverageResp {
		t.Helper()
		var cov coverageResp
		ed.json(200, "GET", "/projects/req/usage/coverage?mine=1", nil, &cov)
		return cov
	}
	snap := func(trigger, issue, op, via string, total int64) {
		t.Helper()
		b := usageBody("20260918010000-abcdef", "copilot-sess-1", trigger, issue, op, total, 10, 1)
		b["client"], b["client_version"], b["at"] = "copilot", "1.0.83", e.clock.Now().UTC().Format(time.RFC3339)
		if via != "" {
			b["via"] = via
		} else {
			delete(b, "via")
		}
		ed.json(201, "POST", "/projects/req/usage", b, nil)
	}
	m := e.mcpAsClient(ed.token, hdr, "github-copilot-developer", "1.0.0", "")

	// 有効にする前（Copilot のスナップショットが無い）: 対象外
	text, _ := m.call("create_issue", map[string]any{"title": "有効にする前"}, false)
	if !strings.Contains(text, "作成: REQ-0001") || strings.Contains(text, "トークン情報が未付与") {
		t.Errorf("有効にする前の起票に付与の指示が出た: %s", text)
	}
	// 1 時間後に OTel を有効にした（ターン終了の hook が stop のスナップショットを送った）
	e.clock.Add(time.Hour)
	snap("stop", "", "", "", 100)

	// 有効にした後: 対象（フックの MCP の操作の送信がまだ無いので、応答に付与の指示が出る）
	text, _ = m.call("create_issue", map[string]any{"title": "有効にした後"}, false)
	if !strings.Contains(text, "作成: REQ-0002") || !strings.Contains(text, "トークン情報が未付与です") {
		t.Errorf("計測している Copilot の起票に付与の指示が無い: %s", text)
	}
	// usage.require_on_close: トークン情報が無ければ Done は拒否される
	if text, _ = m.call("set_status", map[string]any{"id": "REQ-0002", "status": "Done", "comment": "検証済み"}, true); !strings.Contains(text, "usage attach REQ-0002") {
		t.Errorf("計測している Copilot の Done が規則 usage で拒否されない: %s", text)
	}
	if cov := coverage(); cov.Target != 1 || cov.Missing != 1 || len(cov.Items) != 1 || cov.Items[0].Issue != "REQ-0002" {
		t.Errorf("有効にした後の起票だけが未付与のはず（有効にする前の REQ-0001 は対象外のまま）: %+v", cov)
	}

	// フック（PostToolUse）が起票の分を送ると付与済みになり、Done が通る
	snap("issue_op", "REQ-0002", "create", "mcp", 200)
	text, _ = m.call("set_status", map[string]any{"id": "REQ-0002", "status": "Done", "comment": "検証済み"}, false)
	if !strings.Contains(text, "Done") || strings.Contains(text, "警告") {
		t.Errorf("付与済みの Done: %s", text)
	}
	if cov := coverage(); cov.Target != 2 || cov.Missing != 0 {
		t.Errorf("起票と Done が対象・付与済みのはず: %+v", cov)
	}

	// 8 日たってスナップショットが途絶えた（OTel を無効にした）: 対象外に戻る
	e.clock.Add(8 * 24 * time.Hour)
	text, _ = m.call("create_issue", map[string]any{"title": "無効にした後"}, false)
	if !strings.Contains(text, "作成: REQ-0003") || strings.Contains(text, "トークン情報が未付与") {
		t.Errorf("計測をやめた Copilot の起票に付与の指示が出た: %s", text)
	}
	if text, _ = m.call("set_status", map[string]any{"id": "REQ-0003", "status": "Done", "comment": "検証済み"}, false); strings.Contains(text, "警告") {
		t.Errorf("計測をやめた Copilot の Done に警告: %s", text)
	}
}
