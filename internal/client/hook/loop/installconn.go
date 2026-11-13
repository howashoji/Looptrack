package loop

// 接続先（LOOPTRACK_API_URL・LOOPTRACK_PROJECT）が hook の環境に無いとき、導入先の設定から補う（Codex・Copilot）。
//
// init は Claude Code には .claude/settings.json の env で接続先を渡すが、Codex と Copilot の設定には env が無い。
// core の配線（summary・usage）は命令の前に接続先を前置しているが、loop の配線は前置していないので、Codex と Copilot の
// loop の hook（連絡・タイムライン・サーバの引き継ぎ・それらが起動する CLI）はサーバの設定が無いとみなして黙って通っていた。
// 配線を書き換えると導入先は init のやり直しと（Codex は）hook の信頼のやり直しが要るので、hook の側で init が書いた設定を読む。
//
//   - Codex: <ルート>/.codex/config.toml の [shell_environment_policy] の set（init が書くインライン表 1 行）
//   - Copilot: <ルート>/.claude/.looptrack-kit.json の url と project（Copilot の init は接続先をほかの設定ファイルに書かない）
//
// 環境にある値が常に優先する。読めない・形が違う・壊れているときは何も補わない（従来どおり）。Claude Code は補わない。
// URL を補ったときは、環境の LOOPTRACK_TOKEN を hook とその子の環境から外す。環境のトークンは URL に関係なく使われるので、
// リポジトリの中のファイルが指す URL へ送ってしまう。外せば、補った URL には資格情報のファイルのその URL の項目だけが使われる。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
)

const (
	connURLEnv     = "LOOPTRACK_API_URL"
	connProjectEnv = "LOOPTRACK_PROJECT"
	connTokenEnv   = "LOOPTRACK_TOKEN"
)

var (
	// tomlSectionRe は表の見出しの行。tomlSetRe は shell_environment_policy の中の set の行か、外の shell_environment_policy.set の行。
	tomlSectionRe = regexp.MustCompile(`^\s*\[\s*([A-Za-z0-9_.\-]+)\s*\]\s*(?:#.*)?$`)
	tomlSetRe     = regexp.MustCompile(`^\s*set\s*=\s*\{(.*)\}\s*(?:#.*)?$`)
	tomlDottedRe  = regexp.MustCompile(`^\s*shell_environment_policy\s*\.\s*set\s*=\s*\{(.*)\}\s*(?:#.*)?$`)
	// tomlPairRe はインライン表の 1 組（エスケープを含まない基本文字列だけを読む）。
	tomlPairRe = regexp.MustCompile(`([A-Za-z0-9_]+)\s*=\s*"([^"\\]*)"`)
)

// codexConnection は .codex/config.toml の shell_environment_policy.set から接続先を読む（無ければ空の map）。
func codexConnection(root string) map[string]string {
	b, err := os.ReadFile(filepath.Join(root, ".codex", "config.toml"))
	if err != nil {
		return nil
	}
	section := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if m := tomlSectionRe.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		var body string
		if m := tomlDottedRe.FindStringSubmatch(line); m != nil && section == "" {
			body = m[1]
		} else if m := tomlSetRe.FindStringSubmatch(line); m != nil && section == "shell_environment_policy" {
			body = m[1]
		} else {
			continue
		}
		out := map[string]string{}
		for _, p := range tomlPairRe.FindAllStringSubmatch(body, -1) {
			if p[1] == connURLEnv || p[1] == connProjectEnv {
				out[p[1]] = p[2]
			}
		}
		return out
	}
	return nil
}

// copilotConnection は .claude/.looptrack-kit.json（init の記録）の url と project を読む。
func copilotConnection(root string) map[string]string {
	b, err := os.ReadFile(filepath.Join(root, ".claude", ".looptrack-kit.json"))
	if err != nil {
		return nil
	}
	var rec struct {
		URL     string `json:"url"`
		Project string `json:"project"`
	}
	if json.Unmarshal(b, &rec) != nil {
		return nil
	}
	out := map[string]string{}
	if v := strings.TrimSpace(rec.URL); v != "" {
		out[connURLEnv] = v
	}
	if v := strings.TrimSpace(rec.Project); v != "" {
		out[connProjectEnv] = v
	}
	return out
}

// withInstallConnection は、環境に接続先が無い Codex・Copilot の hook に、導入先の設定の接続先を補った環境を返す。
// 補うのは Getenv（hook 自身が読む）と Environ（hook が起動する CLI に渡す）の両方。補うものが無ければ e をそのまま返す。
func withInstallConnection(e *Env, ev hookio.Event) *Env {
	var read func(string) map[string]string
	switch ev.Agent {
	case hookio.Codex:
		read = codexConnection
	case hookio.Copilot:
		read = copilotConnection
	default:
		return e
	}
	missing := false
	for _, k := range []string{connURLEnv, connProjectEnv} {
		if strings.TrimSpace(e.Getenv(k)) == "" {
			missing = true
		}
	}
	if !missing {
		return e
	}
	found := read(root(ev, e))
	add := map[string]string{}
	for k, v := range found {
		if strings.TrimSpace(e.Getenv(k)) == "" && v != "" {
			add[k] = v
		}
	}
	if len(add) == 0 {
		return e
	}
	_, dropToken := add[connURLEnv]
	c := *e
	getenv, environ := e.Getenv, e.Environ
	c.Getenv = func(k string) string {
		if v, ok := add[k]; ok {
			return v
		}
		if dropToken && k == connTokenEnv {
			return ""
		}
		return getenv(k)
	}
	c.Environ = func() []string {
		var out []string
		for _, kv := range environ() {
			if k, _, _ := strings.Cut(kv, "="); dropToken && (k == connTokenEnv || k == connURLEnv) {
				continue // 外したトークン（と、空の URL の行）を子に渡さない
			}
			out = append(out, kv)
		}
		for k, v := range add {
			out = append(out, k+"="+v)
		}
		return out
	}
	return &c
}
