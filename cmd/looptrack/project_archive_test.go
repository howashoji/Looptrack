package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// looptrack project archive / unarchive は本番と同じ権限のアプリ用ユーザーで印だけを付け外しし、
// project list は既定でアーカイブ済みを隠す（--archived でアーカイブ済みだけ）。対照の other は常に一覧に出る。
func TestProjectArchiveCmd(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	for _, p := range []store.Project{{Slug: "demo", Prefix: "DEMO", Width: 4, Name: "デモ"}, {Slug: "other", Prefix: "OTHER", Width: 4, Name: "対照"}} {
		if _, err := store.CreateProject(ctx, admin, p); err != nil {
			t.Fatal(err)
		}
	}
	listed := func(archived bool) string {
		t.Helper()
		var out bytes.Buffer
		if err := projectList(ctx, app, &out, archived); err != nil {
			t.Fatal(err)
		}
		var slugs []string
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n")[1:] {
			slugs = append(slugs, strings.Fields(line)[0])
		}
		return strings.Join(slugs, ",")
	}
	if got := listed(false); got != "demo,other" {
		t.Fatalf("前提が崩れている: アーカイブ前の一覧 %q", got)
	}

	var out bytes.Buffer
	if err := projectArchive(ctx, app, i18n.JA, &out, "demo", true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "アーカイブ: demo（") {
		t.Errorf("ja: %q", out.String())
	}
	if got := listed(false); got != "other" {
		t.Errorf("アーカイブ後の一覧: %q", got)
	}
	if got := listed(true); got != "demo" {
		t.Errorf("--archived: %q", got)
	}
	out.Reset()
	if err := projectArchive(ctx, app, i18n.EN, &out, "demo", true); err != nil || out.String() != "No change: demo is already archived\n" {
		t.Errorf("en（二度目）: %q %v", out.String(), err)
	}

	out.Reset()
	if err := projectArchive(ctx, app, i18n.EN, &out, "demo", false); err != nil || !strings.HasPrefix(out.String(), "Restored from the archive: demo") {
		t.Errorf("戻す: %q %v", out.String(), err)
	}
	if got := listed(false); got != "demo,other" {
		t.Errorf("戻した後の一覧: %q", got)
	}
	if got := listed(true); got != "" {
		t.Errorf("戻した後の --archived: %q", got)
	}
	if err := projectArchive(ctx, app, i18n.JA, &out, "nope", true); err == nil {
		t.Error("無いプロジェクトを受け付けた")
	}
}
