package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクトの作成（DESIGN.md の「(c) 画面からプロジェクトを作る」と MCP の create_project・
// サーバ上の looptrack project create・setup の最初のプロジェクト）。
//
// 省略された値の埋め方は経路によらずここ（NewProjectDefaults）1 か所にそろえる。作った人を admin で
// 参加させる手順は経路ごとに違う（Web・MCP は既にログイン中の利用者を参加させる。CLI の project create と
// setup には「作った人」という HTTP 上の利用者の概念が無い）ので、参加させない CreateProjectNoMember を
// 土台にし、参加させる版（CreateProject）はその上に乗せる。

// ProjectDefaultOption は NewProjectDefaults・CreateProjectNoMember・CreateProject に渡し、0 を「未指定」として
// 既定値に置き換える対象から外す（明示した 0 をそのまま保存する）。「指定したか」を持つのはサーバの CLI の
// flag（flag.Visit）だけ（MCP は JSON の省略と 0 を区別できず、Web の管理画面には width・並び順の入力欄自体が
// 無く、setup の最初のプロジェクトも値を渡さない）ので、使うのは cmd/looptrack の project create だけでよい。
// 渡さなければ 48a939ee より前と同じく、全経路で 0 は未指定として扱う。
type ProjectDefaultOption func(*projectDefaultOpts)

type projectDefaultOpts struct{ widthExplicit, sortOrderExplicit bool }

// WidthExplicit は width に明示した 0 をそのまま保存する（既定値に置き換えない。store.ValidateNewProject が
// 1〜9 の範囲外として拒む）。
func WidthExplicit() ProjectDefaultOption {
	return func(o *projectDefaultOpts) { o.widthExplicit = true }
}

// SortOrderExplicit は並び順に明示した 0 をそのまま保存する（既定値に置き換えない）。
func SortOrderExplicit() ProjectDefaultOption {
	return func(o *projectDefaultOpts) { o.sortOrderExplicit = true }
}

// NewProjectDefaults は省略された値を埋める。prefix は slug を英大文字にしたもの、表示名は slug、
// width は store.DefaultProjectWidth、並び順は store.DefaultProjectSortOrder
// （setup の「最初のプロジェクト」・管理画面・MCP・CLI の作成で同じ）。前後の空白は落とす。
// WidthExplicit・SortOrderExplicit を渡すと、その値の 0 は未指定として扱わない。
func NewProjectDefaults(p store.Project, opts ...ProjectDefaultOption) store.Project {
	var o projectDefaultOpts
	for _, opt := range opts {
		opt(&o)
	}
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
	if p.Width == 0 && !o.widthExplicit {
		p.Width = store.DefaultProjectWidth
	}
	if p.SortOrder == 0 && !o.sortOrderExplicit {
		p.SortOrder = store.DefaultProjectSortOrder
	}
	return p
}

// CreateProjectNoMember は空のプロジェクト（採番 0）を作る（既定値の穴埋め・規則の検査・挿入だけ。
// 作った人を参加させない）。値の誤りは Invalid、slug か prefix の重複は Conflict（code project_exists）。
// q は *sql.DB でも *sql.Tx でもよい（CLI は DB を直接、CreateProject と setup はトランザクションの中で使う）。
func CreateProjectNoMember(ctx context.Context, q store.Queryer, p store.Project, opts ...ProjectDefaultOption) (store.Project, error) {
	p = NewProjectDefaults(p, opts...)
	if err := store.ValidateNewProject(p); err != nil {
		return store.Project{}, erri(Invalid, "invalid_argument", err)
	}
	id, err := store.CreateProject(ctx, q, p)
	switch {
	case errors.Is(err, store.ErrProjectExists) || store.IsDuplicateKey(err):
		return store.Project{}, errm(Conflict, "project_exists", i18n.M("service.err.conflict.project_exists", "slug", p.Slug, "prefix", p.Prefix))
	case err != nil:
		return store.Project{}, err
	}
	p.ID = id
	return p, nil
}

// CreateProject は CreateProjectNoMember で作り、作った人 u を admin で参加させる（1 つのトランザクション。
// 参加しないと管理者でも閲覧のみのため）。管理者（u.Role が admin）以外は Forbidden。
func (s *Service) CreateProject(ctx context.Context, u store.User, p store.Project, opts ...ProjectDefaultOption) (store.Project, error) {
	if u.Role != "admin" {
		return store.Project{}, errm(Forbidden, "forbidden", i18n.M("service.err.forbidden.create_project"))
	}
	var out store.Project
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = CreateProjectNoMember(ctx, tx, p, opts...)
		if err != nil {
			return err
		}
		return store.SetMember(ctx, tx, out.ID, u.ID, "admin")
	})
	if err != nil {
		return store.Project{}, err
	}
	return out, nil
}
