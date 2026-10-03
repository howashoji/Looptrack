package service

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 添付の管理者向けの操作（上限の変更・使用量と一覧）と、画面が画像をその場で表示してよいかの判定。
// 管理者かどうかの判定はここに置く。画面（/admin/attachments）も入口で管理者以外を止めるが、規則の本体はここにある。

// attachChangesShown は管理者の画面に出す上限の変更の記録の数。
const attachChangesShown = 50

// attachListShown は管理者の画面に出す添付の数（新しい順）。
const attachListShown = 200

// attachAdmin は操作した利用者が管理者（利用者の役割 admin）であることを確かめる。
func (s *Service) attachAdmin(ctx context.Context, a Actor) (store.User, error) {
	u, err := s.actorUser(ctx, a)
	if err != nil {
		return u, err
	}
	if u.Role != "admin" {
		return u, errm(Forbidden, "forbidden", i18n.M("service.err.attach.admin_only"))
	}
	return u, nil
}

// AttachmentInline は、添付をその場で（画面の中に）表示してよいかを返す。本体の応答（REST の GET）と同じ規則
// （AttachmentServeTypeOf。申告と本体の先頭の両方が png・jpeg・gif・webp で一致するときだけ）で決める。
// 消去済み・置き場が無い・本体を開けないときは false。画面はこの値に従い、自分では判定し直さない。
func (s *Service) AttachmentInline(at store.Attachment) bool {
	if at.Purged || s.AttachDir == "" || !sha256Re.MatchString(at.SHA256) {
		return false
	}
	f, err := os.Open(AttachmentBodyPath(s.AttachDir, at.SHA256))
	if err != nil {
		return false
	}
	defer f.Close()
	_, inline := AttachmentServeTypeOf(at.MediaType, f)
	return inline
}

// SetAttachLimits は添付の上限（1 ファイル・1 プロジェクト。全プロジェクト共通）を変える。管理者だけ。
// 変えた設定ごとに setting_changes へ変更前後の値と操作した人を残す（同じ値の項目は記録しない）。変えた設定の名前を返す。
func (s *Service) SetAttachLimits(ctx context.Context, a Actor, l store.AttachLimits, ip string) ([]string, error) {
	u, err := s.attachAdmin(ctx, a)
	if err != nil {
		return nil, err
	}
	for _, v := range []int64{l.MaxFile, l.MaxProject} {
		if v <= 0 {
			return nil, errm(Invalid, "invalid_argument", i18n.M("store.err.attach_limit.invalid", "value", v))
		}
	}
	via := a.Via
	if via == "" {
		via = "web"
	}
	ch := store.SettingChange{ActorUserID: sql.NullInt64{Int64: u.ID, Valid: true}, Via: via, IP: ip}
	var changed []string
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		changed = nil
		for _, it := range []struct {
			name string
			v    int64
		}{{store.SettingAttachMaxFile, l.MaxFile}, {store.SettingAttachMaxProject, l.MaxProject}} {
			_, ok, err := store.SetAttachLimitTx(ctx, tx, it.name, it.v, ch)
			if err != nil {
				return err
			}
			if ok {
				changed = append(changed, it.name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// ProjectAttachmentUsage はプロジェクト 1 つの添付の使用量（管理者の画面の表の 1 行）。
type ProjectAttachmentUsage struct {
	Project store.Project
	store.AttachmentUsage
}

// AttachmentOverview は管理者の画面に出すもの一式。
type AttachmentOverview struct {
	Limits      store.AttachLimits
	Usage       []ProjectAttachmentUsage // 全プロジェクト（アーカイブ済みも含む。並びはプロジェクトの並び順）
	Attachments []store.Attachment       // 絞り込みに合う添付（新しい順・attachListShown まで）
	Changes     []store.SettingChange    // 上限の変更の記録（新しい順）
	Available   bool                     // 置き場が設定されているか（無ければ添付・読む・消去はできない）
}

// AttachmentAdminFilter は管理者の画面の一覧の絞り込み（slug と イシューの表示用の ID。空なら絞らない）。
type AttachmentAdminFilter struct {
	Project string
	Issue   string
}

// AttachmentOverview は管理者の画面に出すもの（上限・プロジェクトごとの使用量・添付の一覧・上限の変更の記録）を返す。管理者だけ。
func (s *Service) AttachmentOverview(ctx context.Context, a Actor, f AttachmentAdminFilter) (*AttachmentOverview, error) {
	if _, err := s.attachAdmin(ctx, a); err != nil {
		return nil, err
	}
	out := &AttachmentOverview{Available: s.AttachDir != ""}
	var err error
	if out.Limits, err = store.ReadAttachLimits(ctx, s.DB); err != nil {
		return nil, err
	}
	projects, err := store.ListProjects(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	usage, err := store.AttachmentUsageByProject(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	filter := store.AttachmentFilter{IssueID: strings.TrimSpace(f.Issue), Limit: attachListShown}
	slug := strings.TrimSpace(f.Project)
	for _, p := range projects {
		out.Usage = append(out.Usage, ProjectAttachmentUsage{Project: p, AttachmentUsage: usage[p.ID]})
		if slug != "" && p.Slug == slug {
			filter.ProjectID = p.ID
		}
	}
	if slug != "" && filter.ProjectID == 0 {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", slug))
	}
	if out.Attachments, err = store.ListAttachments(ctx, s.DB, filter); err != nil {
		return nil, err
	}
	for _, name := range []string{store.SettingAttachMaxFile, store.SettingAttachMaxProject} {
		changes, err := store.SettingChanges(ctx, s.DB, name, attachChangesShown)
		if err != nil {
			return nil, err
		}
		out.Changes = append(out.Changes, changes...)
	}
	sort.SliceStable(out.Changes, func(i, j int) bool { return out.Changes[i].At.After(out.Changes[j].At) })
	if len(out.Changes) > attachChangesShown {
		out.Changes = out.Changes[:attachChangesShown]
	}
	return out, nil
}
