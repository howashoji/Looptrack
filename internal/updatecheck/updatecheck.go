// Package updatecheck は新しい版の確認のうち、デスクトップ版とサーバ版で共通の部分（DESIGN.md §5-1「新しい版の確認」）。
// 知らせ方（トレイ・画面の帯・管理画面・doctor）と置き換えは呼ぶ側が受け持ち、このパッケージは次だけを行う。
//
//   - リリースの一覧（既定は GitHub の /repos/<所有者>/<名前>/releases）を取り、下書きと semver でないタグ（v で始まる
//     relver の形でないもの）を除く。/releases/latest は使わない（Latest が何を指すかに依らず、チャンネルで選ぶため）
//   - チャンネル: stable は正式版だけ、prerelease はプレリリース（rc）も含める。既定は今の版で決める（今の版が rc なら prerelease）。
//     プレリリースかどうかはタグの版（-rc.1 など）と GitHub の prerelease の印のどちらかで見る（rc を Latest として公開しても rc として扱う）
//   - 選んだ版が今の版より新しければ、そのリリースの SHA256SUMS と SHA256SUMS.minisig を取り、埋め込みの公開鍵で署名を確かめ、
//     署名で守られた版（trusted comment と、呼ぶ側が渡した資産の名前）がタグと同じことを確かめてから「新しい版がある」とする。
//     確かめられなければ新しい版として扱わない（前の候補へは戻らない）
//   - 公開鍵を持たないビルドは、GitHub から新しい版を知らせない（確認先へ通信もしない）。確認先を差し替えたときだけ、署名なしで知らせる
//   - 確認しない設定: LOOPTRACK_UPDATE_CHECK=off か、控えのファイルの "check": "off"（メニューの切り替え）。どちらかが off なら通信しない
//   - 起動時と 24 時間ごとに確かめ（Runner）、結果を控えのファイル（Store）に残す
//
// 今の版が比べられない版（dev・日付-コミット ID）なら確かめない（relver と同じく、手元のビルドに更新を迫らない）。
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/relsig"
	"github.com/howashoji/looptrack/internal/relver"
)

// 設定の環境変数。
const (
	EnvCheck   = "LOOPTRACK_UPDATE_CHECK"   // off で確認しない（on・空は確かめる）
	EnvChannel = "LOOPTRACK_UPDATE_CHANNEL" // stable / prerelease（空は今の版で決める）
	EnvURL     = "LOOPTRACK_UPDATE_URL"     // 確認先（リリースの一覧。GitHub の API と同じ形の JSON を返す https の URL）
)

// DefaultURL は既定の確認先（公開リポジトリのリリースの一覧。1 ページ目の 100 件まで）。
const DefaultURL = "https://api.github.com/repos/howashoji/looptrack/releases?per_page=100"

// Interval は確認の間隔（起動時に 1 回、その後はこの間隔で）。
const Interval = 24 * time.Hour

// FileName は控えのファイルの既定の名前（置き場は呼ぶ側が決める。デスクトップ版ならデータの置き場）。
const FileName = "update-check.json"

// 取得の上限（GitHub の一覧・SHA256SUMS・.minisig はどれもこれより十分小さい）。
const (
	maxListBytes = 8 << 20
	maxSumsBytes = 1 << 20
)

// Channel は追う版の範囲。
type Channel string

const (
	// Stable は正式版だけを追う。
	Stable Channel = "stable"
	// Prerelease はプレリリース（rc）も追う。
	Prerelease Channel = "prerelease"
)

// Prefs は控えのファイルに置く利用者の設定（メニューの切り替え）。環境変数と重なるときの扱いは Resolve。
type Prefs struct {
	Check   string  `json:"check,omitempty"`   // "off" で確認しない。空は既定（確かめる）
	Channel Channel `json:"channel,omitempty"` // 空は既定（今の版で決める）
	// Auto は新しい版を自動で置き換えるか（デスクトップ版のメニューの切り替え。既定は false＝知らせるだけ）。
	// 確認そのもの（Resolve）には効かない。置き換えるのは呼ぶ側（internal/client/desktop）
	Auto bool `json:"auto,omitempty"`
}

// Settings は環境変数と Prefs から決めた設定。
type Settings struct {
	Enabled bool
	Channel Channel
	URL     string
}

// Resolve は設定を決める。
//   - 確認の有無: 環境変数と Prefs のどちらかが off なら確かめない（通信しない側を優先する）
//   - チャンネル: 環境変数 > Prefs > 今の版（プレリリースなら Prerelease、それ以外は Stable）
//   - 確認先: 環境変数 > DefaultURL。https の URL だけを受ける
//
// 分からない値は誤りとして返す（呼ぶ側は確かめない。取り違えた設定で通信しない）。
func Resolve(getenv func(string) string, current string, p Prefs) (Settings, error) {
	s := Settings{Enabled: true, URL: DefaultURL}
	for _, c := range []struct{ name, v string }{{EnvCheck, getenv(EnvCheck)}, {"check", p.Check}} {
		switch strings.ToLower(strings.TrimSpace(c.v)) {
		case "", "on":
		case "off":
			s.Enabled = false
		default:
			return Settings{}, i18n.Errorf("updatecheck.err.bad_check", "name", c.name, "value", c.v)
		}
	}
	if v, ok := relver.Parse(current); ok && len(v.Pre) > 0 {
		s.Channel = Prerelease
	} else {
		s.Channel = Stable
	}
	for _, c := range []struct{ name, v string }{{"channel", string(p.Channel)}, {EnvChannel, getenv(EnvChannel)}} {
		switch v := Channel(strings.ToLower(strings.TrimSpace(c.v))); v {
		case "":
		case Stable, Prerelease:
			s.Channel = v
		default:
			return Settings{}, i18n.Errorf("updatecheck.err.bad_channel", "name", c.name, "value", c.v)
		}
	}
	if v := strings.TrimSpace(getenv(EnvURL)); v != "" {
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return Settings{}, i18n.Errorf("updatecheck.err.bad_url", "name", EnvURL, "url", v)
		}
		s.URL = v
	}
	return s, nil
}

// fromGitHub は確認先が GitHub か（公開鍵を持たないビルドは GitHub から新しい版を知らせない）。
func fromGitHub(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := strings.ToLower(p.Hostname())
	return h == "github.com" || strings.HasSuffix(h, ".github.com")
}

// Release はリリース 1 件（GitHub の API の形のうち使う部分）。
type Release struct {
	Tag        string    `json:"tag_name"`
	Draft      bool      `json:"draft,omitempty"`
	Prerelease bool      `json:"prerelease"`
	URL        string    `json:"html_url,omitempty"`
	Published  time.Time `json:"published_at,omitzero"`
	Assets     []Asset   `json:"assets,omitempty"`
}

// Asset はリリースの資産 1 つ。SHA256 は署名された SHA256SUMS から読んだもの（Result.Asset のときだけ）。
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// asset は名前の資産（無ければ nil）。
func (r *Release) asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// isPrerelease はタグの版か GitHub の印がプレリリースを示すか。
func (r *Release) isPrerelease(v relver.Version) bool { return r.Prerelease || len(v.Pre) > 0 }

// Select はリリースの一覧から、チャンネルで追う版のうち最も新しいものを選ぶ（無ければ nil）。
// 下書きと、v で始まる semver でないタグ（appimage-runtime-source-20251108 など）は除く。一覧の並びには依らない。
func Select(list []Release, ch Channel) *Release {
	var best *Release
	var bestV relver.Version
	for i := range list {
		r := &list[i]
		if r.Draft || !strings.HasPrefix(r.Tag, "v") {
			continue
		}
		v, ok := relver.Parse(r.Tag)
		if !ok || (ch != Prerelease && r.isPrerelease(v)) {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	return best
}

// Status は確認の結果の種類。
type Status string

const (
	StatusDisabled      Status = "disabled"       // 確認しない設定（通信していない）
	StatusNotComparable Status = "not_comparable" // 今の版が比べられない（dev など。通信していない）
	StatusNoKey         Status = "no_key"         // 公開鍵を持たないビルドで確認先が GitHub（通信していない）
	StatusUpToDate      Status = "up_to_date"     // 追う版の中で最新
	StatusAvailable     Status = "available"      // 新しい版がある（公開鍵を持つビルドでは署名を確かめた）
	StatusError         Status = "error"          // 設定の誤り・取得や署名の確認の失敗
)

// Result は 1 回の確認の結果（控えのファイルにもこの形で残す）。
type Result struct {
	CheckedAt time.Time `json:"checked_at"`
	Current   string    `json:"current"`
	Channel   Channel   `json:"channel,omitempty"`
	URL       string    `json:"url,omitempty"`
	Status    Status    `json:"status"`
	// Latest は追う版の中で最も新しいリリース（up_to_date と available のとき）
	Latest *Release `json:"latest,omitempty"`
	// Asset は Checker.Asset の名前の資産（available で名前を渡したとき。SHA256 は署名された SHA256SUMS の値）
	Asset *Asset `json:"asset,omitempty"`
	// Signed は署名を確かめたか（公開鍵を持たないビルドで確認先を差し替えたときだけ false の available がある）
	Signed bool `json:"signed,omitempty"`
	// Transient は通信の失敗（取得できない・HTTP の失敗・大きすぎる・一覧を読めない）による error か。
	// 設定の誤り・署名や版の確認の失敗・資産が無いときは false（前の回の結果で知らせ続けない。NoticeFor）
	Transient bool `json:"transient,omitempty"`
	// Err は失敗の理由（StatusError と StatusNoKey）。控えには Error（日英の文面）で残す
	Err   error                `json:"-"`
	Error map[i18n.Lang]string `json:"error,omitempty"`
}

// Available は新しい版があるか。
func (r Result) Available() bool { return r.Status == StatusAvailable }

// Notice は利用者に知らせる新しい版（デスクトップ版のトレイ・画面の帯）。
type Notice struct {
	Version string // 新しい版（タグ）
	Current string // 今の版
	URL     string // リリースのページ（https:// のときだけ。それ以外は空）
}

// Notice は結果から知らせる新しい版を作る（新しい版が無ければ nil）。リリースのページの URL は一覧の値なので、
// 確認先を差し替えたときも考えて https:// のものだけを残す（それ以外をブラウザで開かない）。
func (r Result) Notice() *Notice {
	if !r.Available() || r.Latest == nil {
		return nil
	}
	n := &Notice{Version: r.Latest.Tag, Current: r.Current}
	if u, err := url.Parse(r.Latest.URL); err == nil && u.Scheme == "https" && u.Host != "" {
		n.URL = r.Latest.URL
	}
	return n
}

// Checker は新しい版を確かめる。
type Checker struct {
	Current string // 今の版
	// PublicKey は SHA256SUMS の署名を確かめる公開鍵（selfupdate.MinisignPublicKey を渡す）。空なら GitHub からは知らせない
	PublicKey string
	// Asset は今の版の置き換えに使う資産の名前を版から作る（任意。例 ArchiveAsset・DesktopAsset）。渡すと、その名前が署名された SHA256SUMS と
	// リリースの資産の両方にあることも確かめ、Result.Asset に URL とハッシュを入れる
	Asset  func(version string) string
	Getenv func(string) string // nil なら os.Getenv
	Client *http.Client        // nil なら 30 秒で打ち切り、https でない転送先へは行かない
	Now    func() time.Time    // nil なら time.Now
}

// NoticeFor は今回の結果と、控えにある最後に成功した確認の結果（Record.LastOK）から、知らせる新しい版を決める（無ければ nil）。
//   - 今回 available なら今回の知らせ
//   - 今回が通信の失敗（Transient）なら、最後に成功した結果が available で、その版が今の版より新しいときだけその知らせを残す
//     （オフラインの起動や一時的な失敗で知らせが消えないように。置き換え済み＝その版が今の版以下なら残さない）
//   - それ以外（新しい版が無い・確認を止めた・設定の誤り・署名や版の確認の失敗・資産が無い・比べられない・鍵が無い）は nil
func NoticeFor(res Result, lastOK *Result) *Notice {
	if res.Available() {
		return res.Notice()
	}
	if res.Status != StatusError || !res.Transient || lastOK == nil || !lastOK.Available() || lastOK.Latest == nil ||
		!relver.Older(res.Current, lastOK.Latest.Tag) {
		return nil
	}
	n := lastOK.Notice()
	n.Current = res.Current
	return n
}

// DesktopAsset はデスクトップ版の配布物（Looptrack_<版>_…。名前は deploy/release/desktop.sh と
// .github/workflows/release.yml と同じ）の名前を作る Checker.Asset。macOS は universal の dmg、Linux は AppImage
// （アーキテクチャは x86_64 / aarch64 で書く）、Windows は zip。分からない組み合わせは空（名前では確かめない）。
func DesktopAsset(goos, arch string) func(string) string {
	return func(v string) string {
		switch goos {
		case "darwin":
			return "Looptrack_" + v + "_macos_universal.dmg"
		case "linux":
			switch arch {
			case "amd64":
				return "Looptrack_" + v + "_linux_x86_64.AppImage"
			case "arm64":
				return "Looptrack_" + v + "_linux_aarch64.AppImage"
			}
		case "windows":
			switch arch {
			case "amd64", "arm64":
				return "Looptrack_" + v + "_windows_" + arch + ".zip"
			}
		}
		return ""
	}
}

// ArchiveAsset は Releases に上げる書庫（looptrack_<版>_<os>_<arch>_server.tar.gz。windows は .zip。
// 名前は relsig.ServerArchiveName で、deploy/release/dist.sh の server_base・archive_ext と .github/workflows/release.yml の
// 書庫と同じ）の名前を作る Checker.Asset（サーバ版の知らせが使う）。素の実行ファイルは Releases には無い
// （書庫の中だけ）。SHA256SUMS には書庫自身の行があるので、この名前のまま照合できる（書庫の中の実行ファイルを
// 従来の名前にした行は dev サーバの配布 /api/v1/dist と self-update が使うためのもので、ここでは使わない）。
// 配っていない組み合わせ（linux・darwin・windows × amd64・arm64 の 6 対象の外）は空（名前では確かめない）。
func ArchiveAsset(goos, arch string) func(string) string {
	return func(v string) string {
		switch arch {
		case "amd64", "arm64":
		default:
			return ""
		}
		switch goos {
		case "linux", "darwin", "windows":
			return relsig.ServerArchiveName(v, goos, arch)
		}
		return ""
	}
}

// ServerUpgradeCommand は install.sh で入れたサーバを新しい版に置き換える 1 行（利用者ガイド updating.md「サーバ」・
// DEPLOY.md「更新（--upgrade）」と同じ）。サーバ版の知らせ（管理画面の帯・doctor・起動時のログ）が案内する。
const ServerUpgradeCommand = "curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade"

// Check は 1 回確かめる。p は控えのファイルの設定（メニューの切り替え）。
func (c *Checker) Check(ctx context.Context, p Prefs) Result {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	getenv := c.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	res := Result{CheckedAt: now().UTC(), Current: c.Current}
	s, err := Resolve(getenv, c.Current, p)
	if err != nil {
		return res.fail(StatusError, err)
	}
	res.Channel, res.URL = s.Channel, s.URL
	switch {
	case !s.Enabled:
		res.Status = StatusDisabled
		return res
	case !relver.Valid(c.Current):
		res.Status = StatusNotComparable
		return res
	case c.PublicKey == "" && fromGitHub(s.URL):
		return res.fail(StatusNoKey, i18n.Errorf("updatecheck.err.no_key"))
	}
	list, err := c.fetchList(ctx, s.URL)
	if err != nil {
		return res.fail(StatusError, err)
	}
	r := Select(list, s.Channel)
	if r == nil || !relver.Older(c.Current, r.Tag) {
		res.Status, res.Latest = StatusUpToDate, r.brief()
		return res
	}
	a, err := c.verify(ctx, r)
	if err != nil {
		return res.fail(StatusError, err)
	}
	res.Status, res.Latest, res.Asset, res.Signed = StatusAvailable, r.brief(), a, c.PublicKey != ""
	return res
}

// brief は控えに残す形（資産の一覧は持たない）。
func (r *Release) brief() *Release {
	if r == nil {
		return nil
	}
	b := *r
	b.Assets = nil
	return &b
}

func (r Result) fail(st Status, err error) Result {
	r.Status, r.Err = st, err
	var ff fetchFailure
	r.Transient = errors.As(err, &ff)
	r.Error = map[i18n.Lang]string{i18n.JA: i18n.Text(i18n.JA, err), i18n.EN: i18n.Text(i18n.EN, err)}
	return r
}

// verify は r の SHA256SUMS の署名を確かめ、署名で守られた版がタグと同じことを確かめる（公開鍵が無ければ署名は見ない）。
// Asset を渡されていれば、その名前の資産（URL と署名された SHA256SUMS のハッシュ）を返す。
func (c *Checker) verify(ctx context.Context, r *Release) (*Asset, error) {
	var name string
	if c.Asset != nil {
		name = c.Asset(r.Tag)
	}
	var out *Asset
	if name != "" {
		a := r.asset(name)
		if a == nil {
			return nil, i18n.Errorf("updatecheck.err.asset_missing", "tag", r.Tag, "name", name)
		}
		cp := *a
		out = &cp
	}
	if c.PublicKey == "" {
		return out, nil
	}
	sumsA, sigA := r.asset("SHA256SUMS"), r.asset("SHA256SUMS.minisig")
	if sumsA == nil || sigA == nil {
		return nil, i18n.Errorf("updatecheck.err.no_signature", "tag", r.Tag)
	}
	sums, err := c.get(ctx, sumsA.URL, maxSumsBytes, "")
	if err != nil {
		return nil, err
	}
	sig, err := c.get(ctx, sigA.URL, maxSumsBytes, "")
	if err != nil {
		return nil, err
	}
	trusted, err := relsig.Verify(c.PublicKey, sums, sig)
	if err != nil {
		return nil, i18n.Wrapf(err, "updatecheck.err.signature", "tag", r.Tag)
	}
	v, versioned := relsig.TrustedVersion(trusted)
	if versioned && v != r.Tag {
		return nil, i18n.Errorf("updatecheck.err.trusted_version", "tag", r.Tag, "signed", v)
	}
	if out != nil {
		h, ok := relsig.Lookup(sums, name)
		if !ok {
			return nil, i18n.Errorf("updatecheck.err.asset_missing", "tag", r.Tag, "name", name)
		}
		out.SHA256 = strings.ToLower(h)
	} else if !versioned {
		// 版を持たない trusted comment で、名前でも確かめられない → タグの版を署名で確かめられない
		return nil, i18n.Errorf("updatecheck.err.version_unsigned", "tag", r.Tag)
	}
	return out, nil
}

// fetchList はリリースの一覧を取る。
func (c *Checker) fetchList(ctx context.Context, u string) ([]Release, error) {
	b, err := c.get(ctx, u, maxListBytes, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var list []Release
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fetchFailure{i18n.Errorf("updatecheck.err.list_parse", "url", u, "reason", err)}
	}
	return list, nil
}

// fetchFailure は通信の失敗（Result.Transient）の印。包んだ error の文面・判定はそのまま通す。
type fetchFailure struct{ error }

func (f fetchFailure) Unwrap() error { return f.error }

// get は https の URL の本体を取る（limit バイトまで。超えたら誤り）。https でない URL は通信の失敗ではない
// （確認先・資産の URL の誤り）。それ以外の失敗は fetchFailure で包む。
func (c *Checker) get(ctx context.Context, u string, limit int64, accept string) ([]byte, error) {
	if !strings.HasPrefix(u, "https://") {
		return nil, i18n.Errorf("updatecheck.err.not_https", "url", u)
	}
	b, err := c.fetch(ctx, u, limit, accept)
	if err != nil {
		return nil, fetchFailure{err}
	}
	return b, nil
}

func (c *Checker) fetch(ctx context.Context, u string, limit int64, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, i18n.Wrapf(err, "updatecheck.err.fetch", "url", u)
	}
	req.Header.Set("User-Agent", "looptrack/"+c.Current)
	if accept != "" {
		req.Header.Set("Accept", accept)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	res, err := c.client().Do(req)
	if err != nil {
		return nil, i18n.Wrapf(err, "updatecheck.err.fetch", "url", u)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, i18n.Errorf("updatecheck.err.fetch_http", "url", u, "status", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, i18n.Wrapf(err, "updatecheck.err.fetch", "url", u)
	}
	if int64(len(b)) > limit {
		return nil, i18n.Errorf("updatecheck.err.too_large", "url", u, "limit", limit)
	}
	return b, nil
}

func (c *Checker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: httpsOnly}
}

// httpsOnly は https でない転送先へ行かない（GitHub の資産は objects.githubusercontent.com へ転送される）。
func httpsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return i18n.Errorf("updatecheck.err.not_https", "url", req.URL.String())
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// String はログ向けの 1 行（言語を決めない。利用者に見せる文面は呼ぶ側が Error / Err から作る）。
func (r Result) String() string {
	s := fmt.Sprintf("update check: status=%s current=%s channel=%s", r.Status, r.Current, r.Channel)
	if r.Latest != nil {
		s += " latest=" + r.Latest.Tag
	}
	if r.Err != nil {
		s += " error=" + i18n.Text(i18n.EN, r.Err)
	}
	return s
}
