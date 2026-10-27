package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

var nginxBodyLimit = regexp.MustCompile(`client_max_body_size\s+(\d+)m;`)

// TestInstallNginxExampleCoversTranscriptChunk は、install.sh が示す nginx の例の本文の上限が、
// 会話記録のチャンクの既定の上限（store.DefaultTranscriptMaxChunk）以上であることを見る。
// 下回るとチャンクの送信が前段のプロキシの 413 で止まる（CLI はプロキシの 413 では送り直さない）。
func TestInstallNginxExampleCoversTranscriptChunk(t *testing.T) {
	for _, rel := range []string{"deploy/install.sh", "docs/server/DEPLOY.md"} {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		ms := nginxBodyLimit.FindAllSubmatch(b, -1)
		if len(ms) == 0 {
			t.Fatalf("%s: client_max_body_size の例が見つかりません。前提が崩れています", rel)
		}
		for _, m := range ms {
			mib, _ := strconv.ParseInt(string(m[1]), 10, 64)
			if mib<<20 <= store.DefaultTranscriptMaxChunk {
				t.Errorf("%s: client_max_body_size %dm はチャンクの上限 %d バイト以下です（gzip の増分の余裕を足した大きさまでサーバは受けます）", rel, mib, store.DefaultTranscriptMaxChunk)
			}
		}
	}
}

var caddyBodyLimit = regexp.MustCompile(`max_size\s+(\d+)MiB`)

// TestDeployCaddyExampleCoversTranscriptChunk は、DEPLOY.md の Caddy の例の max_size が、会話記録のチャンクの
// 既定の上限以上の大きさであることを見る（nginx の例と同じ理由）。
func TestDeployCaddyExampleCoversTranscriptChunk(t *testing.T) {
	rel := "docs/server/DEPLOY.md"
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	ms := caddyBodyLimit.FindAllSubmatch(b, -1)
	if len(ms) == 0 {
		t.Fatalf("%s: max_size の例が見つかりません。前提が崩れています", rel)
	}
	for _, m := range ms {
		mib, _ := strconv.ParseInt(string(m[1]), 10, 64)
		if mib<<20 <= store.DefaultTranscriptMaxChunk {
			t.Errorf("%s: max_size %dMiB はチャンクの上限 %d バイト以下です", rel, mib, store.DefaultTranscriptMaxChunk)
		}
	}
}
