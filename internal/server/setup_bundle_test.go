package server

import (
	"strings"
	"testing"
)

// 今の CLI は files を空で送る。そのときの導入状態は bundle_sha256 を返さない（空の入力の SHA-256 は壊れた控えに見える）。
// 対照として、files を送る古い CLI の通知では、これまでどおり files のハッシュを返す。
func TestInstallStateOmitsEmptyFilesBundle(t *testing.T) {
	e, _, ed := newAPIEnv(t)
	withFakeDist(t, e)
	post := func(body map[string]any) installStateJSON {
		t.Helper()
		var st installStateJSON
		ed.json(200, "POST", "/projects/req/install", body, &st)
		return st
	}
	if st := post(installBody("claude-code", "hook", "copy", "", nil)); st.Bundle != "" {
		t.Errorf("files が空なのに bundle_sha256 = %q", st.Bundle)
	}
	old := map[string]any{"agent": "codex", "trigger": "hook", "source": "server", "files": map[string]string{"kit.tar.gz": strings.Repeat("0", 64)}}
	if st := post(old); len(st.Bundle) != 64 {
		t.Errorf("前提が崩れています: files を送る通知の bundle_sha256 が返らない: %q", st.Bundle)
	}
}
