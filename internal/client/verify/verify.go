// Package verify は `looptrack issue verify` の中身（DESIGN §5-8-2・§5-11）。
//
// サーバはコマンドを実行しない。GET /issues/{id}/verify の commands を手元でその順に実行し（Run）、
// 同じ body_sha256 で POST /issues/{id}/verify に 1 回で送る（Send）。
//
// 1 コマンドの実行（RunCommand）:
//
//   - シェル: POSIX は PATH の bash、無ければ sh。
//     Windows は bash（Git for Windows）を探す。WSL の起動口（System32\bash.exe・WindowsApps\bash.exe）は使わない
//     （WSL の中で動き、作業ディレクトリ・PATH・環境変数が Windows 側と違う）。見つからなければ sh、それも無ければ
//     起動できない失敗にする（cmd.exe・PowerShell には落とさない。検証コマンドは bash -c の前提で書かれるため）。
//     探す順は shellrule.go の WindowsShell。
//   - 子孫まで止める: POSIX は新しいプロセスグループで起動し、時間切れはグループに SIGTERM → 3 秒後に SIGKILL。
//     Windows は一時停止のまま起動して Job Object に入れてから再開し、時間切れは TerminateJobObject で木ごと止める。
//   - 標準入力は空（/dev/null・NUL）。標準出力と標準エラーは 1 本の pipe にまとめ、末尾 64KiB だけ保つ。
//     本体が終わっても背景の子孫が pipe を開いたままなら 5 秒待ってからグループ（Job）ごと止める。
//   - 送る出力は、保った末尾の途中で切れた文字を捨て、不正な UTF-8 を置き換え、秘密をマスクしてから末尾 4,000 バイトに切る。
//     Windows では UTF-8 として不正な出力を先に ANSI コードページ（cp932 など）として読み直す（codepage.go）。
//
// コマンドを起動する処理は internal/client の下でだけ許される（internal/server の TestServerNeverExecutes）。
package verify

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
)

// 既定値と上限（以前の CLI と同じ）。
const (
	DefaultTimeout      = 600 * time.Second  // 1 コマンドの上限（--timeout）
	DefaultTotalTimeout = 1800 * time.Second // 全体の上限（超えた残りは skipped）
	OutputBytes         = 4000               // 送る出力の末尾（サーバは 4,096 バイトで切り直す）
	KeepBytes           = 65536              // 実行中に保つ出力の末尾（マスクしてから切るための余白）
	ShowLines           = 20                 // 手元に表示する失敗の出力（末尾の行数）
)

// 待ち時間（以前の CLI と同じ。テストだけが短くする）。
var (
	termGrace = 3 * time.Second // 時間切れで SIGTERM を送ってから SIGKILL までの猶予
	pipeGrace = 5 * time.Second // 本体の終了後、出力が閉じるのを待つ時間
	killGrace = 2 * time.Second // 止めた後、出力が閉じるのを待つ時間
)

// 結果の状態（POST の results[].status）。
const (
	StatusOK      = "ok"
	StatusFail    = "fail"
	StatusTimeout = "timeout"
	StatusSkipped = "skipped"
)

// Result は 1 コマンドの結果（POST の results の 1 件）。ExitCode が nil なら null（時間切れ・起動できない・skipped）。
// Cached は出力に go test の結果キャッシュの印（(cached)）があったか（注記。成否は変えない）。
type Result struct {
	Command    string
	Status     string
	ExitCode   *int
	DurationMS int64
	OutputTail string
	Cached     bool
}

// Skipped は全体の上限を超えて実行しなかったコマンドの結果。
func Skipped(command string) Result {
	return Result{Command: command, Status: StatusSkipped}
}

// Root は実行場所: CLAUDE_PROJECT_DIR → git のルート → カレント
// （AI の Bash のカレントは作業中に変わるため）。projectDir は CLAUDE_PROJECT_DIR の値。
func Root(ctx context.Context, projectDir string) string {
	if projectDir != "" {
		return projectDir
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return filepath.FromSlash(s)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// Env は検証コマンドの環境: IM_ と LOOPTRACK_ で始まる変数を除き（テストが CLI を起動して本番に書き込むのを防ぐ。
// 足す）、LOOPTRACK_VERIFY_ID と GOFLAGS（-count=1）を足す。base は os.Environ() の形。
// 今の CLI は LOOPTRACK_ を読み、以前の CLI は IM_ を読んでいたので、両方を除く。
//
// GOFLAGS に -count=1 を足すのは、go test の結果キャッシュが返ると「走っていないテスト」で
// 「検証コマンド 1/1 成功」の記録が残るため。go 以外のコマンド（bash・node・shellcheck …）は
// GOFLAGS を読まないので影響しない。go build / go vet / go list / go mod tidy / go run は、
// GOFLAGS の中の「そのコマンドが知らないフラグ」を無視する（go1.27.0 darwin/arm64 で実測。すべて exit 0）。
// 利用者が既に GOFLAGS を持っていればその値を残して後ろに足し、-count が既にあればそのままにする。
func Env(base []string, issueID string) []string {
	out := make([]string, 0, len(base)+2)
	goflags := ""
	for _, kv := range base {
		name, value := kv, ""
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name, value = kv[:i], kv[i+1:]
		}
		if runtime.GOOS == "windows" { // Windows の環境変数の名前は大文字小文字を区別しない
			name = strings.ToUpper(name)
		}
		if strings.HasPrefix(name, "IM_") || strings.HasPrefix(name, "LOOPTRACK_") {
			continue
		}
		if name == "GOFLAGS" { // 最後に 1 つだけ足し直す（同じ名前を 2 つ渡すと、どちらが効くかが環境で変わる）
			goflags = value
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GOFLAGS="+withoutTestCache(goflags), "LOOPTRACK_VERIFY_ID="+issueID)
}

// NoTestCache は go test の結果キャッシュを無効にするフラグ（GOFLAGS に足す）。
const NoTestCache = "-count=1"

// withoutTestCache は GOFLAGS に -count=1 を足す（-count が既にあれば利用者の指定をそのまま残す）。
func withoutTestCache(goflags string) string {
	for _, f := range strings.Fields(goflags) {
		if f == "-count" || strings.HasPrefix(f, "-count=") {
			return goflags
		}
	}
	if strings.TrimSpace(goflags) == "" {
		return NoTestCache
	}
	return goflags + " " + NoTestCache
}

// cachedLine は go test が結果キャッシュを返した行（`ok  \tpkg\t(cached)`。カバレッジ付きは後ろに続きがある）。
// 「(cached)」という語が出力のどこかにあるだけでは印にしない（偶然の一致で誤って注記を付けないため）。
var cachedLine = regexp.MustCompile(`(?m)^ok[ \t]+[^ \t]+[ \t]+\(cached\)(?:[ \t]|$)`)

// Cached は出力に go test の結果キャッシュの印があるか（注記。成否の判定には使わない）。
// 判定は go test の書式にだけ当たる。make・turbo など印を出さない実行系は捕まえられない。
func Cached(out string) bool { return cachedLine.MatchString(out) }

// Output は保った出力の末尾を送る形にする: 保つときに切れた文字の残りを捨て、
// 不正な UTF-8 を U+FFFD に置き換え（以前の CLI と同じ置き換えの単位）、マスクしてから末尾 4,000 バイトに切る。
func Output(raw []byte) string {
	i := 0
	for i < len(raw) && i < 3 && raw[i]&0xC0 == 0x80 {
		i++
	}
	// 先頭の切れた文字を除いても UTF-8 として不正なら、Windows の ANSI コードページの出力とみて読み直す（codepage.go）
	// （Shift_JIS の先頭バイトは UTF-8 の続きのバイトと重なるので、読み直すのは切る前の raw）
	if !utf8.Valid(raw[i:]) {
		if s, ok := decodeLegacy(codePage(), raw); ok {
			return domain.TailBytes(domain.MaskSecrets(s), OutputBytes)
		}
	}
	return domain.TailBytes(domain.MaskSecrets(DecodeReplace(raw[i:])), OutputBytes)
}

// DecodeReplace は bytes.decode("utf-8", errors="replace") と同じ置き換えをする。
// 不正な並びは「正しい並びの最長の先頭部分」ごとに 1 つの U+FFFD にする（Go の strings.ToValidUTF8 は連続した不正を 1 つにまとめ、
// utf8.DecodeRune は 1 バイトずつ置き換えるので、どちらとも違う）。
func DecodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) + 16)
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r != utf8.RuneError || n > 1 {
			sb.Write(b[:n])
			b = b[n:]
			continue
		}
		sb.WriteRune(utf8.RuneError)
		b = b[maximalSubpart(b):]
	}
	return sb.String()
}

// maximalSubpart は b の先頭の不正な並びの長さ（1 以上。Unicode の「maximal subpart」）。
func maximalSubpart(b []byte) int {
	c := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF) // 2 バイト目の範囲
	switch {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c == 0xED:
		need, hi = 2, 0x9F
	case c >= 0xE1 && c <= 0xEF:
		need = 2
	case c == 0xF0:
		need, lo = 3, 0x90
	case c == 0xF4:
		need, hi = 3, 0x8F
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(b); k++ {
		if k > 0 {
			lo, hi = 0x80, 0xBF
		}
		if b[n] < lo || b[n] > hi {
			break
		}
		n++
	}
	return n
}

// tailBuffer は書かれたものの末尾 max バイトだけを保つ（読む goroutine と止める側から触るので排他する）。
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return bytes.Clone(t.buf)
}
