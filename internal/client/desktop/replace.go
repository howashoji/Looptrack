package desktop

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// 新しい版への置き換え（DESIGN.md §5-4「置き換え」）。macOS の .app・Linux の AppImage・Windows の Looptrack.exe を置き換える。
// どれも次の順で進め、途中で失敗したら今の版に手を付けない（手を付けた分は戻す）。
//
//  1. 取得: 確認の結果（updatecheck.Result）の資産を取り、**署名された SHA256SUMS の SHA-256** と比べる
//     （署名を確かめていない結果・資産やハッシュの無い結果は置き換えない。リリースのページを開く）
//  2. 確認: macOS は dmg を spctl（open）→ 中の .app を今の .app と同じディレクトリに写して codesign --verify・
//     spctl（execute）・Bundle ID・TeamIdentifier（今の .app に Team があれば同じこと）。Linux は ELF の頭。
//     Windows は MZ の頭（zip は中の Looptrack.exe）
//  3. 置き換え: 今のもの（<名前>.app・AppImage）を <名前>.prev に改名し、新しいものを元の名前に改名する
//     （同じディレクトリの中の rename。前の .prev は消す＝前の版は 1 つだけ残す）。2 つ目の改名に失敗したら .prev を戻す。
//     Windows の zip 版は中の 4 ファイルを 1 つずつ同じように改名し、途中で失敗したら済んだ分を逆順に戻す。
//     Windows のインストーラ版はここでは何も改名しない（setup.exe が上書きする）
//  4. 起動し直し: 新しいものを desktop --after-update で起こし（前のインスタンスが止まるのを待ってから起動する）、今のものは終わる。
//     起こせなければ .prev を戻して今の版のまま動き続ける。Windows のインストーラ版は setup.exe を無人で起こし、
//     起動し直しは Looptrack.iss の DeinitializeSetup が受け持つ（上書きに失敗して Inno が戻したときも前の版を起こす）
//
// 置き場に書けない（/Applications に書く権限が無いなど）ときは置き換えず、macOS は確かめた dmg を開き（利用者がドラッグで入れる）、
// Linux と Windows の zip 版はリリースのページを開く。外部のコマンド（codesign・spctl・hdiutil・ditto・open）と起動し直しは差し替えられる（テスト）。

// maxDownload は取得物の上限（リリースの一覧に大きさが無いとき）。dmg・AppImage はどちらもこれより十分小さい。
const maxDownload = 1 << 30

// afterUpdateWait は --after-update で起動したときに、前のインスタンスが止まる（ロックが外れる）のを待つ時間
// （前のインスタンスはサーバを最大 15 秒かけて止める）。
const afterUpdateWait = 60 * time.Second

// runFunc は外部のコマンドを実行し、標準出力と標準エラーをまとめて返す。
type runFunc func(ctx context.Context, name string, args ...string) (string, error)

// startFunc は外部のコマンドを起こして待たない（起動し直し。今のインスタンスが終わっても動き続ける）。
type startFunc func(name string, args ...string) error

// replacer は置き換えの部品（App.replacer が組む。テストは直に組む）。
type replacer struct {
	GOOS     string
	Launcher string // Launcher() の値（macOS は .app の中の実行ファイル。Windows は Looptrack.exe）
	AppImage string // $APPIMAGE（Linux）
	WorkDir  string // 取得物（dmg・setup.exe）とマウント先の置き場（データの置き場の updates）
	BundleID string
	Client   *http.Client
	Run      runFunc
	Start    startFunc
	// Swap は Windows の zip 版で 1 ファイルずつ改名する（nil は swap。テストが途中の失敗を起こすために差し替える）
	Swap func(cur, next, prev string) error
}

// windowsAppExe は Windows のアプリの実行ファイルの名前（zip の Looptrack\Looptrack.exe・インストーラの {app}\Looptrack.exe）。
const windowsAppExe = AppName + ".exe"

// uninstallerName は Inno Setup がインストール先に置くアンインストーラ。これが実行ファイルの隣にあれば、インストーラで入れたものとみなす。
const uninstallerName = "unins000.exe"

// installerArgs は無人の上書きで setup.exe に渡す引数（/LOG は別に足す）。/TASKS は付けない（付けなければ Inno は前回の選択肢を
// 引き継ぐ。付けると「ログイン時に起動する」「CLI を使えるようにする」を外した人にも入れてしまう）。/RELAUNCH=1 は
// Looptrack.iss の DeinitializeSetup が読み、上書きが成功しても失敗しても（Inno が戻したときも）アプリを起動し直す。
// release.yml の smoke も同じ並びで上書きする（TestInstallerScriptSettings が両方を同じ読み方で確かめる）。
var installerArgs = [...]string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-", "/RELAUNCH=1"}

// installerLog は setup.exe の /LOG の置き場（データの置き場の updates の中）。上書きに失敗した理由はここに残る。
const installerLog = "setup.log"

// zipTop は Windows の zip の中の最上位のフォルダ（deploy/release/desktop.sh の windows-zip）。
const zipTop = AppName

// zipFiles は Windows の zip から取り出して置き換えるファイル（zipTop からの相対。置き換える順）。
// 1 つ目は今の実行ファイルそのもの。
var zipFiles = [...]string{windowsAppExe, "cli/looptrack.exe", "NOTICE", "OFL-BIZUDGothic.txt"}

// maxZipEntry は zip から取り出す 1 ファイルの大きさの上限（selfupdate の maxBinaryBytes と同じ値。テストが小さくする）。
var maxZipEntry int64 = 512 << 20

// installedByInstaller は launcher（Looptrack.exe）の隣に unins000.exe（ふつうのファイル）があるか。
// あればインストーラで入れたもの（setup.exe で上書きする）、無ければ zip を展開したもの（zip の中身で置き換える）。
func installedByInstaller(launcher string) bool {
	if launcher == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(filepath.Dir(launcher), uninstallerName))
	return err == nil && fi.Mode().IsRegular()
}

// desktopAssetFor は確認で照らす資産の名前（Checker.Asset）。Windows でインストーラで入れたものは setup.exe、
// それ以外（zip を展開したものを含む）は updatecheck.DesktopAsset。
func desktopAssetFor(goos, arch, launcher string) func(string) string {
	if goos == "windows" && installedByInstaller(launcher) {
		return updatecheck.DesktopInstallerAsset(arch)
	}
	return updatecheck.DesktopAsset(goos, arch)
}

// notWritableError は置き場に書けない（置き換えずに、dmg かリリースのページを開く）。
type notWritableError struct {
	Dir string // 書けなかったディレクトリ
	DMG string // 確かめた dmg（macOS。開けば利用者がドラッグで入れられる）
	err error
}

func (e *notWritableError) Error() string { return e.err.Error() }
func (e *notWritableError) Unwrap() error { return e.err }

func notWritable(dir, dmg string, err error) error {
	return &notWritableError{Dir: dir, DMG: dmg, err: i18n.Wrapf(err, "desktop.update.err.not_writable", "dir", dir)}
}

// target は置き換えるもの（macOS は .app、Linux は AppImage のファイル）。置き換えられない環境なら理由の error。
func (r *replacer) target() (string, error) {
	switch r.GOOS {
	case "darwin":
		exe := r.Launcher
		macos := filepath.Dir(exe)
		contents := filepath.Dir(macos)
		app := filepath.Dir(contents)
		if exe == "" || filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(app, ".app") {
			return "", i18n.Errorf("desktop.update.err.not_app", "path", exe)
		}
		return app, nil
	case "linux":
		p := r.AppImage
		if p == "" || !filepath.IsAbs(p) {
			return "", i18n.Errorf("desktop.update.err.not_appimage")
		}
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return "", i18n.Errorf("desktop.update.err.not_appimage")
		}
		return p, nil
	case "windows":
		exe := r.Launcher
		if exe == "" || !filepath.IsAbs(exe) || !strings.EqualFold(filepath.Base(exe), windowsAppExe) {
			return "", i18n.Errorf("desktop.update.err.not_windows_app", "path", exe)
		}
		if fi, err := os.Stat(exe); err != nil || !fi.Mode().IsRegular() {
			return "", i18n.Errorf("desktop.update.err.not_windows_app", "path", exe)
		}
		// 名前だけでは決めない。同梱の CLI（<アプリ>\cli\looptrack.exe）とその写し（%LOCALAPPDATA%\Programs\looptrack\looptrack.exe）も
		// 大小を無視すれば同じ名前で、そこから desktop を起動したときに CLI のフォルダを zip の中身で置き換えてしまう。
		// zip もインストーラ（Looptrack.iss の [Files]）も、アプリの隣に cli\looptrack.exe を置くので、その配置で確かめる
		if fi, err := os.Stat(filepath.Join(filepath.Dir(exe), "cli", "looptrack.exe")); err != nil || !fi.Mode().IsRegular() {
			return "", i18n.Errorf("desktop.update.err.not_windows_app", "path", exe)
		}
		return exe, nil
	}
	return "", i18n.Errorf("desktop.update.err.unsupported_os", "os", r.GOOS)
}

// fits は資産の名前が今の入れ方に合うか（Windows だけ。インストーラで入れたものは _setup.exe、zip を展開したものは .zip）。
// 控えの last_ok が前の入れ方（入れ直す前・この版より前の確認）の資産を持っているときに、取り違えて置き換えないため。
func (r *replacer) fits(cur string, a *updatecheck.Asset) error {
	if r.GOOS != "windows" || a == nil {
		return nil
	}
	if installedByInstaller(cur) {
		if !strings.HasSuffix(a.Name, "_setup.exe") {
			return i18n.Errorf("desktop.update.err.asset_not_installer", "name", a.Name)
		}
		return nil
	}
	if !strings.HasSuffix(a.Name, ".zip") {
		return i18n.Errorf("desktop.update.err.asset_not_zip", "name", a.Name)
	}
	return nil
}

// ready は src でこの環境のものを置き換えられるか（check・target・fits）。置き換えるもののパスを返す。
func (r *replacer) ready(src *updatecheck.Result) (string, error) {
	if err := check(src); err != nil {
		return "", err
	}
	cur, err := r.target()
	if err != nil {
		return "", err
	}
	if err := r.fits(cur, src.Asset); err != nil {
		return "", err
	}
	return cur, nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// check は src が置き換えに使える確認の結果か（署名を確かめ、資産の URL と署名された SHA-256 を持つ）。
func check(src *updatecheck.Result) error {
	if src == nil || !src.Available() || src.Latest == nil {
		return i18n.Errorf("desktop.update.err.no_update")
	}
	a := src.Asset
	if !src.Signed || a == nil || !sha256Hex.MatchString(a.SHA256) {
		return i18n.Errorf("desktop.update.err.unsigned", "version", src.Latest.Tag)
	}
	if !strings.HasPrefix(a.URL, "https://") || a.Name == "" || filepath.Base(a.Name) != a.Name || strings.ContainsAny(a.Name, `/\`) {
		return i18n.Errorf("desktop.update.err.bad_asset", "name", a.Name, "url", a.URL)
	}
	return nil
}

// move は改名で置き換えた 1 つ（prev が空なら、元の名前には何も無かった。戻すときは新しいものを消すだけ）。
// madeDir は置くために作ったディレクトリ（戻すときに空なら消す。作っていなければ空）。
type move struct{ cur, prev, madeDir string }

// applied は置き換え済み（起動し直す前）の状態。
type applied struct {
	cur, prev string // 主なもの（ログと知らせに出す。Windows のインストーラ版は prev が空）
	moves     []move // 改名で置き換えたもの（置き換えた順。Windows のインストーラ版は空＝戻すものが無い）
	relaunch  func() error
	cleanup   func() // 起動し直した後に消すもの（macOS の dmg）
}

// undo は起動し直せなかったときに前の版を戻す（新しい版は消す）。置き換えた順の逆に戻し、
// 1 つ戻せなくても残りは戻す（誤りはまとめて返す）。
func (ap *applied) undo() error {
	var errs []error
	for i := len(ap.moves) - 1; i >= 0; i-- {
		if err := ap.moves[i].undo(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m move) undo() error {
	failed := m.cur + ".failed"
	if err := os.RemoveAll(failed); err != nil {
		return err
	}
	if err := os.Rename(m.cur, failed); err != nil {
		return err
	}
	if m.prev != "" {
		if err := os.Rename(m.prev, m.cur); err != nil {
			os.Rename(failed, m.cur)
			return err
		}
	}
	if err := os.RemoveAll(failed); err != nil {
		return err
	}
	if m.madeDir != "" {
		os.Remove(m.madeDir) // 空のときだけ消える（ほかのものが置かれていれば残す）
	}
	return nil
}

// apply は取得・確認・置き換えまでを行う（起動し直すのは呼ぶ側が applied.relaunch で）。
func (r *replacer) apply(ctx context.Context, src *updatecheck.Result) (*applied, error) {
	cur, err := r.ready(src)
	if err != nil {
		return nil, err
	}
	switch r.GOOS {
	case "darwin":
		return r.applyMac(ctx, src.Asset, cur)
	case "windows":
		if installedByInstaller(cur) {
			return r.applyInstaller(ctx, src.Asset, cur)
		}
		return r.applyZip(ctx, src.Asset, cur)
	}
	return r.applyAppImage(ctx, src.Asset, cur)
}

// swap は cur を prev に改名し、next を cur に改名する（前の prev は消す）。2 つ目に失敗したら prev を cur に戻す。
func swap(cur, next, prev string) error {
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	if err := os.Rename(cur, prev); err != nil {
		return err
	}
	if err := os.Rename(next, cur); err != nil {
		if rerr := os.Rename(prev, cur); rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	}
	return nil
}

// applyAppImage は Linux: AppImage と同じディレクトリに取得し、確かめてから改名で置き換える。
func (r *replacer) applyAppImage(ctx context.Context, a *updatecheck.Asset, cur string) (*applied, error) {
	dir := filepath.Dir(cur)
	f, err := os.CreateTemp(dir, "."+filepath.Base(cur)+".update-")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, notWritable(dir, "", err)
		}
		return nil, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	err = r.download(ctx, a, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err := isELF(tmp); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return nil, err
	}
	prev := cur + ".prev"
	if err := swap(cur, tmp, prev); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, notWritable(dir, "", err)
		}
		return nil, i18n.Wrapf(err, "desktop.update.err.replace", "path", cur)
	}
	ok = true
	return &applied{cur: cur, prev: prev, moves: []move{{cur: cur, prev: prev}}, cleanup: func() {},
		relaunch: func() error { return r.Start(cur, "desktop", "--after-update") }}, nil
}

// isELF は取得した AppImage が ELF の実行ファイルの形か（SHA-256 は既に合っている。取り違えた資産の名前に備える）。
func isELF(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("\x7fELF")) {
		return i18n.Errorf("desktop.update.err.not_elf")
	}
	return nil
}

// isPE は Windows の実行ファイルの形か（頭が MZ。SHA-256 は既に合っている。取り違えた資産や中身に備える）。
func isPE(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("MZ")) {
		return i18n.Errorf("desktop.update.err.not_pe", "path", p)
	}
	return nil
}

// applyInstaller は Windows のインストーラ版: setup.exe をデータの置き場の updates に取得して確かめる。何も改名しない
// （上書きは setup.exe が行う）ので、undo と cleanup は何もしない。起動し直し（relaunch）は setup.exe を無人で起こすことで、
// 前のインスタンスを止めるのは Looptrack.iss の PrepareToInstall（desktop --quit）と CloseApplications、
// 新しい版を起こすのは DeinitializeSetup（/RELAUNCH=1）。取得した setup.exe は動いている間は消せないので残し、次の取得の前に消す。
func (r *replacer) applyInstaller(ctx context.Context, a *updatecheck.Asset, cur string) (*applied, error) {
	if err := os.MkdirAll(r.WorkDir, 0o700); err != nil {
		return nil, err
	}
	if old, _ := filepath.Glob(filepath.Join(r.WorkDir, "*_setup.exe")); len(old) > 0 {
		for _, p := range old {
			os.Remove(p) // 前の置き換えで取得したもの（消せなければ残す。今回の名前なら下で上書きする）
		}
	}
	setup := filepath.Join(r.WorkDir, a.Name)
	f, err := os.CreateTemp(r.WorkDir, a.Name+".part-")
	if err != nil {
		return nil, err
	}
	err = r.download(ctx, a, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), setup)
	}
	if err != nil {
		os.Remove(f.Name())
		return nil, err
	}
	if err := isPE(setup); err != nil {
		os.Remove(setup)
		return nil, err
	}
	args := append(installerArgs[:], "/LOG="+filepath.Join(r.WorkDir, installerLog))
	return &applied{cur: cur, cleanup: func() {},
		relaunch: func() error { return r.Start(setup, args...) }}, nil
}

// applyZip は Windows の zip 版: 今の Looptrack.exe と同じフォルダの一時のディレクトリに zip を取得し、中の 4 ファイルを取り出して
// 確かめてから、1 ファイルずつ <名前>.prev に退けて置き換える。途中で失敗したら済んだ分を逆順に戻す。
func (r *replacer) applyZip(ctx context.Context, a *updatecheck.Asset, cur string) (*applied, error) {
	dir := filepath.Dir(cur)
	stage, err := os.MkdirTemp(dir, ".looptrack-update-")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, notWritable(dir, "", err)
		}
		return nil, err
	}
	defer os.RemoveAll(stage)
	zipPath := filepath.Join(stage, a.Name)
	f, err := os.Create(zipPath)
	if err != nil {
		return nil, err
	}
	err = r.download(ctx, a, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	files := filepath.Join(stage, "files")
	if err := extractAppZip(zipPath, a.Name, files); err != nil {
		return nil, err
	}
	if err := isPE(filepath.Join(files, windowsAppExe)); err != nil {
		return nil, err
	}
	sw := r.Swap
	if sw == nil {
		sw = swap
	}
	var moves []move
	for _, name := range zipFiles {
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if name == windowsAppExe {
			dst = cur // 名前の大小は今のものに合わせる
		}
		m, err := place(sw, dst, filepath.Join(files, filepath.FromSlash(name)))
		if err != nil {
			if uerr := (&applied{moves: moves}).undo(); uerr != nil {
				return nil, i18n.Wrapf(errors.Join(err, uerr), "desktop.update.err.replace", "path", dst)
			}
			if errors.Is(err, fs.ErrPermission) {
				return nil, notWritable(dir, "", err)
			}
			return nil, i18n.Wrapf(err, "desktop.update.err.replace", "path", dst)
		}
		moves = append(moves, m)
	}
	return &applied{cur: cur, prev: cur + ".prev", moves: moves, cleanup: func() {},
		relaunch: func() error { return r.Start(cur, "desktop", "--after-update") }}, nil
}

// place は next を dst に置く。dst があれば sw で dst.prev に退けてから置き、無ければ（利用者が消した NOTICE など）そのまま置く。
func place(sw func(cur, next, prev string) error, dst, next string) (move, error) {
	if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		made := ""
		if _, err := os.Lstat(filepath.Dir(dst)); errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(filepath.Dir(dst), 0o755); err != nil {
				return move{}, err
			}
			made = filepath.Dir(dst)
		}
		if err := os.Rename(next, dst); err != nil {
			if made != "" {
				os.Remove(made)
			}
			return move{}, err
		}
		return move{cur: dst, madeDir: made}, nil
	}
	prev := dst + ".prev"
	if err := sw(dst, next, prev); err != nil {
		return move{}, err
	}
	return move{cur: dst, prev: prev}, nil
}

// extractAppZip は Windows の zip（name は知らせに出す名前）から zipFiles を dst の下に取り出す。規則は selfupdate の extract と同じ:
//   - 名前に .. の区切りを含む項目があれば、zip ごと拒む（取り出す名前とは別でも。正しい zip には無い）
//   - 取り出すのは zipTop/<zipFiles の名前> と文字どおり同じ名前の、ふつうのファイルだけ（シンボリックリンクや 2 つ目は拒む。
//     ほかの名前は読まない）
//   - 大きさは maxZipEntry まで（項目に書かれた大きさと、実際に読んだ大きさの両方で確かめる）
//
// 1 つでも欠けていれば誤り。desktop は selfupdate を import しない（selfupdate も desktop を import しない）ので、ここに別に持つ。
func extractAppZip(zipPath, name, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
	}
	defer zr.Close()
	want := map[string]string{} // zip の中の名前 → zipFiles の名前
	for _, f := range zipFiles {
		want[zipTop+"/"+f] = f
	}
	found := map[string]bool{}
	for _, zf := range zr.File {
		if hasDotDot(zf.Name) {
			return i18n.Errorf("selfupdate.err.archive_entry", "name", name, "entry", zf.Name)
		}
		rel, ok := want[zf.Name]
		if !ok {
			continue
		}
		if found[rel] {
			return i18n.Errorf("selfupdate.err.archive_dup", "name", name, "entry", zf.Name)
		}
		if !zf.Mode().IsRegular() {
			return i18n.Errorf("selfupdate.err.archive_not_regular", "name", name, "entry", zf.Name)
		}
		if zf.UncompressedSize64 > uint64(maxZipEntry) {
			return i18n.Errorf("selfupdate.err.binary_too_large", "name", name, "entry", zf.Name, "limit", maxZipEntry)
		}
		if err := extractZipFile(zf, filepath.Join(dst, filepath.FromSlash(rel)), name); err != nil {
			return err
		}
		found[rel] = true
	}
	for _, f := range zipFiles {
		if !found[f] {
			return i18n.Errorf("selfupdate.err.archive_no_binary", "name", name, "entry", zipTop+"/"+f)
		}
	}
	return nil
}

// extractZipFile は zip の 1 項目を out に書く（maxZipEntry を超えたら誤り）。
func extractZipFile(zf *zip.File, out, name string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
	}
	defer rc.Close()
	w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	err = copyLimited(w, rc, name, zf.Name)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

// copyLimited は r を w に写し、maxZipEntry を超えたら誤りにする（項目に書かれた大きさの検査とは別に、実際に読んだ大きさで止める）。
func copyLimited(w io.Writer, r io.Reader, name, entry string) error {
	n, err := io.Copy(w, io.LimitReader(r, maxZipEntry+1))
	if err != nil {
		return i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
	}
	if n > maxZipEntry {
		return i18n.Errorf("selfupdate.err.binary_too_large", "name", name, "entry", entry, "limit", maxZipEntry)
	}
	return nil
}

// hasDotDot は名前の区切り（/ と \）で分けた中に .. があるか（selfupdate と同じ）。
func hasDotDot(name string) bool {
	for _, s := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s == ".." {
			return true
		}
	}
	return false
}

// applyMac は macOS: dmg を取得して確かめ、中の .app を今の .app と同じディレクトリに写して確かめてから改名で置き換える。
func (r *replacer) applyMac(ctx context.Context, a *updatecheck.Asset, cur string) (ap *applied, err error) {
	if err := os.MkdirAll(r.WorkDir, 0o700); err != nil {
		return nil, err
	}
	dmg := filepath.Join(r.WorkDir, a.Name)
	f, err := os.CreateTemp(r.WorkDir, a.Name+".part-")
	if err != nil {
		return nil, err
	}
	err = r.download(ctx, a, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), dmg)
	}
	if err != nil {
		os.Remove(f.Name())
		return nil, err
	}
	keepDMG := false
	defer func() {
		if !keepDMG {
			os.Remove(dmg)
		}
	}()
	if out, err := r.Run(ctx, "spctl", "--assess", "--type", "open", "--context", "context:primary-signature", "--verbose=2", dmg); err != nil {
		return nil, verifyErr("spctl", dmg, out, err)
	}
	mnt, err := os.MkdirTemp(r.WorkDir, "mnt-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(mnt)
	if out, err := r.Run(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg); err != nil {
		return nil, i18n.Errorf("desktop.update.err.attach", "path", dmg, "reason", strings.TrimSpace(out+" "+err.Error()))
	}
	defer r.Run(context.Background(), "hdiutil", "detach", mnt, "-force")
	newApp := filepath.Join(mnt, AppName+".app")
	if fi, err := os.Stat(newApp); err != nil || !fi.IsDir() {
		return nil, i18n.Errorf("desktop.update.err.no_app_in_dmg", "name", AppName+".app")
	}
	dir := filepath.Dir(cur)
	stage, err := os.MkdirTemp(dir, ".looptrack-update-")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			keepDMG = true
			return nil, notWritable(dir, dmg, err)
		}
		return nil, err
	}
	defer os.RemoveAll(stage)
	staged := filepath.Join(stage, filepath.Base(cur))
	if out, err := r.Run(ctx, "ditto", newApp, staged); err != nil {
		return nil, i18n.Errorf("desktop.update.err.copy", "path", staged, "reason", strings.TrimSpace(out+" "+err.Error()))
	}
	if out, err := r.Run(ctx, "codesign", "--verify", "--deep", "--strict", "--verbose=2", staged); err != nil {
		return nil, verifyErr("codesign", staged, out, err)
	}
	if out, err := r.Run(ctx, "spctl", "--assess", "--type", "execute", "--verbose=2", staged); err != nil {
		return nil, verifyErr("spctl", staged, out, err)
	}
	newID, newTeam, err := r.signInfo(ctx, staged)
	if err != nil {
		return nil, err
	}
	if newID != r.BundleID {
		return nil, i18n.Errorf("desktop.update.err.bundle_id", "got", newID, "want", r.BundleID)
	}
	if _, curTeam, err := r.signInfo(ctx, cur); err == nil && curTeam != "" && newTeam != curTeam {
		return nil, i18n.Errorf("desktop.update.err.team", "got", newTeam, "want", curTeam)
	}
	prev := cur + ".prev"
	if err := swap(cur, staged, prev); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			keepDMG = true
			return nil, notWritable(dir, dmg, err)
		}
		return nil, i18n.Wrapf(err, "desktop.update.err.replace", "path", cur)
	}
	keepDMG = true // 消すのは起動し直しを試した後（applied.cleanup）
	return &applied{cur: cur, prev: prev, moves: []move{{cur: cur, prev: prev}}, cleanup: func() { os.Remove(dmg) },
		relaunch: func() error {
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := r.Run(rctx, "open", "-n", cur, "--args", "desktop", "--after-update")
			if err != nil {
				return errors.New(strings.TrimSpace(out + " " + err.Error()))
			}
			return nil
		}}, nil
}

func verifyErr(cmd, path, out string, err error) error {
	return i18n.Errorf("desktop.update.err.verify", "cmd", cmd, "path", path, "reason", strings.TrimSpace(out+" "+err.Error()))
}

// signInfo は codesign -dv の Identifier と TeamIdentifier（"not set" は空）を読む。
func (r *replacer) signInfo(ctx context.Context, p string) (id, team string, err error) {
	out, err := r.Run(ctx, "codesign", "-dv", "--verbose=2", p)
	if err != nil {
		return "", "", verifyErr("codesign", p, out, err)
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "Identifier":
			id = v
		case "TeamIdentifier":
			if v != "not set" {
				team = v
			}
		}
	}
	return id, team, nil
}

// download は a を取得して w に書き、署名された SHA-256 と比べる（違えば誤り。書いたものは呼ぶ側が消す）。
func (r *replacer) download(ctx context.Context, a *updatecheck.Asset, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return i18n.Wrapf(err, "desktop.update.err.download", "url", a.URL)
	}
	res, err := r.client().Do(req)
	if err != nil {
		return i18n.Wrapf(err, "desktop.update.err.download", "url", a.URL)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return i18n.Errorf("desktop.update.err.download_http", "url", a.URL, "status", res.StatusCode)
	}
	limit := int64(maxDownload)
	if a.Size > 0 {
		limit = a.Size
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(res.Body, limit+1))
	if err != nil {
		return i18n.Wrapf(err, "desktop.update.err.download", "url", a.URL)
	}
	if n > limit {
		return i18n.Errorf("desktop.update.err.too_large", "url", a.URL, "limit", limit)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return i18n.Errorf("desktop.update.err.sha256", "name", a.Name, "got", got, "want", a.SHA256)
	}
	if f, ok := w.(*os.File); ok {
		return f.Sync()
	}
	return nil
}

func (r *replacer) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return i18n.Errorf("updatecheck.err.not_https", "url", req.URL.String())
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}}
}

// execRun は外部のコマンドを実行する（runFunc の既定）。
func execRun(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// startDetached は新しいセッションでコマンドを起こし、待たない（startFunc の既定。今のインスタンスが終わっても動き続ける）。
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
