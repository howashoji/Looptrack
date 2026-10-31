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

// 会話記録の管理者の画面（/admin/transcripts。docs/server/DESIGN.md §2-5）。上限（1 チャンク・1 利用者）と保持日数の設定と、
// 利用者ごとの使用量（会話の数とバイト数だけ）。中身・題名・ファイル名・セッション ID は出さない（service も返さない）。
// 管理者以外は入口の adminForbidden が 403 で止め、規則（管理者だけ・値の範囲）は service にある。CSRF は s.web が検査する。
// 消去の操作は置かない（運用者は管理用のサブコマンド purge-transcripts。§9-8）。

// maxRetentionDays は保持日数の入力として受け取る最大（100 年）。
const maxRetentionDays = store.MaxRetentionDays

// transcriptUsageRowView は利用者ごとの使用量の 1 行。
type transcriptUsageRowView struct {
	Login, Name, Bytes, Percent string
	Transcripts                 int64
	Disabled, Over              bool
}

type transcriptAdminResult struct {
	Notice, Error string
	status        int
}

// fmtRetention は保持日数を描く（0 は無期限）。
func fmtRetention(lang i18n.Lang, days int64) string {
	if days == 0 {
		return i18n.T(lang, "server.web.admin_transcripts.retention_forever")
	}
	return i18n.TN(lang, "server.web.admin_transcripts.retention_days_value", int(days), "n", days)
}

func (s *Server) adminTranscriptsPage(w http.ResponseWriter, r *http.Request, p *principal) {
	s.renderAdminTranscripts(w, r, p, transcriptAdminResult{})
}

func (s *Server) renderAdminTranscripts(w http.ResponseWriter, r *http.Request, p *principal, res transcriptAdminResult) {
	lang := reqLang(r)
	ov, err := s.svc.TranscriptOverview(r.Context(), actor(r))
	if err != nil {
		var se *service.Error
		if errors.As(err, &se) && se.Kind == service.Forbidden {
			http.Error(w, i18n.Text(lang, se), http.StatusForbidden)
			return
		}
		s.internalError(w, r, err)
		return
	}
	usage := make([]transcriptUsageRowView, 0, len(ov.Usage))
	for _, u := range ov.Usage {
		pct := float64(u.Bytes) * 100 / float64(ov.Limits.MaxUser)
		name := u.DisplayName
		if strings.TrimSpace(name) == "" {
			name = u.Login
		}
		usage = append(usage, transcriptUsageRowView{Login: u.Login, Name: name, Bytes: fmtBytes(u.Bytes),
			Percent: strconv.FormatFloat(pct, 'f', 1, 64), Transcripts: u.Transcripts, Disabled: u.Disabled, Over: u.Bytes > ov.Limits.MaxUser})
	}
	changes := make([]changeRowView, 0, len(ov.Changes))
	for _, c := range ov.Changes {
		v := changeRowView{At: s.fmtWebTime(sql.NullTime{Time: c.At, Valid: true}), Actor: c.ActorLogin, Via: c.Via, IP: c.IP}
		switch c.Name {
		case store.SettingTranscriptMaxChunk:
			v.Setting = i18n.T(lang, "server.web.admin_transcripts.max_chunk")
			v.From, v.To = fmtSettingBytes(lang, c.OldValue), fmtSettingBytes(lang, c.NewValue)
		case store.SettingTranscriptMaxUser:
			v.Setting = i18n.T(lang, "server.web.admin_transcripts.max_user")
			v.From, v.To = fmtSettingBytes(lang, c.OldValue), fmtSettingBytes(lang, c.NewValue)
		case store.SettingTranscriptRetentionDays:
			v.Setting = i18n.T(lang, "server.web.admin_transcripts.retention")
			v.From, v.To = fmtSettingDays(lang, c.OldValue), fmtSettingDays(lang, c.NewValue)
		default:
			v.Setting, v.From, v.To = c.Name, c.OldValue, c.NewValue
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
	s.render(w, r, status, "admin_transcripts.html", map[string]any{
		"User": p.User, "CSRF": p.Session.CSRFToken, "Notice": res.Notice, "Error": res.Error,
		"MaxChunk": fmtBytes(ov.Limits.MaxChunk), "MaxUser": fmtBytes(ov.Limits.MaxUser), "Retention": fmtRetention(lang, ov.Limits.RetentionDays),
		"MaxChunkMiB": (ov.Limits.MaxChunk + mib - 1) / mib, "MaxUserMiB": (ov.Limits.MaxUser + mib - 1) / mib, "RetentionDays": ov.Limits.RetentionDays,
		"MaxRetentionDays": maxRetentionDays, "Available": ov.Available, "Usage": usage, "Changes": changes,
	})
}

// fmtSettingDays は setting_changes の保持日数の値（10 進。空は未設定）を描く。
func fmtSettingDays(lang i18n.Lang, v string) string {
	if v == "" {
		return i18n.T(lang, "server.web.admin_attachments.unset")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return v
	}
	return fmtRetention(lang, n)
}

// parseRetentionDays は保持日数の入力（0 から maxRetentionDays の整数。0 は無期限）を読む。
func parseRetentionDays(v string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 || n > maxRetentionDays {
		return 0, false
	}
	return n, true
}

// adminTranscriptLimits は POST /admin/transcripts/limits（max_chunk_mib・max_user_mib・retention_days）。
func (s *Server) adminTranscriptLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	lang := reqLang(r)
	chunk, ok1 := parseMiB(r.PostFormValue("max_chunk_mib"))
	user, ok2 := parseMiB(r.PostFormValue("max_user_mib"))
	days, ok3 := parseRetentionDays(r.PostFormValue("retention_days"))
	if !ok1 || !ok2 || !ok3 {
		s.renderAdminTranscripts(w, r, p, transcriptAdminResult{Error: i18n.T(lang, "server.web.admin_transcripts.err_limit",
			"max", maxLimitMiB, "days", maxRetentionDays), status: http.StatusBadRequest})
		return
	}
	changed, err := s.svc.SetTranscriptLimits(r.Context(), actor(r), store.TranscriptLimits{MaxChunk: chunk, MaxUser: user, RetentionDays: days}, s.clientIP(r))
	if err != nil {
		var se *service.Error
		if !errors.As(err, &se) {
			s.internalError(w, r, err)
			return
		}
		status := http.StatusBadRequest
		if se.Kind == service.Forbidden {
			status = http.StatusForbidden
		}
		s.renderAdminTranscripts(w, r, p, transcriptAdminResult{Error: i18n.Text(lang, se), status: status})
		return
	}
	if len(changed) == 0 {
		s.renderAdminTranscripts(w, r, p, transcriptAdminResult{Notice: i18n.T(lang, "server.web.admin_transcripts.notice_unchanged")})
		return
	}
	s.cfg.Logger.Info("admin", "action", "transcript_limits", "actor", p.User.Login, "changed", strings.Join(changed, ","),
		"max_chunk", chunk, "max_user", user, "retention_days", days)
	s.renderAdminTranscripts(w, r, p, transcriptAdminResult{Notice: i18n.T(lang, "server.web.admin_transcripts.notice_changed",
		"chunk", fmtBytes(chunk), "user", fmtBytes(user), "retention", fmtRetention(lang, days))})
}
