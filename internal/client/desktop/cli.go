package desktop

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// CLIInstall はメニューの「CLI を使えるようにする」（アプリの中の実体を、管理者権限の要らない置き場から呼べるようにする）。
//
//	macOS・Linux: ~/.local/bin/looptrack → アプリの中の実行ファイル（.app の Contents/MacOS/looptrack・AppImage は $APPIMAGE）への
//	              シンボリックリンク。アプリを更新しても（同じ場所に置き換えれば）リンクはそのまま使える
//	Windows     : %LOCALAPPDATA%\Programs\looptrack\looptrack.exe に、zip に同梱した CLI（cli\looptrack.exe。コンソールの実行ファイル）を
//	              **コピー**する。symlink は開発者モードか管理者が要るのでやめた。.cmd のシムは hook がシェルを通さずに
//	              呼ぶと動かず、Ctrl+C で「バッチ ジョブを終了しますか」が出る。デスクトップ版の Looptrack.exe は GUI の実行ファイル
//	              （-H windowsgui）で標準出力が端末に出ないので、CLI には使えない。コピーは起動のたびに同梱のものと比べて
//	              古ければ置き換える（印のファイル .looptrack-desktop があり、中身が置いたときのままのものだけ。setup で入れたもの・
//	              self-update で新しくしたものは触らない）
//
// 置き場に既に（setup・self-update で入れた）普通のファイルがあれば、置き換えない（そのまま使える。更新は looptrack self-update）。
// PATH は変えない（置き場が PATH に無ければ案内する。looptrack doctor と同じ）。
type CLIInstall struct {
	GOOS         string
	Home         string
	LocalAppData string    // Windows の %LOCALAPPDATA%
	Launcher     string    // macOS・Linux のリンクの先
	BundledCLI   string    // Windows の同梱の CLI（Looptrack.exe の隣の cli\looptrack.exe）
	PathEnv      string    // PATH（案内のため）
	Lang         i18n.Lang // 印のファイルの注釈の言語（空なら対訳表の正本の日本語）
}

// CLIState は置き場の状態。
type CLIState int

const (
	CLIMissing CLIState = iota // 無い
	CLIOurs                    // このアプリのもの（リンクの先・コピーの中身が今のもの）
	CLIStale                   // このアプリが作ったが古い（リンクの先が別の場所・コピーが古い）
	CLIOther                   // 別に入れたもの（setup・self-update の普通のファイル）
)

// markerName は Windows のコピーの印（このファイルがあるコピーだけを置き換える）。
const markerName = ".looptrack-desktop"

// Dir は置き場のディレクトリ。
func (c CLIInstall) Dir() string {
	if c.GOOS == "windows" {
		base := c.LocalAppData
		if base == "" {
			base = filepath.Join(c.Home, "AppData", "Local")
		}
		return filepath.Join(base, "Programs", "looptrack")
	}
	return filepath.Join(c.Home, ".local", "bin")
}

// Target は置き場の実行ファイル。
func (c CLIInstall) Target() string {
	if c.GOOS == "windows" {
		return filepath.Join(c.Dir(), "looptrack.exe")
	}
	return filepath.Join(c.Dir(), "looptrack")
}

// State は置き場の今の状態。
func (c CLIInstall) State() CLIState {
	t := c.Target()
	fi, err := os.Lstat(t)
	if err != nil {
		return CLIMissing
	}
	if c.GOOS == "windows" {
		mark, err := os.ReadFile(filepath.Join(c.Dir(), markerName))
		if err != nil {
			return CLIOther
		}
		// 置いた後に変わっていれば（looptrack self-update で新しくした）、このアプリのものとはみなさない（古い同梱のもので戻さない）
		if want := markedHash(mark); want != "" {
			if h, err := fileHash(t); err != nil || fmt.Sprintf("%x", h) != want {
				return CLIOther
			}
		}
		same, err := sameFile(c.BundledCLI, t)
		if err == nil && same {
			return CLIOurs
		}
		return CLIStale
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return CLIOther
	}
	dest, err := os.Readlink(t)
	if err != nil {
		return CLIStale
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(t), dest)
	}
	if filepath.Clean(dest) == filepath.Clean(c.Launcher) {
		return CLIOurs
	}
	if looksLikeApp(dest) {
		return CLIStale // 前の場所・前の版のアプリ（AppImage はファイル名に版が入る）
	}
	return CLIOther // 利用者が自分で作ったリンクは触らない
}

// looksLikeApp はリンクの先がデスクトップ版のアプリの中の実行ファイルか（.app の中・AppImage）。
func looksLikeApp(p string) bool {
	s := filepath.ToSlash(p)
	return strings.Contains(s, ".app/Contents/MacOS/") || strings.HasSuffix(strings.ToLower(s), ".appimage")
}

// Install は置き場に作る（古いものは直す。別に入れたものは触らない）。利用者に見せる結果の文を返す。
func (c CLIInstall) Install(lang i18n.Lang) (string, error) {
	switch c.State() {
	case CLIOther:
		return i18n.T(lang, "desktop.cli.already_other", "path", c.Target()) + c.pathHint(lang), nil
	case CLIOurs:
		return i18n.T(lang, "desktop.cli.already_ours", "path", c.Target()) + c.pathHint(lang), nil
	}
	if err := c.put(); err != nil {
		return "", err
	}
	return i18n.T(lang, "desktop.cli.installed", "path", c.Target()) + c.pathHint(lang), nil
}

// Refresh は起動のたびに呼ぶ: このアプリが作ったものが古ければ直す（別に入れたもの・無いものは触らない）。直したら true。
func (c CLIInstall) Refresh() (bool, error) {
	if c.State() != CLIStale {
		return false, nil
	}
	return true, c.put()
}

// Uninstall は**このアプリが置いた** CLI（Windows のコピー・macOS / Linux のリンク）を消す（アンインストールで使う）。
// 別に入れたもの（setup・self-update）・利用者が自分で作ったリンクは残す。消したら true。
func (c CLIInstall) Uninstall() (bool, error) {
	switch c.State() {
	case CLIOurs:
		// このアプリのもの
	case CLIStale:
		// Windows は印のファイルで「このアプリが置いたもの」と分かる（同梱のものより古いだけ）。
		// macOS・Linux の CLIStale は**別の場所**のアプリへのリンクなので、そちらのインストールのものとみなして残す
		if c.GOOS != "windows" {
			return false, nil
		}
	default:
		return false, nil
	}
	if err := os.Remove(c.Target()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if c.GOOS == "windows" {
		os.Remove(filepath.Join(c.Dir(), markerName))
		os.Remove(c.Target() + ".old") // 使用中で残っていたもの
	}
	os.Remove(c.Dir()) // 空のときだけ消える（他のものが入っていれば残る）
	return true, nil
}

func (c CLIInstall) put() error {
	if err := os.MkdirAll(c.Dir(), 0o755); err != nil {
		return err
	}
	if c.GOOS == "windows" {
		// Windows のパスは大小を区別しないので、アプリを CLI の置き場（…\Programs\looptrack）に展開していると、
		// looptrack.exe のコピーが Looptrack.exe（アプリ本体）を上書きしてしまう
		if c.Launcher != "" && strings.EqualFold(filepath.Clean(filepath.Dir(c.Launcher)), filepath.Clean(c.Dir())) {
			return i18n.Errorf("desktop.cli.err.app_in_target_dir", "dir", c.Dir())
		}
		return c.copyWindows()
	}
	if c.Launcher == "" {
		return i18n.Errorf("desktop.cli.err.no_launcher")
	}
	tmp := c.Target() + ".tmp-link"
	os.Remove(tmp)
	if err := os.Symlink(c.Launcher, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, c.Target()) // 既存のリンクを原子的に置き換える
}

// copyWindows は同梱の CLI を置き場にコピーする。実行中の looptrack.exe は上書きできないので、.old に改名してから置く
// （self-update と同じやり方。.old は次の置き換えで消す）。
func (c CLIInstall) copyWindows() error {
	if c.BundledCLI == "" {
		return i18n.Errorf("desktop.cli.err.no_bundled")
	}
	src, err := os.Open(c.BundledCLI)
	if err != nil {
		return i18n.Wrapf(err, "desktop.cli.err.open_bundled", "path", c.BundledCLI)
	}
	defer src.Close()
	t := c.Target()
	tmp := t + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	old := t + ".old"
	os.Remove(old)
	if _, err := os.Stat(t); err == nil {
		if err := os.Rename(t, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, t); err != nil {
		os.Rename(old, t)
		return err
	}
	os.Remove(old) // 使用中なら残る（次回に消す）
	h, err := fileHash(t)
	if err != nil {
		return err
	}
	// 1 行目（sha256 <16 進>）だけを markedHash が読む。2 行目は人が読む注釈
	mark := fmt.Sprintf("sha256 %x\r\n# %s\r\n", h, i18n.T(c.Lang, "desktop.cli.marker_comment"))
	return os.WriteFile(filepath.Join(c.Dir(), markerName), []byte(mark), 0o644)
}

// markedHash は印のファイルに書いた SHA-256（16 進）。
func markedHash(mark []byte) string {
	for _, line := range strings.Split(string(mark), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "sha256 "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// pathHint は置き場が PATH に無いときの案内。
func (c CLIInstall) pathHint(lang i18n.Lang) string {
	sep := string(os.PathListSeparator)
	if c.GOOS == "windows" {
		sep = ";"
	} else if c.GOOS != "" {
		sep = ":"
	}
	dir := filepath.Clean(c.Dir())
	for _, p := range strings.Split(c.PathEnv, sep) {
		if p != "" && strings.EqualFold(filepath.Clean(p), dir) {
			return ""
		}
	}
	if c.GOOS == "windows" {
		return "\n\n" + i18n.T(lang, "desktop.cli.path_hint_windows", "dir", dir)
	}
	return "\n\n" + i18n.T(lang, "desktop.cli.path_hint_unix", "dir", dir)
}

// sameFile は 2 つのファイルの中身が同じか（大きさ → SHA-256）。
func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if fa.Size() != fb.Size() {
		return false, nil
	}
	ha, err := fileHash(a)
	if err != nil {
		return false, err
	}
	hb, err := fileHash(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ha, hb), nil
}

func fileHash(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
