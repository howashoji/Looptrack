package hookio

import (
	"encoding/json"
	"io"
	"strings"
)

// Result は hook の判断（AI に依らない形）。空の Result は「何もしない」（何も出さずに exit 0）。
//
// 出力の形（Render）:
//
//   - 出力はすべて stdout の JSON 1 行 + 終了コード 0 にそろえる（Claude Code の exit 2 + stderr は ParseOutput で読めるが、書かない）。
//     JSON は AI が差し戻しの理由として読むので、終了コードに頼らず JSON に理由を置く方が AI 間で揃えやすい。
//   - Claude Code・Codex: 差し戻しはトップレベルの decision: "block" と reason。PreToolUse の拒否・確認は
//     hookSpecificOutput.permissionDecision（deny / ask）と permissionDecisionReason。文脈は hookSpecificOutput.additionalContext。
//     利用者への知らせはトップレベルの systemMessage。
//   - Copilot: 上と同じものを作ってから、kit の hook の copilot_shape と同じ写しをする。Copilot CLI はトップレベル
//     （additionalContext・permissionDecision・decision）、VS Code は hookSpecificOutput の中（Stop の decision も）を読むので、
//     additionalContext・permissionDecision(Reason) をトップレベルにも置き、decision があって hookSpecificOutput が無ければ
//     hookSpecificOutput にも decision / reason を置く。
//   - その AI・イベントで差し戻せない（か実物で確かめていない）ときは、差し戻さずに systemMessage で知らせるだけにする
//     （kit の LOOPTRACK_LOOP_NO_BLOCK と同じ考え。capabilities の表）。文脈を入れられないイベントの Context も systemMessage に回す。
type Result struct {
	// Block は差し戻し（UserPromptSubmit・PostToolUse・Stop・SubagentStop の decision: block）の理由。PreToolUse では Deny と同じ扱い。
	Block string `json:"block,omitempty"`
	// Deny は PreToolUse の拒否（permissionDecision: deny）の理由。PreToolUse 以外では Block と同じ扱い。
	Deny string `json:"deny,omitempty"`
	// Ask は PreToolUse で利用者に確認を求める（permissionDecision: ask）理由。kit の pre-tool-scope-guard が使う。
	// 確認を出せない AI（Codex）では systemMessage で知らせるだけにする（止めない）。
	Ask string `json:"ask,omitempty"`
	// Context は AI の文脈に入れる文（additionalContext）。
	Context string `json:"context,omitempty"`
	// SystemMessage は利用者に見せる知らせ（systemMessage）。
	SystemMessage string `json:"system_message,omitempty"`
	// UpdatedInput は PreToolUse でツールの入力を書き換える（hookSpecificOutput.updatedInput）ときの、書き換えた後の
	// 入力そのもの（JSON のオブジェクト 1 つを文字列で持つ。Result を比較できる形のままにするため map にしない）。
	// 実物で確かめた AI・イベント（capabilities の update）でだけ出し、それ以外では捨てる（止めない）。
	UpdatedInput string `json:"updated_input,omitempty"`
}

// IsZero は何もしない Result か。
func (r Result) IsZero() bool { return r == Result{} }

// RenderOptions は Render の条件。
type RenderOptions struct {
	// NoBlock は差し戻し・拒否・確認をせず systemMessage で知らせるだけにする（kit の LOOPTRACK_LOOP_NO_BLOCK=1・manifest の block: false）。
	NoBlock bool
	// TrustUnconfirmed は、実物で確かめていない差し戻し（capabilities の unconfirmed）も行う（実物確認の試験用）。
	TrustUnconfirmed bool
}

// Output は hook のプロセスとしての出力。
type Output struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Write は stdout・stderr に書いて終了コードを返す（書けなくても終了コードは変えない＝ fail-open）。
func (o Output) Write(stdout, stderr io.Writer) int {
	if o.Stdout != "" && stdout != nil {
		_, _ = io.WriteString(stdout, o.Stdout)
	}
	if o.Stderr != "" && stderr != nil {
		_, _ = io.WriteString(stderr, o.Stderr)
	}
	return o.ExitCode
}

// level はその AI・イベントで差し戻し等ができるか。
type level int

const (
	no          level = iota
	unconfirmed       // 文書にはあるが実物で確かめていない → 既定では差し戻さない
	yes
)

// capability は AI・イベントごとにできること。
type capability struct {
	block   level // decision: block（PreToolUse では permissionDecision: deny）
	ask     level // permissionDecision: ask（PreToolUse だけ）
	context bool  // additionalContext を置けるか（置けなければ systemMessage に回す）
	update  bool  // updatedInput でツールの入力を書き換えられるか（PreToolUse だけ。実物で確かめた AI だけ true）
}

// capabilities は AI・イベントごとの差し戻し等の可否（2026-09-18 時点）。実物で確かめたら unconfirmed を yes / no に直す。
//
//   - Claude Code: 公式文書・実運用どおり。PreToolUse の updatedInput（ツールの入力の書き換え）は実物で確かめた
//     （2026-09-21・Claude Code 2.1.278。サブエージェントの起動の prompt を書き換えると、書き換えた文が子に届く）。
//   - Codex: UserPromptSubmit の差し戻しは kit では未使用（session-scope-guard が使っていた束縛は削除した）。PreToolUse の deny は文書にある（kit では未使用）。
//     Stop の差し戻しは実物で未確認（manifest の codex.block: false）。ask は無い（deny だけ）。
//   - Copilot: PreToolUse の deny は CLI・VS Code とも文書にある。ask は VS Code だけ（CLI は deny しか処理しない＝そのまま続く）。
//     UserPromptSubmit は CLI 1.0.86 がトップレベルの additionalContext を <system_reminder> として利用者の指示に付ける（実測）。
//     差し戻しは未確認。PostToolUse の decision は VS Code の文書にある（CLI は未確認）。
//     Stop の decision: block は CLI 1.0.86 で効く（reason を次の利用者のメッセージとして渡して続け、2 回目は stop_hook_active: true）。
//     Stop の systemMessage は CLI が捨てる（会話にも画面にも出ない）ので、知らせるだけの形は使えない。
var capabilities = map[Agent]map[Name]capability{
	ClaudeCode: {
		SessionStart:     {context: true},
		UserPromptSubmit: {block: yes, context: true},
		PreToolUse:       {block: yes, ask: yes, context: true, update: true},
		PostToolUse:      {block: yes, context: true},
		Stop:             {block: yes},
		SubagentStop:     {block: yes},
	},
	Codex: {
		SessionStart:     {context: true},
		UserPromptSubmit: {block: yes, context: true},
		PreToolUse:       {block: unconfirmed},
		PostToolUse:      {block: unconfirmed, context: true},
		Stop:             {block: unconfirmed},
		SubagentStop:     {block: unconfirmed},
	},
	Copilot: {
		SessionStart:     {context: true},
		UserPromptSubmit: {block: no, context: true}, // CLI は additionalContext を利用者の指示に付ける（1.0.86 で実測）
		PreToolUse:       {block: yes, ask: unconfirmed, context: true},
		PostToolUse:      {block: unconfirmed, context: true},
		Stop:             {block: yes},
		SubagentStop:     {block: unconfirmed},
	},
}

// CopilotStopMarker は Copilot の Stop の差し戻しの理由の先頭に付ける印。Copilot CLI は差し戻しの理由を次の
// 利用者のメッセージとして渡し、UserPromptSubmit の hook もそれに対して動く。task-mode・session-scope-guard がこの印で始まる
// 入力を利用者の指示と見なさない（理由の「更新して」で実行モードに切り替えない）ようにする。以前の kit の bash 版も同じ文字列を使った。
const CopilotStopMarker = "[Stop hook の差し戻し]"

// CanUpdateInput は、その AI・イベントでツールの入力を書き換えられる（updatedInput を出せる）と実物で確かめてあるか。
// hook 本体が「書き換えられないなら何もしない」（知らせだけ出して書き換わらない、を避ける）ために使う。
func CanUpdateInput(agent Agent, name Name) bool { return capabilities[agent][name].update }

func (l level) ok(opts RenderOptions) bool {
	if opts.NoBlock {
		return false
	}
	return l == yes || (l == unconfirmed && opts.TrustUnconfirmed)
}

// Normalize は、その AI・イベントで実際に出せる形に直した Result を返す（Render が出すものを ParseOutput で読んだ結果と同じ）。
//
//   - PreToolUse: Block は Deny に寄せる。Deny > Ask の順に 1 つだけ（Deny があれば Ask は捨てる）。
//     UpdatedInput は、書き換えられる AI（capabilities の update）で、止めない（Deny が無い）ときだけ通す。
//   - PreToolUse 以外: Deny は Block に寄せ、Ask は systemMessage に回す。
//   - 差し戻せないとき（capabilities・NoBlock）は理由を systemMessage に回す。文脈を入れられないときの Context も同じ。
//   - systemMessage に回した文は、元の SystemMessage の後ろに空行で区切って足す。
func Normalize(agent Agent, name Name, r Result, opts RenderOptions) Result {
	c := capabilities[agent][name]
	out := Result{}
	msgs := []string{}
	if r.SystemMessage != "" {
		msgs = append(msgs, r.SystemMessage)
	}
	demote := func(s string) {
		if s != "" {
			msgs = append(msgs, s)
		}
	}
	if name == PreToolUse {
		deny := r.Deny
		if deny == "" {
			deny = r.Block
		} else {
			demote(r.Block)
		}
		switch {
		case deny != "" && c.block.ok(opts):
			out.Deny = deny
			// 拒否するなら確認は出さない（理由の文を捨てないよう systemMessage に回さない＝拒否の理由で足りる）
		case deny != "":
			demote(deny)
			if r.Ask != "" && c.ask.ok(opts) {
				out.Ask = r.Ask
			} else {
				demote(r.Ask)
			}
		case r.Ask != "" && c.ask.ok(opts):
			out.Ask = r.Ask
		default:
			demote(r.Ask)
		}
		// 入力の書き換えは、止めないときだけ（拒否するなら書き換える先が無い）
		if out.Deny == "" && c.update {
			out.UpdatedInput = r.UpdatedInput
		}
	} else {
		block := r.Block
		if block == "" {
			block = r.Deny
		} else {
			demote(r.Deny)
		}
		if block != "" && c.block.ok(opts) {
			out.Block = block
			if agent == Copilot && (name == Stop || name == SubagentStop) && !strings.HasPrefix(block, CopilotStopMarker) {
				out.Block = CopilotStopMarker + " " + block
			}
		} else {
			demote(block)
		}
		demote(r.Ask)
	}
	if r.Context != "" {
		if c.context {
			out.Context = r.Context
		} else {
			demote(r.Context)
		}
	}
	out.SystemMessage = strings.Join(msgs, "\n\n")
	return out
}

// wire は出力の JSON。フィールドの並びが出力の順になる。
type wire struct {
	Decision                 string        `json:"decision,omitempty"`
	Reason                   string        `json:"reason,omitempty"`
	AdditionalContext        string        `json:"additionalContext,omitempty"`        // Copilot だけ（CLI が読む場所）
	PermissionDecision       string        `json:"permissionDecision,omitempty"`       // Copilot だけ（CLI が読む場所）
	PermissionDecisionReason string        `json:"permissionDecisionReason,omitempty"` // Copilot だけ（CLI が読む場所）
	SystemMessage            string        `json:"systemMessage,omitempty"`
	HookSpecificOutput       *hookSpecific `json:"hookSpecificOutput,omitempty"`
}

type hookSpecific struct {
	HookEventName            string          `json:"hookEventName"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string          `json:"additionalContext,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"` // Claude Code の PreToolUse だけ（ツールの入力の書き換え）
	Decision                 string          `json:"decision,omitempty"`     // Copilot だけ（VS Code の Stop が読む場所）
	Reason                   string          `json:"reason,omitempty"`       // Copilot だけ
}

// Render は Result を、その AI の hook の出力にする。何もしない Result は何も出さずに exit 0。
func Render(ev Event, r Result, opts RenderOptions) Output {
	n := Normalize(ev.Agent, ev.Name, r, opts)
	if n.IsZero() {
		return Output{}
	}
	eventName := string(ev.Name)
	if ev.Name == Other || ev.Name == "" {
		eventName = ev.RawName
	}
	w := wire{SystemMessage: n.SystemMessage}
	hs := &hookSpecific{HookEventName: eventName}
	if n.Block != "" {
		w.Decision, w.Reason = "block", n.Block
	}
	switch {
	case n.Deny != "":
		hs.PermissionDecision, hs.PermissionDecisionReason = "deny", n.Deny
	case n.Ask != "":
		hs.PermissionDecision, hs.PermissionDecisionReason = "ask", n.Ask
	}
	hs.AdditionalContext = n.Context
	if n.UpdatedInput != "" && json.Valid([]byte(n.UpdatedInput)) {
		hs.UpdatedInput = json.RawMessage(n.UpdatedInput)
	}
	if hs.PermissionDecision != "" || hs.AdditionalContext != "" || hs.UpdatedInput != nil {
		w.HookSpecificOutput = hs
	}
	if ev.Agent == Copilot {
		copilotShape(&w, eventName)
	}
	b, err := marshal(w)
	if err != nil {
		return Output{} // 文字列だけの構造体なので起きないが、起きても止めない
	}
	return Output{Stdout: string(b)}
}

// copilotShape は以前の kit/loop/hooks/*.sh の copilot_shape と同じ写し。
func copilotShape(w *wire, eventName string) {
	if h := w.HookSpecificOutput; h != nil {
		w.AdditionalContext = h.AdditionalContext
		w.PermissionDecision = h.PermissionDecision
		w.PermissionDecisionReason = h.PermissionDecisionReason
		return
	}
	if w.Decision != "" {
		w.HookSpecificOutput = &hookSpecific{HookEventName: eventName, Decision: w.Decision, Reason: w.Reason}
	}
}

// ParseOutput は hook の出力（AI ごとの形）を Result に読む。hook のテストで出力を AI に依らない形で確かめるため
// （以前は 1.0.0 より前の hook と Go の hook の出力を比べた）と、Render の往復の確認に使う。AI の読み方に合わせる:
//
//   - 終了コード 2: stderr が理由の差し戻し（PreToolUse は拒否）。Claude Code の鮮度ガードの check の形。
//   - Copilot の PreToolUse で 0・2 以外: 拒否（CLI は fail closed）。理由は stderr。
//   - それ以外の 0 以外: 何もしない（止めないエラー）。
//   - 終了コード 0: stdout の JSON（トップレベルと hookSpecificOutput のどちらでも読む）。JSON でない stdout は
//     SessionStart・UserPromptSubmit では文脈（Claude Code の約束）、それ以外は捨てる。
//
// 読んだ結果は Normalize を通していない（hook が意図したもの）。
func ParseOutput(agent Agent, name Name, o Output) Result {
	switch {
	case o.ExitCode == 2 || (o.ExitCode != 0 && agent == Copilot && name == PreToolUse):
		reason := strings.TrimSpace(o.Stderr)
		if name == PreToolUse {
			return Result{Deny: reason}
		}
		return Result{Block: reason}
	case o.ExitCode != 0:
		return Result{}
	}
	text := strings.TrimSpace(o.Stdout)
	if text == "" {
		return Result{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil || m == nil {
		if name == SessionStart || name == UserPromptSubmit {
			return Result{Context: text}
		}
		return Result{}
	}
	h, _ := m["hookSpecificOutput"].(map[string]any)
	if h == nil {
		h = map[string]any{}
	}
	pick := func(k string) string {
		if s := str(h[k]); s != "" {
			return s
		}
		return str(m[k])
	}
	r := Result{Context: pick("additionalContext"), SystemMessage: str(m["systemMessage"])}
	if u, ok := h["updatedInput"]; ok && u != nil {
		if b, err := json.Marshal(u); err == nil {
			r.UpdatedInput = string(b)
		}
	}
	reason := pick("permissionDecisionReason")
	switch pick("permissionDecision") {
	case "deny":
		r.Deny = reason
	case "ask":
		r.Ask = reason
	}
	if pick("decision") == "block" {
		if name == PreToolUse { // 以前の Claude Code の PreToolUse の decision: block は拒否
			if r.Deny == "" {
				r.Deny = pick("reason")
			}
		} else {
			r.Block = pick("reason")
		}
	}
	return r
}
