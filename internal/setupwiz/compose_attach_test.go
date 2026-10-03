package setupwiz

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	composeAttachRe = regexp.MustCompile(`(?m)^ {6}LOOPTRACK_ATTACH_DIR: (\S+)$`)
	composeVolumeRe = regexp.MustCompile(`(?m)^ {6}- (\./[^:\s]+):(/[^:\s]+)(:ro)?$`)
)

// composeAttachMount は compose.yaml の LOOPTRACK_ATTACH_DIR が、書ける volume（:ro でない）の中にあるかを確かめる。
// 置き場の値と、その置き場を入れている volume のホスト側を返す。read_only のコンテナで書けるのは volume の中だけなので、
// volume の外を指していれば添付は書けず、コンテナを作り直すと消える。
func composeAttachMount(compose string) (dir, host string, ok bool) {
	m := composeAttachRe.FindStringSubmatch(compose)
	if m == nil {
		return "", "", false
	}
	dir = m[1]
	for _, v := range composeVolumeRe.FindAllStringSubmatch(compose, -1) {
		if v[3] == "" && (dir == v[2] || strings.HasPrefix(dir, v[2]+"/")) {
			return dir, v[1], true
		}
	}
	return dir, "", false
}

// setup の compose.yaml は、SQLite でも MySQL でも添付の置き場を書ける volume の中に決め、ホスト側のディレクトリを作る。
// MySQL の compose.yaml は以前は read_only で書ける volume を持たず、添付の本体を置く場所が無かった。
func TestComposeAttachDirIsOnWritableVolume(t *testing.T) {
	presets := map[string]Preset{
		"sqlite": {Mode: "team", Store: "sqlite", PublicURL: "http://im.lan:8090", AdminPassword: pw, TwoFactor: "optional"},
		"mysql": {Mode: "team", Store: "mysql", DSN: "im_app:pw@tcp(mysql:3306)/im?parseTime=true",
			MigrateDSN: "root:rootpw@tcp(127.0.0.1:3306)/im?parseTime=true", PublicURL: "https://im.example.com/",
			AdminPassword: pw, TwoFactor: "required"},
	}
	for store, pre := range presets {
		t.Run(store, func(t *testing.T) {
			dir := t.TempDir()
			if _, out, err := run(t, Options{Dir: dir, Yes: true, Preset: pre, Backend: &fakeBackend{}}); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			b, err := os.ReadFile(filepath.Join(dir, ComposeFile))
			if err != nil {
				t.Fatal(err)
			}
			compose := string(b)
			got, host, ok := composeAttachMount(compose)
			if !ok || got != attachContainerDir || host != "./data" {
				t.Fatalf("添付の置き場 = %q（volume のホスト側 %q・書ける volume の中 %v）。want %q を ./data の中に:\n%s",
					got, host, ok, attachContainerDir, compose)
			}
			if !strings.Contains(compose, "    read_only: true\n") {
				t.Errorf("read_only が外れた（置き場は volume だけで書けるようにする）:\n%s", compose)
			}
			if fi, err := os.Stat(filepath.Join(dir, "data")); err != nil || !fi.IsDir() {
				t.Errorf("ホストの ./data が無い（Docker が root の持ち物で作り、uid 65534 のコンテナが書けない）: %v", err)
			}
			if env := envOf(t, dir); env["LOOPTRACK_ATTACH_DIR"] != "" {
				t.Errorf("compose の置き場は compose.yaml に書く（.env に書くと volume の無い compose.yaml でも置き場が決まってしまう）: %q", env["LOOPTRACK_ATTACH_DIR"])
			}

			// 対照: ./data の volume を外す・読み取り専用にすると、同じ判定が「書ける volume の外」と答える
			noData := strings.Replace(compose, "      - ./data:/data\n", "", 1)
			roData := strings.Replace(compose, "      - ./data:/data\n", "      - ./data:/data:ro\n", 1)
			for name, c := range map[string]string{"volume なし": noData, "読み取り専用": roData} {
				if c == compose {
					t.Fatalf("前提が崩れています（%s の対照を作れない。./data の行の形が変わった）", name)
				}
				if _, _, ok := composeAttachMount(c); ok {
					t.Errorf("前提が崩れています（%s でも書ける volume の中と判定した）", name)
				}
			}
		})
	}
}
