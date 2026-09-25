package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクトの表示名の変更（DESIGN.md のプロジェクトの節）。
//
// 変えるのは projects.name だけで、slug・prefix・width は変えない（prefix と width を変えると発番済みの ID が壊れる）。
// 名前の規則は作成と同じ store.ValidateProjectName（空・空白だけ・255 文字を超えるものは拒否）。
// 権限は呼ぶ側が持つ（CLI は LOOPTRACK_DSN を持つ管理者だけが実行できる。project create と同じ）。

// RenameResult は表示名の変更の結果。
type RenameResult struct {
	Project string `json:"project"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
	Changed bool   `json:"changed"`
}

// RenameProject はプロジェクトの表示名を変える。同じ名前なら何もしない（Changed が false）。
func (s *Service) RenameProject(ctx context.Context, p store.Project, name string) (*RenameResult, error) {
	if err := store.ValidateProjectName(name); err != nil {
		return nil, errm(Invalid, "invalid_argument", i18n.M("store.err.project.name"))
	}
	res := &RenameResult{Project: p.Slug, NewName: name}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res.Changed = false
		if err := tx.QueryRowContext(ctx, "SELECT name FROM projects WHERE id = ? FOR UPDATE", p.ID).Scan(&res.OldName); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", p.Slug))
			}
			return err
		}
		if res.OldName == name {
			return nil
		}
		res.Changed = true
		return store.SetProjectName(ctx, tx, p.ID, name)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
