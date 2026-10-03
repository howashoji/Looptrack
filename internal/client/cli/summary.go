package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// SessionStart 用の要約（DESIGN.md §9-3-7）。

// summaryTimeout は SessionStart を止めないための打ち切り（1 要求 2 秒）。
const summaryTimeout = 2 * time.Second

// cmdSummary は SessionStart の要約を出す。
func cmdSummary(c *Ctx, v *Values) error {
	if v.Str("agent") == "claude-code" && hookio.ForeignHost(c.Env.Get, nil) {
		return nil // Copilot は .claude/settings.json の SessionStart も実行する。導入済みの通知も案内も出さない（fail-open）
	}
	if !v.Bool("hook_json") {
		return c.summaryBody(v)
	}
	// hook の出力を JSON の additionalContext に包む（GitHub Copilot の hook は標準出力を JSON として読む）。
	// Copilot CLI はトップレベルの additionalContext、VS Code は hookSpecificOutput の中を読むので両方に置く
	var buf bytes.Buffer
	sub := *c
	sub.Stdout = &buf
	if err := sub.summaryBody(v); err != nil {
		return err // 失敗したときは包んだ出力を捨てる
	}
	if text := strings.TrimSpace(buf.String()); text != "" {
		c.Println(jsonorder.Compact(jsonorder.NewObject().Set("additionalContext", text).
			Set("hookSpecificOutput", jsonorder.NewObject().Set("hookEventName", "SessionStart").Set("additionalContext", text))))
	}
	return nil
}

// summaryBody は要約の本体を出す（API モード）。
//
// 出せない理由（サーバの設定が無い・トークンが無い・届かない・応答が要約の形でない）の見せ方は、呼ばれ方で分ける:
//
//   - hook 経由（--agent か --hook-json が付く。付けるのは配線だけ。91 行目の「フックとして動いている証拠」と同じ見方）は、
//     SessionStart を止めないよう何も出さずに 0 で終える（fail-open。1 要求 2 秒で打ち切る）。
//   - 人・AI が手で `looptrack issue summary` を打ったときは、list / ready / show と同じ案内を出して非 0 で終える。
//     無出力・終了コード 0 は「該当なし」と区別がつかず、実際に「イシューは無い」と誤読された。
//
// `looptrack hook summary`（いまの配線）はここが非 0 で終えても止まらない（CLI の終了コードが 0 でなければ
// 空の結果にする。internal/client/hook/core/summary.go）。--hook-json は、それを通さず直接呼ぶ古い配線のための保険。
func (c *Ctx) summaryBody(v *Values) error {
	quiet := v.Bool("hook_json") || v.Str("agent") != ""
	// fail は hook 経由なら黙って 0 で終え、手で打ったときは理由を出して非 0 で終える。
	fail := func(err error) error {
		if quiet {
			return nil
		}
		return err
	}
	cl, err := api.NewWithTimeout(c.Env, summaryTimeout)
	if err != nil {
		return err
	}
	c.useServerLang(cl)
	if cl.BaseURL == "" {
		return fail(c.noAPI()) // サーバの設定が無い（list / ready / show と同じ案内）
	}
	tok, _, err := cl.TokenSource(cl.BaseURL)
	if err != nil {
		return err // 資格情報の権限・形式の誤り（以前の CLI と同じく握りつぶさない）
	}
	if tok == "" && !cl.LocalMode(cl.BaseURL) { // ローカルモードのサーバはトークンなしで呼べる
		return fail(&api.NoTokenError{URL: cl.BaseURL}) // 設定はある（ログインしていない。show と同じ案内）
	}
	path, err := c.ProjectPath(fmt.Sprintf("/summary?limit=%d", v.Int("limit")))
	if err != nil {
		return err
	}
	raw, err := cl.Get(path)
	if err != nil {
		return fail(err) // 設定はあるが届かない・時間切れ・認証切れ（文面は api が持つ）
	}
	res, ok := raw.(*jsonorder.Object)
	if !ok {
		return fail(i18n.Errorf("cli.summary.err.bad_response", "url", cl.BaseURL))
	}
	data := jsonorder.NewObject()
	for _, k := range []string{"in_progress", "in_review", "ready", "ready_total", "counts"} {
		x, ok := res.Get(k)
		if !ok {
			return fail(i18n.Errorf("cli.err.missing_key", "key", k)) // 古い・別物のサーバ（hook 経由では何も出さない）
		}
		data.Set(k, x)
	}
	data.Set("usage_requests", orList(res, "usage_requests"))
	if x, ok := res.Get("timezone"); ok { // 時刻を描く時間帯（timezone を載せる前のサーバには無い）
		data.Set("timezone", x)
	}
	if x, ok := res.Get("feedback"); ok { // 3 層の要約に対応したサーバ
		data.Set("feedback", x)
	}
	if truthy(res, "requirements_ready_total") { // 下位がすべて完了した要件
		data.Set("requirements_ready", orList(res, "requirements_ready"))
		data.Set("requirements_ready_total", get(res, "requirements_ready_total", nil))
	}
	if truthy(res, "usage_missing") {
		data.Set("usage_missing", get(res, "usage_missing", nil))
	}
	// 上の ProjectPath で解決できている
	slug, _ := c.Project()
	if local := localUsageFailure(c.Env, slug); local != nil { // この PC のフックの送信の失敗（サーバには届いていない）
		data.Set("usage_send_failure", local)
	}
	if agent := v.Str("agent"); agent != "" {
		// SessionStart のフックとして動いている = フックが承認されて動いている証拠。導入済みをサーバへ知らせる
		if inst, err := c.reportInstall(cl, agent, "hook"); err == nil {
			data.Set("install", inst)
		}
	}
	if v.Bool("json") {
		c.PrintJSON(data)
		return nil
	}
	return c.printSummary(data, v.Int("limit"))
}

func orList(o *jsonorder.Object, key string) any {
	if x, _ := o.Get(key); jsonorder.Truthy(x) {
		return x
	}
	return []any{}
}

func (c *Ctx) printSummary(data *jsonorder.Object, limit int64) error {
	loc, _ := serverTZ(c.Lang, data) // 時刻は応答の timezone（サーバのローカル時刻）で描く
	readyTotal := intOf(get(data, "ready_total", nil))
	readyTitle := i18n.T(c.Lang, "cli.summary.ready_title", "shown", min(limit, readyTotal), "total", readyTotal)
	type section struct {
		title string
		rows  any
	}
	var sections []section
	_, layered := data.Get("feedback")
	inProgress := i18n.T(c.Lang, "cli.summary.in_progress")
	if layered {
		c.Println(i18n.T(c.Lang, "cli.summary.layer.work"))
		sections = []section{{inProgress, get(data, "in_progress", nil)}, {readyTitle, get(data, "ready", nil)}}
	} else {
		sections = []section{{inProgress, get(data, "in_progress", nil)},
			{i18n.T(c.Lang, "cli.summary.in_review"), get(data, "in_review", nil)}, {readyTitle, get(data, "ready", nil)}}
	}
	for _, s := range sections {
		c.Println("── " + s.title + " ──")
		if jsonorder.Truthy(s.rows) {
			c.printRows(objects(s.rows), "priority")
			if note := otherSessionNote(c.Lang, objects(s.rows)); note != "" {
				c.Println(note)
			}
			if note := crossPathNote(c.Lang, objects(s.rows)); note != "" {
				c.Println(note)
			}
		} else {
			c.Println(i18n.T(c.Lang, "cli.none"))
		}
		c.Println("")
	}
	if truthy(data, "requirements_ready_total") {
		c.Println(closableSection(c.Lang, objects(get(data, "requirements_ready", nil)), intOf(get(data, "requirements_ready_total", nil))))
	}
	if layered {
		c.Println(reviewLayer(c.Lang, objects(get(data, "in_review", nil))))
		c.Println(feedbackLayer(c.Lang, asObject(get(data, "feedback", nil)), loc))
	}
	counts := asObject(get(data, "counts", nil))
	open, err := must(counts, "open")
	if err != nil {
		return err
	}
	bugs, err := must(counts, "open_bugs")
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.summary.counts", "open", intOf(open), "bugs", intOf(bugs)))
	// 前のセッションで付けそこねたトークン情報を、このセッションで回収させる
	missing := asObject(get(data, "usage_missing", nil))
	if truthy(missing, "count") {
		if truthy(missing, "message") {
			c.Println(getStr(missing, "message", ""))
		} else {
			c.Println(i18n.T(c.Lang, "cli.summary.usage_missing", "count", intOf(get(missing, "count", nil))))
		}
	}
	if line := usageFailureText(c.Lang, asObject(get(data, "usage_send_failure", nil)), loc); line != "" {
		c.Println(line)
	}
	if err := c.printUsageRequests(objects(get(data, "usage_requests", nil)), loc); err != nil {
		return err
	}
	if line := pathMissingNote(c.Lang, c.Root); line != "" {
		c.Println("\n" + line)
	}
	// 導入状態。導入済みで最新なら何も出さない
	install := asObject(get(data, "install", nil))
	if truthy(data, "install") && getStr(install, "state", "") != "current" {
		c.Println("\n" + getStr(install, "message", ""))
	}
	return nil
}

// closableSection は ① の末尾: 下位がすべて完了した開いている要件（サーバの closableSummaryText と同じ文言）。
func closableSection(lang i18n.Lang, items []*jsonorder.Object, total int64) string {
	lines := []string{i18n.T(lang, "cli.summary.closable.heading", "total", total)}
	for _, it := range items {
		r := asObject(get(it, "requirement", nil))
		breakdown := i18n.T(lang, "cli.summary.closable.done", "count", intOf(get(it, "done", int64(0))))
		if truthy(it, "canceled") {
			breakdown += i18n.T(lang, "cli.summary.closable.canceled", "count", intOf(get(it, "canceled", nil)))
		}
		lines = append(lines, fmt.Sprintf("%-9s %s（%s）→ %s", getStr(r, "id", "?"), getStr(r, "title", ""), breakdown, getStr(it, "command", "")))
	}
	if rest := total - int64(len(items)); rest > 0 {
		lines = append(lines, i18n.T(lang, "cli.summary.closable.more", "rest", rest))
	}
	return strings.Join(lines, "\n") + "\n"
}

// reviewLayer は ② 人の判断待ち（In Review と滞留。48 時間超に [48h超]）。
func reviewLayer(lang i18n.Lang, items []*jsonorder.Object) string {
	stale := 0
	for _, it := range items {
		if truthy(it, "review_stale") {
			stale++
		}
	}
	lines := []string{i18n.T(lang, "cli.summary.review.heading", "count", len(items), "stale", stale)}
	if len(items) == 0 {
		lines = append(lines, i18n.T(lang, "cli.none"))
	}
	for _, it := range items {
		mark := ""
		if truthy(it, "review_stale") {
			mark = i18n.T(lang, "cli.summary.review.stale")
		}
		line := fmt.Sprintf("%-9s %-11s %-11s %-3s %-10s %-7s %s", getStr(it, "id", "?"), getStr(it, "type", "?"), getStr(it, "status", "?"),
			getStr(it, "priority", "?"), orStr(it, "review_age", "-"), mark, getStr(it, "title", ""))
		if truthy(it, "verify_self_reported") {
			line += i18n.T(lang, "cli.summary.verify_self_reported")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n"
}

// feedbackLayer は ③ 外からの反応（未応答のフィードバック）。
// feedbackLayer の loc は時刻を描く時間帯（応答の timezone。serverTZ が決める）。
func feedbackLayer(lang i18n.Lang, fb *jsonorder.Object, loc *time.Location) string {
	issueCount := intOf(get(fb, "issue_count", int64(0)))
	lines := []string{i18n.T(lang, "cli.summary.feedback.heading", "count", intOf(get(fb, "count", int64(0))), "issues", issueCount)}
	if !truthy(fb, "count") {
		lines = append(lines, i18n.T(lang, "cli.none"))
	}
	shown := objects(orList(fb, "issues"))
	for _, it := range shown {
		lines = append(lines, fmt.Sprintf("%-9s ", getStr(it, "id", ""))+
			i18n.T(lang, "cli.summary.feedback.row", "time", localTimeIn(orStr(it, "first_at", ""), loc),
				"count", intOf(get(it, "pending", int64(0))), "excerpt", getStr(it, "excerpt", "")))
	}
	if rest := issueCount - int64(len(shown)); rest > 0 {
		lines = append(lines, i18n.TN(lang, "cli.summary.feedback.more", int(rest), "rest", rest))
	}
	return strings.Join(lines, "\n") + "\n"
}

// printUsageRequests は画面の「レポート作成」で登録された未完了の依頼。無ければ何も出さない。
func (c *Ctx) printUsageRequests(requests []*jsonorder.Object, loc *time.Location) error {
	if len(requests) == 0 {
		return nil
	}
	c.Println("")
	c.Println(i18n.T(c.Lang, "cli.summary.usage_requests.heading", "count", len(requests)))
	for _, q := range requests {
		line := i18n.T(c.Lang, "cli.summary.usage_requests.row", "id", getStr(q, "id", ""), "time", localTimeIn(orStr(q, "created_at", ""), loc),
			"by", orStr(q, "requested_by", "-"), "period", getStr(q, "period", ""))
		if truthy(q, "target") {
			line += i18n.T(c.Lang, "cli.summary.usage_requests.target", "target", getStr(q, "target", ""))
		}
		if truthy(q, "note") {
			line += i18n.T(c.Lang, "cli.summary.usage_requests.note", "note", strings.Join(strings.Fields(getStr(q, "note", "")), " "))
		}
		c.Println(line)
	}
	c.Println(i18n.T(c.Lang, "cli.next_command", "command", getStr(requests[len(requests)-1], "command", "")))
	return nil
}

// crossPathNote は「別の経路（CLI / MCP）で着手されている」の注記（対象が無ければ空）。
// サーバは同じセッションかを判定できないので、黙って自分のものとして扱わず、判定できないことを出す。
func crossPathNote(lang i18n.Lang, items []*jsonorder.Object) string {
	ids := noteIDs(lang, items, "cross_path_session")
	if len(ids) == 0 {
		return ""
	}
	return i18n.T(lang, "cli.summary.cross_path", "ids", strings.Join(ids, i18n.T(lang, "cli.sep.list")))
}

// noteIDs は印の付いた ID を並べる（経過時間があれば ID に添える）。
func noteIDs(lang i18n.Lang, items []*jsonorder.Object, flag string) []string {
	var ids []string
	for _, it := range items {
		if !truthy(it, flag) {
			continue
		}
		id := getStr(it, "id", "")
		if ago := getStr(it, "started_ago", ""); ago != "" {
			id = i18n.T(lang, "cli.summary.other_session.item", "id", id, "ago", ago)
		}
		ids = append(ids, id)
	}
	return ids
}

// otherSessionNote は「別のセッションが着手中」の注記（対象が無ければ空）。
// 同じ作業ツリーで複数の AI のセッションが動くとき、着手済みのものを取りに行かせないために出す。
// 経過時間も添えるので、長く動いていないもの（渡したまま放置されたもの）にも気づける。
func otherSessionNote(lang i18n.Lang, items []*jsonorder.Object) string {
	ids := noteIDs(lang, items, "other_session")
	if len(ids) == 0 {
		return ""
	}
	return i18n.T(lang, "cli.summary.other_session", "ids", strings.Join(ids, i18n.T(lang, "cli.sep.list")))
}

// localUsageFailure は、この PC の付与のフックが送れていないこと（最後の失敗と、このプロジェクトで再送を待っている件数）。
// どちらも無ければ nil。フックの失敗はフックの中では誰にも見えない（操作を妨げないよう黙って 0 で終わる）ので、
// 利用者と AI が毎セッション読む要約に出す（internal/client/usagesnap の failure.go）。再送はプロジェクトごとなので、
// 数えるのはこのプロジェクト（API の URL + slug）の置き場だけ（ほかのプロジェクトの分は、そのプロジェクトの要約に出る）。
func localUsageFailure(e env.Env, project string) *jsonorder.Object {
	dir := usagesnap.StateDir(e)
	f := usagesnap.ReadFailure(dir)
	spooled := len(usagesnap.SpoolFiles(usagesnap.SpoolDir(dir, e.Value(env.APIURL), project)))
	if f == nil && spooled == 0 {
		return nil
	}
	o := jsonorder.NewObject().Set("spooled", spooled)
	if f != nil {
		o.Set("reason", f.Reason).Set("at", f.At.UTC().Format(time.RFC3339)).Set("count", f.Count)
		if f.Status != 0 {
			o.Set("status", f.Status)
		}
	}
	return o
}

// usageFailureText は localUsageFailure の 1 行（無ければ ""）。
func usageFailureText(lang i18n.Lang, o *jsonorder.Object, loc *time.Location) string {
	if o == nil || len(o.Keys()) == 0 {
		return ""
	}
	spooled := intOf(get(o, "spooled", int64(0)))
	reason := getStr(o, "reason", "")
	if reason == "" {
		return i18n.TN(lang, "cli.summary.usage_spooled", int(spooled), "count", spooled)
	}
	var why string
	switch reason {
	case usagesnap.FailUnreachable:
		why = i18n.T(lang, "cli.summary.usage_fail.unreachable")
	case usagesnap.FailRejected:
		why = i18n.T(lang, "cli.summary.usage_fail.rejected", "status", intOf(get(o, "status", int64(0))))
	case usagesnap.FailNoToken:
		why = i18n.T(lang, "cli.summary.usage_fail.no_token")
	case usagesnap.FailSpool:
		why = i18n.T(lang, "cli.summary.usage_fail.spool_failed")
	case usagesnap.FailClient:
		why = i18n.T(lang, "cli.summary.usage_fail.client_error")
	default:
		why = reason // 新しい版が書いた知らないキーはそのまま出す
	}
	return i18n.TN(lang, "cli.summary.usage_send_failed", int(spooled), "at", localTimeIn(getStr(o, "at", ""), loc), "reason", why,
		"count", intOf(get(o, "count", int64(0))), "spooled", spooled)
}

// lookPathFn・executableFn は PATH の探し方と実行中の looptrack の場所（テストで差し替える）。
var (
	lookPathFn   = exec.LookPath
	executableFn = os.Executable
)

// pathMissingNote は、サーバ版の導入（控えの置き方が server）で PATH から looptrack を解決できないときの注意（それ以外は ""）。
// スキル・規律・hook の案内は looptrack を名前で呼ぶので、解決できないと AI が打つコマンドが動かない。直し方は setup の手順の
// 再実行（置き場を PATH に足す）で、導入済みで手順が返らないときは doctor が PATH を足すコマンドを示す。
//
// SessionStart の hook として動くときに見る PATH は、AI のアプリが hook に渡す PATH（アプリのプロセスの環境）で、AI がコマンドを
// 流すシェルの PATH とは別のもの。シェルの PATH は起動ファイルを読むぶん広いことがあるので、ここで見つからなくてもシェルでは
// 見つかることがある（アプリを開き直すまでの間など）。直し方はどちらでも「開き直す」なので、案内はそろえる。
func pathMissingNote(lang i18n.Lang, root string) string {
	if source, _ := installFiles(root); source != "server" {
		return ""
	}
	if p, err := lookPathFn("looptrack"); err == nil && p != "" {
		return ""
	}
	doctor := "looptrack doctor"
	if exe, err := executableFn(); err == nil && exe != "" {
		if runtime.GOOS == "windows" {
			doctor = "& '" + strings.ReplaceAll(exe, "'", "''") + "' doctor"
		} else {
			doctor = `"` + exe + `" doctor`
		}
	}
	// 正本（looptrack 自身のリポジトリ）では init をしないので、取得と PATH だけを案内する
	if IsSelfRepo(root) {
		return i18n.T(lang, "cli.summary.path_missing.self_repo", "doctor", doctor)
	}
	return i18n.T(lang, "cli.summary.path_missing", "doctor", doctor)
}
