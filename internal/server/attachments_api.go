package server

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 添付（エビデンスのファイル）の REST。権限・上限・消去の規則は service（attachments.go）にあり、ここでは HTTP の形だけを決める。
//
//	POST /api/v1/issues/{id}/attachments        本文はファイルそのもの。名前は X-Looptrack-Filename（UTF-8 をパーセントで符号化）、
//	                                            形式は Content-Type。editor 以上
//	GET  /api/v1/issues/{id}/attachments        一覧（メタデータだけ。消去済みも purged で返す）。閲覧できる人
//	GET  /api/v1/attachments/{id}               本体。閲覧できる人
//	POST /api/v1/attachments/{id}/purge         本体の消去（{"reason": "…"}）。管理者（利用者の役割 admin）だけ
//
// 本文の上限は添付の経路だけ 1 ファイルの上限に合わせる（ほかの経路は maxRequestBody のまま）。上限を超えた本文は
// 413 で拒み、本体もメタデータも残さない。Content-Length が上限を超えていれば本文を読まずに拒む。
// http.Server の ReadTimeout（30 秒）・WriteTimeout（60 秒）では遅い回線で 20MiB を送りきれないので、
// 添付の経路だけ締切を attachWindow まで延ばす（配布物の binWriteWindow と同じやり方）。
//
// どの応答にも X-Content-Type-Options: nosniff と Content-Security-Policy: sandbox を付ける（ServeHTTP が attachmentPath で
// 見分ける。認証の前に付けるので 401 にも付く）。本体を開いたブラウザに中身を推測させず、スクリプトも動かさないため。
// 画面の中に表示してよい形式は、service.AttachmentServeType が申告と本体の先頭の両方から決める。

// attachWindow は添付の本文の受け取りと本体の送信に許す時間。
const attachWindow = 10 * time.Minute

// attachFilenameHeader は添付のファイル名を渡すヘッダ（UTF-8 をパーセントで符号化した 1 区間）。
const attachFilenameHeader = "X-Looptrack-Filename"

// attachCSP は添付の応答の Content-Security-Policy（サーバ全体の既定を置き換える）。
const attachCSP = "sandbox; default-src 'none'"

type attachmentJSON struct {
	ID        int64  `json:"id"`
	Issue     string `json:"issue"`
	Project   string `json:"project"`
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"created_at"`
	Via       string `json:"via"`
	Purged    bool   `json:"purged"`
	// URL は本体を取る経路（サーバの基点からの絶対パス）
	URL string `json:"url"`
	// Inline は本体の GET が inline で返るか（service.AttachmentInline。画面はこれが true のものだけを画像として埋め込み、
	// 形式を自分で判定し直さない）
	Inline bool `json:"inline"`
}

func (s *Server) toAttachmentJSON(a store.Attachment) attachmentJSON {
	return attachmentJSON{ID: a.ID, Issue: a.IssueDisplayID, Project: a.ProjectSlug, Filename: a.Filename, MediaType: a.MediaType,
		Size: a.Size, SHA256: a.SHA256, CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339), Via: a.Via, Purged: a.Purged,
		URL: s.cfg.BasePath + "/api/v1/attachments/" + strconv.FormatInt(a.ID, 10), Inline: s.svc.AttachmentInline(a)}
}

// attachmentPath は添付の経路か（ServeHTTP が、認証より前にすべての応答へ attachCSP を付けるのに使う。
// X-Content-Type-Options: nosniff はサーバ全体の既定で、どの応答にも付いている）。
func (s *Server) attachmentPath(path string) bool {
	api := s.cfg.BasePath + "/api/v1/"
	return strings.HasPrefix(path, api+"attachments/") || (strings.HasPrefix(path, api+"issues/") && strings.HasSuffix(path, "/attachments"))
}

// extendDeadlines は添付の経路だけ、読み取りと書き込みの締切を延ばす（できない接続では何もしない）。
func extendDeadlines(w http.ResponseWriter, read bool) {
	rc := http.NewResponseController(w)
	until := time.Now().Add(attachWindow)
	if read {
		_ = rc.SetReadDeadline(until)
	}
	_ = rc.SetWriteDeadline(until)
}

// attachmentID はパスの {id}（正の整数）。読めなければ 404（存在しない添付と同じ応答）。
func (s *Server) attachmentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", i18n.T(reqLang(r), "service.err.attach.not_found", "id", raw))
		return 0, false
	}
	return id, true
}

// apiPostAttachment は POST /issues/{id}/attachments。
func (s *Server) apiPostAttachment(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, true)
	name, err := url.PathUnescape(r.Header.Get(attachFilenameHeader))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.attach_filename_header", "header", attachFilenameHeader))
		return
	}
	at, err := s.svc.Attach(r.Context(), actor(r), service.AttachInput{Issue: r.PathValue("id"), Project: r.URL.Query().Get("project"),
		Filename: name, MediaType: r.Header.Get("Content-Type"), Body: r.Body, Declared: r.ContentLength})
	if err != nil {
		var se *service.Error
		if errors.As(err, &se) && se.Kind == service.TooLarge {
			// 残りの本文は読まずに接続を閉じる（上限を超えた分まで受け取らない）
			w.Header().Set("Connection", "close")
		}
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": s.toAttachmentJSON(*at),
		"message": i18n.TN(reqLang(r), "server.api.attach.added", int(at.Size), "id", at.ID, "filename", at.Filename, "issue", at.IssueDisplayID, "size", at.Size)})
}

// apiListAttachments は GET /issues/{id}/attachments。
func (s *Server) apiListAttachments(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Attachments(r.Context(), actor(r), r.PathValue("id"), r.URL.Query().Get("project"))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	out := make([]attachmentJSON, 0, len(list))
	for _, a := range list {
		out = append(out, s.toAttachmentJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"issue": strings.ToUpper(r.PathValue("id")), "attachments": out, "count": len(out)})
}

// apiGetAttachment は GET /attachments/{id}（本体）。png・jpeg・gif・webp だけ inline、ほかはダウンロードさせる。
func (s *Server) apiGetAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := s.attachmentID(w, r)
	if !ok {
		return
	}
	at, f, err := s.svc.OpenAttachment(r.Context(), actor(r), id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	defer f.Close()
	extendDeadlines(w, false)
	ct, inline := service.AttachmentServeTypeOf(at.MediaType, f)
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": at.Filename}))
	h.Set("X-Looptrack-SHA256", at.SHA256)
	http.ServeContent(w, r, "", at.CreatedAt, f)
}

type purgeRequest struct {
	Reason string `json:"reason"`
}

// apiPurgeAttachment は POST /attachments/{id}/purge（管理者だけ）。同じ本体を指す添付はすべて消去済みになる。
func (s *Server) apiPurgeAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := s.attachmentID(w, r)
	if !ok {
		return
	}
	var req purgeRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", i18n.T(reqLang(r), "server.api.err.invalid_json", "reason", err.Error()))
			return
		}
	}
	res, err := s.svc.PurgeAttachment(r.Context(), actor(r), id, req.Reason)
	var se *service.Error
	if err != nil && (res == nil || errors.As(err, &se)) {
		s.serviceError(w, r, err)
		return
	}
	ids := res.AttachmentIDs
	if ids == nil {
		ids = []int64{}
	}
	body := map[string]any{"sha256": res.SHA256, "attachment_ids": ids, "changed": res.Changed}
	if err != nil {
		// 消去は記録したが本体を消せなかった（整合の検査の --apply で消せる）
		s.cfg.Logger.Error("attachment purge: remove body", "sha256", res.SHA256, "err", err)
		body["error"] = map[string]any{"code": "purge_remove_failed", "message": i18n.Text(reqLang(r), err)}
		writeJSON(w, http.StatusInternalServerError, body)
		return
	}
	body["message"] = i18n.TN(reqLang(r), "server.api.attach.purged", len(ids), "id", id, "count", len(ids))
	writeJSON(w, http.StatusOK, body)
}
