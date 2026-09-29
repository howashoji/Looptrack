package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

func TestUpdateStatePath(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"systemd の StateDirectory が先", map[string]string{"STATE_DIRECTORY": "/var/lib/looptrack", "LOOPTRACK_DSN": "sqlite:/srv/x/im.db"},
			filepath.Join("/var/lib/looptrack", "update-check.json")},
		{"StateDirectory が複数なら最初", map[string]string{"STATE_DIRECTORY": "/var/lib/a" + string(os.PathListSeparator) + "/var/lib/b"},
			filepath.Join("/var/lib/a", "update-check.json")},
		{"SQLite の DB の隣（compose の /data）", map[string]string{"LOOPTRACK_DSN": "sqlite:/data/im.db"}, filepath.Join("/data", "update-check.json")},
		{"メモリの SQLite は置き場なし", map[string]string{"LOOPTRACK_DSN": "sqlite::memory:"}, ""},
		{"MySQL は置き場なし", map[string]string{"LOOPTRACK_DSN": "u:p@tcp(db:3306)/im"}, ""},
	} {
		if got := updateStatePath(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Windows: ドライブ文字の : で切らない（切ると D だけになり、作業ディレクトリの下の D\update-check.json に書く）
	if runtime.GOOS == "windows" {
		d := `D:\srv\looptrack`
		if got, want := updateStatePath(func(k string) string { return map[string]string{"STATE_DIRECTORY": d}[k] }), filepath.Join(d, "update-check.json"); got != want {
			t.Errorf("ドライブ文字つき: %q, want %q", got, want)
		}
	}
}

// 確認先の応答（serveReleaseSource の mode）。
const (
	srcNormal   = iota // 新しい版（v1.1.0。この OS・CPU のサーバ版の書庫つき）を返す
	srcDown            // HTTP 500（通信の失敗）
	srcUpToDate        // 今の版（v1.0.0）だけ
)

// serveReleaseSource は確認先の代わり（GitHub の API と同じ形のリリースの一覧を返す https のサーバ）。
func serveReleaseSource(t *testing.T) (*httptest.Server, *atomic.Int64, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int64
	var mode atomic.Int32
	asset := updatecheck.ArchiveAsset(runtime.GOOS, runtime.GOARCH)("v1.1.0")
	if asset == "" {
		t.Skipf("この OS・CPU（%s/%s）にはサーバ版の書庫が無い", runtime.GOOS, runtime.GOARCH)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		switch mode.Load() {
		case srcDown:
			w.WriteHeader(http.StatusInternalServerError)
		case srcUpToDate:
			fmt.Fprint(w, `[{"tag_name":"v1.0.0","prerelease":false,"assets":[]}]`)
		default:
			fmt.Fprintf(w, `[{"tag_name":"v1.1.0","prerelease":false,"html_url":"https://example.invalid/releases/tag/v1.1.0",
			  "assets":[{"name":%q,"browser_download_url":"https://example.invalid/%s"}]}]`, asset, asset)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &mode
}

// syncBuffer はログの書き込み先（確認の goroutine とテストが同時に触る）。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startServerUpdates は serve と同じ組み方で確認を動かす（公開鍵なし・確認先を差し替え＝署名を確かめずに知らせる）。
// 止める関数と、ログを返す。
func startServerUpdates(t *testing.T, src *httptest.Server, env map[string]string) (*serverUpdates, func(), *syncBuffer) {
	t.Helper()
	e := map[string]string{updatecheck.EnvURL: src.URL + "/releases"}
	for k, v := range env {
		e[k] = v
	}
	logs := &syncBuffer{}
	u := newServerUpdates(serverUpdateOptions{Version: "v1.0.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Getenv: func(k string) string { return e[k] }, Client: src.Client(), Lang: i18n.JA}, slog.New(slog.NewJSONHandler(logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { u.run(ctx); close(done) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return u, stop, logs
}

// waitHandled は n 回目の確認の結果まで知らせに反映するのを待つ（上限 20 秒）。
func waitHandled(t *testing.T, u *serverUpdates, n int64) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); u.handled.Load() < n; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("待っても %d 回目の確認が終わらない（%d 回）", n, u.handled.Load())
		}
	}
}

// 起動時に確かめて新しい版を知らせ（ログの Warn に更新の 1 行）、控えを StateDirectory に書く → 通信に失敗した回も知らせを残す →
// 起動し直して最初の回が失敗しても、控えの last_ok で知らせる → 新しい版が無いと分かれば消える。
func TestServerUpdates(t *testing.T) {
	src, hits, mode := serveReleaseSource(t)
	dir := t.TempDir()
	env := map[string]string{"STATE_DIRECTORY": dir}
	u, stop, logs := startServerUpdates(t, src, env)
	waitHandled(t, u, 1)
	n := u.current()
	if n == nil || n.Version != "v1.1.0" || n.Current != "v1.0.0" || n.URL != "https://example.invalid/releases/tag/v1.1.0" {
		t.Fatalf("起動時の知らせ: %+v\nlog: %s", n, logs)
	}
	for _, want := range []string{`"msg":"update-check"`, "status=available", `"level":"WARN"`,
		"新しい版 v1.1.0 があります（いまの版は v1.0.0）。install.sh で入れたサーバは次の 1 行で更新します: " + updatecheck.ServerUpgradeCommand} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("起動時のログに %q が無い:\n%s", want, logs)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "update-check.json")); err != nil || !strings.Contains(string(b), `"last_ok"`) {
		t.Errorf("控え（StateDirectory の update-check.json）: %v %s", err, b)
	}

	// 通信の失敗: 知らせを残す（ログは error の 1 行と、残した知らせの Warn）
	mode.Store(srcDown)
	u.runner.Wake()
	waitHandled(t, u, 2)
	if n := u.current(); n == nil || n.Version != "v1.1.0" {
		t.Errorf("通信に失敗した回に知らせが消えた: %+v", n)
	}
	stop()

	// 起動し直す（最初の回から失敗）: 控えの last_ok で知らせる
	u2, stop2, _ := startServerUpdates(t, src, env)
	waitHandled(t, u2, 1)
	if n := u2.current(); n == nil || n.Version != "v1.1.0" {
		t.Errorf("起動し直した最初の回（失敗）に控えの知らせが無い: %+v", n)
	}
	// 新しい版が無いと分かれば消える
	mode.Store(srcUpToDate)
	u2.runner.Wake()
	waitHandled(t, u2, 2)
	if n := u2.current(); n != nil {
		t.Errorf("新しい版が無いのに知らせが残った: %+v", n)
	}
	stop2()

	// LOOPTRACK_UPDATE_CHECK=off: 通信しない・知らせない（対照: ここまでは一覧を取っている）
	before := hits.Load()
	if before == 0 {
		t.Fatal("前提: 確認先に一度も届いていない")
	}
	mode.Store(srcNormal)
	u3, _, logs3 := startServerUpdates(t, src, map[string]string{"STATE_DIRECTORY": t.TempDir(), updatecheck.EnvCheck: "off"})
	waitHandled(t, u3, 1)
	if n := u3.current(); n != nil || hits.Load() != before || !strings.Contains(logs3.String(), "status=disabled") {
		t.Errorf("off: 知らせ %+v・一覧を取った回数 %d → %d\n%s", n, before, hits.Load(), logs3)
	}
}

// 控えの置き場が無い（MySQL・StateDirectory なし）ときも、同じプロセスの中では最後に成功した結果で知らせを残す。
func TestServerUpdatesWithoutStateFile(t *testing.T) {
	src, _, mode := serveReleaseSource(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	u, _, _ := startServerUpdates(t, src, map[string]string{"LOOPTRACK_DSN": "u:p@tcp(db:3306)/im"})
	waitHandled(t, u, 1)
	if n := u.current(); n == nil || n.Version != "v1.1.0" {
		t.Fatalf("知らせ: %+v", n)
	}
	mode.Store(srcDown)
	u.runner.Wake()
	waitHandled(t, u, 2)
	if n := u.current(); n == nil {
		t.Error("控えが無いときに、通信に失敗した回で知らせが消えた")
	}
	if ents, _ := os.ReadDir(cwd); len(ents) != 0 {
		t.Errorf("置き場が無いのにファイルを書いた: %v", ents)
	}
}
