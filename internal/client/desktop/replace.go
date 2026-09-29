package desktop

import (
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

// 新しい版への置き換え（DESIGN.md §5-4「置き換え」）。macOS の .app と Linux の AppImage だけを置き換える
// （Windows はコード署名が入るまで知らせにとどめる）。どちらも次の順で進め、途中で失敗したら今の版に手を付けない。
//
//  1. 取得: 確認の結果（updatecheck.Result）の資産を取り、**署名された SHA256SUMS の SHA-256** と比べる
//     （署名を確かめていない結果・資産やハッシュの無い結果は置き換えない。リリースのページを開く）
//  2. 確認: macOS は dmg を spctl（open）→ 中の .app を今の .app と同じディレクトリに写して codesign --verify・
//     spctl（execute）・Bundle ID・TeamIdentifier（今の .app に Team があれば同じこと）。Linux は ELF の頭
//  3. 置き換え: 今のもの（<名前>.app・AppImage）を <名前>.prev に改名し、新しいものを元の名前に改名する
//     （同じディレクトリの中の rename。前の .prev は消す＝前の版は 1 つだけ残す）。2 つ目の改名に失敗したら .prev を戻す
//  4. 起動し直し: 新しいものを desktop --after-update で起こし（前のインスタンスが止まるのを待ってから起動する）、今のものは終わる。
//     起こせなければ .prev を戻して今の版のまま動き続ける
//
// 置き場に書けない（/Applications に書く権限が無いなど）ときは置き換えず、macOS は確かめた dmg を開き（利用者がドラッグで入れる）、
// Linux はリリースのページを開く。外部のコマンド（codesign・spctl・hdiutil・ditto・open）と起動し直しは差し替えられる（テスト）。

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
	Launcher string // Launcher() の値（macOS は .app の中の実行ファイル）
	AppImage string // $APPIMAGE（Linux）
	WorkDir  string // 取得物（dmg）とマウント先の置き場（データの置き場の updates）
	BundleID string
	Client   *http.Client
	Run      runFunc
	Start    startFunc
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
	}
	return "", i18n.Errorf("desktop.update.err.unsupported_os", "os", r.GOOS)
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

// applied は置き換え済み（起動し直す前）の状態。
type applied struct {
	cur, prev string
	relaunch  func() error
	cleanup   func() // 起動し直した後に消すもの（macOS の dmg）
}

// undo は起動し直せなかったときに前の版を戻す（新しい版は消す）。
func (ap *applied) undo() error {
	failed := ap.cur + ".failed"
	if err := os.RemoveAll(failed); err != nil {
		return err
	}
	if err := os.Rename(ap.cur, failed); err != nil {
		return err
	}
	if err := os.Rename(ap.prev, ap.cur); err != nil {
		os.Rename(failed, ap.cur)
		return err
	}
	return os.RemoveAll(failed)
}

// apply は取得・確認・置き換えまでを行う（起動し直すのは呼ぶ側が applied.relaunch で）。
func (r *replacer) apply(ctx context.Context, src *updatecheck.Result) (*applied, error) {
	if err := check(src); err != nil {
		return nil, err
	}
	cur, err := r.target()
	if err != nil {
		return nil, err
	}
	if r.GOOS == "darwin" {
		return r.applyMac(ctx, src.Asset, cur)
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
	return &applied{cur: cur, prev: prev, cleanup: func() {},
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
	return &applied{cur: cur, prev: prev, cleanup: func() { os.Remove(dmg) },
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
