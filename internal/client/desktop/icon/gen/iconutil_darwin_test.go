package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// macOS の iconutil（Finder と同じ読み手）でほどいた 10 枚が、元の画素と一致する。
func TestIcnsReadableByIconutil(t *testing.T) {
	iconutil, err := exec.LookPath("iconutil")
	if err != nil {
		t.Fatalf("macOS なのに iconutil が見つからない: %v", err)
	}
	dir := t.TempDir()
	icnsPath := filepath.Join(dir, "x.icns")
	if err := os.WriteFile(icnsPath, icns(appImage), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(iconutil, "-c", "iconset", "-o", filepath.Join(dir, "x.iconset"), icnsPath).CombinedOutput(); err != nil {
		t.Fatalf("iconutil: %v\n%s", err, out)
	}
	files := map[string]int{
		"icon_16x16.png": 16, "icon_16x16@2x.png": 32, "icon_32x32.png": 32, "icon_32x32@2x.png": 64,
		"icon_128x128.png": 128, "icon_128x128@2x.png": 256, "icon_256x256.png": 256, "icon_256x256@2x.png": 512,
		"icon_512x512.png": 512, "icon_512x512@2x.png": 1024,
	}
	for name, n := range files {
		b, err := os.ReadFile(filepath.Join(dir, "x.iconset", name))
		if err != nil {
			t.Errorf("%s: iconutil がほどいた中に無い: %v", name, err)
			continue
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Errorf("%s: PNG ではない: %v", name, err)
			continue
		}
		got := image.NewNRGBA(img.Bounds())
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				got.Set(x, y, img.At(x, y))
			}
		}
		if n == 16 || name == "icon_32x32.png" {
			// ic04・ic05（ARGB 形式）は半透明の縁が 1 段階ずれうる。それ以外の 8 枚（PNG）は完全に一致する。
			closePremul(t, name, got, appImage(n))
		} else {
			samePixels(t, name, got, appImage(n))
		}
	}
}
