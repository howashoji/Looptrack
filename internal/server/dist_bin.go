package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/relver"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// 実行ファイル（looptrack）の配布（DESIGN.md §5-1「配布と更新」）。
//
// 並行期間（公開前）は、サーバの配布ディレクトリ（LOOPTRACK_DIST_DIR）に deploy/release/dist.sh の成果物
// （looptrack_<版>_<os>_<arch>[.exe] と SHA256SUMS。置き方は docs/server/RELEASE.md §2-1）を置き、
//   - GET /api/v1/dist（と券の一覧 /setup/<券>/）の binaries に {name, os, arch, version, sha256, size, url} を出す
//   - GET /api/v1/dist/bin/<名前>（と /setup/<券>/bin/<名前>）で本体を返す（X-Looptrack-SHA256）
//
// 配布ディレクトリが無い・空なら binaries は空の一覧（既存のクライアントは binaries を読まないので壊れない）。
// 一覧に出すのは (os, arch) ごとに最も新しい版（relver の順。比べられない版の名前は出さない）の 1 つだけ。
// SHA-256 はサーバが計算し（ファイルの大きさと更新時刻で覚える）、SHA256SUMS があれば突き合わせる（載っていない・違うものは出さない）。
// install.sh で入れたサーバでは、install.sh が配布ディレクトリを受け持つ（入れるときと --upgrade のたびに、取得したリリースの
// 6 対象の実行ファイルと署名つきの SHA256SUMS を置く。docs/server/DEPLOY.md「クライアントに配る looptrack」）。
// 配布物がサーバの版にそろっていないこと（置き場が無い・対象が足りない・古い）は、起動時のログと管理者の画面の帯で知らせる（distLagStatus）。

const (
	binPrefix      = "bin/"
	binCommand     = "looptrack"
	sumsName       = "SHA256SUMS"
	sumsSigName    = "SHA256SUMS.minisig"
	licenseName    = "OFL-BIZUDGothic.txt" // looptrack に埋め込んだフォントのライセンス文（dist.sh が置く）
	noticeName     = "NOTICE"              // 第三者のライセンス文（依存モジュール・Go・フォント。dist.sh が置く）
	binWriteWindow = 10 * time.Minute      // 本体の送信に許す時間（http.Server の WriteTimeout 60 秒では遅い回線で足りない）
)

var binTargets = map[string]bool{
	"linux/amd64": true, "linux/arm64": true, "darwin/amd64": true, "darwin/arm64": true, "windows/amd64": true, "windows/arm64": true,
}

// distTargetOrder は配る対象（binTargets と同じ 6 つ。dist.sh の RELEASE_TARGETS の既定）を、知らせに並べる順で持つ。
var distTargetOrder = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"}

// distBinaryJSON は配布する実行ファイル 1 つ。
type distBinaryJSON struct {
	Name    string `json:"name"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	URL     string `json:"url"`
}

// binSumCache はファイルの SHA-256 の控え（パス → 大きさ・更新時刻・ハッシュ）。
var binSumCache = struct {
	sync.Mutex
	m map[string]binSum
}{m: map[string]binSum{}}

type binSum struct {
	size int64
	mod  time.Time
	sum  string
}

func fileSHA256(p string, fi os.FileInfo) (string, error) {
	binSumCache.Lock()
	c, ok := binSumCache.m[p]
	binSumCache.Unlock()
	if ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.sum, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	binSumCache.Lock()
	binSumCache.m[p] = binSum{fi.Size(), fi.ModTime(), sum}
	binSumCache.Unlock()
	return sum, nil
}

// parseBinName は looptrack_<版>_<os>_<arch>[.exe] を読む。
func parseBinName(name string) (version, goos, arch string, ok bool) {
	rest, found := strings.CutPrefix(name, binCommand+"_")
	if !found {
		return "", "", "", false
	}
	exe := strings.HasSuffix(rest, ".exe")
	rest = strings.TrimSuffix(rest, ".exe")
	parts := strings.Split(rest, "_")
	if len(parts) != 3 {
		return "", "", "", false
	}
	version, goos, arch = parts[0], parts[1], parts[2]
	if !binTargets[goos+"/"+arch] || exe != (goos == "windows") || !relver.Valid(version) {
		return "", "", "", false
	}
	return version, goos, arch, true
}

// readSums は SHA256SUMS（sha256sum の形。名前の前の * は二進の印）を読む。無ければ nil。
func readSums(dir string) map[string]string {
	f, err := os.Open(filepath.Join(dir, sumsName))
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || !sha256Re.MatchString(strings.ToLower(fields[0])) {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return out
}

// distBinaries は配布ディレクトリの実行ファイル（(os, arch) ごとに最新の 1 つ。os・arch の順）。urlBase は本体の URL の前（…/bin/ の手前まで）。
func (s *Server) distBinaries(urlBase string) []distBinaryJSON {
	out := []distBinaryJSON{}
	dir := s.cfg.DistDir
	if dir == "" {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.cfg.Logger.Warn("dist dir", "dir", dir, "err", err)
		}
		return out
	}
	sums := readSums(dir)
	best := map[string]distBinaryJSON{}
	for _, e := range entries {
		version, goos, arch, ok := parseBinName(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		key := goos + "/" + arch
		if cur, seen := best[key]; seen && !relver.Older(cur.Version, version) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		sum, err := fileSHA256(p, fi)
		if err != nil {
			s.cfg.Logger.Warn("dist binary", "name", e.Name(), "err", err)
			continue
		}
		if sums != nil && sums[e.Name()] != sum {
			// SHA256SUMS に無い・違う（置き換えの途中・壊れたコピー）は配らない
			s.cfg.Logger.Warn("dist binary not in SHA256SUMS", "name", e.Name(), "sha256", sum, "listed", sums[e.Name()])
			continue
		}
		best[key] = distBinaryJSON{Name: e.Name(), OS: goos, Arch: arch, Version: version, SHA256: sum, Size: fi.Size(),
			URL: urlBase + binPrefix + e.Name()}
	}
	for _, b := range best {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		return out[i].Arch < out[j].Arch
	})
	return out
}

// latestClient は (os, arch) の配布中の最新の版（無ければ ""）。os・arch が空なら全体の最新。
func (s *Server) latestClient(goos, arch string) string {
	latest := ""
	for _, b := range s.distBinaries("") {
		if (goos != "" && b.OS != goos) || (arch != "" && b.Arch != arch) {
			continue
		}
		if latest == "" || relver.Older(latest, b.Version) {
			latest = b.Version
		}
	}
	return latest
}

// distSumsLinks は SHA256SUMS と署名（minisign）・フォントのライセンス文・NOTICE の URL（置いてあるものだけ）。
func (s *Server) distSumsLinks(urlBase string) map[string]string {
	out := map[string]string{}
	if s.cfg.DistDir == "" {
		return out
	}
	for key, name := range map[string]string{"sha256sums_url": sumsName, "sha256sums_minisig_url": sumsSigName, "license_url": licenseName, "notice_url": noticeName} {
		if fi, err := os.Stat(filepath.Join(s.cfg.DistDir, name)); err == nil && fi.Mode().IsRegular() {
			out[key] = urlBase + binPrefix + name
		}
	}
	return out
}

// serveDistBinary は bin/<名前> の本体を返す（一覧に出したもの・SHA256SUMS・その署名だけ）。見つからなければ false。
func (s *Server) serveDistBinary(w http.ResponseWriter, r *http.Request, name string) bool {
	base, ok := strings.CutPrefix(name, binPrefix)
	if !ok || s.cfg.DistDir == "" || strings.ContainsAny(base, `/\`) {
		return false
	}
	var sum string
	switch base {
	case sumsName, sumsSigName, licenseName, noticeName:
	default:
		found := false
		for _, b := range s.distBinaries("") {
			if b.Name == base {
				found, sum = true, b.SHA256
			}
		}
		if !found {
			return false
		}
	}
	f, err := os.Open(filepath.Join(s.cfg.DistDir, base))
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if sum == "" {
		if sum, err = fileSHA256(f.Name(), fi); err != nil {
			return false
		}
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(binWriteWindow))
	ct := "application/octet-stream"
	if base == sumsName || base == licenseName || base == noticeName {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Looptrack-SHA256", sum)
	w.Header().Set("Content-Disposition", `attachment; filename="`+base+`"`)
	http.ServeContent(w, r, base, fi.ModTime(), f)
	return true
}

// distLag は配布ディレクトリの looptrack がサーバの版にそろっていないこと（起動時のログと管理者の画面の帯で知らせる）。
type distLag struct {
	Version string   // サーバの版
	Unset   bool     // 配布ディレクトリ（LOOPTRACK_DIST_DIR）が無い
	Missing []string // 配っていない対象（os/arch）
	Older   []string // サーバの版より古い版を配っている対象（「os/arch 版」）
}

// distLagStatus は配布物がサーバの版にそろっていなければその中身を返す（そろっていれば nil）。
// 比べないとき（ローカルモード・比べられない版のサーバ（dev など）・LOOPTRACK_DIST_DIR を空の値で明示した（配らない））も nil。
// サーバより新しい版を配っているのは古さではないので知らせない。
func (s *Server) distLagStatus() *distLag {
	v := s.cfg.Version
	if s.cfg.LocalMode || !relver.Valid(v) || (s.cfg.DistOff && s.cfg.DistDir == "") {
		return nil
	}
	lag := &distLag{Version: v}
	if s.cfg.DistDir == "" {
		lag.Unset = true
		return lag
	}
	have := map[string]distBinaryJSON{}
	for _, b := range s.distBinaries("") {
		have[b.OS+"/"+b.Arch] = b
	}
	for _, t := range distTargetOrder {
		b, ok := have[t]
		switch {
		case !ok:
			lag.Missing = append(lag.Missing, t)
		case relver.Older(b.Version, v):
			lag.Older = append(lag.Older, t+" "+b.Version)
		}
	}
	if len(lag.Missing) == 0 && len(lag.Older) == 0 {
		return nil
	}
	return lag
}

// distLagText は知らせの文面（command を渡せば更新の 1 行を添える。画面の帯は 1 行を <code> で別に出すので空にする）。
func distLagText(lang i18n.Lang, lag *distLag, dir, command string) string {
	if lag.Unset {
		return i18n.T(lang, "server.dist.lag.unset", "version", lag.Version) + distLagHint(lang, command)
	}
	var detail []string
	if len(lag.Missing) > 0 {
		detail = append(detail, i18n.T(lang, "server.dist.lag.missing", "targets", strings.Join(lag.Missing, ", ")))
	}
	if len(lag.Older) > 0 {
		detail = append(detail, i18n.T(lang, "server.dist.lag.older", "targets", strings.Join(lag.Older, ", ")))
	}
	return i18n.T(lang, "server.dist.lag.stale", "version", lag.Version, "dir", dir,
		"detail", strings.Join(detail, i18n.T(lang, "server.dist.lag.sep"))) + distLagHint(lang, command)
}

// templateLang はテンプレートから渡る .Lang（i18n.Lang か言語の名前。i18n.TFunc と同じ読み方）。
func templateLang(lang any) i18n.Lang {
	switch v := lang.(type) {
	case i18n.Lang:
		return v
	case string:
		if p, ok := i18n.Parse(v); ok {
			return p
		}
	}
	var l i18n.Lang
	return l
}

func distLagHint(lang i18n.Lang, command string) string {
	if command == "" {
		return ""
	}
	return i18n.T(lang, "server.dist.lag.hint", "command", command)
}

// LogDistLag は配布物がサーバの版にそろっていなければ、起動時のログに 1 行（Warn）を出す（looptrack serve が起動時に呼ぶ）。
// LOOPTRACK_DIST_DIR を空の値で明示したとき（配らないと決めた）は、警告ではなく Info の 1 行だけを残す。
func (s *Server) LogDistLag(lang i18n.Lang) {
	if s.cfg.DistOff && s.cfg.DistDir == "" && !s.cfg.LocalMode {
		s.cfg.Logger.Info(i18n.T(lang, "server.dist.lag.off"))
		return
	}
	lag := s.distLagStatus()
	if lag == nil {
		return
	}
	s.cfg.Logger.Warn(distLagText(lang, lag, s.cfg.DistDir, updatecheck.ServerUpgradeCommand),
		"version", lag.Version, "dist_dir", s.cfg.DistDir, "missing", strings.Join(lag.Missing, ","), "older", strings.Join(lag.Older, ","))
}
