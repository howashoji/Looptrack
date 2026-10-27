package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 会話記録（利用者ごとの置き場。docs/server/DESIGN.md §2-5・§9-8）の REST。権限（持ち主だけ）・連番・上限・中身の検査は
// service（transcripts.go）にあり、ここでは HTTP の形だけを決める。
//
//	POST /api/v1/projects/{slug}/transcripts     登録（{agent, vendor_session_id, machine_label, origin_os, agent_version}）。
//	                                             新しく作れば 201、同じものが既にあれば 200
//	POST /api/v1/transcripts/{id}/chunks         追記。本文はチャンクそのもの（Content-Encoding: gzip なら gzip のまま置く）。
//	                                             If-Match: "<今の seq>" が必須。位置と印は X-Looptrack-Transcript-* のヘッダ
//	GET  /api/v1/transcripts?project=            自分の会話記録の一覧（メタデータだけ）
//	GET  /api/v1/transcripts/{id}                会話記録 1 つとチャンクの一覧
//	GET  /api/v1/transcripts/{id}/chunks/{seq}   チャンクの本体（置いた形のまま。X-Looptrack-Transcript-Encoding で形を知らせる）
//	POST /api/v1/transcripts/{id}/purge          本体の消去（{"reason": "…"}）
//
// ほかの利用者の会話記録は、どの経路でも無いものと同じ 404（service が持ち主で絞る）。
// 追記の本文の上限は transcript_max_chunk_bytes（service が設定から読み、Content-Length が上限を超えていれば読まずに拒む）。
// 読み書きの締切は添付と同じく attachWindow まで延ばす（extendDeadlines。配布物の binWriteWindow と同じやり方）。
// どの応答にも Content-Security-Policy: sandbox を付ける（ServeHTTP が transcriptPath で見分ける。X-Content-Type-Options: nosniff は
// サーバ全体の既定）。本体の応答は Content-Disposition: attachment で、Content-Encoding は付けない（HTTP の層で勝手に伸ばされないように。
// gzipJSON は JSON の応答しか圧縮しない）。

// 追記の位置と印を渡すヘッダ（§9-8）。
const (
	transcriptFileHeader         = "X-Looptrack-Transcript-File" // file_key（パーセント符号化）
	transcriptOffsetHeader       = "X-Looptrack-Transcript-Offset"
	transcriptLengthHeader       = "X-Looptrack-Transcript-Length"
	transcriptNonceHeader        = "X-Looptrack-Transcript-Nonce"
	transcriptPlaceholdersHeader = "X-Looptrack-Transcript-Placeholders" // プレースホルダの種類の名前の JSON の配列
	transcriptStatsHeader        = "X-Looptrack-Transcript-Stats"        // 件数の JSON のオブジェクト
	transcriptEncodingHeader     = "X-Looptrack-Transcript-Encoding"     // 本体の応答: identity / gzip
)

type transcriptFileJSON struct {
	FileKey string `json:"file_key"`
	Length  int64  `json:"length"`
}

// transcriptJSON は会話記録 1 つの今の状態（登録・追記・取得の応答）。
type transcriptJSON struct {
	ID              int64                `json:"id"`
	Project         string               `json:"project"`
	Agent           string               `json:"agent"`
	VendorSessionID string               `json:"vendor_session_id"`
	MachineLabel    string               `json:"machine_label"`
	OriginOS        string               `json:"origin_os"`
	AgentVersion    string               `json:"agent_version"`
	CreatedAt       string               `json:"created_at"`
	Via             string               `json:"via"`
	Seq             int64                `json:"seq"`   // 今の連番（次の追記の If-Match）
	Files           []transcriptFileJSON `json:"files"` // ファイルごとの送った長さ（次の src_offset）
	// TranscriptsEnabled は記録を送ったプロジェクトのルール session.transcripts（クライアントが覚える）
	TranscriptsEnabled bool `json:"transcripts_enabled"`
}

func toTranscriptJSON(st service.TranscriptState) transcriptJSON {
	t := st.Transcript
	out := transcriptJSON{ID: t.ID, Project: st.ProjectSlug, Agent: t.Agent, VendorSessionID: t.VendorSessionID, MachineLabel: t.MachineLabel,
		OriginOS: t.OriginOS, AgentVersion: t.AgentVersion, CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339), Via: t.Via, Seq: st.Seq,
		Files: make([]transcriptFileJSON, 0, len(st.Files)), TranscriptsEnabled: st.TranscriptsEnabled}
	for _, f := range st.Files {
		out.Files = append(out.Files, transcriptFileJSON{FileKey: f.FileKey, Length: f.Length})
	}
	return out
}

// transcriptChunkJSON はチャンク 1 つのメタデータ。
type transcriptChunkJSON struct {
	Seq          int64           `json:"seq"`
	FileKey      string          `json:"file_key"`
	FileKind     string          `json:"file_kind"`
	SrcOffset    int64           `json:"src_offset"`
	SrcLength    int64           `json:"src_length"`
	LineCount    int64           `json:"line_count"`
	SHA256       string          `json:"sha256"`
	Size         int64           `json:"size"`
	Encoding     string          `json:"encoding"`
	Nonce        string          `json:"nonce"`
	Placeholders json.RawMessage `json:"placeholders"`
	Stats        json.RawMessage `json:"stats"`
	CreatedAt    string          `json:"created_at"`
	Via          string          `json:"via"`
	Purged       bool            `json:"purged"`
	// URL は本体を取る経路（サーバの基点からの絶対パス）
	URL string `json:"url"`
}

// rawJSON は保存した JSON の本文を応答に埋める（壊れていれば fallback）。
func rawJSON(s, fallback string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(s)
}

func (s *Server) toTranscriptChunkJSON(c store.TranscriptChunk) transcriptChunkJSON {
	return transcriptChunkJSON{Seq: c.Seq, FileKey: c.FileKey, FileKind: c.FileKind, SrcOffset: c.SrcOffset, SrcLength: c.SrcLength,
		LineCount: c.LineCount, SHA256: c.SHA256, Size: c.Size, Encoding: c.Encoding, Nonce: c.Nonce,
		Placeholders: rawJSON(c.Placeholders, "[]"), Stats: rawJSON(c.Stats, "{}"), CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		Via: c.Via, Purged: c.Purged,
		URL: s.cfg.BasePath + "/api/v1/transcripts/" + strconv.FormatInt(c.TranscriptID, 10) + "/chunks/" + strconv.FormatInt(c.Seq, 10)}
}

// transcriptSummaryJSON は一覧の 1 行（件数と大きさは消去していないチャンクだけを数える）。
type transcriptSummaryJSON struct {
	ID              int64  `json:"id"`
	Project         string `json:"project"`
	Agent           string `json:"agent"`
	VendorSessionID string `json:"vendor_session_id"`
	MachineLabel    string `json:"machine_label"`
	OriginOS        string `json:"origin_os"`
	AgentVersion    string `json:"agent_version"`
	CreatedAt       string `json:"created_at"`
	Seq             int64  `json:"seq"`
	Chunks          int64  `json:"chunks"`
	Files           int64  `json:"files"`
	Bytes           int64  `json:"bytes"`
	LastChunkAt     string `json:"last_chunk_at,omitempty"`
	Purges          int64  `json:"purges"`
	Purged          bool   `json:"purged"`
}

func toTranscriptSummaryJSON(t store.TranscriptSummary) transcriptSummaryJSON {
	out := transcriptSummaryJSON{ID: t.ID, Project: t.ProjectSlug, Agent: t.Agent, VendorSessionID: t.VendorSessionID, MachineLabel: t.MachineLabel,
		OriginOS: t.OriginOS, AgentVersion: t.AgentVersion, CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339), Seq: t.Seq,
		Chunks: t.LiveChunks, Files: t.LiveFiles, Bytes: t.LiveBytes, Purges: t.Purges, Purged: t.Purged()}
	if !t.LastChunkAt.IsZero() {
		out.LastChunkAt = t.LastChunkAt.UTC().Format(time.RFC3339)
	}
	return out
}

// transcriptPath は会話記録の経路か（ServeHTTP が、認証より前にすべての応答へ attachCSP を付けるのに使う）。
func (s *Server) transcriptPath(path string) bool {
	api := s.cfg.BasePath + "/api/v1/"
	return path == api+"transcripts" || strings.HasPrefix(path, api+"transcripts/") ||
		(strings.HasPrefix(path, api+"projects/") && strings.HasSuffix(path, "/transcripts"))
}

// transcriptID はパスの {id}（正の整数）。読めなければ 404（存在しない会話記録と同じ応答）。
func transcriptID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", i18n.T(reqLang(r), "service.err.transcript.not_found", "id", raw))
		return 0, false
	}
	return id, true
}

// setTranscriptETag は今の連番を ETag に載せる（次の追記の If-Match にそのまま使える形）。
func setTranscriptETag(w http.ResponseWriter, seq int64) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(seq, 10)+`"`)
}

type registerTranscriptRequest struct {
	Agent           string `json:"agent"`
	VendorSessionID string `json:"vendor_session_id"`
	MachineLabel    string `json:"machine_label"`
	OriginOS        string `json:"origin_os"`
	AgentVersion    string `json:"agent_version"`
}

// apiRegisterTranscript は POST /projects/{slug}/transcripts。
func (s *Server) apiRegisterTranscript(w http.ResponseWriter, r *http.Request) {
	var req registerTranscriptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	st, err := s.svc.RegisterTranscript(r.Context(), actor(r), service.RegisterTranscriptInput{Project: r.PathValue("slug"), Agent: req.Agent,
		VendorSessionID: req.VendorSessionID, MachineLabel: req.MachineLabel, OriginOS: req.OriginOS, AgentVersion: req.AgentVersion})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	status := http.StatusOK
	if st.Created {
		status = http.StatusCreated
	}
	setTranscriptETag(w, st.Seq)
	writeJSON(w, status, map[string]any{"transcript": toTranscriptJSON(*st), "created": st.Created})
}

// headerInt は数のヘッダを読む（無い・読めなければ false）。
func headerInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(name)), 10, 64)
	return n, err == nil
}

// apiAppendTranscriptChunk は POST /transcripts/{id}/chunks。
func (s *Server) apiAppendTranscriptChunk(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, true)
	id, ok := transcriptID(w, r)
	if !ok {
		return
	}
	lang := reqLang(r)
	badHeader := func(name string) {
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(lang, "server.api.err.transcript_header", "header", name))
	}
	key, err := url.PathUnescape(r.Header.Get(transcriptFileHeader))
	if err != nil {
		badHeader(transcriptFileHeader)
		return
	}
	offset, ok := headerInt(r, transcriptOffsetHeader)
	if !ok {
		badHeader(transcriptOffsetHeader)
		return
	}
	length, ok := headerInt(r, transcriptLengthHeader)
	if !ok {
		badHeader(transcriptLengthHeader)
		return
	}
	var placeholders []string
	if v := strings.TrimSpace(r.Header.Get(transcriptPlaceholdersHeader)); v != "" {
		if err := json.Unmarshal([]byte(v), &placeholders); err != nil {
			badHeader(transcriptPlaceholdersHeader)
			return
		}
	}
	res, err := s.svc.AppendTranscriptChunk(r.Context(), actor(r), service.AppendTranscriptChunkInput{TranscriptID: id,
		IfMatch: r.Header.Get("If-Match"), FileKey: key, SrcOffset: offset, SrcLength: length, Encoding: r.Header.Get("Content-Encoding"),
		Nonce: r.Header.Get(transcriptNonceHeader), Placeholders: placeholders, Stats: r.Header.Get(transcriptStatsHeader),
		Body: r.Body, Declared: r.ContentLength})
	if err != nil {
		var se *service.Error
		if errors.As(err, &se) && se.Kind == service.TooLarge {
			// 残りの本文は読まずに接続を閉じる（上限を超えた分まで受け取らない）
			w.Header().Set("Connection", "close")
		}
		s.serviceError(w, r, err)
		return
	}
	setTranscriptETag(w, res.State.Seq)
	writeJSON(w, http.StatusCreated, map[string]any{"chunk": s.toTranscriptChunkJSON(res.Chunk), "transcript": toTranscriptJSON(res.State)})
}

// apiListTranscripts は GET /transcripts?project=。
func (s *Server) apiListTranscripts(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.ListTranscripts(r.Context(), actor(r), r.URL.Query().Get("project"))
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	out := make([]transcriptSummaryJSON, 0, len(list))
	for _, t := range list {
		out = append(out, toTranscriptSummaryJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"transcripts": out, "count": len(out)})
}

// apiGetTranscript は GET /transcripts/{id}。
func (s *Server) apiGetTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := transcriptID(w, r)
	if !ok {
		return
	}
	d, err := s.svc.GetTranscript(r.Context(), actor(r), id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	chunks := make([]transcriptChunkJSON, 0, len(d.Chunks))
	for _, c := range d.Chunks {
		chunks = append(chunks, s.toTranscriptChunkJSON(c))
	}
	setTranscriptETag(w, d.Seq)
	writeJSON(w, http.StatusOK, map[string]any{"transcript": toTranscriptJSON(d.TranscriptState), "chunks": chunks})
}

// apiGetTranscriptChunk は GET /transcripts/{id}/chunks/{seq}（本体。置いた形のまま返す）。
func (s *Server) apiGetTranscriptChunk(w http.ResponseWriter, r *http.Request) {
	id, ok := transcriptID(w, r)
	if !ok {
		return
	}
	rawSeq := r.PathValue("seq")
	seq, err := strconv.ParseInt(rawSeq, 10, 64)
	if err != nil || seq <= 0 {
		writeError(w, http.StatusNotFound, "not_found", i18n.T(reqLang(r), "service.err.transcript.chunk_not_found", "id", id, "seq", rawSeq))
		return
	}
	c, f, err := s.svc.OpenTranscriptChunk(r.Context(), actor(r), id, seq)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	defer f.Close()
	extendDeadlines(w, false)
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", "attachment")
	h.Set(transcriptEncodingHeader, c.Encoding)
	h.Set("X-Looptrack-SHA256", c.SHA256)
	h.Del("Content-Encoding")
	http.ServeContent(w, r, "", c.CreatedAt, f)
}

// apiPurgeTranscript は POST /transcripts/{id}/purge（持ち主だけ）。
func (s *Server) apiPurgeTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := transcriptID(w, r)
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
	res, err := s.svc.PurgeTranscript(r.Context(), actor(r), id, req.Reason)
	var se *service.Error
	if err != nil && (res == nil || errors.As(err, &se)) {
		s.serviceError(w, r, err)
		return
	}
	ids := res.ChunkIDs
	if ids == nil {
		ids = []int64{}
	}
	body := map[string]any{"transcript_id": res.TranscriptID, "changed": res.Changed, "chunk_ids": ids, "removed": res.Removed}
	if err != nil {
		// 消去は記録したが本体を消せなかった（repair-transcripts --apply で消せる）
		s.cfg.Logger.Error("transcript purge: remove body", "transcript", id, "err", err)
		body["error"] = map[string]any{"code": "purge_remove_failed", "message": i18n.Text(reqLang(r), err)}
		writeJSON(w, http.StatusInternalServerError, body)
		return
	}
	body["message"] = i18n.TN(reqLang(r), "server.api.transcript.purged", len(ids), "id", id, "count", len(ids))
	writeJSON(w, http.StatusOK, body)
}
