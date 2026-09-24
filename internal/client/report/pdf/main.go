package pdf

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/client/report"
	"github.com/howashoji/looptrack/internal/client/textenc"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Usage は looptrack report pdf の使い方。
func Usage(lang i18n.Lang) string { return i18n.T(lang, "report.pdf.usage", "env", FontEnv) }

// Env は Main が読む環境（テストで差し替える）。
type Env struct {
	Getenv func(string) string
	Home   string
	Lang   i18n.Lang
}

// Main は looptrack report pdf（引数は以前の実装と同じ）。終了コードを返す。
func Main(args []string, stdout, stderr io.Writer, env Env) int {
	lang := env.Lang
	var reportPath, contentPath, out string
	check := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		switch name {
		case "-h", "--help":
			fmt.Fprint(stdout, Usage(lang))
			return 0
		case "--check":
			if hasValue {
				return usageErr(stderr, lang, i18n.T(lang, "report.pdf.err.check_no_value"))
			}
			check = true
			continue
		case "--report", "--content", "--out":
		default:
			return usageErr(stderr, lang, i18n.T(lang, "report.pdf.err.unknown_arg", "arg", a))
		}
		if !hasValue {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return usageErr(stderr, lang, i18n.T(lang, "report.pdf.err.missing_value", "name", name))
			}
			i++
			value = args[i]
		}
		switch name {
		case "--report":
			reportPath = value
		case "--content":
			contentPath = value
		case "--out":
			out = value
		}
	}
	if reportPath == "" || contentPath == "" {
		return usageErr(stderr, lang, i18n.T(lang, "report.pdf.err.required"))
	}
	read := func(p string) (any, error) {
		b, err := os.ReadFile(expand(p, env.Home))
		if err != nil {
			return nil, err
		}
		return report.Decode(textenc.Decode(b)) // BOM・UTF-16（PowerShell 5.1 の `>`）を UTF-8 に揃える
	}
	rep, err := read(reportPath)
	var content any
	if err == nil {
		content, err = read(contentPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, i18n.T(lang, "cmd.prefix.error", "msg",
			i18n.T(lang, "report.pdf.err.read_input", "reason", err)))
		return 1
	}
	if err := run(rep, content, out, check, stdout, stderr, env); err != nil {
		fmt.Fprintln(stderr, i18n.T(lang, "cmd.prefix.error", "msg", err))
		return 1
	}
	return 0
}

func usageErr(w io.Writer, lang i18n.Lang, msg string) int {
	fmt.Fprintf(w, "%slooptrack report pdf: error: %s\n", Usage(lang), msg)
	return 2
}

func run(rep, content any, out string, check bool, stdout, stderr io.Writer, env Env) error {
	lang := env.Lang
	blocks, err := report.Build(lang, rep, content)
	if err != nil {
		return err
	}
	if check {
		headings, tables := 0, 0
		for _, b := range blocks {
			switch b.Kind {
			case report.Heading:
				headings++
			case report.TableKind:
				tables++
			}
		}
		total := rep.(map[string]any)["total_tokens"]
		fmt.Fprintln(stdout, i18n.T(lang, "report.pdf.check_ok", "headings", headings, "tables", tables,
			"tokens", report.Num(total)))
		return nil
	}
	if out == "" {
		return i18n.Errorf("report.pdf.err.no_out")
	}
	out, err = filepath.Abs(expand(out, env.Home))
	if err != nil {
		return err
	}
	getenv := env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	font, err := FindFont(lang, getenv, env.Home, func(b []byte) error { _, err := NewMetrics(b); return err },
		func(msg string) { fmt.Fprintln(stderr, i18n.T(lang, "report.pdf.warn", "msg", msg)) })
	if err != nil {
		return err
	}
	title := strings.TrimSpace(fmt.Sprint(content.(map[string]any)["title"]))
	data, _, err := Render(lang, blocks, title, font)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "PDF: %s\n", out)
	return nil
}
