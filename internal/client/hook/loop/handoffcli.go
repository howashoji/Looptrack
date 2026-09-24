package loop

// handoff（looptrack handoff append|compact）: 引き継ぎのファイルへの書き込みの入口。
//
// なぜ要るか: 記憶・引き継ぎのパスを**本体の作業ツリー**で解決するようにしたので、作業ツリーの中で動く
// セッションも本体の 1 つの handoff.md を読み書きする。skill の手順は「読んで全文を書き直す」形なので、
// 2 つのセッションが近い時刻に書くと**後から書いた方が前の内容を丸ごと消す**（last-write-wins）。
// 消された側にはエラーが出ないので、黙って失われる。
//
//	looptrack handoff append [--title <見出し>] [--file <パス>] [--session <ID>] [<本文> | -]
//	looptrack handoff compact --sha <読んだ時点の SHA-256> [--drop-marks] [--lock-ttl <秒>] [<本文> | -]
//
// append は O_APPEND で 1 回だけ書く。同じファイルへ同時に書いても、それぞれの追記は混ざらないし消えない
// （読んでから書き戻す経路を通らないので、そもそも他人の内容を持たない）。ロックは掛けない。
//
// compact は全文の書き直し（圧縮）専用。危険なのはこちらだけなので、こちらにだけ守りを付ける:
//
//   - 版照合: 呼ぶ側が「読んだ時点の SHA-256」を渡す。実際のファイルと食い違えば**何も書かずに拒否**する
//     （ほかのセッションが書いた内容を、読み直さずに上書きしないため）。
//   - 排他: <ファイル>.lock を O_EXCL で作る。失効時間を過ぎた古いロックは奪える（落ちたセッションの
//     ロックで、以後ずっと圧縮できなくなるのを避けるため）。
//
// 追記する節には、見出しの直後にセッションの印（時刻つき）を 1 行入れる。鮮度ガードが
// 「このセッションが今回の完了より後に書いたか」を、この印の時刻で見る。
// **印の形は書く側でここだけが持たない。**形の定数（writerMarkPrefix / writerMarkSuffix）と組み立て
// （writerMarkAt）は読む側（鮮度ガード）に 1 つだけ置き、書く側はそれを呼ぶ。2 か所に同じ文字列を
// 置くと、片方を変えただけで判定が黙って外れる（書いても差し戻されるのに、テストは緑のまま）。
// 印が入ったファイルは鮮度ガードが厳密な判定に切り替わるので、並行セッションが去って 1 セッションに
// 戻ったときのために、印を落とす道（compact --drop-marks）を用意してある。
//
// 終了コード: 0 = 成功 / 1 = 入出力の失敗 / 2 = 使い方の誤り / 3 = 衝突（SHA の不一致・ロック中）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	clientenv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 終了コード。
const (
	handoffExitOK       = 0
	handoffExitError    = 1
	handoffExitUsage    = 2
	handoffExitConflict = 3
)

// handoffLockTTLDefault はロックの失効時間の既定（これより古いロックは奪える）。
const handoffLockTTLDefault = 10 * time.Minute

// handoffHeadingPrefix は節の見出し。追記も圧縮も、この見出しで始まる本文しか書かない
// （読む側は節の単位で切り出すので、途中から始まると節が壊れる）。
const handoffHeadingPrefix = "## "

// handoffSHALen は SHA-256 の 16 進表記の長さ。
const handoffSHALen = 64

// handoffOpts は解いた引数。
type handoffOpts struct {
	file      string
	title     string
	session   string
	sha       string
	dropMarks bool
	lockTTL   time.Duration
	hasTTL    bool
	args      []string // 位置引数（本文。無いか "-" なら標準入力）
}

// Handoff は looptrack handoff（env が nil なら実際の環境）。
func Handoff(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, env *Env) int {
	if env == nil {
		env = &Env{}
	}
	e := env.withDefaults()
	lang := e.lang()
	if len(args) == 0 {
		fmt.Fprint(stderr, i18n.T(lang, "loop.handoff.cli.usage"))
		return handoffExitUsage
	}
	sub := args[0]
	if sub != "append" && sub != "compact" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.unknown_subcommand", "name", sub))
		fmt.Fprint(stderr, i18n.T(lang, "loop.handoff.cli.usage"))
		return handoffExitUsage
	}
	opt, bad := handoffParse(args[1:])
	if bad != "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.bad_flag", "name", bad))
		fmt.Fprint(stderr, i18n.T(lang, "loop.handoff.cli.usage"))
		return handoffExitUsage
	}
	body, code := handoffReadBody(opt, stdin, stderr, lang)
	if code != handoffExitOK {
		return code
	}
	if sub == "append" {
		return handoffAppend(e, lang, opt, body, stdout, stderr)
	}
	return handoffCompact(e, lang, opt, body, stdout, stderr)
}

// handoffErr は「エラー: …」を標準エラーへ書く。
func handoffErr(w io.Writer, lang i18n.Lang, msg string) {
	fmt.Fprintln(w, i18n.T(lang, "cmd.prefix.error", "msg", msg))
}

// handoffParse は引数を解く（--name=値 と --name 値 の両方を受ける）。戻り値の bad は誤った選択肢の名前。
func handoffParse(args []string) (handoffOpts, string) {
	var o handoffOpts
	take := func(i *int, inline string, ok bool) (string, bool) {
		if ok {
			return inline, true
		}
		if *i+1 < len(args) {
			*i++
			return args[*i], true
		}
		return "", false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.args = append(o.args, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") {
			o.args = append(o.args, a)
			continue
		}
		name, inline, hasInline := strings.Cut(a, "=")
		var ok bool
		switch name {
		case "--file":
			o.file, ok = take(&i, inline, hasInline)
		case "--title":
			o.title, ok = take(&i, inline, hasInline)
		case "--session":
			o.session, ok = take(&i, inline, hasInline)
		case "--sha":
			o.sha, ok = take(&i, inline, hasInline)
		case "--lock-ttl":
			var v string
			v, ok = take(&i, inline, hasInline)
			if ok {
				n, err := strconv.Atoi(trimSpace(v))
				if err != nil || n < 0 {
					return o, name
				}
				o.lockTTL, o.hasTTL = time.Duration(n)*time.Second, true
			}
		case "--drop-marks":
			o.dropMarks, ok = true, true
		default:
			return o, name
		}
		if !ok {
			return o, name
		}
	}
	return o, ""
}

// handoffReadBody は本文（位置引数が無いか "-" なら標準入力）。
func handoffReadBody(o handoffOpts, stdin io.Reader, stderr io.Writer, lang i18n.Lang) (string, int) {
	if len(o.args) > 1 {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.too_many_args"))
		return "", handoffExitUsage
	}
	if len(o.args) == 1 && o.args[0] != "-" {
		return o.args[0], handoffExitOK
	}
	if stdin == nil {
		return "", handoffExitOK
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", "-", "err", err.Error()))
		return "", handoffExitError
	}
	return decodeReplace(b), handoffExitOK
}

// handoffCLIFile は書き込むファイル（--file → 鮮度ガードと同じ解決）。
//
// 解決を新しく決めない: 鮮度ガードが見るファイルと 1 バイトでも違うと、書いたのに差し戻される。
func handoffCLIFile(e *Env, override string) string {
	if override != "" {
		return e.abs(override)
	}
	return handoffFile(handoffCLIEvent(e), e)
}

// handoffCLIEvent は hook の入力が無いときの代わり（プロジェクトのルートの決め方は gates と同じ）。
func handoffCLIEvent(e *Env) hookio.Event {
	r := e.env("CLAUDE_PROJECT_DIR")
	if r == "" {
		r = gitTop(e.Getwd())
	}
	if r == "" {
		r = e.Getwd()
	}
	return hookio.Event{ProjectDir: r}
}

// handoffSessionID はこのセッションの ID（--session → シェルに渡る環境変数）。
// 記号を落とすのは hook の sessionFile と同じ（印が hook の印と食い違わないように）。
func handoffSessionID(e *Env, explicit string) string {
	v := trimSpace(explicit)
	if v == "" {
		v = session.Detect(clientenv.FromFunc(func(k string) string { return e.env(k) })).Session
	}
	return nonIDChar.ReplaceAllString(v, "")
}

// handoffAppend は節を末尾に足す（O_APPEND で 1 回だけ書く）。
func handoffAppend(e *Env, lang i18n.Lang, o handoffOpts, body string, stdout, stderr io.Writer) int {
	body = trimSpace(body)
	if body == "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.empty_body"))
		return handoffExitUsage
	}
	sid := handoffSessionID(e, o.session)
	heading, rest := handoffHeading(body, o.title, sid, e.Now())
	var b strings.Builder
	// 直前の内容がどう終わっていても節を分けたいので、必ず空行から始める（読み直さずに済ませるため）
	b.WriteString("\n")
	b.WriteString(heading + "\n")
	if sid != "" {
		// 時刻つきの印（鮮度ガードは、この時刻を完了のイベントと比べる）
		b.WriteString(writerMarkAt(sid, e.Now()) + "\n")
	} else {
		fmt.Fprintln(stderr, i18n.T(lang, "loop.handoff.cli.no_session"))
	}
	if rest != "" {
		b.WriteString("\n" + rest + "\n")
	}
	p := handoffCLIFile(e, o.file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p, "err", err.Error()))
		return handoffExitError
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p, "err", err.Error()))
		return handoffExitError
	}
	// 1 回の Write にまとめる（分けて書くと、同時に書いたほかのセッションの節と行が混ざる）
	if _, err := f.Write([]byte(b.String())); err != nil {
		f.Close()
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p, "err", err.Error()))
		return handoffExitError
	}
	if err := f.Close(); err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p, "err", err.Error()))
		return handoffExitError
	}
	fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.appended", "path", p, "sha", handoffFileSHA(p)))
	return handoffExitOK
}

// handoffHeading は追記する節の見出しと、その下に置く本文を返す。
//
// 本文が既に "## " の見出しで始まっていればそれを使い、無ければ作る（節の途中から始まる追記を作らないため）。
func handoffHeading(body, title, sid string, now time.Time) (heading, rest string) {
	if t := trimSpace(title); t != "" {
		return handoffHeadingPrefix + t, body
	}
	first, after, _ := strings.Cut(body, "\n")
	if first = trimSpaceRight(first); strings.HasPrefix(first, handoffHeadingPrefix) {
		return first, trimSpace(after)
	}
	label := sid
	if label == "" {
		label = "-"
	}
	return handoffHeadingPrefix + now.Format("2006-01-02 15:04:05") + " " + label, body
}

// handoffCompact は全文を書き直す（版照合とロックつき）。
func handoffCompact(e *Env, lang i18n.Lang, o handoffOpts, body string, stdout, stderr io.Writer) int {
	want := strings.ToLower(trimSpace(o.sha))
	if want == "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.need_sha"))
		return handoffExitUsage
	}
	if !isHex(want) || len(want) != handoffSHALen {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.bad_sha", "sha", o.sha))
		return handoffExitUsage
	}
	body = trimSpace(body)
	if o.dropMarks {
		body = trimSpace(handoffDropMarks(body))
	}
	if body == "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.empty_body"))
		return handoffExitUsage
	}
	// 書き出す本文は必ず節の見出しから始める（読む側は節の単位で切り出す）
	if !strings.HasPrefix(body, handoffHeadingPrefix) {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.need_heading", "prefix", trimSpaceRight(handoffHeadingPrefix)))
		return handoffExitUsage
	}
	p := handoffCLIFile(e, o.file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p, "err", err.Error()))
		return handoffExitError
	}
	release, busy, err := handoffAcquireLock(p, handoffLockTTL(e, o), handoffSessionID(e, o.session), e.Now())
	if busy != "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.locked", "path", p+".lock", "age", busy))
		return handoffExitConflict
	}
	if err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p+".lock", "err", err.Error()))
		return handoffExitError
	}
	defer release()

	cur, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p, "err", err.Error()))
		return handoffExitError
	}
	got := sha256hex(cur)
	if got != want {
		// ここで返るとき、ファイルは 1 バイトも変えていない
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.conflict", "path", p, "want", want, "got", got))
		return handoffExitConflict
	}
	if err := handoffReplace(p, body+"\n"); err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", p, "err", err.Error()))
		return handoffExitError
	}
	fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.compacted", "path", p, "sha", handoffFileSHA(p)))
	return handoffExitOK
}

// handoffDropMarks はセッションの印の行だけを落とす（見出しと本文は残す）。
//
// 印が 1 つでもあるファイルは鮮度ガードが厳密な判定に切り替わるので、並行セッションが去った後に
// 元の判定へ戻す道が要る。落とすのは明示されたときだけ（黙って落とすと、並行作業中の判定が壊れる）。
//
// 前置きと閉じだけを見るので、時刻つきの印（`<ID>` の後ろに時刻が入る形）も、時刻の無い古い印も落ちる。
func handoffDropMarks(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := trimSpace(ln)
		if strings.HasPrefix(t, writerMarkPrefix) && strings.HasSuffix(t, writerMarkSuffix) {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// handoffLockTTL はロックの失効時間（--lock-ttl → LOOPTRACK_LOOP_HANDOFF_LOCK_TTL（秒）→ 既定）。
func handoffLockTTL(e *Env, o handoffOpts) time.Duration {
	if o.hasTTL {
		return o.lockTTL
	}
	if v := trimSpace(e.env("LOOPTRACK_LOOP_HANDOFF_LOCK_TTL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return handoffLockTTLDefault
}

// handoffAcquireLock は <ファイル>.lock を O_EXCL で作る。
//
// 既にあるときは、失効時間を過ぎていれば奪う（落ちたセッションのロックで、以後ずっと圧縮できなくなるため）。
// 過ぎていなければ busy に経過時間を入れて返す（このとき release は呼ばれない）。
func handoffAcquireLock(target string, ttl time.Duration, owner string, now time.Time) (release func(), busy string, err error) {
	lock := target + ".lock"
	create := func() (*os.File, error) {
		return os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	f, err := create()
	if err != nil && os.IsExist(err) {
		age := time.Duration(-1)
		if st, e2 := os.Stat(lock); e2 == nil {
			age = now.Sub(st.ModTime())
		}
		if age < 0 || age <= ttl {
			return func() {}, handoffAge(age), nil
		}
		// 失効したロックを奪う（奪う競争に負けたら busy として返す）
		if e2 := os.Remove(lock); e2 != nil && !os.IsNotExist(e2) {
			return func() {}, handoffAge(age), nil
		}
		f, err = create()
		if err != nil && os.IsExist(err) {
			return func() {}, handoffAge(0), nil
		}
	}
	if err != nil {
		return func() {}, "", err
	}
	fmt.Fprintf(f, "pid=%d session=%s at=%s\n", os.Getpid(), owner, now.Format(time.RFC3339))
	f.Close()
	return func() { os.Remove(lock) }, "", nil
}

// handoffAge は経過時間の表示（分からなければ ?）。
func handoffAge(d time.Duration) string {
	if d < 0 {
		return "?"
	}
	return d.Truncate(time.Second).String()
}

// handoffReplace は同じディレクトリの一時ファイルへ書いてから置き換える（途中で落ちても半端な本文を残さない）。
func handoffReplace(p, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// handoffFileSHA はファイルの SHA-256（読めなければ空）。
func handoffFileSHA(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return sha256hex(b)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
