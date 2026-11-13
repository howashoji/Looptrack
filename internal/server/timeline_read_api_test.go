package server

import (
	"bytes"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// タイムラインを読む側の経路（timeline_read_api.go・client/cli/timeline.go の unread・now・seen）。規則の本体は service（timeline_read.go）。
// ここでは REST・MCP・CLI がそこへ届くことと、next・set_status の結果に未読と未了解が載ること、古いクライアントの見る本文が変わらないことを確かめる。

type feedBody struct {
	Seq        int64 `json:"head_seq"`
	ReadSeq    int64 `json:"read_seq"`
	NewSession bool  `json:"new_session"`
	Rows       []struct {
		Seq   int64    `json:"seq"`
		Type  string   `json:"type"`
		Text  string   `json:"text"`
		Quote []string `json:"quote"`
	} `json:"rows"`
	UnackedHolds []timelinePostJSON `json:"unacked_holds"`
	Remaining    int                `json:"remaining"`
	AdvanceTo    int64              `json:"advance_to"`
	Now          map[string]any     `json:"now"`
	Text         string             `json:"text"`
}

func TestTimelineReadREST(t *testing.T) {
	e := newEnv(t)
	pr := e.project("tlr")
	ed := timelinePostMember(t, e, pr, "tlr-ed", "editor", "s-ed")
	vi := timelinePostMember(t, e, pr, "tlr-vi", "viewer", "s-vi")
	ed.json(http.StatusCreated, "POST", "/projects/tlr/issues", map[string]any{"title": "読む側の確認"}, nil)
	ed.json(http.StatusCreated, "POST", "/projects/tlr/timeline", map[string]any{"kind": "hold", "to": "project", "body": "待ってください"}, nil)

	// 新しいセッション: 「いま」と直近の行。viewer も読める（役割を問わない）
	var f feedBody
	vi.json(http.StatusOK, "GET", "/projects/tlr/timeline/unread", nil, &f)
	if !f.NewSession || f.Now == nil || len(f.Rows) != 1 || f.Rows[0].Type != store.TimelineEventIssueCreate || len(f.UnackedHolds) != 1 || f.AdvanceTo != f.Seq {
		t.Fatalf("新しいセッションの未読: %+v", f)
	}
	if !strings.Contains(f.Text, "[未了解の待機]") || !strings.Contains(f.Text, "> 読む側の確認") || strings.Contains(f.Rows[0].Text, "読む側の確認") {
		t.Errorf("文面: %q / %q", f.Text, f.Rows[0].Text)
	}
	// 読むだけでは位置は動かない（軽い確認の位置が無いまま）
	var c struct {
		Seq         int64 `json:"seq"`
		ReadSeq     int64 `json:"read_seq"`
		HasPosition bool  `json:"has_position"`
		Behind      bool  `json:"behind"`
	}
	vi.json(http.StatusOK, "GET", "/projects/tlr/timeline/check", nil, &c)
	if c.HasPosition || !c.Behind {
		t.Fatalf("読む前の軽い確認: %+v", c)
	}
	var seen struct {
		Seq      int64 `json:"seq"`
		Advanced bool  `json:"advanced"`
	}
	vi.json(http.StatusOK, "POST", "/projects/tlr/timeline/seen", map[string]any{"seq": f.AdvanceTo}, &seen)
	if seen.Seq != f.AdvanceTo || !seen.Advanced {
		t.Fatalf("位置: %+v", seen)
	}
	vi.json(http.StatusOK, "GET", "/projects/tlr/timeline/check", nil, &c)
	if !c.HasPosition || c.Behind || c.ReadSeq != c.Seq {
		t.Fatalf("読んだ後の軽い確認: %+v", c)
	}
	// 続きのセッション: 前の位置からの未読だけ。「いま」は求めたときだけ
	ed.json(http.StatusCreated, "POST", "/projects/tlr/timeline", map[string]any{"kind": "update", "body": "実装中です"}, nil)
	f = feedBody{}
	vi.json(http.StatusOK, "GET", "/projects/tlr/timeline/unread", nil, &f)
	if f.NewSession || f.Now != nil || len(f.Rows) != 1 || f.Rows[0].Type != "update" || len(f.Rows[0].Quote) != 1 {
		t.Fatalf("続きのセッションの未読: %+v", f)
	}
	f = feedBody{}
	vi.json(http.StatusOK, "GET", "/projects/tlr/timeline/unread?now=1&limit=1", nil, &f)
	if f.Now == nil {
		t.Errorf("求めた「いま」が無い: %+v", f)
	}
	var now map[string]any
	vi.json(http.StatusOK, "GET", "/projects/tlr/timeline/now", nil, &now)
	if now["sessions"] == nil || now["leases"] == nil || now["text"] == nil {
		t.Errorf("いま: %v", now)
	}
	// 0 以下は何もしない（位置は動かない）
	vi.json(http.StatusOK, "POST", "/projects/tlr/timeline/seen", map[string]any{"seq": -1}, &seen)
	if seen.Advanced {
		t.Errorf("負の位置で進んだ: %+v", seen)
	}
	// セッションの無い要求は拒む（対照: 上のセッションのある要求は通る）
	bare := timelinePostMember(t, e, pr, "tlr-bare", "viewer", "")
	delete(bare.header, "X-Looptrack-Session")
	if code, _, b := bare.do("POST", "/projects/tlr/timeline/seen", map[string]any{"seq": 1}); code != http.StatusBadRequest {
		t.Errorf("セッションの無い seen: %d %s", code, b)
	}
}

// next と set_status の結果に未読と未了解が載る。REST は構造化の値（timeline）だけで、text と messages には足さず位置も進めない
// （位置を送らない古いクライアントの見る本文が変わらない）。MCP は本文に足して位置を進める。
func TestTimelineFeedOnNextAndStatus(t *testing.T) {
	e := newEnv(t)
	pr := e.project("tlf")
	ed := timelinePostMember(t, e, pr, "tlf-ed", "editor", "s-ed")
	ed2 := timelinePostMember(t, e, pr, "tlf-ed2", "editor", "s-ed2")
	for i := 1; i <= 4; i++ {
		ed.json(http.StatusCreated, "POST", "/projects/tlf/issues", map[string]any{"title": "課題 " + strconv.Itoa(i), "status": "Todo"}, nil)
	}
	// 対照: 未読も未了解も無いときの REST の本文（自分の起票は自分の未読に出ない）
	type statusRes struct {
		Messages []string       `json:"messages"`
		Timeline map[string]any `json:"timeline"`
	}
	var r0 statusRes
	ed.json(http.StatusOK, "POST", "/issues/TLF-0001/status", map[string]any{"status": "In Progress"}, &r0)
	ed2.json(http.StatusCreated, "POST", "/projects/tlf/timeline", map[string]any{"kind": "request", "to": "project", "body": "テストを回してください"}, nil)
	ed2.json(http.StatusCreated, "POST", "/issues/TLF-0002/comments", map[string]any{"text": "見ました"}, nil)

	var r1 statusRes
	ed.json(http.StatusOK, "POST", "/issues/TLF-0003/status", map[string]any{"status": "In Progress"}, &r1)
	rows, _ := r1.Timeline["rows"].([]any)
	reqs, _ := r1.Timeline["unacked_requests"].([]any)
	if len(rows) == 0 || len(reqs) != 1 {
		t.Fatalf("REST の set_status の timeline: %v", r1.Timeline)
	}
	if strings.Contains(strings.Join(r1.Messages, "\n"), "タイムライン:") || len(r1.Messages) != len(r0.Messages) {
		t.Errorf("REST の messages が変わった: %q / %q", r1.Messages, r0.Messages)
	}
	// In Review への変更には付かない
	var r2 statusRes
	ed.json(http.StatusOK, "POST", "/issues/TLF-0003/status", map[string]any{"status": "In Review"}, &r2)
	if r2.Timeline != nil {
		t.Errorf("In Review の timeline: %v", r2.Timeline)
	}
	var n struct {
		Text     string         `json:"text"`
		Timeline map[string]any `json:"timeline"`
	}
	ed.json(http.StatusOK, "POST", "/projects/tlf/next", map[string]any{}, &n)
	if n.Timeline == nil || strings.Contains(n.Text, "タイムライン:") || strings.Contains(n.Text, "テストを回してください") {
		t.Fatalf("REST の next: %q %v", n.Text, n.Timeline)
	}
	// REST は位置を進めない
	var c struct {
		HasPosition bool `json:"has_position"`
	}
	ed.json(http.StatusOK, "GET", "/projects/tlf/timeline/check", nil, &c)
	if c.HasPosition {
		t.Fatalf("REST の next と set_status が位置を進めた")
	}

	// MCP の set_status と next は本文に足して位置を進める
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "tlf", "X-Looptrack-Session": "s-ed"})
	text, data := m.call("set_status", map[string]any{"id": "TLF-0004", "status": "In Progress"}, false)
	if !strings.HasPrefix(text, "TLF-0004: Todo → In Progress") || !strings.Contains(text, "[未了解の依頼]") || !strings.Contains(text, "> テストを回してください") ||
		!strings.Contains(text, "TLF-0002 にコメントを書きました") || data["timeline"] == nil {
		t.Fatalf("MCP の set_status: %q", text)
	}
	ed.json(http.StatusOK, "GET", "/projects/tlf/timeline/check", nil, &c)
	if !c.HasPosition {
		t.Fatalf("MCP の set_status が位置を進めない")
	}
	// 2 度目は進めた後なので出来事は出ない。了解していない依頼は出続ける
	text, _ = m.call("next", map[string]any{}, false)
	if strings.Contains(text, "コメントを書きました") || !strings.Contains(text, "[未了解の依頼]") {
		t.Fatalf("MCP の next: %q", text)
	}
	// 読むツールは位置を進めず、進めるツールで進む
	ed2.json(http.StatusCreated, "POST", "/issues/TLF-0002/comments", map[string]any{"text": "もう 1 つ"}, nil)
	text, data = m.call("timeline_unread", map[string]any{}, false)
	adv, _ := data["advance_to"].(float64)
	if !strings.Contains(text, "コメントを書きました") || !strings.Contains(text, "timeline_seen") || adv == 0 {
		t.Fatalf("timeline_unread: %q %v", text, data)
	}
	if text2, _ := m.call("timeline_unread", map[string]any{}, false); !strings.Contains(text2, "コメントを書きました") {
		t.Fatalf("読むだけで位置が進んだ: %q", text2)
	}
	m.call("timeline_seen", map[string]any{"seq": int64(adv)}, false)
	if text3, _ := m.call("timeline_unread", map[string]any{}, false); strings.Contains(text3, "コメントを書きました") {
		t.Fatalf("進めた後の timeline_unread: %q", text3)
	}
	if text4, _ := m.call("timeline_now", map[string]any{}, false); !strings.Contains(text4, "[いま]") {
		t.Fatalf("timeline_now: %q", text4)
	}
}

// looptrack timeline unread は出し終えてから位置を進め、--no-seen は進めない。seen と check の 1 行も確かめる。
func TestTimelineReadCLI(t *testing.T) {
	e := newEnv(t)
	pr := e.project("tlc")
	ed := timelinePostMember(t, e, pr, "tlc-ed", "editor", "x")
	ed2 := timelinePostMember(t, e, pr, "tlc-ed2", "editor", "x")
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": "tlc", "LOOPTRACK_TOKEN": ed2.token, "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"),
		"CLAUDE_PROJECT_DIR": t.TempDir(), "LOOPTRACK_MACHINE_LABEL": "box-b", "LOOPTRACK_SESSION_ID": "cli-b"}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := cli.TimelineMain(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
		return code, out.String(), errb.String()
	}
	ed.json(http.StatusCreated, "POST", "/projects/tlc/issues", map[string]any{"title": "CLI で読む"}, nil)
	if code, out, errs := run("unread", "--no-seen"); code != 0 || !strings.Contains(out, "> CLI で読む") || !strings.Contains(out, "起票しました") {
		t.Fatalf("unread --no-seen: %d %q %q", code, out, errs)
	}
	if code, out, _ := run("check"); code != 0 || !regexp.MustCompile(`^seq=\d+ holds= read=- behind=1\n$`).MatchString(out) {
		t.Fatalf("--no-seen の後の check: %q", out)
	}
	if code, out, errs := run("unread"); code != 0 || !strings.Contains(out, "> CLI で読む") {
		t.Fatalf("unread: %d %q %q", code, out, errs)
	}
	if code, out, _ := run("check"); code != 0 || !regexp.MustCompile(`^seq=(\d+) holds= read=(\d+) behind=0\n$`).MatchString(out) {
		t.Fatalf("unread の後の check: %q", out)
	}
	if code, out, _ := run("unread"); code != 0 || !strings.Contains(out, "未読も未了解もありません") {
		t.Fatalf("2 度目の unread: %q", out)
	}
	if code, out, _ := run("seen", "1"); code != 0 || !regexp.MustCompile(`^seq=\d+ advanced=false\n$`).MatchString(out) {
		t.Fatalf("戻す seen: %q", out)
	}
	if code, out, _ := run("now"); code != 0 || !strings.Contains(out, "[いま]") {
		t.Fatalf("now: %q", out)
	}
}

// MCP の set_status が未読の上限を越えたとき、位置を進めた経路の文面（続きは timeline_unread）を出す。対照として上限以内では出ない。
// セッションの無い読み手には、位置を記録できない旨の文面を出す（位置を進めた文面は出さない）。
func TestTimelineFeedRemainingOnMCP(t *testing.T) {
	e := newEnv(t)
	pr := e.project("tlm")
	ed := timelinePostMember(t, e, pr, "tlm-ed", "editor", "s-ed")
	ed2 := timelinePostMember(t, e, pr, "tlm-ed2", "editor", "s-ed2")
	for i := 1; i <= 3; i++ {
		ed.json(http.StatusCreated, "POST", "/projects/tlm/issues", map[string]any{"title": "課題 " + strconv.Itoa(i), "status": "Todo"}, nil)
	}
	const advanced, noSession, readOnly = "位置はここまで進めたので", "セッション ID が無いので読んだ位置を記録できません", "位置を進めてからもう一度読んでください"
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "tlm", "X-Looptrack-Session": "m-ed"})
	// 位置を作る（このセッションの最初の結果）。ほかの利用者のコメント 5 件は上限以内なので残りの文面は出ない（対照）
	m.call("timeline_seen", map[string]any{"seq": 1}, false)
	for i := 0; i < 5; i++ {
		ed2.json(http.StatusCreated, "POST", "/issues/TLM-0001/comments", map[string]any{"text": "c"}, nil)
	}
	_, data := m.call("timeline_unread", map[string]any{}, false)
	adv, _ := data["advance_to"].(float64)
	m.call("timeline_seen", map[string]any{"seq": int64(adv)}, false)
	for i := 0; i < 5; i++ {
		ed2.json(http.StatusCreated, "POST", "/issues/TLM-0001/comments", map[string]any{"text": "c"}, nil)
	}
	if text, _ := m.call("set_status", map[string]any{"id": "TLM-0002", "status": "In Progress"}, false); !strings.Contains(text, "コメントを書きました") ||
		strings.Contains(text, advanced) || strings.Contains(text, readOnly) {
		t.Fatalf("上限以内の set_status: %q", text)
	}
	for i := 0; i < 25; i++ {
		ed2.json(http.StatusCreated, "POST", "/issues/TLM-0001/comments", map[string]any{"text": "c"}, nil)
	}
	if text, _ := m.call("set_status", map[string]any{"id": "TLM-0002", "status": "Done"}, false); !strings.Contains(text, "ほかに 5 行が未読のまま残っています。"+advanced) ||
		strings.Contains(text, readOnly) {
		t.Fatalf("上限を越えた set_status: %q", text)
	}
	// セッションの無い読み手は位置を持たないので常に新しいセッションとして読み、古い分は件数だけになる（残りの文面は出ない）。
	// 位置を進めた経路の文面も出さない
	bare := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "tlm"})
	text, data := bare.call("set_status", map[string]any{"id": "TLM-0003", "status": "In Progress"}, false)
	tl, _ := data["timeline"].(map[string]any)
	if tl == nil || tl["new_session"] != true || strings.Contains(text, advanced) {
		t.Fatalf("セッションの無い set_status: %q %v", text, tl)
	}
	// 残りの文面は経路で使い分ける（セッションの無い経路は位置を記録できない旨。上の読みでは残りが出ないので文面の組み立てを直接確かめる）
	feed := &service.TimelineFeed{ProjectSlug: "tlm", Rows: []service.TimelineRow{{Seq: 1, Text: "x"}}, Remaining: 3}
	for mode, want := range map[feedMode]string{feedReadOnly: readOnly, feedAdvanced: advanced, feedNoSession: noSession} {
		got := timelineFeedText(i18n.JA, feed, time.UTC, false, mode)
		if !strings.Contains(got, "ほかに 3 行が未読のまま残っています。"+want) {
			t.Errorf("mode %d の残りの文面: %q", mode, got)
		}
		for _, other := range []string{readOnly, advanced, noSession} {
			if other != want && strings.Contains(got, other) {
				t.Errorf("mode %d に別の経路の文面 %q が出た", mode, other)
			}
		}
	}
}
