package usagesnap

// 作業名を送るかの判定。以前の CLI（1.0.0 より前）のテストの SendPromptsTest のうち usage_snapshot の
// 2 件を移したもの。以前の CLI の結果は撤去の前に記録した（golden_test.go）。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

const testAPIURL = "https://im.invalid/im"

// promptsEnv は API モードの環境（XDG_CONFIG_HOME は home の下）。rule は覚えた値（"" なら覚えていない・"1"・"0"）、
// envValue は LOOPTRACK_USAGE_SEND_PROMPTS（"" なら未設定）。
func promptsEnv(t testing.TB, home, rule, envValue string) map[string]string {
	t.Helper()
	m := homeEnv(home, "LOOPTRACK_API_URL", testAPIURL, "LOOPTRACK_PROJECT", "req", "XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if envValue != "" {
		m["LOOPTRACK_USAGE_SEND_PROMPTS"] = envValue
	}
	f := sendPromptsFile(Options{Env: env.FromMap(m), Home: home})
	_ = os.Remove(f)
	if rule != "" {
		writeRaw(t, f, rule)
	}
	return m
}

func writeRaw(t testing.TB, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSendPromptsMatrix(t *testing.T) {
	home, path := claudeSetup(t)
	// (覚えたルール（"" = 未受信・古いサーバ）, 環境変数（"" = 未設定）) → 送るか
	cases := []struct {
		rule, env string
		want      bool
	}{
		{"", "", false}, {"", "1", false}, {"", "0", false},
		{"0", "", false}, {"0", "1", false}, {"0", "0", false},
		{"1", "", true}, {"1", "1", true}, {"1", "0", false}, {"1", " 0 ", false}, {"1", "yes", true},
	}
	for _, c := range cases {
		e := promptsEnv(t, home, c.rule, c.env)
		if got := snap(t, call{Fn: "allowed", Env: e}); got != c.want {
			t.Errorf("%+v: allowed = %v", c, got)
		}
		p := snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", WS: true, Env: e})
		if got := hasKey(pick(t, p, "segments", 0), "label"); got != c.want {
			t.Errorf("%+v: label の有無 = %v", c, got)
		}
	}
	// 区間ごとの数値は、送らないときも同じ（label 以外）
	off := snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", WS: true, Env: promptsEnv(t, home, "0", "")})
	on := snapObj(t, call{Fn: "claude", Path: path, SID: "sess-1", WS: true, Env: promptsEnv(t, home, "1", "")})
	for _, s := range pick(t, on, "segments").([]any) {
		s.(*jsonorder.Object).Delete("label")
	}
	if jsonorder.Compact(pick(t, off, "segments")) != jsonorder.Compact(pick(t, on, "segments")) {
		t.Error("label 以外の区間の値が変わった")
	}
}

func TestSendPromptsRemember(t *testing.T) {
	home := t.TempDir()
	e := promptsEnv(t, home, "1", "")
	check := func(e map[string]string, res string, want bool) {
		t.Helper()
		if got := snap(t, call{Fn: "remember", Arg: res, Env: e}); got != want {
			t.Errorf("remember(%s) の後 allowed = %v, want %v", res, got, want)
		}
	}
	check(e, `{"id": 2, "duplicate": false}`, true) // 古いサーバの応答（キーなし）は覚えた値を変えない
	check(e, `{"id": 3, "send_prompts": false}`, false)
	check(e, `null`, false)
	check(e, `{"send_prompts": "yes"}`, false)
	// 別のプロジェクト・別のサーバは別に覚える
	other := merge2(e, "LOOPTRACK_PROJECT", "other")
	check(other, `{"send_prompts": true}`, true)
	check(e, `{}`, false)
	if got := snap(t, call{Fn: "allowed", Env: merge2(e, "LOOPTRACK_API_URL", "https://im.invalid/im/api/v1/")}); got != false {
		t.Error("末尾の /api/v1 は同じサーバ")
	}
	// API モードでない（LOOPTRACK_API_URL・LOOPTRACK_PROJECT なし）ときは覚えず、送らない
	check(homeEnv(home, "XDG_CONFIG_HOME", filepath.Join(home, "xdg")), `{"send_prompts": true}`, false)
}

func merge2(m map[string]string, k, v string) map[string]string {
	c := map[string]string{}
	for a, b := range m {
		c[a] = b
	}
	c[k] = v
	return c
}

// 置き場（looptrack の置き場だけ）。
func TestSendPromptsPlace(t *testing.T) {
	home := t.TempDir()
	m := promptsEnv(t, home, "", "")
	o := Options{Env: env.FromMap(m), Home: home}
	f := sendPromptsFile(o)
	if filepath.Base(filepath.Dir(filepath.Dir(f))) != "looptrack" {
		t.Errorf("置き場 = %v", f)
	}
	if SendPromptsAllowed(o) {
		t.Error("覚えていないのに送る")
	}
	writeRaw(t, f, "1")
	if !SendPromptsAllowed(o) {
		t.Error("置き場の値を読まない")
	}
	RememberSendPrompts(map[string]any{"send_prompts": false}, o)
	if SendPromptsAllowed(o) {
		t.Error("map の応答を覚えない")
	}
	RememberSendPrompts(jsonorder.NewObject().Set("send_prompts", true), o)
	if b, _ := os.ReadFile(f); string(b) != "1" {
		t.Errorf("%s = %q", f, b)
	}
	// LOOPTRACK_USAGE_SEND_PROMPTS=0 は覚えた値より優先
	m2 := merge2(m, "LOOPTRACK_USAGE_SEND_PROMPTS", "0")
	if SendPromptsAllowed(Options{Env: env.FromMap(m2), Home: home}) {
		t.Error("LOOPTRACK_USAGE_SEND_PROMPTS=0 で止まらない")
	}
}
