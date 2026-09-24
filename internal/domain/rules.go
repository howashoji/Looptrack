package domain

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// プロジェクト別ルール（DESIGN.md §5）。projects.rules の JSON を解釈して判定する。
// ルールはデータで持ち、コードにプロジェクト固有の分岐を入れない。メッセージは rules の message テンプレートで
// 上書きでき、{id} {status} {hit} {bugs} {count} を置き換える。

// Rules は 1 プロジェクトのルール。nil のルールは無効。
type Rules struct {
	ForbidStatus          *ForbidStatusRule    `json:"forbid_status,omitempty"`
	RequireCommentBefore  *RequireCommentRule  `json:"require_comment_before,omitempty"`
	DoneRequiresKeyword   *KeywordRule         `json:"done_requires_keyword,omitempty"`
	ForbidCheckboxPattern *CheckboxPatternRule `json:"forbid_checkbox_pattern,omitempty"`
	// ZeroBugGate は撤去したゼロバグゲート（2026-09-20）の名残。解釈も判定もしない。
	// ParseRules が DisallowUnknownFields を使うため、DB の projects.rules に古いキーが残っている
	// プロジェクトで全操作が落ちないよう、受け取って捨てるためだけに残す（後方互換）。
	ZeroBugGate *json.RawMessage `json:"zero_bug_gate,omitempty"`
	Usage       *UsageRule       `json:"usage,omitempty"`
	Verify      *VerifyRule      `json:"verify,omitempty"`
	// Acceptance は受け入れ条件の記入の必須化（acceptance.go）。
	Acceptance *AcceptanceRule `json:"acceptance,omitempty"`
}

// VerifyRule は検証コマンドの記録の必須化（DESIGN.md §5-8-4）。RequireOnClose が true なら、
// 「## 検証コマンド」節を持つイシューを Statuses（既定 Done だけ）にするとき、直近の verify が現在の本文に対する
// 全件成功であることを求める（理由付きで上書き可）。経路（AI・人）を問わない。
// Message は 3 つの状態（記録なし・本文が変わった・失敗）で共通の上書き（{id} {status} {command} {state} {failed}）。
type VerifyRule struct {
	RequireOnClose bool     `json:"require_on_close"`
	Statuses       []string `json:"statuses,omitempty"`
	Message        string   `json:"message,omitempty"`
}

// UsageRule はトークン計測（DESIGN.md §5-4）の強さ。RequireOnClose が false（既定）なら警告だけで、
// true なら AI からの操作で Statuses（既定 Done / Canceled）にするとき、その会話のトークン情報が
// そのイシューに 1 件以上あることを求める（理由付きで上書き可）。
// CasePattern は案件ラベルの正規表現。レポートの案件別（by_case）で、区間の帰属先イシューのラベル、無ければ
// 会話のブランチ名から案件を決める。捕捉グループがあれば 1 番目、無ければ一致した部分が案件名。未設定なら全部「案件なし」。
// SendPrompts は人間の指示文の先頭 44 文字（区間の作業名 label）を送ってよいか（既定 false = 送らない）。
// CLI / フックは POST /usage の応答の send_prompts でこの値を知り、利用者の環境変数 LOOPTRACK_USAGE_SEND_PROMPTS=0 でも止められる
// （両方が許すときだけ送る）。サーバは false のプロジェクトに届いた label を保存しない。
type UsageRule struct {
	RequireOnClose bool     `json:"require_on_close"`
	Statuses       []string `json:"statuses,omitempty"`
	Message        string   `json:"message,omitempty"`
	CasePattern    string   `json:"case_pattern,omitempty"`
	SendPrompts    bool     `json:"send_prompts,omitempty"`

	caseRe *regexp.Regexp
}

// ForbidStatusRule は使わない状態への起票・遷移を拒否する。
type ForbidStatusRule struct { // 起票時も同じメッセージ（{id} は空）
	Statuses []string `json:"statuses"`
	Message  string   `json:"message,omitempty"`
}

// RequireCommentRule は、既存コメントが 0 件で同時コメントも無いまま Statuses へ進めるのを拒否する。
type RequireCommentRule struct {
	Statuses      []string `json:"statuses"`
	Message       string   `json:"message,omitempty"`
	CreateMessage string   `json:"create_message,omitempty"` // 起票時（{id} が無い）
}

// KeywordRule は、Statuses（既定 Done）へ進めるとき、コメント節か同時コメントに Keyword を含むことを求める。
type KeywordRule struct {
	Keyword       string   `json:"keyword"`
	Statuses      []string `json:"statuses,omitempty"`
	Message       string   `json:"message,omitempty"`
	CreateMessage string   `json:"create_message,omitempty"` // 起票時（{id} が無い）
}

// CheckboxPatternRule は、チェックボックスの区間に Env・Action・Ask がそろい Exempt が無い記述を拒否する。
type CheckboxPatternRule struct {
	Env     string `json:"env"`
	Action  string `json:"action"`
	Ask     string `json:"ask"`
	Exempt  string `json:"exempt,omitempty"`
	Message string `json:"message,omitempty"`

	env, action, ask, exempt *regexp.Regexp
}

// ParseRules は JSON を解釈して検査する。null・空は nil（ルールなし）。未知のキーは誤りにする
// （綴り間違いでルールが黙って無効になるのを防ぐ）。
func ParseRules(raw []byte) (*Rules, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r Rules
	if err := dec.Decode(&r); err != nil {
		return nil, i18n.Wrapf(err, "domain.rules.err.json")
	}
	checkStatuses := func(name string, list []string, required bool) error {
		if required && len(list) == 0 {
			return i18n.Errorf("domain.rules.err.field_empty", "name", name+".statuses")
		}
		for _, s := range list {
			if !contains(Statuses, s) {
				return i18n.Errorf("domain.rules.err.not_a_status", "name", name+".statuses", "value", strconv.Quote(s), "statuses", strings.Join(Statuses, " / "))
			}
		}
		return nil
	}
	if f := r.ForbidStatus; f != nil {
		if err := checkStatuses("forbid_status", f.Statuses, true); err != nil {
			return nil, err
		}
	}
	if f := r.RequireCommentBefore; f != nil {
		if err := checkStatuses("require_comment_before", f.Statuses, true); err != nil {
			return nil, err
		}
	}
	if f := r.DoneRequiresKeyword; f != nil {
		if strings.TrimSpace(f.Keyword) == "" {
			return nil, i18n.Errorf("domain.rules.err.field_empty", "name", "done_requires_keyword.keyword")
		}
		if len(f.Statuses) == 0 {
			f.Statuses = []string{"Done"}
		}
		if err := checkStatuses("done_requires_keyword", f.Statuses, true); err != nil {
			return nil, err
		}
	}
	if f := r.ForbidCheckboxPattern; f != nil {
		for _, p := range []struct {
			name, src string
			dst       **regexp.Regexp
			required  bool
		}{{"env", f.Env, &f.env, true}, {"action", f.Action, &f.action, true}, {"ask", f.Ask, &f.ask, true}, {"exempt", f.Exempt, &f.exempt, false}} {
			if p.src == "" {
				if p.required {
					return nil, i18n.Errorf("domain.rules.err.field_empty", "name", "forbid_checkbox_pattern."+p.name)
				}
				continue
			}
			re, err := regexp.Compile(p.src)
			if err != nil {
				return nil, i18n.Wrapf(err, "domain.rules.err.bad_regexp", "name", "forbid_checkbox_pattern."+p.name)
			}
			*p.dst = re
		}
	}
	if f := r.Usage; f != nil {
		if len(f.Statuses) == 0 {
			f.Statuses = append([]string(nil), ClosedStatuses...)
		}
		if err := checkStatuses("usage", f.Statuses, true); err != nil {
			return nil, err
		}
		if strings.TrimSpace(f.CasePattern) != "" {
			re, err := regexp.Compile(f.CasePattern)
			if err != nil {
				return nil, i18n.Wrapf(err, "domain.rules.err.bad_regexp", "name", "usage.case_pattern")
			}
			if re.MatchString("") {
				return nil, i18n.Errorf("domain.rules.err.case_pattern_matches_empty", "pattern", f.CasePattern)
			}
			f.caseRe = re
		}
	}
	if f := r.Verify; f != nil {
		if len(f.Statuses) == 0 {
			f.Statuses = []string{"Done"}
		}
		if err := checkStatuses("verify", f.Statuses, true); err != nil {
			return nil, err
		}
	}
	if f := r.Acceptance; f != nil {
		if len(f.Statuses) == 0 { // 既定は Done だけ（Canceled は受け入れ条件を満たさずに終わらせる状態なので見ない）
			f.Statuses = []string{"Done"}
		}
		if err := checkStatuses("acceptance", f.Statuses, true); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// CasePattern は案件ラベルの正規表現（usage.case_pattern）。未設定なら nil。
func (r *Rules) CasePattern() *regexp.Regexp {
	if r == nil || r.Usage == nil {
		return nil
	}
	return r.Usage.caseRe
}

// SendPrompts は指示文の作業名を送ってよいプロジェクトか（usage.send_prompts）。未設定なら false。
func (r *Rules) SendPrompts() bool {
	return r != nil && r.Usage != nil && r.Usage.SendPrompts
}

// Violation はルール違反。
//
// Msg が既定の文面の正本（表示側が Msg.In(lang) で利用者の言語にする）。プロジェクトが message を
// 設定している場合は、その文面が利用者（管理者）の書いたものなので**訳さず**、Msg は空にする。
// Message は判定を呼んだ側が渡した言語（要求の言語）で作る。設定された文面は Message にしか無いので、
// そこに埋める値（{id} の「新しいイシュー」・{state}）もこの言語で揃う。
type Violation struct {
	Rule        string
	Message     string
	Msg         i18n.Msg // 既定の文面の ID（設定された文面のときは ID が空）
	Overridable bool     // 理由付きで上書きできる（usage_required_on_close・verify_required_on_close）
}

func (v *Violation) Error() string { return v.Message }

// violation は違反を作る。tmpl（プロジェクトが設定した文面）があればそれを置換して使い、
// 無ければ既定の文面 m を使う。
//
// lang は表示の言語。domain は言語を決めず、呼ぶ側（service。要求ごとの言語）から受け取る。
func violation(lang i18n.Lang, rule, tmpl string, vars map[string]string, m i18n.Msg) *Violation {
	if tmpl != "" {
		return &Violation{Rule: rule, Message: fill(tmpl, "", vars)}
	}
	return &Violation{Rule: rule, Message: m.In(lang), Msg: m}
}

// 以下の i18n.T(lang, …) は、**プロジェクトが設定した文面**（rules の message）に埋める値。
// 設定された文面そのものは訳さない（管理者が 1 つの言語で書いたもの）が、埋める値は要求の言語で作る
// （既定の文面と同じ言語。設定された文面には ID が無く、表示側で訳し直せないため、ここで決める）。
// ID はここで直接書く（ID を引数で受けるヘルパにすると、訳の抜けを見つける検査が ID を追えない）。

// Transition は状態の変更（起票を含む）の判定材料。
type Transition struct {
	ID         string
	Type       string
	From       string // 起票は空
	To         string
	Creating   bool
	Preamble   string   // コメント節の前置き（キーワードの検索対象。件数には数えない）
	Comments   []string // 既存コメントの本文
	NewComment string
}

// Override は上書きで通した違反。
type Override struct {
	Rule   string
	Reason string
}

func fill(tmpl, def string, vars map[string]string) string {
	if tmpl == "" {
		tmpl = def
	}
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", v)
	}
	return tmpl
}

// CheckTransition は状態変更を判定する。違反は hook と同じ順（使わない状態 → キーワード → コメント 0 件）で最初の 1 件を返す。
// lang は違反の文面の言語（以下の Check* も同じ）。
func (r *Rules) CheckTransition(lang i18n.Lang, t Transition) *Violation {
	if r == nil {
		return nil
	}
	vars := map[string]string{"id": t.ID, "status": t.To}
	creating := t.Creating && t.ID == ""
	if creating {
		vars["id"] = i18n.T(lang, "domain.rules.new_issue")
	}
	if f := r.ForbidStatus; f != nil && contains(f.Statuses, t.To) {
		if creating {
			return violation(lang, "forbid_status", f.Message, vars, i18n.M("domain.rules.forbid_status.create", "status", t.To))
		}
		return violation(lang, "forbid_status", f.Message, vars, i18n.M("domain.rules.forbid_status", "id", t.ID, "status", t.To))
	}
	if f := r.DoneRequiresKeyword; f != nil && contains(f.Statuses, t.To) {
		found := strings.Contains(t.NewComment, f.Keyword) || strings.Contains(t.Preamble, f.Keyword)
		for _, c := range t.Comments {
			found = found || strings.Contains(c, f.Keyword)
		}
		if !found {
			vars["keyword"] = f.Keyword
			if t.Creating {
				return violation(lang, "done_requires_keyword", f.CreateMessage, vars,
					i18n.M("domain.rules.done_requires_keyword.create", "status", t.To, "keyword", f.Keyword))
			}
			return violation(lang, "done_requires_keyword", f.Message, vars,
				i18n.M("domain.rules.done_requires_keyword", "id", vars["id"], "status", t.To, "keyword", f.Keyword))
		}
	}
	if f := r.RequireCommentBefore; f != nil && contains(f.Statuses, t.To) {
		if len(t.Comments) == 0 && strings.TrimSpace(t.NewComment) == "" {
			if t.Creating {
				return violation(lang, "require_comment_before", f.CreateMessage, vars,
					i18n.M("domain.rules.require_comment_before.create", "status", t.To))
			}
			return violation(lang, "require_comment_before", f.Message, vars,
				i18n.M("domain.rules.require_comment_before", "id", vars["id"], "status", t.To))
		}
	}
	return nil
}

// UsageAttachCommand は、今の会話のトークン情報をイシューに手動で付けるコマンド（looptrack issue usage attach）。
func UsageAttachCommand(id string) string {
	return "looptrack issue usage attach " + id
}

// UsageCheck はクローズ時のトークン情報の判定材料。
type UsageCheck struct {
	ID             string
	To             string
	Target         bool // AI からの操作（MCP か、セッション ID 付きの CLI）。人の操作は判定しない
	Attached       bool // その会話のスナップショットがそのイシューに 1 件以上ある
	OverrideReason string
}

// RequiresUsage は、To にするときトークン情報が必須か（usage.require_on_close）。
func (r *Rules) RequiresUsage(to string) bool {
	return r != nil && r.Usage != nil && r.Usage.RequireOnClose && contains(r.Usage.Statuses, to)
}

// CheckUsage はクローズ時のトークン情報の必須化（usage.require_on_close）を判定する。
// 必須でない（既定）ときは何も返さない（警告は応答に載せる。service.UsageNotice）。
func (r *Rules) CheckUsage(lang i18n.Lang, c UsageCheck) (*Override, *Violation) {
	if !r.RequiresUsage(c.To) || !c.Target || c.Attached {
		return nil, nil
	}
	if reason := strings.TrimSpace(c.OverrideReason); reason != "" {
		return &Override{Rule: "usage_required_on_close", Reason: reason}, nil
	}
	vars := map[string]string{"id": c.ID, "status": c.To, "command": UsageAttachCommand(c.ID)}
	v := violation(lang, "usage_required_on_close", r.Usage.Message, vars,
		i18n.M("domain.rules.usage_required_on_close", "id", c.ID, "status", c.To, "command", UsageAttachCommand(c.ID)))
	v.Overridable = true
	return nil, v
}

// 直近の verify の状態（VerifyCheck.State）。
const (
	VerifyNone    = "none"    // 記録なし
	VerifyStale   = "stale"   // 最後の verify の後に本文が変わった
	VerifyFailed  = "failed"  // 最後の verify に失敗がある
	VerifyCurrent = "current" // 現在の本文に対して全件成功
)

// VerifyCheck はクローズ時の検証コマンドの判定材料。
type VerifyCheck struct {
	ID             string
	To             string
	HasCommands    bool   // 本文に検証コマンドが 1 件以上ある
	State          string // VerifyNone / VerifyStale / VerifyFailed / VerifyCurrent
	Failed         int    // VerifyFailed のときの失敗件数
	Creating       bool   // 起票（ID はまだ無い）
	OverrideReason string
}

// RequiresVerify は、To にするとき verify の記録が必須か（verify.require_on_close）。
func (r *Rules) RequiresVerify(to string) bool {
	return r != nil && r.Verify != nil && r.Verify.RequireOnClose && contains(r.Verify.Statuses, to)
}

// CheckVerify は verify.require_on_close を判定する。節の無いイシュー・対象外の状態は何も返さない。
func (r *Rules) CheckVerify(lang i18n.Lang, c VerifyCheck) (*Override, *Violation) {
	if !r.RequiresVerify(c.To) || !c.HasCommands || c.State == VerifyCurrent {
		return nil, nil
	}
	if reason := strings.TrimSpace(c.OverrideReason); reason != "" {
		return &Override{Rule: "verify_required_on_close", Reason: reason}, nil
	}
	vars := map[string]string{"id": c.ID, "status": c.To, "command": VerifyCommand(c.ID), "failed": strconv.Itoa(c.Failed)}
	var m i18n.Msg
	switch {
	case c.Creating:
		vars["id"], vars["command"] = i18n.T(lang, "domain.rules.new_issue"), VerifyCommand("<ID>")
		vars["state"] = i18n.T(lang, "domain.rules.verify.state.none")
		m = i18n.M("domain.rules.verify_required_on_close.create", "status", c.To, "command", VerifyCommand("<ID>"))
	case c.State == VerifyStale:
		vars["state"] = i18n.T(lang, "domain.rules.verify.state.stale")
		m = i18n.M("domain.rules.verify_required_on_close.stale", "id", c.ID, "command", VerifyCommand(c.ID))
	case c.State == VerifyFailed:
		vars["state"] = i18n.T(lang, "domain.rules.verify.state.failed", "failed", c.Failed)
		m = i18n.M("domain.rules.verify_required_on_close.failed", "id", c.ID, "failed", c.Failed, "command", VerifyCommand(c.ID))
	default:
		vars["state"] = i18n.T(lang, "domain.rules.verify.state.none")
		m = i18n.M("domain.rules.verify_required_on_close.none", "id", c.ID, "command", VerifyCommand(c.ID))
	}
	v := violation(lang, "verify_required_on_close", r.Verify.Message, vars, m)
	v.Overridable = true
	return nil, v
}

var checkbox = regexp.MustCompile(`[-*]\s*\[[ xX]\]`)

// checkboxHits は text の中で規則に当たるチェックボックスの区間（前後の空白を除く）を返す。
// 区間は出現位置から次の改行または次のチェックボックスまで（--body は 1 行で渡ることがあるため）。
func (f *CheckboxPatternRule) checkboxHits(text string) []string {
	var out []string
	locs := checkbox.FindAllStringIndex(text, -1)
	for i, loc := range locs {
		end := len(text)
		if nl := strings.IndexByte(text[loc[0]:], '\n'); nl >= 0 {
			end = loc[0] + nl
		}
		if i+1 < len(locs) && locs[i+1][0] < end {
			end = locs[i+1][0]
		}
		seg := text[loc[0]:end]
		if f.exempt != nil && f.exempt.MatchString(seg) {
			continue
		}
		if f.env.MatchString(seg) && f.action.MatchString(seg) && f.ask.MatchString(seg) {
			out = append(out, strings.TrimSpace(seg))
		}
	}
	return out
}

// CheckText は新しく書く文章を判定する。before（変更前の文章）に既にある該当箇所は対象にしない
// （過去の記述を理由に、関係ない編集まで止めないため）。id が空なら起票（まだ ID が無い）。
func (r *Rules) CheckText(lang i18n.Lang, id, before, after string) *Violation {
	if r == nil || r.ForbidCheckboxPattern == nil {
		return nil
	}
	f := r.ForbidCheckboxPattern
	old := map[string]int{}
	for _, h := range f.checkboxHits(before) {
		old[h]++
	}
	for _, h := range f.checkboxHits(after) {
		if old[h] > 0 {
			old[h]--
			continue
		}
		hit := h
		if rs := []rune(hit); len(rs) > 140 {
			hit = string(rs[:140])
		}
		shown := id
		m := i18n.M("domain.rules.forbid_checkbox_pattern", "id", id, "hit", hit)
		if id == "" {
			shown = i18n.T(lang, "domain.rules.new_issue")
			m = i18n.M("domain.rules.forbid_checkbox_pattern.create", "hit", hit)
		}
		return violation(lang, "forbid_checkbox_pattern", f.Message, map[string]string{"id": shown, "hit": hit}, m)
	}
	return nil
}
