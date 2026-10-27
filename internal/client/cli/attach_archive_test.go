package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 秘密の値（止めた理由に出てはいけない）。
const archiveSecretValue = "abc123secret"

var archiveSecretText = "ok\nexport TOKEN=" + archiveSecretValue + "\n"

type archiveMember struct {
	name string
	body []byte
}

func gzipBytes(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Name = name
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zipBytes(t *testing.T, members ...archiveMember) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, m := range members {
		f, err := w.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(m.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func tarBytes(t *testing.T, members ...archiveMember) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, m := range members {
		if err := w.WriteHeader(&tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(m.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// v7Tar は ustar の印を持たない古い形の tar にする（印を消し、見出しの検査和を数え直す）。
func v7Tar(t *testing.T, members ...archiveMember) []byte {
	t.Helper()
	b := tarBytes(t, members...)
	h := b[:512]
	for i := 257; i < 265; i++ {
		h[i] = 0
	}
	for i := 148; i < 156; i++ {
		h[i] = ' '
	}
	sum := 0
	for _, c := range h {
		sum += int(c)
	}
	copy(h[148:156], fmt.Sprintf("%06o\x00 ", sum))
	if string(b[257:262]) == "ustar" {
		t.Fatal("前提が崩れています: ustar の印が残っている")
	}
	return b
}

// errID は i18n.Error の ID（nil なら空）。
func errID(err error) string {
	var ie *i18n.Error
	if errors.As(err, &ie) {
		return ie.Msg.ID
	}
	if err != nil {
		return "(not i18n) " + err.Error()
	}
	return ""
}

func TestCheckAttachFileArchives(t *testing.T) {
	secret := []byte(archiveSecretText)
	clean := []byte("PASS\nok  pkg\n")
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x00")
	utf16 := []byte{0xFF, 0xFE}
	for _, c := range "ok\npassword=hunter2\n" {
		utf16 = append(utf16, byte(c), 0)
	}
	for _, c := range []struct {
		name   string
		path   string
		data   []byte
		id     string // 空なら通る
		member string // 理由に出る中のパス
	}{
		// 対照: 書庫でないテキストの秘密は止まり、画像は検査せずに通る
		{"テキストの秘密", "env.txt", secret, "cli.err.attach_secret", ""},
		{"画像", "shot.png", png, "", ""},
		{"gz の秘密", "transcript.jsonl.gz", gzipBytes(t, "", secret), "cli.err.attach_secret_member", "transcript.jsonl"},
		{"gz の見出しの名前", "x.gz", gzipBytes(t, "env.txt", secret), "cli.err.attach_secret_member", "env.txt"},
		{"gz の UTF-16 の秘密", "env16.txt.gz", gzipBytes(t, "", utf16), "cli.err.attach_secret_member", "env16.txt"},
		{"gz の安全なテキスト", "notes.txt.gz", gzipBytes(t, "", clean), "", ""},
		{"gz の中の画像", "shot.png.gz", gzipBytes(t, "", png), "", ""},
		{"zip の秘密", "logs.zip", zipBytes(t, archiveMember{"a/notes.txt", clean}, archiveMember{"a/env.txt", secret}), "cli.err.attach_secret_member", "a/env.txt"},
		{"zip の安全な要素", "logs.zip", zipBytes(t, archiveMember{"a/notes.txt", clean}, archiveMember{"a/shot.png", png}), "", ""},
		{"空の zip", "empty.zip", zipBytes(t), "", ""},
		{"tar の秘密", "logs.tar", tarBytes(t, archiveMember{"notes.txt", clean}, archiveMember{"dir/env.txt", secret}), "cli.err.attach_secret_member", "dir/env.txt"},
		{"tar の安全な要素", "logs.tar", tarBytes(t, archiveMember{"notes.txt", clean}), "", ""},
		{"拡張子の無い tar（印で見分ける）", "logs", tarBytes(t, archiveMember{"env.txt", secret}), "cli.err.attach_secret_member", "env.txt"},
		{"tgz の秘密", "logs.tgz", gzipBytes(t, "", tarBytes(t, archiveMember{"dir/env.txt", secret})), "cli.err.attach_secret_member", "dir/env.txt"},
		{"tar.gz の秘密", "logs.tar.gz", gzipBytes(t, "", tarBytes(t, archiveMember{"dir/env.txt", secret})), "cli.err.attach_secret_member", "dir/env.txt"},
		{"tgz の安全な要素", "logs.tgz", gzipBytes(t, "", tarBytes(t, archiveMember{"notes.txt", clean})), "", ""},
		{"v7 の tar（.tar の拡張子）", "old.tar", v7Tar(t, archiveMember{"env.txt", secret}), "cli.err.attach_secret_member", "env.txt"},
		// 対照: 拡張子が .tar でなく印も無ければ、ふつうのバイナリとして検査せずに通す
		{"v7 の tar（拡張子なし）", "old.bin", v7Tar(t, archiveMember{"env.txt", secret}), "", ""},
		{"zip の中の gz の秘密（2 重）", "logs.zip", zipBytes(t, archiveMember{"app.log.gz", gzipBytes(t, "", secret)}), "cli.err.attach_secret_member", "app.log.gz/app.log"},
		{"tar の中の zip の秘密（2 重）", "logs.tar", tarBytes(t, archiveMember{"in.zip", zipBytes(t, archiveMember{"env.txt", secret})}), "cli.err.attach_secret_member", "in.zip/env.txt"},
		{"tgz の中の gz（3 重）", "logs.tgz", gzipBytes(t, "", tarBytes(t, archiveMember{"app.log.gz", gzipBytes(t, "", clean)})), "cli.err.attach_archive_too_deep", "app.log.gz"},
		{"zip の中の tgz（3 重）", "logs.zip", zipBytes(t, archiveMember{"in.tgz", gzipBytes(t, "", tarBytes(t, archiveMember{"notes.txt", clean}))}), "cli.err.attach_archive_too_deep", "in.tgz"},
		{"壊れた gz", "broken.gz", gzipBytes(t, "", secret)[:20], "cli.err.attach_archive_broken", ""},
	} {
		err := checkAttachFile(c.path, c.data)
		if got := errID(err); got != c.id {
			t.Errorf("%s: 理由 %q（期待 %q）", c.name, got, c.id)
			continue
		}
		if err == nil {
			continue
		}
		for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
			text := i18n.Text(lang, err)
			if strings.Contains(text, archiveSecretValue) || strings.Contains(text, "hunter2") {
				t.Errorf("%s: 止めた理由に値が出た: %q", c.name, text)
			}
			if !strings.Contains(text, c.path) {
				t.Errorf("%s: 理由に書庫のパスが無い: %q", c.name, text)
			}
			if c.member != "" && !strings.Contains(text, c.member) {
				t.Errorf("%s: 理由に中のパス %q が無い: %q", c.name, c.member, text)
			}
		}
		if c.id == "cli.err.attach_secret_member" {
			if text := i18n.Text(i18n.JA, err); !strings.Contains(text, "2 行目") || !strings.Contains(text, "TOKEN=") && !strings.Contains(text, "password=") {
				t.Errorf("%s: 理由に行と規則の語が無い: %q", c.name, text)
			}
		}
	}
}

// 上限を超えた書庫は「検査できないので送らない」で止まる。申告の大きさを確保せず、枠を超えたところで読むのをやめる。
func TestCheckAttachFileArchiveLimits(t *testing.T) {
	zeros := make([]byte, 1<<20)
	oneMiB := gzipBytes(t, "", zeros)

	// gzip の爆弾: 1 MiB の 0 を圧縮したものを 2048 個つなぐ（展開すると 2 GiB。gzip は続けて読む）
	bomb := bytes.Repeat(oneMiB, 2048)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	err := checkAttachFile("bomb.gz", bomb)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if errID(err) != "cli.err.attach_archive_too_large" {
		t.Fatalf("gzip の爆弾: 理由 %q", errID(err))
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("gzip の爆弾（%d バイト・展開すると 2 GiB）: %v・確保 %d MiB", len(bomb), elapsed, alloc>>20)
	if alloc > 512<<20 {
		t.Errorf("gzip の爆弾で %d MiB を確保した（枠は %d MiB）", alloc>>20, attachArchiveMaxExpanded>>20)
	}
	if text := i18n.Text(i18n.JA, err); !strings.Contains(text, "64 MiB") || !strings.Contains(text, "何も送らずに止めました") {
		t.Errorf("gzip の爆弾の理由: %q", text)
	}

	// 対照: 枠ちょうどまでは検査して通す（境界）
	atLimit := bytes.Repeat(oneMiB, attachArchiveMaxExpanded>>20)
	if err := checkAttachFile("limit.gz", atLimit); err != nil {
		t.Errorf("枠ちょうどの gz が止まった: %q", errID(err))
	}

	// zip の爆弾: 申告の大きさが枠を超える要素（開く前に止まる）
	big := zipBytes(t, archiveMember{"zeros.txt", make([]byte, attachArchiveMaxExpanded+1)})
	if got := errID(checkAttachFile("bomb.zip", big)); got != "cli.err.attach_archive_too_large" {
		t.Errorf("zip の爆弾: 理由 %q", got)
	}

	// 申告を偽った zip（小さく申告して大きく展開する）: 枠や申告を超えて読まずに止まる
	var raw bytes.Buffer
	zw := zip.NewWriter(&raw)
	var deflated bytes.Buffer
	fw, _ := flate.NewWriter(&deflated, flate.BestSpeed)
	_, _ = fw.Write(make([]byte, 8<<20))
	_ = fw.Close()
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "zeros.txt", Method: zip.Deflate, CompressedSize64: uint64(deflated.Len()), UncompressedSize64: 10})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(deflated.Bytes())
	_ = zw.Close()
	liar := checkAttachFile("liar.zip", raw.Bytes())
	t.Logf("申告を偽った zip: %s", i18n.Text(i18n.EN, liar))
	if got := errID(liar); got != "cli.err.attach_archive_broken" && got != "cli.err.attach_archive_too_large" {
		t.Errorf("申告を偽った zip: 理由 %q", got)
	}

	// 要素の数: 上限 + 1 個の tar（tgz）と zip は止まり、上限ちょうどは通る
	many := func(n int) []archiveMember {
		out := make([]archiveMember, n)
		for i := range out {
			out[i] = archiveMember{name: "f" + strconv.Itoa(i), body: nil}
		}
		return out
	}
	if got := errID(checkAttachFile("many.tgz", gzipBytes(t, "", tarBytes(t, many(attachArchiveMaxMembers+1)...)))); got != "cli.err.attach_archive_too_many" {
		t.Errorf("要素の多い tgz: 理由 %q", got)
	}
	if got := errID(checkAttachFile("many.zip", zipBytes(t, many(attachArchiveMaxMembers+1)...))); got != "cli.err.attach_archive_too_many" {
		t.Errorf("要素の多い zip: 理由 %q", got)
	}
	if err := checkAttachFile("max.tar", tarBytes(t, many(attachArchiveMaxMembers)...)); err != nil {
		t.Errorf("要素が上限ちょうどの tar が止まった: %q", errID(err))
	}
}
