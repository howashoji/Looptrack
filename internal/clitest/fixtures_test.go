package clitest

import (
	"encoding/json"
	"fmt"
	"strings"
)

// フィクスチャ: サーバ（internal/server）の応答の形を手で写したもの。プロジェクト demo（接頭辞 DEMO・幅 4）。
// 時刻はすべて 2024 年の固定値（正規化の $NOW に当たらないよう、実行時刻から離しておく）。

// item は一覧の 1 件（issueJSON と同じキーの順）。extra は末尾に足すキー（"assignee":"alice" など）。
func item(id, typ, status, prio, title string, blocked []string, extra string) string {
	b, _ := json.Marshal(blocked)
	if blocked == nil {
		b = []byte("[]")
	}
	closed := status == "Done" || status == "Canceled"
	s := fmt.Sprintf(`{"id":%q,"project":"demo","title":%q,"type":%q,"status":%q,"priority":%q,"parent":"","labels":[],`+
		`"blocked_by":%s,"traces":[],"refs":[],"created":"2024-05-01 10:00","updated":"2024-05-02 11:30","closed":%t,"version":3,`+
		`"file_name":"%s-x.md"`, id, title, typ, status, prio, b, closed, id)
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

var (
	item1 = item("DEMO-0001", "task", "In Progress", "P1", "ログイン画面を作る", nil, `"assignee":"alice"`)
	item2 = item("DEMO-0002", "bug", "Todo", "P0", "保存でエラーになる", []string{"DEMO-0001"}, "")
	item3 = item("DEMO-0003", "requirement", "In Review", "P2", "認証の要件", nil, `"assignee":"bob","assignee_inactive":true,"feedback_pending":2`)
	item4 = item("DEMO-0004", "test", "Done", "P3", "ログインの結合テスト", nil, "")
)

func list(items ...string) string {
	return fmt.Sprintf(`{"count":%d,"items":[%s]}`, len(items), strings.Join(items, ","))
}

const markdown1 = `---
id: DEMO-0001
title: ログイン画面を作る
type: task
status: In Progress
assignee: alice
priority: P1
labels: []
parent: ""
blocked_by: []
traces: []
refs: []
created: 2024-05-01 10:00
updated: 2024-05-02 11:30
---

# DEMO-0001 ログイン画面を作る

## 内容

ログイン画面を作る。

## 受け入れ条件

- [ ] ID とパスワードでログインできる

## コメント

### 2024-05-02 11:30

着手。
`

// detail は 1 件の全文（issueDetailJSON）。
func detail(it, md string) string {
	m, _ := json.Marshal(md)
	return strings.TrimSuffix(it, "}") + `,"body":"## 内容\n\nログイン画面を作る。\n","preamble":null,` +
		`"comments":[{"seq":1,"ts":"2024-05-02 11:30","content":"着手。"}],"markdown":` + string(m) + "}"
}

var detail1 = detail(item1, markdown1)

// 変更系の応答
var (
	createRes  = `{"issue":` + detail(item("DEMO-0005", "bug", "Todo", "P1", "新しい不具合", []string{"DEMO-0001", "DEMO-0002"}, ""), markdown1) + `,"path":"open/DEMO-0005-x.md"}`
	commentRes = `{"issue":` + item1 + `,"message":"コメント追記: DEMO-0001","seq":2}`
	statusRes  = `{"from":"Todo","issue":` + item("DEMO-0002", "bug", "In Progress", "P0", "保存でエラーになる", nil, `"assignee":"alice"`) +
		`,"messages":["DEMO-0002: Todo → In Progress","担当: alice"],"to":"In Progress"}`
	closeRes = `{"from":"In Progress","issue":` + item("DEMO-0001", "task", "Done", "P1", "ログイン画面を作る", nil, `"assignee":"alice"`) +
		`,"messages":["DEMO-0001: In Progress → Done","コメント追記: DEMO-0001","要件 DEMO-0003 の下位がすべて完了しました。受け入れ条件を検証して close してください"],"to":"Done"}`
	assignRes = `{"changed":true,"from":"alice","issue":` + item1 + `,"message":"DEMO-0001 の担当: alice → bob","to":"bob"}`
	pushRes   = strings.Replace(detail1, `"version":3`, `"version":4`, 1)
)

// 409（版の競合）で返る current（サーバの最新）
var conflictCurrent = strings.Replace(strings.Replace(detail1, `"version":3`, `"version":4`, 1),
	`ID とパスワードでログインできる`, `ID とパスワードでログインできる\n- [ ] ログアウトできる`, 1)

var pushConflict = Response{Status: 409, Body: `{"error":{"code":"version_conflict","message":"DEMO-0001 は他で更新されています（指定した版 3、現在の版 4）。最新の内容を取得し、変更を当て直してください","current":` + conflictCurrent + `}}`}

const matrixMD = "# トレーサビリティ表\n\n| 要件 | 設計 | 実装 | テスト |\n| -- | -- | -- | -- |\n| DEMO-0003 認証の要件 | - | DEMO-0001 | DEMO-0004 |\n"

const activityRes = `{"items":[{"id":"DEMO-0001","project":"demo","title":"ログイン画面を作る","status":"In Progress","closed":false,` +
	`"last_at":"2024-05-02T02:30:00Z","last_epoch":1714617000,"last_kind":"comment","last_via":"cli","events_since":2},` +
	`{"id":"DEMO-0002","project":"demo","title":"保存でエラーになる","status":"Todo","closed":false,` +
	`"last_at":"2024-05-01T01:00:00Z","last_epoch":1714525200,"last_kind":"create","last_via":"mcp","events_since":0}],"now_epoch":1714700000.5}`

// verify の計画（GET /issues/{id}/verify）
func verifyPlan(commands []string, last string) string {
	c, _ := json.Marshal(commands)
	if last == "" {
		last = "null"
	}
	return `{"id":"DEMO-0001","body_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","commands":` + string(c) +
		`,"text":"DEMO-0001 の検証コマンド（2 件）\n1. echo ok\n2. printf 'a\\nb\\n'\n直近の verify: 記録なし","current":true,"last":` + last + `}`
}

const verifyLast = `{"at":"2024-05-02T02:40:00Z","passed":1,"failed":1,"via":"cli","host":"build-01","workspace":"demo-app","self_reported":false,` +
	`"results":[{"command":"echo ok","status":"ok","exit_code":0,"duration_ms":12,"output_tail":"ok\n"},` +
	`{"command":"make test","status":"fail","exit_code":2,"duration_ms":3400,"output_tail":"FAIL: TestLogin\nmake: *** [test] Error 2\n"}]}`

const verifyPosted = `{"issue":` + `{"id":"DEMO-0001"}` + `,"seq":2,"ok":true,"passed":2,"failed":0,"message":"verify を記録: DEMO-0001（2/2 成功）"}`

// summary（GET /projects/{slug}/summary）
var summaryRes = `{"counts":{"open":3,"open_bugs":1,"ready":1,"in_progress":1,"in_review":1},` +
	`"feedback":{"count":2,"issue_count":1,"issues":[{"id":"DEMO-0003","first_at":"2024-05-02T01:00:00Z","pending":2,"excerpt":"ボタンが小さい"}]},` +
	`"in_progress":[` + item1 + `],` +
	`"in_review":[` + strings.TrimSuffix(item3, "}") + `,"review_age":"3 日","review_stale":true,"verify_self_reported":true}],` +
	`"ready":[` + item2 + `],"ready_total":1,` +
	`"requirements_ready":[{"requirement":{"id":"DEMO-0003","title":"認証の要件"},"done":2,"canceled":1,"command":"looptrack issue close DEMO-0003"}],"requirements_ready_total":1,` +
	`"usage_missing":{"count":1,"issues":["DEMO-0001"],"days":7,"command":"looptrack issue usage attach DEMO-0001","message":"トークン情報の未付与 1 件（DEMO-0001）。looptrack issue usage attach DEMO-0001 で付けてください"},` +
	`"usage_requests":[{"id":7,"created_at":"2024-05-03T00:00:00Z","requested_by":"carol","period":"2024-04-01〜2024-04-30","target":"demo","note":"月次\n報告","command":"looptrack issue usage report --request 7 --json"}]}`

// 3 層の要約に対応する前のサーバ（feedback が無い）
var summaryOld = `{"counts":{"open":2,"open_bugs":0},"in_progress":[],"in_review":[` + item3 + `],"ready":[` + item2 + `],"ready_total":5}`

const installRes = `{"state":"outdated","message":"【配布スクリプトの更新】looptrack が古い版です。looptrack issue init で更新してください"}`
const installCurrent = `{"state":"current","message":"導入済み（最新）"}`

const projectRes = `{"slug":"demo","name":"デモ","prefix":"DEMO","width":4,"role":"editor","member":true,"counter":5,"rules":{"verify":{"require_on_close":true}},` +
	`"counts":{"total":4,"closed":1,"updated":"2024-05-02 11:30","open":3,"in_progress":1,"in_review":1,"ready":1,"open_bugs":1,"by_status":{"Todo":1}}}`
const projectNoRules = `{"slug":"demo","name":"デモ","prefix":"DEMO","width":4,"role":"viewer","member":false,"counter":5,"rules":null}`
const meRes = `{"login":"alice","name":"Alice","role":"admin"}`

const guideMD = "# 使い方とルール\n\n## 共通規則\n\n- 作業はイシューから始める\n\n## プロジェクト別ルール（demo）\n\n- verify: 有効\n"
const guideJSON = `{"project":"demo","common":"作業はイシューから始める","rules":{"verify":{"require_on_close":true}},"docs":[]}`

var nextStarted = `{"action":"started","issue":` + item2 + `,"text":"着手: DEMO-0002 保存でエラーになる（Todo → In Progress）\n\n## 内容\n\n保存でエラーになる。\n\n## 受け入れ条件\n\n- [ ] 保存できる\n"}`
var nextDry = `{"action":"dry_run","issue":` + item2 + `,"text":"次に着手するもの（--dry-run。状態は変えていません）: DEMO-0002 保存でエラーになる"}`

const usageShow = `{"issue":"DEMO-0001","total_tokens":123456,"stage_count":2,"inconsistent":1,` +
	`"excluded_total":{"main":{"input":10,"cache_create":0,"cache_read":0,"output":5},"sub":{"input":0,"cache_create":0,"cache_read":0,"output":0}},` +
	`"stages":[` +
	`{"id":12,"at":"2024-05-02T03:00:00.5Z","trigger":"issue_op","op":"status","client":"codex","excluded":false,"inconsistent":true,"delta_total":23456,` +
	`"delta":{"main":{"input":20000,"cache_create":0,"cache_read":3000,"output":456},"sub":{"input":0,"cache_create":0,"cache_read":0,"output":0}}},` +
	`{"id":11,"at":"2024-05-02T02:30:00Z","trigger":"manual","op":null,"client":"claude-code","excluded":true,"inconsistent":false,"delta_total":100000,` +
	`"delta":{"main":{"input":50000,"cache_create":10000,"cache_read":30000,"output":5000},"sub":{"input":4000,"cache_create":0,"cache_read":0,"output":1000}}}]}`

const reportMD = "# トークン消費レポート（demo）\n\n期間: 2024-04-01 〜 2024-05-01\n\n| イシュー | トークン |\n| -- | --: |\n| DEMO-0001 | 123,456 |\n\n"
const reportJSON = `{"project":"demo","from":"2024-04-01T00:00:00Z","to":"2024-05-01T00:00:00Z","data_end":"2024-04-30T12:00:00Z",` +
	`"excluded_conversations":["c-1"],"total_tokens":123456,"request":{"id":7},"issues":[{"id":"DEMO-0001","tokens":123456}]}`

const ledgerList = `{"count":2,"next_from":"2024-04-30T12:00:00Z","items":[` +
	`{"id":2,"from":"2024-04-01T00:00:00Z","data_end":"2024-04-30T12:00:00Z","created_at":"2024-05-01T01:00:00Z","total_tokens":123456,"name":"2024-04 月次"},` +
	`{"id":1,"from":null,"data_end":"2024-03-31T15:00:00Z","created_at":"2024-04-01T01:00:00Z","total_tokens":null,"name":"初回"}]}`
const ledgerAdded = `{"id":3,"message":"台帳に登録: #3 2024-05 月次（データ終端 2024-05-31 21:00）"}`

const coverageRes = `{"rate":0.75,"days":30,"mine":true,"target":4,"attached":3,"missing_count":1,"humans":2,` +
	`"missing":[{"at":"2024-05-02T03:00:00Z","issue":"DEMO-0002","kind":"status","via":"mcp","user":"alice","title":"保存でエラーになる"}],"issues":["DEMO-0002"]}`
const coverageNone = `{"rate":null,"days":7,"mine":false,"target":0,"attached":0,"missing_count":0,"humans":0,"missing":[],"issues":[]}`

const requestsRes = `{"items":[` +
	`{"id":6,"created_at":"2024-04-01T00:00:00Z","done":true,"report":{"id":2,"name":"2024-04 月次"},"requested_by":"carol","period":"2024-03","target":"","note":"","command":""},` +
	`{"id":7,"created_at":"2024-05-03T00:00:00Z","done":false,"report":null,"requested_by":"carol","period":"2024-04-01〜2024-04-30","target":"demo","note":"月次\n報告","command":"looptrack issue usage report --request 7 --json"}]}`

// xlsx の代わりの任意のバイト列（CLI は中身を見ずに保存する）
const xlsxBytes = "PK\x03\x04\x00\x00fake-xlsx\x00\x01\x02"

func xlsxResponse(rows string) Response {
	return Response{ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Header: map[string]string{"X-Looptrack-Rows": rows}, Body: xlsxBytes}
}

// OAuth（login --browser と更新トークン）
const oauthMeta = `{"issuer":"http://example.invalid/im","authorization_endpoint":"x","token_endpoint":"x","grant_types_supported":["authorization_code","refresh_token"]}`
const oauthMetaOld = `{"issuer":"http://example.invalid/im","grant_types_supported":["authorization_code"]}`
const oauthRegistered = `{"client_id":"cid_golden","client_name":"looptrack"}`
const oauthToken = `{"access_token":"imp_from_browser","token_type":"Bearer","expires_in":3600,"refresh_token":"imr_refresh_1"}`
const oauthRefreshed = `{"access_token":"imp_refreshed","token_type":"Bearer","expires_in":7200,"refresh_token":"imr_refresh_2"}`
