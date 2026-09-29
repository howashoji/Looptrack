package desktop

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// 新しい版の知らせ（DESIGN.md §5-4「新しい版の知らせ」）。確認そのもの（取得・署名・チャンネル・控え）は internal/updatecheck が行い、
// ここは結果をトレイのメニューの先頭と画面の帯（localserve → server.Config.UpdateNotice）に渡し、確認を止める切り替えを受け持つ。
// OS の通知（Options.Alert）は出さない（利用者の決定: 既定で出さない）。置き換えの手順は replace.go、
// ここはトレイの先頭の項目（ApplyUpdate）・画面の帯の「更新する」（StartUpdate。bannerApplier → server.Config.UpdateApplier）・
// 自動の置き換え（控えの "auto"。既定は無効）から呼ぶ入口を受け持つ。

// updates は起動中のインスタンスの新しい版の確認。
type updates struct {
	store  *updatecheck.Store
	runner *updatecheck.Runner
	getenv func(string) string

	mu     sync.Mutex
	notice *updatecheck.Notice
	// source は知らせの元になった確認の結果（今回か控えの last_ok。置き換えに使う資産の URL と署名された SHA-256 を持つ）
	source   *updatecheck.Result
	onChange []func(*updatecheck.Notice)
	// onAuto は自動の置き換えを入れているときに、知らせが出るたびに呼ぶ（primary が App.autoApply を入れる）
	onAuto func(version string)
	// tray はトレイを出したか（画面の帯の止め方の案内: 出していればメニュー、出していなければ環境変数）
	tray atomic.Bool
	// handled は知らせに反映した確認の結果の数（テストが「この回の結果まで反映した」ことを待つ）
	handled atomic.Int64
	// app は起動中のインスタンス（画面の帯の「更新する」が使う。サーバを起こした後で primary が入れる）
	app atomic.Pointer[App]
}

// bannerApplier は画面の帯の「更新する」（server.Config.UpdateApplier）。サーバはインスタンスより先に起こすので、
// インスタンスが入るまで（updates.app が nil の間）はボタンを出さず、何もしない。
type bannerApplier struct{ u *updates }

func (b *bannerApplier) UpdateReplaceable() bool {
	a := b.u.app.Load()
	return a != nil && a.UpdateReplaceable()
}

func (b *bannerApplier) StartUpdate() bool {
	a := b.u.app.Load()
	return a != nil && a.StartUpdate()
}

func (b *bannerApplier) UpdateApplyState() (running bool, failedVersion, failedReason string) {
	a := b.u.app.Load()
	if a == nil {
		return false, "", ""
	}
	return a.UpdateApplyState()
}

// newUpdates は確認の部品を組む（動かすのは run）。結果は 1 回ごとにログに 1 行残す。
func (o *Options) newUpdates(paths Paths, logger *slog.Logger) *updates {
	u := &updates{store: &updatecheck.Store{Path: paths.UpdateCheck()}, getenv: o.Env.Get}
	u.runner = &updatecheck.Runner{
		Checker: &updatecheck.Checker{
			Current:   o.Version,
			PublicKey: o.UpdatePublicKey,
			Asset:     updatecheck.DesktopAsset(o.GOOS, runtime.GOARCH),
			Getenv:    o.Env.Get,
			Client:    o.UpdateClient,
		},
		Store: u.store,
		OnResult: func(res updatecheck.Result) {
			logger.Info("desktop", "action", "update-check", "result", res.String())
			u.set(res)
		},
	}
	return u
}

// run は ctx が終わるまで確かめ続ける（起動時に 1 回、その後は 24 時間ごと）。戻った後は控えに書かない。
func (u *updates) run(ctx context.Context) { u.runner.Run(ctx) }

// set は確認の結果から知らせを決め、変われば見ている側（トレイ）に渡す。通信に失敗した回は、控えにある最後に成功した結果の
// 知らせを残す（updatecheck.NoticeFor。Runner は OnResult の前に控えへ書くので、LastOK は今回より前の成功のまま）。
// 自動の置き換えを入れていれば（控えの "auto"）、知らせが出たときに onAuto を呼ぶ（handled を数える前。テストが待てるように）。
func (u *updates) set(res updatecheck.Result) {
	rec := u.store.Load()
	n := updatecheck.NoticeFor(res, rec.LastOK)
	var src *updatecheck.Result
	switch {
	case n != nil && res.Available():
		src = &res
	case n != nil:
		src = rec.LastOK
	}
	u.setNotice(n, src)
	if n != nil && rec.Auto && u.onAuto != nil {
		u.onAuto(n.Version)
	}
	u.handled.Add(1)
}

func (u *updates) setNotice(n *updatecheck.Notice, src *updatecheck.Result) {
	u.mu.Lock()
	changed := !sameNotice(u.notice, n)
	u.notice, u.source = n, src
	fs := slices.Clone(u.onChange)
	u.mu.Unlock()
	if changed {
		for _, f := range fs {
			f(n)
		}
	}
}

func sameNotice(a, b *updatecheck.Notice) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (u *updates) current() *updatecheck.Notice {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.notice
}

// sourceResult は知らせの元になった確認の結果（知らせが無ければ nil）。
func (u *updates) sourceResult() *updatecheck.Result {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.source
}

// envOff は環境変数で確認を止めているか（メニューの切り替えでは覆せない）。
func (u *updates) envOff() bool {
	return strings.EqualFold(strings.TrimSpace(u.getenv(updatecheck.EnvCheck)), "off")
}

// enabled は確認するか（環境変数と控えの切り替えのどちらも off でない）。
func (u *updates) enabled() bool {
	return !u.envOff() && !strings.EqualFold(strings.TrimSpace(u.store.Load().Check), "off")
}

// UpdateNotice は知らせる新しい版（無ければ nil）。画面の帯とトレイのメニューの先頭が使う。
func (a *App) UpdateNotice() *updatecheck.Notice {
	if a.updates == nil {
		return nil
	}
	return a.updates.current()
}

// MarkTrayShown はトレイを出したことを記録する（トレイの onReady が呼ぶ）。画面の帯の止め方の案内が
// トレイのメニュー（「新しい版を確認する」）を指すようになる。呼ばれない（headless・--no-tray・トレイを出せない環境）間は、
// 環境変数 LOOPTRACK_UPDATE_CHECK=off を案内する。
func (a *App) MarkTrayShown() {
	if a.updates != nil {
		a.updates.tray.Store(true)
	}
}

// OnUpdateNotice は知らせが変わるたびに f を呼ぶ（トレイがメニューの先頭の項目を出し入れする）。確認の goroutine から呼ばれる。
func (a *App) OnUpdateNotice(f func(*updatecheck.Notice)) {
	if a.updates == nil {
		return
	}
	a.updates.mu.Lock()
	a.updates.onChange = append(a.updates.onChange, f)
	a.updates.mu.Unlock()
}

// UpdateCheckEnabled は新しい版を確認するか（トレイの「新しい版を確認する」のチェック）。
func (a *App) UpdateCheckEnabled() bool { return a.updates != nil && a.updates.enabled() }

// UpdateCheckEnvOff は環境変数 LOOPTRACK_UPDATE_CHECK=off で止めているか（トレイはチェックを押せなくする）。
func (a *App) UpdateCheckEnvOff() bool { return a.updates != nil && a.updates.envOff() }

// SetUpdateCheck は確認を入れる / 止める（トレイの「新しい版を確認する」）。止めると控えに "check": "off" を書き、
// 知らせをその場で消す（次の回からは通信しない）。入れると控えを既定に戻し、24 時間を待たずに確かめ直す。切り替えた後の状態を返す。
func (a *App) SetUpdateCheck(on bool) bool {
	if a.updates == nil {
		return false
	}
	if err := a.updates.store.SetEnabled(on); err != nil {
		a.logger.Warn("update-check", "on", on, "err", err)
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.update_check_failed", "reason", err), true)
		return a.UpdateCheckEnabled()
	}
	a.logger.Info("desktop", "action", "update-check", "on", on)
	if on {
		a.updates.runner.Wake()
	} else {
		a.updates.setNotice(nil, nil)
	}
	return a.UpdateCheckEnabled()
}

// OpenUpdate は知らせた新しい版のリリースのページをブラウザで開く（トレイのメニューの先頭の項目）。
// ページの URL が無い（https:// でない）ときは画面を開く（画面の帯に同じ知らせが出ている）。
func (a *App) OpenUpdate() {
	n := a.UpdateNotice()
	if n == nil || n.URL == "" {
		a.OpenUI()
		return
	}
	a.logger.Info("desktop", "action", "open-update", "version", n.Version)
	if !a.opts.Open(n.URL) {
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.open_browser_failed", "url", n.URL), false)
	}
}

// replacer は置き換えの部品を今のインスタンスから組む（replace.go）。
func (a *App) replacer() *replacer {
	r := &replacer{
		GOOS: a.opts.GOOS, Launcher: a.launcher, WorkDir: filepath.Join(a.paths.DataDir, "updates"), BundleID: BundleID,
		Client: a.opts.UpdateClient, Run: a.opts.UpdateRun, Start: a.opts.UpdateStart,
	}
	if r.GOOS == "linux" {
		r.AppImage = a.opts.Env.Get("APPIMAGE")
	}
	if r.Run == nil {
		r.Run = execRun
	}
	if r.Start == nil {
		r.Start = startDetached
	}
	return r
}

// ReplaceSupported はこの環境で自分を置き換えられるか（macOS の .app・Linux の AppImage）。トレイの「新しい版を自動で入れる」を出すか。
func (a *App) ReplaceSupported() bool {
	_, err := a.replacer().target()
	return err == nil
}

// UpdateReplaceable は知らせている新しい版に 1 クリックで置き換えられるか（置き換えられる環境で、署名を確かめた結果が資産の
// URL と SHA-256 を持つ）。false なら先頭の項目はリリースのページを開く（OpenUpdate）。
func (a *App) UpdateReplaceable() bool {
	if a.updates == nil || !a.ReplaceSupported() {
		return false
	}
	return check(a.updates.sourceResult()) == nil
}

// ApplyUpdate は知らせている新しい版を取得・確認して置き換え、起動し直す（今のインスタンスは Quit で終わる）。
// 起動し直しに進んだら true。置き換えられない・失敗したときは今の版のまま動き続け、理由を知らせる（Alert）。
func (a *App) ApplyUpdate() bool { return a.applyUpdate(false) }

// autoApply は自動の置き換え（控えの "auto"）。同じ版は 1 回しか試さない（失敗の知らせを 24 時間ごとに繰り返さない）。
func (a *App) autoApply(version string) {
	a.autoMu.Lock()
	if a.autoTried == nil {
		a.autoTried = map[string]bool{}
	}
	tried := a.autoTried[version]
	a.autoTried[version] = true
	a.autoMu.Unlock()
	if tried {
		return
	}
	if !a.UpdateReplaceable() {
		a.logger.Info("desktop", "action", "update-auto", "version", version, "result", "skip")
		return
	}
	a.applyUpdate(true)
}

// StartUpdate は画面の帯の「更新する」（POST {base}/update/apply）。トレイの ApplyUpdate と同じ置き換えを背景で始める。
// 進行中の印（updating）はここで立てるので、戻った直後に描く画面の帯は進行中を出す。置き換えられない
// （UpdateReplaceable が false）・進行中なら false で何もしない。トレイの有無（--no-tray）に依らない。
func (a *App) StartUpdate() bool {
	if a.updates == nil || !a.UpdateReplaceable() || !a.updating.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer a.updating.Store(false)
		a.runUpdate(false)
	}()
	return true
}

// UpdateApplyState は置き換えの状態（画面の帯）。進行中か・直近の失敗の版と理由（失敗が無ければ空）。
func (a *App) UpdateApplyState() (running bool, failedVersion, failedReason string) {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	return a.updating.Load(), a.failVersion, a.failReason
}

// setUpdateFailure は置き換えの失敗を記録する（空で消す）。
func (a *App) setUpdateFailure(version, reason string) {
	a.failMu.Lock()
	a.failVersion, a.failReason = version, reason
	a.failMu.Unlock()
}

func (a *App) applyUpdate(auto bool) bool {
	if a.updates == nil || !a.updating.CompareAndSwap(false, true) {
		return false
	}
	defer a.updating.Store(false)
	return a.runUpdate(auto)
}

// runUpdate は置き換えの本体（呼ぶ側が updating を立てておく）。失敗は版と理由を記録する（画面の帯。次の置き換えの開始で消える）。
func (a *App) runUpdate(auto bool) bool {
	src := a.updates.sourceResult()
	r := a.replacer()
	if !auto && (check(src) != nil || !a.ReplaceSupported()) {
		a.OpenUpdate()
		return false
	}
	version := ""
	if src != nil && src.Latest != nil {
		version = src.Latest.Tag
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-a.quit:
			cancel()
		case <-ctx.Done():
		}
	}()
	a.logger.Info("desktop", "action", "update-apply", "version", version, "auto", auto)
	a.setUpdateFailure("", "")
	ap, err := r.apply(ctx, src)
	var nw *notWritableError
	switch {
	case errors.As(err, &nw):
		a.logger.Warn("update-apply", "version", version, "err", err)
		a.setUpdateFailure(version, err.Error())
		if nw.DMG != "" && !auto {
			if out, oerr := r.Run(ctx, "open", nw.DMG); oerr != nil {
				a.logger.Warn("update-open-dmg", "err", oerr, "out", out)
			}
			a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.update_not_writable_dmg", "dir", nw.Dir, "app", AppName), false)
			return false
		}
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.update_not_writable", "dir", nw.Dir), true)
		if !auto {
			a.OpenUpdate()
		}
		return false
	case err != nil:
		a.logger.Warn("update-apply", "version", version, "err", err)
		a.setUpdateFailure(version, err.Error())
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.update_failed", "version", version, "reason", err), true)
		return false
	}
	defer ap.cleanup()
	if err := ap.relaunch(); err != nil {
		a.logger.Warn("update-relaunch", "version", version, "err", err)
		if uerr := ap.undo(); uerr != nil {
			a.logger.Error("update-undo", "err", uerr)
			a.setUpdateFailure(version, i18n.T(a.opts.Lang, "desktop.alert.update_undo_failed", "reason", err, "undo", uerr, "prev", ap.prev))
			a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.update_undo_failed", "reason", err, "undo", uerr, "prev", ap.prev), true)
			return false
		}
		a.setUpdateFailure(version, err.Error())
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.update_relaunch_failed", "version", version, "reason", err), true)
		return false
	}
	a.logger.Info("desktop", "action", "update-applied", "version", version, "path", ap.cur, "prev", ap.prev)
	a.Quit()
	return true
}

// UpdateAutoEnabled は新しい版を自動で入れるか（トレイの「新しい版を自動で入れる」のチェック。既定は外れている）。
func (a *App) UpdateAutoEnabled() bool { return a.updates != nil && a.updates.store.Load().Auto }

// SetUpdateAuto は自動の置き換えを入れる / 止める（控えの "auto"）。入れたときに新しい版を知らせていれば、その場で置き換えに進む。
// 切り替えた後の状態を返す。
func (a *App) SetUpdateAuto(on bool) bool {
	if a.updates == nil {
		return false
	}
	if err := a.updates.store.SetAuto(on); err != nil {
		a.logger.Warn("update-auto", "on", on, "err", err)
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.update_check_failed", "reason", err), true)
		return a.UpdateAutoEnabled()
	}
	a.logger.Info("desktop", "action", "update-auto", "on", on)
	if on {
		if n := a.UpdateNotice(); n != nil {
			go a.autoApply(n.Version)
		}
	}
	return a.UpdateAutoEnabled()
}
