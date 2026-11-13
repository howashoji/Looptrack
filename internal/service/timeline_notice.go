package service

import (
	"context"
	"strings"

	"github.com/howashoji/looptrack/internal/store"
)

// タイムライン（DESIGN.md §9-9）の summary の件数と、着手のときの待機の知らせ。どちらも読み手のセッションに効くものだけを数え、
// REST・MCP・CLI の結果に同じものが出るように判定はここに置く（経路の側は文面を組むだけ）。

// TimelineCounts は summary に載せる件数（読み手のセッションに効く有効な書き込み・そのうち未了解・そのうち待機）。
type TimelineCounts struct {
	Active  int
	Unacked int
	Holds   int
}

// Any は 1 件でもあるか。
func (c TimelineCounts) Any() bool { return c.Active > 0 || c.Unacked > 0 || c.Holds > 0 }

// TimelineSummaryCounts は summary に載せる件数を返す。
func (s *Service) TimelineSummaryCounts(ctx context.Context, a Actor, slug string) (TimelineCounts, error) {
	var out TimelineCounts
	vs, err := s.timelinePostsForReader(ctx, a, slug)
	if err != nil {
		return out, err
	}
	for _, v := range vs {
		if !v.Active() || !v.AppliesToMe {
			continue
		}
		out.Active++
		if v.Unacked() {
			out.Unacked++
		}
		if v.Kind == store.TimelinePostHold {
			out.Holds++
		}
	}
	return out, nil
}

// StartTimelinePostHolds は着手（next と In Progress への変更）の結果に添える、読み手のセッションに効く有効な待機を返す
// （並びは通し番号の順）。待機は着手を拒まない。知らせるだけで、従うかは受け取った AI が決める。
func (s *Service) StartTimelinePostHolds(ctx context.Context, a Actor, slug string) ([]TimelinePostView, error) {
	vs, err := s.timelinePostsForReader(ctx, a, slug)
	if err != nil {
		return nil, err
	}
	out := []TimelinePostView{}
	for _, v := range vs {
		if v.Kind == store.TimelinePostHold && v.Active() && v.AppliesToMe {
			out = append(out, v)
		}
	}
	sortTimelinePosts(out)
	return out, nil
}

func (s *Service) timelinePostsForReader(ctx context.Context, a Actor, slug string) ([]TimelinePostView, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	return s.timelinePostViews(ctx, a, p, 0)
}
