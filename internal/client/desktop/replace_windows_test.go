//go:build windows

package desktop

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// helperSleepEnv は TestHelperRunningExe を「動き続ける子プロセス」として起こす印。
const helperSleepEnv = "LOOPTRACK_TEST_HELPER_SLEEP"

// TestHelperRunningExe は TestZipSwapRunningExe が起こす子プロセスの本体（印が無ければ何もせずに通る）。
// 印があれば、親が止めるまで（最長 60 秒）動き続ける。
func TestHelperRunningExe(t *testing.T) {
	if os.Getenv(helperSleepEnv) != "1" {
		return
	}
	time.Sleep(60 * time.Second)
}

// TestZipSwapRunningExe は、動いている子プロセスの exe を zip 版の入れ替え（swap）で改名でき、undo で元に戻せることを
// 確かめる（受け入れ条件 6）。Windows は動いている exe を消せないが改名はできる、という前提に置き換えが乗っている。
// 対照として、同じ exe を消すことが拒まれる（＝本当に動いていて、ファイルが使われている）ことも見る。
func TestZipSwapRunningExe(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cur := filepath.Join(dir, "Looptrack.exe")
	if err := os.WriteFile(cur, orig, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cur, "-test.run=^TestHelperRunningExe$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperSleepEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	})
	// 子プロセスが exe を開くまで少し待つ（起動の直後は消せてしまうことがある）。上限は 10 秒
	writable := true
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		select {
		case err := <-exited:
			t.Fatalf("子プロセスが先に終わった: %v", err)
		default:
		}
		// 対照: 動いている exe は書き込みで開けない（共有の違反）。開けるうちは、まだ子プロセスが exe を使っていない
		f, err := os.OpenFile(cur, os.O_WRONLY, 0)
		if err != nil {
			writable = false
			break
		}
		f.Close()
	}
	if writable {
		t.Fatal("前提が崩れています: 動いているはずの exe を書き込みで開けた（子プロセスが exe を使っていない）")
	}
	if err := os.Remove(cur); err == nil {
		t.Fatal("前提が崩れています: 動いている exe を消せた")
	}

	next := filepath.Join(dir, ".looptrack-update-x", "Looptrack.exe")
	os.MkdirAll(filepath.Dir(next), 0o755)
	if err := os.WriteFile(next, []byte("MZ new version"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := cur + ".prev"
	if err := swap(cur, next, prev); err != nil {
		t.Fatalf("動いている exe を改名で入れ替えられない: %v", err)
	}
	if got, _ := os.ReadFile(cur); string(got) != "MZ new version" {
		t.Errorf("入れ替えた後の Looptrack.exe = %d バイト", len(got))
	}
	if got, _ := os.ReadFile(prev); !bytes.Equal(got, orig) {
		t.Errorf("Looptrack.exe.prev が動いている exe ではない（%d バイト）", len(got))
	}
	select {
	case err := <-exited:
		t.Fatalf("入れ替えで子プロセスが終わった: %v", err)
	default:
	}
	// 起動し直せなかったときの戻し: 動いている exe（.prev）を元の名前に戻す
	if err := (&applied{moves: []move{{cur: cur, prev: prev}}}).undo(); err != nil {
		t.Fatalf("動いている exe を元の名前に戻せない: %v", err)
	}
	if got, _ := os.ReadFile(cur); !bytes.Equal(got, orig) {
		t.Errorf("戻した後の Looptrack.exe が動いている exe ではない（%d バイト）", len(got))
	}
	for _, p := range []string{prev, cur + ".failed"} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("戻した後に %s が残っている: %v", filepath.Base(p), err)
		}
	}
}
