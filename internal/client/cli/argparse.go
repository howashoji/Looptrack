package cli

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// 引数の解釈（以前の CLI（1.0.0 より前）と互換）。
//
// 互換にしているもの: サブコマンドの入れ子・位置引数（ちょうど 1 個・省略可・1 個以上）・--opt value と --opt=value・
// 長いオプションの一意な前方一致・真偽のフラグ・int / float / 選択肢の検査・必須・-- の後はすべて位置引数・
// 負の数（-5・-0.5）はオプションでなく値・同じオプションは後の指定が勝つ・-h / --help。
// 誤りは以前の CLI と同じく「usage: …」と「<prog>: error: …」を標準エラーに出して終了コード 2。
//
// 以前の CLI との違い（互換の範囲を広げる向きだけ）: 位置引数はオプションの前後に分かれていても順に割り当てる
// （以前の CLI は 1 個以上を取る位置引数の後ろにオプションを挟むと、残りを unrecognized にした）。usage と help の
// 文面は同じ形だが折り返しはしない。

// Kind は値の型。
type Kind int

const (
	Str Kind = iota
	Bool
	Int
	Float
)

// Arg は位置引数（Name が - で始まらない）かオプション。
type Arg struct {
	Name     string // "--status"・"-h"・"id"
	Dest     string // 既定は Name から（--blocked-by → blocked_by）
	Kind     Kind
	Nargs    string // ""（1 つ）・"?"・"+"（位置引数だけ）
	Default  any    // 省略時の値（nil なら None）
	Choices  []string
	Required bool
	Metavar  string
	Help     string
}

func (a *Arg) positional() bool { return !strings.HasPrefix(a.Name, "-") }

func (a *Arg) dest() string {
	if a.Dest != "" {
		return a.Dest
	}
	return strings.ReplaceAll(strings.TrimLeft(a.Name, "-"), "-", "_")
}

func (a *Arg) metavar() string {
	if a.Metavar != "" {
		return a.Metavar
	}
	if len(a.Choices) > 0 {
		return "{" + strings.Join(a.Choices, ",") + "}"
	}
	if a.positional() {
		return a.Name
	}
	return strings.ToUpper(a.dest())
}

// Command はサブコマンド（入れ子にできる）。
type Command struct {
	Name        string
	Help        string
	Description string
	Args        []*Arg
	Subs        []*Command
	SubDest     string // サブコマンドの名前を入れる dest（usage の action など）
	Run         func(*Ctx, *Values) error
	Planned     string // 未実装のとき、実装する予定のイシューの ID
}

// Values は解釈した引数（dest → 値）。値は string・bool・int64・float64・[]string・nil。
type Values struct {
	m    map[string]any
	Path []string // 選んだサブコマンドの並び（issue の後ろ。例 [usage ledger add]）
}

func (v *Values) Get(dest string) any { return v.m[dest] }

// Str は文字列（None は ""）。
func (v *Values) Str(dest string) string {
	s, _ := v.m[dest].(string)
	return s
}

// IsSet は None でないか。
func (v *Values) IsSet(dest string) bool { return v.m[dest] != nil }

func (v *Values) Bool(dest string) bool { b, _ := v.m[dest].(bool); return b }

func (v *Values) Int(dest string) int64 { i, _ := v.m[dest].(int64); return i }

func (v *Values) Float(dest string) float64 { f, _ := v.m[dest].(float64); return f }

func (v *Values) List(dest string) []string { l, _ := v.m[dest].([]string); return l }

// UsageError は引数の誤り（終了コード 2）。
type UsageError struct {
	Prog  string
	Usage string
	Msg   string
}

func (e *UsageError) Error() string { return e.Msg }

// helpRequested は -h / --help（help を出して終了コード 0）。
type helpRequested struct{ text string }

func (h *helpRequested) Error() string { return "help" }

var negativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// looksLikeOption はオプションとみなす字面か（- だけ・負の数は値）。
func looksLikeOption(s string) bool {
	return strings.HasPrefix(s, "-") && s != "-" && !negativeNumber.MatchString(s)
}

// Parse は args を cmd に照らして解釈する。prog は usage に出す名前（例 "looptrack issue"）。
func Parse(cmd *Command, prog string, args []string) (*Values, *Command, error) {
	v := &Values{m: map[string]any{}}
	leaf, extras, err := parseInto(cmd, prog, args, v)
	if err != nil {
		return nil, nil, err
	}
	if len(extras) > 0 {
		return nil, nil, &UsageError{Prog: prog, Usage: usageLine(cmd, prog), Msg: "unrecognized arguments: " + strings.Join(extras, " ")}
	}
	return v, leaf, nil
}

func parseInto(cmd *Command, prog string, args []string, v *Values) (*Command, []string, error) {
	fail := func(format string, a ...any) error {
		return &UsageError{Prog: prog, Usage: usageLine(cmd, prog), Msg: fmt.Sprintf(format, a...)}
	}
	// 既定値
	for _, a := range cmd.Args {
		if a.Kind == Bool {
			v.m[a.dest()] = false
		} else {
			v.m[a.dest()] = a.Default
		}
	}
	var positionals []string
	var extras []string
	seen := map[string]bool{}
	i := 0
	afterDashes := false
	for i < len(args) {
		tok := args[i]
		if tok == "--" && !afterDashes {
			afterDashes = true
			i++
			continue
		}
		if afterDashes || !looksLikeOption(tok) {
			// サブコマンドは最初の位置引数で選び、残りをすべてサブコマンドに渡す
			if len(cmd.Subs) > 0 {
				sub := findSub(cmd, tok)
				if sub == nil {
					return nil, nil, fail("argument %s: invalid choice: %s (choose from %s)", subDest(cmd), quoteChoice(tok), choiceList(subNames(cmd)))
				}
				v.m[subDest(cmd)] = sub.Name
				v.Path = append(v.Path, sub.Name)
				leaf, ex, err := parseInto(sub, prog+" "+sub.Name, args[i+1:], v)
				if err != nil {
					return nil, nil, err
				}
				if err := checkRequired(cmd, prog, seen, nil); err != nil {
					return nil, nil, err
				}
				return leaf, append(extras, ex...), nil
			}
			positionals = append(positionals, tok)
			i++
			continue
		}
		name, value, hasValue := strings.Cut(tok, "=")
		if !strings.HasPrefix(tok, "--") {
			name, value, hasValue = tok, "", false // 短いオプション（-h）は = を取らない
		}
		if name == "-h" || name == "--help" || (strings.HasPrefix(name, "--") && len(name) > 2 && strings.HasPrefix("--help", name) && matchCount(cmd, name) == 0) {
			return nil, nil, &helpRequested{text: helpText(cmd, prog)}
		}
		a, err := findOption(cmd, name)
		if err != nil {
			return nil, nil, fail("%s", err.Error())
		}
		if a == nil {
			extras = append(extras, tok)
			i++
			continue
		}
		seen[a.dest()] = true
		if a.Kind == Bool {
			if hasValue {
				return nil, nil, fail("argument %s: ignored explicit argument %s", optionNames(a), quoteChoice(value))
			}
			v.m[a.dest()] = true
			i++
			continue
		}
		if !hasValue {
			if i+1 >= len(args) || looksLikeOption(args[i+1]) {
				return nil, nil, fail("argument %s: expected one argument", optionNames(a))
			}
			value = args[i+1]
			i++
		}
		i++
		conv, err := convert(a, value)
		if err != nil {
			return nil, nil, fail("argument %s: %s", optionNames(a), err.Error())
		}
		v.m[a.dest()] = conv
	}
	if len(cmd.Subs) > 0 {
		if err := checkRequired(cmd, prog, seen, nil); err != nil {
			return nil, nil, err
		}
		return nil, nil, fail("the following arguments are required: %s", subDest(cmd))
	}
	// 位置引数を順に割り当てる
	var pos []*Arg
	for _, a := range cmd.Args {
		if a.positional() {
			pos = append(pos, a)
		}
	}
	var missing []string
	rest := positionals
	for idx, a := range pos {
		switch a.Nargs {
		case "?":
			if len(rest) > 0 {
				conv, err := convert(a, rest[0])
				if err != nil {
					return nil, nil, fail("argument %s: %s", a.Name, err.Error())
				}
				v.m[a.dest()] = conv
				rest = rest[1:]
			}
		case "+":
			// 後ろの必須の位置引数の分を残す
			need := 0
			for _, b := range pos[idx+1:] {
				if b.Nargs == "" {
					need++
				}
			}
			n := len(rest) - need
			if n < 1 {
				missing = append(missing, a.Name)
				continue
			}
			var list []string
			for _, s := range rest[:n] {
				conv, err := convert(a, s)
				if err != nil {
					return nil, nil, fail("argument %s: %s", a.Name, err.Error())
				}
				list = append(list, conv.(string))
			}
			v.m[a.dest()] = list
			rest = rest[n:]
		default:
			if len(rest) == 0 {
				missing = append(missing, a.Name)
				continue
			}
			conv, err := convert(a, rest[0])
			if err != nil {
				return nil, nil, fail("argument %s: %s", a.Name, err.Error())
			}
			v.m[a.dest()] = conv
			rest = rest[1:]
		}
	}
	extras = append(extras, rest...)
	if err := checkRequired(cmd, prog, seen, missing); err != nil {
		return nil, nil, err
	}
	return cmd, extras, nil
}

func checkRequired(cmd *Command, prog string, seen map[string]bool, missingPos []string) error {
	var missing []string
	missing = append(missing, missingPos...)
	for _, a := range cmd.Args {
		if !a.positional() && a.Required && !seen[a.dest()] {
			missing = append(missing, optionNames(a))
		}
	}
	if len(missing) > 0 {
		return &UsageError{Prog: prog, Usage: usageLine(cmd, prog), Msg: "the following arguments are required: " + strings.Join(missing, ", ")}
	}
	return nil
}

func convert(a *Arg, s string) (any, error) {
	var out any
	switch a.Kind {
	case Int:
		n, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(s), "_", ""), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int value: %s", quoteChoice(s))
		}
		out = n
	case Float:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid float value: %s", quoteChoice(s))
		}
		out = f
	default:
		out = s
	}
	if len(a.Choices) > 0 {
		ok := false
		for _, c := range a.Choices {
			if c == s {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("invalid choice: %s (choose from %s)", quoteChoice(s), choiceList(a.Choices))
		}
	}
	return out, nil
}

func findSub(cmd *Command, name string) *Command {
	for _, s := range cmd.Subs {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func subNames(cmd *Command) []string {
	out := make([]string, len(cmd.Subs))
	for i, s := range cmd.Subs {
		out[i] = s.Name
	}
	return out
}

func subDest(cmd *Command) string {
	if cmd.SubDest != "" {
		return cmd.SubDest
	}
	return "cmd"
}

func matchCount(cmd *Command, prefix string) int {
	n := 0
	for _, a := range cmd.Args {
		if !a.positional() && strings.HasPrefix(a.Name, prefix) {
			n++
		}
	}
	return n
}

// findOption は完全一致、無ければ一意な前方一致（--で始まるものだけ）。見つからなければ nil。曖昧ならエラー。
func findOption(cmd *Command, name string) (*Arg, error) {
	for _, a := range cmd.Args {
		if !a.positional() && a.Name == name {
			return a, nil
		}
	}
	if !strings.HasPrefix(name, "--") {
		return nil, nil
	}
	var hits []*Arg
	for _, a := range cmd.Args {
		if !a.positional() && strings.HasPrefix(a.Name, name) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 0:
		return nil, nil
	case 1:
		return hits[0], nil
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.Name
	}
	return nil, fmt.Errorf("ambiguous option: %s could match %s", name, strings.Join(names, ", "))
}

func optionNames(a *Arg) string { return a.Name }

func quoteChoice(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func choiceList(c []string) string {
	q := make([]string, len(c))
	for i, s := range c {
		q[i] = quoteChoice(s)
	}
	return strings.Join(q, ", ")
}

// usageLine は usage の 1 行（折り返さない）。
func usageLine(cmd *Command, prog string) string {
	parts := []string{"usage: " + prog, "[-h]"}
	for _, a := range cmd.Args {
		if a.positional() {
			continue
		}
		s := a.Name
		if a.Kind != Bool {
			s += " " + a.metavar()
		}
		if !a.Required {
			s = "[" + s + "]"
		}
		parts = append(parts, s)
	}
	for _, a := range cmd.Args {
		if !a.positional() {
			continue
		}
		switch a.Nargs {
		case "?":
			parts = append(parts, "["+a.metavar()+"]")
		case "+":
			parts = append(parts, a.metavar()+" ["+a.metavar()+" ...]")
		default:
			parts = append(parts, a.metavar())
		}
	}
	if len(cmd.Subs) > 0 {
		parts = append(parts, "{"+strings.Join(subNames(cmd), ",")+"}", "...")
	}
	return strings.Join(parts, " ")
}

// helpText は -h の出力（以前の CLI と同じ節の並び）。
func helpText(cmd *Command, prog string) string {
	var b strings.Builder
	b.WriteString(usageLine(cmd, prog) + "\n")
	desc := cmd.Description
	if desc == "" {
		desc = cmd.Help
	}
	if desc != "" {
		b.WriteString("\n" + desc + "\n")
	}
	var pos, opt []*Arg
	for _, a := range cmd.Args {
		if a.positional() {
			pos = append(pos, a)
		} else {
			opt = append(opt, a)
		}
	}
	if len(pos) > 0 || len(cmd.Subs) > 0 {
		b.WriteString("\npositional arguments:\n")
		if len(cmd.Subs) > 0 {
			fmt.Fprintf(&b, "  {%s}\n", strings.Join(subNames(cmd), ","))
			for _, s := range cmd.Subs {
				fmt.Fprintf(&b, "    %-20s %s\n", s.Name, s.Help)
			}
		}
		for _, a := range pos {
			fmt.Fprintf(&b, "  %-22s %s\n", a.metavar(), a.Help)
		}
	}
	b.WriteString("\noptions:\n")
	fmt.Fprintf(&b, "  %-22s %s\n", "-h, --help", "show this help message and exit")
	for _, a := range opt {
		name := a.Name
		if a.Kind != Bool {
			name += " " + a.metavar()
		}
		fmt.Fprintf(&b, "  %-22s %s\n", name, a.Help)
	}
	return b.String()
}

// printUsageError は以前の CLI と同じ形で誤りを出す。
func printUsageError(w io.Writer, e *UsageError) {
	fmt.Fprintf(w, "%s\n%s: error: %s\n", e.Usage, e.Prog, e.Msg)
}
