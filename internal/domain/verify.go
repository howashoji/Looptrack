package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 検証コマンド（DESIGN.md §5-8-1・§5-8-2）。
// 本文の「## 検証コマンド」節（英語の別名 ## Verify commands も同じ。見出しは heading.go の VerifyHeading）から、verify で実行するコマンドを取り出す。
// 例は testdata/verify_commands.json（verify_test.go が読む）。

// 上限（§5-8-1）。超えたら verify を 400 で拒否し、本文を直すよう返す。
const (
	MaxVerifyCommands   = 20
	MaxVerifyCommandLen = 1000
	// MaxVerifyOutput はサーバが保つ出力の末尾（バイト）。CLI は 4,000 バイトで切って送る
	MaxVerifyOutput = 4096
)

var (
	fenceOpen    = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	h2           = regexp.MustCompile(`^##[ \t]`)
	inlineBullet = regexp.MustCompile("^(?:[-*]|[0-9]+\\.)[ \t]+`([^`]+)`$")
)

// fenceState はコードブロック（``` / ~~~）の内外を追う。
type fenceState struct {
	marker string // 開いているフェンスの文字列（空なら外）
}

// step は行を読み、その行がフェンスの行（開き・閉じ）なら true を返す。
func (f *fenceState) step(line string) bool {
	if f.marker == "" {
		if m := fenceOpen.FindStringSubmatch(line); m != nil {
			f.marker = m[1]
			return true
		}
		return false
	}
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) <= 3 && strings.HasPrefix(t, f.marker[:3]) {
		run := len(t) - len(strings.TrimLeft(t, f.marker[:1]))
		if run >= len(f.marker) && strings.TrimSpace(t[run:]) == "" {
			f.marker = ""
			return true
		}
	}
	return false
}

// SectionLines は、コードブロックの外にある heading に一致する最初の行から、次の（コードブロックの外の）`## ` 見出しまでの行を返す。
// 見出しが無ければ found は false。
func SectionLines(body string, heading *regexp.Regexp) (lines []string, found bool) {
	var fs fenceState
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		inCode := fs.marker != ""
		fence := fs.step(line)
		if !inCode && !fence {
			if found && h2.MatchString(line) {
				return lines, true
			}
			if !found && heading.MatchString(line) {
				found = true
				continue
			}
		}
		if found {
			lines = append(lines, line)
		}
	}
	return lines, found
}

// VerifyCommands は本文の「## 検証コマンド」節からコマンドを取り出す（§5-8-1）。
//   - 節の中の fenced code block の各行（空行と # で始まる行は捨てる）
//   - fenced の外では、行全体が 1 つのインラインコードの箇条書き（- `…` / * `…` / 1. `…`）だけ
//
// 節が無ければ空（nil）。上限の検査は CheckVerifyCommands。
func VerifyCommands(body string) []string {
	lines, found := SectionLines(body, VerifyHeading)
	if !found {
		return nil
	}
	var out []string
	var fs fenceState
	for _, line := range lines {
		inCode := fs.marker != ""
		if fs.step(line) {
			continue
		}
		t := strings.TrimSpace(line)
		if inCode {
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			out = append(out, t)
			continue
		}
		if m := inlineBullet.FindStringSubmatch(t); m != nil {
			if c := strings.TrimSpace(m[1]); c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

// VerifyLimitError は上限を超えたときの理由（code は verify_commands_too_many / verify_command_too_long）。
//
// Msg が文面の正本（表示側が Msg.In(lang) で利用者の言語にする）。Message は判定を呼んだ側が渡した
// 言語（要求の言語）で作る（Violation と同じ形）。
type VerifyLimitError struct {
	Code    string
	Message string
	Msg     i18n.Msg
}

func (e *VerifyLimitError) Error() string { return e.Message }

// limitErr は Message（lang の文面）と Msg を同じ ID から作る。
func limitErr(lang i18n.Lang, code string, m i18n.Msg) *VerifyLimitError {
	return &VerifyLimitError{Code: code, Message: m.In(lang), Msg: m}
}

// CheckVerifyCommands は上限（20 コマンド・1 コマンド 1,000 文字）を確かめる。lang は理由の文面の言語。
func CheckVerifyCommands(lang i18n.Lang, id string, cmds []string) *VerifyLimitError {
	if len(cmds) > MaxVerifyCommands {
		return limitErr(lang, "verify_commands_too_many", i18n.M("domain.verify.err.too_many",
			"id", id, "count", len(cmds), "max", MaxVerifyCommands))
	}
	for i, c := range cmds {
		if n := utf8.RuneCountInString(c); n > MaxVerifyCommandLen {
			return limitErr(lang, "verify_command_too_long", i18n.M("domain.verify.err.too_long",
				"id", id, "index", i+1, "count", n, "max", MaxVerifyCommandLen))
		}
	}
	return nil
}

// BodySHA256 は本文の版（§5-8-3）: 本文（frontmatter とコメント節を除いた body_main）の SHA-256（16 進）。
// コメント・状態変更では変わらない（verify 自身のコメントや close --comment で記録が無効にならない）。
func BodySHA256(bodyMain string) string {
	sum := sha256.Sum256([]byte(bodyMain))
	return hex.EncodeToString(sum[:])
}

// VerifyCommand は verify を実行するコマンド（CLI）。
func VerifyCommand(id string) string {
	return "looptrack issue verify " + id
}

// NoVerifyCommandsMsg は節が無いときの文言（CLI の GET …/verify の message と MCP verify_issue で共通）。
// 文面の中の「## 検証コマンド」は見出しの綴りそのもの（訳すと利用者が書けない）なので、対訳ごとに
// その言語の見出し（英語は ## Verify commands）を書く。どちらの見出しも常に認める（heading.go）。
func NoVerifyCommandsMsg(id string) i18n.Msg {
	return i18n.M("domain.verify.err.no_commands", "id", id)
}

// 出力のマスク（§5-8-2）。CLI が送る前にかけ、サーバも同じ規則で再度かける。
var secretMasks = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)(-----BEGIN [A-Z ]*PRIVATE KEY-----).*?(-----END [A-Z ]*PRIVATE KEY-----|\z)`), "${1}***${2}"},
	{regexp.MustCompile(`imp_[A-Za-z0-9_-]+`), "imp_***"},
	{regexp.MustCompile(`(?i)bearer\s+\S+`), "Bearer ***"},
	{regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key)(\s*[=:]\s*)\S+`), "${1}${2}***"},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`), "sk-***"},
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "AKIA***"},
}

// MaskSecrets は出力の中の秘密らしい文字列を置き換える（何度かけても同じ結果）。
func MaskSecrets(s string) string {
	for _, m := range secretMasks {
		s = m.re.ReplaceAllString(s, m.repl)
	}
	return s
}

// TailBytes は s の末尾 n バイト以内を、UTF-8 の文字境界で切って返す。
func TailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// CleanVerifyOutput はサーバで保つ出力: マスクしてから末尾 4,096 バイトに切る
// （切ってからマスクすると、秘密の前置き（token= 等）だけが切れて値が残ることがある）。
func CleanVerifyOutput(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return TailBytes(MaskSecrets(s), MaxVerifyOutput)
}
