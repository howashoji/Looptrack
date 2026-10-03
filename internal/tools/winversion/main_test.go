package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestNumericVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v1.0.0":                "1.0.0",
		"v1.0.0-rc.5":           "1.0.0",
		"1.2.3":                 "1.2.3",
		"v9.8.7-rc.1":           "9.8.7",
		"1.2":                   "1.2",
		"v1.2.3+meta":           "1.2.3",
		"20261002-abc1234":      "0.0.0",
		"20260925-ddc984df1eb4": "0.0.0",
		"v70000.0.0":            "0.0.0",
		"v1":                    "0.0.0",
		"v1.2.3.4.5":            "0.0.0",
	} {
		if got := numericVersion(in); got != want {
			t.Errorf("numericVersion(%q) = %q, 期待 %q", in, got, want)
		}
	}
}

func TestWinresJSON(t *testing.T) {
	b, err := winresJSON("v1.0.0-rc.5", "looptrack.exe", false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]map[string]struct {
		Fixed map[string]string            `json:"fixed"`
		Info  map[string]map[string]string `json:"info"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["RT_GROUP_ICON"]; ok {
		t.Error("アイコンを渡していないのに RT_GROUP_ICON があります")
	}
	v := m["RT_VERSION"]["#1"]["0000"]
	if v.Fixed["product_version"] != "1.0.0" || v.Fixed["file_version"] != "1.0.0" {
		t.Errorf("固定部 = %v", v.Fixed)
	}
	s := v.Info["0409"]
	if s["ProductName"] != "Looptrack" || s["ProductVersion"] != "v1.0.0-rc.5" || s["OriginalFilename"] != "looptrack.exe" {
		t.Errorf("文字列 = %v", s)
	}
	// 対照: アイコンを渡せば入る
	b, err = winresJSON("v1.0.0", "Looptrack.exe", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"RT_GROUP_ICON"`) || !strings.Contains(string(b), `"app.ico"`) {
		t.Errorf("アイコンを渡したのに RT_GROUP_ICON がありません:\n%s", b)
	}
}

// --- テスト用の VS_VERSIONINFO と .syso を作る（道具の本体とは別の書き方で組む） ---

func u16z(s string) []byte {
	var b bytes.Buffer
	for _, c := range utf16.Encode([]rune(s)) {
		_ = binary.Write(&b, binary.LittleEndian, c)
	}
	b.Write([]byte{0, 0})
	return b.Bytes()
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

// vsBlock は 1 段を組む。text の値は UTF-16 の文字列（valueLen は文字の数）。
func vsBlock(key string, value []byte, valueLen int, text bool, children ...[]byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0, 0}) // wLength は後で埋める
	_ = binary.Write(&b, binary.LittleEndian, uint16(valueLen))
	typ := uint16(0)
	if text {
		typ = 1
	}
	_ = binary.Write(&b, binary.LittleEndian, typ)
	b.Write(u16z(key))
	pad4(&b)
	b.Write(value)
	for _, c := range children {
		pad4(&b)
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out, uint16(len(out)))
	return out
}

func vsString(key, val string) []byte {
	v := u16z(val)
	return vsBlock(key, v, len(v)/2, true)
}

func versionResource(fixed [4]uint16, strs map[string]string) []byte {
	var ff bytes.Buffer
	le := binary.LittleEndian
	ms := uint32(fixed[0])<<16 | uint32(fixed[1])
	ls := uint32(fixed[2])<<16 | uint32(fixed[3])
	for _, v := range []uint32{0xFEEF04BD, 0x00010000, ms, ls, ms, ls, 0x3f, 0, 0x40004, 1, 0, 0, 0} {
		_ = binary.Write(&ff, le, v)
	}
	var strBlocks [][]byte
	for _, k := range []string{"CompanyName", "FileDescription", "OriginalFilename", "ProductName", "ProductVersion"} {
		if v, ok := strs[k]; ok {
			strBlocks = append(strBlocks, vsString(k, v))
		}
	}
	table := vsBlock("040904b0", nil, 0, true, strBlocks...)
	sfi := vsBlock("StringFileInfo", nil, 0, true, table)
	var trans bytes.Buffer
	_ = binary.Write(&trans, le, uint32(0x04b00409))
	vfi := vsBlock("VarFileInfo", nil, 0, true, vsBlock("Translation", trans.Bytes(), 4, false))
	return vsBlock("VS_VERSION_INFO", ff.Bytes(), ff.Len(), false, sfi, vfi)
}

// sysoAMD64 は RT_VERSION を 1 つだけ持つ COFF のオブジェクト（.rsrc）を作る。データの項目の RVA は
// IMAGE_REL_AMD64_ADDR32NB の再配置で、リンカがセクションの位置を足す。
func sysoAMD64(ver []byte) []byte {
	le := binary.LittleEndian
	var r bytes.Buffer
	dir := func(ids ...[2]uint32) {
		_ = binary.Write(&r, le, [4]uint32{0, 0, 0, uint32(len(ids)) << 16}) // 特性・時刻・版・名前 0 件 + ID の件数
		for _, e := range ids {
			_ = binary.Write(&r, le, e)
		}
	}
	// ルート（16 + 8）→ 種類 16（24 から）→ 名前 1（48 から）→ 言語 0x409 のデータの項目（72 から）→ データ（88 から）
	dir([2]uint32{16, 0x80000000 | 24})
	dir([2]uint32{1, 0x80000000 | 48})
	dir([2]uint32{0x409, 72})
	_ = binary.Write(&r, le, [4]uint32{88, uint32(len(ver)), 0, 0}) // OffsetToData は再配置の加数
	r.Write(ver)
	pad4(&r)
	data := r.Bytes()

	var o bytes.Buffer
	const hdr, sect = 20, 40
	relocOff := hdr + sect + len(data)
	symOff := relocOff + 10
	_ = binary.Write(&o, le, uint16(0x8664))
	_ = binary.Write(&o, le, uint16(1))
	_ = binary.Write(&o, le, uint32(0))
	_ = binary.Write(&o, le, uint32(symOff))
	_ = binary.Write(&o, le, uint32(1))
	_ = binary.Write(&o, le, uint16(0))
	_ = binary.Write(&o, le, uint16(0))
	o.WriteString(".rsrc\x00\x00\x00")
	_ = binary.Write(&o, le, [6]uint32{0, 0, uint32(len(data)), hdr + sect, uint32(relocOff), 0})
	_ = binary.Write(&o, le, [2]uint16{1, 0})
	_ = binary.Write(&o, le, uint32(0x40000040)) // 初期化済みのデータ・読み取り
	o.Write(data)
	_ = binary.Write(&o, le, uint32(72)) // 再配置: データの項目の OffsetToData
	_ = binary.Write(&o, le, uint32(0))  // シンボル 0（.rsrc）
	_ = binary.Write(&o, le, uint16(3))  // IMAGE_REL_AMD64_ADDR32NB
	o.WriteString(".rsrc\x00\x00\x00")
	_ = binary.Write(&o, le, uint32(0))
	_ = binary.Write(&o, le, int16(1))
	_ = binary.Write(&o, le, uint16(0))
	o.Write([]byte{3, 0})               // IMAGE_SYM_CLASS_STATIC・補助 0
	_ = binary.Write(&o, le, uint32(4)) // 文字列表（空）
	return o.Bytes()
}

// buildExe は小さな Windows（amd64）の exe を作る。syso があればパッケージに置いてリンクさせる。
func buildExe(t *testing.T, syso []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module winversiontest\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if syso != nil {
		if err := os.WriteFile(filepath.Join(dir, "rsrc_windows_amd64.syso"), syso, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(dir, "x.exe")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return exe
}

func goodStrings(version string) map[string]string {
	return map[string]string{
		"CompanyName":      "Looptrack",
		"FileDescription":  "Looptrack",
		"OriginalFilename": "x.exe",
		"ProductName":      "Looptrack",
		"ProductVersion":   version,
	}
}

func TestCheckReadsVersionFromExe(t *testing.T) {
	if testing.Short() {
		t.Skip("exe をビルドする")
	}
	exe := buildExe(t, sysoAMD64(versionResource([4]uint16{1, 0, 0, 0}, goodStrings("v1.0.0-rc.5"))))
	var out bytes.Buffer
	if err := run([]string{"check", "-version", "v1.0.0-rc.5", exe}, &out); err != nil {
		t.Fatalf("版の情報が正しい exe で失敗しました: %v", err)
	}
	if !strings.Contains(out.String(), "ProductName=Looptrack ProductVersion=v1.0.0-rc.5 固定部=1.0.0.0") {
		t.Errorf("出力 = %q", out.String())
	}
	// 値が違えば落ちる（同じ exe を別の版で照合する）
	err := run([]string{"check", "-version", "v1.0.1", exe}, &out)
	if err == nil || !strings.Contains(err.Error(), `ProductVersion が "v1.0.0-rc.5"`) {
		t.Errorf("版が違うのに落ちませんでした: %v", err)
	}
}

func TestCheckFailsOnWrongProductName(t *testing.T) {
	if testing.Short() {
		t.Skip("exe をビルドする")
	}
	s := goodStrings("20261002-abc1234")
	s["ProductName"] = "Other"
	exe := buildExe(t, sysoAMD64(versionResource([4]uint16{0, 0, 0, 0}, s)))
	err := run([]string{"check", "-version", "20261002-abc1234", exe}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `ProductName が "Other"`) {
		t.Errorf("ProductName が違うのに落ちませんでした: %v", err)
	}
	// 固定部の版が規則と違えば落ちる（試しの版は 0.0.0）
	exe = buildExe(t, sysoAMD64(versionResource([4]uint16{2026, 0, 0, 0}, goodStrings("20261002-abc1234"))))
	err = run([]string{"check", "-version", "20261002-abc1234", exe}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "固定部の ProductVersion が 2026.0.0.0") {
		t.Errorf("固定部が違うのに落ちませんでした: %v", err)
	}
	// 対照: 正しい値なら通る（上の 2 つが「何でも落ちる」のではないこと）
	exe = buildExe(t, sysoAMD64(versionResource([4]uint16{0, 0, 0, 0}, goodStrings("20261002-abc1234"))))
	if err := run([]string{"check", "-version", "20261002-abc1234", exe}, &bytes.Buffer{}); err != nil {
		t.Errorf("前提が崩れています: 正しい試しの版の exe で失敗しました: %v", err)
	}
}

func TestCheckFailsWithoutVersionInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("exe をビルドする")
	}
	exe := buildExe(t, nil)
	err := run([]string{"check", "-version", "v1.0.0", exe}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "版の情報が入っていない") {
		t.Errorf("版の情報が無い exe で落ちませんでした: %v", err)
	}
	// Windows の exe でないものも落ちる
	notPE := filepath.Join(t.TempDir(), "x.exe")
	if err := os.WriteFile(notPE, []byte("not a pe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check", "-version", "v1.0.0", notPE}, &bytes.Buffer{}); err == nil {
		t.Error("PE でないファイルで落ちませんでした")
	}
}

func TestParseVersionInfoRejectsBrokenData(t *testing.T) {
	good := versionResource([4]uint16{1, 2, 3, 0}, goodStrings("v1.2.3"))
	vi, err := parseVersionInfo(good)
	if err != nil {
		t.Fatalf("前提が崩れています: 正しいデータを読めません: %v", err)
	}
	if vi.Strings["ProductVersion"] != "v1.2.3" || vi.FixedProduct != [4]uint16{1, 2, 3, 0} {
		t.Errorf("読んだ値 = %+v", vi)
	}
	for name, b := range map[string][]byte{
		"空":    nil,
		"途中まで": good[:40],
		"鍵が違う": vsBlock("OTHER", good[40:92], 52, false),
	} {
		if _, err := parseVersionInfo(b); err == nil {
			t.Errorf("%s: 落ちませんでした", name)
		}
	}
	noStrings := vsBlock("VS_VERSION_INFO", good[40:92], 52, false)
	if _, err := parseVersionInfo(noStrings); err == nil {
		t.Error("StringFileInfo が無いのに落ちませんでした")
	}
}
