// winversion は Windows の実行ファイルに入れる版の情報（VERSIONINFO）を扱う。リリースのビルド
// （deploy/release/desktop.sh と dist.sh）だけが使い、成果物には入らない。この道具は標準ライブラリだけで書く。
//
//	go run ./internal/tools/winversion syso -version <版> -arch <amd64|arm64> -original <ファイル名> [-icon <.ico>] -out <.syso>
//	    版の情報（とアイコン）を持つ .syso を作る。中では go-winres（下の goWinres。go run で版を固定して取る。0BSD）の
//	    make に、この道具が書いた winres.json を渡す。go.mod には足さない（ビルドの道具で、成果物にリンクされない）
//	go run ./internal/tools/winversion check -version <版> <exe>...
//	    exe の資源から VS_VERSION_INFO を読み戻し、ProductName が Looptrack で ProductVersion が <版> そのもの、
//	    固定部の版が numeric の規則どおりかを確かめる。違えば（版の情報が無いときも）非 0 で終わる
//	go run ./internal/tools/winversion numeric <版>
//	    固定部に入れる数値の版を出す
//
// 値の決め方:
//   - ProductName は Looptrack。ProductVersion（文字列）はビルドの版をそのまま入れる（v1.0.0-rc.5 も、試しの版も）。
//   - 固定部の FileVersion・ProductVersion は 0〜65535 の数を点で 4 つまでつないだものしか持てない。
//     そこで desktop.sh の iss_version（Inno Setup の VersionInfoVersion）と同じ規則で決める。
//     vX.Y.Z と vX.Y.Z-rc.N は X.Y.Z、それ以外（日付-コミット ID の試しの版）は 0.0.0。文字列の FileVersion も同じ値。
//     規則を変えたら desktop.sh の iss_version もそろえる（deploy/release/desktop_version_test.sh が
//     この道具の numeric と iss_version を同じ入力で突き合わせる）。
//   - CompanyName は Looptrack.iss の AppPublisher と同じ Looptrack。著作権の欄は Looptrack.iss と同じく入れない。
//   - アイコンを渡したときだけ RT_GROUP_ICON を入れる。マニフェストは入れない（置き換える前の rsrc も入れていなかった）。
package main

import (
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

const productName = "Looptrack"

// goWinres は .syso を作る道具（https://github.com/tc-hib/go-winres。0BSD）。版を上げるときは
// docs/server/RELEASE.md の道具の表と DESIGN.md のライセンスの表も直す。
const goWinres = "github.com/tc-hib/go-winres@v0.3.3"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "winversion:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("使い方: winversion syso|check|numeric …")
	}
	switch args[0] {
	case "syso":
		return cmdSyso(args[1:])
	case "check":
		return cmdCheck(args[1:], stdout)
	case "numeric":
		if len(args) != 2 {
			return errors.New("使い方: winversion numeric <版>")
		}
		fmt.Fprintln(stdout, numericVersion(args[1]))
		return nil
	default:
		return fmt.Errorf("知らないサブコマンドです: %s", args[0])
	}
}

// numericVersion は固定部に入れる版を返す（desktop.sh の iss_version と同じ規則）。
func numericVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return "0.0.0"
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return "0.0.0"
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || n > 65535 {
			return "0.0.0"
		}
	}
	return v
}

// fixedWords は "X.Y[.Z[.W]]" を固定部の 4 つの数にする（足りない分は 0）。
func fixedWords(v string) [4]uint16 {
	var w [4]uint16
	for i, p := range strings.Split(v, ".") {
		if i >= 4 {
			break
		}
		n, _ := strconv.ParseUint(p, 10, 16)
		w[i] = uint16(n)
	}
	return w
}

func cmdSyso(args []string) error {
	fs := flag.NewFlagSet("syso", flag.ContinueOnError)
	version := fs.String("version", "", "ビルドの版")
	arch := fs.String("arch", "", "amd64 か arm64")
	original := fs.String("original", "", "OriginalFilename に入れるファイル名")
	icon := fs.String("icon", "", "入れるアイコン（.ico。省略するとアイコンは入れない）")
	out := fs.String("out", "", "書き出す .syso のパス")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || *original == "" || *out == "" || fs.NArg() != 0 {
		return errors.New("使い方: winversion syso -version <版> -arch <amd64|arm64> -original <ファイル名> [-icon <.ico>] -out <.syso>")
	}
	if *arch != "amd64" && *arch != "arm64" {
		return fmt.Errorf("arch は amd64 か arm64: %q", *arch)
	}
	b, err := winresJSON(*version, *original, *icon != "")
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "looptrack-winres.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if *icon != "" {
		data, err := os.ReadFile(*icon)
		if err != nil {
			return err
		}
		// go-winres は JSON の中のパスを JSON の置き場から解決するので、同じ場所に写す
		if err := os.WriteFile(filepath.Join(dir, "app.ico"), data, 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "winres.json"), b, 0o644); err != nil {
		return err
	}
	cmd := exec.Command("go", "run", goWinres, "make", "--in", filepath.Join(dir, "winres.json"),
		"--arch", *arch, "--out", *out, "--no-suffix")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s make: %w", goWinres, err)
	}
	return nil
}

// winresJSON は go-winres make の入力（winres.json）を作る。アイコンは同じディレクトリの app.ico を指す。
func winresJSON(version, original string, withIcon bool) ([]byte, error) {
	num := numericVersion(version)
	res := map[string]any{
		"RT_VERSION": map[string]any{
			"#1": map[string]any{
				"0000": map[string]any{
					"fixed": map[string]string{
						"file_version":    num,
						"product_version": num,
					},
					"info": map[string]any{
						"0409": map[string]string{
							"CompanyName":      productName,
							"FileDescription":  productName,
							"FileVersion":      num,
							"OriginalFilename": original,
							"ProductName":      productName,
							"ProductVersion":   version,
						},
					},
				},
			},
		},
	}
	if withIcon {
		res["RT_GROUP_ICON"] = map[string]any{"APP": map[string]any{"0000": "app.ico"}}
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func cmdCheck(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	version := fs.String("version", "", "期待する ProductVersion（ビルドの版）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || fs.NArg() == 0 {
		return errors.New("使い方: winversion check -version <版> <exe>...")
	}
	var fails []string
	for _, f := range fs.Args() {
		if err := checkFile(f, *version, stdout); err != nil {
			fails = append(fails, fmt.Sprintf("%s: %v", f, err))
		}
	}
	if len(fails) > 0 {
		return errors.New(strings.Join(fails, "\n"))
	}
	return nil
}

func checkFile(path, version string, stdout io.Writer) error {
	vi, err := readVersionInfo(path)
	if err != nil {
		return err
	}
	var bad []string
	if got := vi.Strings["ProductName"]; got != productName {
		bad = append(bad, fmt.Sprintf("ProductName が %q（期待 %q）", got, productName))
	}
	if got := vi.Strings["ProductVersion"]; got != version {
		bad = append(bad, fmt.Sprintf("ProductVersion が %q（期待 %q）", got, version))
	}
	want := fixedWords(numericVersion(version))
	if vi.FixedProduct != want {
		bad = append(bad, fmt.Sprintf("固定部の ProductVersion が %s（期待 %s）", words(vi.FixedProduct), words(want)))
	}
	if vi.FixedFile != want {
		bad = append(bad, fmt.Sprintf("固定部の FileVersion が %s（期待 %s）", words(vi.FixedFile), words(want)))
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "・"))
	}
	fmt.Fprintf(stdout, "ok %s: ProductName=%s ProductVersion=%s 固定部=%s\n",
		filepath.Base(path), vi.Strings["ProductName"], vi.Strings["ProductVersion"], words(vi.FixedProduct))
	return nil
}

func words(w [4]uint16) string {
	return fmt.Sprintf("%d.%d.%d.%d", w[0], w[1], w[2], w[3])
}

// versionInfo は VS_VERSION_INFO から読んだもの。Strings は最初の StringTable の値。
type versionInfo struct {
	FixedFile    [4]uint16
	FixedProduct [4]uint16
	Strings      map[string]string
}

const rtVersion = 16

// readVersionInfo は PE の資源（.rsrc）から RT_VERSION を探して読む。
func readVersionInfo(path string) (*versionInfo, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var dd pe.DataDirectory
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if len(oh.DataDirectory) > pe.IMAGE_DIRECTORY_ENTRY_RESOURCE {
			dd = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
		}
	case *pe.OptionalHeader32:
		if len(oh.DataDirectory) > pe.IMAGE_DIRECTORY_ENTRY_RESOURCE {
			dd = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
		}
	default:
		return nil, errors.New("PE の optional header がありません")
	}
	if dd.VirtualAddress == 0 || dd.Size == 0 {
		return nil, errors.New("資源（.rsrc）がありません（版の情報が入っていない）")
	}
	var sect *pe.Section
	for _, s := range f.Sections {
		if dd.VirtualAddress >= s.VirtualAddress && dd.VirtualAddress < s.VirtualAddress+max(s.VirtualSize, s.Size) {
			sect = s
			break
		}
	}
	if sect == nil {
		return nil, errors.New("資源のディレクトリを含むセクションがありません")
	}
	data, err := sect.Data()
	if err != nil {
		return nil, err
	}
	base := dd.VirtualAddress - sect.VirtualAddress
	if int(base) >= len(data) {
		return nil, errors.New("資源のディレクトリがセクションの外を指しています")
	}
	rsrc := data[base:]
	raw, err := findResource(rsrc, rtVersion)
	if err != nil {
		return nil, err
	}
	// データの項目は RVA を持つ
	off := int64(raw.rva) - int64(dd.VirtualAddress)
	if off < 0 || off+int64(raw.size) > int64(len(rsrc)) {
		return nil, errors.New("RT_VERSION のデータが資源の外を指しています")
	}
	return parseVersionInfo(rsrc[off : off+int64(raw.size)])
}

type rawResource struct{ rva, size uint32 }

// findResource は資源のディレクトリ（種類 → 名前 → 言語）をたどり、種類 typeID の最初の項目を返す。
func findResource(rsrc []byte, typeID uint32) (rawResource, error) {
	entry, ok, err := dirEntry(rsrc, 0, func(id uint32, _ int) bool { return id == typeID })
	if err != nil {
		return rawResource{}, err
	}
	if !ok {
		return rawResource{}, errors.New("RT_VERSION がありません（版の情報が入っていない）")
	}
	for depth := 0; depth < 2; depth++ {
		if entry&0x80000000 == 0 {
			return rawResource{}, errors.New("資源のディレクトリの形が不正です")
		}
		entry, ok, err = dirEntry(rsrc, int(entry&0x7fffffff), func(uint32, int) bool { return true })
		if err != nil {
			return rawResource{}, err
		}
		if !ok {
			return rawResource{}, errors.New("RT_VERSION のディレクトリが空です")
		}
	}
	if entry&0x80000000 != 0 || int(entry)+16 > len(rsrc) {
		return rawResource{}, errors.New("資源のデータの項目が不正です")
	}
	return rawResource{
		rva:  binary.LittleEndian.Uint32(rsrc[entry:]),
		size: binary.LittleEndian.Uint32(rsrc[entry+4:]),
	}, nil
}

// dirEntry は off にあるディレクトリから match に合う最初の項目の OffsetToData を返す。
func dirEntry(rsrc []byte, off int, match func(id uint32, i int) bool) (uint32, bool, error) {
	if off < 0 || off+16 > len(rsrc) {
		return 0, false, errors.New("資源のディレクトリが範囲の外です")
	}
	named := int(binary.LittleEndian.Uint16(rsrc[off+12:]))
	ids := int(binary.LittleEndian.Uint16(rsrc[off+14:]))
	for i := 0; i < named+ids; i++ {
		e := off + 16 + 8*i
		if e+8 > len(rsrc) {
			return 0, false, errors.New("資源のディレクトリの項目が範囲の外です")
		}
		name := binary.LittleEndian.Uint32(rsrc[e:])
		if name&0x80000000 != 0 {
			// 名前つきの項目は種類の照合では使わない（名前・言語の段は match が常に真）
			if match(^uint32(0), i) {
				return binary.LittleEndian.Uint32(rsrc[e+4:]), true, nil
			}
			continue
		}
		if match(name, i) {
			return binary.LittleEndian.Uint32(rsrc[e+4:]), true, nil
		}
	}
	return 0, false, nil
}

// block は VS_VERSIONINFO の入れ子の 1 段（wLength・wValueLength・wType・szKey・Value・Children）。
type block struct {
	key      string
	value    []byte
	text     bool
	children []byte
}

func align4(n int) int { return (n + 3) &^ 3 }

// parseBlock は b の先頭の 1 段を読み、その長さ（4 の倍数に切り上げる前）を返す。
func parseBlock(b []byte) (block, int, error) {
	if len(b) < 6 {
		return block{}, 0, errors.New("VS_VERSIONINFO が途中で切れています")
	}
	length := int(binary.LittleEndian.Uint16(b))
	valueLen := int(binary.LittleEndian.Uint16(b[2:]))
	typ := binary.LittleEndian.Uint16(b[4:])
	if length < 6 || length > len(b) {
		return block{}, 0, errors.New("VS_VERSIONINFO の長さが不正です")
	}
	b = b[:length]
	// szKey（UTF-16 の NUL 終端）
	var key []uint16
	p := 6
	for {
		if p+2 > len(b) {
			return block{}, 0, errors.New("VS_VERSIONINFO の鍵が終わっていません")
		}
		c := binary.LittleEndian.Uint16(b[p:])
		p += 2
		if c == 0 {
			break
		}
		key = append(key, c)
	}
	p = align4(p)
	bl := block{key: string(utf16.Decode(key)), text: typ == 1}
	vbytes := valueLen
	if bl.text {
		vbytes = valueLen * 2 // 文字列の値の長さは WORD（文字）の数
	}
	if p > len(b) {
		return block{}, 0, errors.New("VS_VERSIONINFO の値が範囲の外です")
	}
	if p+vbytes > len(b) {
		// 書き手によって wValueLength の数え方（終端の NUL を含むか）が揺れるので、段の終わりで切る
		vbytes = len(b) - p
	}
	bl.value = b[p : p+vbytes]
	p = align4(p + vbytes)
	if p < len(b) {
		bl.children = b[p:]
	}
	return bl, length, nil
}

func eachChild(b []byte, fn func(block) error) error {
	for len(b) > 0 {
		c, n, err := parseBlock(b)
		if err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
		n = align4(n)
		if n >= len(b) {
			break
		}
		b = b[n:]
	}
	return nil
}

func utf16String(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// parseVersionInfo は RT_VERSION のデータ（VS_VERSIONINFO）を読む。
func parseVersionInfo(b []byte) (*versionInfo, error) {
	root, _, err := parseBlock(b)
	if err != nil {
		return nil, err
	}
	if root.key != "VS_VERSION_INFO" {
		return nil, fmt.Errorf("VS_VERSION_INFO ではありません（%q）", root.key)
	}
	if len(root.value) < 52 || binary.LittleEndian.Uint32(root.value) != 0xFEEF04BD {
		return nil, errors.New("VS_FIXEDFILEINFO がありません")
	}
	le := binary.LittleEndian
	vi := &versionInfo{Strings: map[string]string{}}
	split := func(ms, ls uint32) [4]uint16 {
		return [4]uint16{uint16(ms >> 16), uint16(ms), uint16(ls >> 16), uint16(ls)}
	}
	vi.FixedFile = split(le.Uint32(root.value[8:]), le.Uint32(root.value[12:]))
	vi.FixedProduct = split(le.Uint32(root.value[16:]), le.Uint32(root.value[20:]))
	tables := 0
	err = eachChild(root.children, func(c block) error {
		if c.key != "StringFileInfo" {
			return nil
		}
		return eachChild(c.children, func(t block) error {
			tables++
			if tables > 1 {
				return nil
			}
			return eachChild(t.children, func(s block) error {
				vi.Strings[s.key] = utf16String(s.value)
				return nil
			})
		})
	})
	if err != nil {
		return nil, err
	}
	if tables == 0 {
		return nil, errors.New("StringFileInfo がありません")
	}
	return vi, nil
}
