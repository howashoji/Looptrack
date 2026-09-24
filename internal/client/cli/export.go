package cli

import (
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

// cmdExport は課題管理表を保存する（xlsx はサーバが internal/xlsxreport で作る。ここは受け取って保存するだけ）。
func cmdExport(c *Ctx, v *Values) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	if cl.BaseURL == "" {
		return i18n.Errorf("cli.err.export_api_only", "env", env.Name(env.APIURL))
	}
	if s := v.Str("status"); s != "" {
		if err := validate("--status", s, statuses); err != nil {
			return err
		}
	}
	path, err := c.ProjectPath("/issues.xlsx?" + listQuery(v,
		[2]string{"status", v.Str("status")}, [2]string{"type", v.Str("type")}, [2]string{"label", v.Str("label")},
		[2]string{"ref", v.Str("ref")}, [2]string{"all", flag1(v.Bool("all"))}, [2]string{"assignee", v.Str("assignee")}))
	if err != nil {
		return err
	}
	dest, rows, err := c.saveAttachment(cl, path, v.Str("xlsx"))
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.export.saved", "path", dest, "rows", rows))
	return nil
}
