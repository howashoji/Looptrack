package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// エビデンスの網（verify.require_evidence。判定は service.checkEvidence と domain.Rules.CheckEvidence）を REST・MCP で確かめる。
// 文面は Accept-Language で言語を固定して見る。

// evidenceCreate は起票して ID を返す。
func (a *attachAPIEnv) evidenceCreate(title, body string) string {
	a.t.Helper()
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	a.ed.json(201, "POST", "/projects/"+a.pr.Slug+"/issues", map[string]any{"title": title, "body": body}, &created)
	return created.Issue.ID
}

// evidenceRecord は現在の本文に対して全件成功の verify を記録する（attachments が空なら添付なし）。
func (a *attachAPIEnv) evidenceRecord(id string, attachments []int64, statuses ...string) {
	a.t.Helper()
	var p verifyPlanJSON
	a.ed.json(200, "GET", "/issues/"+id+"/verify", nil, &p)
	req := map[string]any{"body_sha256": p.BodySHA256, "results": results(p.Commands, statuses...)}
	if len(attachments) > 0 {
		req["attachments"] = attachments
	}
	a.ed.json(201, "POST", "/issues/"+id+"/verify", req, nil)
}

func (a *attachAPIEnv) overrideRules(id string) []string {
	a.t.Helper()
	rows, err := a.db.Query(`SELECT JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.rule')), JSON_UNQUOTE(JSON_EXTRACT(e.detail, '$.reason')) FROM issue_events e
JOIN issues i ON i.id = e.issue_id WHERE i.display_id = ? AND e.kind = 'rule_override' ORDER BY e.id`, id)
	if err != nil {
		a.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var rule, reason string
		if err := rows.Scan(&rule, &reason); err != nil {
			a.t.Fatal(err)
		}
		out = append(out, rule+":"+reason)
	}
	return out
}

// ルールを何も設定していないプロジェクト（既定は入）で、検証コマンドのあるイシューの Done は
// 記録なし・添付の無い記録のどちらも拒否され、添付のある記録なら通る（拒否と通過を同じテストで見る）。
func TestEvidenceRequiredByDefault(t *testing.T) {
	a := newAttachAPIEnv(t)
	if a.pr.Rules != nil {
		t.Fatalf("前提が崩れています: ルールの無いプロジェクトで確かめるはずが rules=%s", a.pr.Rules)
	}
	id := a.evidenceCreate("検証あり", verifyBody)
	done := map[string]any{"status": "Done", "comment": "検証結果: verify 2/2"}

	// 記録なし → 拒否（日本語・英語の文面）
	e := a.ed.rule("verify_evidence_required", "POST", "/issues/"+id+"/status", done)
	if !e.Error.Overridable || !strings.Contains(e.Error.Message, id+" は検証コマンドを持っていますが、いまの本文に対する verify の記録がありません") ||
		!strings.Contains(e.Error.Message, "looptrack issue verify "+id+" --attach-output") {
		t.Errorf("記録なしの拒否: %+v", e.Error)
	}
	var en apiErr
	a.ed.json(422, "POST", "/issues/"+id+"/status", done, &en, "Accept-Language", "en")
	if en.Error.Rule != "verify_evidence_required" || !strings.Contains(en.Error.Message, id+" has verify commands but no verify record for the current body") {
		t.Errorf("記録なしの拒否（英語）: %+v", en.Error)
	}

	// 添付の無い記録 → 拒否
	a.evidenceRecord(id, nil)
	e = a.ed.rule("verify_evidence_required", "POST", "/issues/"+id+"/status", done)
	if !strings.Contains(e.Error.Message, id+" の最新の verify の記録にエビデンスの添付がありません") {
		t.Errorf("添付の無い記録の拒否: %s", e.Error.Message)
	}
	a.ed.json(422, "POST", "/issues/"+id+"/status", done, &en, "Accept-Language", "en")
	if !strings.Contains(en.Error.Message, "The latest verify record of "+id+" carries no evidence attachment") {
		t.Errorf("添付の無い記録の拒否（英語）: %s", en.Error.Message)
	}
	if st := a.ed.state(id); st.status != "Todo" {
		t.Fatalf("拒否で状態が変わった: %+v", st)
	}

	// 対照: 添付のある記録 → 通る（上書きの記録は残らない）
	at := a.mustUpload(id, "go-test.log", "text/plain", []byte("ok  ./...\n"))
	a.evidenceRecord(id, []int64{at.ID})
	var res map[string]any
	a.ed.json(200, "POST", "/issues/"+id+"/status", done, &res)
	if st := a.ed.state(id); st.status != "Done" || res["evidence_notice"] != nil {
		t.Errorf("添付のある記録で Done にならない: %+v %v", st, res)
	}
	if o := a.overrideRules(id); len(o) != 0 {
		t.Errorf("添付のある記録なのに上書きが残った: %v", o)
	}

	// 添付の後に本文が変わったら、いまの本文に対する記録が無いのと同じ（拒否）
	id2 := a.evidenceCreate("本文が変わる", verifyBody)
	at2 := a.mustUpload(id2, "go-test.log", "text/plain", []byte("ok\n"))
	a.evidenceRecord(id2, []int64{at2.ID})
	var d issueDetailJSON
	a.ed.json(200, "GET", "/issues/"+id2, nil, &d)
	a.ed.json(200, "PATCH", "/issues/"+id2, map[string]any{"markdown": strings.Replace(d.Markdown, "make lint", "make vet", 1)}, nil, "If-Match", jsonInt(d.Version))
	if e := a.ed.rule("verify_evidence_required", "POST", "/issues/"+id2+"/status", done); !strings.Contains(e.Error.Message, "いまの本文に対する verify の記録がありません") {
		t.Errorf("本文が変わった後の拒否: %s", e.Error.Message)
	}

	// 成否は見ない（失敗の記録でも添付があれば通る。成否は verify.require_on_close の役目で、ここでは切）
	id3 := a.evidenceCreate("失敗の記録", verifyBody)
	at3 := a.mustUpload(id3, "fail.log", "text/plain", []byte("FAIL\n"))
	a.evidenceRecord(id3, []int64{at3.ID}, "ok", "fail")
	a.ed.json(200, "POST", "/issues/"+id3+"/status", done, nil)

	// 理由付きの上書きは通り、rule_override（verify_evidence_required）が残る
	id4 := a.evidenceCreate("上書き", verifyBody)
	a.ed.json(200, "POST", "/issues/"+id4+"/status", map[string]any{"status": "Done", "override_reason": "旧版のクライアントで添付できない（利用者の指示）"}, nil)
	if o := a.overrideRules(id4); len(o) != 1 || o[0] != "verify_evidence_required:旧版のクライアントで添付できない（利用者の指示）" {
		t.Errorf("rule_override: %v", o)
	}

	// 本文に検証コマンドの無いイシューは添付が無くても通る（節があってもコマンドが 0 件なら対象外）。Canceled は見ない
	for _, body := range []string{"説明だけ", "説明\n\n## 検証コマンド\n\n```bash\n# コメントだけ\n```\n"} {
		plain := a.evidenceCreate("検証なし", body)
		a.ed.json(200, "POST", "/issues/"+plain+"/status", map[string]any{"status": "Done"}, nil)
	}
	canceled := a.evidenceCreate("取りやめ", verifyBody)
	a.ed.json(200, "POST", "/issues/"+canceled+"/status", map[string]any{"status": "Canceled"}, nil)

	// 起票で Done にする場合も同じ判定（記録はありえないので拒否。上書き可）
	e = a.ed.rule("verify_evidence_required", "POST", "/projects/"+a.pr.Slug+"/issues", map[string]any{"title": "x", "status": "Done", "body": verifyBody})
	if !strings.Contains(e.Error.Message, "起票することはできません（エビデンスを添付した verify の記録がありません）") {
		t.Errorf("起票の拒否: %s", e.Error.Message)
	}
	var created struct {
		Issue issueDetailJSON `json:"issue"`
	}
	a.ed.json(201, "POST", "/projects/"+a.pr.Slug+"/issues", map[string]any{"title": "x", "status": "Done", "body": verifyBody, "override_reason": "移行分"}, &created)
	if o := a.overrideRules(created.Issue.ID); len(o) != 1 || o[0] != "verify_evidence_required:移行分" {
		t.Errorf("起票の rule_override: %v", o)
	}
	a.ed.json(201, "POST", "/projects/"+a.pr.Slug+"/issues", map[string]any{"title": "y", "status": "Done", "body": "説明だけ"}, nil)

	// MCP の set_status も同じ判定（経路を問わない）
	m := a.mcpAs(a.ed.token, map[string]string{"X-Looptrack-Project": a.pr.Slug})
	id5 := a.evidenceCreate("MCP", verifyBody)
	if text, _ := m.call("set_status", map[string]any{"id": id5, "status": "Done"}, true); !strings.Contains(text, "いまの本文に対する verify の記録がありません") {
		t.Errorf("MCP の拒否: %s", text)
	}
}

// verify.require_evidence を切にしたプロジェクトでは、同じ条件で注意だけが返り、状態は Done になる。
func TestEvidenceNoticeWhenOff(t *testing.T) {
	a := newAttachAPIEnv(t)
	if err := store.SetRules(context.Background(), a.db, a.pr.ID, []byte(`{"verify": {"require_evidence": false}}`)); err != nil {
		t.Fatal(err)
	}
	id := a.evidenceCreate("検証あり", verifyBody)
	var res struct {
		To             string   `json:"to"`
		EvidenceNotice string   `json:"evidence_notice"`
		Messages       []string `json:"messages"`
	}
	a.ed.json(200, "POST", "/issues/"+id+"/status", map[string]any{"status": "Done"}, &res, "Accept-Language", "ja")
	want := "注意: " + id + " を Done にしましたが、いまの本文に対する verify の記録がありません"
	if res.To != "Done" || !strings.HasPrefix(res.EvidenceNotice, want) || !strings.Contains(strings.Join(res.Messages, "\n"), want) {
		t.Errorf("記録なしの注意: %+v", res)
	}
	if o := a.overrideRules(id); len(o) != 0 {
		t.Errorf("切なのに上書きが残った: %v", o)
	}

	// 添付の無い記録（英語）
	id2 := a.evidenceCreate("添付なし", verifyBody)
	a.evidenceRecord(id2, nil)
	a.ed.json(200, "POST", "/issues/"+id2+"/status", map[string]any{"status": "Done"}, &res, "Accept-Language", "en")
	if res.To != "Done" || !strings.HasPrefix(res.EvidenceNotice, "Note: "+id2+" is now Done, but the latest verify record for the current body carries no evidence attachment") {
		t.Errorf("添付の無い記録の注意（英語）: %+v", res)
	}

	// 対照: 添付のある記録では注意が出ない
	id3 := a.evidenceCreate("添付あり", verifyBody)
	at := a.mustUpload(id3, "go-test.log", "text/plain", []byte("ok\n"))
	a.evidenceRecord(id3, []int64{at.ID})
	var raw map[string]any
	a.ed.json(200, "POST", "/issues/"+id3+"/status", map[string]any{"status": "Done"}, &raw)
	if _, ok := raw["evidence_notice"]; ok {
		t.Errorf("添付のある記録で注意が出た: %v", raw)
	}

	// MCP の set_status は本文と data の両方に載せる
	m := a.mcpAs(a.ed.token, map[string]string{"X-Looptrack-Project": a.pr.Slug})
	id4 := a.evidenceCreate("MCP", verifyBody)
	text, data := m.call("set_status", map[string]any{"id": id4, "status": "Done"}, false)
	if !strings.Contains(text, "注意: "+id4+" を Done にしましたが") || data["evidence_notice"] == nil {
		t.Errorf("MCP の注意: %s %v", text, data)
	}
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
