package cli

import "github.com/howashoji/looptrack/internal/client/jsonorder"

// cmdShow はイシューを出す（--json は応答のまま、それ以外は Markdown の全文）。
func cmdShow(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	var params [][2]string
	if !v.Bool("json") {
		params = append(params, [2]string{"format", "md"})
	}
	path, err := c.issuePath(v.Str("id"), "", params...)
	if err != nil {
		return err
	}
	res, err := cl.Get(path)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	c.Println(jsonorder.Str(res))
	return nil
}
