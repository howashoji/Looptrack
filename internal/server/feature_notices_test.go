package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// 既定で切の機能の知らせは、管理者のプロジェクト管理の画面にだけ出る。どこでも切なら出て、どこかで入なら出ない。
// 管理者でない利用者はこの画面を開けない（404）ので、知らせも見えない。
func TestAdminProjectsFeatureNotice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("fa")
	e.project("fb")
	e.user("root", "root-password-12", "admin")
	dave := e.user("dave", "dave-password-12", "member")
	store.SetMember(ctx, e.db, pr.ID, dave.ID, "editor")
	const notice = "どのプロジェクトでも切のまま"
	const hint = "looptrack project rules set &lt;slug&gt; -"

	ac := e.client()
	e.enroll(ac, "root", "root-password-12")
	res, page := e.get(ac, "/im/admin/projects")
	if res.StatusCode != 200 || !strings.Contains(page, notice) || !strings.Contains(page, "session.transcripts") || !strings.Contains(page, hint) {
		t.Fatalf("どこでも切なのに管理者の画面に知らせが無い: %d\n%s", res.StatusCode, page)
	}

	mc := e.client()
	e.enroll(mc, "dave", "dave-password-12")
	if res, page := e.get(mc, "/im/admin/projects"); res.StatusCode != http.StatusNotFound || strings.Contains(page, notice) {
		t.Errorf("member に知らせが見えた: %d", res.StatusCode)
	}
	if res, page := e.get(mc, "/im/"); res.StatusCode != 200 || strings.Contains(page, notice) {
		t.Errorf("member のハブに知らせが出た: %d", res.StatusCode)
	}

	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"session":{"transcripts":true}}`)); err != nil {
		t.Fatal(err)
	}
	if res, page := e.get(ac, "/im/admin/projects"); res.StatusCode != 200 || strings.Contains(page, notice) {
		t.Errorf("どこかで入なのに知らせが出た: %d", res.StatusCode)
	}
}

// summary の応答の session_transcripts は、プロジェクト別ルール session.transcripts の値（既定 false）。
func TestSummarySessionTranscripts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("st")
	u := e.user("st-ed", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	h := e.apiAs(u)
	got := func(label string) bool {
		t.Helper()
		var res map[string]any
		h.json(200, "GET", "/projects/st/summary", nil, &res)
		v, ok := res["session_transcripts"].(bool)
		if !ok {
			t.Fatalf("%s: summary に session_transcripts が無い: %v", label, res)
		}
		return v
	}
	if got("ルールなし") {
		t.Errorf("既定は false のはず")
	}
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"session":{"transcripts":true}}`)); err != nil {
		t.Fatal(err)
	}
	if !got("入") {
		t.Errorf("入にしたのに false")
	}
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"session":{"transcripts":false}}`)); err != nil {
		t.Fatal(err)
	}
	if got("切") {
		t.Errorf("切にしたのに true")
	}
}
