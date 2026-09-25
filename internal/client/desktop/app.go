// Package desktop はデスクトップ版（looptrack の desktop ビルド・DESIGN.md §5-14）の起動と、トレイから呼ぶ操作。
//
// ダブルクリック（引数なし）か looptrack desktop で起動すると:
//  1. データの置き場（ResolvePaths）にロックのファイルを作り、OS のファイルロックを取る
//  2. 取れなければ（既に起動している）、起動中のインスタンスの URL（desktop.json）が応答するのを待って、ブラウザで開くだけで終わる
//  3. 取れたら、127.0.0.1 の決まったポート（前回と同じ・既定 18090）でローカルモードのサーバ（internal/localserve）を上げ、
//     既定のブラウザで画面を開き、トレイ / メニューバー（UI。desktop ビルドだけ）を出す。管理者が 0 人なら画面は初回設定
//
// トレイの部品（fyne.io/systray。macOS は cgo）は internal/client/desktop/tray（-tags desktop）に分け、このパッケージは
// cgo なしでビルド・テストできるようにしている（UI を差し替える）。
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/atotto/clipboard"

	"github.com/howashoji/looptrack/internal/client/browser"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/localserve"
	"github.com/howashoji/looptrack/internal/privfile"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
)

// BundleID は macOS の Bundle ID（Info.plist の CFBundleIdentifier・LaunchAgent の Label）。
// **仮の値**（公開名・識別子は公開の準備で確定する）。配布物の組み立て（deploy/release/desktop/build-macos.sh）はここから読むので、
// 変えるときはこの 1 か所だけを直す。
var BundleID = "net.howashoji.looptrack"

// DefaultPort は最初の起動で使うポート（looptrack serve の既定 8090 と重ならないもの）。使えなければ OS に選ばせ、
// 以後はそのポートを使う（AI の接続設定の URL が変わらないように、前回のポートを desktop.json に残す）。
const DefaultPort = 18090

// BasePath は URL の接頭辞（looptrack serve と同じ）。
const BasePath = "/looptrack"

// UI はトレイ / メニューバー。Run は a.Done() が閉じたら（Quit）戻る（macOS はメインスレッドで呼ぶ）。
type UI interface {
	Run(a *App)
}

// Options は Main の外から渡すもの（テストで差し替える）。
type Options struct {
	Version string
	Env     env.Env
	Stdout  io.Writer
	Stderr  io.Writer
	UI      UI                    // nil ならトレイを出さない（シグナルか looptrack desktop --quit で止まる）
	Open    func(url string) bool // ブラウザで開く（nil なら browser.Open）
	Alert   func(title, msg string, isError bool) bool
	GOOS    string    // 空なら runtime.GOOS
	Home    string    // 空なら os.UserHomeDir
	Lang    i18n.Lang // 空なら環境変数（LOOPTRACK_LANG・LC_ALL・LC_MESSAGES・LANG）から決める
}

// state は desktop.json（起動中のインスタンスと、次の起動で使うポート）。
type state struct {
	PID     int    `json:"pid,omitempty"`
	Port    int    `json:"port"`
	URL     string `json:"url,omitempty"`
	Version string `json:"version,omitempty"`
	Started string `json:"started,omitempty"`
}

func readState(path string) state {
	var s state
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func writeState(path string, s state) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return privfile.WriteFile(path, append(b, '\n'))
}

// Main は looptrack desktop（desktop ビルドでは引数なしの起動も）。
//
//	--background        ブラウザを開かない（ログイン時の自動起動）
//	--no-tray           トレイを出さない（表示の無い環境・テスト）
//	--status            起動中なら URL を出して 0、起動していなければ 1
//	--quit              起動中のインスタンスを止める（トレイが出ない環境の逃げ道）
//	--enable-autostart  ログイン時の起動を登録して終わる（Windows のインストーラの選択肢）
//	--install-cli       CLI を使えるようにして終わる（同上）
//	--unregister        アンインストールの後始末（登録と、このアプリが置いた CLI のコピーを消す。データは消さない）
//
// 後ろの 3 つはサーバを上げず、ロックもデータの置き場も作らない（インストーラ / アンインストーラから呼ぶため）。
func Main(args []string, o Options) int {
	o.defaults()
	fs := flag.NewFlagSet("looptrack desktop", flag.ContinueOnError)
	fs.SetOutput(o.Stderr)
	background := fs.Bool("background", false, i18n.T(o.Lang, "desktop.arg.background"))
	noTray := fs.Bool("no-tray", false, i18n.T(o.Lang, "desktop.arg.no_tray"))
	status := fs.Bool("status", false, i18n.T(o.Lang, "desktop.arg.status"))
	quit := fs.Bool("quit", false, i18n.T(o.Lang, "desktop.arg.quit"))
	enableAutostart := fs.Bool("enable-autostart", false, i18n.T(o.Lang, "desktop.arg.enable_autostart"))
	installCLI := fs.Bool("install-cli", false, i18n.T(o.Lang, "desktop.arg.install_cli"))
	unregister := fs.Bool("unregister", false, i18n.T(o.Lang, "desktop.arg.unregister"))
	// macOS の古い版は Finder からの起動に -psn_… を付ける
	var rest []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			rest = append(rest, a)
		}
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *enableAutostart || *installCLI || *unregister {
		return o.maintain(*enableAutostart, *installCLI, *unregister)
	}
	paths, err := ResolvePaths(o.Env, o.GOOS, o.Home)
	if err != nil {
		return o.fail(i18n.T(o.Lang, "desktop.err.data_dir_unknown"), err)
	}
	if err := privfile.MkdirAll(paths.DataDir); err != nil {
		return o.fail(i18n.T(o.Lang, "desktop.err.data_dir_create"), err)
	}
	release, locked, err := tryLock(paths.Lock())
	// --status・--quit も一瞬ロックを取る（起動していないことを確かめるため）ので、起動はそれと重なっても少し待って取り直す
	for i := 0; err == nil && !locked && !*status && !*quit && i < 20; i++ {
		if st := readState(paths.State()); st.PID > 0 {
			break // 起動中のインスタンスがいる
		}
		time.Sleep(100 * time.Millisecond)
		release, locked, err = tryLock(paths.Lock())
	}
	if err != nil {
		return o.fail(i18n.T(o.Lang, "desktop.err.lock_create"), err)
	}
	if !locked {
		return o.secondary(paths, *background, *status, *quit)
	}
	defer release()
	if *status {
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.not_running"))
		return 1
	}
	if *quit {
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.not_running"))
		return 0
	}
	return o.primary(paths, *background, *noTray)
}

func (o *Options) defaults() {
	if o.Lang == "" {
		o.Lang = i18n.FromEnv(func(k string) string { return o.Env.Get(k) })
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Open == nil {
		e := o.Env
		o.Open = func(u string) bool { return browser.Open(e, u) }
	}
	if o.Alert == nil {
		o.Alert = showAlert
	}
}

// fail は起動の失敗を端末（あれば）と知らせ（ダブルクリックの起動では端末が無い）に出す。
func (o *Options) fail(what string, err error) int {
	msg := fmt.Sprintf("%s: %s", what, i18n.Text(o.Lang, err))
	fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "desktop.prefix.error", "msg", msg))
	o.Alert(AppName, msg, true)
	return 1
}

// stopWait は --quit が「止まった（ロックが外れた）」のを待つ時間。
const stopWait = 20 * time.Second

// secondary は既に起動しているときの処理（二重に起動しない）。
func (o *Options) secondary(paths Paths, background, status, quit bool) int {
	if quit {
		return o.quit(paths)
	}
	url, err := waitReady(paths, 30*time.Second)
	if err != nil {
		if status {
			fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.starting"))
			return 1
		}
		return o.fail(i18n.T(o.Lang, "desktop.err.no_response", "app", AppName, "log", paths.Log()), err)
	}
	if status {
		fmt.Fprintln(o.Stdout, url)
		return 0
	}
	fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.already_running", "url", url))
	if !background && !o.Open(url) {
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.open_in_browser", "url", url))
	}
	return 0
}

// quit は起動中のインスタンスを止める（トレイが出ない環境・インストーラ / アンインストーラの逃げ道）。
//
// まず askStop で終了を頼み（unix は SIGTERM、Windows はトレイの窓に WM_CLOSE）、止まらなければ forceStop
// （Windows の TerminateProcess。unix には無い）。Windows は GUI のプロセスにシグナルを送れないので、
// 頼む手立てが無い（--no-tray で動いている・窓が見つからない）ときも強制終了に進む。
func (o *Options) quit(paths Paths) int {
	st := readState(paths.State())
	if st.PID <= 0 {
		fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "desktop.prefix.error", "msg", i18n.T(o.Lang, "desktop.err.no_pid", "path", paths.State())))
		return 1
	}
	askErr := askStop(o.Lang, st.PID)
	if askErr == nil && o.stopped(paths) {
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.stopped"))
		return 0
	}
	if err := forceStop(st.PID); err != nil {
		if askErr != nil {
			fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "desktop.prefix.error", "msg", i18n.T(o.Lang, "desktop.err.stop_failed", "pid", st.PID, "reason", askErr)))
		} else {
			fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "desktop.prefix.error", "msg", i18n.T(o.Lang, "desktop.err.stop_timeout", "wait", stopWait.String())))
		}
		return 1
	}
	if !o.stopped(paths) {
		fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "desktop.prefix.error", "msg", i18n.T(o.Lang, "desktop.err.force_stop_failed", "pid", st.PID)))
		return 1
	}
	if askErr != nil {
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.stopped_forced", "reason", askErr))
	} else {
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.stopped_forced_timeout", "wait", stopWait.String()))
	}
	return 0
}

// stopped は止まった（ロックが外れた）のを stopWait まで待つ。
func (o *Options) stopped(paths Paths) bool {
	for deadline := time.Now().Add(stopWait); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if release, ok, _ := tryLock(paths.Lock()); ok {
			release()
			return true
		}
	}
	return false
}

// maintain は起動せずに登録だけを行う（Windows のインストーラの選択肢・アンインストールの後始末）。
// サーバもロックもデータの置き場も作らない。出力は端末（GUI の exe では見えない）とログの代わりの標準エラー。
func (o *Options) maintain(autostartOn, installCLI, unregister bool) int {
	launcher, err := Launcher(o.Env)
	if err != nil || launcher == "" {
		// この条件は err が nil でも通る形で、T に直接渡すと nil のときに "<nil>" が出る。
		// nil で来る経路は確かめられていないが、条件の形に合わせて Text で空文字にしておく
		fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "desktop.prefix.error", "msg", i18n.T(o.Lang, "desktop.err.app_path_unknown", "reason", i18n.Text(o.Lang, err))))
		return 1
	}
	as, c := o.autostartFor(launcher), o.cliFor(launcher)
	rc := 0
	warn := func(what string, err error) {
		// Sprintf の %s は i18n の置き換えを通らないので、ここは Text で文面にしてから埋める
		fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "desktop.prefix.warn", "msg", fmt.Sprintf("%s: %s", what, i18n.Text(o.Lang, err))))
		rc = 1
	}
	if unregister {
		// ログイン時の起動: このアプリが登録したものだけを消す（別の場所に置いた持ち運び用のものは残す）
		switch removed, err := as.RemoveIfOurs(); {
		case err != nil:
			warn(i18n.T(o.Lang, "desktop.err.autostart_remove"), err)
		case removed:
			fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.autostart_removed"))
		}
		// CLI: このアプリが置いたものだけを消す（setup・self-update で入れたものは残す）
		switch removed, err := c.Uninstall(); {
		case err != nil:
			warn(i18n.T(o.Lang, "desktop.err.cli_remove"), err)
		case removed:
			fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.cli_removed", "path", c.Target()))
		}
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.data_kept"))
		return rc
	}
	if autostartOn {
		if err := as.Enable(); err != nil {
			warn(i18n.T(o.Lang, "desktop.err.autostart_enable"), err)
		} else {
			fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.autostart_enabled"))
		}
	}
	if installCLI {
		msg, err := c.Install(o.Lang)
		if err != nil {
			warn(i18n.T(o.Lang, "desktop.err.cli_install"), err)
		} else {
			fmt.Fprintln(o.Stdout, msg)
		}
	}
	return rc
}

// waitReady は起動中のインスタンスの URL が応答するまで待つ（起動の途中なら desktop.json がまだ古いことがある）。
func waitReady(paths Paths, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	var last error = i18n.Errorf("desktop.err.state_no_url")
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		st := readState(paths.State())
		if st.PID <= 0 || st.URL == "" {
			continue
		}
		res, err := client.Get(strings.TrimSuffix(st.URL, "/") + "/healthz")
		if err != nil {
			last = err
			continue
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			return st.URL, nil
		}
		last = fmt.Errorf("healthz: HTTP %d", res.StatusCode)
	}
	return "", last
}

// App は起動中のデスクトップ版（トレイから呼ぶ操作を持つ）。
type App struct {
	opts     *Options
	paths    Paths
	inst     *localserve.Instance
	port     int
	logger   *slog.Logger
	launcher string
	quit     chan struct{}
	quitOnce sync.Once
}

// URL は画面の URL（http://127.0.0.1:<port>/looptrack/）。
func (a *App) URL() string { return fmt.Sprintf("http://127.0.0.1:%d%s/", a.port, BasePath) }

// Version は版。
func (a *App) Version() string { return a.opts.Version }

// Lang は画面に出す文面の言語（トレイの項目もこれで作る）。
func (a *App) Lang() i18n.Lang { return a.opts.Lang }

// Paths はファイルの置き場。
func (a *App) Paths() Paths { return a.paths }

// Logf はログのファイルに残す（UI から）。
func (a *App) Logf(format string, args ...any) {
	a.logger.Warn(fmt.Sprintf(format, args...))
}

func (o *Options) primary(paths Paths, background, noTray bool) int {
	logger, closeLog, err := openLog(paths)
	if err != nil {
		return o.fail(i18n.T(o.Lang, "desktop.err.log_create"), err)
	}
	defer closeLog()
	st := readState(paths.State())
	ln, err := listen(o.Env, st.Port, logger, o.Lang)
	if err != nil {
		return o.fail(i18n.T(o.Lang, "desktop.err.listen"), err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	inst, err := localserve.Start(context.Background(), localserve.Options{
		DBPath: paths.DB(), Listener: ln, BasePath: BasePath, Logger: logger,
	})
	if err != nil {
		ln.Close()
		logger.Error("start", "err", err)
		return o.fail(i18n.T(o.Lang, "desktop.err.server_start", "log", paths.Log()), err)
	}
	// シグナル（looptrack desktop --quit の SIGTERM を含む）は desktop.json に pid を書く前から受ける
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	quitReq := make(chan struct{}, 1)
	// Windows は GUI のプロセスにシグナルを送れないので、終了を頼む印を見張る（トレイの有無に依らない）。
	// 非 Windows では何もしない。
	stopWatch := watchQuit(func() {
		select {
		case quitReq <- struct{}{}:
		default:
		}
	})
	defer stopWatch()
	launcher, _ := Launcher(o.Env)
	a := &App{opts: o, paths: paths, inst: inst, port: port, logger: logger, launcher: launcher, quit: make(chan struct{})}
	if err := writeState(paths.State(), state{
		PID: os.Getpid(), Port: port, URL: a.URL(), Version: o.Version, Started: time.Now().Format(time.RFC3339),
	}); err != nil {
		logger.Warn(i18n.T(o.Lang, "desktop.log.state_write_failed"), "err", err)
	}
	logger.Info("desktop", "action", "start", "url", a.URL(), "version", o.Version, "data", paths.DataDir, "launcher", launcher)
	fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.started", "app", AppName, "url", a.URL(), "data", paths.DataDir, "log", paths.Log()))
	a.refresh()
	if !background && !o.Open(a.URL()) {
		fmt.Fprintln(o.Stdout, i18n.T(o.Lang, "desktop.msg.open_in_browser", "url", a.URL()))
	}

	go func() {
		select {
		case s := <-sig:
			logger.Info("desktop", "action", "signal", "signal", s.String())
			a.Quit()
		case <-quitReq:
			logger.Info("desktop", "action", "quit-request")
			a.Quit()
		case err := <-inst.Done():
			if err != nil {
				logger.Error("serve", "err", err)
				o.Alert(AppName, i18n.T(o.Lang, "desktop.alert.server_stopped", "reason", err, "log", paths.Log()), true)
			}
			a.Quit()
		case <-a.quit:
		}
	}()

	if o.UI != nil && !noTray {
		o.UI.Run(a) // Quit（トレイの「終了」・シグナル）で戻る
		a.Quit()    // OS の都合（ログアウト等）でトレイが先に終わったときも止める
	}
	<-a.quit
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := inst.Shutdown(ctx); err != nil {
		logger.Warn("shutdown", "err", err)
	}
	// 次の起動で同じポートを使うため、ポートだけ残す
	if err := writeState(paths.State(), state{Port: port}); err != nil {
		logger.Warn(i18n.T(o.Lang, "desktop.log.state_write_failed"), "err", err)
	}
	logger.Info("desktop", "action", "stop")
	return 0
}

// Quit はサーバを止めて終わる（トレイの「終了」・シグナル）。何度呼んでもよい。
func (a *App) Quit() {
	a.quitOnce.Do(func() { close(a.quit) })
}

// Done は Quit で閉じる（UI はこれを見て終わる）。
func (a *App) Done() <-chan struct{} { return a.quit }

// listen は 127.0.0.1 の待ち受けを開く。LOOPTRACK_DESKTOP_PORT（0 は OS に選ばせる）→ 前回のポート → DefaultPort の順。
// 環境変数で決めたポートが使えなければエラー、それ以外は OS に選ばせて続ける（ログに残す。AI の接続設定の URL が変わる）。
func listen(e env.Env, last int, logger *slog.Logger, lang i18n.Lang) (net.Listener, error) {
	if v := e.Get("LOOPTRACK_DESKTOP_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 0 || p > 65535 {
			return nil, i18n.Errorf("desktop.err.bad_port", "value", v)
		}
		return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
	}
	want := DefaultPort
	if last > 0 && last <= 65535 {
		want = last
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(want)))
	if err == nil {
		return ln, nil
	}
	logger.Warn(i18n.T(lang, "desktop.log.port_in_use"), "port", want, "err", err)
	return net.Listen("tcp", "127.0.0.1:0")
}

// openLog はログのファイルを開く（5MB を超えていたら looptrack.log.1 に回す）。
func openLog(paths Paths) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(paths.LogDir, 0o700); err != nil {
		return nil, nil, err
	}
	p := paths.Log()
	if fi, err := os.Stat(p); err == nil && fi.Size() > 5<<20 {
		os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return slog.New(slog.NewJSONHandler(f, nil)), func() { f.Close() }, nil
}

// Launcher はこのアプリを外から起動するときのパス（自動起動・CLI のリンクの先）。AppImage の中で動いているときは $APPIMAGE
// （中の実行ファイルのパスは起動ごとに変わるマウント先なので使えない）。
func Launcher(e env.Env) (string, error) {
	if p := e.Get("APPIMAGE"); p != "" && runtime.GOOS == "linux" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}

// refresh は起動のたびに、自動起動の登録と CLI の置き場を今のアプリの場所に合わせる（.app・AppImage を動かした・更新した）。
func (a *App) refresh() {
	if as := a.autostart(); as != nil {
		if on, _ := as.Enabled(); on && !as.Current() {
			if err := as.Enable(); err != nil {
				a.logger.Warn(i18n.T(a.opts.Lang, "desktop.log.autostart_refresh_failed"), "err", err)
			} else {
				a.logger.Info("desktop", "action", "autostart-refresh")
			}
		}
	}
	if fixed, err := a.cli().Refresh(); err != nil {
		a.logger.Warn(i18n.T(a.opts.Lang, "desktop.log.cli_refresh_failed"), "err", err)
	} else if fixed {
		a.logger.Info("desktop", "action", "cli-refresh", "target", a.cli().Target())
	}
}

// autostartFor は launcher をログイン時に起動する登録（launcher が空なら nil）。
func (o *Options) autostartFor(launcher string) *Autostart {
	if launcher == "" {
		return nil
	}
	return &Autostart{
		GOOS: o.GOOS, Home: o.Home, ConfigHome: o.Env.Get("XDG_CONFIG_HOME"), Label: BundleID,
		Argv: []string{launcher, "desktop", "--background"}, Lang: o.Lang,
	}
}

// cliFor は launcher（アプリの実行ファイル）に対する CLI の置き場。
func (o *Options) cliFor(launcher string) CLIInstall {
	c := CLIInstall{
		GOOS: o.GOOS, Home: o.Home, LocalAppData: o.Env.Get("LOCALAPPDATA"),
		Launcher: launcher, PathEnv: o.Env.Get("PATH"), Lang: o.Lang,
	}
	if launcher != "" {
		c.BundledCLI = filepath.Join(filepath.Dir(launcher), "cli", "looptrack.exe")
	}
	return c
}

func (a *App) autostart() *Autostart { return a.opts.autostartFor(a.launcher) }

func (a *App) cli() CLIInstall { return a.opts.cliFor(a.launcher) }

// OpenUI は画面をブラウザで開く。
func (a *App) OpenUI() { a.OpenPath("/") }

// SettingsPath は「設定」（OpenSettings）が開く画面の中の経路。ローカルモードは常に最初の管理者として
// 自動ログインするので、管理者権限を要る /admin/* ではなく誰でも開ける /account を選ぶ
// （画面自身の利用者メニューでも「アカウント設定」として案内されている経路。internal/server/templates/layout.html）。
const SettingsPath = "/account"

// OpenSettings はアカウント設定の画面（SettingsPath）をブラウザで開く（トレイの「設定」）。
func (a *App) OpenSettings() { a.OpenPath(SettingsPath) }

// OpenPath は画面の中の経路（BasePath より後）をブラウザで開く。
func (a *App) OpenPath(p string) {
	u := fmt.Sprintf("http://127.0.0.1:%d%s%s", a.port, BasePath, p)
	if !a.opts.Open(u) {
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.open_browser_failed", "url", u), false)
	}
}

// errNotSetUp は初回設定がまだ（管理者が 0 人）。
var errNotSetUp = i18n.Errorf("desktop.err.not_set_up")

// MCPLabels はメニューに並べる接続設定の名前（setupwiz.MCPConfigs の順。画面の /first-run/done と同じもの）。
func MCPLabels(lang i18n.Lang) []string {
	var out []string
	for _, c := range setupwiz.MCPConfigs(lang, "http://127.0.0.1", "") {
		out = append(out, i18n.T(lang, "desktop.menu.mcp_item", "client", c.Client, "where", c.Where))
	}
	return out
}

// MCPConfigs は AI の MCP の接続設定（URL はこのインスタンス、X-Looptrack-Project は最初のプロジェクト。画面の /first-run/done と同じ）。
func (a *App) MCPConfigs() ([]setupwiz.MCPConfig, error) {
	ctx := context.Background()
	u, err := store.FirstActiveAdmin(ctx, a.inst.DB)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNotSetUp
	}
	if err != nil {
		return nil, err
	}
	projects, _, err := store.MemberProjects(ctx, a.inst.DB, u.ID)
	if err != nil {
		return nil, err
	}
	slug := ""
	if len(projects) > 0 {
		slug = projects[0].Slug
	}
	return setupwiz.MCPConfigs(a.opts.Lang, fmt.Sprintf("http://127.0.0.1:%d%s", a.port, BasePath), slug), nil
}

// CopyMCP は i 番目の接続設定をクリップボードに写す。
func (a *App) CopyMCP(i int) {
	cfgs, err := a.MCPConfigs()
	if err == nil && (i < 0 || i >= len(cfgs)) {
		err = i18n.Errorf("desktop.err.no_such_mcp", "index", i)
	}
	if err == nil {
		err = clipboard.WriteAll(cfgs[i].Text)
	}
	if err != nil {
		a.logger.Warn("copy-mcp", "err", err)
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.copy_mcp_failed", "reason", err), true)
		return
	}
	a.logger.Info("desktop", "action", "copy-mcp", "client", cfgs[i].Client)
	msg := i18n.T(a.opts.Lang, "desktop.alert.copy_mcp_done", "client", cfgs[i].Client, "where", cfgs[i].Where)
	if cfgs[i].Note != "" {
		msg += "\n" + cfgs[i].Note
	}
	a.opts.Alert(AppName, msg, false)
}

// InstallCLI は「CLI を使えるようにする」。
func (a *App) InstallCLI() {
	msg, err := a.cli().Install(a.opts.Lang)
	if err != nil {
		a.logger.Warn("install-cli", "err", err)
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.cli_install_failed", "reason", err), true)
		return
	}
	a.logger.Info("desktop", "action", "install-cli", "target", a.cli().Target())
	a.opts.Alert(AppName, msg, false)
}

// AutostartEnabled はログイン時の自動起動が登録されているか。
func (a *App) AutostartEnabled() bool {
	as := a.autostart()
	if as == nil {
		return false
	}
	on, _ := as.Enabled()
	return on
}

// SetAutostart はログイン時の自動起動を登録する / 消す。登録の後の状態を返す。
func (a *App) SetAutostart(on bool) bool {
	as := a.autostart()
	if as == nil {
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.autostart_no_app_path"), true)
		return false
	}
	var err error
	if on {
		err = as.Enable()
	} else {
		err = as.Disable()
	}
	if err != nil {
		a.logger.Warn("autostart", "on", on, "err", err)
		a.opts.Alert(AppName, i18n.T(a.opts.Lang, "desktop.alert.autostart_failed", "reason", err), true)
	}
	a.logger.Info("desktop", "action", "autostart", "on", on)
	return a.AutostartEnabled()
}
