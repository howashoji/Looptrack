package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"reflect"
	"testing"
)

// icnsEntry は icns の中の 1 件。
type icnsEntry struct {
	typ  string
	data []byte
}

func parseIcns(t *testing.T, b []byte) []icnsEntry {
	t.Helper()
	if len(b) < 8 || string(b[:4]) != "icns" || int(binary.BigEndian.Uint32(b[4:8])) != len(b) {
		t.Fatalf("icns の見出しが正しくない（%d バイト）", len(b))
	}
	var out []icnsEntry
	for o := 8; o < len(b); {
		if o+8 > len(b) {
			t.Fatalf("icns の項目の見出しが途中で切れている（offset %d）", o)
		}
		l := int(binary.BigEndian.Uint32(b[o+4 : o+8]))
		if l < 8 || o+l > len(b) {
			t.Fatalf("icns の項目 %q の長さ %d が不正", b[o:o+4], l)
		}
		out = append(out, icnsEntry{string(b[o : o+4]), b[o+8 : o+l]})
		o += l
	}
	return out
}

// unpackBits は icns の RLE を n バイト分ほどく。使ったバイト数も返す。
func unpackBits(t *testing.T, p []byte, n int) (out []byte, used int) {
	t.Helper()
	for len(out) < n {
		if used >= len(p) {
			t.Fatalf("RLE が途中で終わっている（%d/%d バイト）", len(out), n)
		}
		c := p[used]
		used++
		if c < 0x80 {
			k := int(c) + 1
			if used+k > len(p) {
				t.Fatalf("RLE の生データが足りない")
			}
			out = append(out, p[used:used+k]...)
			used += k
		} else {
			if used >= len(p) {
				t.Fatalf("RLE の繰り返しの値が無い")
			}
			k := int(c) - 0x80 + 3
			for i := 0; i < k; i++ {
				out = append(out, p[used])
			}
			used++
		}
	}
	if len(out) != n {
		t.Fatalf("RLE をほどいた長さ %d が面の大きさ %d を超えた", len(out), n)
	}
	return out, used
}

// decodeARGB は ARGB 形式（ic04・ic05）を n×n の画像にほどく。R・G・B は格納された値（プリマルチプライ）のまま入れる。
func decodeARGB(t *testing.T, data []byte, n int) *image.NRGBA {
	t.Helper()
	if len(data) < 4 || string(data[:4]) != "ARGB" {
		t.Fatalf("ARGB 形式の印が無い: % x", data[:min(4, len(data))])
	}
	p := data[4:]
	var planes [4][]byte
	for i := range planes {
		var used int
		planes[i], used = unpackBits(t, p, n*n)
		p = p[used:]
	}
	if len(p) != 0 {
		t.Fatalf("ARGB の面の後に %d バイト余っている", len(p))
	}
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for i := 0; i < n*n; i++ {
		img.SetNRGBA(i%n, i/n, color.NRGBA{R: planes[1][i], G: planes[2][i], B: planes[3][i], A: planes[0][i]})
	}
	return img
}

func appImage(n int) *image.NRGBA {
	bg := color.NRGBA{0x3B, 0x5B, 0xDB, 0xFF}
	return render(n, &bg, color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}, 0.22)
}

// premulImage は画像の R・G・B に A を掛ける（ARGB 形式に格納される値）。
func premulImage(img *image.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	for i := 0; i < len(img.Pix); i += 4 {
		a := img.Pix[i+3]
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = premul(img.Pix[i], a), premul(img.Pix[i+1], a), premul(img.Pix[i+2], a), a
	}
	return out
}

// closePremul は A が一致し、A を掛けた R・G・B の差が 1 以内であることを確かめる。
// ARGB 形式は 8 ビットのプリマルチプライなので、半透明の画素は読み戻しで 1 段階ずれうる（元の値には戻せない）。
func closePremul(t *testing.T, what string, got, want *image.NRGBA) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("%s: 大きさが違う %v / %v", what, got.Bounds(), want.Bounds())
	}
	for i := 0; i < len(want.Pix); i += 4 {
		if got.Pix[i+3] != want.Pix[i+3] {
			t.Fatalf("%s: 画素 %d の A が違う（%d、期待 %d）", what, i/4, got.Pix[i+3], want.Pix[i+3])
		}
		for c := 0; c < 3; c++ {
			g := float64(got.Pix[i+c]) * float64(got.Pix[i+3]) / 255
			w := float64(want.Pix[i+c]) * float64(want.Pix[i+3]) / 255
			if g-w > 1 || w-g > 1 {
				t.Fatalf("%s: 画素 %d の色 %d が違う（%d、期待 %d、A=%d）", what, i/4, c, got.Pix[i+c], want.Pix[i+c], want.Pix[i+3])
			}
		}
	}
}

func samePixels(t *testing.T, what string, got, want *image.NRGBA) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("%s: 大きさが違う %v / %v", what, got.Bounds(), want.Bounds())
	}
	if !bytes.Equal(got.Pix, want.Pix) {
		for i := range want.Pix {
			if got.Pix[i] != want.Pix[i] {
				t.Fatalf("%s: 画素が違う（Pix[%d] が %d、期待 %d）", what, i, got.Pix[i], want.Pix[i])
			}
		}
	}
}

// checkIcns は icns の型の一覧と、全項目の画素が appImage と一致することを確かめる。
func checkIcns(t *testing.T, b []byte) {
	t.Helper()
	wantTypes := []string{"ic04", "ic05", "ic07", "ic08", "ic09", "ic10", "ic11", "ic12", "ic13", "ic14"}
	sizes := map[string]int{"ic04": 16, "ic05": 32, "ic07": 128, "ic08": 256, "ic09": 512, "ic10": 1024, "ic11": 32, "ic12": 64, "ic13": 256, "ic14": 512}
	entries := parseIcns(t, b)
	var types []string
	for _, e := range entries {
		types = append(types, e.typ)
	}
	// icp4・icp5 は macOS が PNG ではなく旧形式の生データとして読み、16・32 がノイズになる。入れてはいけない。
	if !reflect.DeepEqual(types, wantTypes) {
		t.Fatalf("icns の型の一覧が違う: %v\n期待: %v", types, wantTypes)
	}
	for _, e := range entries {
		n := sizes[e.typ]
		var got *image.NRGBA
		switch e.typ {
		case "ic04", "ic05":
			// 格納された値は A を掛けたもの。元の画素に A を掛けた値と完全に一致する。
			samePixels(t, e.typ, decodeARGB(t, e.data, n), premulImage(appImage(n)))
			continue
		default:
			img, err := png.Decode(bytes.NewReader(e.data))
			if err != nil {
				t.Fatalf("%s の PNG をほどけない: %v", e.typ, err)
			}
			got = image.NewNRGBA(img.Bounds())
			for y := 0; y < n; y++ {
				for x := 0; x < n; x++ {
					got.Set(x, y, img.At(x, y))
				}
			}
		}
		samePixels(t, e.typ, got, appImage(n))
	}
}

func TestIcnsSmallSizesAreARGB(t *testing.T) {
	checkIcns(t, icns(appImage))
	// 独立の確かめ: 上の端の半透明の画素（A=64 の藍色）は、A を掛けた値（15・23・55）で入る。
	// 掛けずに入れると macOS は色を A で割って白く浮かせる。
	for _, e := range parseIcns(t, icns(appImage)) {
		if e.typ != "ic04" {
			continue
		}
		p := decodeARGB(t, e.data, 16).NRGBAAt(3, 0)
		if p != (color.NRGBA{R: 15, G: 23, B: 55, A: 64}) {
			t.Errorf("ic04 の (3,0) = %v、期待 {15 23 55 64}", p)
		}
	}
}

// コミット済みの app.icns が、いまの図形の画素と一致する（バイト列は Go の版で変わりうるので画素で見る）。
func TestCommittedIcnsMatchesGenerator(t *testing.T) {
	b, err := os.ReadFile("../app.icns")
	if err != nil {
		t.Fatal(err)
	}
	checkIcns(t, b)
}

func TestPackBitsRoundTrip(t *testing.T) {
	cases := [][]byte{
		{7},
		{1, 1},
		{1, 1, 1},
		{1, 2, 2, 3, 3, 3, 4},
		bytes.Repeat([]byte{9}, 300),
		append(bytes.Repeat([]byte{5}, 131), 1, 2),
	}
	var long []byte
	for i := 0; i < 400; i++ {
		long = append(long, byte(i*7))
	}
	cases = append(cases, long)
	for _, c := range cases {
		got, used := unpackBits(t, packBits(c), len(c))
		if !bytes.Equal(got, c) || used != len(packBits(c)) {
			t.Errorf("RLE の往復が合わない: %v", c)
		}
	}
}
