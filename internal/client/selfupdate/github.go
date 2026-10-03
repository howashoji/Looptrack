package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/relsig"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// GitHub のリリースから取る経路（--from github。--url も LOOPTRACK_API_URL も無いときの既定）。
//
//  1. 公開鍵を持たないビルドは何も取らずに止まる（署名を確かめられないものでは置き換えない。確認先を差し替えても同じ）
//  2. updatecheck.Checker でリリースの一覧から版を選ぶ（semver のタグだけ・/releases/latest は使わない・今の版が rc なら rc も追う・
//     LOOPTRACK_UPDATE_CHECK=off なら通信しない）。選んだ版の SHA256SUMS の署名と版を確かめ、書庫（relsig.ServerArchiveName）の
//     URL と署名されたハッシュを受け取る
//  3. 書庫を上限つきで取り、署名されたハッシュと照合する
//  4. 書庫の中の <書庫の名前から拡張子を除いたもの>/looptrack[.exe] だけを上限つきで取り出す（.. を含む名前があれば書庫ごと拒む）
//  5. 取り出した中身を、同じ署名つきの SHA256SUMS の relsig.BinaryName の行と照合する
//  6. サーバの経路と同じ replace で置き換える（Windows は <名前>.old に退けてから置く）
//
// 今の版より新しい版があるときだけ置き換える。--force は受けない（古い版へ戻す道を作らない）。

// 取り出しの上限（テストで小さくする）。書庫と実行ファイルはどちらもこれより十分小さい。
var (
	maxArchiveBytes int64 = 512 << 20
	maxBinaryBytes  int64 = 512 << 20
)

func runGitHub(lang i18n.Lang, o Options, check bool) error {
	if MinisignPublicKey == "" {
		return i18n.Errorf("selfupdate.err.github_no_key")
	}
	c := &updatecheck.Checker{
		Current:   o.Version,
		PublicKey: MinisignPublicKey,
		Asset:     updatecheck.ArchiveAsset(o.GOOS, o.GOARCH),
		Getenv:    o.Env.Get,
		Client:    o.HTTPClient,
	}
	ctx := context.Background()
	res := c.Check(ctx, updatecheck.Prefs{})
	switch res.Status {
	case updatecheck.StatusDisabled:
		return i18n.Errorf("selfupdate.err.github_disabled", "env", updatecheck.EnvCheck)
	case updatecheck.StatusNotComparable:
		return i18n.Errorf("selfupdate.err.github_not_comparable", "local", o.Version)
	case updatecheck.StatusUpToDate:
		if res.Latest == nil {
			fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.github.no_release", "local", o.Version, "channel", string(res.Channel)))
			return nil
		}
		fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.github.up_to_date", "local", o.Version, "latest", res.Latest.Tag,
			"channel", string(res.Channel), "os", o.GOOS, "arch", o.GOARCH))
		return nil
	case updatecheck.StatusAvailable:
	default:
		// 鍵が無い（ここには来ない）・設定の誤り・取得や署名の確認の失敗
		if res.Err == nil {
			return i18n.Errorf("selfupdate.err.github_check", "reason", string(res.Status))
		}
		return i18n.Wrapf(res.Err, "selfupdate.err.github_check")
	}
	tag := res.Latest.Tag
	a := res.Asset
	if a == nil {
		return i18n.Errorf("selfupdate.err.github_no_asset", "tag", tag, "os", o.GOOS, "arch", o.GOARCH)
	}
	if !res.Signed || len(res.SignedSums) == 0 || a.SHA256 == "" {
		return i18n.Errorf("selfupdate.err.no_signature")
	}
	if check {
		fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.github.available", "local", o.Version, "latest", tag, "os", o.GOOS, "arch", o.GOARCH))
		return nil
	}
	exe, err := target(o)
	if err != nil {
		return err
	}
	cleanupOld(exe)
	archive, err := fetchArchive(ctx, o.HTTPClient, a)
	if err != nil {
		return err
	}
	inner := innerName(a.Name, o.GOOS)
	bin, err := extract(archive, a.Name, inner)
	if err != nil {
		return err
	}
	binName := relsig.BinaryName(tag, o.GOOS, o.GOARCH)
	want, ok := relsig.Lookup(res.SignedSums, binName)
	if !ok {
		return i18n.Errorf("selfupdate.err.sums_missing", "name", binName)
	}
	got := sha256.Sum256(bin)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return i18n.Errorf("selfupdate.err.extracted_mismatch", "archive", a.Name, "entry", inner, "name", binName)
	}
	if err := replace(exe, bin, o.GOOS == "windows"); err != nil {
		return err
	}
	fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.done", "from", o.Version, "to", tag, "exe", exe, "sha256", hex.EncodeToString(got[:])))
	fmt.Fprintln(o.Stdout, i18n.T(lang, "selfupdate.note.reinit"))
	return nil
}

// innerName は書庫の中で取り出す名前（dist.sh archive の並び: 最上位のディレクトリ <書庫の名前から拡張子を除いたもの> の下の looptrack[.exe]）。
func innerName(archive, goos string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(archive, ".tar.gz"), ".zip")
	name := base + "/" + relsig.Command
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// fetchArchive は書庫を上限つきで取り、署名された SHA256SUMS のハッシュ（a.SHA256）と照合する。
func fetchArchive(ctx context.Context, hc *http.Client, a *updatecheck.Asset) ([]byte, error) {
	if !strings.HasPrefix(a.URL, "https://") {
		return nil, i18n.Errorf("selfupdate.err.not_https", "url", a.URL)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	// 渡されたクライアントでも転送先は https に限る（写しに付ける。呼ぶ側のクライアントは変えない）
	c := *hc
	c.CheckRedirect = httpsOnly
	hc = &c
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, i18n.Wrapf(err, "selfupdate.err.fetch", "url", a.URL)
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, i18n.Wrapf(err, "selfupdate.err.fetch", "url", a.URL)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, i18n.Errorf("selfupdate.err.fetch_http", "url", a.URL, "status", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, i18n.Wrapf(err, "selfupdate.err.fetch", "url", a.URL)
	}
	if int64(len(body)) > maxArchiveBytes {
		return nil, i18n.Errorf("selfupdate.err.too_large", "name", a.Name, "limit", maxArchiveBytes)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, a.SHA256) {
		return nil, i18n.Errorf("selfupdate.err.archive_sha", "name", a.Name, "want", a.SHA256, "got", got)
	}
	return body, nil
}

// httpsOnly は https でない転送先へ行かない（GitHub の資産は objects.githubusercontent.com へ転送される）。
func httpsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return i18n.Errorf("selfupdate.err.not_https", "url", req.URL.String())
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// extract は書庫（name の拡張子で tar.gz か zip を決める）から inner だけを取り出す。
//   - 名前に .. の区切りを含む項目があれば、書庫ごと拒む（取り出す名前とは別でも。正しい書庫には無い）
//   - inner と文字どおり同じ名前の、ふつうのファイル 1 つだけを取り出す（ほかの名前は読まない。シンボリックリンクや 2 つ目は拒む）
//   - 大きさは maxBinaryBytes まで（項目に書かれた大きさと、実際に読んだ大きさの両方で確かめる）
func extract(archive []byte, name, inner string) ([]byte, error) {
	var (
		out   []byte
		found bool
	)
	take := func(entry string, regular bool, size int64, open func() (io.ReadCloser, error)) error {
		if hasDotDot(entry) {
			return i18n.Errorf("selfupdate.err.archive_entry", "name", name, "entry", entry)
		}
		if entry != inner {
			return nil
		}
		if found {
			return i18n.Errorf("selfupdate.err.archive_dup", "name", name, "entry", inner)
		}
		if !regular {
			return i18n.Errorf("selfupdate.err.archive_not_regular", "name", name, "entry", inner)
		}
		if size > maxBinaryBytes {
			return i18n.Errorf("selfupdate.err.binary_too_large", "name", name, "entry", inner, "limit", maxBinaryBytes)
		}
		r, err := open()
		if err != nil {
			return i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
		}
		defer r.Close()
		b, err := io.ReadAll(io.LimitReader(r, maxBinaryBytes+1))
		if err != nil {
			return i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
		}
		if int64(len(b)) > maxBinaryBytes {
			return i18n.Errorf("selfupdate.err.binary_too_large", "name", name, "entry", inner, "limit", maxBinaryBytes)
		}
		out, found = b, true
		return nil
	}
	switch {
	case strings.HasSuffix(name, ".tar.gz"):
		zr, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
		}
		tr := tar.NewReader(zr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
			}
			if err := take(h.Name, h.Typeflag == tar.TypeReg, h.Size, func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }); err != nil {
				return nil, err
			}
		}
	case strings.HasSuffix(name, ".zip"):
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, i18n.Wrapf(err, "selfupdate.err.archive_read", "name", name)
		}
		for _, f := range zr.File {
			size := int64(f.UncompressedSize64)
			if f.UncompressedSize64 > uint64(maxBinaryBytes) {
				size = maxBinaryBytes + 1
			}
			if err := take(f.Name, f.Mode().IsRegular(), size, f.Open); err != nil {
				return nil, err
			}
		}
	default:
		return nil, i18n.Errorf("selfupdate.err.archive_type", "name", name)
	}
	if !found {
		return nil, i18n.Errorf("selfupdate.err.archive_no_binary", "name", name, "entry", inner)
	}
	return out, nil
}

// hasDotDot は名前の区切り（/ と \）で分けた中に .. があるか。
func hasDotDot(name string) bool {
	for _, s := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s == ".." {
			return true
		}
	}
	return false
}
