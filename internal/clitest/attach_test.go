package clitest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 受け入れ条件 5: 秘密らしい文字列を含むテキストのファイルを issue attach に渡すと、サーバに要求を送らずに止まる。
// 同じ行に秘密の無いファイルを並べても、そのファイルも送らない（全部確かめてから 1 つ目を送る）。
// 対照: 秘密の無いファイルだけなら要求が 1 件出る。マスク済みの形（token=***）は止めない。
func TestAttachSecretSendsNothing(t *testing.T) {
	impl := GoImpl(goBin(t))
	route := []Route{r("POST", pIssue1+"/attachments", created(attachRes(11, "notes.txt", "text/plain; charset=utf-8", 12)))}
	run := func(name string, args []string, seeds []Seed) Result {
		t.Helper()
		return Run(t, impl, Case{Name: "attach-secret/" + name, Args: append([]string{"attach", "DEMO-0001"}, args...), Seeds: seeds, Routes: route})
	}
	secret := Seed{Path: "ws/env.txt", Content: "ok\nexport TOKEN=abc123\n"}
	for _, c := range []struct {
		name  string
		args  []string
		seeds []Seed
	}{
		{"秘密だけ", []string{"env.txt"}, []Seed{secret}},
		{"秘密の無いファイルの後ろ", []string{"notes.txt", "env.txt"}, append([]Seed{secret}, attachSeeds...)},
		{"UTF-16 の秘密", []string{"env16.txt"}, []Seed{{Path: "ws/env16.txt", Content: utf16le("password=hunter2\n")}}},
	} {
		res := run(c.name, c.args, c.seeds)
		if len(res.Requests) != 0 || res.Code == 0 || !strings.Contains(res.Stderr, "秘密らしい文字列") {
			t.Errorf("%s: 要求 %d 件・exit %d・stderr %q", c.name, len(res.Requests), res.Code, res.Stderr)
		}
		if strings.Contains(res.Stderr+res.Stdout, "abc123") || strings.Contains(res.Stderr+res.Stdout, "hunter2") {
			t.Errorf("%s: 止めた理由に値が出た: %q", c.name, res.Stderr)
		}
	}
	// 対照: 秘密の無いテキスト・マスク済みの形は送る（要求 1 件）
	for _, c := range []struct {
		name string
		seed Seed
	}{
		{"秘密なし", attachSeeds[1]},
		{"マスク済み", Seed{Path: "ws/notes.txt", Content: "token=***\nPASS\n"}},
	} {
		res := run(c.name, []string{"notes.txt"}, []Seed{c.seed})
		if len(res.Requests) != 1 || res.Code != 0 || res.Requests[0].Method != "POST" || string(res.Requests[0].Body) != c.seed.Content {
			t.Errorf("前提が崩れています（%s のファイルが送られない）: 要求 %d 件・exit %d・stderr %q", c.name, len(res.Requests), res.Code, res.Stderr)
		}
	}
}

// utf16le は BOM 付きの UTF-16LE（Windows の PowerShell 5.1 の既定の出力）にする（ASCII だけを想定）。
func utf16le(s string) string {
	b := []byte{0xFF, 0xFE}
	for i := 0; i < len(s); i++ {
		b = append(b, s[i], 0)
	}
	return string(b)
}

// 書庫（gzip・zip・tar）は中を展開して要素ごとに同じ検査を当てる。当たれば 1 件も送らない。
// 上限（展開後の合計・要素の数）を超えた書庫は検査できないので送らない。対照: 中身が安全な書庫は送る（要求 1 件・本体はそのまま）。
func TestAttachArchiveSecretSendsNothing(t *testing.T) {
	impl := GoImpl(goBin(t))
	route := []Route{r("POST", pIssue1+"/attachments", created(attachRes(11, "logs.tgz", "application/gzip", 12)))}
	run := func(name, file, content string) Result {
		t.Helper()
		return Run(t, impl, Case{Name: "attach-archive/" + name, Args: []string{"attach", "DEMO-0001", file},
			Seeds: []Seed{{Path: "ws/" + file, Content: content}}, Routes: route})
	}
	secret := []byte("ok\nexport TOKEN=abc123\n")
	clean := []byte("PASS\nok  pkg\n")
	zeros := make([]byte, 65<<20)   // 展開後の合計の上限（64 MiB）を超える
	many := make([]tarEntry, 10001) // 要素の数の上限（10000）を超える
	for i := range many {
		many[i] = tarEntry{name: fmt.Sprintf("f%05d.txt", i)}
	}
	for _, c := range []struct {
		name, file string
		content    []byte
		send       bool
		want       string // 止まるときの理由の一部
	}{
		{"gz の秘密", "transcript.jsonl.gz", gzipOf(secret), false, "transcript.jsonl.gz の中の transcript.jsonl の 2 行目"},
		{"gz の安全なテキスト", "notes.txt.gz", gzipOf(clean), true, ""},
		{"gz の上限超え", "zeros.gz", gzipOf(zeros), false, "64 MiB を超える"},
		{"zip の秘密", "logs.zip", zipOf(tarEntry{"a/env.txt", secret}), false, "logs.zip の中の a/env.txt の 2 行目"},
		{"zip の安全な要素", "logs.zip", zipOf(tarEntry{"a/notes.txt", clean}), true, ""},
		{"zip の上限超え", "zeros.zip", zipOf(tarEntry{"zeros.bin", zeros}), false, "64 MiB を超える"},
		{"tgz の秘密", "logs.tgz", gzipOf(tarOf(tarEntry{"dir/env.txt", secret})), false, "logs.tgz の中の dir/env.txt の 2 行目"},
		{"tgz の安全な要素", "logs.tgz", gzipOf(tarOf(tarEntry{"dir/notes.txt", clean})), true, ""},
		{"tgz の要素の上限超え", "many.tgz", gzipOf(tarOf(many...)), false, "10000 個を超える"},
		{"tar の秘密", "logs.tar", tarOf(tarEntry{"dir/env.txt", secret}), false, "logs.tar の中の dir/env.txt の 2 行目"},
		{"tar の安全な要素", "logs.tar", tarOf(tarEntry{"dir/notes.txt", clean}), true, ""},
	} {
		res := run(c.name, c.file, string(c.content))
		out := res.Stderr + res.Stdout
		if strings.Contains(out, "abc123") {
			t.Errorf("%s: 出力に値が出た: %q", c.name, out)
		}
		if c.send {
			if len(res.Requests) != 1 || res.Code != 0 || string(res.Requests[0].Body) != string(c.content) {
				t.Errorf("前提が崩れています（%s が送られない）: 要求 %d 件・exit %d・stderr %q", c.name, len(res.Requests), res.Code, res.Stderr)
			}
			continue
		}
		if len(res.Requests) != 0 || res.Code == 0 || !strings.Contains(res.Stderr, c.want) || !strings.Contains(res.Stderr, "何も送らずに止めました") {
			t.Errorf("%s: 要求 %d 件・exit %d・stderr %q（期待 %q）", c.name, len(res.Requests), res.Code, res.Stderr, c.want)
		}
	}
}

// tarEntry は書庫の要素（名前と中身）。書庫を作る補助はメモリに書くだけなので、失敗は壊れた前提として panic にする。
type tarEntry struct {
	name string
	body []byte
}

func gzipOf(body []byte) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(body); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func zipOf(entries ...tarEntry) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		// 時刻を固定して中身を毎回同じにする（golden が本体の sha256 を持つため）
		f, err := w.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			panic(err)
		}
		if _, err := f.Write(e.body); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func tarOf(entries ...tarEntry) []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if err := w.WriteHeader(h); err != nil {
			panic(err)
		}
		if _, err := w.Write(e.body); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}
