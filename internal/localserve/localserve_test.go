package localserve

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellQuoteAll(t *testing.T) {
	if got := shellQuoteAll([]string{"/a b/im.db", "/x'y"}); got != `'/a b/im.db' '/x'\''y'` {
		t.Errorf("shellQuoteAll = %s", got)
	}
}

// デスクトップ版の起動: 無いディレクトリに DB と鍵を作り、スキーマを最新にして待ち受ける。
// 管理者 0 人なので画面は初回設定へ転送し、healthz は通す。Shutdown で止まり、2 回目の起動は同じ鍵を読む。
func TestStartAndShutdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Looptrack")
	dbPath := filepath.Join(dir, "looptrack.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := func() (*Instance, string) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		inst, err := Start(context.Background(), Options{DBPath: dbPath, Listener: ln, Logger: logger})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		return inst, "http://" + ln.Addr().String()
	}
	inst, base := start()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(base + "/looptrack/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", res, err)
	}
	res.Body.Close()
	res, err = client.Get(base + "/looptrack/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.HasSuffix(res.Header.Get("Location"), "/looptrack/first-run") {
		t.Errorf("/looptrack/ = %d %s, want 303 → /looptrack/first-run", res.StatusCode, res.Header.Get("Location"))
	}
	key1, err := os.ReadFile(dbPath + SecretKeySuffix)
	if err != nil {
		t.Fatalf("鍵のファイル: %v", err)
	}
	// 手元の接続を閉じてから止める。2 回目の Get が 1 回目の接続の返却（アイドルに戻る）より先に走ると、
	// Transport は予備の接続を張り、それは要求を送らないままアイドルに残る。サーバ側ではその接続が StateNew のままで、
	// http.Server.Shutdown は StateNew の接続を「5 秒たつまでアイドルとみなさない」（net/http の closeIdleConns）ため、
	// 5 秒の期限の Shutdown が context deadline exceeded で時々落ちていた（予備の接続があると必ず 5 秒以上かかる）。
	client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := inst.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-inst.Done():
		if err != nil {
			t.Errorf("Done: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Done が返らない")
	}
	if _, err := client.Get(base + "/looptrack/healthz"); err == nil {
		t.Error("Shutdown の後も応答する")
	}

	inst, _ = start()
	defer inst.Shutdown(context.Background())
	key2, _ := os.ReadFile(dbPath + SecretKeySuffix)
	if string(key1) != string(key2) {
		t.Error("2 回目の起動で鍵が変わった")
	}
}

// ローカルモードは認証を省くので、loopback 以外の待ち受けでは起動しない。
func TestStartRejectsNonLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = Start(context.Background(), Options{DBPath: filepath.Join(t.TempDir(), "x.db"), Listener: ln,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil {
		t.Fatal("全アドレスの待ち受けで起動した")
	}
}
