package hookio

import (
	"encoding/json"
	"strings"
)

// ツール名 → 種類。AI ごとに名前が違う（Copilot は文書のみで実物は未確認）。
// 大文字小文字は区別する（Claude Code の Bash と Copilot CLI の bash はどちらもシェルだが、名前の表は分けて持つ）。
var toolKinds = map[Agent]map[string]ToolKind{
	ClaudeCode: {
		"Bash": KindBash,
		"Edit": KindEdit, "MultiEdit": KindEdit, "NotebookEdit": KindEdit,
		"Write": KindWrite,
		"Read":  KindRead,
	},
	// Codex: シェルは Bash（hook の tool_name。Claude Code に合わせてある）、編集は apply_patch。
	Codex: {
		"Bash": KindBash, "shell": KindBash,
		"apply_patch": KindEdit, "Edit": KindEdit, "Write": KindWrite, "Read": KindRead,
	},
	// Copilot: CLI（bash・powershell・edit・create・view）と VS Code（run_in_terminal・replace_string_in_file・create_file・read_file）。
	// PascalCase の配線の CLI は Claude の書式の名前に読み替えることがあるので Claude Code の名前も受ける。
	Copilot: {
		"bash": KindBash, "powershell": KindBash, "run_in_terminal": KindBash, "Bash": KindBash,
		"edit": KindEdit, "replace_string_in_file": KindEdit, "multi_replace_string_in_file": KindEdit,
		"insert_edit_into_file": KindEdit, "edit_notebook_file": KindEdit, "apply_patch": KindEdit, "Edit": KindEdit,
		"create": KindWrite, "create_file": KindWrite, "Write": KindWrite,
		"view": KindRead, "read_file": KindRead, "Read": KindRead,
	},
}

// newTool はツール名と入力から Tool を作る。input はオブジェクト・JSON の文字列（Copilot CLI の toolArgs）・その他のどれでもよい。
func newTool(agent Agent, name string, input any) *Tool {
	t := &Tool{RawName: name, Name: name, Kind: KindOther}
	if k, ok := toolKinds[agent][name]; ok {
		t.Kind = k
	} else if srv, tool, ok := splitMCP(agent, name); ok {
		t.Kind, t.Server, t.Name = KindMCP, srv, tool
	}
	switch v := input.(type) {
	case map[string]any:
		t.Input = v
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err == nil && m != nil {
			t.Input = m
		} else {
			t.InputText = v
		}
	case nil:
	default:
		b, _ := json.Marshal(v)
		t.InputText = string(b)
	}
	return t
}

// splitMCP は MCP のツール名をサーバ名とツール名に分ける（推定）。
//
//   - mcp__<サーバ>__<ツール>: Claude Code・Codex（Copilot も PascalCase の配線で同じ形のことがある）。最初の __ で分ける
//   - mcp_<サーバ>_<ツール>: VS Code（推論・未確認）。最初の _ で分ける（サーバ名に _ があると誤る → MatchMCP で照合する）
//   - <サーバ>-<ツール>: Copilot CLI（文書）。最後の - で分ける（サーバ名は im-oauth のように - を含み、ツール名は snake_case が多い）
func splitMCP(agent Agent, name string) (server, tool string, ok bool) {
	if rest, found := strings.CutPrefix(name, "mcp__"); found {
		if i := strings.Index(rest, "__"); i > 0 && i+2 < len(rest) {
			return rest[:i], rest[i+2:], true
		}
		return "", "", false
	}
	if agent != Copilot {
		return "", "", false
	}
	if rest, found := strings.CutPrefix(name, "mcp_"); found {
		if i := strings.Index(rest, "_"); i > 0 && i+1 < len(rest) {
			return rest[:i], rest[i+1:], true
		}
		return "", "", false
	}
	if i := strings.LastIndex(name, "-"); i > 0 && i+1 < len(name) {
		return name[:i], name[i+1:], true
	}
	return "", "", false
}

// MatchMCP は、このツールが MCP サーバ server のツール tool か（AI ごとのどの書式でも）。
// Server・Name の推定が区切りの曖昧さで外れても、名前全体で照合するので確実に当たる。
func (t *Tool) MatchMCP(server, tool string) bool {
	if t == nil {
		return false
	}
	for _, f := range []string{"mcp__%s__%s", "mcp_%s_%s", "%s-%s"} {
		if t.RawName == strings.Replace(strings.Replace(f, "%s", server, 1), "%s", tool, 1) {
			return true
		}
	}
	return t.Kind == KindMCP && t.Server == server && t.Name == tool
}

// newResponse は PostToolUse の結果を読む。
func newResponse(raw any) *Response {
	if raw == nil {
		return nil
	}
	r := &Response{Raw: raw}
	switch v := raw.(type) {
	case string:
		r.Text = v
	case map[string]any:
		if b, ok := first2(v, "isError", "is_error").(bool); ok && b {
			r.Failed = true
		}
		if rt := str(first2(v, "resultType", "result_type")); rt != "" && rt != "success" {
			r.Failed = true
		}
		switch {
		case first2(v, "textResultForLlm", "text_result_for_llm") != nil:
			r.Text = str(first2(v, "textResultForLlm", "text_result_for_llm"))
		case contentText(v["content"]) != "":
			// MCP の結果: {content: [{type: "text", text}], isError}
			r.Text = contentText(v["content"])
		case v["stdout"] != nil || v["stderr"] != nil:
			// Claude Code の Bash: {stdout, stderr, interrupted, isImage}
			r.Text = str(v["stdout"])
			if e := str(v["stderr"]); e != "" {
				if r.Text != "" {
					r.Text += "\n"
				}
				r.Text += e
			}
		default:
			r.Text = compactJSON(v)
		}
	case []any:
		// Claude Code の MCP の結果は content の配列そのもの
		if r.Text = contentText(v); r.Text == "" {
			r.Text = compactJSON(v)
		}
	default:
		r.Text = compactJSON(v)
	}
	return r
}

// contentText は MCP の content（[{type: "text", text}, …]）の text を改行でつなぐ（text が 1 つも無ければ ""）。
func contentText(v any) string {
	items, ok := v.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && str(m["type"]) == "text" {
			parts = append(parts, str(m["text"]))
		}
	}
	return strings.Join(parts, "\n")
}

func first2(m map[string]any, a, b string) any {
	if v, ok := m[a]; ok && v != nil {
		return v
	}
	return m[b]
}

func compactJSON(v any) string {
	b, err := marshal(v)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}
