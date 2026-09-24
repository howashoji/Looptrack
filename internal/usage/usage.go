// Package usage はトークン消費のスナップショットから、区間の消費とイシューへの帰属を計算する。
// 保存してあるのは「会話の累計」だけで、差分と帰属はここで毎回計算する（規則を後から直せるようにするため。
// 設計は docs/server/DESIGN.md §5-4）。DB にも HTTP にも依存しない。
package usage

import (
	"sort"
	"time"
)

// 送信のきっかけ。
const (
	TriggerIssueOp    = "issue_op"
	TriggerStop       = "stop"
	TriggerSessionEnd = "session_end"
	TriggerManual     = "manual"
	TriggerImport     = "import"
)

// Triggers は受け付けるきっかけ、Ops は issue_op のときの操作（issue_events.kind と同じ語）。
var (
	Triggers = []string{TriggerIssueOp, TriggerStop, TriggerSessionEnd, TriggerManual, TriggerImport}
	Ops      = []string{"create", "update", "comment", "status", "verify"}
)

// Tokens は 4 種のトークン数。
type Tokens struct {
	Input       int64 `json:"input"`
	CacheCreate int64 `json:"cache_create"`
	CacheRead   int64 `json:"cache_read"`
	Output      int64 `json:"output"`
}

// Total は 4 種の合計。
func (t Tokens) Total() int64 { return t.Input + t.CacheCreate + t.CacheRead + t.Output }

func (t Tokens) add(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.CacheCreate + o.CacheCreate, t.CacheRead + o.CacheRead, t.Output + o.Output}
}

// Counters は本体・サブエージェントの累計と応答数。
type Counters struct {
	Main         Tokens `json:"main"`
	Sub          Tokens `json:"sub"`
	Responses    int64  `json:"responses"`
	SubResponses int64  `json:"sub_responses"`
}

// Total は本体とサブの合計トークン。
func (c Counters) Total() int64 { return c.Main.Total() + c.Sub.Total() }

func (c Counters) add(o Counters) Counters {
	return Counters{c.Main.add(o.Main), c.Sub.add(o.Sub), c.Responses + o.Responses, c.SubResponses + o.SubResponses}
}

// Snapshot は保存してある 1 行（計算に使う列だけ）。
type Snapshot struct {
	ID             int64
	Client         string
	SessionID      string
	ConversationID string
	Trigger        string
	IssueID        int64  // issue_op のときの対象。それ以外は 0
	IssueDisplayID string // 表示用
	Op             string
	IssueStatus    string // 受け取った時点のイシューの状態
	Via            string
	At             time.Time
	Counters       Counters
	Excluded       bool
	Branch         string // 送った時点の git ブランチ（案件ラベルの材料）。無ければ空
}

// Stage は 1 区間（前のスナップショットからこのスナップショットまで）の消費。
type Stage struct {
	Snapshot   Snapshot
	Delta      Counters
	IssueID    int64  // 帰属先。0 は未帰属
	DisplayID  string // 帰属先の表示 ID
	Excluded   bool   // レポート対象外の会話
	Inconsist  bool   // 累計が巻き戻っていた（負の差を 0 にした）
	Cumulative Counters
}

// Stages はスナップショットを会話ごとに並べ、区間の消費と帰属先を返す。
//
//  1. 会話 ID ごとに、累計の合計（同値なら時刻・ID）の昇順に並べる。
//  2. 隣り合う 2 件の差が区間の消費。会話の最初の 1 件は 0 からの差。負になった項目は 0 にして Inconsist を立てる。
//  3. 区間は、その区間を閉じたスナップショットのイシューに帰属させる（起票の前の調査は起票したイシューへ。
//     手動の付与 manual にイシューが付いていれば同じ）。
//  4. イシューの無いスナップショット（stop / session_end / manual）で閉じた区間は、その会話で直前に操作した
//     イシューが未クローズならそこへ、でなければ未帰属。
//
// closed はクローズ扱いの状態（Done / Canceled）。返す順序は会話 ID → 上の並び順。
func Stages(snaps []Snapshot, closed []string) []Stage {
	isClosed := map[string]bool{}
	for _, s := range closed {
		isClosed[s] = true
	}
	byConv := map[string][]Snapshot{}
	var convs []string
	for _, s := range snaps {
		if _, ok := byConv[s.ConversationID]; !ok {
			convs = append(convs, s.ConversationID)
		}
		byConv[s.ConversationID] = append(byConv[s.ConversationID], s)
	}
	sort.Strings(convs)

	var out []Stage
	for _, conv := range convs {
		list := byConv[conv]
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if ta, tb := a.Counters.Total(), b.Counters.Total(); ta != tb {
				return ta < tb
			}
			if !a.At.Equal(b.At) {
				return a.At.Before(b.At)
			}
			return a.ID < b.ID
		})
		excluded := false
		for _, s := range list {
			excluded = excluded || s.Excluded
		}
		var prev Counters
		var lastIssue int64
		var lastDisplay, lastStatus string
		for _, s := range list {
			delta, bad := diff(s.Counters, prev)
			st := Stage{Snapshot: s, Delta: delta, Excluded: excluded, Inconsist: bad, Cumulative: s.Counters}
			if s.IssueID != 0 {
				st.IssueID, st.DisplayID = s.IssueID, s.IssueDisplayID
				lastIssue, lastDisplay, lastStatus = s.IssueID, s.IssueDisplayID, s.IssueStatus
			} else if lastIssue != 0 && !isClosed[lastStatus] {
				st.IssueID, st.DisplayID = lastIssue, lastDisplay
			}
			out = append(out, st)
			prev = maxCounters(prev, s.Counters)
		}
	}
	return out
}

// SortByTime は区間を表示用に時刻順（同時刻は スナップショットの ID 順）へ並べ替える。
// Stages の返す順（会話 ID → 累計順）は差分の計算のためで、別の会話の新しい段階が先頭に紛れる。
// 差分と帰属は Stages で決まっているので、並べ替えても値は変わらない。
func SortByTime(stages []Stage) {
	sort.SliceStable(stages, func(i, j int) bool {
		a, b := stages[i].Snapshot, stages[j].Snapshot
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.ID < b.ID
	})
}

// Sum は区間の消費を合計する。
func Sum(stages []Stage) Counters {
	var total Counters
	for _, s := range stages {
		total = total.add(s.Delta)
	}
	return total
}

func diff(cur, prev Counters) (Counters, bool) {
	bad := false
	sub := func(a, b int64) int64 {
		if a < b {
			bad = true
			return 0
		}
		return a - b
	}
	tok := func(a, b Tokens) Tokens {
		return Tokens{sub(a.Input, b.Input), sub(a.CacheCreate, b.CacheCreate), sub(a.CacheRead, b.CacheRead), sub(a.Output, b.Output)}
	}
	d := Counters{Main: tok(cur.Main, prev.Main), Sub: tok(cur.Sub, prev.Sub),
		Responses: sub(cur.Responses, prev.Responses), SubResponses: sub(cur.SubResponses, prev.SubResponses)}
	return d, bad
}

// maxCounters は項目ごとの大きい方（巻き戻った 1 件の後も、次の差を正しく取るため）。
func maxCounters(a, b Counters) Counters {
	m := func(x, y int64) int64 {
		if x > y {
			return x
		}
		return y
	}
	tok := func(x, y Tokens) Tokens {
		return Tokens{m(x.Input, y.Input), m(x.CacheCreate, y.CacheCreate), m(x.CacheRead, y.CacheRead), m(x.Output, y.Output)}
	}
	return Counters{tok(a.Main, b.Main), tok(a.Sub, b.Sub), m(a.Responses, b.Responses), m(a.SubResponses, b.SubResponses)}
}
