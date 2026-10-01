package kitinit

import (
	"runtime"
	"testing"
)

// TestRerunCommand は、loop を足すなどで init をやり直す案内のコマンドが、サーバ版（--url か --source server）では
// 実行中の looptrack の絶対パスと --project・--url・--agent を持ち、--url の無い looptrack issue init にならないことを確かめる
// （サーバ版の利用者の端末に PATH の looptrack は無く、--url の無い init は CLI の既定の接続先に向く）。
// 対照: サーバ版でない（埋め込みの kit・--url なし）ときは従来どおり looptrack issue init（判定の両側を通る）。
func TestRerunCommand(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	exe := "/nonexistent/looptrack-test/looptrack"
	bin := `"` + exe + `"`
	if runtime.GOOS == "windows" {
		exe = `C:\nonexistent\it's\looptrack.exe`
		bin = `& 'C:\nonexistent\it''s\looptrack.exe'`
	}
	executable = func() (string, error) { return exe, nil }

	in := &installer{o: &Options{Project: "demo", agents: []string{"codex", "copilot"}}, url: "https://example.invalid/lt", source: "copy"}
	if got := in.rerunCommand(" --loop"); got != "looptrack issue init --loop" {
		t.Errorf("サーバ版でない init: %q", got)
	}
	in.o.URL = "https://example.invalid/lt"
	if got, want := in.rerunCommand(" --loop"), bin+" issue init --project demo --url https://example.invalid/lt --agent codex,copilot --loop"; got != want {
		t.Errorf("--url を付けた init:\n got %q\nwant %q", got, want)
	}
	in.o.URL, in.source = "", "server"
	if got, want := in.rerunCommand(" --loop"), bin+" issue init --project demo --url https://example.invalid/lt --agent codex,copilot --source server --loop"; got != want {
		t.Errorf("--source server の init:\n got %q\nwant %q", got, want)
	}
}
