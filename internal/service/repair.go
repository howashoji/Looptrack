package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
)

// 既存データの補正: ID の項目（blocked_by / traces / refs）に空白区切りで 1 要素として入った値を分割する。
// クローズ済みのイシューも対象にする（本文・項目の編集を拒否する規則の例外。管理コマンド looptrack repair-lists だけが通る）。
// 変更は issue_events（kind repair_lists・via admin）に変更前後の値ごと残す。updated は変えない（利用者の更新ではないため）。

// RepairKind は補正の記録に使う issue_events.kind。
const RepairKind = "repair_lists"

// FieldRepair は 1 項目の補正内容。
type FieldRepair struct {
	Field  string   `json:"field"`
	Before []string `json:"before"`
	After  []string `json:"after"`
}

// IssueRepair は 1 イシューの補正内容。
type IssueRepair struct {
	ID      string        `json:"id"`
	Status  string        `json:"status"`
	Changes []FieldRepair `json:"changes"`
	issueID int64
}

// planRepair は doc の fields を補正した結果を返す（doc は変えない）。
func planRepair(doc *mdformat.Document, fields []string) []FieldRepair {
	var out []FieldRepair
	for _, key := range fields {
		f := doc.Field(key)
		if f == nil || !f.IsList {
			continue
		}
		if after, changed := domain.SplitSpaced(f.List); changed {
			out = append(out, FieldRepair{Field: key, Before: append([]string(nil), f.List...), After: after})
		}
	}
	return out
}

// ValidateRepairFields は補正対象の項目名を検査する（labels / blocked_by / traces / refs のいずれか）。
func ValidateRepairFields(fields []string) error {
	if len(fields) == 0 {
		return fmt.Errorf("補正する項目がありません")
	}
	for _, f := range fields {
		if err := domain.ValidateValue("--fields", f, domain.ListFields); err != nil {
			return err
		}
	}
	return nil
}

// SpacedLists はプロジェクトの全イシュー（クローズ済みを含む）から、fields に空白を含む値を持つものと補正後の値を返す（dry-run）。
func (s *Service) SpacedLists(ctx context.Context, p store.Project, fields []string) ([]IssueRepair, error) {
	if err := ValidateRepairFields(fields); err != nil {
		return nil, err
	}
	rows, err := store.LoadFronts(ctx, s.DB, p.ID)
	if err != nil {
		return nil, err
	}
	var out []IssueRepair
	for _, r := range rows {
		if ch := planRepair(r.Doc, fields); len(ch) > 0 {
			it := domain.FromDocument(r.Doc)
			out = append(out, IssueRepair{ID: it.ID, Status: it.Status, Changes: ch, issueID: r.ID})
		}
	}
	return out, nil
}

var errNothingToRepair = errors.New("補正する値がありません")

// RepairLists は SpacedLists の対象を補正し、補正したものを返す。1 イシューずつ別のトランザクションで行い、
// 行をロックしてから補正内容を計算し直す（一覧を取った後に直されたものは飛ばす）。
func (s *Service) RepairLists(ctx context.Context, a Actor, p store.Project, fields []string, reason string) ([]IssueRepair, error) {
	plan, err := s.SpacedLists(ctx, p, fields)
	if err != nil {
		return nil, err
	}
	var done []IssueRepair
	for _, target := range plan {
		var got []FieldRepair
		_, err := s.mutate(ctx, a, p, target.issueID, 0, func(_ *sql.Tx, doc *mdformat.Document, it domain.Issue, _ string) (change, error) {
			got = planRepair(doc, fields)
			if len(got) == 0 {
				return change{}, errNothingToRepair
			}
			changed := make([]string, 0, len(got))
			before, after := map[string][]string{}, map[string][]string{}
			for _, c := range got {
				domain.SetList(doc, c.Field, c.After)
				changed = append(changed, c.Field)
				before[c.Field], after[c.Field] = c.Before, c.After
			}
			return change{kind: RepairKind, detail: map[string]any{"fields": changed, "before": before, "after": after,
				"status": it.Status, "reason": reason}}, nil
		})
		if errors.Is(err, errNothingToRepair) {
			continue
		}
		if err != nil {
			return done, fmt.Errorf("%s: %w", target.ID, err)
		}
		target.Changes = got
		done = append(done, target)
	}
	return done, nil
}
