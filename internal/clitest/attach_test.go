package clitest

import (
	"strings"
	"testing"
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
