package main

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// サーバ版の新しい版の知らせ（DESIGN.md §5-1「サーバ版の知らせ」）。確認そのもの（取得・チャンネル・署名・控え）は
// internal/updatecheck が行い、ここは serve の起動時に Runner を動かし、結果を起動時のログ・管理画面の帯（server.Config.UpdateNotice）・
// GET /api/v1/dist の server_update（looptrack doctor）に渡す。置き換えはここでは行わない（install.sh --upgrade と、
// 設定で有効にしたときだけの systemd timer。deploy/install.sh の --auto-upgrade）。

// serverUpdateOptions は serve の確認の設定（テストが確認先・公開鍵・環境を差し替える）。
type serverUpdateOptions struct {
	Version   string
	PublicKey string // selfupdate.MinisignPublicKey（空なら確認先が GitHub のとき通信しない）
	GOOS      string
	GOARCH    string
	Getenv    func(string) string
	Client    *http.Client // nil なら updatecheck の既定
	Lang      i18n.Lang    // ログの文面の言語
}

// serverUpdates は動いている serve の新しい版の確認。
type serverUpdates struct {
	runner *updatecheck.Runner
	logger *slog.Logger
	lang   i18n.Lang

	mu     sync.Mutex
	notice *updatecheck.Notice
	lastOK *updatecheck.Result // 最後に成功した確認（起動時は控えの last_ok。控えを書けない置き場でもメモリに持つ）
	// handled は知らせに反映した確認の結果の数（テストが「この回の結果まで反映した」ことを待つ）
	handled atomic.Int64
}

// updateStatePath は控え（update-check.json）の置き場。install.sh の unit の StateDirectory（systemd が $STATE_DIRECTORY で渡す。
// /var/lib/looptrack）→ SQLite の DB と同じディレクトリ（compose の /data・ローカルモード）→ どちらも無ければ空（控えを書かない）。
// unit のサンドボックス（ProtectSystem=strict）で書けるのは StateDirectory だけなので、そこを先に見る。
// 複数あれば最初。区切りは OS のパスの並びの区切り（systemd の Linux は :）で読む。: で切ると Windows のドライブ文字
// （D:\…）が D だけになり、作業ディレクトリの下の D\update-check.json に書いてしまう。
func updateStatePath(getenv func(string) string) string {
	if ds := filepath.SplitList(getenv("STATE_DIRECTORY")); len(ds) > 0 && strings.TrimSpace(ds[0]) != "" {
		return filepath.Join(ds[0], updatecheck.FileName)
	}
	if p, ok := store.SQLitePath(getenv("LOOPTRACK_DSN")); ok && p != "" && p != ":memory:" {
		return filepath.Join(filepath.Dir(p), updatecheck.FileName)
	}
	return ""
}

// newServerUpdates は確認の部品を組む（動かすのは run）。
func newServerUpdates(o serverUpdateOptions, logger *slog.Logger) *serverUpdates {
	st := &updatecheck.Store{Path: updateStatePath(o.Getenv)}
	u := &serverUpdates{logger: logger, lang: o.Lang}
	if st.Path != "" {
		u.lastOK = st.Load().LastOK
	}
	u.runner = &updatecheck.Runner{
		Checker: &updatecheck.Checker{
			Current:   o.Version,
			PublicKey: o.PublicKey,
			Asset:     updatecheck.ArchiveAsset(o.GOOS, o.GOARCH),
			Getenv:    o.Getenv,
			Client:    o.Client,
		},
		Store:    st, // Path が空なら読み書きは失敗し、既定の設定（環境変数だけ）で確かめる
		OnResult: u.set,
	}
	return u
}

// run は ctx が終わるまで確かめ続ける（起動時に 1 回、その後は 24 時間ごと）。
func (u *serverUpdates) run(ctx context.Context) { u.runner.Run(ctx) }

// set は確認の結果から知らせを決め（updatecheck.NoticeFor。通信に失敗した回は、最後に成功した結果の知らせを残す）、
// 毎回 1 行をログに残す。新しい版があれば、更新の 1 行を添えて Warn で出す（起動時のログ・24 時間ごと）。
func (u *serverUpdates) set(res updatecheck.Result) {
	u.mu.Lock()
	n := updatecheck.NoticeFor(res, u.lastOK)
	if res.Status == updatecheck.StatusUpToDate || res.Status == updatecheck.StatusAvailable {
		r := res
		u.lastOK = &r
	}
	u.notice = n
	u.mu.Unlock()
	u.logger.Info("update-check", "result", res.String())
	if n != nil {
		u.logger.Warn(i18n.T(u.lang, "cmd.serve.update_available", "version", n.Version, "current", n.Current,
			"command", updatecheck.ServerUpgradeCommand), "version", n.Version, "current", n.Current)
	}
	u.handled.Add(1)
}

// current は知らせる新しい版（無ければ nil）。管理画面の帯と GET /api/v1/dist の server_update が使う。
func (u *serverUpdates) current() *updatecheck.Notice {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.notice
}
