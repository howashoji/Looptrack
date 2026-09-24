package cli

// 作業コピー（edit・push）。置き場とファイルの形は以前の CLI（1.0.0 より前）と同じ
// （<プロジェクト>/.claude/.looptrack-work/<ID>.md・.base.md・.json・.server.md）。
// 楽観ロック: push は edit 時の版を If-Match で送る。409 ならサーバの最新版を .server.md に置き、差分を標準エラーに出して止める。
// 差分を取り込んだ後の push --rebase は、最新版の版で送り直す。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// idFields は ID を入れる frontmatter の項目（ID は空白を含まない）。
var idFields = []string{"blocked_by", "traces", "refs"}

type workFiles struct{ work, base, meta, server string }

func (c *Ctx) workDir() string { return filepath.Join(c.Root, ".claude", ".looptrack-work") }

// workFiles は作業コピーのファイルの組（ID は大文字にする）。
func (c *Ctx) workFiles(id string) workFiles {
	base := filepath.Join(c.workDir(), strings.ToUpper(id))
	return workFiles{base + ".md", base + ".base.md", base + ".json", base + ".server.md"}
}

func (w workFiles) all() []string { return []string{w.work, w.base, w.meta, w.server} }

// rel は ROOT から見た相対パス。
func (c *Ctx) rel(p string) string {
	root, err1 := filepath.Abs(c.Root)
	abs, err2 := filepath.Abs(p)
	if err1 != nil || err2 != nil {
		return p
	}
	r, err := filepath.Rel(root, abs)
	if err != nil {
		return p
	}
	return r
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeText は open(path, "w", newline="") で書く（改行を変えない。既存のファイルは中身だけ置き換える）。
func writeText(p, text string) error { return os.WriteFile(p, []byte(text), 0o666) }

func readText(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

func removeAll(paths ...string) {
	for _, p := range paths {
		if exists(p) {
			os.Remove(p)
		}
	}
}

// cmdEdit は作業コピーを書き出す（API モード）。
func cmdEdit(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	id := v.Str("id")
	w := c.workFiles(id)
	if exists(w.work) && !v.Bool("force") {
		work, err := readText(w.work)
		if err != nil {
			return err
		}
		base, err := readText(w.base)
		if err != nil {
			return err
		}
		if work != base {
			return i18n.Errorf("cli.err.work_unpushed", "path", c.rel(w.work), "id", id)
		}
	}
	path, err := c.issuePath(id, "")
	if err != nil {
		return err
	}
	detail, err := c.getObject(cl, path)
	if err != nil {
		return err
	}
	md, err := must(detail, "markdown")
	if err != nil {
		return err
	}
	mdText, _ := md.(string)
	idv, err := must(detail, "id")
	if err != nil {
		return err
	}
	ver, err := must(detail, "version")
	if err != nil {
		return err
	}
	slug, err := c.Project()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.workDir(), 0o777); err != nil {
		return err
	}
	if err := writeText(filepath.Join(c.workDir(), ".gitignore"), "*\n"); err != nil {
		return err
	}
	if err := writeText(w.work, mdText); err != nil {
		return err
	}
	if err := writeText(w.base, mdText); err != nil {
		return err
	}
	meta := jsonorder.NewObject().Set("id", idv).Set("project", slug).Set("version", ver)
	if err := writeText(w.meta, jsonorder.Compact(meta)); err != nil {
		return err
	}
	removeAll(w.server)
	c.Println(i18n.T(c.Lang, "cli.edit.workcopy", "path", c.rel(w.work), "version", intOf(ver)))
	c.Println(i18n.T(c.Lang, "cli.edit.howto", "entry", api.EntryCommand(), "id", jsonorder.Str(idv)))
	return nil
}

// cmdPush は作業コピーをサーバへ反映する（API モード）。
func cmdPush(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	id := v.Str("id")
	w := c.workFiles(id)
	if !exists(w.work) || !exists(w.meta) {
		return i18n.Errorf("cli.err.no_workcopy", "id", strings.ToUpper(id), "entry", api.EntryCommand())
	}
	raw, err := os.ReadFile(w.meta)
	if err != nil {
		return err
	}
	info, err := jsonorder.DecodeObject(raw)
	if err != nil {
		return err
	}
	text, err := readText(w.work)
	if err != nil {
		return err
	}
	if ref, err := readText(w.base); err == nil {
		if text, err = normalizeWork(text, ref); err != nil {
			if errors.Is(err, errNotUTF8) {
				return i18n.Errorf("cli.err.work_not_utf8", "path", c.rel(w.work))
			}
			return err
		}
	}
	infoID := func() string { x, _ := info.Get("id"); return jsonorder.Str(x) }
	if v.Bool("rebase") {
		if !exists(w.server) || !info.Has("server_version") {
			return i18n.Errorf("cli.err.rebase_only_after_conflict")
		}
		sv, _ := info.Get("server_version")
		info.Delete("server_version")
		info.Set("version", sv)
		server, err := readText(w.server)
		if err != nil {
			return err
		}
		if err := writeText(w.base, server); err != nil {
			return err
		}
	} else {
		base, err := readText(w.base)
		if err != nil {
			return err
		}
		if text == base {
			removeAll(w.all()...)
			c.Println(i18n.T(c.Lang, "cli.push.no_change", "id", infoID()))
			return nil
		}
	}
	base, err := readText(w.base)
	if err != nil {
		return err
	}
	if key, val, ok := spacedID(text, base); ok {
		// サーバも拒否するが、検査の無い古いサーバでも 1 要素のまま保存させない
		return i18n.Errorf("cli.err.spaced_id", "key", key, "value", quoteString(val),
			"list", strings.Join(splitFields(val), ", "), "path", c.rel(w.work))
	}
	body := jsonorder.NewObject().Set("markdown", text)
	if o := v.Str("override"); o != "" { // 既定値なら送らない（古いサーバは未知のキーを 400 で拒否する）
		body.Set("override_reason", o)
	}
	verv, _ := info.Get("version")
	path, err := c.issuePath(infoID(), "")
	if err != nil {
		return err
	}
	resp, err := cl.Do(api.Request{Method: "PATCH", Path: path, Body: body,
		Headers: map[string]string{"If-Match": fmt.Sprintf(`"%d"`, intOf(verv))}})
	if err != nil {
		var ae *api.Error
		if !errors.As(err, &ae) {
			return err
		}
		current := ae.Body.Object("current")
		if ae.Status == 409 && current != nil && len(current.Members) > 0 {
			cmd, _ := current.Get("markdown")
			cmdText := jsonorder.Str(cmd)
			if err := writeText(w.server, cmdText); err != nil {
				return err
			}
			cver, _ := current.Get("version")
			info.Set("server_version", cver)
			if err := writeText(w.meta, jsonorder.Compact(info)); err != nil {
				return err
			}
			diff := unifiedDiff(splitLinesKeep(base), splitLinesKeep(cmdText),
				i18n.T(c.Lang, "cli.push.diff.at_edit", "version", intOf(verv)),
				i18n.T(c.Lang, "cli.push.diff.server", "version", intOf(cver)))
			fmt.Fprint(c.Stderr, diff)
			return i18n.Errorf("cli.err.push_conflict", "message", ae.Message, "path", c.rel(w.server),
				"entry", api.EntryCommand(), "id", infoID())
		}
		if ae.Status == 401 {
			return &Fail{i18n.T(c.Lang, "cli.err.unauthorized", "message", ae.Text(c.Lang), "relogin", api.Relogin(c.Lang))}
		}
		return &Fail{ae.Text(c.Lang)}
	}
	res := asObject(resp.Value)
	removeAll(w.all()...)
	rid, err := must(res, "id")
	if err != nil {
		return err
	}
	rver, err := must(res, "version")
	if err != nil {
		return err
	}
	c.Println(i18n.T(c.Lang, "cli.push.updated", "id", jsonorder.Str(rid), "version", intOf(rver)))
	return c.afterChange(res, jsonorder.Str(rid), "update")
}

// normalizeWork は Windows のエディタが保存した作業コピーを、サーバから取った形（edit 時の本文 ref）に揃える。
//
//   - 先頭の BOM（メモ帳などが UTF-8 に付ける）は ref に無ければ外す（frontmatter の "---" が先頭に来なくなるのを防ぐ）
//   - 改行が CRLF になっていて ref に CR が無ければ LF に戻す（全行が差分になる・frontmatter を読めないのを防ぐ）
//   - UTF-8 でなければ（ANSI・cp932 で保存した）誤り（化けたまま送らない。errNotUTF8。
//     利用者に見せる文面は、対象のパスを知っている呼ぶ側が作る）
//
// macOS・Linux でも同じ（ref と同じ形のファイルは何も変えない）。
// errNotUTF8 は作業コピーが UTF-8 でないときの印（文面は呼ぶ側が作る）。
var errNotUTF8 = errors.New("work copy is not utf-8")

func normalizeWork(text, ref string) (string, error) {
	const bom = "\ufeff"
	if strings.HasPrefix(text, bom) && !strings.HasPrefix(ref, bom) {
		text = text[len(bom):]
	}
	if !strings.Contains(ref, "\r") && strings.Contains(text, "\r\n") {
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	if !utf8.ValidString(text) {
		return "", errNotUTF8
	}
	return text, nil
}

// parseFront は frontmatter を読む（スカラーとインラインリスト）。
// 値は string か []string。
func parseFront(text string) map[string]any {
	data := map[string]any{}
	if !strings.HasPrefix(text, "---") {
		return data
	}
	end := strings.Index(text[3:], "\n---")
	if end < 0 {
		return data
	}
	end += 3
	raw := strings.Trim(text[3:end], "\n")
	for _, line := range strings.Split(raw, "\n") {
		if trimSpace(line) == "" || strings.HasPrefix(strings.TrimLeftFunc(line, isSpaceRune), "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val = trimSpace(key), trimSpace(val)
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") && len(val) >= 2 {
			inner := trimSpace(val[1 : len(val)-1])
			list := []string{}
			if inner != "" {
				for _, x := range strings.Split(inner, ",") {
					if x = trimSpace(x); x != "" {
						list = append(list, x)
					}
				}
			}
			data[key] = list
		} else {
			data[key] = val
		}
	}
	return data
}

// spacedID は作業コピーの ID の項目に新しく入った、空白を含む値の最初の 1 件。
// edit 時点で既に入っていた値（補正前の旧データ）は対象にしない（サーバの検査と同じ）。
func spacedID(text, baseText string) (string, string, bool) {
	front, base := parseFront(text), parseFront(baseText)
	for _, key := range idFields {
		vals, ok := front[key].([]string)
		if !ok {
			continue
		}
		old, _ := base[key].([]string)
		for _, v := range vals {
			if strings.IndexFunc(v, isSpaceRune) < 0 {
				continue
			}
			seen := false
			for _, o := range old {
				if o == v {
					seen = true
					break
				}
			}
			if !seen {
				return key, v, true
			}
		}
	}
	return "", "", false
}

// quoteString は文面に埋め込むために引用符で囲む（引用符の選び方・逃がし方は以前の CLI と同じ）。
func quoteString(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(q):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == ' ' || isPrintable(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

// isPrintable は str.isprintable の 1 文字（空白の類・制御文字・書式文字は印字しない）。
func isPrintable(r rune) bool {
	if isSpaceRune(r) || r == 0xad || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2060 && r <= 0x2064) || r == 0xfeff {
		return false
	}
	return r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0) && unicode.IsGraphic(r)
}
