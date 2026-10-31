package cli

// `looptrack memory import`: 手元の記憶のディレクトリ（kit の file の置き場）をサーバの記憶へ入れる。
//
// 入れるのは引き継ぎのファイル（既定 handoff.md）・decision-*.md・caveat-*.md だけ。既にある名前は上書きしない（作成は 409 で止まる）。
// 拒まれたファイルがあっても残りは続け、最後に終了コード 1 を返す。資格情報の形が当たったファイルは送らず、名前と行と規則の語だけを出す。

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// memoryDefaultHandoff は引き継ぎの既定の名前（サーバの service.DefaultHandoffName と同じ値）。
const memoryDefaultHandoff = "main"

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// memoryImportTarget はファイルの名前から種類と名前を決める（対象でなければ ok が false）。
func memoryImportTarget(file, handoff string) (kind, name string, ok bool) {
	if file == handoff {
		return "handoff", memoryDefaultHandoff, true
	}
	base := strings.TrimSuffix(file, ".md")
	if name, ok := strings.CutPrefix(base, "decision-"); ok && base != file {
		return "decision", name, true
	}
	if name, ok := strings.CutPrefix(base, "caveat-"); ok && base != file {
		return "caveat", name, true
	}
	return "", "", false
}

// memoryImportContent はサーバへ送る本文を作る。サーバの要約は本文の最初の空でない行なので、`> 要約:` の行は要約を先頭に移す。
// 空の `> 要約:` は要約なしとして行だけを落とす（落とさないと、その行そのものが要約になる）。
func memoryImportContent(text string) string {
	sum, at := MemoryFileSummary(text)
	if at < 0 {
		return text
	}
	lines := memoryLines(text)
	rest := strings.TrimLeft(strings.Join(append(lines[:at:at], lines[at+1:]...), "\n"), "\n")
	switch {
	case sum == "":
		return rest
	case strings.TrimSpace(rest) == "":
		return sum + "\n"
	}
	return sum + "\n\n" + rest
}

// memoryImportName は出力に出すファイルの名前。名前が資格情報の形なら値を出さず規則の語だけにする。
func memoryImportName(lang i18n.Lang, file string) string {
	if f, hit := domain.FindSecret(file); hit {
		return i18n.T(lang, "cli.memory.import.secret_name", "label", f.Label)
	}
	return file
}

// memoryImportDir は読むディレクトリ（引数 → LOOPTRACK_LOOP_MEMORIES_DIR → 本体の作業ツリーの .claude/memories）。
// 作業ツリーの中でも kit の hook と同じ場所を読む。
func (c *Ctx) memoryImportDir(arg string) string {
	if arg != "" {
		return arg
	}
	if d := c.Env.Get("LOOPTRACK_LOOP_MEMORIES_DIR"); d != "" {
		return d
	}
	root := c.Root
	if g, err := gitOutput(root, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		if g = strings.TrimSpace(g); filepath.Base(g) == ".git" {
			root = filepath.Dir(g)
		}
	}
	return filepath.Join(root, ".claude", "memories")
}

func cmdMemoryImport(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path, err := c.ProjectPath("/memory")
	if err != nil {
		return err
	}
	dir := c.memoryImportDir(v.Str("dir"))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return i18n.Errorf("cli.err.memory.import_dir", "path", dir, "reason", err)
	}
	handoff := "handoff.md"
	if h := c.Env.Get("LOOPTRACK_LOOP_HANDOFF_FILE"); h != "" {
		handoff = filepath.Base(h)
	}
	names := make([]string, 0, len(ents))
	for _, ent := range ents {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".md") {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)
	label := c.memLabel()
	imported, skipped, rejected := 0, 0, 0
	var ignored []string
	for _, file := range names {
		kind, name, ok := memoryImportTarget(file, handoff)
		if !ok {
			if file != "README.md" {
				ignored = append(ignored, memoryImportName(c.Lang, file))
			}
			continue
		}
		shown := memoryImportName(c.Lang, file)
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			rejected++
			c.Println(i18n.T(c.Lang, "cli.memory.import.rejected", "file", shown, "reason", i18n.T(c.Lang, "cli.memory.import.unreadable")))
			continue
		}
		text := strings.TrimPrefix(string(raw), "\ufeff") // BOM は本文にも要約にも入れない
		// 送る前に手元の行番号で確かめる（本文は要約を移すので、サーバの行番号はファイルの行とずれる）
		if f, hit := domain.FindSecret(text); hit {
			rejected++
			c.Println(i18n.T(c.Lang, "cli.memory.import.secret", "file", shown, "line", f.Line, "label", f.Label))
			continue
		}
		req := api.Request{Method: http.MethodPost, Path: path, Body: jsonorder.NewObject().Set("scope", "project").Set("kind", kind).
			Set("name", name).Set("content", memoryImportContent(text)).Set("machine_label", label)}
		var body memResult
		_, err = memDo(cl, req, &body)
		var ae *api.Error
		var ce *api.ConnError
		switch {
		case err == nil:
			imported++
			c.Println(i18n.T(c.Lang, "cli.memory.import.done", "file", shown, "kind", kind, "name", name, "version", body.Memory.Version))
		case errors.As(err, &ae) && ae.Code == "memory_exists":
			skipped++
			c.Println(i18n.T(c.Lang, "cli.memory.import.skipped", "file", shown, "kind", kind, "name", name))
		case errors.As(err, &ce):
			return err // サーバに届かない。残りを試しても同じ
		default:
			rejected++
			c.Println(i18n.T(c.Lang, "cli.memory.import.rejected", "file", shown, "reason", Message(c.Lang, err)))
		}
	}
	if len(ignored) > 0 {
		c.Println(i18n.T(c.Lang, "cli.memory.import.ignored", "files", strings.Join(ignored, ", ")))
	}
	c.Println(i18n.T(c.Lang, "cli.memory.import.total", "imported", imported, "skipped", skipped, "rejected", rejected))
	if rejected > 0 {
		return &Exit{Code: 1}
	}
	return nil
}
