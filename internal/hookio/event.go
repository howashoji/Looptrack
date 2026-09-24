package hookio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ParseOptions は Parse の条件（配線の引数と環境）。
type ParseOptions struct {
	// Agent は配線の --agent。空なら DetectAgent で推測する（予備。推測したら Event.AgentGuessed）。
	Agent Agent
	// Event は配線の --event。入力に hook_event_name（hookEventName）があればそちらを優先する。
	// Copilot CLI の camelCase の入力にはイベント名が入らないので、その配線では必ず渡す。
	Event string
	// Getenv は環境変数（nil なら os.Getenv）。
	Getenv Getenv
	// GitRoot は dir の git のルート（nil なら GitRoot。テストで差し替える）。
	GitRoot func(dir string) string
	// Getwd は入力に cwd が無いときの作業ディレクトリ（nil なら os.Getwd）。
	Getwd func() string
}

func (o ParseOptions) getenv(k string) string {
	if o.Getenv == nil {
		return os.Getenv(k)
	}
	return o.Getenv(k)
}

// ErrNotObject は入力が JSON のオブジェクトでないとき。
var ErrNotObject = errors.New("hookio: hook の入力が JSON のオブジェクトではありません")

// イベント名の対応（入力の名前 → 共通の名前）。PascalCase は Claude Code・Codex・Copilot の PascalCase の配線・VS Code、
// camelCase は Copilot CLI（userPromptSubmitted・agentStop が Claude Code と違う）。
var eventNames = map[string]Name{
	"SessionStart": SessionStart, "sessionStart": SessionStart,
	"UserPromptSubmit": UserPromptSubmit, "userPromptSubmitted": UserPromptSubmit, "userPromptSubmit": UserPromptSubmit,
	"PreToolUse": PreToolUse, "preToolUse": PreToolUse,
	"PostToolUse": PostToolUse, "postToolUse": PostToolUse,
	"Stop": Stop, "agentStop": Stop, "stop": Stop,
	"SubagentStop": SubagentStop, "subagentStop": SubagentStop,
	"SessionEnd": SessionEnd, "sessionEnd": SessionEnd,
}

// NameOf は入力のイベント名を共通の名前にする（知らない名前は Other）。
func NameOf(raw string) Name {
	if n, ok := eventNames[raw]; ok {
		return n
	}
	return Other
}

// Parse は hook の stdin を Event にする。空の入力は {} とみなす（Stop などで何も渡さない配線がある）。
// JSON として読めない・オブジェクトでないときは error（呼び出し側は何もせず exit 0 にする＝ Run が行う）。
func Parse(input []byte, opts ParseOptions) (Event, error) {
	raw := map[string]any{}
	if len(bytes.TrimSpace(input)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(input))
		var v any
		if err := dec.Decode(&v); err != nil {
			return Event{}, fmt.Errorf("hookio: hook の入力を JSON として読めません: %w", err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return Event{}, ErrNotObject
		}
		raw = m
	}
	return FromMap(raw, opts), nil
}

// FromMap は JSON を解いた hook の入力を Event にする。
func FromMap(raw map[string]any, opts ParseOptions) Event {
	ev := Event{Raw: raw, Agent: opts.Agent, Shape: Snake}
	if ev.Agent == "" {
		if a, ok := DetectAgent(opts.getenv, raw); ok {
			ev.Agent = a
		} else {
			ev.Agent = ClaudeCode // 分からなければ最も多い形（snake_case は Codex・Copilot の PascalCase の配線とも共通）
		}
		ev.AgentGuessed = true
	}
	if ev.Agent == ClaudeCode && ForeignHost(opts.getenv, raw) {
		ev.Foreign = true
	}
	// Copilot の PascalCase の配線（VS Code 互換の形式）は必ず hook_event_name を持つ。無ければ CLI の camelCase の配線
	// （sessionId が無い版もあるので isCamel だけでは見分けられない）
	if ev.Agent == Copilot && (isCamel(raw) || (len(raw) > 0 && first(raw, "hook_event_name") == "")) {
		ev.Shape = Camel
	}

	ev.RawName = first(raw, "hook_event_name", "hookEventName")
	if ev.RawName == "" {
		ev.RawName = opts.Event
	}
	if ev.RawName == "" && ev.Shape == Camel {
		ev.RawName = guessCamelEvent(raw) // 予備（配線の --event 忘れ）。項目の有無から推す
	}
	ev.Name = NameOf(ev.RawName)
	if ev.RawName == "" {
		ev.Name = ""
	}

	ev.SessionID = first(raw, "session_id", "sessionId")
	if ev.SessionID == "" {
		ev.SessionID = sessionFromEnv(ev.Agent, opts.getenv)
	}
	ev.SubagentID = first(raw, "agent_id", "agentId")
	ev.TranscriptPath = first(raw, "transcript_path", "transcriptPath")
	ev.CWD = first(raw, "cwd")
	ev.ProjectDir = projectDir(ev.CWD, opts)

	ev.Prompt = first(raw, "prompt")
	if ev.Prompt == "" && ev.Name == SessionStart {
		ev.Prompt = first(raw, "initialPrompt", "initial_prompt")
	}
	ev.StopHookActive = truthy(first2(raw, "stop_hook_active", "stopHookActive"))
	ev.Source = first(raw, "source")
	ev.Reason = first(raw, "reason")

	if name := first(raw, "tool_name", "toolName"); name != "" {
		input, ok := raw["tool_input"]
		if !ok {
			input = raw["toolArgs"]
		}
		ev.Tool = newTool(ev.Agent, name, input)
		ev.Tool.UseID = first(raw, "tool_use_id", "toolUseId", "toolCallId")
		for _, k := range []string{"tool_response", "tool_result", "toolResult", "toolResponse"} {
			if v, ok := raw[k]; ok && v != nil {
				ev.Tool.Response = newResponse(v)
				break
			}
		}
	}
	return ev
}

// guessCamelEvent は、イベント名の無い Copilot CLI の camelCase の入力のイベントを項目から推す（配線の --event が無いときの予備）。
func guessCamelEvent(raw map[string]any) string {
	has := func(k string) bool { _, ok := raw[k]; return ok }
	switch {
	case has("toolResult"):
		return "postToolUse"
	case has("toolName"):
		return "preToolUse"
	case has("prompt"):
		return "userPromptSubmitted"
	case has("source") || has("initialPrompt"):
		return "sessionStart"
	case has("reason"):
		return "sessionEnd"
	}
	return ""
}

// sessionFromEnv は入力に session_id が無いときの予備（Codex の CODEX_THREAD_ID・Copilot CLI の COPILOT_AGENT_SESSION_ID）。
func sessionFromEnv(a Agent, getenv Getenv) string {
	switch a {
	case Codex:
		if v := getenv("CODEX_THREAD_ID"); v != "" {
			return v
		}
		return getenv("CODEX_SESSION_ID")
	case Copilot:
		return getenv("COPILOT_AGENT_SESSION_ID")
	}
	return ""
}

// projectDir は CLAUDE_PROJECT_DIR → cwd の git のルート → cwd（kit の hook・hookcmd と同じ順）。
// Codex・Copilot には CLAUDE_PROJECT_DIR が渡らないので git のルートになる。
func projectDir(cwd string, opts ParseOptions) string {
	if d := opts.getenv("CLAUDE_PROJECT_DIR"); d != "" {
		return d
	}
	if cwd == "" {
		if opts.Getwd != nil {
			cwd = opts.Getwd()
		} else if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	root := GitRoot
	if opts.GitRoot != nil {
		root = opts.GitRoot
	}
	if r := root(cwd); r != "" {
		return r
	}
	return cwd
}

// first は m の keys のうち最初に値（空でない文字列・数値）のあるものを文字列で返す。
func first(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	}
	return false
}

// EncodeInput は Event を、その AI・書式の hook の入力（stdin の JSON）に戻す。
// テストで AI ごとの入力を作るために使う（kit/loop/verify のケースを Go の表駆動テストに移すとき）。
// Raw は使わない（Event の項目だけから作る）。Parse(EncodeInput(ev)) は ev と同じ Event になる
// （camelCase ではイベント名が入らないので ParseOptions.Event に ev.RawName を渡す）。
func EncodeInput(ev Event) ([]byte, error) {
	m := map[string]any{}
	put := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	if ev.Shape == Camel {
		put("sessionId", ev.SessionID)
		put("transcriptPath", ev.TranscriptPath)
		put("cwd", ev.CWD)
		put("source", ev.Source)
		put("reason", ev.Reason)
		put("agentId", ev.SubagentID)
		if ev.Name == SessionStart {
			put("initialPrompt", ev.Prompt)
		} else {
			put("prompt", ev.Prompt)
		}
		if ev.StopHookActive {
			m["stopHookActive"] = true
		}
		if t := ev.Tool; t != nil {
			m["toolName"] = t.RawName
			put("toolCallId", t.UseID)
			if t.Input != nil {
				b, err := marshal(t.Input)
				if err != nil {
					return nil, err
				}
				m["toolArgs"] = strings.TrimRight(string(b), "\n")
			} else {
				put("toolArgs", t.InputText)
			}
			if t.Response != nil {
				m["toolResult"] = t.Response.Raw
			}
		}
	} else {
		put("hook_event_name", ev.RawName)
		put("session_id", ev.SessionID)
		put("agent_id", ev.SubagentID)
		put("transcript_path", ev.TranscriptPath)
		put("cwd", ev.CWD)
		put("prompt", ev.Prompt)
		put("source", ev.Source)
		put("reason", ev.Reason)
		if ev.StopHookActive {
			m["stop_hook_active"] = true
		}
		if t := ev.Tool; t != nil {
			m["tool_name"] = t.RawName
			put("tool_use_id", t.UseID)
			if t.Input != nil {
				m["tool_input"] = t.Input
			} else if t.InputText != "" {
				m["tool_input"] = t.InputText
			}
			if t.Response != nil {
				m["tool_response"] = t.Response.Raw
			}
		}
	}
	return marshal(m)
}

// marshal は HTML のエスケープをしない JSON（末尾に改行）。hook の出力は AI と人が読むので <>& をそのまま出す。
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
