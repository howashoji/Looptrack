// Package proc はプロセスの一覧（PID・親の PID・経過時間・コマンド行）を OS ごとの方法で取る。
//
// kit/loop の stop-runaway-background-process（終わらない子プロセスの検知）が、コーディング AI の本体の子の木をたどるのに使う。
// bash 版は `ps -eo pid,ppid,etime,command` に頼っていたので Windows で動かなかった。取り方は build tag で分ける:
//
//   - Linux: /proc/<pid>/stat（親・開始時刻）と /proc/<pid>/cmdline。プロセスを起動しない
//   - Windows: ToolHelp32（CreateToolhelp32Snapshot）で PID と親、GetProcessTimes で開始時刻、
//     NtQueryInformationProcess（ProcessCommandLineInformation）でコマンド行（取れなければ実行ファイル名）
//   - macOS・その他の unix: ps（-ww で切り詰めない）。sysctl の kern.proc.all はコマンド行を持たない（引数は KERN_PROCARGS2 を
//     PID ごとに引く必要がある）ので、ps の方が単純で確か
//
// コマンドを起動する処理（ps）は internal/client の下でだけ許される（internal/server の TestServerNeverExecutes）。
package proc

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Proc は 1 つのプロセス。
type Proc struct {
	PID  int
	PPID int
	// Elapsed は開始からの経過時間。ElapsedOK が false なら不明（ps の etime が読めなかった）。
	Elapsed   time.Duration
	ElapsedOK bool
	// Command はコマンド行（引数を空白でつないだもの。ps の command 列と同じ形）。
	Command string
}

// Minutes は経過時間の分（端数は切り捨て。ps の etime の秒を捨てる bash 版と同じ）。
func (p Proc) Minutes() int { return int(p.Elapsed / time.Minute) }

// ErrUnsupported はこの OS ではプロセスの一覧を取れないとき。
var ErrUnsupported = errors.New("proc: この OS ではプロセスの一覧を取れません")

// List は今のプロセスの一覧を返す（順不同）。取れなければ error（呼び出し側は fail-open で何もしない）。
func List(ctx context.Context) ([]Proc, error) { return list(ctx) }

var psLine = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\S+)\s+(.*)$`)

// ParsePS は `ps -eo pid,ppid,etime,command` の出力を読む（見出しなど形の合わない行は捨てる）。
// etime が読めない行も PID と親は残す（木をたどるのに要る）。ElapsedOK = false になる。
func ParsePS(text string) []Proc {
	var out []Proc
	for _, line := range strings.Split(text, "\n") {
		m := psLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err1 := strconv.Atoi(m[1])
		ppid, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		p := Proc{PID: pid, PPID: ppid, Command: m[4]}
		if d, ok := ParseEtime(m[3]); ok {
			p.Elapsed, p.ElapsedOK = d, true
		}
		out = append(out, p)
	}
	return out
}

// ParseEtime は ps の etime（[[日-]時:]分:秒）を読む。bash 版の minutes() と同じ規則:
// 「:」で分けて 3 つなら時・分・秒、2 つなら分・秒、それ以外は 0（読めない数字があれば ok = false）。
func ParseEtime(s string) (time.Duration, bool) {
	days := 0
	if d, rest, found := strings.Cut(s, "-"); found {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	var parts []int
	for _, x := range strings.Split(s, ":") {
		n, err := strconv.Atoi(x)
		if err != nil {
			return 0, false
		}
		parts = append(parts, n)
	}
	var h, m, sec int
	switch len(parts) {
	case 3:
		h, m, sec = parts[0], parts[1], parts[2]
	case 2:
		m, sec = parts[0], parts[1]
	default:
		return 0, true
	}
	return time.Duration(days*1440+h*60+m)*time.Minute + time.Duration(sec)*time.Second, true
}

// parseProcStat は Linux の /proc/<pid>/stat と cmdline を読む（OS に依らない形にしてテストできるようにしたもの）。
// stat は「pid (comm) state ppid … starttime（22 番目）」。comm は空白や括弧を含みうるので最後の ')' の後ろを分ける。
// 経過時間は uptime（/proc/uptime の秒）− starttime / ticks。cmdline は NUL 区切り（空ならカーネルのスレッドなので [comm]）。
func parseProcStat(pid int, stat, cmdline []byte, uptime float64, ticks float64) (Proc, bool) {
	s := string(stat)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return Proc{}, false
	}
	comm := s[open+1 : close]
	rest := strings.Fields(s[close+1:])
	if len(rest) < 20 {
		return Proc{}, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Proc{}, false
	}
	p := Proc{PID: pid, PPID: ppid}
	if start, err := strconv.ParseFloat(rest[19], 64); err == nil && ticks > 0 {
		el := uptime - start/ticks
		if el < 0 {
			el = 0
		}
		p.Elapsed, p.ElapsedOK = time.Duration(el*float64(time.Second)), true
	}
	cmdline = bytes.TrimRight(cmdline, "\x00")
	if len(cmdline) == 0 {
		p.Command = "[" + comm + "]"
	} else {
		p.Command = string(bytes.ReplaceAll(cmdline, []byte{0}, []byte{' '}))
	}
	return p, true
}
