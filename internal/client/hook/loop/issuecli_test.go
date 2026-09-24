package loop

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIssueCLI は issue の CLI の起動のしかた: LOOPTRACK_LOOP_ISSUE_CLI → 実行中の looptrack。
func TestIssueCLI(t *testing.T) {
	old := selfExe
	t.Cleanup(func() { selfExe = old })
	root := t.TempDir()
	vars := map[string]string{}
	e := (&Env{Getenv: func(k string) string { return vars[k] }, Getwd: func() string { return root }}).withDefaults()

	selfExe = func() (string, error) { return "/x/loop.test", nil }
	if _, _, ok := issueCLI(e, root); ok {
		t.Errorf("looptrack が無ければ起動しない")
	}
	selfExe = func() (string, error) { return "/opt/bin/looptrack", nil }
	if name, pre, ok := issueCLI(e, root); !ok || name != "/opt/bin/looptrack" || len(pre) != 1 || pre[0] != "issue" {
		t.Errorf("既定は looptrack issue: %v %v %v", name, pre, ok)
	}
	// 明示された実行ファイルはそのまま起動する
	mine := filepath.Join(root, "my-issue")
	os.WriteFile(mine, []byte("#!/bin/sh\n"), 0o755)
	vars["LOOPTRACK_LOOP_ISSUE_CLI"] = mine
	if name, pre, ok := issueCLI(e, root); !ok || name != mine || len(pre) != 0 {
		t.Errorf("LOOPTRACK_LOOP_ISSUE_CLI をそのまま起動する: %v %v %v", name, pre, ok)
	}
	vars["LOOPTRACK_LOOP_ISSUE_CLI"] = filepath.Join(root, "missing")
	if _, _, ok := issueCLI(e, root); ok {
		t.Errorf("LOOPTRACK_LOOP_ISSUE_CLI が無いファイルなら起動しない（looptrack に落とさない）")
	}
}
