package service

import (
	"context"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 既定で切の機能の知らせ。プロジェクト別ルールで入れる機能は、足した版に上げただけではどこでも切のまま残り、
// 誰も気づかない。どこでも切の機能を、サーバの起動ログ・管理者の画面（/admin/projects）・looptrack features
// （install.sh の --upgrade の終わり）で知らせる。判定はこのファイルの 1 か所に置き、見せる側はここを呼ぶだけにする。
// 次の機能は DefaultOffFeatures に 1 件足せば、3 か所すべてに出る。

// DefaultOffFeature は既定で切の機能（プロジェクト別ルールで入れるもの）。
type DefaultOffFeature struct {
	// Key はルールのキー（ドット区切り。例 session.transcripts）
	Key string
	// Since は足した版
	Since string
	// Snippet はルールの JSON に足す断片（rules set に渡す全体の中に入れる形）
	Snippet string
	// Enabled はルールでこの機能が入か（読めたルールに対して呼ぶ。nil のルールは何も設定していないプロジェクト）
	Enabled func(r *domain.Rules) bool
	// Name は画面とログに出す機能の名前
	Name func(lang i18n.Lang) string
	// Note はルールのほかに要るものの 1 文（無ければ nil）
	Note func(lang i18n.Lang) string
}

// DefaultOffFeatures は、この版の既定で切の機能の一覧。
var DefaultOffFeatures = []DefaultOffFeature{
	{
		Key:     "session.transcripts",
		Since:   "1.1.0",
		Snippet: `"session": {"transcripts": true}`,
		Enabled: func(r *domain.Rules) bool { return r.TranscriptsEnabled() },
		Name:    func(lang i18n.Lang) string { return i18n.T(lang, "service.feature.session_transcripts.name") },
		Note: func(lang i18n.Lang) string {
			return i18n.T(lang, "service.feature.session_transcripts.note", "env", "LOOPTRACK_SESSION_TRANSCRIPTS=1")
		},
	},
}

// FeatureCount は既定で切の機能 1 件の、入のプロジェクトの数（アーカイブ済みは数えない）。
type FeatureCount struct {
	Feature DefaultOffFeature
	Enabled int // 入のプロジェクトの数
	Total   int // アーカイブされていないプロジェクトの数
}

// OffEverywhere は、アーカイブされていないプロジェクトが 1 つ以上あり、そのどれでも切か。
func (c FeatureCount) OffEverywhere() bool { return c.Total > 0 && c.Enabled == 0 }

// featureEnabled はプロジェクトのルールで機能が入か。読めないルールは切（SessionTranscriptsEnabled と同じ側）。
func featureEnabled(f DefaultOffFeature, p store.Project) bool {
	r, err := domain.ParseRules(p.Rules)
	return err == nil && f.Enabled(r)
}

// CountDefaultOffFeatures は、既定で切の機能ごとに入のプロジェクトを数える（並びは DefaultOffFeatures の順）。
// アーカイブ済みのプロジェクトは、入でも全体にも数えない。
func CountDefaultOffFeatures(projects []store.Project) []FeatureCount {
	out := make([]FeatureCount, 0, len(DefaultOffFeatures))
	for _, f := range DefaultOffFeatures {
		c := FeatureCount{Feature: f}
		for _, p := range projects {
			if p.Archived {
				continue
			}
			c.Total++
			if featureEnabled(f, p) {
				c.Enabled++
			}
		}
		out = append(out, c)
	}
	return out
}

// FeaturesOffEverywhere は、どこでも切の機能を返す（プロジェクトが 0 件・どこかで入なら返さない）。
func FeaturesOffEverywhere(projects []store.Project) []DefaultOffFeature {
	var out []DefaultOffFeature
	for _, c := range CountDefaultOffFeatures(projects) {
		if c.OffEverywhere() {
			out = append(out, c.Feature)
		}
	}
	return out
}

// DefaultOffFeatureCounts は DB の全プロジェクトから CountDefaultOffFeatures を求める。
func DefaultOffFeatureCounts(ctx context.Context, q store.Queryer) ([]FeatureCount, error) {
	projects, err := store.ListProjects(ctx, q)
	if err != nil {
		return nil, err
	}
	return CountDefaultOffFeatures(projects), nil
}

// FeatureOffNotice は、どこでも切の機能の知らせの 1 文（ログ・画面・looptrack features で同じ文面）。
func FeatureOffNotice(lang i18n.Lang, f DefaultOffFeature) string {
	return i18n.T(lang, "service.feature.off_everywhere", "name", f.Name(lang), "key", f.Key, "since", f.Since)
}

// FeatureEnableHint は、機能の入れ方の文（rules show で今のルールを見て断片を足し、rules set で入れ直す）。
// rules set は全体を置き換えるので、既存の鍵を消さないよう注意を添える。ルールのほかに要るものがあれば続ける。
func FeatureEnableHint(lang i18n.Lang, f DefaultOffFeature) string {
	hint := i18n.T(lang, "service.feature.enable_hint", "show", "looptrack project rules show <slug>",
		"snippet", f.Snippet, "set", "looptrack project rules set <slug> -")
	if f.Note != nil {
		if note := f.Note(lang); note != "" {
			hint = i18n.T(lang, "service.feature.enable_hint_note", "hint", hint, "note", note)
		}
	}
	return hint
}
