package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTryLock は二重起動の防止の土台（ロック）が、OS ごとの実装でも同じ約束を守ることを確かめる。
// 約束: 1 つ目は取れる・持っている間は 2 つ目が取れない（エラーではなく ok=false）・放すと取れる。
func TestTryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "looptrack.lock")

	release, ok, err := tryLock(path)
	if err != nil || !ok {
		t.Fatalf("1 つ目のロックが取れない: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("ロックのファイルが作られていない: %v", err)
	}

	// 同じプロセスからでも、別のファイル記述子なので取れないのが正しい（2 つ目の起動の代わり）。
	release2, ok2, err2 := tryLock(path)
	if err2 != nil {
		t.Fatalf("2 つ目のロックでエラー: %v", err2)
	}
	if ok2 {
		release2()
		t.Fatal("持っている間に 2 つ目のロックが取れてしまった（二重起動を止められない）")
	}

	release()

	release3, ok3, err3 := tryLock(path)
	if err3 != nil || !ok3 {
		t.Fatalf("放した後にロックが取れない: ok=%v err=%v", ok3, err3)
	}
	release3()
}

// TestTryLockBadPath は、置き場が無いときにエラーを返す（ok=true にしない）ことを確かめる。
// ここで ok=true を返すと、ロックを持っていないのに起動してしまう。
func TestTryLockBadPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "looptrack.lock")
	release, ok, err := tryLock(path)
	if ok {
		release()
		t.Fatal("置き場が無いのにロックが取れた")
	}
	if err == nil {
		t.Error("置き場が無いときはエラーを返すはず（ok=false・err=nil は「ほかが持っている」の意味）")
	}
}
