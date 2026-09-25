package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクトの作成（DESIGN.md の「(c) 画面からプロジェクトを作る」と MCP の create_project）。
//
// 作った人を admin で参加させるところまでを 1 つのトランザクションで行う（参加しないと管理者でも閲覧のみのため）。
// Web の管理画面（POST /admin/projects）と MCP の create_project がこの関数を呼ぶ。経路ごとに規則を写さない。
// slug・prefix・width・表示名・説明の規則は store.ValidateNewProject、重複の判定は store.CreateProject にある
// （サーバ上の looptrack project create と同じ）。prefix と width は後から変えられない。

// NewProjectDefaults は省略された値を埋める。prefix は slug を英大文字にしたもの、表示名は slug、
// width は store.DefaultProjectWidth（setup の「最初のプロジェクト」と管理画面の既定と同じ）。前後の空白は落とす。
func NewProjectDefaults(p store.Project) store.Project {
	p.Slug = strings.TrimSpace(p.Slug)
	p.Prefix = strings.TrimSpace(p.Prefix)
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	if p.Prefix == "" {
		p.Prefix = strings.ToUpper(p.Slug)
	}
	if p.Name == "" {
		p.Name = p.Slug
	}
	if p.Width == 0 {
		p.Width = store.DefaultProjectWidth
	}
	return p
}

// CreateProject は空のプロジェクト（採番 0）を作り、作った人 u を admin で参加させる。作ったプロジェクトを返す。
// 管理者（u.Role が admin）以外は Forbidden。値の誤りは Invalid、slug か prefix の重複は Conflict（code project_exists）。
// 省略された値は NewProjectDefaults で埋める。
func (s *Service) CreateProject(ctx context.Context, u store.User, p store.Project) (store.Project, error) {
	if u.Role != "admin" {
		return store.Project{}, errm(Forbidden, "forbidden", i18n.M("service.err.forbidden.create_project"))
	}
	p = NewProjectDefaults(p)
	if err := store.ValidateNewProject(p); err != nil {
		return store.Project{}, erri(Invalid, "invalid_argument", err)
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		id, err := store.CreateProject(ctx, tx, p)
		if err != nil {
			return err
		}
		p.ID = id
		return store.SetMember(ctx, tx, id, u.ID, "admin")
	})
	switch {
	case errors.Is(err, store.ErrProjectExists) || store.IsDuplicateKey(err):
		return store.Project{}, errm(Conflict, "project_exists", i18n.M("service.err.conflict.project_exists", "slug", p.Slug, "prefix", p.Prefix))
	case err != nil:
		return store.Project{}, err
	}
	return p, nil
}
