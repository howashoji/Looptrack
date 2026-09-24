package cli

import (
	"io"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// cmdGuide は使い方とルールを 1 回で表示する（MCP の guide ツールと同じ内容）。
func cmdGuide(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("guide")
	if err != nil {
		return err
	}
	if v.Bool("json") {
		path, err := c.ProjectPath("/guide")
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
	path, err := c.ProjectPath("/guide?format=md")
	if err != nil {
		return err
	}
	res, err := cl.Get(path)
	if err != nil {
		return err
	}
	io.WriteString(c.Stdout, jsonorder.Str(res)) // print(…, end="")
	return nil
}
