// Package guide は AI 向けの使い方（共通規則 + プロジェクト別ルール + 運用文書）を 1 つの Markdown にまとめる
// （CLI の guide・REST の /guide・MCP の guide ツールが同じものを返す。設計は DESIGN.md §5-5）。
//
// 共通規則（common.md）はサーバの版と一緒に変わる（ツール名・サブコマンドに依存する）ためバイナリに埋め込む。
// 本文は日英の 2 言語で、日本語が正本・英語は en/ に同じファイル名で置く（kit の rules と同じ置き方。DESIGN.md §9-6）。
// 組み立ての言語は呼び出し側が決めて渡す（REST は reqLang、MCP は接続の言語。グローバルに置かない）。
// 運用文書はプロジェクトごとに DB（project_guides）に持ち、looptrack project guide set で登録する
// （利用者が登録した自由文なので、どちらの言語でも原文のまま返す）。
package guide

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// commonFS は全プロジェクト共通の規則（docs/AI-GUIDE.md の要点）の日本語版と英語版。
//
//go:embed common.md en/common.md
var commonFS embed.FS

// Common は共通規則の本文を lang で返す。英語が読めなければ日本語（正本）に戻す。
func Common(lang i18n.Lang) string {
	if lang != i18n.JA {
		if b, err := commonFS.ReadFile("en/common.md"); err == nil && len(b) > 0 {
			return string(b)
		}
	}
	b, err := commonFS.ReadFile("common.md")
	if err != nil { // 埋め込みが壊れているときだけ。落とさずに空を返す
		return ""
	}
	return string(b)
}

// Input は 1 プロジェクト分の材料。
type Input struct {
	Slug, Name, Prefix string
	Width              int
	Role               string          // 呼び出した利用者の権限（viewer / editor / admin）
	Rules              json.RawMessage // projects.rules（NULL は nil）
	Doc                string          // 運用文書（未登録は空）
	DocSource          string
	DocUpdated         string
	// Agents は呼び出した利用者のこのプロジェクトへの導入済み通知（AI ごと 1 件。DESIGN.md §5-7）。
	// 「次に読むもの」を AI ごとの行で並べる（loop の有無は AI ごとに違う）。
	// 空（通知が 1 件も無い）なら「次に読むもの」を出さない
	Agents []AgentLoop
}

// AgentLoop は 1 つの AI の導入セットの状態。
type AgentLoop struct {
	Agent   string // claude-code / codex / copilot / other
	Label   string // 表示名（Claude Code / Codex / その他の AI）
	Loop    string // installed / declined / none（未選択・古い CLI）
	Version string // loop の版（installed のとき。無ければ空）
}

// Rule はプロジェクト別ルール 1 件の説明。
type Rule struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Output は組み立てた結果。
type Output struct {
	Markdown string `json:"markdown"`
	Common   string `json:"common"`
	Rules    []Rule `json:"rules"`
	Doc      string `json:"doc"`
	DocInfo  string `json:"doc_source"`
}

// Compose は Markdown を lang で組み立てる。
// Markdown の骨組み（見出しの段・表の区切り）は Go 側に置き、対訳表には文面だけを持たせる
// （日英で構成がずれないようにするため。構成の一致は internal/docscheck が確かめる）。
func Compose(lang i18n.Lang, in Input) Output {
	common := Common(lang)
	rules := DescribeRules(lang, in.Rules)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", i18n.T(lang, "guide.title", "name", in.Name, "slug", in.Slug))
	fmt.Fprintf(&b, "%s\n", i18n.T(lang, "guide.id_and_role",
		"example", fmt.Sprintf("%s-%0*d", in.Prefix, in.Width, 1), "role", in.Role))
	b.WriteString(i18n.T(lang, "guide.intro") + "\n\n")
	writeNextReading(&b, lang, in.Agents)
	b.WriteString("## 1. " + i18n.T(lang, "guide.section.common") + "\n\n")
	b.WriteString(strings.TrimSpace(common))
	b.WriteString("\n\n## 2. " + i18n.T(lang, "guide.section.rules") + "\n\n")
	if len(rules) == 0 {
		b.WriteString(i18n.T(lang, "guide.rules.none") + "\n")
	}
	for _, r := range rules {
		b.WriteString("- " + i18n.T(lang, "guide.rules.item", "description", r.Description, "name", r.Name) + "\n")
	}
	b.WriteString("\n## 3. " + i18n.T(lang, "guide.section.doc") + "\n\n")
	if strings.TrimSpace(in.Doc) == "" {
		b.WriteString(i18n.T(lang, "guide.doc.none", "slug", in.Slug) + "\n")
	} else {
		if in.DocSource != "" || in.DocUpdated != "" {
			b.WriteString("> " + i18n.T(lang, "guide.doc.source",
				"source", orDash(in.DocSource), "updated", orDash(in.DocUpdated)) + "\n\n")
		}
		b.WriteString(strings.TrimSpace(Demote(in.Doc, 2)))
		b.WriteString("\n")
	}
	return Output{Markdown: b.String(), Common: common, Rules: rules, Doc: in.Doc, DocInfo: in.DocSource}
}

// writeNextReading は「次に読むもの」を AI ごとの行の表で書く。guide は CLI・REST・MCP で同じ Markdown を返すため
// 呼んだ AI では出し分けず、利用者の導入済みの AI を全部並べて、読む AI が自分の行に従う。導入済み通知が無ければ書かない。
func writeNextReading(b *strings.Builder, lang i18n.Lang, agents []AgentLoop) {
	if len(agents) == 0 {
		return
	}
	b.WriteString(i18n.T(lang, "guide.next_reading.intro") + "\n\n")
	fmt.Fprintf(b, "| %s | %s | %s |\n| -- | -- | -- |\n",
		i18n.T(lang, "guide.next_reading.th.agent"), i18n.T(lang, "guide.next_reading.th.loop"),
		i18n.T(lang, "guide.next_reading.th.how"))
	for _, a := range agents {
		fmt.Fprintf(b, "| %s | %s | %s |\n", a.Label, loopCell(lang, a), howToLoop(lang, a))
	}
	b.WriteString("\n" + i18n.T(lang, "guide.next_reading.note") + "\n\n")
}

func loopCell(lang i18n.Lang, a AgentLoop) string {
	switch a.Loop {
	case "installed":
		if a.Version != "" {
			// 版は通知の自由な文字列。表を壊さない
			return i18n.T(lang, "guide.loop.installed_version", "version", strings.ReplaceAll(a.Version, "|", "\\|"))
		}
		return i18n.T(lang, "guide.loop.installed")
	case "declined":
		return i18n.T(lang, "guide.loop.declined")
	}
	return i18n.T(lang, "guide.loop.none")
}

func howToLoop(lang i18n.Lang, a AgentLoop) string {
	if a.Loop == "installed" {
		switch a.Agent {
		case "codex", "copilot": // どちらも AGENTS.md の loop 節
			return i18n.T(lang, "guide.how.agents_md")
		default:
			return i18n.T(lang, "guide.how.skill")
		}
	}
	return i18n.T(lang, "guide.how.minimal")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Demote は Markdown の見出しを n 段下げる（コードブロックの中は変えない。6 段より深くはしない）。
// 運用文書の「# 見出し」が guide 全体の見出し構造を壊さないようにする。
func Demote(md string, n int) string {
	lines := strings.Split(md, "\n")
	fence := ""
	for i, l := range lines {
		t := strings.TrimLeft(l, " ")
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			mark := t[:3]
			if fence == "" {
				fence = mark
			} else if fence == mark {
				fence = ""
			}
			continue
		}
		if fence != "" || !strings.HasPrefix(l, "#") {
			continue
		}
		level := len(l) - len(strings.TrimLeft(l, "#"))
		if level > 6 || (len(l) > level && l[level] != ' ' && l[level] != '\t') {
			continue
		}
		lines[i] = strings.Repeat("#", min(level+n, 6)) + l[level:]
	}
	return strings.Join(lines, "\n")
}

// DescribeRules は projects.rules の JSON をルールごとの説明にする。知らないキー（後から足したルール）は
// JSON をそのまま示す（説明の無いルールを黙って隠さない）。
func DescribeRules(lang i18n.Lang, raw json.RawMessage) []Rule {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []Rule
	for _, name := range names {
		var v struct {
			Statuses []string `json:"statuses"`
			Keyword  string   `json:"keyword"`
			Env      string   `json:"env"`
			Action   string   `json:"action"`
			Ask      string   `json:"ask"`
			Exempt   string   `json:"exempt"`
			Require  bool     `json:"require_on_close"`
			Case     string   `json:"case_pattern"`
			Prompts  bool     `json:"send_prompts"`
		}
		_ = json.Unmarshal(m[name], &v)
		st := strings.Join(v.Statuses, " / ")
		rawDesc := func() string { return i18n.T(lang, "guide.rule.raw", "json", string(m[name])) }
		var d string
		switch name {
		case "forbid_status":
			d = i18n.T(lang, "guide.rule.forbid_status", "statuses", st)
		case "require_comment_before":
			d = i18n.T(lang, "guide.rule.require_comment_before", "statuses", st)
		case "done_requires_keyword":
			if st == "" {
				st = "Done"
			}
			d = i18n.T(lang, "guide.rule.done_requires_keyword", "statuses", st, "keyword", v.Keyword)
		case "forbid_checkbox_pattern":
			d = i18n.T(lang, "guide.rule.forbid_checkbox_pattern", "env", v.Env, "action", v.Action, "ask", v.Ask)
			if v.Exempt != "" {
				d += i18n.T(lang, "guide.rule.forbid_checkbox_pattern.exempt", "exempt", v.Exempt)
			}
		case "usage":
			var parts []string
			if v.Require {
				if st == "" {
					st = "Done / Canceled"
				}
				parts = append(parts, i18n.T(lang, "guide.rule.usage.require_on_close", "statuses", st))
			}
			if v.Case != "" { // 案件ラベル別の集計
				parts = append(parts, i18n.T(lang, "guide.rule.usage.case_pattern", "pattern", v.Case))
			}
			if v.Prompts { // 指示文の作業名を送る設定
				parts = append(parts, i18n.T(lang, "guide.rule.usage.send_prompts"))
			}
			if len(parts) == 0 {
				parts = append(parts, rawDesc())
			}
			d = strings.Join(parts, i18n.T(lang, "guide.rule.sep"))
		case "verify": // 検証コマンド（§5-8-4）
			if !v.Require {
				d = rawDesc()
				break
			}
			if st == "" {
				st = "Done"
			}
			d = i18n.T(lang, "guide.rule.verify", "statuses", st)
		case "acceptance": // 受け入れ条件の記入（§9-1）
			if !v.Require {
				d = rawDesc()
				break
			}
			if st == "" {
				st = "Done"
			}
			d = i18n.T(lang, "guide.rule.acceptance", "statuses", st)
		default:
			d = rawDesc()
		}
		out = append(out, Rule{Name: name, Description: d})
	}
	return out
}
