package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 導入済み通知（DESIGN.md §5-6）。

// installFiles は (置き方, {配布ファイル名: SHA-256})。
//
// files は空で送る。以前は導入先に置いたスクリプトのハッシュを送っていたが、今はスクリプトを 1 つも置かない。
// 古さはサーバが client.version（実行ファイルの版）で決めるので、files は判定に使われない
// （client を知らない古いサーバへの互換のためだけにキーを残す）。
//
// source: init が控えた置き方（.claude/.looptrack-kit.json、無ければ旧 .claude/scripts/.im-dist.json の source）。
// 実行ファイルが symlink の先にあるわけではないので、控えだけを見る（link の導入は init が .looptrack-kit.json に link と控える）。
func installFiles(root string) (string, *jsonorder.Object) {
	files := jsonorder.NewObject()
	source := ""
	{
		// .claude/.looptrack-kit.json。古い導入は .claude/scripts/.im-dist.json
		for _, p := range []string{filepath.Join(root, filepath.FromSlash(KitJSON)), filepath.Join(root, ".claude", "scripts", ".im-dist.json")} {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			v, err := jsonorder.Decode(b)
			if err != nil {
				continue
			}
			o, ok := v.(*jsonorder.Object)
			if !ok {
				break
			}
			source = orStr(o, "source", "")
			break
		}
	}
	return source, files
}

// installKit は .claude/.looptrack-kit.json の core / loop の控えを通知の形にする（無い・読めなければ空）。
func installKit(root string) *jsonorder.Object {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(KitJSON)))
	if err != nil {
		return jsonorder.NewObject()
	}
	v, err := jsonorder.Decode(b)
	if err != nil {
		return jsonorder.NewObject()
	}
	data, ok := v.(*jsonorder.Object)
	if !ok {
		return jsonorder.NewObject()
	}
	core, loop := asObject(get(data, "core", nil)), asObject(get(data, "loop", nil))
	if !truthy(data, "core") {
		core = jsonorder.NewObject()
	}
	if !truthy(data, "loop") {
		loop = jsonorder.NewObject()
	}
	installed := truthy(loop, "installed")
	sha, ver := "", ""
	if installed {
		sha = orStr(loop, "bundle_sha256", "")
		ver = orStr(loop, "version", "")
		if r := []rune(ver); len(r) > 64 {
			ver = string(r[:64])
		}
	}
	out := jsonorder.NewObject().Set("loop", jsonorder.NewObject().
		Set("installed", installed).Set("declined", !installed && truthy(loop, "declined_at")).
		Set("bundle_sha256", sha).Set("version", ver))
	if truthy(core, "bundle_sha256") {
		out.Set("core", jsonorder.NewObject().Set("bundle_sha256", get(core, "bundle_sha256", nil)))
	}
	return out
}

// reportInstall は導入済みをサーバへ知らせ、導入状態を返す（パスは送らない。ディレクトリ名とホスト名だけ）。
func (c *Ctx) reportInstall(cl *api.Client, agent, trigger string) (*jsonorder.Object, error) {
	source, files := installFiles(c.Root)
	host, _ := os.Hostname()
	ws := c.Root
	if r, err := filepath.EvalSymlinks(ws); err == nil {
		ws = r
	}
	body := jsonorder.NewObject().Set("agent", agent).Set("trigger", trigger).Set("source", source).Set("files", files).
		Set("host", truncRunes(host, 255)).Set("workspace", truncRunes(filepath.Base(ws), 255))
	// looptrack 自身のリポジトリ（kit の正本）からの通知は印を付ける。サーバはこの印がある導入の kit を
	// 配布物と比べない（手元の kit のほうが新しいのは当たり前で、促される init の再実行も拒否される）。
	// 偽のときは送らない（古いサーバは未知のキーを 400 で拒むので、普通のプロジェクトの通知の形を変えない）
	if IsSelfRepo(c.Root) {
		body.Set("self_repo", true)
	}
	kit := installKit(c.Root)
	for _, k := range kit.Keys() {
		x, _ := kit.Get(k)
		body.Set(k, x)
	}
	// 実行ファイルの版。サーバはこれで古さを判定する（【配布スクリプトの更新】→ looptrack self-update）
	body.Set("client", ClientInfo())
	path, err := c.ProjectPath("/install")
	if err != nil {
		return nil, err
	}
	res, err := cl.Do(api.Request{Method: "POST", Path: path, Body: body})
	var ae *api.Error
	if err != nil && errors.As(err, &ae) && ae.Status == 400 && strings.Contains(ae.Message, "unknown field") {
		// 古いサーバは core / loop や client を未知のキーとして 400 で拒否する。
		// 導入済みの通知だけは届ける（入口の無い導入は files が空なので、古いサーバには届かない）
		for _, k := range kit.Keys() {
			body.Delete(k)
		}
		body.Delete("client")
		body.Delete("self_repo")
		res, err = cl.Do(api.Request{Method: "POST", Path: path, Body: body})
	}
	if err != nil {
		return nil, err
	}
	return asObject(res.Value), nil
}

// ClientInfo は導入済み通知の client（実行ファイルの版・OS・CPU）。
func ClientInfo() *jsonorder.Object {
	return jsonorder.NewObject().Set("version", api.Version).Set("os", runtime.GOOS).Set("arch", runtime.GOARCH)
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// cmdInstalled は導入済みをサーバへ通知し、結果を出す。
func cmdInstalled(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("installed")
	if err != nil {
		return err
	}
	res, err := c.reportInstall(cl, v.Str("agent"), "manual")
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			msg := ae.Text(c.Lang)
			if ae.Status == 401 {
				msg = i18n.T(c.Lang, "cli.err.unauthorized", "message", msg, "relogin", api.Relogin(c.Lang))
			}
			return &Fail{msg}
		}
		return err
	}
	if v.Bool("json") {
		c.PrintJSON(res)
		return nil
	}
	msg, err := must(res, "message")
	if err != nil {
		return err
	}
	c.Println(jsonorder.Str(msg))
	return nil
}
