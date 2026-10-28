package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

var nginxBodyLimit = regexp.MustCompile(`client_max_body_size\s+(\d+)m;`)

// nginxBodyLimitDocs は、nginx の本文の上限の例を載せる文書。利用者ガイドの getting-started は日英の両方を見る。
var nginxBodyLimitDocs = []string{
	"deploy/install.sh",
	"docs/server/DEPLOY.md",
	"docs/guide/server/getting-started.md",
	"docs/guide/ja/server/getting-started.md",
}

// caddyBodyLimitDocs は、Caddy の max_size の例を載せる文書。install.sh の Caddy の例は上限を付けない。
var caddyBodyLimitDocs = []string{
	"docs/server/DEPLOY.md",
	"docs/guide/server/getting-started.md",
	"docs/guide/ja/server/getting-started.md",
}

// TestInstallNginxExampleCoversTranscriptChunk は、install.sh が示す nginx の例の本文の上限が、
// 会話記録のチャンクの既定の上限（store.DefaultTranscriptMaxChunk）以上であることを見る。
// 下回るとチャンクの送信が前段のプロキシの 413 で止まる（CLI はプロキシの 413 では送り直さない）。
func TestInstallNginxExampleCoversTranscriptChunk(t *testing.T) {
	for _, rel := range nginxBodyLimitDocs {
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

// TestDeployCaddyExampleCoversTranscriptChunk は、Caddy の例の max_size が、会話記録のチャンクの
// 既定の上限以上の大きさであることを見る（nginx の例と同じ理由）。
func TestDeployCaddyExampleCoversTranscriptChunk(t *testing.T) {
	for _, rel := range caddyBodyLimitDocs {
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
}

// nginxLocationDirective は、install.sh の nginx の例の location の中で、ガイドにも同じ値で載せる行。
// proxy_set_header は名前と値の組で見る（install.sh では $ を \$ と書くので、比べる前に外す）。
var nginxLocationDirective = regexp.MustCompile(`(?m)^\s*(proxy_set_header\s+\S+\s+\S+|proxy_buffering\s+\S+|proxy_read_timeout\s+\S+|client_max_body_size\s+\S+);`)

func nginxDirectives(t *testing.T, rel string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range nginxLocationDirective.FindAllSubmatch(b, -1) {
		out = append(out, strings.Join(strings.Fields(strings.ReplaceAll(string(m[1]), `\$`, "$")), " "))
	}
	return out
}

// TestGuideNginxExampleMatchesInstall は、利用者ガイドの nginx の例の location の行（ヘッダ・proxy_buffering・
// proxy_read_timeout・client_max_body_size）が、install.sh の表示する例と同じ並び・同じ値であることを見る。
// どちらかだけを変えると、ガイドを写した利用者と install.sh の例を写した利用者で設定が食い違う。
// 本文の上限は DEPLOY.md の例とも比べる。
func TestGuideNginxExampleMatchesInstall(t *testing.T) {
	want := nginxDirectives(t, "deploy/install.sh")
	// 前提: install.sh の例から比べる行が取れていること。取れなければ、ガイドが空でも通ってしまう。
	for _, d := range []string{"proxy_buffering off", "client_max_body_size 33m", "proxy_set_header X-Forwarded-Proto $scheme"} {
		if !slices.Contains(want, d) {
			t.Fatalf("前提が崩れています: install.sh の nginx の例から %q が取れません（取れたのは %q）", d, want)
		}
	}
	for _, rel := range []string{"docs/guide/server/getting-started.md", "docs/guide/ja/server/getting-started.md"} {
		if got := nginxDirectives(t, rel); !slices.Equal(got, want) {
			t.Errorf("%s の nginx の例が install.sh と違います:\n  ガイド:     %q\n  install.sh: %q", rel, got, want)
		}
	}
	var limit string
	for _, d := range want {
		if strings.HasPrefix(d, "client_max_body_size ") {
			limit = d
		}
	}
	for _, d := range nginxDirectives(t, "docs/server/DEPLOY.md") {
		if strings.HasPrefix(d, "client_max_body_size ") && d != limit {
			t.Errorf("docs/server/DEPLOY.md の %q が install.sh の %q と違います", d, limit)
		}
	}
}

// TestGuideCaddyExampleMatchesDeploy は、利用者ガイドの Caddy の max_size が DEPLOY.md の例と同じ値であることを見る。
func TestGuideCaddyExampleMatchesDeploy(t *testing.T) {
	sizes := func(rel string) []string {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range caddyBodyLimit.FindAllSubmatch(b, -1) {
			out = append(out, string(m[1]))
		}
		return out
	}
	want := sizes("docs/server/DEPLOY.md")
	if len(want) == 0 {
		t.Fatal("前提が崩れています: DEPLOY.md に Caddy の max_size の例がありません")
	}
	for _, rel := range []string{"docs/guide/server/getting-started.md", "docs/guide/ja/server/getting-started.md"} {
		for _, got := range sizes(rel) {
			if got != want[0] {
				t.Errorf("%s の max_size %sMiB が DEPLOY.md の %sMiB と違います", rel, got, want[0])
			}
		}
	}
}
