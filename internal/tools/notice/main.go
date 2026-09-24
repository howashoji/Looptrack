// notice は配布物に添える第三者のライセンス文（リポジトリのルートの NOTICE）を作る。
//
//	go run ./internal/tools/notice          # NOTICE を作り直す
//	go run ./internal/tools/notice -check   # 作り直した内容と NOTICE が違えば失敗する（書き換えない）
//
// 集めるもの:
//   - looptrack（./cmd/looptrack）に実際にリンクされるモジュール。リリースで作る形（deploy/release/dist.sh と desktop.sh）
//     ごとに `go list -deps -json` を回し、その和をとる（標準ライブラリと本体のモジュールは除く）:
//     headless は linux・darwin・windows × amd64・arm64（CGO_ENABLED=0）、desktop（-tags desktop）は同じ 6 対象で
//     darwin だけ CGO_ENABLED=1（fyne.io/systray の Cocoa）。
//   - 各モジュールの Module.Dir の直下の LICENSE・LICENCE・COPYING・NOTICE（大文字小文字・拡張子・LICENSE-MIT のような接尾辞の違いを許す）。
//     ライセンス文が 1 つも無いモジュールがあれば失敗する（手で確かめて NOTICE に足す方法は作らない。依存を見直す）。
//   - 埋め込んだフォント BIZ UDGothic の SIL OFL 1.1（internal/client/report/pdf/fonts/OFL.txt）と、
//     実行ファイルに入る Go の標準ライブラリ・ランタイムのライセンス（$GOROOT/LICENSE。版は書かない＝パッチ版の違いで差分を出さない）。
//   - Linux の AppImage の先頭に付く type2-runtime の MIT（deploy/release/licenses/AppImage-type2-runtime-LICENSE.txt。
//     版は deploy/release/desktop.sh の APPIMAGE_RUNTIME_TAG から読む＝同梱する runtime と版がずれない）。
//   - その runtime に静的リンクされた部品（libfuse・musl libc・squashfuse・zstd・zlib・mimalloc）のライセンス文。
//     一覧の正本は deploy/release/licenses/runtime-components.json（版・ライセンス・ソースの URL と SHA-256・全文の写しの
//     ファイル名とその SHA-256）。マニフェストの runtime.tag が desktop.sh の APPIMAGE_RUNTIME_TAG と違えば失敗する
//     （runtime を上げたのに部品の一覧を取り直し忘れた状態で NOTICE を作らない）。写しの SHA-256 も確かめる。
//     libfuse 3.15.0 は LGPL-2.1 なので、全文（LGPL-2.1.txt）・改変の所在・対応ソースの置き場・作り直しの手順
//     （deploy/release/licenses/RELINKING.md）を節に書く。同じものは AppImage の usr/share/doc/looptrack/licenses/ にも入る。
//
// 各モジュールの節には入手先の URL を書く（MPL-2.0 の「実行ファイルで配るときに受け取った人へソースの入手先を知らせる」義務に応える。
// リポジトリの URL＝module path から /v2 のような major 版の接尾辞を外したもの、その版のソース＝module proxy の zip。
// module path の大文字は `!小文字` にエスケープする）。MPL-2.0 のモジュールには、その節に短い案内を添える。
//
// 出力はモジュールのパスで並べ、日付など実行ごとに変わるものを入れない（決定的）。依存を足したら作り直してコミットする
// （CI の notice ジョブが「作り直して git diff が無いこと」を確かめる。手順は docs/server/RELEASE.md「NOTICE」）。
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// target はリリースで作る形 1 つ（go list に渡す環境とタグ）。
type target struct {
	goos, goarch, tags string
	cgo                bool
}

// targets はリリースで作る形の一覧（dist.sh の 6 対象と desktop.sh の .app・AppImage・Windows の zip）。
func targets() []target {
	var ts []target
	for _, tags := range []string{"", "desktop"} {
		for _, goos := range []string{"linux", "darwin", "windows"} {
			for _, goarch := range []string{"amd64", "arm64"} {
				ts = append(ts, target{goos: goos, goarch: goarch, tags: tags, cgo: tags == "desktop" && goos == "darwin"})
			}
		}
	}
	return ts
}

// module は go list -json の Module の要るところ。
type module struct {
	Path    string
	Version string
	Dir     string
	Main    bool
	Replace *module
}

type pkg struct {
	ImportPath string
	Standard   bool
	Module     *module
}

// licenseFile はライセンス文のファイル 1 つ。
type licenseFile struct {
	name string
	text string
}

// dep は NOTICE に載せるモジュール 1 つ。
type dep struct {
	path, version string
	files         []licenseFile
	mpl           bool // MPL-2.0（ソースの入手先を知らせる義務がある）
}

const (
	pkgPath   = "./cmd/looptrack"
	fontOFL   = "internal/client/report/pdf/fonts/OFL.txt"
	outName   = "NOTICE"
	ruleHeavy = "================================================================================"
	ruleLight = "--------------------------------------------------------------------------------"
	// AppImage の runtime（desktop.sh が AppImage の先頭に付ける。MIT）
	appImageScript  = "deploy/release/desktop.sh"
	licensesDir     = "deploy/release/licenses"
	appImageLicense = licensesDir + "/AppImage-type2-runtime-LICENSE.txt"
	appImageRepo    = "https://github.com/AppImage/type2-runtime"
	// runtime に静的リンクされた部品の一覧（版・ライセンス・ソース・全文の写し）
	appImageComponents = licensesDir + "/runtime-components.json"
	// proxyBase は Go の module proxy（版ごとのソースの zip が取れる）
	proxyBase = "https://proxy.golang.org/"
	// mplMarker は MPL-2.0 のライセンス文の見出し
	mplMarker = "Mozilla Public License Version 2.0"
)

func main() {
	check := flag.Bool("check", false, "NOTICE を書き換えず、作り直した内容と違えば失敗する")
	flag.Parse()
	if err := run(*check, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "notice: エラー:", err)
		os.Exit(1)
	}
}

func run(check bool, stdout, stderr io.Writer) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	deps, err := collect(root)
	if err != nil {
		return err
	}
	ofl, err := os.ReadFile(filepath.Join(root, fontOFL))
	if err != nil {
		return err
	}
	goLicense, err := goRootLicense(root)
	if err != nil {
		return err
	}
	rt, err := appImageRuntime(root)
	if err != nil {
		return err
	}
	out := render(deps, string(ofl), goLicense, rt)
	path := filepath.Join(root, outName)
	if check {
		cur, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(cur, out) {
			return errors.New(outName + " が依存と合っていません。go run ./internal/tools/notice で作り直してコミットしてください")
		}
		fmt.Fprintf(stdout, "%s は最新です（モジュール %d 個）\n", outName, len(deps))
		return nil
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "%s を書きました（モジュール %d 個）\n", path, len(deps))
	return nil
}

// moduleRoot は本体のモジュールのルート（go.mod のあるディレクトリ）。
func moduleRoot() (string, error) {
	b, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(b))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("モジュールの中で実行してください（go.mod が見つかりません）")
	}
	return filepath.Dir(gomod), nil
}

// collect は全ての形でリンクされるモジュールを集め、パスで並べて返す。
func collect(root string) ([]dep, error) {
	mods := map[string]module{}
	for _, t := range targets() {
		ms, err := listModules(root, t)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if prev, ok := mods[m.Path]; ok && prev.Version != m.Version {
				return nil, fmt.Errorf("%s の版が形によって違います（%s と %s）", m.Path, prev.Version, m.Version)
			}
			mods[m.Path] = m
		}
	}
	paths := make([]string, 0, len(mods))
	for p := range mods {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var deps []dep
	var missing []string
	for _, p := range paths {
		m := mods[p]
		files, err := licenseFiles(m.Dir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			missing = append(missing, p+" "+m.Version+"（"+m.Dir+"）")
			continue
		}
		d := dep{path: p, version: m.Version, files: files}
		for _, f := range files {
			if strings.Contains(f.text, mplMarker) {
				d.mpl = true
			}
		}
		deps = append(deps, d)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ライセンス文（LICENSE・LICENCE・COPYING・NOTICE）が見つからないモジュールがあります:\n  %s", strings.Join(missing, "\n  "))
	}
	return deps, nil
}

// listModules は 1 つの形で ./cmd/looptrack にリンクされるモジュール（標準ライブラリと本体を除く）。
func listModules(root string, t target) ([]module, error) {
	args := []string{"list", "-deps", "-json=ImportPath,Standard,Module"}
	if t.tags != "" {
		args = append(args, "-tags", t.tags)
	}
	args = append(args, pkgPath)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cgo := "0"
	if t.cgo {
		cgo = "1"
	}
	cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, "CGO_ENABLED="+cgo, "GOFLAGS=-mod=readonly")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s（%s/%s tags=%q cgo=%s）: %w\n%s", strings.Join(args, " "), t.goos, t.goarch, t.tags, cgo, err, stderr.String())
	}
	var ms []module
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list の出力を読めません: %w", err)
		}
		if p.Standard || p.Module == nil || p.Module.Main {
			continue
		}
		m := *p.Module
		if m.Replace != nil {
			// replace されたモジュールは置き換え先の中身がリンクされる（版は置き換え先。ローカルのパスなら空）
			m.Version, m.Dir = m.Replace.Version, m.Replace.Dir
		}
		if m.Dir == "" {
			return nil, fmt.Errorf("%s の置き場（Module.Dir）が分かりません（go mod download を流す）", m.Path)
		}
		ms = append(ms, m)
	}
	return ms, nil
}

// isLicenseName はライセンス文のファイル名か（LICENSE・LICENCE・COPYING・NOTICE。大文字小文字・拡張子・「-MIT」のような接尾辞を許す）。
func isLicenseName(name string) bool {
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	for _, w := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE"} {
		if base == w || strings.HasPrefix(base, w+"-") || strings.HasPrefix(base, w+"_") {
			return true
		}
	}
	return false
}

// licenseFiles は dir の直下のライセンス文（名前で並べる）。
func licenseFiles(dir string) ([]licenseFile, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var fs []licenseFile
	for _, e := range ents {
		if !e.Type().IsRegular() || !isLicenseName(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if t := normalize(string(b)); t != "" {
			fs = append(fs, licenseFile{name: e.Name(), text: t})
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].name < fs[j].name })
	return fs, nil
}

// goRootLicense は Go の標準ライブラリ・ランタイムのライセンス（$GOROOT/LICENSE）。
func goRootLicense(root string) (string, error) {
	cmd := exec.Command("go", "env", "GOROOT")
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOROOT: %w", err)
	}
	goroot := strings.TrimSpace(string(b))
	// 公式の配布は $GOROOT/LICENSE。Homebrew の Go は GOROOT が libexec で、LICENSE はその 1 つ上に置かれる
	for _, dir := range []string{goroot, filepath.Dir(goroot)} {
		if lic, err := os.ReadFile(filepath.Join(dir, "LICENSE")); err == nil {
			return normalize(string(lic)), nil
		}
	}
	return "", fmt.Errorf("Go のライセンス文（%s/LICENSE）を読めません", goroot)
}

// runtimeInfo は AppImage の先頭に付く type2-runtime（Go のモジュールではないので別に扱う）。
type runtimeInfo struct {
	tag, text string
	man       componentsManifest
}

// tarball は対応ソースの tarball 1 本（LGPL-2.1 §6 の資料。再取得して SHA-256 で照合できる）。
type tarball struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// licenseCopy は deploy/release/licenses/ に置いたライセンス文の写し 1 つ。
type licenseCopy struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	text   string // 読み込んだ本文（normalize 済み）
}

// modification は上流が部品に当てている改変（LGPL-2.1 §2 の「改変した版である旨」）。
type modification struct {
	Patch       string `json:"patch"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// component は runtime に静的リンクされた部品 1 つ。
type component struct {
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	License       string        `json:"license"`
	Copyright     string        `json:"copyright"`
	Repository    string        `json:"repository"`
	SourceTarball tarball       `json:"source_tarball"`
	LicenseFiles  []licenseCopy `json:"license_files"`
	Modified      bool          `json:"modified"`
	Modification  *modification `json:"modification"`
	Notes         string        `json:"notes"`
}

// runtimeMeta は runtime 自身（版・ソース・対応ソースの置き場・作り直しの手順）。
type runtimeMeta struct {
	Tag                 string  `json:"tag"`
	Commit              string  `json:"commit"`
	Repository          string  `json:"repository"`
	SourceTarball       tarball `json:"source_tarball"`
	BuildEnvironment    string  `json:"build_environment"`
	LGPLOption          string  `json:"lgpl_option"`
	Relinking           string  `json:"relinking"`
	CorrespondingSource struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		Note   string `json:"note"`
	} `json:"corresponding_source"`
}

// componentsManifest は deploy/release/licenses/runtime-components.json。
type componentsManifest struct {
	Runtime    runtimeMeta `json:"runtime"`
	Components []component `json:"components"`
}

// loadComponents はマニフェストを読み、desktop.sh の runtime の版と合っていること・
// ライセンス文の写しが記録の SHA-256 と合っていること・作り直しの手順があることを確かめて返す。
func loadComponents(root, tag string) (componentsManifest, error) {
	var man componentsManifest
	b, err := os.ReadFile(filepath.Join(root, appImageComponents))
	if err != nil {
		return man, err
	}
	if err := json.Unmarshal(b, &man); err != nil {
		return man, fmt.Errorf("%s を読めません: %w", appImageComponents, err)
	}
	if man.Runtime.Tag != tag {
		return man, fmt.Errorf("%s の runtime.tag（%s）が %s の APPIMAGE_RUNTIME_TAG（%s）と違います。"+
			"runtime の版を上げたら、部品の版・ソース・ライセンス文の写しを取り直してマニフェストを直してください（docs/server/RELEASE.md「2-2. NOTICE」）",
			appImageComponents, man.Runtime.Tag, appImageScript, tag)
	}
	if len(man.Components) == 0 {
		return man, fmt.Errorf("%s に部品がありません", appImageComponents)
	}
	if man.Runtime.Relinking == "" {
		return man, fmt.Errorf("%s の runtime.relinking（作り直しの手順）がありません", appImageComponents)
	}
	if _, err := os.Stat(filepath.Join(root, licensesDir, man.Runtime.Relinking)); err != nil {
		return man, fmt.Errorf("runtime.relinking のファイルがありません: %w", err)
	}
	for i, c := range man.Components {
		if c.Name == "" || c.Version == "" || c.License == "" {
			return man, fmt.Errorf("%s の %d 番目の部品に name・version・license のどれかがありません", appImageComponents, i+1)
		}
		if len(c.LicenseFiles) == 0 {
			return man, fmt.Errorf("%s（%s）にライセンス文の写しがありません", c.Name, appImageComponents)
		}
		if c.SourceTarball.URL == "" || c.SourceTarball.SHA256 == "" {
			return man, fmt.Errorf("%s のソースの tarball（url・sha256）がありません", c.Name)
		}
		for j, f := range c.LicenseFiles {
			text, err := readLicenseCopy(root, f)
			if err != nil {
				return man, fmt.Errorf("%s: %w", c.Name, err)
			}
			man.Components[i].LicenseFiles[j].text = text
		}
	}
	return man, nil
}

// readLicenseCopy は写しを読み、記録の SHA-256 と合っているか確かめる（取り直した写しと入れ替え忘れを見つける）。
func readLicenseCopy(root string, f licenseCopy) (string, error) {
	if f.File == "" || f.SHA256 == "" {
		return "", errors.New("ライセンス文の写しの file・sha256 がありません")
	}
	if strings.ContainsAny(f.File, `/\`) {
		return "", fmt.Errorf("ライセンス文の写しは %s の直下に置いてください: %s", licensesDir, f.File)
	}
	b, err := os.ReadFile(filepath.Join(root, licensesDir, f.File))
	if err != nil {
		return "", err
	}
	if sum := hex.EncodeToString(sha256Sum(b)); sum != f.SHA256 {
		return "", fmt.Errorf("%s の SHA-256 が %s の記録と違います（%s ≠ %s）。"+
			"取り直したなら sha256 も直してください", f.File, appImageComponents, sum, f.SHA256)
	}
	return normalize(string(b)), nil
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// appImageRuntime は desktop.sh が固定している runtime の版と、リポジトリに置いたその版のライセンス文。
// 版を desktop.sh から読むので、runtime を上げたら NOTICE の版も一緒に変わる（CI の notice ジョブが作り直しを促す）。
func appImageRuntime(root string) (runtimeInfo, error) {
	b, err := os.ReadFile(filepath.Join(root, appImageScript))
	if err != nil {
		return runtimeInfo{}, err
	}
	var tag string
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "APPIMAGE_RUNTIME_TAG="); ok {
			tag = strings.Trim(strings.TrimSpace(rest), `"'`)
			break
		}
	}
	if tag == "" {
		return runtimeInfo{}, fmt.Errorf("%s の APPIMAGE_RUNTIME_TAG を読めません", appImageScript)
	}
	lic, err := os.ReadFile(filepath.Join(root, appImageLicense))
	if err != nil {
		return runtimeInfo{}, err
	}
	man, err := loadComponents(root, tag)
	if err != nil {
		return runtimeInfo{}, err
	}
	return runtimeInfo{tag: tag, text: normalize(string(lic)), man: man}, nil
}

// escapeModulePath は module proxy の URL に使う形（大文字は `!小文字`。module path も版も同じ規則）。
func escapeModulePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// repoURL は module path から作るリポジトリの URL（末尾の `/v2` のような major 版の接尾辞は外す）。
func repoURL(path string) string {
	if i := strings.LastIndex(path, "/v"); i > 0 {
		if suffix := path[i+2:]; suffix != "" && suffix != "0" && suffix != "1" && strings.IndexFunc(suffix, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			path = path[:i]
		}
	}
	return "https://" + path
}

// sourceLines は 1 モジュールの入手先の行（と MPL-2.0 の案内）。
func sourceLines(d dep) []string {
	lines := []string{"Source: " + repoURL(d.path)}
	if d.version != "" {
		lines = append(lines, "Source (this version): "+proxyBase+escapeModulePath(d.path)+"/@v/"+escapeModulePath(d.version)+".zip")
	}
	if d.mpl {
		lines = append(lines,
			"This module is licensed under MPL-2.0; its source is available at the URLs above.",
			"このモジュールは MPL-2.0 です。上の URL からソースを取得できます。")
	}
	return lines
}

// normalize は改行を LF にそろえ、各行の末尾の空白と前後の空行を落とす（checkout の改行の違いで差分を出さない）。
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\f\v")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// render は NOTICE の本文。
func render(deps []dep, ofl, goLicense string, rt runtimeInfo) []byte {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("Looptrack - third-party notices\n")
	w("Looptrack の配布物に含まれる第三者のソフトウェアとフォントのライセンス文\n\n")
	w("This file is generated by `go run ./internal/tools/notice`. Do not edit it by hand.\n")
	w("このファイルは `go run ./internal/tools/notice` が作る。手で直さない（依存を変えたら作り直す）。\n\n")
	w("Source URLs / ソースの入手先:\n")
	w("  Each section below has `Source:` (the repository, i.e. the module path without a major-version\n")
	w("  suffix such as `/v2`) and `Source (this version):` (that exact version as a source zip on the Go\n")
	w("  module proxy; an upper-case letter in a module path is escaped as `!` + the lower-case letter).\n")
	w("  各節の `Source:` はリポジトリ、`Source (this version):` は module proxy に置かれたその版のソースの zip\n")
	w("  （module path の大文字は `!小文字`）。MPL-2.0 のモジュールには、その節にソースの入手先の案内を添えた。\n\n")
	w("AppImage runtime components / AppImage の runtime の部品:\n")
	w("  The Linux AppImage prepends a prebuilt runtime that statically links libfuse (LGPL-2.1) and other\n")
	w("  libraries. Their versions, copyright notices, sources (with SHA-256) and full license texts follow\n")
	w("  the runtime section below as `AppImage runtime component: …`.\n")
	w("  Linux の AppImage の先頭に付く runtime には libfuse（LGPL-2.1）ほかが静的リンクされている。版・著作権表示・\n")
	w("  ソース（SHA-256 つき）・ライセンス文の全文は、runtime の節の後ろの「AppImage runtime component: …」にある。\n\n")
	w("Contents:\n")
	w("  BIZ UDGothic (font embedded for PDF reports)\n")
	w("  Go standard library and runtime\n")
	w("  AppImage type2-runtime %s (Linux AppImage only)\n", rt.tag)
	for _, c := range rt.man.Components {
		w("  %s (statically linked into the AppImage runtime, %s)\n", componentName(c), c.License)
	}
	for _, d := range deps {
		w("  %s %s\n", d.path, d.version)
	}
	section := func(title string, sources []string, file, text string) {
		w("\n%s\n%s\n", ruleHeavy, title)
		for _, s := range sources {
			w("%s\n", s)
		}
		if file != "" {
			w("(%s)\n", file)
		}
		w("%s\n\n%s\n", ruleLight, text)
	}
	section("BIZ UDGothic (font embedded for PDF reports) - SIL Open Font License 1.1", nil, "", normalize(ofl))
	section("Go standard library and runtime", []string{
		"Source: https://go.dev/",
		"Source (this version): https://go.dev/dl/ (the toolchain that built this binary)",
	}, "LICENSE", goLicense)
	section("AppImage type2-runtime "+rt.tag+" - MIT", runtimeSourceLines(rt), "LICENSE", rt.text)
	for _, c := range rt.man.Components {
		for _, f := range c.LicenseFiles {
			section("AppImage runtime component: "+componentName(c)+" - "+c.License, componentSourceLines(rt, c), f.File, f.text)
		}
	}
	for _, d := range deps {
		for _, f := range d.files {
			section(d.path+" "+d.version, sourceLines(d), f.name, f.text)
		}
	}
	return []byte(b.String())
}

// componentName は部品の表示名（名前 + 版）。
func componentName(c component) string {
	return c.Name + " " + c.Version
}

// runtimeSourceLines は runtime の節の説明（静的リンクされた部品への案内と、LGPL-2.1 への対応）。
func runtimeSourceLines(rt runtimeInfo) []string {
	m := rt.man.Runtime
	lines := []string{
		"Source: " + appImageRepo,
		"Source (this version): " + appImageRepo + "/releases/tag/" + rt.tag,
	}
	if m.Commit != "" {
		lines = append(lines, "Commit: "+m.Commit)
	}
	if m.SourceTarball.URL != "" {
		lines = append(lines, "Source (tarball): "+m.SourceTarball.URL, "  SHA-256: "+m.SourceTarball.SHA256)
	}
	if m.BuildEnvironment != "" {
		lines = append(lines, "Built by upstream in: "+m.BuildEnvironment)
	}
	names := make([]string, 0, len(rt.man.Components))
	for _, c := range rt.man.Components {
		names = append(names, componentName(c))
	}
	lines = append(lines,
		"Linux 版の AppImage の先頭に付く runtime（Go のモジュールではない）。AppImage の中の",
		"usr/share/doc/looptrack/AppImage-type2-runtime-LICENSE.txt にも同じライセンス文を入れている。",
		fmt.Sprintf("この runtime には %d つの部品が静的リンクされている: %s。", len(names), strings.Join(names, "・")),
		"それぞれの版・著作権表示・ソースの入手先・ライセンス文の全文は、この節の後ろに続く",
		"「AppImage runtime component: …」の節にある（一覧の正本は "+appImageComponents+"）。",
		"Looptrack 自身（usr/bin/looptrack）はこれらの部品と一切リンクしていない（MIT の Go の実行ファイル）。",
	)
	lgpl := false
	for _, c := range rt.man.Components {
		if strings.HasPrefix(c.License, "LGPL") {
			lgpl = true
		}
	}
	if lgpl {
		lines = append(lines,
			"このうち LGPL-2.1 の部品があるため、LGPL-2.1 §6 への対応として、全文（LGPL-2.1.txt）と著作権表示を",
			"ここと AppImage の usr/share/doc/looptrack/licenses/ に入れ、対応ソース（runtime と部品のソースの",
			"tarball）の置き場と、runtime を作り直して差し替える手順（"+m.Relinking+"）を示している。",
		)
		if m.LGPLOption != "" {
			lines = append(lines, "採った選択肢: LGPL-2.1 §"+m.LGPLOption+"。")
		}
	}
	if m.CorrespondingSource.URL != "" {
		lines = append(lines, "対応ソース一式（SHA256SUMS つき）: "+m.CorrespondingSource.URL)
		if m.CorrespondingSource.Status != "published" {
			lines = append(lines,
				"  ↑ この置き場は公開のときに作る（それまでは、下の各部品の Source (tarball) の URL と SHA-256 から",
				"  同じものを取得できる）。")
		}
	}
	lines = append(lines,
		"作り直しの手順: AppImage の usr/share/doc/looptrack/licenses/"+m.Relinking+"（リポジトリの "+licensesDir+"/"+m.Relinking+"）。",
		"fusermount3（GPL-2.0）は同梱しておらず、利用者の OS に入っているものを実行する。",
	)
	return lines
}

// componentSourceLines は部品 1 つの節の説明（著作権表示・ソース・改変の所在）。
func componentSourceLines(rt runtimeInfo, c component) []string {
	var lines []string
	if c.Copyright != "" {
		lines = append(lines, c.Copyright)
	}
	lines = append(lines, "AppImage type2-runtime "+rt.tag+" に静的リンクされている（Linux の AppImage だけ）。")
	if c.Repository != "" {
		lines = append(lines, "Source: "+c.Repository)
	}
	lines = append(lines, "Source (this version): "+c.SourceTarball.URL, "  SHA-256: "+c.SourceTarball.SHA256)
	if c.Modified && c.Modification != nil {
		lines = append(lines,
			"Modified: 上流（AppImage）が改変している。改変の内容は "+c.Modification.Patch,
			"  "+c.Modification.URL,
			"  "+c.Modification.Description)
	}
	if c.Notes != "" {
		lines = append(lines, "Notes: "+c.Notes)
	}
	if strings.HasPrefix(c.License, "LGPL") {
		m := rt.man.Runtime
		lines = append(lines,
			"LGPL-2.1 の全文は、この節の写し（LGPL-2.1.txt）としてこの NOTICE に入っている",
			"（AppImage の中では usr/share/doc/looptrack/licenses/LGPL-2.1.txt）。",
			"runtime を自分で作り直した libfuse で差し替える手順は "+m.Relinking+"。")
		if m.CorrespondingSource.URL != "" {
			lines = append(lines, "対応ソース一式: "+m.CorrespondingSource.URL)
		}
	}
	return lines
}
