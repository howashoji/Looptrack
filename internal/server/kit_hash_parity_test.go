package server

import (
	"encoding/json"
	"testing"
)

// TestKitBundleParity: looptrack issue init（実行ファイルに埋め込んだ kit を置く）が .claude/.looptrack-kit.json に控える
// kit/core・kit/loop の bundle_sha256 が、サーバの配布物（go:embed + kit.Names）の distLatest と一致する。一致しないと
// 導入先に「配布物の更新」が出続ける（OS の付随ファイルの除き方が揃っていることの確認）。
// 以前は 1.0.0 より前の CLI の計算と比べていた（その CLI は撤去した）。
func TestKitBundleParity(t *testing.T) {
	e, _, _ := newAPIEnv(t)
	latest, err := latestDist()
	if err != nil {
		t.Fatal(err)
	}
	proj, home := t.TempDir(), t.TempDir()
	r := runCLI(t, proj, cliHomeEnv(home), "", "issue", "init", "--project", "req", "--url", e.srv.URL+"/im", "--agent", "claude-code", "--loop")
	if r.code != 0 {
		t.Fatalf("init: %d %s %s", r.code, r.stdout, r.stderr)
	}
	var kitJSON struct {
		Core struct {
			Bundle string `json:"bundle_sha256"`
		} `json:"core"`
		Loop struct {
			Bundle string `json:"bundle_sha256"`
		} `json:"loop"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, proj+"/.claude/.looptrack-kit.json")), &kitJSON); err != nil {
		t.Fatal(err)
	}
	if kitJSON.Core.Bundle != latest.Core || kitJSON.Loop.Bundle != latest.Loop {
		t.Errorf("init の bundle_sha256 core=%q loop=%q と サーバの core=%q loop=%q が違う", kitJSON.Core.Bundle, kitJSON.Loop.Bundle, latest.Core, latest.Loop)
	}
}
