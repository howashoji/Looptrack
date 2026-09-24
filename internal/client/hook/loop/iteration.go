package loop

// session-start-iteration（以前の kit/loop/hooks/session-start-iteration.sh の Go 版）。
//
// 未解決の bug（issue の CLI の `list --type bug --json`）の件数と一覧・次の手順を文脈に入れる。取得できなければ「件数不明」
// （0 件と見なさない）。検証ゲートの実行は `looptrack gates` を案内する（以前の bash の gates.sh は配らない）。ゲートの Makefile（LOOPTRACK_LOOP_GATES_DIR。既定 .）が無く bug も無い間は何も出さない。
// CLI は issueCLI（LOOPTRACK_LOOP_ISSUE_CLI → 実行中の looptrack issue）。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// SessionStartIteration は実装イテレーションの状態（未解決の bug と検証ゲート）をセッション冒頭の文脈に入れる。
func SessionStartIteration(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	r := root(ev, e)
	gdir := e.env("LOOPTRACK_LOOP_GATES_DIR")
	if gdir == "" {
		gdir = "."
	}
	if !filepath.IsAbs(gdir) {
		gdir = filepath.Join(r, gdir)
	}

	var bugsJSON []byte
	if name, pre, ok := issueCLI(e, r); ok {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		out, _, err := e.run(c, e.Getwd(), []string{"CLAUDE_PROJECT_DIR=" + r, "LOOPTRACK_TIMEOUT=5"}, name, append(pre, "list", "--type", "bug", "--json")...)
		cancel()
		if err == nil {
			bugsJSON = out
		}
	}
	fetched := true
	var bugs []any
	var doc map[string]any
	if err := json.Unmarshal(bugsJSON, &doc); err != nil {
		fetched = false
	} else if items, ok := doc["items"].([]any); ok {
		bugs = items
	} else {
		fetched = false
	}
	hasGates := isFile(filepath.Join(gdir, "Makefile"))
	if !hasGates && len(bugs) == 0 {
		return hookio.Result{}, nil // 実装フェーズの前は黙る
	}

	bar := strings.Repeat("=", 64)
	lang := e.lang()
	out := []string{bar, i18n.T(lang, "loop.iteration.header"), bar}
	switch {
	case !fetched:
		out = append(out, i18n.T(lang, "loop.iteration.bugs_unknown"))
	case len(bugs) > 0:
		out = append(out, i18n.T(lang, "loop.iteration.bugs_open", "n", len(bugs)))
		for i, it := range bugs {
			if i >= 20 {
				break
			}
			m, _ := it.(map[string]any)
			id := "?"
			if v, ok := m["id"]; ok {
				id = formatValue(v)
			}
			title := ""
			if v, ok := m["title"]; ok {
				title = formatValue(v)
			}
			out = append(out, fmt.Sprintf("   - %s %s", id, title))
		}
	default:
		out = append(out, i18n.T(lang, "loop.iteration.bugs_none"))
	}
	out = append(out, i18n.T(lang, "loop.iteration.steps"), bar)
	return hookio.Result{Context: strings.Join(out, "\n")}, nil
}

// formatValue は JSON を解いた値の文字列化（真偽は True / False、値なしは None ＝以前の CLI の出力と同じ形）。
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if s := toStr(x); s != "" {
			return s
		}
		return "0"
	}
	b, _ := json.Marshal(v)
	return string(b)
}
