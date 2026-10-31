package cli

// hook と looptrack handoff が記憶の API を使う口（kit の引き継ぎの置き場 server。DESIGN.md §9-9）。
// 検査はサーバの service にあり、ここでは要求と応答の形だけを扱う。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/i18n"
)

// MemoryAccess は 1 つのプロジェクトの記憶の API の口。
type MemoryAccess struct {
	ctx *Ctx
	cl  *api.Client
}

// MemoryItem は記憶 1 件（一覧では Content が空）。
type MemoryItem struct {
	Scope     string `json:"scope"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	Summary   string `json:"summary"`
	Content   string `json:"content"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

// MemoryVersionInfo は版の履歴 1 件（本文は入らない）。
type MemoryVersionInfo struct {
	Version   int64  `json:"version"`
	Op        string `json:"op"`
	User      string `json:"user"`
	SessionID string `json:"session_id"`
	CreatedAt string `json:"created_at"`
	WrittenAt string `json:"written_at"`
}

// WrittenTime はこの版の本文が書かれた時刻（申告が無ければ版の時刻）。
func (v MemoryVersionInfo) WrittenTime() (time.Time, error) {
	if v.WrittenAt != "" {
		return time.Parse(time.RFC3339, v.WrittenAt)
	}
	return time.Parse(time.RFC3339, v.CreatedAt)
}

// MemoryDoc は記憶 1 件と版の履歴（新しい順）。
type MemoryDoc struct {
	Memory   MemoryItem          `json:"memory"`
	Versions []MemoryVersionInfo `json:"versions"`
}

// NewMemoryAccess は e のサーバとプロジェクトを指す口を作る。timeout が 0 なら LOOPTRACK_TIMEOUT か既定の待ち時間を使う。
func NewMemoryAccess(e env.Env, root string, lang i18n.Lang, timeout time.Duration) (*MemoryAccess, error) {
	c := &Ctx{Env: e, Root: root, Lang: lang}
	var cl *api.Client
	var err error
	if timeout > 0 {
		cl, err = api.NewWithTimeout(e, timeout)
	} else {
		cl, err = api.New(e)
	}
	if err != nil {
		return nil, err
	}
	if cl.BaseURL == "" {
		return nil, i18n.Errorf("cli.err.memory.no_server", "url_env", env.Name(env.APIURL))
	}
	c.useServerLang(cl)
	c.client = cl
	return &MemoryAccess{ctx: c, cl: cl}, nil
}

// MemoryList は一覧の応答。
type MemoryList struct {
	Items []MemoryItem
	// CanWrite はプロジェクトの範囲を書けるか。欄の無い古いサーバでは書けるものとして扱う（書いた時点でサーバが拒む）
	CanWrite bool
	// User は要求した利用者の login（古いサーバでは空）
	User string
}

// List はプロジェクトの記憶と自分の personal の一覧を返す。
func (m *MemoryAccess) List() (*MemoryList, error) {
	path, err := m.ctx.ProjectPath("/memory")
	if err != nil {
		return nil, err
	}
	var body struct {
		Memories []MemoryItem `json:"memories"`
		CanWrite *bool        `json:"can_write"`
		User     string       `json:"user"`
	}
	if _, err := memDo(m.cl, api.Request{Method: http.MethodGet, Path: path}, &body); err != nil {
		return nil, err
	}
	return &MemoryList{Items: body.Memories, CanWrite: body.CanWrite == nil || *body.CanWrite, User: body.User}, nil
}

// SpoolDir はこのプロジェクトの引き継ぎの退避の置き場（決められなければ ""）。
func (m *MemoryAccess) SpoolDir() string {
	slug, err := m.ctx.Project()
	if err != nil {
		return ""
	}
	return usagesnap.HandoffSpoolDir(usagesnap.StateDir(m.ctx.Env), m.cl.BaseURL, slug)
}

// WithContext は以後の要求を ctx で打ち切る（nil で解く）。
func (m *MemoryAccess) WithContext(ctx context.Context) { m.cl.Context = ctx }

// AsSession は以後の書き込みの書き手のセッションを session にする（退避した本文を、書いたセッションの名前で送る）。
// 戻り値は前の値。
func (m *MemoryAccess) AsSession(session string) string {
	prev := m.cl.Mark.Session
	m.cl.Mark.Session = session
	return prev
}

func (m *MemoryAccess) handoffPath(suffix string) (string, error) {
	return m.ctx.ProjectPath("/memory/handoff/" + memoryDefaultHandoff + suffix)
}

// Handoff はプロジェクトの引き継ぎ（既定の名前）と版の履歴を返す。無ければ IsMemoryMissing の誤り。
func (m *MemoryAccess) Handoff() (*MemoryDoc, error) {
	path, err := m.handoffPath("")
	if err != nil {
		return nil, err
	}
	var doc MemoryDoc
	if _, err := memDo(m.cl, api.Request{Method: http.MethodGet, Path: path}, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// AppendHandoff は引き継ぎの末尾に節を足し、書いた後の版を返す。key は冪等の鍵で、同じ鍵の再送は版を増やさない。
// writtenAt は本文を作った時刻（RFC 3339。空なら送らない）。
func (m *MemoryAccess) AppendHandoff(text, key, writtenAt string) (int64, error) {
	path, err := m.handoffPath("/append")
	if err != nil {
		return 0, err
	}
	req := api.Request{Method: http.MethodPost, Path: path}
	if key != "" {
		req.Headers = map[string]string{"Idempotency-Key": key}
	}
	return m.write(req, text, writtenAt)
}

// ReplaceHandoff は読んだ版を渡して引き継ぎの全体を置き換える。版が違えば IsMemoryConflict の誤りで、何も書かれない。
func (m *MemoryAccess) ReplaceHandoff(text string, version int64, writtenAt string) (int64, error) {
	path, err := m.handoffPath("")
	if err != nil {
		return 0, err
	}
	return m.write(api.Request{Method: http.MethodPut, Path: path, Headers: map[string]string{"If-Match": `"` + itoa64(version) + `"`}}, text, writtenAt)
}

func (m *MemoryAccess) write(req api.Request, text, writtenAt string) (int64, error) {
	body := jsonorder.NewObject().Set("content", text).Set("machine_label", m.ctx.memLabel())
	if _, err := time.Parse(time.RFC3339, writtenAt); err == nil {
		body.Set("written_at", writtenAt)
	}
	req.Body = body
	var res memResult
	if _, err := memDo(m.cl, req, &res); err != nil {
		return 0, err
	}
	return res.Memory.Version, nil
}

// IsMemoryMissing は、指した記憶がサーバに無いときの誤りか（消した印のものも含む）。
func IsMemoryMissing(err error) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "not_found"
}

// IsMemoryUnreachable は、サーバに届かなかったときの誤りか（接続の失敗・時間切れ・5xx）。4xx はサーバが答えたので含めない。
func IsMemoryUnreachable(err error) bool {
	var ce *api.ConnError
	var ae *api.Error
	return errors.As(err, &ce) || (errors.As(err, &ae) && ae.Status >= 500)
}

// MemoryServerStatus は、サーバが 5xx を返したときの状態コードを返す（それ以外は 0）。
func MemoryServerStatus(err error) int {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status >= 500 {
		return ae.Status
	}
	return 0
}

// IsMemoryConflict は、読んだ版が今の版と違うときの誤りか。
func IsMemoryConflict(err error) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Status == http.StatusConflict
}

// memoryFileSummary は記憶のファイルの要約の行（`> 要約: …`）。空白は全角も含む。
var memoryFileSummary = hookcmd.CompatRe(`^>\s*要約:\s*(.*)$`)

// memoryLines は本文を行に分ける（CRLF と CR も行の区切りとして読む）。
func memoryLines(text string) []string {
	return strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
}

// MemoryFileSummary は記憶のファイルの最初の要約の行を探し、要約と行の番号（0 始まり。memoryLines の行）を返す。無ければ -1。
// 要約が空の行も見つけたものとして返す（読む側は要約なしとして扱う）。
func MemoryFileSummary(text string) (string, int) {
	for i, line := range memoryLines(text) {
		if m := memoryFileSummary.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1]), i
		}
	}
	return "", -1
}
