// gen はデスクトップ版の**仮の**アイコン（単純な図形）を作る。
//
//	go run ./internal/client/desktop/icon/gen            # internal/client/desktop/icon/ に書き出す
//
// 書き出すもの（差し替えるときは、同じ名前・同じ形式のファイルを置けばよい。このプログラムを使わなくてよい）:
//
//	app.png            1024×1024 のアプリのアイコン（Linux の AppImage・.desktop は 256 に縮めたもの app_256.png を使う）
//	app_256.png        256×256
//	app.icns           macOS の .app（Contents/Resources/Looptrack.icns）。PNG を入れた icns（16〜1024・@2x）
//	app.ico            Windows の .exe に埋め込む（16・32・48・256。PNG を入れた ico）
//	tray.png           トレイ（Linux）・色つき 64×64
//	tray.ico           トレイ（Windows）・16・32・48
//	tray_template.png  メニューバー（macOS のテンプレート画像。黒と透明だけ・44×44＝22pt の @2x）
//
// 図形: 角の丸い四角（藍色）に、白い輪（ループ）と矢じり。公開名・意匠が決まったら差し替える。
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "internal/client/desktop/icon", "書き出す先")
	flag.Parse()
	bg := color.NRGBA{0x3B, 0x5B, 0xDB, 0xFF}
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	black := color.NRGBA{0, 0, 0, 0xFF}
	app := func(n int) []byte { return encode(render(n, &bg, white, 0.22)) }
	trayColor := func(n int) []byte { return encode(render(n, &bg, white, 0.30)) }
	write := func(name string, b []byte) {
		p := filepath.Join(*out, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s（%d バイト）\n", p, len(b))
	}
	write("app.png", app(1024))
	write("app_256.png", app(256))
	write("app.icns", icns(app))
	write("app.ico", ico(app, 16, 32, 48, 256))
	write("tray.png", trayColor(64))
	write("tray.ico", ico(trayColor, 16, 32, 48))
	write("tray_template.png", encode(render(44, nil, black, 0)))
}

// render は n×n の画像を描く。bg が nil なら背景は透明（テンプレート画像）。4×4 の超標本化で縁を滑らかにする。
func render(n int, bg *color.NRGBA, fg color.NRGBA, radius float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	const ss = 4
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var cover, fgCover float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(x) + (float64(sx)+0.5)/ss) / float64(n)
					v := (float64(y) + (float64(sy)+0.5)/ss) / float64(n)
					inBG := bg == nil || roundRect(u, v, 0.04, radius)
					if inBG {
						cover++
					}
					if inBG && loop(u, v, bg == nil) {
						fgCover++
					}
				}
			}
			cover /= ss * ss
			fgCover /= ss * ss
			var c color.NRGBA
			if bg == nil {
				c = color.NRGBA{fg.R, fg.G, fg.B, uint8(math.Round(fgCover * 255))}
			} else if cover > 0 {
				t := fgCover / cover
				c = color.NRGBA{
					R: mix(bg.R, fg.R, t), G: mix(bg.G, fg.G, t), B: mix(bg.B, fg.B, t),
					A: uint8(math.Round(cover * 255)),
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func mix(a, b uint8, t float64) uint8 { return uint8(math.Round(float64(a)*(1-t) + float64(b)*t)) }

// roundRect は余白 m・角の半径 r（どちらも 1 辺に対する割合）の角の丸い四角の中か。
func roundRect(u, v, m, r float64) bool {
	if u < m || v < m || u > 1-m || v > 1-m {
		return false
	}
	cx := math.Max(m+r-u, math.Max(0, u-(1-m-r)))
	cy := math.Max(m+r-v, math.Max(0, v-(1-m-r)))
	return cx*cx+cy*cy <= r*r
}

// loop は輪（切れ目つき）と矢じりの中か。template はメニューバー用（余白が無いので少し大きく描く）。
func loop(u, v float64, template bool) bool {
	scale := 1.0
	if template {
		scale = 1.35
	}
	x, y := (u-0.5)*2/scale, (v-0.5)*2/scale // 中心 0・半径 1 の座標
	r := math.Hypot(x, y)
	a := math.Atan2(-y, x) * 180 / math.Pi // 右が 0 度、反時計回り
	if a < 0 {
		a += 360
	}
	const outer, inner = 0.56, 0.36
	const gapFrom, gapTo = 20.0, 70.0 // 右上に切れ目
	if r >= inner && r <= outer && (a < gapFrom || a > gapTo) {
		return true
	}
	// 矢じり: 切れ目の終わり（70 度）の先に、時計回りに向く三角形
	mid := (outer + inner) / 2
	rad := gapTo * math.Pi / 180
	tip := [2]float64{mid * math.Cos(rad-0.55), -mid * math.Sin(rad-0.55)}
	b1 := [2]float64{(outer + 0.13) * math.Cos(rad), -(outer + 0.13) * math.Sin(rad)}
	b2 := [2]float64{(inner - 0.13) * math.Cos(rad), -(inner - 0.13) * math.Sin(rad)}
	return inTriangle([2]float64{x, y}, tip, b1, b2)
}

func inTriangle(p, a, b, c [2]float64) bool {
	s := func(p1, p2, p3 [2]float64) float64 {
		return (p1[0]-p3[0])*(p2[1]-p3[1]) - (p2[0]-p3[0])*(p1[1]-p3[1])
	}
	d1, d2, d3 := s(p, a, b), s(p, b, c), s(p, c, a)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// ico は PNG を入れた .ico（Windows Vista 以降）。
func ico(f func(int) []byte, sizes ...int) []byte {
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, f(s))
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := uint8(s)
		if s >= 256 {
			w = 0 // 256 は 0 で表す
		}
		b.Write([]byte{w, w, 0, 0})
		binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, img := range imgs {
		b.Write(img)
	}
	return b.Bytes()
}

// icns は PNG を入れた .icns（macOS 10.7 以降）。
func icns(f func(int) []byte) []byte {
	entries := []struct {
		typ  string
		size int
	}{
		{"icp4", 16}, {"icp5", 32}, {"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024},
		{"ic11", 32}, {"ic12", 64}, {"ic13", 256}, {"ic14", 512},
	}
	var body bytes.Buffer
	for _, e := range entries {
		data := f(e.size)
		body.WriteString(e.typ)
		binary.Write(&body, binary.BigEndian, uint32(8+len(data)))
		body.Write(data)
	}
	var b bytes.Buffer
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, uint32(8+body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}
