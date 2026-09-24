package usagesnap

// 作業名（指示文の先頭 44 文字・segments[].label）を送るかの判定（以前の CLI（1.0.0 より前）と同じ規則）。
//
//   - 送るのは「利用者の LOOPTRACK_USAGE_SEND_PROMPTS が "0"（前後の空白を除く）でない」かつ「プロジェクトについて覚えた send_prompts が true」
//     のときだけ。1・未設定・その他の値はルールに従う。
//   - 覚える値は POST /projects/{slug}/usage の応答の send_prompts（bool でなければ何もしない＝古いサーバ）。
//     プロジェクト（API の URL（末尾の / と /api/v1 を除く）+ slug）ごとに 1 ファイル（中身は "1" / "0"）。
//     API の URL かプロジェクトが無ければ覚えず、送らない。
//
// 置き場（internal/client/cred と同じ）: $XDG_CONFIG_HOME（無ければ ~/.config）/looptrack/usage-send-prompts/<key>
// （Windows は %APPDATA%\looptrack\…）。以前の CLI（1.0.0 より前）の置き場は読まない。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/howashoji/looptrack/internal/client/cred"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

const sendPromptsDir = "usage-send-prompts"

// sendPromptsFile は覚えた値のファイル。API の URL かプロジェクトが無ければ ""。
func sendPromptsFile(o Options) string {
	base, _ := o.Env.Setting(env.APIURL)
	base = strings.TrimRight(trimSpace(base), "/")
	base = strings.TrimSuffix(base, "/api/v1")
	project, _ := o.Env.Setting(env.Project)
	project = trimSpace(project)
	if base == "" || project == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(base + "|" + project))
	key := hex.EncodeToString(sum[:])[:32]
	var dir string
	if runtime.GOOS == "windows" {
		appdata := o.Env.Get("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(o.home(), "AppData", "Roaming")
		}
		dir = filepath.Join(appdata, cred.AppDir)
	} else {
		cfg := o.Env.Get("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(o.home(), ".config")
		}
		dir = filepath.Join(cfg, cred.AppDir)
	}
	return filepath.Join(dir, sendPromptsDir, key)
}

// SendPromptsAllowed は作業名を送ってよいか（send_prompts_allowed）。
func SendPromptsAllowed(o Options) bool {
	v, _ := o.Env.Setting("USAGE_SEND_PROMPTS")
	if trimSpace(v) == "0" {
		return false
	}
	p := sendPromptsFile(o)
	if p == "" {
		return false
	}
	b, err := os.ReadFile(p)
	return err == nil && trimSpace(string(b)) == "1"
}

// RememberSendPrompts は POST /usage の応答の send_prompts（プロジェクト別ルール usage.send_prompts）を覚える
// （remember_send_prompts）。応答が *jsonorder.Object か map[string]any で send_prompts が bool のときだけ。
// T4（usage attach）・T9（usage_hook の送信）が送信の成功のたびに呼ぶ。書けなくてもエラーにしない。
func RememberSendPrompts(res any, o Options) {
	var v any
	switch r := res.(type) {
	case *jsonorder.Object:
		v, _ = r.Get("send_prompts")
	case map[string]any:
		v = r["send_prompts"]
	default:
		return
	}
	b, ok := v.(bool)
	if !ok {
		return
	}
	content := "0"
	if b {
		content = "1"
	}
	p := sendPromptsFile(o)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(content), 0o644)
}
