package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 書庫の中の秘密の検査。
//
// テキストを gzip・zip・tar にまとめるだけで秘密の検査（checkAttachText）を素通りしないよう、既知の書庫は
// メモリの中で展開し、要素ごとに同じ検査を当てる。展開は標準ライブラリだけで行い、ディスクには書かない。
//
//   - 見分け方は中身の先頭: gzip は 1f 8b、zip は PK\x03\x04（空の zip は PK\x05\x06）、tar は 257 バイト目からの
//     "ustar"。拡張子が .tar で、先頭の見出しが tar として読めるもの（古い v7 の形）も tar として扱う。
//   - 書庫は 2 重まで展開する（tar.gz、zip や tar の中の .gz など）。3 重目の書庫は検査できないので止める。
//   - 上限は 1 つの添付ごとに、展開後の要素の大きさの合計 attachArchiveMaxExpanded と、要素の数 attachArchiveMaxMembers。
//     要素がそれ自体書庫なら、その大きさは数えず中の要素を数える。読むときは必ず残りの枠 + 1 バイトまでしか読まないので、
//     申告の大きさが偽りでも枠を超えて確保しない（zip bomb で止まるまでに確保するのは枠の大きさまで）。
//   - 上限を超えたもの・壊れていて読めないもの・3 重目の書庫は「検査できないので送らない」で止める。検査していないものは送らない。
//   - 書庫の形をしていないバイナリ（画像・PDF・xz・bzip2・7z など）はこれまでどおり中を見ずに送る。
//
// 止めた理由には書庫のパス・中のパス・行・規則の語だけを出し、値は出さない。

const (
	// attachArchiveMaxExpanded は 1 つの添付の書庫から展開する要素の大きさの合計の上限（64 MiB）。
	attachArchiveMaxExpanded = 64 << 20
	// attachArchiveMaxMembers は 1 つの添付の書庫の要素（ファイル）の数の上限（入れ子の中の要素も数える）。
	attachArchiveMaxMembers = 10000
	// attachArchiveMaxDepth は書庫の中の書庫を何段まで展開するか（0 が外側。1 なら 2 重まで）。
	attachArchiveMaxDepth = 1
)

type archiveKind int

const (
	archiveNone archiveKind = iota
	archiveGzip
	archiveZip
	archiveTar
)

func (k archiveKind) String() string {
	switch k {
	case archiveGzip:
		return "gzip"
	case archiveZip:
		return "zip"
	case archiveTar:
		return "tar"
	}
	return ""
}

// detectArchive は中身の先頭（と .tar の拡張子）から書庫の形を見分ける。
func detectArchive(name string, data []byte) archiveKind {
	switch {
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		return archiveGzip
	case bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("PK\x05\x06")):
		return archiveZip
	case len(data) >= 262 && string(data[257:262]) == "ustar":
		return archiveTar
	case strings.EqualFold(path.Ext(filepath.ToSlash(name)), ".tar"):
		if _, err := tar.NewReader(bytes.NewReader(data)).Next(); err == nil || errors.Is(err, tar.ErrInsecurePath) {
			return archiveTar
		}
	}
	return archiveNone
}

// attachDescription は issue attach の -h の説明（検査する書庫と上限を定数から埋める）。
func attachDescription(lang i18n.Lang) string {
	return i18n.T(lang, "cli.cmd.attach.desc", "depth", attachArchiveMaxDepth+1,
		"size", strconv.Itoa(attachArchiveMaxExpanded>>20)+" MiB", "members", attachArchiveMaxMembers)
}

// checkAttachFile は添付するファイル 1 つを確かめる。書庫なら中を展開して要素ごとに、そうでなければそのまま秘密の検査を当てる。
func checkAttachFile(p string, data []byte) error {
	kind := detectArchive(p, data)
	if kind == archiveNone {
		return checkAttachText(p, data)
	}
	s := &archiveScan{path: p}
	return s.scan(kind, filepath.Base(p), data, "", 0)
}

// errArchiveTooLarge は展開後の大きさが枠を超えたこと（呼ぶ側で利用者向けの文面にする）。
var errArchiveTooLarge = errors.New("archive: expanded size over the limit")

// archiveScan は 1 つの添付の書庫を調べる間の枠の消費。
type archiveScan struct {
	path     string // 添付のパス（表示用）
	expanded int64  // ここまでに数えた要素の大きさの合計
	members  int    // ここまでに数えた要素の数
}

// scan は depth 段目の書庫 data（形は kind・名前は name）の要素を調べる。prefix は表示する中のパスの前置き。
func (s *archiveScan) scan(kind archiveKind, name string, data []byte, prefix string, depth int) error {
	switch kind {
	case archiveGzip:
		return s.scanGzip(name, data, prefix, depth)
	case archiveZip:
		return s.scanZip(data, prefix, depth)
	case archiveTar:
		return s.scanTar(data, prefix, depth)
	}
	return nil
}

// scanGzip は gzip を解凍し、中身を 1 つの要素として調べる。中身が書庫（tar.gz など）なら、gzip の層は表示のパスに出さない。
func (s *archiveScan) scanGzip(name string, data []byte, prefix string, depth int) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return s.broken(archiveGzip, err)
	}
	inner, err := s.read(zr)
	if err != nil {
		return s.readErr(archiveGzip, err)
	}
	innerName := gunzipName(name, zr.Name)
	if k := detectArchive(innerName, inner); k != archiveNone {
		if depth+1 > attachArchiveMaxDepth {
			return i18n.Errorf("cli.err.attach_archive_too_deep", "path", s.path, "member", displayMember(prefix+innerName), "depth", attachArchiveMaxDepth+1)
		}
		s.expanded -= int64(len(inner)) // 書庫の中身はそれ自体を数えず、中の要素を数える
		return s.scan(k, innerName, inner, prefix, depth+1)
	}
	return s.member(prefix+innerName, inner, depth)
}

// gunzipName は gzip の中身の名前（表示と .tar の判定に使う）。見出しに名前が無ければ外の名前から .gz を外す。
func gunzipName(outer, header string) string {
	if header != "" {
		return path.Base(filepath.ToSlash(header))
	}
	lower := strings.ToLower(outer)
	switch {
	case strings.HasSuffix(lower, ".tgz"):
		return outer[:len(outer)-len(".tgz")] + ".tar"
	case strings.HasSuffix(lower, ".gz") && len(outer) > len(".gz"):
		return outer[:len(outer)-len(".gz")]
	}
	return outer
}

func (s *archiveScan) scanZip(data []byte, prefix string, depth int) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) { // 書き出さないので、危ないパスの名前でも中は読める
		return s.broken(archiveZip, err)
	}
	files := make([]*zip.File, 0, len(zr.File))
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			files = append(files, f)
		}
	}
	if s.members+len(files) > attachArchiveMaxMembers { // 開く前に、中央の目録の数で止める
		return s.tooMany()
	}
	for _, f := range files {
		s.members++
		if f.UncompressedSize64 > uint64(attachArchiveMaxExpanded-s.expanded) { // 申告の大きさで先に止める（偽りの申告は read が止める）
			return s.tooLarge()
		}
		rc, err := f.Open()
		if err != nil {
			return s.broken(archiveZip, err)
		}
		body, err := s.read(rc)
		_ = rc.Close()
		if err != nil {
			return s.readErr(archiveZip, err)
		}
		if err := s.member(prefix+f.Name, body, depth); err != nil {
			return err
		}
	}
	return nil
}

func (s *archiveScan) scanTar(data []byte, prefix string, depth int) error {
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil && !(errors.Is(err, tar.ErrInsecurePath) && h != nil) {
			return s.broken(archiveTar, err)
		}
		switch h.Typeflag {
		case tar.TypeDir, tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo: // 中身を持たない
			continue
		}
		s.members++
		if s.members > attachArchiveMaxMembers {
			return s.tooMany()
		}
		body, err := s.read(tr) // 疎なファイルは穴を 0 で埋めて返るので、tar の要素も枠を通して読む
		if err != nil {
			return s.readErr(archiveTar, err)
		}
		if err := s.member(prefix+h.Name, body, depth); err != nil {
			return err
		}
	}
}

// member は depth 段目の書庫の要素 1 つを調べる。要素が書庫なら 1 段だけ中へ入り、そうでなければ秘密の検査を当てる。
func (s *archiveScan) member(name string, body []byte, depth int) error {
	if k := detectArchive(name, body); k != archiveNone {
		if depth+1 > attachArchiveMaxDepth {
			return i18n.Errorf("cli.err.attach_archive_too_deep", "path", s.path, "member", displayMember(name), "depth", attachArchiveMaxDepth+1)
		}
		s.expanded -= int64(len(body)) // 書庫の要素はそれ自体を数えず、中の要素を数える
		return s.scan(k, path.Base(name), body, name+"/", depth+1)
	}
	if f, ok := findTextSecret(body); ok {
		return i18n.Errorf("cli.err.attach_secret_member", "path", s.path, "member", displayMember(name), "line", f.Line, "label", f.Label)
	}
	return nil
}

// read は r を残りの枠 + 1 バイトまで読み、枠を超えたら errArchiveTooLarge を返す。読んだ分を枠から引く。
func (s *archiveScan) read(r io.Reader) ([]byte, error) {
	rest := attachArchiveMaxExpanded - s.expanded
	b, err := io.ReadAll(io.LimitReader(r, rest+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > rest {
		return nil, errArchiveTooLarge
	}
	s.expanded += int64(len(b))
	return b, nil
}

func (s *archiveScan) readErr(kind archiveKind, err error) error {
	if errors.Is(err, errArchiveTooLarge) {
		return s.tooLarge()
	}
	return s.broken(kind, err)
}

func (s *archiveScan) tooLarge() error {
	return i18n.Errorf("cli.err.attach_archive_too_large", "path", s.path, "limit", strconv.Itoa(attachArchiveMaxExpanded>>20)+" MiB")
}

func (s *archiveScan) tooMany() error {
	return i18n.Errorf("cli.err.attach_archive_too_many", "path", s.path, "limit", attachArchiveMaxMembers)
}

func (s *archiveScan) broken(kind archiveKind, err error) error {
	return i18n.Errorf("cli.err.attach_archive_broken", "path", s.path, "format", kind.String(), "reason", err.Error())
}

// displayMember は書庫の中のパスを表示できる形にする（端末を操る制御文字を ? に替える）。
func displayMember(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, name)
}
