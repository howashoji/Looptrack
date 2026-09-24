package server

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testdata"
	"github.com/howashoji/looptrack/internal/transfer"
)

// CLI（looptrack issue）をテストのサーバに向けて起動する部品。以前は 1.0.0 より前の CLI のファイルモードと
// API モードの出力を比べていた。以前の CLI は撤去し、CLI の出力の比較は internal/clitest の
// golden（.go.golden）が受け持つ。ここに残すのは、サーバの振る舞い（記録・拒否・表示）を CLI 越しに確かめるためのもの。

type cliResult struct {
	stdout, stderr string
	code           int
}

type cliEnv struct {
	t       *testing.T
	e       *env
	dir     string // 作業ディレクトリ（CLAUDE_PROJECT_DIR・.claude/issues は無い）
	slug    string
	token   string
	home    string
	session string // LOOPTRACK_SESSION_ID（空なら golden）
}

func newCLIEnv(t *testing.T, e *env, slug, token string) *cliEnv {
	t.Helper()
	return &cliEnv{t: t, e: e, dir: t.TempDir(), slug: slug, token: token, home: t.TempDir()}
}

// env は子プロセスの環境（利用者の LOOPTRACK_*・IM_*・資格情報・AI のセッションを持ち込まない）。
func (c *cliEnv) env() []string {
	session := c.session
	if session == "" {
		session = "golden"
	}
	return append(cliAPIEnv(c.e.srv.URL+"/im", c.slug, c.token, c.home), "CLAUDE_PROJECT_DIR="+c.dir, "LOOPTRACK_SESSION_ID="+session)
}

// run は looptrack issue <args> を実行する。サーバの時計を実時刻に合わせる（CLI の手元の時刻と並べるため）。
func (c *cliEnv) run(stdin string, args ...string) cliResult {
	c.t.Helper()
	c.e.clock.mu.Lock()
	c.e.clock.t = time.Now().UTC()
	c.e.clock.mu.Unlock()
	return runCLI(c.t, c.dir, c.env(), stdin, append([]string{"issue"}, args...)...)
}

// dataRoot は旧形式のデータ（internal/testdata の合成フィクスチャ。読むだけ）。
func dataRoot(t *testing.T) string {
	t.Helper()
	return testdata.Root(t)
}

// setupGolden はフィクスチャのプロジェクトを取り込み、編集者 golden のトークンを返す。
func setupGolden(t *testing.T, slugs ...string) (*env, string) {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	src, err := transfer.ReadSource(dataRoot(t), slugs)
	if err != nil {
		t.Fatalf("フィクスチャを読めない: %v", err)
	}
	if _, err := transfer.Import(ctx, e.db, src); err != nil {
		t.Fatal(err)
	}
	u := e.user("golden", "golden-password-1", "member")
	for _, slug := range slugs {
		p, _ := store.ProjectBySlug(ctx, e.db, slug)
		if err := store.SetMember(ctx, e.db, p.ID, u.ID, "editor"); err != nil {
			t.Fatal(err)
		}
	}
	return e, e.apiAs(u).token
}

// TestCLIInReviewEmpty は、In Review が 0 件のプロジェクトで SessionStart の hook が頼る前提
// （In Review の一覧の空白を除くと「該当なし」）を確かめる（フィクスチャの sample は In Review を持たない）。
func TestCLIInReviewEmpty(t *testing.T) {
	e, token := setupGolden(t, "sample")
	c := newCLIEnv(t, e, "sample", token)
	res := c.run("", "list", "--status", "In Review")
	if strings.Join(strings.Fields(res.stdout), "") != "該当なし" || res.code != 0 {
		t.Errorf("sample の In Review: %q %s", res.stdout, res.stderr)
	}
}

func mustCLI(t *testing.T, r cliResult, code int, what string) cliResult {
	t.Helper()
	if r.code != code {
		t.Fatalf("%s: exit %d, want %d\nstdout: %s\nstderr: %s", what, r.code, code, r.stdout, r.stderr)
	}
	return r
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func clip(s string) string {
	if len(s) > 3000 {
		return s[:3000] + "…（省略）\n"
	}
	return s
}
