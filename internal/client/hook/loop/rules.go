package loop

// session-start-rules・user-prompt-rules（kit/loop/hooks/session-start-rules.sh・user-prompt-rules.sh の Go 版）。
//
// rules の *.md（名前順）から、見出しの直後に `<!-- looptrack:inject session -->`（UserPromptSubmit では `prompt`）の印がある節を
// 次の同じか上の階層の見出しまで抜き出して文脈に入れる。印の後ろに AI の名前を並べるとその AI のときだけ入れる。
// rules のディレクトリ: 引数（--prompt 以外）か LOOPTRACK_LOOP_RULES_DIR → <プロジェクト>/.claude/rules/looptrack-loop → kit に埋め込んだ
// kit/loop/rules（bash 版の「hook の隣の ../rules」に当たる）。
//
// 言語: 日本語が正本で、英語は同じディレクトリの en/ に同じファイル名で置く。i18n.FromEnv が EN と決めたときだけ
// en/ の本文を使い、訳が無いファイルは正本（日本語）に戻す。導入では日英の両方を置くので、LOOPTRACK_LANG を
// 切り替えれば次のセッションから注入される文面も切り替わる。
//
// bash 版との違い: 今の AI は配線の --agent（Event.Agent）で決める（bash 版は LOOPTRACK_LOOP_AGENT・CODEX_THREAD_ID から推した）。
// 抜き出しの規則は internal/client/kitinit の injectSections と同じ（変えるときは両方を直す）。

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/kit"
)

// SessionStartRules は印の付いた節をセッション冒頭の文脈に入れる（引数に --prompt があれば UserPromptRules と同じ）。
func SessionStartRules(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	return rules(ctx, ev, "session")
}

// UserPromptRules は `<!-- looptrack:inject prompt -->` の節（毎ターンの要点）を毎ターン文脈に入れる。
func UserPromptRules(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	return rules(ctx, ev, "prompt")
}

var (
	rulesHeading = compatRe(`^(#{1,6})\s+(.*)$`)
	injectAny    = compatRe(`^\s*<!--\s*looptrack:inject\b`)
	injectMarker = map[string]*regexp.Regexp{
		"session": compatRe(`^\s*<!--\s*looptrack:inject\s+session((?:\s+[a-z][a-z0-9-]*)*)\s*-->\s*$`),
		"prompt":  compatRe(`^\s*<!--\s*looptrack:inject\s+prompt((?:\s+[a-z][a-z0-9-]*)*)\s*-->\s*$`),
	}
)

// ruleFile は rules の 1 ファイル（名前と本文）。
type ruleFile struct{ name, text string }

func rules(ctx context.Context, ev hookio.Event, mode string) (hookio.Result, error) {
	e := envFrom(ctx)
	dirArg := ""
	for _, a := range e.Args {
		if a == "--prompt" {
			mode = "prompt"
		} else {
			dirArg = a
		}
	}
	lang := e.lang()
	files, ok := ruleFiles(ev, e, dirArg, lang)
	if !ok {
		return hookio.Result{}, nil
	}
	text := injectSections(lang, files, mode, string(ev.Agent))
	if text == "" {
		return hookio.Result{}, nil
	}
	return hookio.Result{Context: text}, nil
}

// ruleFiles は rules のディレクトリの *.md を読む（ディレクトリが無ければ ok = false）。
// lang が EN なら、同じ名前が <ディレクトリ>/en/ にあればそちらを読む（無ければ正本の日本語）。
func ruleFiles(ev hookio.Event, e *Env, dirArg string, lang i18n.Lang) ([]ruleFile, bool) {
	dir := dirArg
	if dir == "" {
		dir = e.env("LOOPTRACK_LOOP_RULES_DIR")
	}
	if dir == "" {
		if d := filepath.Join(root(ev, e), ".claude", "rules", "looptrack-loop"); isDir(d) {
			dir = d
		}
	}
	if dir == "" {
		return embeddedRules(lang), true
	}
	dir = e.abs(dir)
	if !isDir(dir) {
		return nil, false
	}
	var out []ruleFile
	for _, p := range globMD(dir) {
		name := filepath.Base(p)
		for _, c := range RuleFilePaths(dir, name, lang) {
			t, err := readText(c)
			if err != nil {
				continue
			}
			out = append(out, ruleFile{name, t})
			break
		}
	}
	return out, true
}

// RuleFilePaths は rules のディレクトリ dir の name を読む順のパス（EN なら en/ の訳を先に、無ければ正本）。
// 導入の verify（internal/client/kitinit の verifyRules）が「hook がどの言語の本文を注入するか」を
// 同じ規則で知るために公開している（規則を 2 か所に書くと、片方だけが言語に追随せず誤判定になる）。
func RuleFilePaths(dir, name string, lang i18n.Lang) []string {
	if lang != i18n.JA {
		return []string{filepath.Join(dir, kit.LangDir, name), filepath.Join(dir, name)}
	}
	return []string{filepath.Join(dir, name)}
}

// embeddedRules は kit に埋め込んだ kit/loop/rules/*.md（名前順。lang が EN なら en/ の訳を優先する）。
func embeddedRules(lang i18n.Lang) []ruleFile {
	var out []ruleFile
	for _, n := range kit.Names() { // 名前順
		rest, ok := strings.CutPrefix(n, "kit/loop/rules/")
		if !ok || strings.Contains(rest, "/") || strings.HasPrefix(rest, ".") || path.Ext(rest) != ".md" {
			continue // 訳（rules/en/…）は正本を見たときに ReadFileLang が拾う
		}
		b, err := kit.ReadFileLang(n, string(lang))
		if err != nil {
			continue
		}
		out = append(out, ruleFile{rest, decodeReplace(b)})
	}
	return out
}

// injectSections は rules の本文から mode（session / prompt）の印の付いた節を抜き出して、注入する文にする（無ければ ""）。
func injectSections(lang i18n.Lang, files []ruleFile, mode, agent string) string {
	marker := injectMarker[mode]
	var chunks []string
	for _, f := range files {
		lines := splitlines(f.text)
		fence := false
		i := 0
		for i < len(lines) {
			line := lines[i]
			if strings.HasPrefix(trimSpaceLeft(line), "```") {
				fence = !fence
			}
			var m []string
			if !fence {
				m = rulesHeading.FindStringSubmatch(line)
			}
			j := i + 1
			for m != nil && j < len(lines) && trimSpace(lines[j]) == "" {
				j++
			}
			var mk []string
			if m != nil && j < len(lines) {
				mk = marker.FindStringSubmatch(lines[j])
			}
			if mk != nil {
				if names := strings.Fields(mk[1]); len(names) > 0 && !contains(names, agent) {
					mk = nil // 印に AI の指定があり、今の AI が含まれない節は入れない（中身は読み飛ばさずに次を探す）
				}
			}
			if mk == nil {
				i++
				continue
			}
			level := len(m[1])
			var body []string
			k, inner := j+1, false
			for k < len(lines) {
				l := lines[k]
				if strings.HasPrefix(trimSpaceLeft(l), "```") {
					inner = !inner
				}
				if !inner {
					if h := rulesHeading.FindStringSubmatch(l); h != nil && len(h[1]) <= level {
						break
					}
				}
				if !injectAny.MatchString(l) && trimSpace(l) != "---" {
					body = append(body, l)
				}
				k++
			}
			for len(body) > 0 && trimSpace(body[0]) == "" {
				body = body[1:]
			}
			for len(body) > 0 && trimSpace(body[len(body)-1]) == "" {
				body = body[:len(body)-1]
			}
			if len(body) > 0 {
				if mode == "session" {
					chunks = append(chunks, fmt.Sprintf("■ %s（%s）\n%s", trimSpace(m[2]), f.name, strings.Join(body, "\n")))
				} else {
					var ne []string
					for _, l := range body {
						if trimSpace(l) != "" {
							ne = append(ne, l)
						}
					}
					chunks = append(chunks, strings.Join(ne, "\n"))
				}
			}
			i = k
		}
	}
	if len(chunks) == 0 {
		return ""
	}
	if mode == "session" {
		bar := strings.Repeat("=", 64)
		return fmt.Sprintf("%s\n%s\n%s\n%s\n%s", bar, i18n.T(lang, "loop.rules.header"), bar,
			strings.Join(chunks, "\n"+strings.Repeat("-", 64)+"\n"), bar)
	}
	return strings.Join(chunks, "\n")
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
