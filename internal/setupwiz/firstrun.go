package setupwiz

import (
	"github.com/howashoji/looptrack/internal/i18n"
	"strings"

	"github.com/howashoji/looptrack/internal/auth"
)

// 画面版の初回設定の答えの検査と、最後の案内（MCP の接続設定）。ターミナル版（looptrack setup）と同じ検査・同じ文字列を使う。

// FirstRunAnswers は画面版の初回設定の答え（④最初の管理者 ⑤二段階認証 ⑥最初のプロジェクト。①〜③は起動の設定で決まっている）。
type FirstRunAnswers struct {
	Login         string
	Name          string // 空ならログイン名
	Password      string
	Password2     string // 確認
	TwoFactor     string // required / optional（既定値で決めない）
	ProjectSlug   string // 空か - なら作らない
	ProjectPrefix string // 空なら slug を英大文字にしたもの
	ProjectName   string // 空なら slug
}

// PlanFirstRun は画面版の答えをターミナル版と同じ規則で検査し、Provision に渡す管理者と最初のプロジェクトを返す。
func PlanFirstRun(a FirstRunAnswers) (Admin, FirstProject, error) {
	var ad Admin
	var err error
	if ad.Login, err = checkLogin(a.Login); err != nil {
		return ad, FirstProject{}, err
	}
	ad.Name = strings.TrimSpace(a.Name)
	if ad.Name == "" {
		ad.Name = ad.Login
	} else if ad.Name, err = checkNonEmpty(ad.Name); err != nil {
		return ad, FirstProject{}, i18n.Wrapf(err, "firstrun.err.display_name")
	}
	if len([]rune(a.Password)) < auth.MinPasswordLen {
		return ad, FirstProject{}, auth.ErrPasswordPolicy
	}
	if a.Password != a.Password2 {
		return ad, FirstProject{}, i18n.Errorf("firstrun.err.password_mismatch")
	}
	if strings.TrimSpace(a.TwoFactor) == "" {
		return ad, FirstProject{}, i18n.Errorf("firstrun.err.two_factor_unset")
	}
	if ad.TwoFactor, err = checkTwoFactor(a.TwoFactor); err != nil {
		return ad, FirstProject{}, err
	}
	p, err := checkProject(a.ProjectSlug, a.ProjectPrefix, a.ProjectName)
	if err != nil {
		return ad, FirstProject{}, i18n.Wrapf(err, "firstrun.err.first_project")
	}
	if ad.PasswordHash, err = hashPassword(a.Password); err != nil {
		return ad, FirstProject{}, err
	}
	return ad, p, nil
}

// MCPConfig は AI のクライアント 1 つ分の MCP の接続設定（コピーしてそのまま貼れる形）。
type MCPConfig struct {
	Client string // 例 Claude Code
	Where  string // 貼る場所・使い方
	Text   string // 貼る文字列（複数行あり）
	Note   string // 補足（空あり）
}

// MCPConfigs は Claude Code・Codex・Copilot の MCP の接続設定を返す（looptrack setup の最後の案内と、画面版の最後の画面が同じものを出す）。
// baseURL はブラウザで開く URL の基点（例 http://127.0.0.1:8090/looptrack）。project があれば既定のプロジェクト（X-Looptrack-Project）を付ける。
// Text（貼る文字列）は言語で変わらない。lang で変わるのは Client・Where・Note（貼る場所と補足）だけ。
func MCPConfigs(lang i18n.Lang, baseURL, project string) []MCPConfig {
	mcp := strings.TrimRight(baseURL, "/") + "/mcp"
	claude := "claude mcp add --transport http looptrack " + mcp
	jsonHeaders, tomlHeaders := "", ""
	if project != "" {
		claude += ` --header "X-Looptrack-Project: ` + project + `"`
		jsonHeaders = `, "headers": { "X-Looptrack-Project": "` + project + `" }`
		tomlHeaders = "\n" + `http_headers = { "X-Looptrack-Project" = "` + project + `" }`
	}
	return []MCPConfig{
		{Client: "Claude Code", Where: i18n.T(lang, "setupwiz.mcp.where.claude_cli"), Text: claude},
		{Client: "Claude Code", Where: i18n.T(lang, "setupwiz.mcp.where.claude_json"),
			Text: `{ "mcpServers": { "looptrack": { "type": "http", "url": "` + mcp + `"` + jsonHeaders + ` } } }`},
		{Client: "Codex", Where: i18n.T(lang, "setupwiz.mcp.where.codex"), Text: "[mcp_servers.looptrack]\n" + `url = "` + mcp + `"` + tomlHeaders},
		{Client: i18n.T(lang, "setupwiz.mcp.client.copilot_vscode"), Where: i18n.T(lang, "setupwiz.mcp.where.copilot_vscode"),
			Text: `{ "servers": { "looptrack": { "type": "http", "url": "` + mcp + `"` + jsonHeaders + ` } } }`},
		{Client: "GitHub Copilot CLI", Where: i18n.T(lang, "setupwiz.mcp.where.copilot_cli"),
			Text: `{ "mcpServers": { "looptrack": { "type": "http", "url": "` + mcp + `"` + jsonHeaders + `, "tools": ["*"] } } }`,
			Note: i18n.T(lang, "setupwiz.mcp.note.copilot_cli")},
	}
}
