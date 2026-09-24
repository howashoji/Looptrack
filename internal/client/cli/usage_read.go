package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// usage の読み取り系（show・usage report・usage ledger list・usage missing・usage requests。
// DESIGN.md §5-4）。attach と ledger add は別のファイル。

// usageAPI は usage のサブコマンドの前提（API モードだけ）。
func (c *Ctx) usageAPI() (*api.Client, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	if cl.BaseURL == "" {
		return nil, i18n.Errorf("cli.err.usage_api_only", "env", env.Name(env.APIURL))
	}
	return cl, nil
}

// cmdUsageShow は usage show <ID>（イシューの段階別の消費）。
func cmdUsageShow(c *Ctx, v *Values) error {
	cl, err := c.usageAPI()
	if err != nil {
		return err
	}
	id := v.Str("id")
	if id == "" {
		return i18n.Errorf("cli.err.usage_show_id")
	}
	path, err := c.issuePath(id, "/usage")
	if err != nil {
		return err
	}
	res, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	stages := objects(get(res, "stages", nil))
	sortUsageStages(stages)
	if raw, ok := res.Get("stages"); ok && jsonorder.Truthy(raw) {
		sorted := make([]any, len(stages))
		for i, s := range stages {
			sorted[i] = s
		}
		res.Set("stages", sorted)
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	for _, k := range []string{"issue", "total_tokens", "stage_count", "excluded_total", "inconsistent", "stages"} {
		if _, err := must(res, k); err != nil {
			return err
		}
	}
	ex := asObject(get(res, "excluded_total", nil))
	excluded := sumValues(asObject(get(ex, "main", nil))) + sumValues(asObject(get(ex, "sub", nil)))
	c.Println(i18n.T(c.Lang, "cli.usage.show.total", "issue", getStr(res, "issue", ""),
		"total", comma(get(res, "total_tokens", nil)), "stages", intOf(get(res, "stage_count", nil)),
		"excluded", comma(excluded), "inconsistent", intOf(get(res, "inconsistent", nil))))
	if len(stages) == 0 {
		return nil
	}
	c.Println(fmt.Sprintf("%-20s %-10s %-8s %-12s %10s %10s %10s %10s %10s",
		i18n.T(c.Lang, "cli.usage.col.time"), i18n.T(c.Lang, "cli.usage.col.trigger"), i18n.T(c.Lang, "cli.usage.col.op"),
		i18n.T(c.Lang, "cli.usage.col.client"), i18n.T(c.Lang, "cli.usage.col.input"), i18n.T(c.Lang, "cli.usage.col.cache_create"),
		i18n.T(c.Lang, "cli.usage.col.cache_read"), i18n.T(c.Lang, "cli.usage.col.output"), i18n.T(c.Lang, "cli.usage.col.delta_total")))
	for _, st := range stages {
		d := asObject(get(st, "delta", nil))
		m, sub := asObject(get(d, "main", nil)), asObject(get(d, "sub", nil))
		add := func(k string) string { return comma(intOf(get(m, k, nil)) + intOf(get(sub, k, nil))) }
		note := ""
		if truthy(st, "excluded") {
			note = i18n.T(c.Lang, "cli.usage.note.excluded")
		} else if truthy(st, "inconsistent") {
			note = i18n.T(c.Lang, "cli.usage.note.inconsistent")
		}
		c.Println(fmt.Sprintf("%-20s %-10s %-8s %-12s %10s %10s %10s %10s %10s%s",
			strings.Replace(truncRunes(getStr(st, "at", ""), 19), "T", " ", -1), getStr(st, "trigger", ""), orStr(st, "op", "-"),
			getStr(st, "client", ""), add("input"), add("cache_create"), add("cache_read"), add("output"),
			comma(get(st, "delta_total", nil)), note))
	}
	return nil
}

func sumValues(o *jsonorder.Object) int64 {
	var n int64
	for _, k := range o.Keys() {
		x, _ := o.Get(k)
		n += intOf(x)
	}
	return n
}

// sortUsageStages は段階を時刻順（同時刻は id 順）に並べる。
func sortUsageStages(stages []*jsonorder.Object) {
	type key struct {
		head, frac string
		id         int64
	}
	keyOf := func(st *jsonorder.Object) key {
		at := orStr(st, "at", "")
		r := []rune(at)
		head, frac := string(r[:min(19, len(r))]), ""
		if len(r) > 19 {
			frac = strings.TrimRight(string(r[19:]), "Z")
		}
		if strings.HasPrefix(frac, ".") {
			frac = frac[1:]
		} else {
			frac = ""
		}
		if n := len([]rune(frac)); n < 9 {
			frac += strings.Repeat("0", 9-n)
		}
		return key{head, frac, intOf(get(st, "id", nil))}
	}
	sort.SliceStable(stages, func(i, j int) bool {
		a, b := keyOf(stages[i]), keyOf(stages[j])
		if a.head != b.head {
			return a.head < b.head
		}
		if a.frac != b.frac {
			return a.frac < b.frac
		}
		return a.id < b.id
	})
}

// reportQuery は usage report の期間の指定をクエリ文字列にする（検証はサーバ）。
func reportQuery(v *Values) (string, error) {
	since, from, to := v.Bool("since_last"), v.Str("date_from"), v.Str("date_to")
	q := query{}
	if v.IsSet("request") {
		if since || from != "" || to != "" {
			return "", i18n.Errorf("cli.err.usage_request_conflict")
		}
		q.add("request", fmt.Sprint(v.Int("request")))
		return q.encode(), nil
	}
	if !since && from == "" && to == "" {
		return "", i18n.Errorf("cli.err.usage_period_required")
	}
	if since {
		q.add("since_last", "1")
	}
	q.addIf("from", from)
	q.addIf("to", to)
	return q.encode(), nil
}

// cmdUsageReport は usage report（レポート用の集計。表示用の文・JSON・xlsx はどれもサーバが作る）。
func cmdUsageReport(c *Ctx, v *Values) error {
	cl, err := c.usageAPI()
	if err != nil {
		return err
	}
	q, err := reportQuery(v)
	if err != nil {
		return err
	}
	if out := v.Str("xlsx"); v.IsSet("xlsx") && out != "" {
		path, err := c.ProjectPath("/usage/report.xlsx?" + q)
		if err != nil {
			return err
		}
		dest, rows, err := c.saveAttachment(cl, path, out)
		if err != nil {
			return err
		}
		c.Println(i18n.T(c.Lang, "cli.usage.report.saved", "path", dest, "rows", rows))
		return nil
	}
	if v.Bool("json") {
		path, err := c.ProjectPath("/usage/report?" + q)
		if err != nil {
			return err
		}
		res, err := cl.Get(path)
		if err != nil {
			return err
		}
		c.PrintJSON(res)
		return nil
	}
	path, err := c.ProjectPath("/usage/report?format=md&" + q)
	if err != nil {
		return err
	}
	res, err := cl.Get(path)
	if err != nil {
		return err
	}
	c.Println(strings.TrimRight(jsonorder.Str(res), "\n"))
	return nil
}

// saveAttachment は添付ファイル（xlsx）を GET して out に置き換えで保存し、保存先と X-Looptrack-Rows を返す
// （出す文面は呼ぶ側が作る。文面の ID を引数で受け取ると、訳の抜けを機械で見つけられないため）。
// 誤りは「エラー: <サーバのメッセージ>」だけ（401 の案内・5xx の前置きを付けない）。
func (c *Ctx) saveAttachment(cl *api.Client, path, out string) (dest, rows string, err error) {
	res, err := cl.Do(api.Request{Method: "GET", Path: path, Raw: true, Headers: map[string]string{"Accept": "*/*"}})
	if err != nil {
		return "", "", plainAPIError(err)
	}
	dest, err = absUserPath(c, out)
	if err != nil {
		return "", "", err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, res.Raw, 0o644); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", "", err
	}
	rows = res.Header.Get("X-Looptrack-Rows")
	if rows == "" {
		rows = "?"
	}
	return dest, rows, nil
}

// absUserPath は os.path.abspath(os.path.expanduser(p))（相対パスは作業ディレクトリから）。
func absUserPath(c *Ctx, p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home := c.Env.Get("HOME")
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			home = h
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// cmdUsageLedgerList は usage ledger list（トークンレポートの台帳の一覧）。
func cmdUsageLedgerList(c *Ctx, v *Values) error {
	cl, err := c.usageAPI()
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/usage/ledger")
	if err != nil {
		return err
	}
	res, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	items, err := mustObjects(res, "items")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		c.Println(i18n.T(c.Lang, "cli.usage.ledger.empty"))
		return nil
	}
	loc, tzName := serverTZ(c.Lang, res) // サーバのローカル時刻。時刻も注記もこれで描く
	c.Println(fmt.Sprintf("%-5s %-16s %-16s %-16s %12s %s", "#", i18n.T(c.Lang, "cli.usage.ledger.col.from"),
		i18n.T(c.Lang, "cli.usage.ledger.col.data_end"), i18n.T(c.Lang, "cli.usage.ledger.col.created"),
		i18n.T(c.Lang, "cli.usage.ledger.col.tokens"), i18n.T(c.Lang, "cli.usage.ledger.col.name")))
	for _, it := range items {
		from := localTimeIn(orStr(it, "from", ""), loc)
		if from == "" {
			from = i18n.T(c.Lang, "cli.usage.ledger.from_start")
		}
		total := "-"
		if x, _ := it.Get("total_tokens"); x != nil {
			total = comma(x)
		}
		c.Println(fmt.Sprintf("%-5s %-16s %-16s %-16s %12s %s", getStr(it, "id", ""), from, localTimeIn(orStr(it, "data_end", ""), loc),
			localTimeIn(orStr(it, "created_at", ""), loc), total, getStr(it, "name", "")))
	}
	c.Println("\n" + i18n.T(c.Lang, "cli.usage.ledger.count", "count", intOf(get(res, "count", nil)),
		"from", localTimeIn(orStr(res, "next_from", ""), loc), "tz", tzName))
	return nil
}

// cmdUsageMissing は usage missing（AI の変更操作のうちトークン情報が付いていないものと充足率）。
func cmdUsageMissing(c *Ctx, v *Values) error {
	cl, err := c.usageAPI()
	if err != nil {
		return err
	}
	q := query{}
	q.add("days", fmt.Sprint(v.Int("days")))
	if !v.Bool("all_users") {
		q.add("mine", "1")
	}
	path, err := c.ProjectPath("/usage/coverage?" + q.encode())
	if err != nil {
		return err
	}
	res, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	rate := "-"
	if x, _ := res.Get("rate"); x != nil {
		f, _ := jsonorder.Float(x)
		rate = fmt.Sprintf("%.1f%%", f*100)
	}
	who := i18n.T(c.Lang, "cli.usage.missing.who.all")
	if truthy(res, "mine") {
		who = i18n.T(c.Lang, "cli.usage.missing.who.mine")
	}
	c.Println(i18n.T(c.Lang, "cli.usage.missing.rate", "rate", rate, "days", intOf(get(res, "days", nil)), "who", who,
		"target", intOf(get(res, "target", nil)), "attached", intOf(get(res, "attached", nil)),
		"missing", intOf(get(res, "missing_count", nil)), "humans", intOf(get(res, "humans", nil))))
	missing := objects(get(res, "missing", nil))
	if len(missing) == 0 {
		c.Println(i18n.T(c.Lang, "cli.usage.missing.none"))
		return nil
	}
	c.Println(fmt.Sprintf("%-20s %-9s %-8s %-4s %-12s %s", i18n.T(c.Lang, "cli.usage.col.time"), "ID",
		i18n.T(c.Lang, "cli.usage.col.op"), i18n.T(c.Lang, "cli.usage.col.via"), i18n.T(c.Lang, "cli.usage.col.user"),
		i18n.T(c.Lang, "cli.usage.col.title")))
	for _, m := range missing {
		c.Println(fmt.Sprintf("%-20s %-9s %-8s %-4s %-12s %s", strings.Replace(truncRunes(getStr(m, "at", ""), 19), "T", " ", -1),
			getStr(m, "issue", ""), getStr(m, "kind", ""), getStr(m, "via", ""), getStr(m, "user", ""), getStr(m, "title", "")))
	}
	c.Println(i18n.T(c.Lang, "cli.usage.missing.attach", "entry", api.EntryCommand(), "ids", strings.Join(strList(res, "issues"), " ")))
	return nil
}

// cmdUsageRequests は usage requests（画面の「レポート作成」で登録された依頼。既定は未完了だけ）。
func cmdUsageRequests(c *Ctx, v *Values) error {
	cl, err := c.usageAPI()
	if err != nil {
		return err
	}
	suffix := "/usage/requests"
	if v.Bool("all") {
		suffix += "?all=1"
	}
	path, err := c.ProjectPath(suffix)
	if err != nil {
		return err
	}
	res, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	items, err := mustObjects(res, "items")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		if v.Bool("all") {
			c.Println(i18n.T(c.Lang, "cli.usage.requests.none_all"))
		} else {
			c.Println(i18n.T(c.Lang, "cli.usage.requests.none"))
		}
		return nil
	}
	loc, _ := serverTZ(c.Lang, res) // 時刻は応答の timezone（サーバのローカル時刻）で描く
	var pending []*jsonorder.Object
	for _, q := range items {
		state := i18n.T(c.Lang, "cli.usage.requests.state.pending")
		if truthy(q, "done") {
			r := asObject(get(q, "report", nil))
			state = i18n.T(c.Lang, "cli.usage.requests.state.done", "id", getStr(r, "id", ""), "name", getStr(r, "name", ""))
		} else {
			pending = append(pending, q)
		}
		line := fmt.Sprintf("#%-4s ", getStr(q, "id", "")) +
			i18n.T(c.Lang, "cli.usage.requests.row", "time", localTimeIn(orStr(q, "created_at", ""), loc),
				"state", fmt.Sprintf("%-8s", state), "by", orStr(q, "requested_by", "-"), "period", getStr(q, "period", ""))
		if truthy(q, "target") {
			line += i18n.T(c.Lang, "cli.usage.requests.target", "target", getStr(q, "target", ""))
		}
		if truthy(q, "note") {
			line += i18n.T(c.Lang, "cli.usage.requests.note", "note", strings.Join(strings.Fields(getStr(q, "note", "")), " "))
		}
		c.Println(line)
	}
	if len(pending) > 0 {
		c.Println("\n" + i18n.T(c.Lang, "cli.next_command", "command", getStr(pending[len(pending)-1], "command", "")))
	}
	return nil
}
