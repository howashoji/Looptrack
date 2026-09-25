package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクトのアーカイブ（管理画面の「削除」。DESIGN.md のプロジェクトの節）。
//
// 論理削除で、projects.archived_at に時刻を入れるだけ。行も、イシュー・コメント・イベントも消さない
// （DB の権限は projects・issues に DELETE を与えず、comments・issue_events は追記だけ）。slug と prefix は
// 一意のまま残るので使い回されない。戻すと archived_at を NULL にし、元どおり一覧に出て操作できる。
//
// アーカイブ済みの扱いは 2 か所だけに置く:
//   - 見せない: store.AccessibleProjects / store.UserMemberships がアーカイブ済みを返さない。REST・MCP・Web・
//     ログイン済みの CLI の一覧と slug の解決はどれもここを通るので、どの経路でも一覧に出ず「存在しない」になる。
//   - 書かせない: 起票（Create）と既存イシューの変更（mutate。コメント・状態・担当・編集・検証の記録・補正）の
//     トランザクションの中で checkProjectActive が拒む。一覧の解決を通らない呼び出し（サーバ側の管理コマンドなど）にも効く。
// 権限は呼ぶ側が持つ（Web は管理者だけの経路、CLI は LOOPTRACK_DSN を持つ管理者だけ。RenameProject と同じ）。

// ArchiveResult はアーカイブ・戻すの結果。
type ArchiveResult struct {
	Project  string `json:"project"`
	Archived bool   `json:"archived"`
	Changed  bool   `json:"changed"`
}

// Message は結果を伝える文面（CLI の looptrack project archive / unarchive と Web で同じ文面。表示する側が In で訳す）。
func (r *ArchiveResult) Message() i18n.Msg {
	switch {
	case r.Archived && r.Changed:
		return i18n.M("cmd.project.archived", "slug", r.Project)
	case r.Archived:
		return i18n.M("cmd.project.archive_unchanged", "slug", r.Project)
	case r.Changed:
		return i18n.M("cmd.project.unarchived", "slug", r.Project)
	}
	return i18n.M("cmd.project.unarchive_unchanged", "slug", r.Project)
}

// SetProjectArchived はプロジェクトをアーカイブする（archived が true）か、戻す。すでにその状態なら Changed が false。
func (s *Service) SetProjectArchived(ctx context.Context, p store.Project, archived bool) (*ArchiveResult, error) {
	res := &ArchiveResult{Project: p.Slug, Archived: archived}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		changed, err := store.SetProjectArchived(ctx, tx, p.ID, archived)
		if errors.Is(err, store.ErrNotFound) {
			return errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", p.Slug))
		}
		res.Changed = changed
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// checkProjectActive は書き込みの前に、プロジェクトがアーカイブされていないことを確かめる
// （呼ぶ側が持つ store.Project は古いことがあるので、DB から読み直す）。
func checkProjectActive(ctx context.Context, q store.Queryer, p store.Project) error {
	archived, err := store.ProjectArchived(ctx, q, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		return errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", p.Slug))
	}
	if err != nil {
		return err
	}
	if archived {
		return errm(Rejected, "project_archived", i18n.M("service.err.project_archived", "slug", p.Slug))
	}
	return nil
}
