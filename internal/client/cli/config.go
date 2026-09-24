package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// KitJSON は導入セットの記録（init が書く）。
const KitJSON = ".claude/.looptrack-kit.json"

// cmdConfig は接続先と導入の状態を出す（API モード）。
func cmdConfig(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("")
	if err != nil {
		return err
	}
	prv, err := cl.Get(path)
	if err != nil {
		return err
	}
	pr, _ := prv.(*jsonorder.Object)
	if pr == nil {
		pr = jsonorder.NewObject()
	}
	// ログインの確認（login --browser の後に誰で繋がっているかを見る）。取れなくても設定の表示は続ける
	user := ""
	if me, err := cl.Get("/me"); err == nil {
		if o, ok := me.(*jsonorder.Object); ok {
			if l, ok := o.Get("login"); ok {
				user = jsonorder.Str(l)
			}
		}
	} else if !isAPIFailure(err) {
		return err
	}
	_, src, err := cl.TokenSource(cl.BaseURL)
	if err != nil {
		return err
	}
	get := func(k string) any { x, _ := pr.Get(k); return x }
	member := any(true)
	if x, ok := pr.Get("member"); ok {
		member = x
	}
	var token any
	if src != "" {
		token = src
	}
	data := jsonorder.NewObject().
		Set("mode", "api").Set("url", cl.BaseURL).Set("project", get("slug")).Set("name", get("name")).
		Set("prefix", get("prefix")).Set("width", get("width")).Set("role", get("role")).Set("member", member).
		Set("counter", get("counter")).Set("rules", get("rules")).Set("token", token).Set("user", user)
	kit, line, err := kitSummary(c.Lang, c.Root)
	if err != nil {
		return err
	}
	if kit != nil {
		data.Set("kit", kit)
	} else {
		data.Set("kit", nil)
	}
	if v.Bool("json") {
		c.PrintJSON(data)
		return nil
	}
	width, _ := jsonorder.Int(get("width"))
	c.Println(i18n.T(c.Lang, "cli.config.mode", "url", cl.BaseURL))
	c.Println(i18n.T(c.Lang, "cli.config.project", "slug", jsonorder.Str(get("slug")), "name", jsonorder.Str(get("name")),
		"prefix", jsonorder.Str(get("prefix")), "width", strings.Repeat("0", int(max(width, 0))), "role", jsonorder.Str(get("role"))))
	if !jsonorder.Truthy(member) {
		// 管理者が参加していないプロジェクトを LOOPTRACK_PROJECT で指定したとき。今のサーバは閲覧のみ
		// （role viewer）。それより前のサーバは role admin で書けるので、返ってきた役割で文面を分ける
		if r := jsonorder.Str(get("role")); r == "editor" || r == "admin" {
			c.Println(i18n.T(c.Lang, "cli.config.not_member.editor"))
		} else {
			c.Println(i18n.T(c.Lang, "cli.config.not_member.viewer"))
		}
	}
	if user == "" {
		user = "?"
	}
	c.Println(i18n.T(c.Lang, "cli.config.user", "user", user))
	c.Println(i18n.T(c.Lang, "cli.config.token", "source", jsonorder.Str(token)))
	c.Println(i18n.T(c.Lang, "cli.config.kit", "line", line))
	return nil
}

// isAPIFailure は API の誤り・接続の失敗か（どちらも config では致命ではなく、1 行にして続ける）。
func isAPIFailure(err error) bool {
	var ae *api.Error
	var ce *api.ConnError
	return errors.As(err, &ae) || errors.As(err, &ce)
}

// kitSummary は (.looptrack-kit.json の中身 or nil, config に出す 1 行)。
func kitSummary(lang i18n.Lang, root string) (*jsonorder.Object, string, error) {
	p := filepath.Join(root, filepath.FromSlash(KitJSON))
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, i18n.T(lang, "cli.config.kit.missing", "file", KitJSON), nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	unreadable := i18n.T(lang, "cli.config.kit.unreadable", "file", KitJSON)
	if !utf8.Valid(b) {
		return nil, unreadable, nil
	}
	data, err := jsonorder.DecodeObject(b)
	if err != nil {
		return nil, unreadable, nil
	}
	sub := func(k string) *jsonorder.Object {
		if o := data.Object(k); o != nil {
			return o
		}
		return jsonorder.NewObject()
	}
	core, loop := sub("core"), sub("loop")
	ver := func(o *jsonorder.Object, k string) string {
		s := o.String(k)
		if r := []rune(s); len(r) > 12 {
			s = string(r[:12])
		}
		if s == "" {
			return "-"
		}
		return s
	}
	day := func(o *jsonorder.Object, k string) string {
		t := o.String(k)
		if t == "" {
			return "-"
		}
		s := localTimeIn(t, defaultTZ) // 応答ではなく手元の .looptrack-kit.json。時間帯が分からないので既定に倒す
		if r := []rune(s); len(r) > 10 {
			s = string(r[:10])
		}
		return s
	}
	line := "core "
	if x, _ := core.Get("bundle_sha256"); jsonorder.Truthy(x) {
		line += ver(core, "bundle_sha256")
	} else {
		line += i18n.T(lang, "cli.config.kit.core_none")
	}
	switch {
	case truthyKey(loop, "installed"):
		line += i18n.T(lang, "cli.config.kit.loop_installed", "version", ver(loop, "bundle_sha256"), "date", day(loop, "installed_at"))
	case truthyKey(loop, "removed_at"):
		line += i18n.T(lang, "cli.config.kit.loop_removed", "date", day(loop, "removed_at"))
	case truthyKey(loop, "declined_at"):
		line += i18n.T(lang, "cli.config.kit.loop_declined", "date", day(loop, "declined_at"))
	default:
		line += i18n.T(lang, "cli.config.kit.loop_none")
	}
	return data, line, nil
}

func truthyKey(o *jsonorder.Object, k string) bool {
	x, _ := o.Get(k)
	return jsonorder.Truthy(x)
}

// IsSelfRepo は dir が looptrack 自身のリポジトリ（kit の正本）かを返す。判定はここだけに置く:
//   - init は自分自身への導入を拒む（internal/client/kitinit）
//   - 導入済み通知は self_repo として送り、サーバは kit を配布物と比べない
//
// 見るのは配置（kit/embed.go と cmd/looptrack）で、リポジトリの名前や remote は見ない
// （fork・別名の clone・git の作業ツリーでも同じに判定する）。
func IsSelfRepo(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, "kit", "embed.go"))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	d, err := os.Stat(filepath.Join(dir, "cmd", "looptrack"))
	return err == nil && d.IsDir()
}
