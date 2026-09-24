package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 閲覧画面。以前のビューア（1.0.0 より前）のハブ・ボード・一覧・トレース・詳細を /im 配下へ移す。
// 画面は静的な骨組みだけを返し、データは API から取る（CSP でインラインスクリプトを禁じているため）。

// boardIssueJSON は 1 イシュー分（以前のビューアの payload と同じキー）。
type boardIssueJSON struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
	Priority  string   `json:"priority"`
	Labels    []string `json:"labels"`
	Parent    string   `json:"parent"`
	BlockedBy []string `json:"blocked_by"`
	Traces    []string `json:"traces"`
	Refs      []string `json:"refs"`
	Created   string   `json:"created"`
	Updated   string   `json:"updated"`
	Origin    string   `json:"origin"`
	// Version はイシューの版（コメント・状態の変更でも進む）。画面は詳細の本文をこの版ごとに取り直す。
	// 本文はボードに載せない（全件の本文で応答が数 MB になり、4 秒ごとの見直しが回線を埋めるため）。
	// 本文は詳細を開いたときに GET /issues/{id} で 1 件ずつ取り、本文の検索は GET …/board/search で行う
	Version  int    `json:"version"`
	Path     string `json:"path"`
	Imported bool   `json:"imported"` // 旧ファイルモードから取り込んだ（path が archive/file-mode に実在する）。画面はこのときだけ「ファイル」行を出す
	Ready    bool   `json:"ready"`
	// Assignee は担当者の login（未設定は空）、AssigneeInactive は担当が今このプロジェクトで変更できない印
	Assignee         string `json:"assignee"`
	AssigneeInactive bool   `json:"assignee_inactive"`
	// FeedbackPending は未応答のフィードバックの件数（カードの「反応 N」と絞り込み「未応答の反応」）
	FeedbackPending int `json:"feedback_pending"`
}

// boardMemberJSON は担当にできる利用者（詳細ドロワーの担当の選択肢）。
type boardMemberJSON struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// apiBoard は閲覧画面が使うプロジェクト 1 件分のデータ（本文は含まない。上の Version の注釈）。
// 画面は 4 秒ごとに If-None-Match を付けて見直すので、内容が変わっていなければ本文の無い 304 を返す（転送も再描画もしない）。
// ETag は generated（分単位の時刻）を除いた内容から作る。時刻だけが進んだ周期は変化として扱わない。
// 304 でも画面の「… 時点」を進められるよう、generated は X-Looptrack-Generated のヘッダでも返す。
func (s *Server) apiBoard(w http.ResponseWriter, r *http.Request) {
	pr, role, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	rows, err := store.LoadFronts(r.Context(), s.db, pr.ID) // 本文を使わないので frontmatter だけを読む
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	imported, err := store.ImportedIssueIDs(r.Context(), s.db, pr.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pending, _, err := s.pendingByIssue(r.Context(), pr) // 4 秒ごとの見直しで同じ SQL を 1 回引く（§5-8-6）
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]domain.Issue, 0, len(rows))
	for _, row := range rows {
		items = append(items, domain.FromDocument(row.Doc))
	}
	set := domain.NewSet(items)
	out := make([]boardIssueJSON, 0, len(rows))
	for _, row := range rows {
		it := domain.FromDocument(row.Doc)
		b := boardIssueJSON{ID: it.ID, Title: it.Title, Type: it.Type, Status: it.Status, Priority: it.Priority,
			Labels: nonNil(it.Labels), Parent: it.Parent, BlockedBy: nonNil(it.BlockedBy), Traces: nonNil(it.Traces),
			Refs: nonNil(it.Refs), Created: it.Created, Updated: it.Updated,
			Version: row.Version, Path: row.FileName, Imported: imported[row.ID], Ready: set.IsReady(it),
			Assignee: row.Assignee.Login, AssigneeInactive: row.Assignee.Inactive, FeedbackPending: pending[it.ID]}
		if f := row.Doc.Field("origin"); f != nil && !f.IsList {
			b.Origin = f.Value
		}
		out = append(out, b)
	}
	assignable, err := store.AssignableMembers(r.Context(), s.db, pr.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	members := make([]boardMemberJSON, 0, len(assignable))
	for _, m := range assignable {
		members = append(members, boardMemberJSON{Login: m.Login, Name: m.Name})
	}
	body := map[string]any{
		"slug": pr.Slug, "project": pr.Name, "prefix": pr.Prefix, "issues": out,
		"statuses": domain.Statuses, "closed_statuses": domain.ClosedStatuses,
		"types": domain.Types, "priorities": domain.Priorities,
		// 担当の表示・絞り込み・変更フォーム
		"me": principalFrom(r.Context()).User.Login, "can_edit": canWrite(role), "members": members,
	}
	tag, err := contentETag(body)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	generated := s.stamp()
	h := w.Header()
	h.Set("ETag", tag)
	h.Set("Cache-Control", "no-cache") // 保存してよいが、使う前に必ず見直す（304 で済ませる）
	h.Set("X-Looptrack-Generated", generated)
	if etagMatches(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body["generated"] = generated
	writeJSON(w, http.StatusOK, body)
}

// contentETag は応答の内容から弱い ETag を作る（同じ内容なら同じ値。時刻のような揺れる値は呼び出し側で除いておく）。
func contentETag(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return `W/"` + hex.EncodeToString(sum[:16]) + `"`, nil
}

// etagMatches は If-None-Match（カンマ区切り・* を含む）が tag に当たるかを弱い比較で見る（RFC 9110 13.1.2）。
func etagMatches(header, tag string) bool {
	if header == "" {
		return false
	}
	want := strings.TrimPrefix(tag, "W/")
	for _, v := range strings.Split(header, ",") {
		v = strings.TrimSpace(v)
		if v == "*" || strings.TrimPrefix(v, "W/") == want {
			return true
		}
	}
	return false
}

// boardSearchMaxRunes は本文の検索語の上限（画面の検索欄の 1 語。長すぎる入力で全件の走査を重くしない）。
const boardSearchMaxRunes = 200

// apiBoardSearch は本文（コメントを含む。以前のボードが検索に使っていた範囲と同じ）に検索語を含むイシューの ID を返す。
// 本文の検索をサーバで行う理由: 画面で検索するには全件の本文を一度は送ることになり（数 MB）、
// 検索欄に打つたびにその転送を待たせる。サーバなら返すのは当たった ID の列だけで済む。
// 題名・ID・ラベルなど本文以外の項目の照合は画面に残す（ボードの JSON に既にあり、入力ごとに即座に絞れる）。
// 大文字小文字は区別しない（画面の toLowerCase と同じく小文字にそろえて部分一致）。
func (s *Server) apiBoardSearch(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if rs := []rune(q); len(rs) > boardSearchMaxRunes {
		q = string(rs[:boardSearchMaxRunes])
	}
	ids := []string{}
	if q != "" {
		rows, err := store.LoadDocuments(r.Context(), s.db, pr.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		needle := strings.ToLower(q)
		for _, row := range rows {
			if strings.Contains(strings.ToLower(mdformat.RenderBody(row.Doc)), needle) {
				ids = append(ids, domain.FromDocument(row.Doc).ID)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"q": q, "ids": ids})
}

// assignSubmit は詳細ドロワーの担当変更フォーム（POST /im/p/{slug}/issues/{id}/assign）。
// 画面は閲覧のみの原則の例外（DESIGN.md §5-2）。JS を使わない通常の POST で、web() が CSRF を検査する。
// CLI / MCP と同じ service.Assign を通り、成功はボードの同じイシューへ 303 で戻す。失敗は同じ文言を assign.html に出す。
func (s *Server) assignSubmit(w http.ResponseWriter, r *http.Request, p *principal) {
	slug := r.PathValue("slug")
	pr, row, err := s.resolveIssue(r.Context(), reqLang(r), p.User, r.PathValue("id"), slug, true)
	if err == nil {
		var res *service.AssignResult
		res, err = s.svc.Assign(r.Context(), actor(r), pr, row.ID, r.PostFormValue("assignee"), r.PostFormValue("override_reason"))
		if err == nil {
			http.Redirect(w, r, s.cfg.BasePath+"/p/"+pr.Slug+"/#"+res.Issue.Item.ID, http.StatusSeeOther)
			return
		}
	}
	var se *service.Error
	if !errors.As(err, &se) {
		s.internalError(w, r, err)
		return
	}
	status := map[service.Kind]int{service.Invalid: http.StatusBadRequest, service.NotFound: http.StatusNotFound,
		service.Forbidden: http.StatusForbidden, service.Conflict: http.StatusConflict, service.Rejected: http.StatusUnprocessableEntity}[se.Kind]
	if status == 0 {
		status = http.StatusBadRequest
	}
	s.render(w, r, status, "assign.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath,
		"Slug": slug, "ID": strings.ToUpper(r.PathValue("id")), "Error": i18n.Text(reqLang(r), se), "Overridable": se.Overridable,
		"Assignee": r.PostFormValue("assignee")})
}

// hub はプロジェクト選択画面。
func (s *Server) hub(w http.ResponseWriter, r *http.Request, p *principal) {
	s.render(w, r, http.StatusOK, "hub.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken, "Base": s.cfg.BasePath})
}

// board はプロジェクトのイシュー画面（ボード / 一覧 / トレース）。
func (s *Server) board(w http.ResponseWriter, r *http.Request, p *principal) {
	slug := r.PathValue("slug")
	pr, role, err := s.resolveProject(r.Context(), reqLang(r), p.User, slug)
	if err != nil {
		s.notFoundPage(w, r, i18n.T(reqLang(r), "server.api.err.project_not_found", "project", slug))
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, s.cfg.BasePath+"/p/"+pr.Slug+"/", http.StatusMovedPermanently)
		return
	}
	s.render(w, r, http.StatusOK, "board.html", map[string]any{"User": p.User, "CSRF": p.Session.CSRFToken,
		"Base": s.cfg.BasePath, "Slug": pr.Slug, "Name": pr.Name,
		// 起票ボタン。API の起票と同じ canWrite（editor 以上）で出し分ける。ドロワーのフォームは board の can_edit で出す
		"CanEdit": canWrite(role)})
}

func (s *Server) notFoundPage(w http.ResponseWriter, r *http.Request, msg string) {
	p := principalFrom(r.Context())
	data := map[string]any{"Base": s.cfg.BasePath, "Message": msg}
	if p != nil {
		data["User"] = p.User
		if p.Session != nil {
			data["CSRF"] = p.Session.CSRFToken // ヘッダのメニューのログアウト
		}
	}
	s.render(w, r, http.StatusNotFound, "notfound.html", data)
}
