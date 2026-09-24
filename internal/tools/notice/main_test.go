package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsLicenseName(t *testing.T) {
	for name, want := range map[string]bool{
		"LICENSE": true, "license": true, "LICENSE.txt": true, "License.md": true, "LICENCE": true,
		"COPYING": true, "COPYING.LESSER": true, "NOTICE": true, "notice.txt": true,
		"LICENSE-MIT": true, "LICENSE-APACHE.txt": true, "LICENSE_SQLITE": true,
		"README.md": false, "licenses.go": false, "LICENSES": false, "go.mod": false, "noticeboard.go": false,
	} {
		if got := isLicenseName(name); got != want {
			t.Errorf("isLicenseName(%q) = %v、期待は %v", name, got, want)
		}
	}
}

func TestEscapeModulePath(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/go-sql-driver/mysql": "github.com/go-sql-driver/mysql",
		"github.com/Azure/go-autorest":   "github.com/!azure/go-autorest",
		"rsc.io/QUOTE":                   "rsc.io/!q!u!o!t!e",
		"v1.2.3-RC1":                     "v1.2.3-!r!c1",
	} {
		if got := escapeModulePath(in); got != want {
			t.Errorf("escapeModulePath(%q) = %q、期待は %q", in, got, want)
		}
	}
}

func TestRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/xuri/excelize/v2":        "https://github.com/xuri/excelize",
		"github.com/godbus/dbus/v5":          "https://github.com/godbus/dbus",
		"github.com/yosida95/uritemplate/v3": "https://github.com/yosida95/uritemplate",
		"github.com/go-sql-driver/mysql":     "https://github.com/go-sql-driver/mysql",
		"modernc.org/sqlite":                 "https://modernc.org/sqlite",
		"filippo.io/edwards25519":            "https://filippo.io/edwards25519",
		// /v1・/v0 と、数字でない接尾辞は外さない（本当のディレクトリのことがある）
		"example.com/m/v1":    "https://example.com/m/v1",
		"example.com/m/vpath": "https://example.com/m/vpath",
	} {
		if got := repoURL(in); got != want {
			t.Errorf("repoURL(%q) = %q、期待は %q", in, got, want)
		}
	}
}

// MPL-2.0 のモジュールには、ソースの入手先の案内が付くこと。
func TestSourceLines(t *testing.T) {
	d := dep{path: "github.com/go-sql-driver/mysql", version: "v1.10.1", mpl: true}
	got := strings.Join(sourceLines(d), "\n")
	for _, want := range []string{
		"Source: https://github.com/go-sql-driver/mysql",
		"Source (this version): https://proxy.golang.org/github.com/go-sql-driver/mysql/@v/v1.10.1.zip",
		"MPL-2.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sourceLines に %q がありません:\n%s", want, got)
		}
	}
	if lines := sourceLines(dep{path: "example.com/m"}); len(lines) != 1 {
		t.Errorf("版が分からないときは proxy の URL を書かない: %v", lines)
	}
}

func TestNormalize(t *testing.T) {
	if got := normalize("\n\nA  \r\nB\t\r\n\n"); got != "A\nB" {
		t.Errorf("normalize = %q", got)
	}
}

// loadComponents は、desktop.sh の runtime の版と一覧の runtime.tag が合っていることと、
// ライセンス文の写しが記録の SHA-256 と合っていることを確かめる（取り直し忘れを見つける）。
func TestLoadComponents(t *testing.T) {
	const text = "LICENSE TEXT\n"
	sum := hex.EncodeToString(sha256Sum([]byte(text)))
	manifest := func(tag, fileSum string) string {
		return `{"runtime":{"tag":"` + tag + `","relinking":"RELINKING.md",
			"corresponding_source":{"status":"planned","url":"https://example.com/src"}},
			"components":[{"name":"libfuse","version":"3.15.0","license":"LGPL-2.1-only",
			"source_tarball":{"url":"https://example.com/fuse.tar.xz","sha256":"00"},
			"license_files":[{"file":"LGPL-2.1.txt","sha256":"` + fileSum + `"}]}]}`
	}
	newRoot := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		lic := filepath.Join(dir, licensesDir)
		if err := os.MkdirAll(lic, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"LGPL-2.1.txt":            text,
			"RELINKING.md":            "how to relink\n",
			"runtime-components.json": body,
		} {
			if err := os.WriteFile(filepath.Join(lic, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	man, err := loadComponents(newRoot(t, manifest("20251108", sum)), "20251108")
	if err != nil {
		t.Fatalf("合っているのに失敗しました: %v", err)
	}
	if len(man.Components) != 1 || man.Components[0].LicenseFiles[0].text != normalize(text) {
		t.Errorf("読み込みの結果が違います: %+v", man)
	}

	// runtime の版が desktop.sh と違えば失敗する（部品の一覧の取り直し忘れ）
	if _, err := loadComponents(newRoot(t, manifest("20251108", sum)), "20260101"); err == nil {
		t.Error("runtime の版が違うのに通りました")
	} else if !strings.Contains(err.Error(), "APPIMAGE_RUNTIME_TAG") {
		t.Errorf("版の違いだと分かるエラーにしてください: %v", err)
	}

	// 写しを差し替えたのに SHA-256 を直し忘れたら失敗する
	if _, err := loadComponents(newRoot(t, manifest("20251108", strings.Repeat("0", 64))), "20251108"); err == nil {
		t.Error("写しの SHA-256 が違うのに通りました")
	} else if !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("ハッシュの違いだと分かるエラーにしてください: %v", err)
	}
}
