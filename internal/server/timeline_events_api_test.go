package server

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// タイムラインの自動の出来事の経路（在席の開始の印・終了・コミット・近況の投稿）。規則の本体は service（timeline_events.go）にあり、
// ここでは REST の形がそこへ届くことと、印を送らない古いクライアントの在席が出来事を書かないことを確かめる。
func TestTimelineEventsREST(t *testing.T) {
	e := newEnv(t)
	pr := e.project("tev")
	ed := timelinePostMember(t, e, pr, "tev-ed", "editor", "s-ed")
	vi := timelinePostMember(t, e, pr, "tev-vi", "viewer", "s-vi")
	kinds := func() []string {
		t.Helper()
		evs, err := store.TimelineEventsAfter(context.Background(), e.db, pr.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, ev := range evs {
			out = append(out, ev.Kind)
		}
		return out
	}
	presence := map[string]any{"agent": "claude-code", "machine_label": "box-a", "branch": "main"}
	// 古いクライアント（start を送らない）は在席だけを書き、出来事を書かない
	ed.json(http.StatusOK, "POST", "/projects/tev/presence", presence, nil)
	if got := kinds(); len(got) != 0 {
		t.Fatalf("印の無い在席で出来事: %v", got)
	}
	presence["start"] = true
	ed.json(http.StatusOK, "POST", "/projects/tev/presence", presence, nil)
	commits := map[string]any{"branch": "main", "commits": []map[string]any{{"sha": "abc1234", "subject": "直す"}}}
	var res struct {
		Recorded int `json:"recorded"`
		Withheld int `json:"withheld"`
	}
	ed.json(http.StatusOK, "POST", "/projects/tev/timeline/commits", commits, &res)
	if res.Recorded != 1 || res.Withheld != 0 {
		t.Errorf("コミットの応答: %+v", res)
	}
	if code, _, b := vi.do("POST", "/projects/tev/timeline/commits", commits); code != http.StatusForbidden || errCode(b) != "forbidden" {
		t.Errorf("viewer のコミット: %d %s", code, b)
	}
	ed.json(http.StatusCreated, "POST", "/projects/tev/timeline", map[string]any{"kind": "update", "body": "実装中"}, nil)
	ed.json(http.StatusOK, "DELETE", "/projects/tev/presence", nil, nil)
	if got, want := kinds(), []string{store.TimelineEventSessionStart, store.TimelineEventCommit, store.TimelineEventSessionEnd}; !slices.Equal(got, want) {
		t.Errorf("出来事: %v（want %v）", got, want)
	}
}
