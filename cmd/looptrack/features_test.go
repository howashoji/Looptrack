package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// looptrack features と serve の起動ログ: どこでも切なら機能の名前と入れ方を出し、どこかで入なら数だけ（ログは何も出さない）。
// プロジェクトが 0 件なら features は 1 行だけで、ログは何も出さない。
func TestFeaturesNotice(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	features := func(lang i18n.Lang) string {
		t.Helper()
		var out bytes.Buffer
		if err := printFeatures(ctx, app, lang, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	logged := func() string {
		t.Helper()
		var buf bytes.Buffer
		logFeaturesOff(ctx, app, i18n.JA, slog.New(slog.NewJSONHandler(&buf, nil)))
		return buf.String()
	}
	const hint = "looptrack project rules set <slug> -"

	// 0 件
	if out := features(i18n.JA); strings.Count(out, "\n") != 1 || !strings.Contains(out, "プロジェクトがまだ無い") {
		t.Errorf("0 件の features: %q", out)
	}
	if out := logged(); out != "" {
		t.Errorf("0 件で起動ログが出た: %s", out)
	}

	for _, p := range []store.Project{{Slug: "aa", Prefix: "AA", Width: 4, Name: "a"}, {Slug: "bb", Prefix: "BB", Width: 4, Name: "b"}} {
		if _, err := store.CreateProject(ctx, admin, p); err != nil {
			t.Fatal(err)
		}
	}
	// どこでも切: 出る
	out := features(i18n.JA)
	if !strings.Contains(out, "会話記録の保存（session.transcripts）: 入のプロジェクトは 2 件のうち 0 件です") ||
		!strings.Contains(out, "どのプロジェクトでも切のまま") || !strings.Contains(out, hint) || !strings.Contains(out, "LOOPTRACK_SESSION_TRANSCRIPTS=1") {
		t.Errorf("どこでも切の features（ja）:\n%s", out)
	}
	if en := features(i18n.EN); !strings.Contains(en, "on in 0 of 2 projects") || !strings.Contains(en, "still off in every project") || !strings.Contains(en, hint) {
		t.Errorf("どこでも切の features（en）:\n%s", en)
	}
	if l := logged(); strings.Count(l, "\n") != 1 || !strings.Contains(l, `"level":"WARN"`) || !strings.Contains(l, `"feature":"session.transcripts"`) || !strings.Contains(l, hint) {
		t.Errorf("どこでも切の起動ログ: %s", l)
	}

	// どこかで入: 数の 1 行だけ・ログは無し
	bb, _ := store.ProjectBySlug(ctx, admin, "bb")
	if err := store.SetRules(ctx, admin, bb.ID, []byte(`{"session":{"transcripts":true}}`)); err != nil {
		t.Fatal(err)
	}
	if out := features(i18n.JA); out != "会話記録の保存（session.transcripts）: 入のプロジェクトは 2 件のうち 1 件です\n" {
		t.Errorf("どこかで入の features: %q", out)
	}
	if l := logged(); l != "" {
		t.Errorf("どこかで入なのに起動ログが出た: %s", l)
	}
}
