package desktop

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// 確認先の応答の切り替え（releaseSource）。
const (
	sourceNormal   = iota // tag のリリースを返す
	sourceDown            // HTTP 500（通信の失敗）
	sourceUpToDate        // 今の版（v1.0.0）だけを返す（新しい版が無い）
)

// releaseSource は確認先の代わり（GitHub の API と同じ形のリリースの一覧を返す https のサーバ）。一覧を取った回数を数え、
// mode で応答を切り替える。
func releaseSource(t *testing.T, tag string) (*httptest.Server, *atomic.Int64, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int64
	var mode atomic.Int32
	asset := updatecheck.DesktopAsset(runtime.GOOS, runtime.GOARCH)(tag)
	if asset == "" {
		t.Skipf("この OS・アーキテクチャ（%s/%s）にはデスクトップ版の配布物が無い", runtime.GOOS, runtime.GOARCH)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		switch mode.Load() {
		case sourceDown:
			w.WriteHeader(http.StatusInternalServerError)
			return
		case sourceUpToDate:
			fmt.Fprint(w, `[{"tag_name":"v1.0.0","prerelease":false,"assets":[]}]`)
			return
		}
		fmt.Fprintf(w, `[{"tag_name":%q,"prerelease":false,"html_url":"https://example.invalid/releases/tag/%s",
		  "assets":[{"name":%q,"browser_download_url":"https://example.invalid/%s"}]}]`, tag, tag, asset, asset)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &mode
}

// waitFor は cond が真になるまで待つ（上限 20 秒。超えたら失敗）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("待っても %s にならない", what)
		}
	}
}

// 起動 → 新しい版を知らせる（App.UpdateNotice・画面の帯）→ トレイの「新しい版を確認する」を外すと知らせと帯が消え、控えが off になり、
// 以後は通信しない → 入れ直すと待たずに確かめて知らせが戻る。その間、OS の通知（Alert）は 1 度も出さない。
func TestMainUpdateNotice(t *testing.T) {
	src, hits, mode := releaseSource(t, "v1.1.0")
	dir := t.TempDir()
	e := env.FromMap(map[string]string{"LOOPTRACK_DATA_DIR": dir, "LOOPTRACK_DESKTOP_PORT": "0", "LOOPTRACK_LANG": "ja",
		updatecheck.EnvURL: src.URL + "/releases"})
	rec := &recorder{}
	ui := &fakeUI{ran: make(chan *App, 1)}
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		// 公開鍵を渡さない（鍵の無いビルド）。確認先を差し替えたので、署名を確かめずに知らせる
		done <- Main([]string{"--background"}, Options{Version: "v1.0.0", Env: e, Stdout: &out, UI: ui, Open: rec.open, Alert: rec.alert, Home: dir,
			UpdateClient: src.Client()})
	}()
	var app *App
	select {
	case app = <-ui.ran:
	case code := <-done:
		t.Fatalf("起動しないで終わった（%d）: %s %v", code, out.String(), rec.alerts)
	case <-time.After(20 * time.Second):
		t.Fatal("起動しない")
	}
	defer func() {
		app.Quit()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("終了しない")
		}
	}()
	var mu sync.Mutex
	var changes []string
	app.OnUpdateNotice(func(n *updatecheck.Notice) {
		mu.Lock()
		defer mu.Unlock()
		if n == nil {
			changes = append(changes, "nil")
		} else {
			changes = append(changes, n.Version)
		}
	})

	waitFor(t, "新しい版の知らせ", func() bool { return app.UpdateNotice() != nil })
	n := app.UpdateNotice()
	if n.Version != "v1.1.0" || n.Current != "v1.0.0" || n.URL != "https://example.invalid/releases/tag/v1.1.0" {
		t.Errorf("知らせ = %+v", n)
	}
	if !app.UpdateCheckEnabled() || app.UpdateCheckEnvOff() {
		t.Errorf("既定の切り替え: enabled=%v envOff=%v", app.UpdateCheckEnabled(), app.UpdateCheckEnvOff())
	}

	// 画面の帯（管理者を作ってから。ローカルモードは最初の管理者として通す）
	if _, err := store.CreateUser(context.Background(), app.inst.DB, "boss", "boss", "", "admin"); err != nil {
		t.Fatal(err)
	}
	account := func() string {
		t.Helper()
		req, _ := http.NewRequest("GET", app.URL()+"account", nil)
		req.Header.Set("Accept-Language", "ja")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("アカウント設定: %d %s", res.StatusCode, b)
		}
		return string(b)
	}
	if body := account(); !strings.Contains(body, "新しい版 v1.1.0 があります（いまの版は v1.0.0）。") ||
		!strings.Contains(body, `href="https://example.invalid/releases/tag/v1.1.0"`) {
		t.Errorf("画面に帯が無い")
	}
	// 止め方の案内: トレイを出す前（fakeUI は onReady を持たない＝headless・--no-tray と同じ）は環境変数、
	// トレイが MarkTrayShown した後はトレイのメニュー
	if body := account(); !strings.Contains(body, "環境変数 LOOPTRACK_UPDATE_CHECK=off") || strings.Contains(body, "トレイのメニュー") {
		t.Errorf("トレイなしの帯の止め方の案内が環境変数でない")
	}
	app.MarkTrayShown()
	if body := account(); !strings.Contains(body, "トレイのメニューの「新しい版を確認する」") || strings.Contains(body, "LOOPTRACK_UPDATE_CHECK") {
		t.Errorf("トレイありの帯の止め方の案内がトレイのメニューでない")
	}

	// メニューの先頭の項目: リリースのページを開く
	app.OpenUpdate()
	if got := rec.openedURLs(); len(got) != 1 || got[0] != "https://example.invalid/releases/tag/v1.1.0" {
		t.Errorf("OpenUpdate が開いたもの = %v", got)
	}

	// 確認を止める: 控えが off・知らせと帯がその場で消える（知らせの変わり方はここから数える。最初の知らせは
	// OnUpdateNotice を登録する前に立つことがある）
	mu.Lock()
	changes = nil
	mu.Unlock()
	if app.SetUpdateCheck(false) {
		t.Error("止めた後も確認する状態を返した")
	}
	if app.UpdateNotice() != nil {
		t.Error("止めた後も知らせが残っている")
	}
	b, err := os.ReadFile(app.Paths().UpdateCheck())
	if err != nil || !strings.Contains(string(b), `"check": "off"`) {
		t.Errorf("控えに off が無い: %v %s", err, b)
	}
	if strings.Contains(account(), "appbar-update") {
		t.Error("止めた後も画面に帯がある")
	}
	// 止めた後に確かめ直しても通信しない（控えの最後の結果が disabled になるまで待ち、一覧を取った回数が増えていない）
	before := hits.Load()
	app.updates.runner.Wake()
	waitFor(t, "止めた後の確認の結果（disabled）", func() bool {
		r := app.updates.store.Load()
		return r.Last != nil && r.Last.Status == updatecheck.StatusDisabled
	})
	if hits.Load() != before {
		t.Errorf("止めた後に確認先へ通信した（%d → %d）", before, hits.Load())
	}
	if app.UpdateNotice() != nil {
		t.Error("止めた後の確認で知らせが戻った")
	}

	// 入れ直す: 24 時間を待たずに確かめ、知らせが戻る（止めたときに最後に成功した結果も消えているので、戻るのは新しい確認による）
	if !app.SetUpdateCheck(true) {
		t.Error("入れ直した後に確認しない状態を返した")
	}
	waitFor(t, "入れ直した後の知らせ", func() bool { return app.UpdateNotice() != nil })
	if hits.Load() <= before {
		t.Error("入れ直した後に確認先へ通信していない")
	}
	b, _ = os.ReadFile(app.Paths().UpdateCheck())
	if strings.Contains(string(b), `"check": "off"`) {
		t.Errorf("入れ直した後も控えが off: %s", b)
	}
	mu.Lock()
	if got := strings.Join(changes, " "); got != "nil v1.1.0" {
		t.Errorf("知らせの変わり方 = %q, want \"nil v1.1.0\"（止めて消え、入れ直して戻る）", got)
	}
	mu.Unlock()

	// 確認先に届かない回（通信の失敗）は、控えの最後に成功した結果で知らせを残す（オフラインの起動・一時的な失敗）
	recheck := func(what string, want updatecheck.Status) {
		t.Helper()
		n, h := hits.Load(), app.updates.handled.Load()
		app.updates.runner.Wake()
		// 確認先に届き、その回の結果を知らせに反映し終えるまで待つ（控えに書いた後、知らせに反映する前に読まない）
		waitFor(t, what, func() bool { return hits.Load() > n && app.updates.handled.Load() > h })
		if r := app.updates.store.Load(); r.Last == nil || r.Last.Status != want {
			t.Fatalf("%s: 控えの最後の結果 = %+v, want %s", what, r.Last, want)
		}
	}
	mode.Store(sourceDown)
	recheck("通信の失敗の結果", updatecheck.StatusError)
	if n := app.UpdateNotice(); n == nil || n.Version != "v1.1.0" {
		t.Errorf("通信に失敗した回に知らせが消えた: %+v", n)
	}
	if !strings.Contains(account(), "新しい版 v1.1.0 があります") {
		t.Error("通信に失敗した回に帯が消えた")
	}
	// 対照: 成功した確認で新しい版が無いと分かれば消える
	mode.Store(sourceUpToDate)
	recheck("新しい版が無い結果", updatecheck.StatusUpToDate)
	if n := app.UpdateNotice(); n != nil {
		t.Errorf("新しい版が無いと分かった後も知らせが残る: %+v", n)
	}
	// その後に通信に失敗しても、最後に成功した結果（up_to_date）なので知らせは戻らない
	mode.Store(sourceDown)
	recheck("通信の失敗の結果（2 回目）", updatecheck.StatusError)
	if n := app.UpdateNotice(); n != nil {
		t.Errorf("新しい版が無いと分かった後の通信の失敗で知らせが戻った: %+v", n)
	}
	if len(rec.alerts) != 0 {
		t.Errorf("更新の知らせで OS の通知を出した: %v", rec.alerts)
	}
}

// 環境変数 LOOPTRACK_UPDATE_CHECK=off のときは、メニューで入れても確認しない（トレイはチェックを押せなくする）。
func TestUpdateCheckEnvOff(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		vars   map[string]string
		envOff bool
	}{
		{map[string]string{updatecheck.EnvCheck: "OFF"}, true},
		{map[string]string{updatecheck.EnvCheck: "on"}, false},
		{nil, false}, // 対照: 環境変数が無ければ入れられる
	} {
		o := &Options{Env: env.FromMap(c.vars), Version: "v1.0.0", GOOS: runtime.GOOS, Alert: (&recorder{}).alert}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		a := &App{opts: o, logger: logger, updates: o.newUpdates(Paths{DataDir: dir}, logger, "")}
		if a.UpdateCheckEnvOff() != c.envOff {
			t.Errorf("%v: UpdateCheckEnvOff = %v", c.vars, a.UpdateCheckEnvOff())
		}
		if got := a.SetUpdateCheck(true); got == c.envOff {
			t.Errorf("%v: 入れた後の状態 = %v", c.vars, got)
		}
		if got := a.SetUpdateCheck(false); got {
			t.Errorf("%v: 止めた後の状態 = %v", c.vars, got)
		}
	}
	// 公開鍵を渡さない（テスト・鍵の無いビルド）と、既定の確認先（GitHub）へは通信しない。渡せば確認に使う
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	noKey := (&Options{Env: env.FromMap(nil), Version: "v1.0.0", GOOS: runtime.GOOS}).newUpdates(Paths{DataDir: dir}, logger, "")
	if res := noKey.runner.Checker.Check(context.Background(), updatecheck.Prefs{}); res.Status != updatecheck.StatusNoKey {
		t.Errorf("鍵なし・既定の確認先の結果 = %s, want no_key", res.Status)
	}
	withKey := (&Options{Env: env.FromMap(nil), Version: "v1.0.0", GOOS: runtime.GOOS, UpdatePublicKey: "RWQkey"}).newUpdates(Paths{DataDir: dir}, logger, "")
	if withKey.runner.Checker.PublicKey != "RWQkey" || withKey.store.Path != (Paths{DataDir: dir}).UpdateCheck() {
		t.Errorf("鍵・控えの置き場が渡っていない: %q %q", withKey.runner.Checker.PublicKey, withKey.store.Path)
	}

	// 確認の部品が無い App（起動前）は確認しない・知らせも無い
	a := &App{}
	if a.UpdateCheckEnabled() || a.UpdateNotice() != nil || a.SetUpdateCheck(true) {
		t.Error("確認の部品が無い App で確認する状態を返した")
	}
}
