package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 添付の管理者の画面（/admin/attachments）。上限（1 ファイル・1 プロジェクト）の設定・プロジェクトごとの使用量・添付ごとの消去。
// 管理者以外は入口の adminForbidden が 403 で止める。規則（管理者だけ・上限の値・消去の効き方）は service にあり、
// 画面は入力の読み取りと表示だけを持つ。CSRF は s.web が検査する。

// mib は上限の入力と表示の単位（MiB）。
const mib = 1 << 20

// maxLimitMiB は上限の入力として受け取る最大（1 PiB。int64 のバイト数に収める）。
const maxLimitMiB = 1 << 30

// attachmentRowView は添付の一覧の 1 行。
type attachmentRowView struct {
	ID                             int64
	Project, Issue, Filename, Type string
	Size, Created                  string
	Purged                         bool
}

// usageRowView はプロジェクトごとの使用量の 1 行。
type usageRowView struct {
	Slug, Name, Bytes, Percent string
	Count, Purged              int64
	Archived, Over             bool
}

// changeRowView は上限の変更の記録の 1 行。
type changeRowView struct {
	At, Actor, Via, Setting, From, To, IP string
}

// fmtBytes はバイト数を読みやすい単位で描く（1024 の累乗。単位の記号は言語に依らない）。
func fmtBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 || v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10) + " " + units[i]
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[i]
}

// fmtSettingBytes は setting_changes の値（バイト数の 10 進。空は未設定）を描く。
func fmtSettingBytes(lang i18n.Lang, v string) string {
	if v == "" {
		return i18n.T(lang, "server.web.admin_attachments.unset")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return v
	}
	return fmtBytes(n)
}

type attachAdminResult struct {
	Notice, Error string
	status        int
}

func (s *Server) adminAttachmentsPage(w http.ResponseWriter, r *http.Request, p *principal) {
	s.renderAdminAttachments(w, r, p, attachAdminResult{})
}

// attachFilterOf は絞り込みの値を読む（GET は問い合わせ、POST はフォームの隠し欄）。
func attachFilterOf(r *http.Request) service.AttachmentAdminFilter {
	if r.Method == http.MethodPost {
		return service.AttachmentAdminFilter{Project: r.PostFormValue("project"), Issue: r.PostFormValue("issue")}
	}
	q := r.URL.Query()
	return service.AttachmentAdminFilter{Project: q.Get("project"), Issue: q.Get("issue")}
}

func (s *Server) renderAdminAttachments(w http.ResponseWriter, r *http.Request, p *principal, res attachAdminResult) {
	lang := reqLang(r)
	filter := attachFilterOf(r)
	ov, err := s.svc.AttachmentOverview(r.Context(), actor(r), filter)
	var se *service.Error
	if err != nil && errors.As(err, &se) && se.Kind == service.NotFound && filter.Project != "" {
		// 絞り込みのプロジェクトが無い: 絞らずに出し、その旨をエラーに出す
		res.Error, res.status = i18n.Text(lang, se), http.StatusNotFound
		filter.Project = ""
		ov, err = s.svc.AttachmentOverview(r.Context(), actor(r), filter)
	}
	if err != nil {
		if errors.As(err, &se) && se.Kind == service.Forbidden {
			http.Error(w, i18n.Text(lang, se), http.StatusForbidden)
			return
		}
		s.internalError(w, r, err)
		return
	}
	usage := make([]usageRowView, 0, len(ov.Usage))
	for _, u := range ov.Usage {
		pct := float64(u.Bytes) * 100 / float64(ov.Limits.MaxProject)
		usage = append(usage, usageRowView{Slug: u.Project.Slug, Name: u.Project.Name, Bytes: fmtBytes(u.Bytes),
			Percent: strconv.FormatFloat(pct, 'f', 1, 64), Count: u.Count, Purged: u.Purged, Archived: u.Project.Archived,
			Over: u.Bytes > ov.Limits.MaxProject})
	}
	rows := make([]attachmentRowView, 0, len(ov.Attachments))
	for _, a := range ov.Attachments {
		rows = append(rows, attachmentRowView{ID: a.ID, Project: a.ProjectSlug, Issue: a.IssueDisplayID, Filename: a.Filename,
			Type: a.MediaType, Size: fmtBytes(a.Size), Created: s.fmtWebTime(sql.NullTime{Time: a.CreatedAt, Valid: true}), Purged: a.Purged})
	}
	changes := make([]changeRowView, 0, len(ov.Changes))
	for _, c := range ov.Changes {
		v := changeRowView{At: s.fmtWebTime(sql.NullTime{Time: c.At, Valid: true}), Actor: c.ActorLogin, Via: c.Via, IP: c.IP,
			From: fmtSettingBytes(lang, c.OldValue), To: fmtSettingBytes(lang, c.NewValue)}
		switch c.Name {
		case store.SettingAttachMaxFile:
			v.Setting = i18n.T(lang, "server.web.admin_attachments.max_file")
		case store.SettingAttachMaxProject:
			v.Setting = i18n.T(lang, "server.web.admin_attachments.max_project")
		default:
			v.Setting = c.Name
		}
		switch c.Via {
		case "web":
			v.Via = i18n.T(lang, "server.web.security.via_web")
		case "command":
			v.Via = i18n.T(lang, "server.web.security.via_command")
		}
		if v.Actor == "" {
			v.Actor = "-"
		}
		changes = append(changes, v)
	}
	status := res.status
	if status == 0 {
		status = http.StatusOK
	}
	s.render(w, r, status, "admin_attachments.html", map[string]any{
		"User": p.User, "CSRF": p.Session.CSRFToken, "Notice": res.Notice, "Error": res.Error,
		"MaxFile": fmtBytes(ov.Limits.MaxFile), "MaxProject": fmtBytes(ov.Limits.MaxProject),
		"MaxFileMiB": (ov.Limits.MaxFile + mib - 1) / mib, "MaxProjectMiB": (ov.Limits.MaxProject + mib - 1) / mib,
		"Available": ov.Available, "Usage": usage, "Rows": rows, "Changes": changes,
		"FilterProject": filter.Project, "FilterIssue": filter.Issue, "Shown": len(rows),
	})
}

// parseMiB は上限の入力（MiB の正の整数）をバイト数にする。
func parseMiB(v string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 || n > maxLimitMiB {
		return 0, false
	}
	return n * mib, true
}

// adminAttachmentLimits は POST /admin/attachments/limits（max_file_mib・max_project_mib）。
func (s *Server) adminAttachmentLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	lang := reqLang(r)
	file, ok1 := parseMiB(r.PostFormValue("max_file_mib"))
	project, ok2 := parseMiB(r.PostFormValue("max_project_mib"))
	if !ok1 || !ok2 {
		s.renderAdminAttachments(w, r, p, attachAdminResult{Error: i18n.T(lang, "server.web.admin_attachments.err_limit", "max", maxLimitMiB), status: http.StatusBadRequest})
		return
	}
	changed, err := s.svc.SetAttachLimits(r.Context(), actor(r), store.AttachLimits{MaxFile: file, MaxProject: project}, s.clientIP(r))
	if err != nil {
		s.attachAdminError(w, r, p, err)
		return
	}
	if len(changed) == 0 {
		s.renderAdminAttachments(w, r, p, attachAdminResult{Notice: i18n.T(lang, "server.web.admin_attachments.notice_unchanged")})
		return
	}
	s.cfg.Logger.Info("admin", "action", "attach_limits", "actor", p.User.Login, "changed", strings.Join(changed, ","), "max_file", file, "max_project", project)
	s.renderAdminAttachments(w, r, p, attachAdminResult{Notice: i18n.T(lang, "server.web.admin_attachments.notice_changed",
		"file", fmtBytes(file), "project", fmtBytes(project))})
}

// adminAttachmentPurge は POST /admin/attachments/{id}/purge（reason は必須）。同じ本体を指す添付はまとめて消去済みになる。
func (s *Server) adminAttachmentPurge(w http.ResponseWriter, r *http.Request, p *principal) {
	lang := reqLang(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.renderAdminAttachments(w, r, p, attachAdminResult{Error: i18n.T(lang, "service.err.attach.not_found", "id", r.PathValue("id")), status: http.StatusNotFound})
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if reason == "" {
		s.renderAdminAttachments(w, r, p, attachAdminResult{Error: i18n.T(lang, "server.web.admin_attachments.err_reason"), status: http.StatusBadRequest})
		return
	}
	res, err := s.svc.PurgeAttachment(r.Context(), actor(r), id, reason)
	var se *service.Error
	if err != nil && (res == nil || errors.As(err, &se)) {
		s.attachAdminError(w, r, p, err)
		return
	}
	if err != nil {
		// 記録は残したが本体を消せなかった（整合の検査の --apply で消せる）
		s.cfg.Logger.Error("attachment purge: remove body", "sha256", res.SHA256, "err", err)
		s.renderAdminAttachments(w, r, p, attachAdminResult{Error: i18n.Text(lang, err), status: http.StatusInternalServerError})
		return
	}
	s.cfg.Logger.Info("admin", "action", "attach_purge", "actor", p.User.Login, "attachment", id, "sha256", res.SHA256, "count", len(res.AttachmentIDs))
	if !res.Changed {
		s.renderAdminAttachments(w, r, p, attachAdminResult{Notice: i18n.T(lang, "server.web.admin_attachments.notice_already_purged", "id", id)})
		return
	}
	s.renderAdminAttachments(w, r, p, attachAdminResult{Notice: i18n.TN(lang, "server.api.attach.purged", len(res.AttachmentIDs), "id", id, "count", len(res.AttachmentIDs))})
}

// attachAdminError は service の拒否を画面のエラーとして出す（それ以外は 500）。
func (s *Server) attachAdminError(w http.ResponseWriter, r *http.Request, p *principal, err error) {
	var se *service.Error
	if !errors.As(err, &se) {
		s.internalError(w, r, err)
		return
	}
	status := http.StatusBadRequest
	switch se.Kind {
	case service.NotFound:
		status = http.StatusNotFound
	case service.Forbidden:
		status = http.StatusForbidden
	}
	s.renderAdminAttachments(w, r, p, attachAdminResult{Error: i18n.Text(reqLang(r), se), status: status})
}
