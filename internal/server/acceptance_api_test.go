package server

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 受け入れ条件の記入の関門（acceptance.require_on_close。DESIGN.md §9-1）を経路の上で確かめる。
// 単体テスト（internal/domain/acceptance_test.go）は判定関数だけを見るので、
// 「規則が読み込まれる」「checkStatus を通る」「上書きの理由が記録される」はここでしか捕まらない。

// acceptancePlaceholderLine は起票の雛形が入れる受け入れ条件の 1 行（日本語）。
// 対訳表から取る（写すと片方だけ直されて黙って壊れる）。
func acceptancePlaceholderLine(t *testing.T) string {
	t.Helper()
	line := strings.TrimSpace(domain.AcceptanceCriteria(i18n.T(i18n.JA, "domain.template.acceptance")))
	if line == "" {
		t.Fatal("雛形の受け入れ条件を対訳表から取れない")
	}
	return line
}

// dropAcceptanceSection は本文から「## 受け入れ条件」の節（見出しから次の ## 見出しの前まで）を外す。
// 文字列の完全一致で消すと、描画の空行の入り方が変わっただけで黙って空振りするので、見出しで区切る。
func dropAcceptanceSection(md string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(md, "\n") {
		if domain.AcceptanceHeading.MatchString(line) {
			skip = true
			continue
		}
		if skip {
			if strings.HasPrefix(line, "## ") {
				skip = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestAcceptanceRequireOnClose(t *testing.T) {
	ctx := context.Background()
	v := newVerifyEnv(t, "ac", `{"acceptance": {"require_on_close": true}}`)
	placeholder := acceptancePlaceholderLine(t)

	// 1. 雛形のまま Done にすると 422（rule_violation・overridable）。
	// CLI と Web はこの REST（POST /issues/{id}/status）をそのまま叩くので、この 1 件で REST・CLI・Web の
	// 3 経路を覆う（CLI は internal/client/api、Web は board.html の fetch が同じ API を呼ぶ）。MCP は別の
	// ハンドラなので 6 で別に確かめる。
	id := v.create("雛形のまま", "説明だけ")
	e := v.ed.rule("acceptance_required_on_close", "POST", "/issues/"+id+"/status", map[string]any{"status": "Done", "comment": "終わり"})
	want := i18n.T(i18n.JA, "domain.rules.acceptance_required_on_close", "id", id, "status", "Done", "command", domain.AcceptanceEditCommand(id))
	if e.Error.Message != want {
		t.Errorf("雛形のままの文面:\n got %q\nwant %q", e.Error.Message, want)
	}
	if !e.Error.Overridable {
		t.Errorf("上書き可の印が無い: %+v", e.Error)
	}
	if !strings.Contains(e.Error.Message, domain.AcceptanceEditCommand(id)) {
		t.Errorf("案内のコマンドが文面に無い: %q", e.Error.Message)
	}
	if st := v.ed.state(id); st.status != "Todo" {
		t.Errorf("拒否したのに状態が変わった: %+v", st)
	}

	// 2. 対象外の状態（In Progress）は止めない。next も同じ checkStatus を通る
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress"}, nil)

	// 3. 受け入れ条件を書けば通る（短い条件を誤って弾かないことの経路上の確認）
	var d issueDetailJSON
	v.ed.json(200, "GET", "/issues/"+id, nil, &d)
	filled := strings.Replace(d.Markdown, placeholder, "- [ ] CI が緑", 1)
	if filled == d.Markdown {
		t.Fatalf("起票の雛形の行が本文に無い（%q）", placeholder)
	}
	v.ed.json(200, "PATCH", "/issues/"+id, map[string]any{"markdown": filled}, nil, "If-Match", strconv.Itoa(d.Version))
	v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done", "comment": "検証結果: CI 緑"}, nil)

	// 4. Canceled には効かない（statuses の既定は Done だけ）
	cancel := v.create("取りやめ", "説明だけ")
	v.ed.json(200, "POST", "/issues/"+cancel+"/status", map[string]any{"status": "Canceled"}, nil)

	// 5. 理由を付ければ通り、rule_override に規則と理由が残る
	ov := v.create("上書き", "説明だけ")
	reason := "記録のみのイシューのため受け入れ条件なし（利用者の指示）"
	v.ed.json(200, "POST", "/issues/"+ov+"/status", map[string]any{"status": "Done", "override_reason": reason}, nil)
	var rule, got string
	if err := v.db.QueryRow(`SELECT JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.rule')), JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.reason'))
FROM issue_events e JOIN issues i ON i.id = e.issue_id WHERE i.display_id = ? AND e.kind = 'rule_override'`, ov).Scan(&rule, &got); err != nil {
		t.Fatalf("rule_override のイベントが無い: %v", err)
	}
	if rule != "acceptance_required_on_close" || got != reason {
		t.Errorf("rule_override: rule=%q reason=%q", rule, got)
	}

	// 6. MCP の経路（別のハンドラ）でも同じに拒否し、上書きで通る
	m := v.mcpAs(v.ed.token, map[string]string{"X-Looptrack-Project": "ac"})
	mid := v.create("MCP", "説明だけ")
	text, _ := m.call("set_status", map[string]any{"id": mid, "status": "Done"}, true)
	if !strings.Contains(text, domain.AcceptanceEditCommand(mid)) {
		t.Errorf("MCP set_status の拒否の文面: %q", text)
	}
	if st := v.ed.state(mid); st.status != "Todo" {
		t.Errorf("MCP で拒否したのに状態が変わった: %+v", st)
	}
	m.call("set_status", map[string]any{"id": mid, "status": "Done", "override_reason": "記録のみ（利用者の指示）"}, false)
	if st := v.ed.state(mid); st.status != "Done" {
		t.Errorf("MCP の上書きが通っていない: %+v", st)
	}

	// 7. 「## 受け入れ条件」節そのものが無い本文には効かない（決めた線引き）
	nosec := v.create("節なし", "説明だけ")
	v.ed.json(200, "GET", "/issues/"+nosec, nil, &d)
	dropped := dropAcceptanceSection(d.Markdown)
	if dropped == d.Markdown || domain.HasAcceptanceSection(dropped) {
		t.Fatal("受け入れ条件の節を本文から外せない")
	}
	v.ed.json(200, "PATCH", "/issues/"+nosec, map[string]any{"markdown": dropped}, nil, "If-Match", strconv.Itoa(d.Version))
	v.ed.json(200, "POST", "/issues/"+nosec+"/status", map[string]any{"status": "Done"}, nil)

	// 8. 起票で Done にするのは止めない（雛形が必ず入るため起票には掛けていない。線引きの記録）
	v.ed.json(201, "POST", "/projects/ac/issues", map[string]any{"title": "起票で Done", "status": "Done"}, nil)

	// 9. 規則が未設定なら何も起きない（既存の導入先の挙動を変えないことの証拠）
	if err := store.SetRules(ctx, v.db, v.pr.ID, nil); err != nil {
		t.Fatal(err)
	}
	off := v.create("規則なし", "説明だけ")
	v.ed.json(200, "POST", "/issues/"+off+"/status", map[string]any{"status": "Done", "comment": "終わり"}, nil)
}

// 受け入れ条件が雛形のままの着手は止めずに注意を返す（domain.AcceptanceStartNotice。判定は service の setStatus の 1 か所）。
// REST の status（CLI の issue status と Web が叩く）・REST の next（CLI の issue next）・MCP の set_status・next の
// 4 経路で、状態が In Progress に変わり、応答に同じ注意が載ることを確かめる。誤検知の側（載らない）も同じ経路で見る。
func TestAcceptanceNoticeOnStart(t *testing.T) {
	const rules = `{"acceptance": {"require_on_close": true}}`
	want := func(id string) string {
		return i18n.T(i18n.JA, "domain.rules.acceptance_template_on_start", "id", id, "statuses", "Done", "command", domain.AcceptanceEditCommand(id))
	}

	t.Run("REST status", func(t *testing.T) {
		v := newVerifyEnv(t, "an", rules)
		id := v.create("雛形のまま", "説明だけ")
		var res struct {
			Messages         []string `json:"messages"`
			AcceptanceNotice string   `json:"acceptance_notice"`
		}
		v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress"}, &res)
		if st := v.ed.state(id); st.status != "In Progress" {
			t.Fatalf("注意だけのはずが着手を止めた: %+v", st)
		}
		if res.AcceptanceNotice != want(id) {
			t.Errorf("acceptance_notice:\n got %q\nwant %q", res.AcceptanceNotice, want(id))
		}
		if !slicesContains(res.Messages, want(id)) {
			t.Errorf("messages に注意の行が無い（CLI が表示しない）: %q", res.Messages)
		}

		// 誤検知の側: In Progress → In Progress（着手ではない）・Todo へ戻す・記入済みの着手では載らない
		for _, to := range []string{"In Progress", "Todo"} {
			res.Messages, res.AcceptanceNotice = nil, ""
			v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": to}, &res)
			if res.AcceptanceNotice != "" || strings.Contains(strings.Join(res.Messages, "\n"), domain.AcceptanceEditCommand(id)) {
				t.Errorf("%s への変更で注意が出た: %+v", to, res)
			}
		}
		var d issueDetailJSON
		v.ed.json(200, "GET", "/issues/"+id, nil, &d)
		filled := strings.Replace(d.Markdown, acceptancePlaceholderLine(t), "- [ ] CI が緑", 1)
		if filled == d.Markdown {
			t.Fatal("起票の雛形の行が本文に無い")
		}
		v.ed.json(200, "PATCH", "/issues/"+id, map[string]any{"markdown": filled}, nil, "If-Match", strconv.Itoa(d.Version))
		res.Messages, res.AcceptanceNotice = nil, ""
		v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress"}, &res)
		if res.AcceptanceNotice != "" || len(res.Messages) != 1 {
			t.Errorf("記入済みの着手で注意が出た: %+v", res)
		}

		// 節が無い本文の着手では載らない
		nosec := v.create("節なし", "説明だけ")
		v.ed.json(200, "GET", "/issues/"+nosec, nil, &d)
		v.ed.json(200, "PATCH", "/issues/"+nosec, map[string]any{"markdown": dropAcceptanceSection(d.Markdown)}, nil, "If-Match", strconv.Itoa(d.Version))
		res.Messages, res.AcceptanceNotice = nil, ""
		v.ed.json(200, "POST", "/issues/"+nosec+"/status", map[string]any{"status": "In Progress"}, &res)
		if res.AcceptanceNotice != "" {
			t.Errorf("節の無い本文の着手で注意が出た: %q", res.AcceptanceNotice)
		}
	})

	t.Run("REST next", func(t *testing.T) {
		v := newVerifyEnv(t, "ann", rules)
		id := v.create("雛形のまま", "説明だけ")
		var n nextJSON
		v.ed.json(200, "POST", "/projects/ann/next", map[string]any{"dry_run": true}, &n)
		if n.Action != "would_start" || n.AcceptanceNotice != "" {
			t.Errorf("dry_run で注意が出た（着手していない）: action=%q notice=%q", n.Action, n.AcceptanceNotice)
		}
		v.ed.json(200, "POST", "/projects/ann/next", map[string]any{}, &n)
		if n.Action != "started" || n.Issue == nil || n.Issue.ID != id {
			t.Fatalf("next が着手しない: action=%q", n.Action)
		}
		if n.AcceptanceNotice != want(id) || !strings.Contains(n.Text, want(id)) {
			t.Errorf("next の注意: notice=%q\ntext=%s", n.AcceptanceNotice, n.Text)
		}
		n = nextJSON{}
		v.ed.json(200, "POST", "/projects/ann/next", map[string]any{}, &n)
		if n.Action != "resumed" || n.AcceptanceNotice != "" || strings.Contains(n.Text, want(id)) {
			t.Errorf("resumed で注意が出た（着手ではない）: action=%q notice=%q", n.Action, n.AcceptanceNotice)
		}
	})

	t.Run("MCP", func(t *testing.T) {
		v := newVerifyEnv(t, "anm", rules)
		m := v.mcpAs(v.ed.token, map[string]string{"X-Looptrack-Project": "anm"})
		id := v.create("雛形のまま", "説明だけ")
		text, _ := m.call("set_status", map[string]any{"id": id, "status": "In Progress"}, false)
		if !strings.Contains(text, domain.AcceptanceEditCommand(id)) || !strings.Contains(text, id) {
			t.Errorf("MCP set_status の本文に注意が無い: %q", text)
		}
		if st := v.ed.state(id); st.status != "In Progress" {
			t.Fatalf("MCP で着手を止めた: %+v", st)
		}
		m.call("set_status", map[string]any{"id": id, "status": "Done", "override_reason": "記録のみ（テスト）"}, false)
		nid := v.create("next で着手", "説明だけ")
		text, _ = m.call("next", map[string]any{}, false)
		if !strings.Contains(text, domain.AcceptanceEditCommand(nid)) {
			t.Errorf("MCP next の本文に注意が無い: %q", text)
		}
	})

	t.Run("規則なし", func(t *testing.T) {
		v := newVerifyEnv(t, "ano", "")
		id := v.create("雛形のまま", "説明だけ")
		var res map[string]any
		v.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "In Progress"}, &res)
		if _, ok := res["acceptance_notice"]; ok {
			t.Errorf("規則が未設定なのに注意が出た: %v", res)
		}
	})
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
