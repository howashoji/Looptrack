package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// 既定で切の機能は、アーカイブされていないプロジェクトのどれでも切のときだけ返る。出る側（全部切・入がアーカイブ済みだけ・
// 読めないルール）と出ない側（1 つでも入・プロジェクト 0 件）を同じテストに並べる。
func TestFeaturesOffEverywhere(t *testing.T) {
	on := json.RawMessage(`{"session":{"transcripts":true}}`)
	off := json.RawMessage(`{"session":{"transcripts":false}}`)
	broken := json.RawMessage(`{"session":{"transcripts":true},"no_such_key":1}`)
	keys := func(fs []DefaultOffFeature) string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Key)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		name     string
		projects []store.Project
		want     string
	}{
		{"全部切（未設定と false）", []store.Project{{Slug: "a"}, {Slug: "b", Rules: off}}, "session.transcripts"},
		{"1 つでも入", []store.Project{{Slug: "a"}, {Slug: "b", Rules: on}}, ""},
		{"入がアーカイブ済みだけ", []store.Project{{Slug: "a"}, {Slug: "b", Rules: on, Archived: true}}, "session.transcripts"},
		{"読めないルールは切", []store.Project{{Slug: "a", Rules: broken}}, "session.transcripts"},
		{"プロジェクト 0 件", nil, ""},
		{"アーカイブ済みしかない", []store.Project{{Slug: "a", Archived: true}}, ""},
	} {
		if got := keys(FeaturesOffEverywhere(c.projects)); got != c.want {
			t.Errorf("%s: %q（期待 %q）", c.name, got, c.want)
		}
	}
	cs := CountDefaultOffFeatures([]store.Project{{Slug: "a", Rules: on}, {Slug: "b"}, {Slug: "c", Rules: on, Archived: true}})
	if len(cs) != 1 || cs[0].Enabled != 1 || cs[0].Total != 2 {
		t.Errorf("数: %+v", cs)
	}
}

// DB から数える入口も、アーカイブ済みを数えない。文面は日英とも機能の名前・キー・入れ方のコマンドを含む。
func TestDefaultOffFeatureCountsFromDB(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	if cs, err := DefaultOffFeatureCounts(ctx, db); err != nil || len(cs) != 1 || cs[0].Total != 0 || cs[0].OffEverywhere() {
		t.Fatalf("0 件: %+v %v", cs, err)
	}
	for _, slug := range []string{"aa", "bb"} {
		if _, err := store.CreateProject(ctx, db, store.Project{Slug: slug, Prefix: strings.ToUpper(slug), Width: 4, Name: slug}); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := DefaultOffFeatureCounts(ctx, db)
	if err != nil || cs[0].Total != 2 || !cs[0].OffEverywhere() {
		t.Fatalf("全部切: %+v %v", cs, err)
	}
	bb, _ := store.ProjectBySlug(ctx, db, "bb")
	if err := store.SetRules(ctx, db, bb.ID, []byte(`{"session":{"transcripts":true}}`)); err != nil {
		t.Fatal(err)
	}
	if cs, _ = DefaultOffFeatureCounts(ctx, db); cs[0].Enabled != 1 || cs[0].OffEverywhere() {
		t.Errorf("bb が入: %+v", cs)
	}
	if _, err := store.SetProjectArchived(ctx, db, bb.ID, true); err != nil {
		t.Fatal(err)
	}
	if cs, _ = DefaultOffFeatureCounts(ctx, db); cs[0].Enabled != 0 || cs[0].Total != 1 || !cs[0].OffEverywhere() {
		t.Errorf("入の bb をアーカイブ: %+v", cs)
	}

	f := DefaultOffFeatures[0]
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		text := FeatureOffNotice(lang, f) + "\n" + FeatureEnableHint(lang, f)
		for _, want := range []string{"session.transcripts", "1.1.0", "looptrack project rules show <slug>",
			`"session": {"transcripts": true}`, "looptrack project rules set <slug> -", "LOOPTRACK_SESSION_TRANSCRIPTS=1"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: %q が無い:\n%s", lang, want, text)
			}
		}
	}
}
