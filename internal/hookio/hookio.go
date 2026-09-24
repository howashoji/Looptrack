// Package hookio は、コーディング AI の hook の入出力を AI に依らない形に直す。
//
// クライアント側を Go の実行ファイル 1 つ（looptrack）に統一したとき、hook は `looptrack hook <名前>` のサブコマンドになる。
// 各 hook は AI ごとの入力の違い（項目名・イベント名・ツール名）と出力の違い（JSON のどこに何を置くか・終了コード）を
// 意識せず、共通の Event を読んで共通の Result を返すだけにする。ここはその変換だけを受け持つ（配線や hook の中身は持たない）。
//
//	stdin（AI ごとの JSON）──Parse──▶ Event ──hook の本体──▶ Result ──Render──▶ stdout・stderr・終了コード（AI ごとの形）
//
// どの AI かは配線が `--agent claude-code|codex|copilot` で渡すのが原則（ParseOptions.Agent）。環境変数や入力の形からの
// 推測（DetectAgent）は予備に限る（Copilot は .claude/settings.json の配線も読むので、推測は取り違えやすい）。
//
// 入力の形（2026-09-18 時点。根拠は各 AI の公式文書と手元の調査。Copilot は VS Code の実物で未確認）:
//
//   - Claude Code: snake_case（hook_event_name・session_id・transcript_path・cwd・agent_id・tool_name・tool_input・
//     tool_response・tool_use_id・prompt・stop_hook_active・source・reason）。MCP のツール名は mcp__<サーバ>__<ツール>。
//   - Codex: Claude Code とほぼ同じ形。CLAUDE_PROJECT_DIR は渡らない（ProjectDir は git のルート、無ければ cwd）。
//   - Copilot（.github/hooks/*.json）: 2 つの形がある。
//     (1) イベント名を PascalCase で書いた配線（init はこちらを書く）: Copilot CLI は「VS Code 互換の形式」になり、
//     VS Code と同じく snake_case（hook_event_name・session_id・tool_name・tool_input・transcript_path）。CLI の
//     PostToolUse の結果は tool_result（result_type・text_result_for_llm）。VS Code は tool_response。
//     (2) camelCase のイベント名（sessionStart・userPromptSubmitted・preToolUse・postToolUse・agentStop・subagentStop・
//     sessionEnd）の配線: Copilot CLI だけ。入力は camelCase（sessionId・timestamp・cwd・toolName・toolArgs（JSON の
//     **文字列**）・toolResult（resultType・textResultForLlm）・prompt・transcriptPath・source・reason・initialPrompt）で、
//     **イベント名が入らない**ので配線で `--event` を渡す（ParseOptions.Event）。
//     ツール名も AI ごとに違う（CLI: bash・edit・create・view・<サーバ>-<ツール>。VS Code: run_in_terminal・
//     replace_string_in_file・create_file・read_file・mcp_<サーバ>_<ツール>（VS Code の MCP の形は推論））。Tool.Kind に揃える。
//
// 出力の形（Render）は result.go の冒頭を参照。
//
// fail-open: hook は入力が読めない・panic・時間切れでも AI の操作を止めない（exit 0・何も出さない）。Run と Recover を使う。
package hookio

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Agent は hook を起動したコーディング AI。
type Agent string

const (
	ClaudeCode Agent = "claude-code"
	Codex      Agent = "codex"
	Copilot    Agent = "copilot"
)

// Agents は対応している AI（--agent に渡せる値）。
var Agents = []Agent{ClaudeCode, Codex, Copilot}

// ParseAgent は --agent の値を Agent にする。知らない値は ok = false。
func ParseAgent(s string) (Agent, bool) {
	switch Agent(s) {
	case ClaudeCode, Codex, Copilot:
		return Agent(s), true
	}
	return "", false
}

// Name は AI に依らないイベント名（Claude Code の名前にそろえる）。
type Name string

const (
	SessionStart     Name = "SessionStart"
	UserPromptSubmit Name = "UserPromptSubmit"
	PreToolUse       Name = "PreToolUse"
	PostToolUse      Name = "PostToolUse"
	Stop             Name = "Stop"
	SubagentStop     Name = "SubagentStop" // Claude Code・VS Code・Copilot CLI（subagentStop）。鮮度ガードが見分ける
	SessionEnd       Name = "SessionEnd"
	Other            Name = "Other" // 上のどれでもない（Notification・PreCompact・errorOccurred など）。RawName に元の名前
)

// Shape は入力の JSON の書式（EncodeInput で同じ形に戻すために持つ）。
type Shape string

const (
	Snake Shape = "snake_case" // Claude Code・Codex・Copilot の PascalCase の配線（VS Code 互換の形式）
	Camel Shape = "camelCase"  // Copilot CLI の camelCase の配線（toolArgs が文字列・イベント名なし）
)

// Event は hook の入力を AI に依らない形にしたもの。
type Event struct {
	Agent Agent `json:"agent"`
	// AgentGuessed は --agent が無く、環境変数や入力の形から推測したとき true（予備の経路を通った印）。
	AgentGuessed bool `json:"agent_guessed,omitempty"`
	// Foreign は Claude Code 向けの配線（--agent claude-code）を Claude Code 以外（Copilot など）が起動したとき true。
	// VS Code の Copilot・Copilot CLI は .claude/settings.json の hook も実行する。Run は何もせずに exit 0 で終える。
	Foreign bool `json:"foreign,omitempty"`

	Name    Name   `json:"name"`
	RawName string `json:"raw_name,omitempty"` // 入力（か --event）のイベント名そのまま（agentStop・userPromptSubmitted など）
	Shape   Shape  `json:"shape"`

	SessionID      string `json:"session_id,omitempty"`
	SubagentID     string `json:"subagent_id,omitempty"` // Claude Code の agent_id（サブエージェントの中の呼び出し。session_id は親と同じ）
	TranscriptPath string `json:"transcript_path,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	ProjectDir     string `json:"project_dir,omitempty"` // CLAUDE_PROJECT_DIR → cwd の git のルート → cwd

	Tool *Tool `json:"tool,omitempty"` // PreToolUse・PostToolUse だけ

	Prompt         string `json:"prompt,omitempty"`           // UserPromptSubmit の prompt（Copilot CLI の sessionStart の initialPrompt も）
	StopHookActive bool   `json:"stop_hook_active,omitempty"` // Stop の差し戻しの後の再停止（無限ループ防止に使う）
	Source         string `json:"source,omitempty"`           // SessionStart の source（startup・resume・clear・compact・new）
	Reason         string `json:"reason,omitempty"`           // SessionEnd の reason

	// Raw は入力の JSON そのまま（ここで拾っていない項目を読むため）。比較・出力には使わない。
	Raw map[string]any `json:"-"`
}

// ToolKind はツールの種類（AI ごとのツール名を揃えたもの）。
type ToolKind string

const (
	KindBash  ToolKind = "Bash"  // シェル: Bash（Claude Code・Codex）・bash / powershell（Copilot CLI）・run_in_terminal（VS Code）
	KindEdit  ToolKind = "Edit"  // 既存ファイルの編集
	KindWrite ToolKind = "Write" // ファイルの作成・全体の書き込み
	KindRead  ToolKind = "Read"  // ファイルの読み取り（鮮度ガードが「参照した」を拾う）
	KindMCP   ToolKind = "MCP"   // MCP サーバのツール（Server と Name に分ける）
	KindOther ToolKind = "other"
)

// Tool は PreToolUse・PostToolUse のツール呼び出し。
type Tool struct {
	Kind    ToolKind `json:"kind"`
	RawName string   `json:"raw_name"`         // 入力のツール名そのまま
	Server  string   `json:"server,omitempty"` // MCP のサーバ名（推定。区切りの形が AI で違う。MatchMCP で照合する）
	Name    string   `json:"name"`             // MCP ならツール名だけ、それ以外は RawName と同じ
	UseID   string   `json:"use_id,omitempty"` // tool_use_id（Copilot CLI の toolCallId）
	// Input はツールの入力（Copilot CLI の toolArgs は JSON の文字列なので解いて入れる）。JSON のオブジェクトでなければ nil で InputText に。
	Input     map[string]any `json:"input,omitempty"`
	InputText string         `json:"input_text,omitempty"`
	Response  *Response      `json:"response,omitempty"` // PostToolUse だけ
}

// Response は PostToolUse のツールの結果。
type Response struct {
	Raw    any    `json:"raw"`              // tool_response / tool_result / toolResult そのまま
	Text   string `json:"text,omitempty"`   // 読みやすい本文（文字列・text_result_for_llm・Bash の stdout と stderr。それ以外は JSON）
	Failed bool   `json:"failed,omitempty"` // 失敗（isError / is_error・result_type が success 以外）
}

// Command はシェルのツールのコマンド文字列（無ければ ""）。
func (t *Tool) Command() string {
	if t == nil {
		return ""
	}
	return str(t.Input["command"])
}

// FilePath は編集・作成・読み取りのツールの対象のパス（file_path・notebook_path・filePath・path の順。無ければ apply_patch の
// 本文の最初のファイル。無ければ ""）。
func (t *Tool) FilePath() string {
	if p := t.FilePaths(); len(p) > 0 {
		return p[0]
	}
	return ""
}

// patchFileRe は apply_patch の本文（*** Begin Patch … *** Update File: <パス>）の対象のファイルの行。
var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+?)\s*$`)

// FilePaths は編集・作成・読み取りのツールの対象のパスをすべて返す。file_path などの項目があればその 1 つ。無ければ
// apply_patch の本文（Copilot CLI の GPT 系のモデルの Edit は tool_input が patch の文字列。Codex の apply_patch は input・patch）
// の Update / Add / Delete File の行の順。
func (t *Tool) FilePaths() []string {
	if t == nil {
		return nil
	}
	for _, k := range []string{"file_path", "notebook_path", "filePath", "path"} {
		if s := str(t.Input[k]); s != "" {
			return []string{s}
		}
	}
	text := t.InputText
	if text == "" {
		for _, k := range []string{"patch", "input"} {
			if s := str(t.Input[k]); s != "" {
				text = s
				break
			}
		}
	}
	if !strings.Contains(text, "*** ") {
		return nil
	}
	var out []string
	for _, m := range patchFileRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case json.Number:
		return x.String()
	case float64, bool:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}
