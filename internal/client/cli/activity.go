package cli

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// cmdActivity はイシューごとの最終更新を出す（API モード。鮮度ガードが使う）。
func cmdActivity(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	q := query{}
	q.add("ids", strings.Join(v.List("ids"), ","))
	q.add("since", jsonorder.FormatFloat(v.Float("since"))) // repr(float)
	res, err := c.getObject(cl, "/activity?"+q.encode())
	if err != nil {
		return err
	}
	raw, err := must(res, "items")
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(jsonorder.NewObject().Set("items", raw))
		return nil
	}
	loc, _ := serverTZ(c.Lang, res) // 時刻は応答の timezone（サーバのローカル時刻）で描く
	items := objects(raw)
	if len(items) == 0 {
		c.Println(i18n.T(c.Lang, "cli.none"))
		return nil
	}
	for _, i := range items {
		c.Println(fmt.Sprintf("%-9s ", getStr(i, "id", "")) +
			i18n.T(c.Lang, "cli.activity.row", "time", fromTimestamp(get(i, "last_epoch", nil), loc),
				"kind", fmt.Sprintf("%-8s", getStr(i, "last_kind", "")), "count", intOf(get(i, "events_since", nil))))
	}
	return nil
}

// fromTimestamp は epoch 秒を loc の「YYYY-MM-DD HH:MM:SS」にする。
// 時間帯は必ず引数で受け取る（応答の timezone。サーバや台帳と同じ時刻を描くため）。
// 以前の CLI と同じく、マイクロ秒に丸めてから秒未満を捨てる。
func fromTimestamp(v any, loc *time.Location) string {
	f, _ := jsonorder.Float(v)
	sec, frac := math.Modf(f)
	us := math.RoundToEven(frac * 1e6)
	t := time.Unix(int64(sec), int64(us)*1000).In(loc)
	return t.Format("2006-01-02 15:04:05")
}
