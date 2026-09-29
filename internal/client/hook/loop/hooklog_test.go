package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
)

// TestHookLogGuardsNoSecrets は、秘密の形の入力（.env を読むコマンド・git reset --hard）をガードに与えたとき、
// 記録の行に kind（定数の語）が入り、コマンドの文字列・パス・理由の文面・ファイルの中身が入らないこと。
// 記録が実際に起きたこと（kind が期待の定数）も同じテストで確かめる（「入らない」だけを見ると、書いていなくても通る）。
// 設定しないときは置き場に何も作らないこと（対照）も確かめる。
func TestHookLogGuardsNoSecrets(t *testing.T) {
	proj, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const secretValue = "SECRET_VALUE_zq81"
	if err := os.WriteFile(filepath.Join(proj, ".env"), []byte("TOKEN="+secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(proj, ".claude", ".looptrack-freshness", "hook-log.jsonl")
	envFor := func(on bool) *Env {
		return &Env{Getenv: func(k string) string {
			switch k {
			case "CLAUDECODE":
				return "1"
			case "CLAUDE_PROJECT_DIR":
				return proj
			case hookio.HookLogEnv:
				if on {
					return "1"
				}
			}
			return ""
		}, Getwd: func() string { return proj }}
	}
	bash := func(cmd string) string {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "sess-7", "cwd": proj,
			"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})
		return string(b)
	}
	readB, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "sess-7", "cwd": proj,
		"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(proj, "zz-marker-dir", ".env")}})
	cases := []struct {
		hook, in, kind, decision string
		leaks                    []string
	}{
		{"pre-tool-git-guard", bash("git reset --hard HEAD~3"), "git-guard: reset_hard", "deny", []string{"git reset", "HEAD~3", "--hard"}},
		{"pre-tool-secrets-guard", bash("cat zz-marker-dir/.env && echo " + secretValue), "secrets: show .env", "ask", []string{"cat ", "zz-marker-dir", secretValue, "echo"}},
		{"pre-tool-secrets-guard", string(readB), "secrets: open_file .env", "ask", []string{"zz-marker-dir", proj}},
		{"pre-tool-secrets-guard", bash("security find-generic-password -s zz-marker-svc -w"), "secrets: store", "ask", []string{"zz-marker-svc", "find-generic"}},
	}
	for _, c := range cases {
		// 設定しないとき: 出力は出るが、置き場は作らない（対照）
		os.RemoveAll(filepath.Join(proj, ".claude"))
		var offOut bytes.Buffer
		Main(context.Background(), c.hook, []string{"--agent", "claude-code"}, strings.NewReader(c.in), &offOut, nil, envFor(false))
		if _, err := os.Stat(filepath.Join(proj, ".claude")); err == nil {
			t.Errorf("%s: 設定しないのに置き場ができた", c.hook)
		}
		// 設定したとき
		var out bytes.Buffer
		Main(context.Background(), c.hook, []string{"--agent", "claude-code"}, strings.NewReader(c.in), &out, nil, envFor(true))
		if out.String() != offOut.String() {
			t.Errorf("%s: 記録の有無で出力が変わった", c.hook)
		}
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("%s: 記録が無い: %v（出力 %s）", c.hook, err, out.String())
		}
		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(b), &rec); err != nil {
			t.Fatalf("%s: 1 行の JSON でない: %q", c.hook, b)
		}
		if rec["kind"] != c.kind || rec["decision"] != c.decision || rec["hook"] != c.hook || rec["session"] != "sess-7" {
			t.Errorf("%s: 記録 %v, want kind %q decision %q", c.hook, rec, c.kind, c.decision)
		}
		// 理由の文面（出力の permissionDecisionReason）が記録に入らない
		var o struct {
			H struct {
				Reason string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &o); err != nil || o.H.Reason == "" {
			t.Fatalf("%s: 前提が崩れています（理由が出ていない）: %s", c.hook, out.String())
		}
		leaks := append([]string{o.H.Reason, "permissionDecision", "tool_input"}, c.leaks...)
		for _, l := range leaks {
			if strings.Contains(string(b), l) {
				t.Errorf("%s: 記録に %q が入った: %s", c.hook, l, b)
			}
		}
	}
}

// TestSecretRuleKinds は、secretRule が当たった規則の種類を定数で返し、secretPath と同じ判定であること。
func TestSecretRuleKinds(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"/p/.env", secretKindEnv},
		{"/p/.env.production", secretKindEnv},
		{"/p/credentials.json", secretKindCredJSON},
		{"/p/.netrc", secretKindNetrc},
		{"/p/_netrc", secretKindNetrc},
		{"/p/.pgpass", secretKindPgpass},
		{"/p/.npmrc", secretKindNpmrc},
		{"/p/.htpasswd", secretKindHtpasswd},
		{"/p/id_ed25519", secretKindSSHKey},
		{"/p/CERT.P12", secretKindKeystore},
		{"/p/.env*", secretKindGlob},
		{"/h/.aws/credentials", secretKindCreds},
		{"/h/.ssh/notes.pem", "dir:.ssh"},
		{"/etc/ssl/Private/x.pem", "dir:private"},
		{"/p/server.key", secretKindKey},
		{"/p/.key", secretKindKey},
		{"/p/privatekey.pem", secretKindPem},
		{"/p/vault.asc", secretKindAsc},
		// 秘密でないもの（対照）
		{"/p/.env.example", ""},
		{"/p/fullchain.pem", ""},
		{"/p/slides.key", ""},
		{"/p/credentials", ""},
		{"/private/etc/ssl/cert.pem", ""},
	} {
		got := secretRule(c.path)
		if got != c.want {
			t.Errorf("secretRule(%q) = %q, want %q", c.path, got, c.want)
		}
		if secretPath(c.path) != (c.want != "") {
			t.Errorf("secretPath(%q) と secretRule が食い違う", c.path)
		}
	}
}

// TestCombinedKindOnlyForShownParts は、合成の hook が記録に並べる kind が、実際に出力へ残る部分のものだけであること。
// keepBlock=false（SessionStart）では Block だけを出す部分の出力は捨てられるので、その kind も入らない。
// 対照: 同じ部分でも keepBlock=true（UserPromptSubmit）なら Block が残るので kind も入る。Context を出す部分は keepBlock に関わらず入る。
func TestCombinedKindOnlyForShownParts(t *testing.T) {
	const blockPart, ctxPart = "test-kind-block-only", "test-kind-context"
	RegisterPart(blockPart, time.Second, func(context.Context, hookio.Getenv, func() string, func() time.Time, []string, hookio.Event) (hookio.Result, error) {
		return hookio.Result{Block: "止める理由", Kind: "kind-block"}, nil
	})
	RegisterPart(ctxPart, time.Second, func(context.Context, hookio.Getenv, func() string, func() time.Time, []string, hookio.Event) (hookio.Result, error) {
		return hookio.Result{Context: "文脈", Kind: "kind-context"}, nil
	})
	ctx := WithEnv(context.Background(), &Env{Args: []string{"--parts", blockPart + "," + ctxPart}})
	ev := hookio.Event{}

	off, err := runCombined(ctx, ev, false)
	if err != nil {
		t.Fatal(err)
	}
	if off.Kind != "kind-context" || off.Block != "" || off.Context != "文脈" {
		t.Errorf("keepBlock=false: Kind=%q Block=%q Context=%q（kind-context だけ・Block は捨てる）", off.Kind, off.Block, off.Context)
	}
	on, err := runCombined(ctx, ev, true)
	if err != nil {
		t.Fatal(err)
	}
	if on.Kind != "kind-block, kind-context" || on.Block != "止める理由" {
		t.Errorf("対照 keepBlock=true: Kind=%q Block=%q（前提が崩れています。Block を出す部分の kind が入るはず）", on.Kind, on.Block)
	}
}
