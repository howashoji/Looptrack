package main

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
)

// TestRunDispatchesTimeline は、main の振り分け（run）が `looptrack timeline` を timeline のサブコマンドへ渡すことを確かめる。
// 振り分けの名前を取り違えると「知らないサブコマンド」で止まる（対照: 本当に知らない名前はそう出る）。
func TestRunDispatchesTimeline(t *testing.T) {
	vars := map[string]string{"LOOPTRACK_LANG": "ja"}
	runIt := func(args ...string) (int, string) {
		var out, errb strings.Builder
		code := run(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(vars))
		return code, out.String() + errb.String()
	}
	if code, text := runIt("no-such-subcommand-xyz"); code == 0 || !strings.Contains(text, "知らないサブコマンドです") {
		t.Fatalf("前提が崩れています（知らない名前の扱い）: %d %q", code, text)
	}
	code, text := runIt("timeline", "-h")
	if strings.Contains(text, "知らないサブコマンドです") || !strings.Contains(text, "looptrack timeline") {
		t.Fatalf("timeline が振り分けられていません: %d %q", code, text)
	}
}
