package loop

// kit/loop/manifest.json の検査（rules・skill の項目と kit/loop の実体の対応・hook の配線の形・汎用性）と、
// Go 版の登録表との対応（manifest の hook がすべて登録表にあり、イベントが同じで、打ち切りが manifest の timeout より短い）。
// hook は looptrack hook <名前> で動く（runner: looptrack）ので kit/loop に実体のファイルは無い。以前の bash 版（hooks/*.sh・
// scripts/gates.sh）と verify の項目は撤去した。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/kit"
	"time"
)

type manifestEntry struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Runner  string          `json:"runner"`
	Event   string          `json:"event"`
	Matcher *string         `json:"matcher"`
	Timeout json.RawMessage `json:"timeout"`
	Order   json.RawMessage `json:"order"`
	raw     map[string]json.RawMessage
}

func loadManifest(t *testing.T) (int, []manifestEntry) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(kitLoop(t), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version int               `json:"version"`
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var out []manifestEntry
	for _, r := range m.Entries {
		var e manifestEntry
		if err := json.Unmarshal(r, &e); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(r, &e.raw)
		out = append(out, e)
	}
	return m.Version, out
}

func intOf(r json.RawMessage) (int, bool) {
	var n float64
	if json.Unmarshal(r, &n) != nil || n != float64(int(n)) {
		return 0, false
	}
	return int(n), true
}

func TestManifest(t *testing.T) {
	loop := kitLoop(t)
	version, entries := loadManifest(t)
	if version != 1 {
		t.Errorf("manifest の version は 1: %d", version)
	}
	var files []string
	filepath.WalkDir(loop, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(loop, p)
		// 訳（rules/en/x.md）は正本と同じ 1 本なので manifest には載せない（kit/README.ja.md「本文の言語」）
		if rel = filepath.ToSlash(rel); rel != "manifest.json" && !kit.IsTranslation(rel) {
			files = append(files, rel)
		}
		return nil
	})
	names := map[string]int{}
	for _, e := range entries {
		names[e.Name]++
	}

	t.Run("1. name の重複が無く、rules・skill は実体と 1 対 1・hook は実体が無い", func(t *testing.T) {
		for n, c := range names {
			if c > 1 {
				t.Errorf("manifest の name に重複: %s", n)
			}
		}
		for _, f := range files {
			if names[f] == 0 {
				t.Errorf("kit/loop のファイルが manifest に無い: %s", f)
			}
		}
		fs := map[string]bool{}
		for _, f := range files {
			fs[f] = true
		}
		for _, e := range entries {
			switch e.Kind {
			case "rules", "skill":
				if !fs[e.Name] {
					t.Errorf("manifest の name が実在しない: %s", e.Name)
				}
			case "hook":
				if fs[e.Name] || strings.Contains(e.Name, ".") {
					t.Errorf("hook は looptrack hook で動かすので実体のファイル・拡張子を持たない: %s", e.Name)
				}
			default:
				t.Errorf("kind は hook・rules・skill のどれか: %s（%s）", e.Name, e.Kind)
			}
			if _, ok := e.raw["verify"]; ok {
				t.Errorf("verify のキーは撤去した: %s", e.Name)
			}
		}
	})

	t.Run("2. kind と置き場", func(t *testing.T) {
		kind := map[string]string{"hooks": "hook", "rules": "rules", "skills": "skill"}
		for _, e := range entries {
			if kind[strings.SplitN(e.Name, "/", 2)[0]] != e.Kind {
				t.Errorf("kind が置き場のディレクトリと合わない: %s", e.Name)
			}
		}
	})

	events := map[string]bool{"SessionStart": true, "UserPromptSubmit": true, "PreToolUse": true, "PostToolUse": true,
		"Stop": true, "SubagentStop": true, "SessionEnd": true}
	t.Run("3. hook の配線", func(t *testing.T) {
		seen := map[string][]string{}
		for _, e := range entries {
			if e.Kind != "hook" {
				continue
			}
			timeout, okT := intOf(e.Timeout)
			order, okO := intOf(e.Order)
			ok := events[e.Event] && okT && timeout > 0 && okO && e.Runner == "looptrack"
			if e.Event == "PreToolUse" || e.Event == "PostToolUse" {
				ok = ok && e.Matcher != nil && *e.Matcher != ""
			}
			for _, col := range []string{"codex", "copilot"} {
				r, present := e.raw[col]
				if !present {
					ok = false
					continue
				}
				if string(r) == "null" {
					continue
				}
				var c map[string]any
				if json.Unmarshal(r, &c) != nil || !events[fmt.Sprint(c["event"])] {
					ok = false
					continue
				}
				// Codex の Stop は知らせるだけ（実物で未確認）。Copilot の Stop は差し戻してよい（CLI 1.0.86 で確認）
				if e.Event == "Stop" && col == "codex" && c["block"] != false {
					ok = false
				}
			}
			if !ok {
				t.Errorf("%s: event / matcher / timeout / order / runner / codex / copilot", e.Name)
			}
			k := fmt.Sprintf("%s/%d", e.Event, order)
			seen[k] = append(seen[k], e.Name)
		}
		for k, v := range seen {
			if len(v) > 1 {
				t.Errorf("同じ event で order が重なる: %s %v", k, v)
			}
		}
	})

	t.Run("3b. rules / skill の codex・copilot", func(t *testing.T) {
		inject := regexp.MustCompile(`^\s*<!--\s*looptrack:inject\s+session((?:\s+[a-z][a-z0-9-]*)*)\s*-->\s*$`)
		for _, e := range entries {
			if e.Kind != "rules" && e.Kind != "skill" {
				continue
			}
			allowed := "sections"
			if e.Kind == "skill" {
				allowed = "description"
			}
			for _, col := range []string{"codex", "copilot"} {
				r, present := e.raw[col]
				if !present {
					t.Errorf("%s: %s が無い", e.Name, col)
					continue
				}
				if string(r) == "null" {
					continue
				}
				var c map[string]any
				if json.Unmarshal(r, &c) != nil || c["agents_md"] != allowed {
					t.Errorf("%s: %s = %s", e.Name, col, r)
					continue
				}
				if allowed == "sections" {
					f, err := os.Open(filepath.Join(loop, e.Name))
					if err != nil {
						t.Error(err)
						continue
					}
					found := false
					sc := bufio.NewScanner(f)
					for sc.Scan() {
						if m := inject.FindStringSubmatch(sc.Text()); m != nil {
							if ns := strings.Fields(m[1]); len(ns) == 0 || contains(ns, col) {
								found = true
							}
						}
					}
					f.Close()
					if !found {
						t.Errorf("%s: sections なのに %s に入る looptrack:inject session の節が無い", e.Name, col)
					}
				}
			}
		}
	})

	t.Run("3c. 全項目に codex と copilot の列がある", func(t *testing.T) {
		for _, e := range entries {
			for _, col := range []string{"codex", "copilot"} {
				if _, ok := e.raw[col]; !ok {
					t.Errorf("%s に %s が無い（null でもよい）", e.Name, col)
				}
			}
		}
	})

	t.Run("4. 汎用性（固有の語が入っていない）", func(t *testing.T) {
		// 語はこのファイル自身に当たらないよう組み立てる
		namesRe := strings.Join([]string{"h" + "pc", "co" + "muse", "req" + "weave", "acade" + "meia", "how" + "ashoji", "link" + "see"}, "|")
		patterns := []struct {
			label string
			rx    *regexp.Regexp
			// notAlpha は前後が英字でないこと（以前の検査の (?<![a-z])…(?![a-z])。RE2 に後読みが無いので照合後に確かめる）
			notAlpha bool
		}{
			{"固定のパス（利用者のホーム）", regexp.MustCompile(`/` + `Users/|/` + `home/[a-z]`), false},
			{"特定のプロジェクト・ツールの名前", regexp.MustCompile(`(?i)(` + namesRe + `)`), true},
			{"特定の課題管理サービスの名前", regexp.MustCompile(`\bLin` + `ear\b`), false},
			{"特定のプロジェクトの課題・決定・文書の番号", regexp.MustCompile(`\b(CO` + `M|RE` + `Q|HP` + `C|DE` + `C|BD|N?FR)-[A-Z]*-?[0-9]`), false},
			{"特定のプロジェクトのマイルストーン", regexp.MustCompile(`\bM` + `0\s*〜\s*M[0-9]`), false},
			{"記憶のファイル名の直書き", regexp.MustCompile(`\b(feed` + `back|pro` + `ject)_[a-z0-9_]+\.md`), false},
		}
		isAlpha := func(b byte) bool { return (b|0x20) >= 'a' && (b|0x20) <= 'z' }
		for _, p := range patterns {
			for _, f := range append(append([]string(nil), files...), "manifest.json") {
				b, _ := os.ReadFile(filepath.Join(loop, f))
				for i, line := range strings.Split(string(b), "\n") {
					for _, m := range p.rx.FindAllStringIndex(line, -1) {
						if p.notAlpha && ((m[0] > 0 && isAlpha(line[m[0]-1])) || (m[1] < len(line) && isAlpha(line[m[1]]))) {
							continue
						}
						t.Errorf("%s: %s:%d: %s", p.label, f, i+1, line)
						break
					}
				}
			}
		}
	})

	t.Run("Go 版の登録表と manifest の hook が 1 対 1", func(t *testing.T) {
		var want []string
		for _, e := range entries {
			if e.Kind != "hook" {
				continue
			}
			name := strings.TrimPrefix(e.Name, "hooks/")
			want = append(want, name)
			reg, ok := Lookup(name)
			if !ok {
				t.Errorf("登録表に無い: %s", name)
				continue
			}
			if string(reg.Event) != e.Event {
				t.Errorf("%s: イベント 登録表 %s / manifest %s", name, reg.Event, e.Event)
			}
			if timeout, _ := intOf(e.Timeout); reg.Timeout <= 0 || reg.Timeout >= time.Duration(timeout)*time.Second {
				t.Errorf("%s: 打ち切り %s は manifest の timeout %d 秒より短くする", name, reg.Timeout, timeout)
			}
		}
		sort.Strings(want)
		if strings.Join(want, ",") != strings.Join(Names(), ",") {
			t.Errorf("登録表 %v / manifest %v", Names(), want)
		}
	})
}
