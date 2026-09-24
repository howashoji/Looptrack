package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 指示文の作業名（segments の label）はプロジェクト別ルール usage.send_prompts が許すときだけ保存する。
// 応答の send_prompts で CLI / フックにルールを知らせる。管理者は Web のプロジェクト管理画面で切り替えられる。

// rulesOfProject はプロジェクトの projects.rules をキーごとの生の JSON で返す。
func rulesOfProject(t *testing.T, e *env, slug string) map[string]json.RawMessage {
	t.Helper()
	pr, err := store.ProjectBySlug(context.Background(), e.db, slug)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]json.RawMessage{}
	if pr.Rules != nil {
		if err := json.Unmarshal(pr.Rules, &m); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func storedSegments(t *testing.T, e *env, id int64) []map[string]any {
	t.Helper()
	var raw []byte
	if err := e.db.QueryRowContext(context.Background(), "SELECT segments FROM usage_snapshots WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var segs []map[string]any
	if err := json.Unmarshal(raw, &segs); err != nil {
		t.Fatalf("segments: %v %s", err, raw)
	}
	return segs
}

func TestUsageSendPromptsRule(t *testing.T) {
	e, pr, ed := newAPIEnv(t)
	ctx := context.Background()
	type postRes struct {
		ID          int64 `json:"id"`
		Duplicate   bool  `json:"duplicate"`
		SendPrompts *bool `json:"send_prompts"`
	}
	withLabels := func(session string, in int64) map[string]any {
		b := usageBody("c-"+session, session, "stop", "", "", in, 1, 1)
		b["segments"] = []map[string]any{
			{"start": "2026-09-18T01:00:00Z", "kind": "human", "main": in, "label": "顧客 A の障害を調べて"},
			{"start": "2026-09-18T01:05:00Z", "kind": "auto", "main": 1},
		}
		return b
	}

	// ルールなし（既定）: label は保存されず、応答は send_prompts=false。label 以外の項目は残る
	var res postRes
	ed.json(201, "POST", "/projects/req/usage", withLabels("s1", 100), &res)
	if res.SendPrompts == nil || *res.SendPrompts {
		t.Fatalf("既定の応答: %+v", res)
	}
	segs := storedSegments(t, e, res.ID)
	if len(segs) != 2 || segs[0]["label"] != nil || segs[0]["kind"] != "human" || segs[0]["main"] != float64(100) || segs[1]["kind"] != "auto" {
		t.Errorf("既定で label が残った / 他の項目が消えた: %v", segs)
	}
	// 重複の応答にも載る
	ed.json(200, "POST", "/projects/req/usage", withLabels("s1", 100), &res)
	if !res.Duplicate || res.SendPrompts == nil || *res.SendPrompts {
		t.Errorf("重複の応答: %+v", res)
	}

	// 他の usage のキーだけ（send_prompts なし）も送らない
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"require_on_close": false, "case_pattern": "REQ-\\d+"}}`)); err != nil {
		t.Fatal(err)
	}
	ed.json(201, "POST", "/projects/req/usage", withLabels("s2", 200), &res)
	if *res.SendPrompts || storedSegments(t, e, res.ID)[0]["label"] != nil {
		t.Errorf("send_prompts なしで label が残った: %+v", res)
	}

	// usage.send_prompts: true なら保存し、応答も true
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"send_prompts": true}}`)); err != nil {
		t.Fatal(err)
	}
	ed.json(201, "POST", "/projects/req/usage", withLabels("s3", 300), &res)
	if !*res.SendPrompts {
		t.Errorf("許可の応答: %+v", res)
	}
	if segs := storedSegments(t, e, res.ID); segs[0]["label"] != "顧客 A の障害を調べて" {
		t.Errorf("許可しても label が無い: %v", segs)
	}

	// segments が配列でなければ 400（label を見落とさない）。null・無しは通る
	bad := usageBody("c-s4", "s4", "stop", "", "", 400, 1, 1)
	bad["segments"] = map[string]any{"label": "x"}
	if err := store.SetRules(ctx, e.db, pr.ID, nil); err != nil {
		t.Fatal(err)
	}
	ed.json(400, "POST", "/projects/req/usage", bad, nil)
	ed.json(201, "POST", "/projects/req/usage", usageBody("c-s5", "s5", "stop", "", "", 500, 1, 1), nil)
}

func TestUsageSendPromptsWeb(t *testing.T) {
	e, _, _ := newAPIEnv(t)
	e.user("root", "root-password-12", "admin")
	ctx := context.Background()
	pr, _ := store.ProjectBySlug(ctx, e.db, "req")
	// 既存の usage のキーと他のルールは残す（撤去したゼロバグゲートのキーが残っていても落ちない）
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"require_on_close": true}, "zero_bug_gate": {}}`)); err != nil {
		t.Fatal(err)
	}
	changes := func() []store.SettingChange {
		cs, err := store.SettingChanges(ctx, e.db, store.UsageSendPromptsSetting("req"), 10)
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}

	mc := e.client()
	e.enroll(mc, "ed", "ed-password-123")
	if res, _ := e.formAt(mc, "/im/account", "/im/admin/projects/req/send-prompts", url.Values{"enabled": {"on"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("member の POST: %d", res.StatusCode)
	}

	c := e.client()
	e.enroll(c, "root", "root-password-12")
	res, page := e.get(c, "/im/admin/projects")
	if res.StatusCode != 200 || !strings.Contains(page, "指示文の作業名: 送らない（既定）") {
		t.Fatalf("表示: %d %s", res.StatusCode, page)
	}
	res, page = e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/send-prompts", url.Values{"enabled": {"on"}})
	if res.StatusCode != 200 || !strings.Contains(page, "送るようにしました") || !strings.Contains(page, "指示文の作業名: <strong>送る</strong>") ||
		!strings.Contains(page, "root が送るに（web）") {
		t.Errorf("送るに: %d %s", res.StatusCode, page)
	}
	rules := rulesOfProject(t, e, "req")
	var u map[string]any
	_ = json.Unmarshal(rules["usage"], &u)
	if u["send_prompts"] != true || u["require_on_close"] != true {
		t.Errorf("usage: %s", rules["usage"])
	}
	if _, ok := rules["zero_bug_gate"]; !ok {
		t.Error("他のキー（撤去したゼロバグゲート）が消えた")
	}
	// 同じ値は記録しない
	if _, page = e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/send-prompts", url.Values{"enabled": {"on"}}); !strings.Contains(page, "変更なし") {
		t.Errorf("同じ値: %s", page)
	}
	res, page = e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/send-prompts", url.Values{"enabled": {"off"}})
	if res.StatusCode != 200 || !strings.Contains(page, "送らないようにしました") {
		t.Errorf("送らないに: %d %s", res.StatusCode, page)
	}
	rules = rulesOfProject(t, e, "req")
	u = nil
	_ = json.Unmarshal(rules["usage"], &u)
	if len(u) != 1 || u["require_on_close"] != true {
		t.Errorf("外した後の usage: %s", rules["usage"])
	}
	if cs := changes(); len(cs) != 2 || cs[0].NewValue != "off" || cs[1].NewValue != "on" || cs[0].Via != "web" {
		t.Errorf("記録: %+v", cs)
	}
	if res, _ := e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/send-prompts", url.Values{"enabled": {"x"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("不正な値: %d", res.StatusCode)
	}

	// usage が send_prompts だけなら、外すと usage ごと消える
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"usage": {"send_prompts": true}}`)); err != nil {
		t.Fatal(err)
	}
	e.formAt(c, "/im/admin/projects", "/im/admin/projects/req/send-prompts", url.Values{"enabled": {"off"}})
	if rules := rulesOfProject(t, e, "req"); len(rules) != 0 {
		t.Errorf("空のルールが残った: %v", rules)
	}
}
