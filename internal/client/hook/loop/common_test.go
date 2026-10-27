package loop

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// execRun は、シグナルで止められた子プロセスの理由を err で返す（終了コードは -1）。
// 返さないと、呼び出し元は「終了コード -1」しか出せず、止められた理由を利用者が見られない。
func TestExecRunReportsSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("シグナルで自分を止める sh が無い")
	}
	_, rc, err := execRun(context.Background(), Command{Name: "sh", Args: []string{"-c", "kill -9 $$"}})
	if rc != -1 || err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("シグナルの理由を err で返すはず: rc=%d err=%v", rc, err)
	}
	// 対照: ふつうの終了コードは err にしない（呼び出し元は終了コードで分ける）
	_, rc, err = execRun(context.Background(), Command{Name: "sh", Args: []string{"-c", "exit 3"}})
	if rc != 3 || err != nil {
		t.Fatalf("終了コード 3 は err なしで返すはず: rc=%d err=%v", rc, err)
	}
}
