package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト別ルール session.digest_required（DESIGN.md §9-1・§9-7）: summary の応答の session_digest_required で
// 引き継ぎの鮮度ガード（Stop hook）に届き、guide の「このプロジェクトのルール」に文で出る。既定は false。
func TestSessionDigestRequiredRule(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("dg")
	u := e.user("dg-ed", "editor-password-1", "member")
	store.SetMember(ctx, e.db, pr.ID, u.ID, "editor")
	h := e.apiAs(u)
	run := issueCLI(t, e, "dg", h.token, "LOOPTRACK_USAGE=0")

	required := func(label string) bool {
		t.Helper()
		var res map[string]any
		h.json(200, "GET", "/projects/dg/summary", nil, &res)
		v, ok := res["session_digest_required"].(bool)
		if !ok {
			t.Fatalf("%s: summary に session_digest_required が無い: %v", label, res)
		}
		// CLI の summary --json（hook が読む形）にも素通しで載る
		out := run("summary", "--limit", "0", "--json")
		var js map[string]any
		if out.code != 0 || json.Unmarshal([]byte(out.stdout), &js) != nil || js["session_digest_required"] != v {
			t.Errorf("%s: summary --json の session_digest_required が REST（%v）と違う: %d %s %s", label, v, out.code, out.stdout, out.stderr)
		}
		return v
	}
	const wording = "このセッションが着手中のイシューに、完了より新しい自分のセッション要約が無ければ、引き継ぎの鮮度ガード（Stop hook）が 1 回差し戻す"

	// 既定（ルールなし）は false で、guide にも出ない
	if required("ルールなし") {
		t.Errorf("既定は false のはず")
	}
	if out := run("guide"); out.code != 0 || strings.Contains(out.stdout, wording) {
		t.Errorf("ルールなしの guide にルールの文が出た: %d\n%s", out.code, out.stdout)
	}

	// 入にすると true になり、guide（CLI・REST の同じ Markdown）に文で出る
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"session":{"digest_required":true}}`)); err != nil {
		t.Fatal(err)
	}
	if !required("入") {
		t.Errorf("入にしたのに false")
	}
	out := run("guide")
	if out.code != 0 || !strings.Contains(out.stdout, wording) || !strings.Contains(out.stdout, "（`session`）") ||
		!strings.Contains(out.stdout, "looptrack session digest <ID> -") {
		t.Errorf("guide にルールの文が無い: %d %s\n%s", out.code, out.stderr, out.stdout)
	}
	if !strings.Contains(out.stdout, "## 2. このプロジェクトのルール") {
		t.Errorf("guide の節が違う:\n%s", out.stdout)
	}

	// 切（false を明示）は false で、guide には設定の JSON のまま出る（説明の無い設定を隠さない）
	if err := store.SetRules(ctx, e.db, pr.ID, []byte(`{"session":{"digest_required":false}}`)); err != nil {
		t.Fatal(err)
	}
	if required("切") {
		t.Errorf("切にしたのに true")
	}
	if out := run("guide"); out.code != 0 || strings.Contains(out.stdout, wording) {
		t.Errorf("切の guide にルールの文が出た:\n%s", out.stdout)
	}
}
