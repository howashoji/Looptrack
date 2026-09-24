package verify

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// 不正な UTF-8 を U+FFFD に置き換えた結果の golden（以前の CLI の置き換えと同じ単位）。
func TestDecodeReplaceGolden(t *testing.T) {
	for in, want := range map[string]string{
		"\xe3\x81a":        "\uFFFDa",
		"\xff\xfe":         "\uFFFD\uFFFD",
		"\xed\xa0\x80":     "\uFFFD\uFFFD\uFFFD",
		"\xf0\x9f\x98":     "\uFFFD",
		"a\xc3":            "a\uFFFD",
		"\xe0\x80\x80":     "\uFFFD\uFFFD\uFFFD",
		"\xf4\x90\x80\x80": "\uFFFD\uFFFD\uFFFD\uFFFD",
		"\xc0\xaf":         "\uFFFD\uFFFD",
		"あ\x80":            "あ\uFFFD",
		"\xf0\x9f\x98\x80": "😀",
		"ok\n":             "ok\n",
	} {
		if got := DecodeReplace([]byte(in)); got != want {
			t.Errorf("DecodeReplace(%q) = %q, want %q", in, got, want)
		}
	}
}

// 以前の CLI のテストと同じ確かめ方（末尾を切る・秘密をマスクする）。
func TestOutput(t *testing.T) {
	raw := "password=" + strings.Repeat("x", 5000) + "\nend"
	if out := Output([]byte(raw)); strings.Contains(out, "xxx") || !strings.HasSuffix(out, "password=***\nend") {
		t.Errorf("マスクしてから切る: %q", out[max(0, len(out)-40):])
	}
	if out := Output([]byte("あい")[1:]); out != "い" {
		t.Errorf("途中で切れた文字を捨てる: %q", out)
	}
	long := strings.Repeat(strings.Repeat("y", 10)+"\n", 1000)
	if out := Output([]byte(long)); len(out) != 4000 || !strings.HasSuffix(out, "y\n") {
		t.Errorf("出力は末尾 4,000 バイト: %d", len(out))
	}
	if out := Output([]byte("token=imp_abcdef\n")); out != "token=***\n" {
		t.Errorf("マスク: %q", out)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 5}
	b.Write([]byte("abc"))
	b.Write([]byte("defg"))
	if got := string(b.Bytes()); got != "cdefg" {
		t.Errorf("%q", got)
	}
	b.Write([]byte("0123456789"))
	if got := string(b.Bytes()); got != "56789" {
		t.Errorf("%q", got)
	}
}

func TestEnv(t *testing.T) {
	got := Env([]string{"IM_API_URL=http://x", "LOOPTRACK_PROJECT=p", "LOOPTRACK_TOKEN=t", "KEEP_ME=1", "PATH=/bin", "IMX=1"}, "TST-0001")
	want := []string{"KEEP_ME=1", "PATH=/bin", "IMX=1", "GOFLAGS=-count=1", "LOOPTRACK_VERIFY_ID=TST-0001"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Env = %v, want %v", got, want)
	}
}

// go test の結果キャッシュを無効にする GOFLAGS を必ず 1 つだけ渡す（案 A）。
// 利用者の GOFLAGS は残して後ろに足し、-count が既にあれば触らない。
func TestEnvGoflagsNoTestCache(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"足す", "", "GOFLAGS=-count=1"},
		{"残して足す", "GOFLAGS=-mod=mod", "GOFLAGS=-mod=mod -count=1"},
		{"-count があればそのまま", "GOFLAGS=-count=3 -mod=mod", "GOFLAGS=-count=3 -mod=mod"},
		{"-count だけでもそのまま", "GOFLAGS=-count", "GOFLAGS=-count"},
	} {
		base := []string{"PATH=/bin"}
		if c.in != "" {
			base = append(base, c.in)
		}
		got := Env(base, "TST-0001")
		n := 0
		var value string
		for _, kv := range got {
			if strings.HasPrefix(kv, "GOFLAGS=") {
				n++
				value = kv
			}
		}
		if n != 1 || value != c.want {
			t.Errorf("%s: GOFLAGS が %d 件・%q（want 1 件・%q）", c.name, n, value, c.want)
		}
	}
}

// 出力の (cached) は go test の書式の行だけを印にする（偶然の一致で誤って注記を付けない。案 B）。
func TestCached(t *testing.T) {
	for _, c := range []struct {
		out  string
		want bool
	}{
		{"ok  \tgithub.com/x/y\t(cached)\n", true},
		{"ok  \tgithub.com/x/y\t(cached)\tcoverage: 12.3% of statements\n", true},
		{"ok  \tgithub.com/x/y\t0.440s\nok  \tgithub.com/x/z\t(cached)\n", true},
		{"ok  \tgithub.com/x/y\t0.440s\n", false},
		{"(cached) という語が出力に混ざっただけ\n", false},
		{"echo ok (cached)\n", false},
		{"", false},
	} {
		if got := Cached(c.out); got != c.want {
			t.Errorf("Cached(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

func TestLine(t *testing.T) {
	three, zero := 3, 0
	for _, c := range []struct {
		r    Result
		want string
	}{
		{Result{Command: "echo ok", Status: StatusOK, ExitCode: &zero, DurationMS: 1234}, "[1/2] ok      echo ok（1.2 秒）"},
		{Result{Command: "exit 3", Status: StatusFail, ExitCode: &three, DurationMS: 50}, "[1/2] fail    exit 3（exit 3・0.1 秒）"},
		{Result{Command: "nope", Status: StatusFail, DurationMS: 0}, "[1/2] fail    nope（exit None・0.0 秒）"},
		{Result{Command: "sleep 9", Status: StatusTimeout, DurationMS: 500}, "[1/2] timeout sleep 9（時間切れ・0.5 秒）"},
		{Skipped("make"), "[1/2] skipped make（全体の上限を超えたため実行しない）"},
	} {
		if got := Line(1, 2, c.r); got != c.want {
			t.Errorf("Line = %q, want %q", got, c.want)
		}
	}
}

func TestIndentTail(t *testing.T) {
	if got := IndentTail(" \n", 20); got != "" {
		t.Errorf("%q", got)
	}
	if got := IndentTail("a\nb\nc\n\n", 2); got != "      | b\n      | c" {
		t.Errorf("%q", got)
	}
}

func TestResultJSONOrder(t *testing.T) {
	one := 1
	got := jsonorder.Compact(Result{Command: "x", Status: StatusFail, ExitCode: &one, DurationMS: 7, OutputTail: "o"}.JSON())
	if want := `{"command": "x", "status": "fail", "exit_code": 1, "duration_ms": 7, "output_tail": "o"}`; got != want {
		t.Errorf("%s", got)
	}
	got = jsonorder.Compact(Skipped("x").JSON())
	if want := `{"command": "x", "status": "skipped", "exit_code": null, "duration_ms": 0, "output_tail": ""}`; got != want {
		t.Errorf("%s", got)
	}
	r := ResultFromJSON(Result{Command: "x", Status: StatusFail, ExitCode: &one, DurationMS: 7, OutputTail: "o"}.JSON())
	if r.ExitCode == nil || *r.ExitCode != 1 || r.DurationMS != 7 || r.OutputTail != "o" {
		t.Errorf("%+v", r)
	}
	if r.Cached {
		t.Errorf("cached が無いのに立っています: %+v", r)
	}
	// 注記は付いたときだけ送り、読み戻せる
	got = jsonorder.Compact(Result{Command: "x", Status: StatusOK, ExitCode: &one, DurationMS: 7, OutputTail: "o", Cached: true}.JSON())
	if want := `{"command": "x", "status": "ok", "exit_code": 1, "duration_ms": 7, "output_tail": "o", "cached": true}`; got != want {
		t.Errorf("%s", got)
	}
	if r := ResultFromJSON(Result{Command: "x", Status: StatusOK, Cached: true}.JSON()); !r.Cached {
		t.Errorf("cached を読み戻せません: %+v", r)
	}
}

// 全体の上限を超えた残りは実行せず skipped にする。
// 上限は過去（負）にする。1ns だと、Windows の単調時計は続けて読んだ 2 回が同じ値になることがあり（分解能）、
// 残りが 1ns あるとみなして 1 件目を起動してしまう（CI の windows-latest で発生）。
func TestRunAllSkipsAfterTotalTimeout(t *testing.T) {
	var before []string
	rs := RunAll([]string{"exit 0", "exit 0"}, Options{Dir: t.TempDir(), Timeout: time.Minute, TotalTimeout: -time.Second,
		Before: func(i, n int, c string) { before = append(before, c) }})
	if len(before) != 0 || len(rs) != 2 || rs[0].Status != StatusSkipped || rs[1].Status != StatusSkipped {
		t.Errorf("before=%v results=%+v", before, rs)
	}
}

func TestWindowsShell(t *testing.T) {
	type env map[string]string
	look := func(m map[string]string) func(string) (string, error) {
		return func(name string) (string, error) {
			if p, ok := m[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		}
	}
	exists := func(files ...string) func(string) bool {
		return func(p string) bool {
			for _, f := range files {
				if strings.EqualFold(f, p) {
					return true
				}
			}
			return false
		}
	}
	getenv := func(e env) func(string) string { return func(k string) string { return e[k] } }
	std := env{"SystemRoot": `C:\WINDOWS`, "ProgramFiles": `C:\Program Files`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`}
	for _, c := range []struct {
		name  string
		path  map[string]string
		files []string
		env   env
		want  string
	}{
		{"PATH の bash（Git Bash の中）", map[string]string{"bash": `C:\Program Files\Git\usr\bin\bash.exe`}, nil, std,
			`C:\Program Files\Git\usr\bin\bash.exe`},
		{"WSL の起動口は飛ばして git の隣", map[string]string{"bash": `C:\Windows\System32\bash.exe`, "git": `D:\tools\Git\cmd\git.exe`},
			[]string{`D:\tools\Git\bin\bash.exe`}, std, `D:\tools\Git\bin\bash.exe`},
		{"WindowsApps の bash も飛ばす", map[string]string{"bash": `C:\Users\u\AppData\Local\Microsoft\WindowsApps\bash.exe`},
			[]string{`C:\Program Files\Git\bin\bash.exe`}, std, `C:\Program Files\Git\bin\bash.exe`},
		{"mingw64 の git から", map[string]string{"git": `E:/Git/mingw64/bin/git.exe`}, []string{`E:\Git\usr\bin\bash.exe`}, std,
			`E:\Git\usr\bin\bash.exe`},
		{"利用者ごとの導入", nil, []string{`C:\Users\u\AppData\Local\Programs\Git\bin\bash.exe`}, std,
			`C:\Users\u\AppData\Local\Programs\Git\bin\bash.exe`},
		{"bash が無ければ sh", map[string]string{"sh": `C:\msys64\usr\bin\sh.exe`}, nil, std, `C:\msys64\usr\bin\sh.exe`},
		{"どれも無い", map[string]string{"bash": `C:\WINDOWS\system32\bash.exe`}, nil, std, ""},
	} {
		got, err := WindowsShell(look(c.path), getenv(c.env), exists(c.files...))
		if c.want == "" {
			if !errors.Is(err, ErrNoShell) {
				t.Errorf("%s: %q %v、ErrNoShell のはず", c.name, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
}

// StripCached は results[].cached だけを落とし、ほかの欄と並びは変えない。
func TestStripCached(t *testing.T) {
	one := 0
	rs := []Result{{Command: "a", Status: StatusOK, ExitCode: &one, DurationMS: 1, OutputTail: "x"},
		{Command: "b", Status: StatusOK, ExitCode: &one, DurationMS: 2, OutputTail: "y", Cached: true}}
	body := Body("sha", rs, t.TempDir())
	if !StripCached(body) {
		t.Fatal("cached があるのに落としていません")
	}
	if StripCached(body) {
		t.Error("2 回目は落とすものが無いので false のはず")
	}
	got := jsonorder.Compact(body)
	if strings.Contains(got, `"cached"`) {
		t.Errorf("cached が残っています: %s", got)
	}
	for _, want := range []string{`"command": "b"`, `"status": "ok"`, `"duration_ms": 2`, `"output_tail": "y"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s が落ちています: %s", want, got)
		}
	}
	if StripCached(Body("sha", rs[:1], t.TempDir())) {
		t.Error("cached が無い本文で true を返しています")
	}
}
