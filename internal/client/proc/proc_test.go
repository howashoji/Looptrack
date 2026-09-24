package proc

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]struct {
		mins int
		ok   bool
	}{
		"00:31:00": {31, true}, "15:26:10": {926, true}, "1-02:00:00": {1560, true}, "05:09": {5, true},
		"42": {0, true}, "x:00": {0, false}, "a-01:00:00": {0, false}, "": {0, false},
	} {
		d, ok := ParseEtime(in)
		if ok != want.ok || (ok && int(d/time.Minute) != want.mins) {
			t.Errorf("ParseEtime(%q) = %v %v, want %d 分 %v", in, d, ok, want.mins, want.ok)
		}
	}
}

func TestParsePS(t *testing.T) {
	ps := ParsePS("  PID  PPID     ELAPSED COMMAND\n 30451 99999 15:26:10 /bin/zsh -c eval 'x'\n 7 1 bad cmd\nnot a line\n")
	if len(ps) != 2 {
		t.Fatalf("%d 行: %+v", len(ps), ps)
	}
	if p := ps[0]; p.PID != 30451 || p.PPID != 99999 || !p.ElapsedOK || p.Minutes() != 926 || p.Command != "/bin/zsh -c eval 'x'" {
		t.Errorf("%+v", p)
	}
	if p := ps[1]; p.PID != 7 || p.ElapsedOK {
		t.Errorf("etime が読めない行は経過時間なしで残す: %+v", p)
	}
}

func TestParseProcStat(t *testing.T) {
	stat := []byte("1234 (my (odd) name) S 99 1234 1234 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 50000 1000000 100 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0\n")
	p, ok := parseProcStat(1234, stat, []byte("/bin/bash\x00-c\x00sleep 60\x00"), 1000, 100)
	if !ok || p.PPID != 99 || p.Command != "/bin/bash -c sleep 60" || !p.ElapsedOK || p.Elapsed != 500*time.Second {
		t.Errorf("%+v %v", p, ok)
	}
	p, ok = parseProcStat(2, []byte("2 (kthreadd) S 0 0 0 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0\n"), nil, 1000, 100)
	if !ok || p.Command != "[kthreadd]" {
		t.Errorf("カーネルのスレッド: %+v %v", p, ok)
	}
	if _, ok := parseProcStat(3, []byte("3 no paren"), nil, 1, 100); ok {
		t.Error("形の合わない stat は ok = false")
	}
}

// TestList は実際の OS から一覧が取れ、自分が入っていること（取れない OS では飛ばす）。
func TestList(t *testing.T) {
	ps, err := List(context.Background())
	if err == ErrUnsupported {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.PID == os.Getpid() {
			if p.PPID != os.Getppid() || !p.ElapsedOK || p.Command == "" {
				t.Errorf("自分の親・経過時間・コマンド行: %+v（親 %d）", p, os.Getppid())
			}
			return
		}
	}
	t.Errorf("自分（%d）が一覧に無い（%d 件）", os.Getpid(), len(ps))
}
