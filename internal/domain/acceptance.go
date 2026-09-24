package domain

import (
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 受け入れ条件の記入の必須化（DESIGN.md §9-1）。
//
// 起票の雛形（NewDocument が i18n の domain.template.acceptance から差し込む）は「## 受け入れ条件」の節に
// 中身の無いチェックボックスを 1 行置く。これを書き換えないまま close できると、「何をもって完了としたか」が
// 後から分からない。そこで verify.require_on_close と同じ形の関門を足す。
//
// 判定は「節の中に中身のある行が 1 行でもあるか」だけを見る（fail-open）。
// 弾くのは、節の中のすべての行が次のいずれかのときに限る:
//   - 空行
//   - 中身の無い箇条書き・チェックボックス（`- [ ]` だけ、全角の空白だけ 等）
//   - 起票の雛形の文面そのもの（日英どちらも。文面は i18n から取るので、対訳表を直せば自動で追随する）
//   - 未記入を表す印（domain.template.empty と、TODO / TBD / N/A）
//
// 短い受け入れ条件（`- [ ] CI が緑`）は弾かない。記入の「質」は判定しない（雛形のままかどうかだけを見る）。
// 節そのものが無いイシューには効かない（verify の関門が節を持つイシューだけに効くのと同じ）。

// AcceptanceRule は受け入れ条件の記入の必須化（DESIGN.md §9-1）。RequireOnClose が true なら、
// 「## 受け入れ条件」節を持つイシューを Statuses（既定 Done だけ）にするとき、節に中身のある行が
// 1 行以上あることを求める（理由付きで上書き可）。経路（REST・MCP・CLI・Web）を問わない。
// Message はプロジェクトが上書きする文面（{id} {status} {command}）。
type AcceptanceRule struct {
	RequireOnClose bool     `json:"require_on_close"`
	Statuses       []string `json:"statuses,omitempty"`
	Message        string   `json:"message,omitempty"`
}

// AcceptanceCheck はクローズ時の受け入れ条件の判定材料。
type AcceptanceCheck struct {
	ID             string
	To             string
	HasSection     bool // 本文に「## 受け入れ条件」節がある
	Filled         bool // その節に中身のある行が 1 行以上ある
	OverrideReason string
}

// AcceptanceEditCommand は受け入れ条件を書くコマンド（CLI）。
func AcceptanceEditCommand(id string) string {
	return "looptrack issue edit " + id
}

// RequiresAcceptance は、To にするとき受け入れ条件の記入が必須か（acceptance.require_on_close）。
func (r *Rules) RequiresAcceptance(to string) bool {
	return r != nil && r.Acceptance != nil && r.Acceptance.RequireOnClose && contains(r.Acceptance.Statuses, to)
}

// CheckAcceptance は acceptance.require_on_close を判定する。節の無いイシュー・記入済み・対象外の状態は何も返さない。
func (r *Rules) CheckAcceptance(lang i18n.Lang, c AcceptanceCheck) (*Override, *Violation) {
	if !r.RequiresAcceptance(c.To) || !c.HasSection || c.Filled {
		return nil, nil
	}
	if reason := strings.TrimSpace(c.OverrideReason); reason != "" {
		return &Override{Rule: "acceptance_required_on_close", Reason: reason}, nil
	}
	vars := map[string]string{"id": c.ID, "status": c.To, "command": AcceptanceEditCommand(c.ID)}
	v := violation(lang, "acceptance_required_on_close", r.Acceptance.Message, vars,
		i18n.M("domain.rules.acceptance_required_on_close", "id", c.ID, "status", c.To, "command", AcceptanceEditCommand(c.ID)))
	v.Overridable = true
	return nil, v
}

// AcceptanceFilled は「## 受け入れ条件」節に中身のある行が 1 行以上あるか。節が無ければ false
// （節の有無は HasAcceptanceSection で別に見る）。
func AcceptanceFilled(body string) bool {
	for _, line := range strings.Split(AcceptanceCriteria(body), "\n") {
		if s := normalizeAcceptanceItem(line); s != "" && !acceptanceEmptyItems[s] {
			return true
		}
	}
	return false
}

var (
	// acceptanceMarker は箇条書きの印（`- ` `* ` `+ ` `1. ` `1) `）。後ろの空白は無くてもよい（`-[ ]` と書く人がいる）。
	acceptanceMarker = regexp.MustCompile(`^(?:[-*+]|\d+[.)])[ \t]*`)
	// acceptanceCheckbox はチェックボックス（中身は空・x・X）。
	acceptanceCheckbox = regexp.MustCompile(`^\[[ \t xX]?\][ \t]*`)
	acceptanceSpaces   = regexp.MustCompile(`[ \t]+`)
)

// acceptanceTrimCut は、前後から削る飾り（強調・引用・空白）。
const acceptanceTrimCut = "*_~`> \t"

// acceptanceBrackets は、前後を囲む括弧の組。入れ子は繰り返し剥がす。
var acceptanceBrackets = [][2]string{{"(", ")"}, {"（", "）"}, {"「", "」"}, {"『", "』"}, {"【", "】"}, {"[", "]"}, {"<", ">"}}

// normalizeAcceptanceItem は 1 行を比較用に均す。
// 箇条書きの印・チェックボックス・飾り・前後の括弧を外し、空白を 1 つに畳み、ASCII を小文字にする。
// 中身が無くなれば空文字（＝空の項目）。
func normalizeAcceptanceItem(line string) string {
	s := strings.ReplaceAll(line, "　", " ") // 全角の空白
	s = strings.Trim(s, acceptanceTrimCut)
	s = acceptanceMarker.ReplaceAllString(s, "")
	s = acceptanceCheckbox.ReplaceAllString(s, "")
	for {
		t := strings.Trim(s, acceptanceTrimCut)
		for _, b := range acceptanceBrackets {
			if len(t) > len(b[0])+len(b[1]) && strings.HasPrefix(t, b[0]) && strings.HasSuffix(t, b[1]) {
				t = t[len(b[0]) : len(t)-len(b[1])]
				break
			}
		}
		if t == s {
			break
		}
		s = t
	}
	s = acceptanceSpaces.ReplaceAllString(strings.TrimSpace(s), " ")
	return strings.ToLower(s)
}

// acceptanceEmptyItems は「書かれていない」と見なす行（normalizeAcceptanceItem を通した形）。
// 雛形の文面と未記入の印は対訳表から取る（文面を直したらここも自動で追随する。写すと片方だけ直されて黙って壊れる）。
var acceptanceEmptyItems = buildAcceptanceEmptyItems()

func buildAcceptanceEmptyItems() map[string]bool {
	m := map[string]bool{}
	add := func(s string) {
		if n := normalizeAcceptanceItem(s); n != "" {
			m[n] = true
		}
	}
	// 対訳表を足したらここも足す（訳の抜けは internal/i18n の検査が見つける）。
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		for _, line := range strings.Split(AcceptanceCriteria(i18n.T(lang, "domain.template.acceptance")), "\n") {
			add(line)
		}
		add(i18n.T(lang, "domain.template.empty"))
	}
	for _, s := range []string{"todo", "tbd", "n/a", "-", "*"} {
		m[s] = true
	}
	return m
}
