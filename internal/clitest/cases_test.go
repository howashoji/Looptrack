package clitest

import (
	"net/http"
	"sort"
)

// ケースの一覧。spec（サブコマンドの正常系）から、エラーの版（404・409・422・401・通信断）と --json の版を機械的に作り、
// それで表しにくいもの（再試行・作業コピー・資格情報・ログイン・ヘッダ）は special に個別に書く。

func r(method, path string, res ...Response) Route {
	return Route{Method: method, Path: path, Responses: res}
}
func rq(method, path string, query map[string]string, res ...Response) Route {
	return Route{Method: method, Path: path, Query: query, Responses: res}
}
func ok(body string) Response      { return Response{Body: body} }
func created(body string) Response { return Response{Status: http.StatusCreated, Body: body} }
func text(body string) Response {
	return Response{Body: body, ContentType: "text/markdown; charset=utf-8"}
}
func status(code int, body string) Response { return Response{Status: code, Body: body} }

const (
	pIssues = "/api/v1/projects/demo/issues"
	pIssue1 = "/api/v1/issues/DEMO-0001"
	pIssue2 = "/api/v1/issues/DEMO-0002"
	pProj   = "/api/v1/projects/demo"
)

// errorKinds はどの spec にも当てるエラーの版。
var errorKinds = []struct {
	name string
	res  Response
}{
	{"404", ErrorResponse(404, "not_found", "イシューが見つかりません: DEMO-0999")},
	{"409", ErrorResponse(409, "status_changed", "DEMO-0001 は他の操作で Done になっています")},
	{"422", Response{Status: 422, Body: `{"error":{"code":"rule_violation","message":"DEMO-0001 は検証コマンドを持っていますが、verify の記録がありません。次を実行してから閉じてください: looptrack issue verify DEMO-0001","rule":"verify_required_on_close","overridable":true}}`}},
	{"401", ErrorResponse(401, "unauthorized", "トークンが無効です")},
	{"down", Response{Drop: true}},
}

// spec はサブコマンドの正常系 1 つ。
type spec struct {
	name    string
	args    []string
	json    bool // --json の版も作る
	routes  []Route
	primary int                 // エラーの版で差し替える Route（既定 0 = 最初の要求）
	errs    map[string]Response // エラーの版の差し替え（409 の current など）
	env     map[string]string
	stdin   string
	seeds   []Seed
}

var workSeeds = []Seed{
	{Path: "ws/.claude/.looptrack-work/DEMO-0001.md", Content: markdown1 + "\n追記した行。\n"},
	{Path: "ws/.claude/.looptrack-work/DEMO-0001.base.md", Content: markdown1},
	{Path: "ws/.claude/.looptrack-work/DEMO-0001.json", Content: `{"id": "DEMO-0001", "project": "demo", "version": 3}`},
}

func specs() []spec {
	return []spec{
		{name: "list", args: []string{"list"}, json: true, routes: []Route{r("GET", pIssues, ok(list(item1, item2, item3)))}},
		{name: "list-filter", args: []string{"list", "--status", "Todo", "--type", "bug", "--label", "ui", "--ref", "FR-1", "--all", "--assignee", "me", "--sort", "updated", "--reverse"},
			json: true, routes: []Route{r("GET", pIssues, ok(list(item2)))}},
		{name: "ready", args: []string{"ready"}, json: true, routes: []Route{r("GET", pProj+"/ready", ok(list(item2)))}},
		{name: "show", args: []string{"show", "DEMO-0001"}, json: true, routes: []Route{
			rq("GET", pIssue1, map[string]string{"format": "md"}, text(markdown1)),
			r("GET", pIssue1, ok(detail1))}},
		{name: "new", args: []string{"new", "新しい不具合", "--type", "bug", "--priority", "P1", "--labels", "ui,api", "--parent", "DEMO-0003",
			"--blocked-by", "DEMO-0001 DEMO-0002", "--traces", "DEMO-0003", "--refs", "FR-1,UC-2", "--body", "再現手順:\n1. 保存する", "--assignee", "me"},
			routes: []Route{r("POST", pIssues, created(createRes))}},
		{name: "new-minimal", args: []string{"new", "最小の起票"}, routes: []Route{r("POST", pIssues, created(createRes))}},
		{name: "comment", args: []string{"comment", "DEMO-0001", "原因が分かった。\n設定の読み込み順。"}, routes: []Route{r("POST", pIssue1+"/comments", created(commentRes))}},
		{name: "status", args: []string{"status", "DEMO-0002", "In Progress", "--comment", "着手", "--override", "急ぎ", "--assignee", "me"},
			routes: []Route{r("POST", pIssue2+"/status", ok(statusRes))}},
		{name: "close", args: []string{"close", "DEMO-0001", "--comment", "検証: go test が通った"}, routes: []Route{r("POST", pIssue1+"/status", ok(closeRes))}},
		{name: "assign", args: []string{"assign", "DEMO-0001", "bob", "--override", "引き継ぎ"}, routes: []Route{r("POST", pIssue1+"/assign", ok(assignRes))}},
		{name: "index", args: []string{"index"}, routes: []Route{r("GET", pIssues, ok(list(item1, item2, item3, item4)))}},
		{name: "matrix", args: []string{"matrix"}, routes: []Route{r("GET", pProj+"/matrix", text(matrixMD))}},
		{name: "edit", args: []string{"edit", "DEMO-0001"}, routes: []Route{r("GET", pIssue1, ok(detail1))}},
		{name: "push", args: []string{"push", "DEMO-0001"}, seeds: workSeeds, routes: []Route{r("PATCH", pIssue1, ok(pushRes))},
			errs: map[string]Response{"409": pushConflict}},
		{name: "activity", args: []string{"activity", "DEMO-0001", "DEMO-0002", "--since", "1714600000.25"}, json: true,
			routes: []Route{r("GET", "/api/v1/activity", ok(activityRes))}},
		{name: "verify", args: []string{"verify", "DEMO-0001"}, json: true, routes: []Route{
			r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok", `printf '出力\n2 行目\n'`}, ""))),
			r("POST", pIssue1+"/verify", created(verifyPosted))}},
		{name: "verify-list", args: []string{"verify", "DEMO-0001", "--list"}, json: true,
			routes: []Route{r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok", "make test"}, verifyLast)))}},
		{name: "verify-last", args: []string{"verify", "DEMO-0001", "--last"}, json: true,
			routes: []Route{r("GET", pIssue1+"/verify", ok(verifyPlan([]string{"echo ok", "make test"}, verifyLast)))}},
		{name: "summary", args: []string{"summary"}, json: true, routes: []Route{r("GET", pProj+"/summary", ok(summaryRes))}},
		{name: "summary-agent", args: []string{"summary", "--agent", "claude-code", "--limit", "3"}, json: true, routes: []Route{
			r("GET", pProj+"/summary", ok(summaryRes)), r("POST", pProj+"/install", ok(installRes))}},
		{name: "installed", args: []string{"installed", "--agent", "codex"}, json: true, routes: []Route{r("POST", pProj+"/install", ok(installCurrent))}},
		{name: "config", args: []string{"config"}, json: true, routes: []Route{r("GET", pProj, ok(projectRes)), r("GET", "/api/v1/me", ok(meRes))}},
		{name: "guide", args: []string{"guide"}, json: true, routes: []Route{
			rq("GET", pProj+"/guide", map[string]string{"format": "md"}, text(guideMD)), r("GET", pProj+"/guide", ok(guideJSON))}},
		{name: "next", args: []string{"next", "--type", "bug,task", "--comment", "着手します", "--assignee", "me", "--override", "理由"}, json: true,
			routes: []Route{r("POST", pProj+"/next", ok(nextStarted))}},
		{name: "next-dry-run", args: []string{"next", "--dry-run"}, json: true, routes: []Route{r("POST", pProj+"/next", ok(nextDry))}},
		{name: "usage-show", args: []string{"usage", "show", "DEMO-0001"}, json: true, routes: []Route{r("GET", pIssue1+"/usage", ok(usageShow))}},
		{name: "usage-report", args: []string{"usage", "report", "--since-last"}, json: true, routes: []Route{
			rq("GET", pProj+"/usage/report", map[string]string{"format": "md"}, text(reportMD)), r("GET", pProj+"/usage/report", ok(reportJSON))}},
		{name: "usage-report-period", args: []string{"usage", "report", "--from", "2024-04-01", "--to", "2024-04-30 09:30"}, json: true, routes: []Route{
			rq("GET", pProj+"/usage/report", map[string]string{"format": "md"}, text(reportMD)), r("GET", pProj+"/usage/report", ok(reportJSON))}},
		{name: "usage-report-xlsx", args: []string{"usage", "report", "--request", "7", "--xlsx", "report.xlsx"},
			routes: []Route{r("GET", pProj+"/usage/report.xlsx", xlsxResponse("12"))}},
		{name: "usage-ledger-list", args: []string{"usage", "ledger", "list"}, json: true, routes: []Route{r("GET", pProj+"/usage/ledger", ok(ledgerList))}},
		{name: "usage-ledger-add", args: []string{"usage", "ledger", "add", "2024-05 月次", "--from", "2024-05-01", "--to", "2024-06-01", "--data-end", "2024-05-31 21:00",
			"--excluded", "c-1, c-2", "--total-tokens", "4567", "--note", "reports/2024-05.pdf", "--created-at", "2024-06-01 09:00", "--request-id", "7"},
			json: true, routes: []Route{r("POST", pProj+"/usage/ledger", created(ledgerAdded))}},
		{name: "usage-ledger-add-from-report", args: []string{"usage", "ledger", "add", "2024-04 月次", "--from-report", "report.json"},
			seeds: []Seed{{Path: "ws/report.json", Content: reportJSON}}, routes: []Route{r("POST", pProj+"/usage/ledger", created(ledgerAdded))}},
		{name: "usage-missing", args: []string{"usage", "missing"}, json: true, routes: []Route{r("GET", pProj+"/usage/coverage", ok(coverageRes))}},
		{name: "usage-requests", args: []string{"usage", "requests", "--all"}, json: true, routes: []Route{r("GET", pProj+"/usage/requests", ok(requestsRes))}},
		{name: "export", args: []string{"export", "--xlsx", "out/issues.xlsx", "--status", "Todo", "--all"},
			seeds: []Seed{{Path: "ws/out/.keep", Content: ""}}, routes: []Route{r("GET", pIssues+".xlsx", xlsxResponse("3"))}},
		{name: "login", args: []string{"login"}, stdin: "imp_pasted_token\n", env: map[string]string{"LOOPTRACK_TOKEN": ""},
			routes: []Route{r("GET", "/api/v1/me", ok(meRes))}},
	}
}

// generated は spec から正常・エラー × --json の有無のケースを作る。
func generated() []Case {
	var out []Case
	for _, s := range specs() {
		variants := []struct {
			name   string
			routes []Route
		}{{"ok", s.routes}}
		for _, k := range errorKinds {
			res := k.res
			if e, ok := s.errs[k.name]; ok {
				res = e
			}
			rs := append([]Route(nil), s.routes...)
			rs[s.primary] = Route{Method: rs[s.primary].Method, Path: rs[s.primary].Path, Responses: []Response{res}}
			variants = append(variants, struct {
				name   string
				routes []Route
			}{k.name, rs})
		}
		for _, v := range variants {
			c := Case{Name: s.name + "/" + v.name, Args: s.args, Env: s.env, Stdin: s.stdin, Routes: v.routes, Seeds: s.seeds}
			out = append(out, c)
			if s.json {
				j := c
				j.Name += "-json"
				j.Args = append(append([]string(nil), s.args...), "--json")
				out = append(out, j)
			}
		}
	}
	return out
}

func allCases() []Case {
	out := append(append(append(generated(), special()...), initCases()...), usageCases()...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
