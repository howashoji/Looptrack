//go:build windows

package proc

import (
	"context"
	"syscall"
	"time"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	processCommandLineInformation  = 60         // PROCESSINFOCLASS（Windows 8.1 以降）
	statusInfoLengthMismatch       = 0xC0000004 // NTSTATUS
)

var (
	ntdll                         = syscall.NewLazyDLL("ntdll.dll")
	procNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
)

// list は ToolHelp32 で PID と親を取り、開始時刻とコマンド行は OpenProcess で 1 つずつ引く（取れないものは空のまま）。
// 標準の syscall だけを使う（golang.org/x/sys は internal/server の検査が禁じる対象に含まれるため入れない）。
func list(ctx context.Context) ([]Proc, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snap)
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := syscall.Process32First(snap, &pe); err != nil {
		return nil, err
	}
	now := time.Now()
	var out []Proc
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p := Proc{PID: int(pe.ProcessID), PPID: int(pe.ParentProcessID), Command: syscall.UTF16ToString(pe.ExeFile[:])}
		fill(&p, now)
		out = append(out, p)
		if err := syscall.Process32Next(snap, &pe); err != nil {
			break // ERROR_NO_MORE_FILES
		}
	}
	return out, nil
}

// fill は開始時刻とコマンド行を足す（権限が無い・終わったプロセスはそのまま）。
func fill(p *Proc, now time.Time) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(p.PID))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err == nil && creation.Nanoseconds() > 0 {
		if el := now.Sub(time.Unix(0, creation.Nanoseconds())); el >= 0 {
			p.Elapsed, p.ElapsedOK = el, true
		}
	}
	if cmd := commandLine(h); cmd != "" {
		p.Command = cmd
	}
}

// unicodeString は UNICODE_STRING。
type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

// commandLine は NtQueryInformationProcess(ProcessCommandLineInformation) でコマンド行を取る（取れなければ ""）。
func commandLine(h syscall.Handle) string {
	if procNtQueryInformationProcess.Find() != nil {
		return ""
	}
	size := uint32(1024)
	for i := 0; i < 4; i++ {
		buf := make([]byte, size)
		var ret uint32
		st, _, _ := procNtQueryInformationProcess.Call(uintptr(h), processCommandLineInformation,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(size), uintptr(unsafe.Pointer(&ret)))
		if uint32(st) == statusInfoLengthMismatch && ret > size {
			size = ret
			continue
		}
		if st != 0 {
			return ""
		}
		us := (*unicodeString)(unsafe.Pointer(&buf[0]))
		if us.Buffer == nil || us.Length == 0 {
			return ""
		}
		return syscall.UTF16ToString(unsafe.Slice(us.Buffer, int(us.Length)/2))
	}
	return ""
}
