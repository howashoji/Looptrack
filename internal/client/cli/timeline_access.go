package cli

// hook がタイムライン（DESIGN.md §9-9「確認と投稿の場面」）を読む口。要求と応答の形だけを扱い、未読・読む窓・位置の規則はサーバの service にある。
// 本文の組み立てもサーバ（unread の text）に任せる。hook が自分で組むのは、書き込みの前に出す待機と依頼の見出しだけで、
// その形は `looptrack timeline list` と同じ（timelinePostHeading・timelinePostQuote）。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TimelinePost は未了解・未達の投稿 1 件（hook が見出しと受け取りの記録に使う欄）。
type TimelinePost = timelinePostItem

// TimelineFeed は GET …/timeline/unread の応答（hook が使う欄）。
type TimelineFeed struct {
	Text            string         `json:"text"`
	HeadSeq         int64          `json:"head_seq"`
	ReadSeq         int64          `json:"read_seq"`
	HasPosition     bool           `json:"has_position"`
	NewSession      bool           `json:"new_session"`
	AdvanceTo       int64          `json:"advance_to"`
	Remaining       int            `json:"remaining"`
	UnackedHolds    []TimelinePost `json:"unacked_holds"`
	UnackedRequests []TimelinePost `json:"unacked_requests"`
	Undelivered     []TimelinePost `json:"undelivered"`
	Rows            []struct {
		Seq int64 `json:"seq"`
	} `json:"rows"`
	Timezone string `json:"timezone"`
}

// TimelineCheck は GET …/timeline/check の応答。
type TimelineCheck struct {
	Seq         int64   `json:"seq"`
	Holds       []int64 `json:"holds"`
	ReadSeq     int64   `json:"read_seq"`
	HasPosition bool    `json:"has_position"`
	Behind      bool    `json:"behind"`
}

// errTimelineUnsupported はタイムラインの経路を持たない古いサーバ（404 unknown_api）。
var errTimelineUnsupported = errors.New("timeline unsupported")

// TimelineUnsupported は、サーバがタイムラインの経路を持たない（古いサーバ）ことを示す誤りか。
func TimelineUnsupported(err error) bool { return errors.Is(err, errTimelineUnsupported) }

// Unreachable は、サーバに届かなかった（接続できない・時間切れ）ことを示す誤りか。サーバが応答した誤り（権限など）は偽。
func Unreachable(err error) bool {
	var ce *api.ConnError
	return errors.As(err, &ce)
}

func (m *MemoryAccess) timelineDo(req api.Request, v any) error {
	req.Raw = true
	res, err := m.cl.Do(req)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "unknown_api" {
			return errTimelineUnsupported
		}
		return err
	}
	if v != nil {
		if err := json.Unmarshal(res.Raw, v); err != nil {
			return i18n.Errorf("cli.err.timeline.bad_response", "reason", err)
		}
	}
	return nil
}

// TimelineUnread はこのセッションの未読と未了解を limit 行まで読む（位置は動かさない）。文面は hook 向け（残りは次の場面で届くと書く）。
func (m *MemoryAccess) TimelineUnread(limit int) (*TimelineFeed, error) {
	path, err := m.ctx.timelinePostPath("/unread?hook=1&limit=" + strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	var f TimelineFeed
	if err := m.timelineDo(api.Request{Method: http.MethodGet, Path: path}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// TimelineCheck は軽い確認（最新の通し番号・このセッションに効く有効な待機の ID・読んだ位置）。
func (m *MemoryAccess) TimelineCheck() (*TimelineCheck, error) {
	path, err := m.ctx.timelinePostPath("/check")
	if err != nil {
		return nil, err
	}
	var c TimelineCheck
	if err := m.timelineDo(api.Request{Method: http.MethodGet, Path: path}, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// TimelineSeen はこのセッションの読んだ位置を seq まで進める。セッションの無い読み手は位置を持たないので何もしない。
func (m *MemoryAccess) TimelineSeen(seq int64) error {
	if m.cl.Mark.Session == "" || seq <= 0 {
		return nil
	}
	path, err := m.ctx.timelinePostPath("/seen")
	if err != nil {
		return err
	}
	return m.timelineDo(api.Request{Method: http.MethodPost, Path: path, Body: jsonorder.NewObject().Set("seq", seq)}, nil)
}

// TimelineReceipts は文脈に入れた投稿の受け取りを記録する（未達の知らせを次から出さない。了解は記録しない）。
func (m *MemoryAccess) TimelineReceipts(posts []TimelinePost) error {
	if len(posts) == 0 {
		return nil
	}
	path, err := m.ctx.timelinePostPath("/receipts")
	if err != nil {
		return err
	}
	items := make([]any, 0, len(posts))
	for _, p := range posts {
		items = append(items, jsonorder.NewObject().Set("id", p.ID).Set("version", p.LastVersion))
	}
	return m.timelineDo(api.Request{Method: http.MethodPost, Path: path, Body: jsonorder.NewObject().Set("items", items)}, nil)
}

// TimelinePostsText は待機と依頼の見出しと引用の本文（`looptrack timeline list` と同じ形）。tz は応答の timezone。
func (m *MemoryAccess) TimelinePostsText(posts []TimelinePost, tz string) string {
	loc := timelinePostLoc(m.ctx.Lang, tz)
	blocks := make([]string, 0, len(posts))
	for _, p := range posts {
		body := p.Body
		head := m.ctx.timelinePostHeading(p, loc)
		if p.State == "withdrawn" {
			head += "\n" + i18n.T(m.ctx.Lang, "cli.timeline.withdrawn_by", "by", orDash(p.WithdrawnBy))
			body = orDash(p.WithdrawReason)
		}
		blocks = append(blocks, head+"\n"+timelinePostQuote(body))
	}
	return strings.Join(blocks, "\n\n")
}
