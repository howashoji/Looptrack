package usage

import (
	"regexp"
	"sort"
	"time"
)

// レポート用の集計。区間の消費（Stages の結果）を期間で絞り、イシュー・ラベル・種類・段階・AI・会話ごとにまとめる。
//
// 期間の規則: 区間は「その区間を閉じたスナップショットの at（会話記録の最後の時刻）」が [from, to) に入れば期間に入る。
// 区間の差分は期間の外のスナップショットとの差でもよい（会話の途中から期間が始まっても、期間内の消費だけを数える）。
// レポート対象外の会話の区間は合計にも内訳にも入れず、Excluded* に別に返す。
//
// 案件別（ByCase）: 区間ごとに案件を 1 つだけ決める（合計は Total と一致する）。帰属先のイシューのラベルに
// 案件の正規表現に当たるものがあればそれ（ラベルの並び順で最初）、無ければ区間を閉じたスナップショットのブランチ名、
// どちらも当たらなければ「案件なし」（key ""）。正規表現が未設定のプロジェクトは全部「案件なし」。

// IssueInfo はレポートで区間をまとめるイシューの項目（レポートを作った時点の値）。
type IssueInfo struct {
	ID     string
	Title  string
	Type   string
	Status string
	Labels []string
}

// Group は 1 つの切り口（ラベル・種類・段階・AI）の 1 行。
type Group struct {
	Key         string   `json:"key"`
	Total       Counters `json:"total"`
	TotalTokens int64    `json:"total_tokens"`
	Stages      int      `json:"stages"`
	Issues      int      `json:"issues"`
}

// IssueGroup はイシュー別の 1 行。
type IssueGroup struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
	Total       Counters `json:"total"`
	TotalTokens int64    `json:"total_tokens"`
	Stages      int      `json:"stages"`
}

// ConversationGroup は会話（セッション）別の 1 行。
type ConversationGroup struct {
	ConversationID string    `json:"conversation_id"`
	Client         string    `json:"client"`
	Sessions       []string  `json:"sessions"`
	FirstAt        time.Time `json:"first_at"`
	LastAt         time.Time `json:"last_at"`
	Total          Counters  `json:"total"`
	TotalTokens    int64     `json:"total_tokens"`
	Stages         int       `json:"stages"`
	Unattributed   int64     `json:"unattributed_tokens"`
	Issues         []string  `json:"issues"`
}

// Report は期間の集計。
type Report struct {
	Total                 Counters            `json:"total"`
	TotalTokens           int64               `json:"total_tokens"`
	Stages                int                 `json:"stage_count"`
	Unattributed          Group               `json:"unattributed"`
	Excluded              Counters            `json:"excluded_total"`
	ExcludedTokens        int64               `json:"excluded_tokens"`
	ExcludedStages        int                 `json:"excluded_stage_count"`
	ExcludedConversations []string            `json:"excluded_conversations"`
	Inconsistent          int                 `json:"inconsistent"`
	FirstAt               *time.Time          `json:"first_at"` // 期間に入った区間の最初と最後の at（区間が無ければ null）
	LastAt                *time.Time          `json:"last_at"`
	ByIssue               []IssueGroup        `json:"by_issue"`
	ByLabel               []Group             `json:"by_label"` // 1 つのイシューが複数のラベルを持てば、それぞれに数える（合計は total と一致しない）
	ByType                []Group             `json:"by_type"`
	ByStage               []Group             `json:"by_stage"` // 段階（操作）: create / update / comment / status / stop / session_end / manual / import
	ByClient              []Group             `json:"by_client"`
	ByCase                []Group             `json:"by_case"`      // 案件ラベル別（1 区間は 1 案件。合計は total と一致する。案件なしは key ""）
	CasePattern           string              `json:"case_pattern"` // 案件の正規表現（プロジェクト別ルール usage.case_pattern。未設定は ""）
	Conversations         []ConversationGroup `json:"conversations"`
}

// CaseOf は案件名を決める。labels（帰属先イシューのラベル）を先に、無ければ branch を re で調べる。
// 捕捉グループがあれば 1 番目、無ければ一致した部分。source は "label" / "branch" / ""（案件なし）。
func CaseOf(re *regexp.Regexp, labels []string, branch string) (key, source string) {
	if re == nil {
		return "", ""
	}
	match := func(s string) string {
		m := re.FindStringSubmatch(s)
		if m == nil {
			return ""
		}
		if len(m) > 1 && m[1] != "" {
			return m[1]
		}
		return m[0]
	}
	for _, l := range labels {
		if k := match(l); k != "" {
			return k, "label"
		}
	}
	if k := match(branch); k != "" {
		return k, "branch"
	}
	return "", ""
}

// StageKey は区間の段階（操作）の名前。イシュー操作なら操作、それ以外はきっかけ。
func StageKey(s Snapshot) string {
	if s.Trigger == TriggerIssueOp && s.Op != "" {
		return s.Op
	}
	return s.Trigger
}

// InPeriod は区間が期間 [from, to) に入るか。from がゼロ値なら下限なし。
func InPeriod(st Stage, from, to time.Time) bool {
	at := st.Snapshot.At
	return (from.IsZero() || !at.Before(from)) && at.Before(to)
}

type groupAcc struct {
	g      Group
	issues map[string]bool
}

func (a *groupAcc) add(st Stage, issue string) {
	a.g.Total = a.g.Total.add(st.Delta)
	a.g.Stages++
	if issue != "" {
		if a.issues == nil {
			a.issues = map[string]bool{}
		}
		a.issues[issue] = true
	}
}

type groups map[string]*groupAcc

func (m groups) add(key string, st Stage, issue string) {
	a, ok := m[key]
	if !ok {
		a = &groupAcc{g: Group{Key: key}}
		m[key] = a
	}
	a.add(st, issue)
}

func (m groups) list() []Group {
	out := make([]Group, 0, len(m))
	for _, a := range m {
		g := a.g
		g.TotalTokens, g.Issues = g.Total.Total(), len(a.issues)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// BuildReport は期間に入る区間を集計する。info は帰属先の表示 ID からイシューの項目を引く（見つからなければ ID だけで出す）。
// caseRe は案件の正規表現（nil なら案件別は全部「案件なし」）。
func BuildReport(stages []Stage, from, to time.Time, info func(displayID string) (IssueInfo, bool), caseRe *regexp.Regexp) Report {
	rep := Report{ExcludedConversations: []string{}, ByIssue: []IssueGroup{}, Conversations: []ConversationGroup{}}
	if caseRe != nil {
		rep.CasePattern = caseRe.String()
	}
	byIssue := map[string]*IssueGroup{}
	labels, types, stageKeys, clients, cases := groups{}, groups{}, groups{}, groups{}, groups{}
	convs := map[string]*ConversationGroup{}
	excludedConv := map[string]bool{}
	var unattributed groupAcc

	for _, st := range stages {
		if !InPeriod(st, from, to) {
			continue
		}
		at := st.Snapshot.At
		if st.Inconsist {
			rep.Inconsistent++
		}
		if st.Excluded {
			rep.Excluded = rep.Excluded.add(st.Delta)
			rep.ExcludedStages++
			excludedConv[st.Snapshot.ConversationID] = true
			continue
		}
		if rep.FirstAt == nil || at.Before(*rep.FirstAt) {
			t := at
			rep.FirstAt = &t
		}
		if rep.LastAt == nil || at.After(*rep.LastAt) {
			t := at
			rep.LastAt = &t
		}
		rep.Total = rep.Total.add(st.Delta)
		rep.Stages++
		stageKeys.add(StageKey(st.Snapshot), st, st.DisplayID)
		clients.add(st.Snapshot.Client, st, st.DisplayID)

		c, ok := convs[st.Snapshot.ConversationID]
		if !ok {
			c = &ConversationGroup{ConversationID: st.Snapshot.ConversationID, Client: st.Snapshot.Client,
				Sessions: []string{}, Issues: []string{}, FirstAt: at, LastAt: at}
			convs[st.Snapshot.ConversationID] = c
		}
		c.Total = c.Total.add(st.Delta)
		c.Stages++
		if at.Before(c.FirstAt) {
			c.FirstAt = at
		}
		if at.After(c.LastAt) {
			c.LastAt = at
		}
		if !containsStr(c.Sessions, st.Snapshot.SessionID) {
			c.Sessions = append(c.Sessions, st.Snapshot.SessionID)
		}

		if st.IssueID == 0 {
			unattributed.add(st, "")
			c.Unattributed += st.Delta.Total()
			key, _ := CaseOf(caseRe, nil, st.Snapshot.Branch)
			cases.add(key, st, "")
			continue
		}
		if !containsStr(c.Issues, st.DisplayID) {
			c.Issues = append(c.Issues, st.DisplayID)
		}
		ig, ok := byIssue[st.DisplayID]
		if !ok {
			it, found := info(st.DisplayID)
			if !found {
				it = IssueInfo{ID: st.DisplayID}
			}
			ig = &IssueGroup{ID: st.DisplayID, Title: it.Title, Type: it.Type, Status: it.Status, Labels: append([]string{}, it.Labels...)}
			byIssue[st.DisplayID] = ig
		}
		ig.Total = ig.Total.add(st.Delta)
		ig.Stages++
		types.add(ig.Type, st, ig.ID)
		key, _ := CaseOf(caseRe, ig.Labels, st.Snapshot.Branch)
		cases.add(key, st, ig.ID)
		if len(ig.Labels) == 0 {
			labels.add("", st, ig.ID)
		}
		seen := map[string]bool{}
		for _, l := range ig.Labels {
			if !seen[l] {
				seen[l] = true
				labels.add(l, st, ig.ID)
			}
		}
	}

	rep.TotalTokens = rep.Total.Total()
	rep.ExcludedTokens = rep.Excluded.Total()
	unattributed.g.TotalTokens = unattributed.g.Total.Total()
	rep.Unattributed = unattributed.g
	for id := range excludedConv {
		rep.ExcludedConversations = append(rep.ExcludedConversations, id)
	}
	sort.Strings(rep.ExcludedConversations)
	for _, ig := range byIssue {
		ig.TotalTokens = ig.Total.Total()
		rep.ByIssue = append(rep.ByIssue, *ig)
	}
	sort.Slice(rep.ByIssue, func(i, j int) bool {
		a, b := rep.ByIssue[i], rep.ByIssue[j]
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		return a.ID < b.ID
	})
	rep.ByLabel, rep.ByType, rep.ByStage, rep.ByClient, rep.ByCase = labels.list(), types.list(), stageKeys.list(), clients.list(), cases.list()
	for _, c := range convs {
		c.TotalTokens = c.Total.Total()
		rep.Conversations = append(rep.Conversations, *c)
	}
	sort.Slice(rep.Conversations, func(i, j int) bool {
		a, b := rep.Conversations[i], rep.Conversations[j]
		if !a.FirstAt.Equal(b.FirstAt) {
			return a.FirstAt.Before(b.FirstAt)
		}
		return a.ConversationID < b.ConversationID
	})
	return rep
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
