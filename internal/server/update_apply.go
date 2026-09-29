package server

import (
	"net/http"
	"net/url"

	"github.com/howashoji/looptrack/internal/i18n"
)

// updateApply は POST {base}/update/apply（帯の「更新する」。デスクトップ版だけ）。CSRF は s.web が検査する。
// 置き換えは背景で始め（Config.UpdateApplier.StartUpdate）、元の画面へ戻す。戻った画面の帯は進行中を出し、
// 成功すればアプリが起動し直す（同じポートで待ち受けるので、読み込み直すと新しい版の画面になる）。
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request, p *principal) {
	if s.cfg.UpdateApplier == nil || s.cfg.UpdateServer {
		s.notFoundPage(w, r, i18n.T(reqLang(r), "server.web.err.page_not_found"))
		return
	}
	started := s.cfg.UpdateApplier.StartUpdate()
	s.cfg.Logger.Info("web", "action", "update-apply", "user", p.User.Login, "started", started)
	http.Redirect(w, r, s.refererPath(r), http.StatusSeeOther)
}

// refererPath は同じ出どころの Referer のパス（クエリ付き。safeNext を通るものだけ）。無ければ {base}/。
// Referrer-Policy は same-origin なので、画面のフォームからの POST には付いてくる。
func (s *Server) refererPath(r *http.Request) string {
	u, err := url.Parse(r.Referer())
	if err != nil || u.Host != r.Host {
		return s.cfg.BasePath + "/"
	}
	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return s.safeNext(p)
}
