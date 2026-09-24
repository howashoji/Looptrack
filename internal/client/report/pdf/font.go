package pdf

import (
	"embed"
	"encoding/binary"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 日本語フォント（§5-11 Q4）。
//
// 探す順: TOKEN_REPORT_FONT（TrueType のパス。指定は残す）→ 実行ファイルに埋め込んだフォント（fonts/ の *.ttf。
// BIZ UDGothic を置いてビルドする）→ 手元の既定の置き場（以前の PDF の作り方と同じ並び）。
// gopdf は TrueType（glyf）だけを扱える。CFF（OpenType の OTTO）は読めないので飛ばす。TTC は最初のフォントを取り出して使う。

// 埋め込んだフォントのライセンス文 fonts/OFL.txt（SIL OFL 1.1）は、ルートの NOTICE（go run ./internal/tools/notice が作る）に入り、
// looptrack licenses が表示する。配布物には NOTICE と OFL-BIZUDGothic.txt（この写し）を置く。

// FontEnv はフォントを指定する環境変数。
const FontEnv = "TOKEN_REPORT_FONT"

// 埋め込むのはフォント本体とライセンス文だけに絞る（ディレクトリ丸ごとだと fonts/README.md のような
// 置き場の説明まで配布物に入る）。OFL.txt は再頒布の条件なので必ず含める。
//
//go:embed fonts/*.ttf fonts/*.txt
var embedded embed.FS

// Candidates は手元の既定の置き場（~ は home）。
var Candidates = []string{
	"~/Library/Fonts/BIZUDGothic-Regular.ttf", "/Library/Fonts/BIZUDGothic-Regular.ttf",
	"/usr/share/fonts/truetype/fonts-japanese-gothic.ttf",
	"/usr/share/fonts/opentype/ipaexfont-gothic/ipaexg.ttf", "/usr/share/fonts/truetype/ipaexfont-gothic/ipaexg.ttf",
	"~/Library/Fonts/ipaexg.ttf", "/Library/Fonts/ipaexg.ttf",
	"C:/Windows/Fonts/BIZ-UDGothicR.ttc",
}

// Font は使うフォント。
type Font struct {
	Name string // 由来（パス・embedded:<名前>）
	Data []byte // TrueType（TTC から取り出したものを含む）
}

// ErrNoFont は使える日本語フォントが無い。
var ErrNoFont = i18n.Errorf("report.font.err.none", "env", FontEnv)

// EmbeddedFonts は埋め込んだ TrueType の名前（無ければ空）。
func EmbeddedFonts() []string {
	var out []string
	_ = fs.WalkDir(embedded, "fonts", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(path.Ext(p), ".ttf") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// FindFont は探す順にフォントを探す。getenv は環境変数、home はホームディレクトリ。warn に飛ばした理由を書く。
// check は日本語を描けるかを確かめる関数（nil なら形式だけ見る）。
func FindFont(lang i18n.Lang, getenv func(string) string, home string, check func([]byte) error, warn func(string)) (Font, error) {
	try := func(name string, data []byte) (Font, bool) {
		ttf, err := trueType(data)
		if err == nil && check != nil {
			err = check(ttf)
		}
		if err != nil {
			if warn != nil {
				warn(i18n.T(lang, "report.font.warn.unusable", "name", name, "reason", err))
			}
			return Font{}, false
		}
		return Font{Name: name, Data: ttf}, true
	}
	if p := strings.TrimSpace(getenv(FontEnv)); p != "" {
		p = expand(p, home)
		data, err := os.ReadFile(p)
		if err != nil {
			if warn != nil {
				warn(i18n.T(lang, "report.font.warn.unreadable", "env", FontEnv, "reason", err))
			}
		} else if f, ok := try(p, data); ok {
			return f, nil
		}
	}
	for _, name := range EmbeddedFonts() {
		data, err := embedded.ReadFile(name)
		if err != nil {
			continue
		}
		if f, ok := try("embedded:"+path.Base(name), data); ok {
			return f, nil
		}
	}
	for _, p := range Candidates {
		p = expand(p, home)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if f, ok := try(p, data); ok {
			return f, nil
		}
	}
	return Font{}, ErrNoFont
}

func expand(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		return filepath.Join(home, p[1:])
	}
	return p
}

// trueType は TrueType（glyf）のフォントのバイト列を返す。TTC は最初のフォントを 1 つの TrueType に組み直す。
func trueType(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, i18n.Errorf("report.font.err.not_font")
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "true":
		if !hasTable(data, 0, "glyf") {
			return nil, i18n.Errorf("report.font.err.no_glyf")
		}
		return data, nil
	case "OTTO":
		return nil, i18n.Errorf("report.font.err.cff")
	case "ttcf":
		return firstOfCollection(data)
	}
	return nil, i18n.Errorf("report.font.err.not_truetype")
}

// hasTable は off から始まる表の目録に tag があるか。
func hasTable(data []byte, off int, tag string) bool {
	if off+12 > len(data) {
		return false
	}
	n := int(binary.BigEndian.Uint16(data[off+4:]))
	for i := 0; i < n; i++ {
		rec := off + 12 + 16*i
		if rec+16 > len(data) {
			return false
		}
		if string(data[rec:rec+4]) == tag {
			return true
		}
	}
	return false
}

// firstOfCollection は TTC の最初のフォントを単独の TrueType にする（表の目録を作り直し、表の中身を写す）。
func firstOfCollection(data []byte) ([]byte, error) {
	if len(data) < 16 || binary.BigEndian.Uint32(data[8:]) == 0 {
		return nil, i18n.Errorf("report.font.err.ttc_empty")
	}
	off := int(binary.BigEndian.Uint32(data[12:]))
	if off+12 > len(data) {
		return nil, i18n.Errorf("report.font.err.ttc_broken")
	}
	if tag := string(data[off : off+4]); tag == "OTTO" {
		return nil, i18n.Errorf("report.font.err.cff")
	}
	if !hasTable(data, off, "glyf") {
		return nil, i18n.Errorf("report.font.err.no_glyf")
	}
	n := int(binary.BigEndian.Uint16(data[off+4:]))
	head := 12 + 16*n
	if head > len(data) {
		return nil, i18n.Errorf("report.font.err.ttc_broken")
	}
	out := make([]byte, head, max(head, len(data)/2))
	copy(out[:12], data[off:off+12])
	for i := 0; i < n; i++ {
		rec := off + 12 + 16*i
		if rec+16 > len(data) {
			return nil, i18n.Errorf("report.font.err.ttc_broken")
		}
		start := int(binary.BigEndian.Uint32(data[rec+8:]))
		length := int(binary.BigEndian.Uint32(data[rec+12:]))
		if start < 0 || length < 0 || start+length > len(data) {
			return nil, i18n.Errorf("report.font.err.ttc_table")
		}
		copy(out[12+16*i:], data[rec:rec+8]) // tag・checksum
		binary.BigEndian.PutUint32(out[12+16*i+8:], uint32(len(out)))
		binary.BigEndian.PutUint32(out[12+16*i+12:], uint32(length))
		out = append(out, data[start:start+length]...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return out, nil
}
