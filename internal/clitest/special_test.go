package clitest

import (
	"net/http"
	"net/url"
	"strings"
)

// oauthDenied はブラウザで「許可しない」を選んだときの戻り（error=access_denied）。
func oauthDenied(r *http.Request) string {
	q := r.URL.Query()
	v := url.Values{"error": {"access_denied"}, "error_description": {"利用者が拒否しました"}, "state": {q.Get("state")}}
	return q.Get("redirect_uri") + "?" + v.Encode()
}

// credPath は資格情報のファイル（XDG_CONFIG_HOME/looptrack/credentials.json）。URL のキーは実行時に決まるので
// APIPlaceholder で書く（Run が偽 API の URL に置き換える）。
const credPath = "home/.config/looptrack/credentials.json"

func creds(entry string) Seed {
	return Seed{Path: credPath, Content: `{"` + APIPlaceholder + `": ` + entry + `}`, Mode: 0o600}
}

// special は spec で表しにくいケース（1 件ずつ）。
func special() []Case {
	noToken := map[string]string{"LOOPTRACK_TOKEN": ""}
	noAPI := map[string]string{"LOOPTRACK_API_URL": "", "LOOPTRACK_PROJECT": ""} // サーバの設定が無いシェル（Claude Code の外）
	cases := []Case{
		// ---- 検証（引数の検査で API を呼ばずに止まる）
		{Name: "new/invalid-type", Args: []string{"new", "x", "--type", "story"}},
		{Name: "list/invalid-status", Args: []string{"list", "--status", "Doing"}},
		{Name: "next/invalid-type", Args: []string{"next", "--type", "task,story"}},
		{Name: "status/invalid-status", Args: []string{"status", "DEMO-0001", "Doing"}},
		{Name: "usage-show/no-id", Args: []string{"usage", "show"}},
		{Name: "usage-attach/no-transcript", Args: []string{"usage", "attach", "DEMO-0001"}},
		{Name: "usage-report/no-period", Args: []string{"usage", "report"}},
		{Name: "usage-report/request-and-from", Args: []string{"usage", "report", "--request", "7", "--from", "2024-04-01"}},
		{Name: "usage-ledger-add/no-to", Args: []string{"usage", "ledger", "add", "名前", "--from", "2024-04-01"}},
		{Name: "usage-ledger-add/other-project", Args: []string{"usage", "ledger", "add", "名前", "--from-report", "-"},
			Stdin: strings.Replace(reportJSON, `"project":"demo"`, `"project":"other"`, 1)},
		{Name: "usage-ledger-add/bad-report", Args: []string{"usage", "ledger", "add", "名前", "--from-report", "missing.json"}},
		{Name: "verify/bad-timeout", Args: []string{"verify", "DEMO-0001", "--timeout", "0"},
			Routes: []Route{r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok"}, "")))}},

		// ---- 一覧の形
		{Name: "list/empty", Args: []string{"list"}, Routes: []Route{r("GET", pIssues, ok(list()))}},
		{Name: "list/has-feedback", Args: []string{"list", "--has-feedback"}, Routes: []Route{r("GET", pIssues, ok(list(item3)))}},
		{Name: "list/long-blocked", Args: []string{"list"}, Routes: []Route{r("GET", pIssues, ok(list(
			item("DEMO-0010", "task", "Todo", "P2", "長い blocked_by", []string{"DEMO-0001", "DEMO-0002", "DEMO-0003", "DEMO-0004"}, ""))))}},
		{Name: "ready/empty", Args: []string{"ready", "--assignee", "-"}, Routes: []Route{r("GET", pProj+"/ready", ok(list()))}},
		{Name: "show/500", Args: []string{"show", "DEMO-0001"}, Routes: []Route{r("GET", pIssue1, ErrorResponse(500, "internal", "内部エラー"))}},
		{Name: "list/unknown-api", Args: []string{"list"}}, // フィクスチャなし → 404 unknown_api「API が見つかりません」
		// 別のセッションが着手中のものに注記を出す（横取りの防止。サーバが other_session / started_ago を付ける）
		{Name: "list/other-session", Args: []string{"list"}, Routes: []Route{r("GET", pIssues, ok(list(
			item("DEMO-0010", "bug", "In Progress", "P1", "別のセッションが着手中", nil, `"assignee":"alice","other_session":true,"started_ago":"3時間"`),
			item2)))}},

		// ---- 状態変更の再試行（usage_required_on_close は付与してから 1 回だけやり直す。会話記録が無ければ拒否のまま）
		{Name: "close/422-usage-required", Args: []string{"close", "DEMO-0001"}, Routes: []Route{r("POST", pIssue1+"/status",
			status(422, `{"error":{"code":"usage_required","message":"AI の Done にはトークン情報が要ります（usage attach DEMO-0001）","rule":"usage_required_on_close"}}`))}},

		// ---- 作業コピー（edit / push）
		{Name: "edit/dirty", Args: []string{"edit", "DEMO-0001"}, Seeds: workSeeds},
		{Name: "edit/force", Args: []string{"edit", "DEMO-0001", "--force"}, Seeds: append(append([]Seed(nil), workSeeds...),
			Seed{Path: "ws/.claude/.looptrack-work/DEMO-0001.server.md", Content: "古い最新版\n"}), Routes: []Route{r("GET", pIssue1, ok(detail1))}},
		{Name: "push/no-change", Args: []string{"push", "DEMO-0001"}, Seeds: []Seed{
			{Path: "ws/.claude/.looptrack-work/DEMO-0001.md", Content: markdown1}, workSeeds[1], workSeeds[2]}},
		{Name: "push/no-work-copy", Args: []string{"push", "DEMO-0001"}},
		{Name: "push/override", Args: []string{"push", "DEMO-0001", "--override", "担当者の不在中に修正"}, Seeds: workSeeds,
			Routes: []Route{r("PATCH", pIssue1, ok(pushRes))}},
		{Name: "push/spaced-ids", Args: []string{"push", "DEMO-0001"}, Seeds: []Seed{
			{Path: "ws/.claude/.looptrack-work/DEMO-0001.md", Content: strings.Replace(markdown1, "blocked_by: []", "blocked_by: [DEMO-0002 DEMO-0003]", 1)},
			workSeeds[1], workSeeds[2]}},
		{Name: "push/rebase", Args: []string{"push", "DEMO-0001", "--rebase"}, Seeds: []Seed{workSeeds[0], workSeeds[1],
			{Path: "ws/.claude/.looptrack-work/DEMO-0001.json", Content: `{"id": "DEMO-0001", "project": "demo", "version": 3, "server_version": 4}`},
			{Path: "ws/.claude/.looptrack-work/DEMO-0001.server.md", Content: markdown1 + "\nサーバ側の追記。\n"}},
			Routes: []Route{r("PATCH", pIssue1, ok(pushRes))}},
		{Name: "push/rebase-without-conflict", Args: []string{"push", "DEMO-0001", "--rebase"}, Seeds: workSeeds},

		// ---- verify の実行結果
		{Name: "verify/no-commands", Args: []string{"verify", "DEMO-0001"},
			Routes: []Route{r("GET", pIssue1+"/verify", ok(`{"id":"DEMO-0001","body_sha256":"00","commands":[],"message":"DEMO-0001 に検証コマンドの節がありません"}`))}},
		{Name: "verify/no-commands-json", Args: []string{"verify", "DEMO-0001", "--json"},
			Routes: []Route{r("GET", pIssue1+"/verify", ok(`{"id":"DEMO-0001","body_sha256":"00","commands":[],"message":"DEMO-0001 に検証コマンドの節がありません"}`))}},
		{Name: "verify/fail", Args: []string{"verify", "DEMO-0001"}, Routes: []Route{
			r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok", "echo 失敗した; echo token=imp_secretsecret; exit 3"}, ""))),
			r("POST", pIssue1+"/verify", created(`{"message":"verify を記録: DEMO-0001（1/2 成功・1 失敗）"}`))}},
		{Name: "verify/timeout", Args: []string{"verify", "DEMO-0001", "--timeout", "0.5"}, Routes: []Route{
			r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"sleep 5"}, ""))),
			r("POST", pIssue1+"/verify", created(`{"message":"verify を記録: DEMO-0001（0/1 成功・1 失敗）"}`))}},
		{Name: "verify/post-fails", Args: []string{"verify", "DEMO-0001"}, Routes: []Route{
			r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok"}, ""))),
			r("POST", pIssue1+"/verify", ErrorResponse(409, "body_changed", "DEMO-0001 の本文が変わりました。verify をやり直してください（looptrack issue verify DEMO-0001）"))}},
		{Name: "verify/post-fails-json", Args: []string{"verify", "DEMO-0001", "--json"}, Routes: []Route{
			r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok"}, ""))),
			r("POST", pIssue1+"/verify", Response{Drop: true})}},
		{Name: "verify-last/none", Args: []string{"verify", "DEMO-0001", "--last"},
			Routes: []Route{r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok"}, "")))}},

		// ---- summary（hook 経由（--agent・--hook-json）は失敗しても何も出さずに 0 で終わる。
		//      手で打ったときは list / ready / show と同じ案内を出して非 0 で終える）
		{Name: "summary/old-server", Args: []string{"summary"}, Routes: []Route{r("GET", pProj+"/summary", ok(summaryOld))}},
		{Name: "summary/no-token", Args: []string{"summary"}, Env: noToken},
		{Name: "summary/no-token-hook", Args: []string{"summary", "--agent", "claude-code"}, Env: noToken},
		{Name: "summary/no-api", Args: []string{"summary"}, Env: noAPI},
		{Name: "summary/no-api-hook", Args: []string{"summary", "--agent", "claude-code"}, Env: noAPI},
		{Name: "summary/no-api-hook-json", Args: []string{"summary", "--hook-json", "--agent", "copilot"}, Env: noAPI},
		// 通信断の手の経路は機械生成の summary/down（cases_test.go）。ここは hook 経路が黙ることを見る
		{Name: "summary/down-hook", Args: []string{"summary", "--agent", "claude-code"},
			Routes: []Route{r("GET", pProj+"/summary", Response{Drop: true})}},
		{Name: "summary/not-a-summary", Args: []string{"summary"}, Routes: []Route{r("GET", pProj+"/summary", ok(`{"in_progress":[]}`))}},
		{Name: "summary/not-an-object", Args: []string{"summary"}, Routes: []Route{r("GET", pProj+"/summary", ok(`[]`))}},
		{Name: "summary/hook-json", Args: []string{"summary", "--hook-json", "--agent", "copilot"}, Routes: []Route{
			r("GET", pProj+"/summary", ok(summaryOld)), r("POST", pProj+"/install", ok(installCurrent))}},
		{Name: "summary/install-fails", Args: []string{"summary", "--agent", "codex"}, Routes: []Route{
			r("GET", pProj+"/summary", ok(summaryOld)), r("POST", pProj+"/install", Response{Drop: true})}},

		// ---- installed（core / loop の控えを付けた通知を知らない古いサーバには、控えを外して送り直す）
		{Name: "installed/old-server-retry", Args: []string{"installed", "--agent", "claude-code"},
			Seeds: []Seed{{Path: "ws/.claude/.looptrack-kit.json", Content: `{"core":{"bundle_sha256":"c0ffee"},"loop":{"installed":true,"bundle_sha256":"beef","version":"1.2.0"}}`}},
			Routes: []Route{r("POST", pProj+"/install",
				ErrorResponse(400, "invalid_json", `リクエストの JSON を解釈できません: json: unknown field "core"`), ok(installCurrent))}},

		// ---- config の分岐
		{Name: "config/me-fails", Args: []string{"config"}, Routes: []Route{r("GET", pProj, ok(projectNoRules)),
			r("GET", "/api/v1/me", ErrorResponse(401, "unauthorized", "トークンが無効です"))}},
		{Name: "config/not-member-admin", Args: []string{"config"}, Routes: []Route{
			r("GET", pProj, ok(strings.Replace(projectNoRules, `"role":"viewer"`, `"role":"admin"`, 1))), r("GET", "/api/v1/me", ok(meRes))}},

		// ---- AI のセッションのヘッダ（X-Looptrack-Session・X-Looptrack-Agent）
		{Name: "headers/im-session-id", Args: []string{"comment", "DEMO-0001", "x"}, Env: map[string]string{"LOOPTRACK_SESSION_ID": "sess-explicit"},
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes))}},
		{Name: "headers/claude-code", Args: []string{"show", "DEMO-0001"}, Env: map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-session-1"},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		// 器（デスクトップ版の窓）が CLAUDE_CODE_SESSION_ID を渡さないとき。HOST だけでもセッションを見分けられるように送る
		{Name: "headers/claude-code-host", Args: []string{"show", "DEMO-0001"}, Env: map[string]string{"CLAUDE_CODE_HOST_SESSION_ID": "host-session-1"},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		{Name: "headers/codex", Args: []string{"show", "DEMO-0001"}, Env: map[string]string{"CODEX_THREAD_ID": "codex-thread-1", "CODEX_SESSION_ID": "codex-parent"},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		{Name: "headers/copilot-cli", Args: []string{"show", "DEMO-0001"}, Env: map[string]string{"COPILOT_CLI": "1", "COPILOT_AGENT_SESSION_ID": "copilot-1"},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		{Name: "headers/copilot-vscode", Args: []string{"show", "DEMO-0001"}, Env: map[string]string{"AI_AGENT": "github_copilot_vscode_agent"},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		{Name: "headers/copilot-explicit-session", Args: []string{"show", "DEMO-0001"},
			Env: map[string]string{"LOOPTRACK_SESSION_ID": "sess-explicit", "COPILOT_AGENT": "1"}, Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		// クラウド版: Copilot cloud agent は Copilot CLI と同じ変数に Actions の変数が加わる。Claude Code on the web は CLAUDE_CODE_REMOTE* が加わる
		{Name: "headers/copilot-cloud-agent", Args: []string{"show", "DEMO-0001"},
			Env:    map[string]string{"COPILOT_CLI": "1", "COPILOT_AGENT_SESSION_ID": "cloud-1", "COPILOT_AGENT_ACTION": "task", "CI": "true", "GITHUB_ACTIONS": "true"},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		{Name: "headers/claude-code-web", Args: []string{"show", "DEMO-0001"},
			Env:    map[string]string{"CLAUDE_CODE_SESSION_ID": "cc-web-1", "CLAUDE_CODE_REMOTE": "true", "CLAUDE_CODE_REMOTE_SESSION_ID": "cse_web"},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		{Name: "headers/long-session-id", Args: []string{"show", "DEMO-0001"}, Env: map[string]string{"LOOPTRACK_SESSION_ID": strings.Repeat("s", 200)},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		// ---- 言語のヘッダ（X-Looptrack-Lang）の対照
		// 既定の子の環境は golden の言語を固定するために LOOPTRACK_LANG=ja を渡すので、ほかのすべての
		// ケースの記録には X-Looptrack-Lang: ja が載る。ここはその**対照**で、LOOPTRACK_LANG を外すと
		// **送らない**ことを記録する（Accept-Language は環境変数が無くても en を送るので、あちらでは
		// 「明示したのか既定で en になっただけか」を見分けられない。単体は
		// internal/client/api の TestLangHeaderOnlyWhenExplicit）。
		// この対照が無いと、「送る」側の記録 434 件は「送らない条件が壊れた」ことに気づけない。
		{Name: "headers/lang-unset", Args: []string{"show", "DEMO-0001"}, Env: map[string]string{"LOOPTRACK_LANG": ""},
			Routes: []Route{r("GET", pIssue1, text(markdown1))}},

		// ---- 資格情報（LOOPTRACK_TOKEN なし。保存したトークン・期限前の更新・401 での更新）
		{Name: "credentials/stored-token", Args: []string{"show", "DEMO-0001"}, Env: noToken,
			Seeds: []Seed{creds(`{"token":"imp_stored","login":"alice"}`)}, Routes: []Route{r("GET", pIssue1, text(markdown1))}},
		{Name: "credentials/no-token", Args: []string{"show", "DEMO-0001"}, Env: noToken},
		{Name: "credentials/too-open", Args: []string{"show", "DEMO-0001"}, Env: noToken, PosixPerm: true,
			Seeds: []Seed{{Path: credPath, Content: `{}`, Mode: 0o644}}},
		{Name: "credentials/broken", Args: []string{"show", "DEMO-0001"}, Env: noToken,
			Seeds: []Seed{{Path: credPath, Content: `{"broken`, Mode: 0o600}}},
		{Name: "credentials/refresh-before-expiry", Args: []string{"show", "DEMO-0001"}, Env: noToken,
			Seeds:  []Seed{creds(`{"token":"imp_old","refresh_token":"imr_refresh_1","client_id":"cid_golden","expires_at":1000,"login":"alice"}`)},
			Routes: []Route{r("POST", "/oauth/token", ok(oauthRefreshed)), r("GET", pIssue1, text(markdown1))}},
		{Name: "credentials/refresh-on-401", Args: []string{"show", "DEMO-0001"}, Env: noToken,
			Seeds: []Seed{creds(`{"token":"imp_old","refresh_token":"imr_refresh_1","client_id":"cid_golden","login":"alice"}`)},
			Routes: []Route{r("GET", pIssue1, ErrorResponse(401, "unauthorized", "トークンが失効しているか期限切れです"), text(markdown1)),
				r("POST", "/oauth/token", ok(oauthRefreshed))}},
		{Name: "credentials/refresh-invalid", Args: []string{"show", "DEMO-0001"}, Env: noToken,
			Seeds: []Seed{creds(`{"token":"imp_old","refresh_token":"imr_refresh_1","client_id":"cid_golden","login":"alice"}`)},
			Routes: []Route{r("GET", pIssue1, ErrorResponse(401, "unauthorized", "トークンが失効しているか期限切れです")),
				r("POST", "/oauth/token", status(400, `{"error":"invalid_grant","error_description":"更新トークンが無効です"}`))}},
		{Name: "credentials/refresh-down", Args: []string{"show", "DEMO-0001"}, Env: noToken,
			Seeds: []Seed{creds(`{"token":"imp_old","refresh_token":"imr_refresh_1","client_id":"cid_golden","login":"alice"}`)},
			Routes: []Route{r("GET", pIssue1, ErrorResponse(401, "unauthorized", "トークンが失効しているか期限切れです")),
				r("POST", "/oauth/token", Response{Drop: true})}},

		// ---- login（貼る方式・ブラウザ）
		{Name: "login/keeps-client-id", Args: []string{"login"}, Stdin: "imp_pasted_token\n", Env: noToken,
			Seeds: []Seed{creds(`{"token":"imp_old","client_id":"cid_golden","refresh_token":"imr_x"}`)}, Routes: []Route{r("GET", "/api/v1/me", ok(meRes))}},
		{Name: "login/empty-token", Args: []string{"login"}, Stdin: "\n", Env: noToken},
		{Name: "login/no-url", Args: []string{"login"}, Stdin: "imp_x\n", Env: map[string]string{"LOOPTRACK_TOKEN": "", "LOOPTRACK_API_URL": ""}},
		{Name: "login-browser/ok", Args: []string{"login", "--browser"}, Env: noToken, Browser: true, Routes: []Route{
			r("GET", "/.well-known/oauth-authorization-server", ok(oauthMeta)),
			r("POST", "/oauth/register", created(oauthRegistered)),
			r("GET", "/oauth/authorize", Response{Redirect: OAuthRedirect("code_golden")}),
			r("POST", "/oauth/token", ok(oauthToken)),
			r("GET", "/api/v1/me", ok(meRes))}},
		{Name: "login-browser/reuse-client", Args: []string{"login", "--browser"}, Env: noToken, Browser: true,
			Seeds: []Seed{creds(`{"client_id":"cid_saved"}`)}, Routes: []Route{
				r("GET", "/.well-known/oauth-authorization-server", ok(oauthMeta)),
				r("GET", "/oauth/authorize", Response{Redirect: OAuthRedirect("code_golden")}),
				r("POST", "/oauth/token", ok(oauthToken)),
				r("GET", "/api/v1/me", ok(meRes))}},
		{Name: "login-browser/denied", Args: []string{"login", "--browser"}, Env: noToken, Browser: true, Routes: []Route{
			r("GET", "/.well-known/oauth-authorization-server", ok(oauthMeta)),
			r("POST", "/oauth/register", created(oauthRegistered)),
			r("GET", "/oauth/authorize", Response{Redirect: oauthDenied})}},
		{Name: "login-browser/token-error", Args: []string{"login", "--browser"}, Env: noToken, Browser: true, Routes: []Route{
			r("GET", "/.well-known/oauth-authorization-server", ok(oauthMeta)),
			r("POST", "/oauth/register", created(oauthRegistered)),
			r("GET", "/oauth/authorize", Response{Redirect: OAuthRedirect("code_golden")}),
			r("POST", "/oauth/token", status(400, `{"error":"invalid_grant","error_description":"認可コードが無効です"}`))}},
		{Name: "login-browser/old-server", Args: []string{"login", "--browser"}, Env: noToken,
			Routes: []Route{r("GET", "/.well-known/oauth-authorization-server", ok(oauthMetaOld))}},
		{Name: "login-browser/register-fails", Args: []string{"login", "--browser"}, Env: noToken, Routes: []Route{
			r("GET", "/.well-known/oauth-authorization-server", ok(oauthMeta)),
			r("POST", "/oauth/register", status(400, `{"error":"invalid_client_metadata","error_description":"redirect_uris が不正です"}`))}},
		{Name: "login-browser/down", Args: []string{"login", "--browser"}, Env: noToken,
			Routes: []Route{r("GET", "/.well-known/oauth-authorization-server", Response{Drop: true})}},
	}
	return cases
}
