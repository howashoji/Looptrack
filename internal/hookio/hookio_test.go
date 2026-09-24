package hookio

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// 各 AI の匿名化した入力のフィクスチャ（testdata/<AI>/<イベント>/*.json）で Event と Result が往復することを確かめる。
//
// フィクスチャの項目:
//
//	note               何のケースか（出典・確度）
//	args               配線が渡す引数（--agent・--event・--no-block）
//	env                環境変数（これ以外は空）
//	git_root           cwd の git のルート（空なら git の外）
//	wd                 入力に cwd が無いときの作業ディレクトリ
//	trust_unconfirmed  RenderOptions.TrustUnconfirmed
//	input              hook の stdin（AI ごとの形そのまま。実物由来の値は合成値に置き換える）
//	event              Parse の結果（期待値）
//	result             hook の本体が返す Result
//	output             Render の結果（期待値。stdout は JSON として比べる。何も出さないなら null）
//	effective          Normalize の結果（result と違うとき＝差し戻せず systemMessage に回したときだけ）
//
// 確かめること: input → Event が event と同じ／Event → EncodeInput → Parse で同じ Event に戻る／result → Render が output と同じ／
// output → ParseOutput が effective（無ければ result）と同じ。`go test ./internal/hookio -run Fixtures -update` で event・output・
// effective を書き直す（書き直した差分は必ず目で確かめる）。

var update = flag.Bool("update", false, "フィクスチャの event・output・effective を今の実装の結果で書き直す")

type fixture struct {
	Note             string            `json:"note"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env,omitempty"`
	GitRoot          string            `json:"git_root,omitempty"`
	Wd               string            `json:"wd,omitempty"`
	TrustUnconfirmed bool              `json:"trust_unconfirmed,omitempty"`
	Input            json.RawMessage   `json:"input"`
	Event            json.RawMessage   `json:"event"`
	Result           Result            `json:"result"`
	Output           *goldenOutput     `json:"output"`
	Effective        *Result           `json:"effective,omitempty"`
}

type goldenOutput struct {
	Stdout   json.RawMessage `json:"stdout"`
	Stderr   string          `json:"stderr,omitempty"`
	ExitCode int             `json:"exit_code"`
}

// requiredEvents は AI ごとにフィクスチャが要るイベント（受け入れ条件: 各 AI の入力で往復する）。
var requiredEvents = []Name{SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SessionEnd}

func fixtureFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "*", "*", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("フィクスチャがありません: %v", err)
	}
	return files
}

func (f fixture) options(t *testing.T) (RunOptions, ParseOptions) {
	t.Helper()
	getenv := func(k string) string { return f.Env[k] }
	opts, rest, err := ParseArgs(f.Args, getenv)
	if err != nil || len(rest) > 0 {
		t.Fatalf("args %v: err=%v rest=%v", f.Args, err, rest)
	}
	opts.Parse.GitRoot = func(dir string) string {
		if dir == "" {
			return ""
		}
		return f.GitRoot
	}
	opts.Parse.Getwd = func() string { return f.Wd }
	opts.Render.TrustUnconfirmed = f.TrustUnconfirmed
	return opts, opts.Parse
}

func TestFixtures(t *testing.T) {
	seen := map[Agent]map[Name]bool{}
	for _, path := range fixtureFiles(t) {
		rel, _ := filepath.Rel("testdata", path)
		parts := strings.Split(rel, string(filepath.Separator))
		agent, dirEvent := Agent(parts[0]), Name(parts[1])
		t.Run(filepath.ToSlash(rel), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var f fixture
			if err := json.Unmarshal(b, &f); err != nil {
				t.Fatalf("フィクスチャを読めません: %v", err)
			}
			if f.Note == "" {
				t.Error("note が空（何のケースか・出典を書く）")
			}
			opts, popts := f.options(t)

			// 1. input → Event
			ev, err := Parse(f.Input, popts)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if ev.Agent != agent {
				t.Errorf("Agent = %q, ディレクトリは %q", ev.Agent, agent)
			}
			if ev.Name != dirEvent {
				t.Errorf("Name = %q, ディレクトリは %q", ev.Name, dirEvent)
			}
			gotEvent := mustJSON(t, ev)

			// 2. Event → EncodeInput → Parse で同じ Event に戻る
			enc, err := EncodeInput(ev)
			if err != nil {
				t.Fatalf("EncodeInput: %v", err)
			}
			popts2 := popts
			if ev.Shape == Camel {
				popts2.Event = ev.RawName
			}
			ev2, err := Parse(enc, popts2)
			if err != nil {
				t.Fatalf("EncodeInput の結果を Parse できません: %v\n%s", err, enc)
			}
			if !jsonEqual(mustJSON(t, ev2), gotEvent) {
				t.Errorf("EncodeInput で往復しません\n  enc: %s\n  got: %s\n want: %s", enc, mustJSON(t, ev2), gotEvent)
			}

			// 3. Result → Render
			out := Render(ev, f.Result, opts.Render)
			if out.Stdout != "" && (!strings.HasSuffix(out.Stdout, "\n") || strings.Count(out.Stdout, "\n") != 1) {
				t.Errorf("stdout は JSON 1 行 + 改行のはず: %q", out.Stdout)
			}
			gotOut := &goldenOutput{Stdout: json.RawMessage("null"), Stderr: out.Stderr, ExitCode: out.ExitCode}
			if out.Stdout != "" {
				gotOut.Stdout = json.RawMessage(strings.TrimSpace(out.Stdout))
			}

			// 4. output → ParseOutput が Normalize と同じ（AI が読む結果＝ hook が意図した結果のうち出せたもの）
			norm := Normalize(ev.Agent, ev.Name, f.Result, opts.Render)
			if back := ParseOutput(ev.Agent, ev.Name, out); back != norm {
				t.Errorf("ParseOutput(Render) = %+v, Normalize = %+v", back, norm)
			}
			var gotEff *Result
			if norm != f.Result {
				gotEff = &norm
			}

			if *update {
				f.Event, f.Output, f.Effective = gotEvent, gotOut, gotEff
				writeFixture(t, path, f)
				return
			}
			if !jsonEqual(gotEvent, f.Event) {
				t.Errorf("Event が違います\n  got: %s\n want: %s", gotEvent, f.Event)
			}
			if f.Output == nil {
				t.Fatalf("output がありません（-update で作る）")
			}
			if !jsonEqual(gotOut.Stdout, f.Output.Stdout) || gotOut.Stderr != f.Output.Stderr || gotOut.ExitCode != f.Output.ExitCode {
				t.Errorf("出力が違います\n  got: %s %q %d\n want: %s %q %d", gotOut.Stdout, gotOut.Stderr, gotOut.ExitCode,
					f.Output.Stdout, f.Output.Stderr, f.Output.ExitCode)
			}
			if !reflect.DeepEqual(gotEff, f.Effective) {
				t.Errorf("effective が違います\n  got: %+v\n want: %+v", gotEff, f.Effective)
			}
			// golden の出力そのものも ParseOutput で読める（他の実装の出力と比べるときと同じ経路）
			golden := Output{ExitCode: f.Output.ExitCode, Stderr: f.Output.Stderr}
			if s := string(f.Output.Stdout); s != "null" {
				golden.Stdout = s + "\n"
			}
			if back := ParseOutput(ev.Agent, ev.Name, golden); back != norm {
				t.Errorf("ParseOutput(golden) = %+v, want %+v", back, norm)
			}
		})
		if seen[agent] == nil {
			seen[agent] = map[Name]bool{}
		}
		seen[agent][dirEvent] = true
	}
	for _, a := range Agents {
		for _, n := range requiredEvents {
			if !seen[a][n] {
				t.Errorf("%s の %s のフィクスチャがありません", a, n)
			}
		}
	}
}

// Claude Code 2.1.278 の実物の stdin には hookio が読まない項目（scratchpad_dir・prompt_id・permission_mode・effort・
// mcp_server・duration_ms）が入る。mcp-full-keys.json はその全項目の記録で、ここでは「Raw からは読めるが Event には出ない」
// （＝知らない項目が増えても Event が変わらない）ことを確かめる。
func TestClaudeCodeExtraInputKeysIgnored(t *testing.T) {
	extra := map[Name][]string{
		PreToolUse:  {"scratchpad_dir", "prompt_id", "permission_mode", "effort", "mcp_server"},
		PostToolUse: {"scratchpad_dir", "prompt_id", "permission_mode", "effort", "mcp_server", "duration_ms"},
	}
	for name, keys := range extra {
		path := filepath.Join("testdata", "claude-code", string(name), "mcp-full-keys.json")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f fixture
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		_, popts := f.options(t)
		ev, err := Parse(f.Input, popts)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		encoded := string(mustJSON(t, ev))
		for _, k := range keys {
			if _, ok := ev.Raw[k]; !ok {
				t.Errorf("%s: input に %s がありません（実物から採り直す）", path, k)
			}
			if strings.Contains(encoded, `"`+k+`"`) {
				t.Errorf("%s: Event に %s が出ています: %s", path, k, encoded)
			}
		}
		// 読む項目（tool_use_id・tool_response）は落ちない
		if ev.Tool == nil || ev.Tool.UseID == "" || !ev.Tool.MatchMCP("im", "set_status") {
			t.Errorf("%s: tool = %+v", path, ev.Tool)
		}
		if name == PostToolUse && (ev.Tool.Response == nil || ev.Tool.Response.Text == "") {
			t.Errorf("%s: tool_response が落ちました", path)
		}
	}
}

// フィクスチャに実物由来の値（利用者名・実際のパス・社内のホスト名）が残っていないこと。
func TestFixturesAnonymized(t *testing.T) {
	// 語は分けて書く（このファイル自身が公開物の検査（deploy/public-scan.sh）に掛からないように）
	bad := regexp.MustCompile(`(?i)/Users/|` + "iss" + "ui|how" + "ashoji|kaz" + "umi" + `|Documents/Development`)
	for _, path := range fixtureFiles(t) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := bad.Find(b); m != nil {
			t.Errorf("%s に実物由来の値があります: %q", path, m)
		}
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(bytes.TrimSpace(b))
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func writeFixture(t *testing.T, path string, f fixture) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFilePaths は編集のツールの対象のパス（Copilot の path・filePath・apply_patch の本文）。
func TestFilePaths(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: /p/a.go\n@@\n-a\n+b\n*** Add File: /p/b.go\n+x\n*** Delete File: c.go\n*** End Patch\n"
	cases := []struct {
		name  string
		agent Agent
		tool  string
		input any
		want  string
	}{
		{"Claude Code の file_path", ClaudeCode, "Edit", map[string]any{"file_path": "/p/a.go"}, "/p/a.go"},
		{"Copilot CLI の path", Copilot, "Edit", map[string]any{"path": "/p/a.go", "old_str": "a"}, "/p/a.go"},
		{"VS Code の filePath", Copilot, "create_file", map[string]any{"filePath": "/p/a.go"}, "/p/a.go"},
		{"Copilot CLI の apply_patch の本文（文字列）", Copilot, "Edit", patch, "/p/a.go|/p/b.go|c.go"},
		{"Codex の apply_patch（input）", Codex, "apply_patch", map[string]any{"input": patch}, "/p/a.go|/p/b.go|c.go"},
		{"パスの無い入力", Copilot, "Edit", map[string]any{"x": 1}, ""},
		{"patch でない文字列", Copilot, "Edit", "hello", ""},
	}
	for _, c := range cases {
		tl := newTool(c.agent, c.tool, c.input)
		if got := strings.Join(tl.FilePaths(), "|"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if first, _, _ := strings.Cut(c.want, "|"); tl.FilePath() != first {
			t.Errorf("%s: FilePath %q", c.name, tl.FilePath())
		}
	}
}
