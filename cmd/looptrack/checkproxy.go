package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/proxycheck"
	"github.com/howashoji/looptrack/internal/store"
)

// checkProxyTimeout は確認 1 回の締切（32MiB を自分の公開 URL へ送り切るまで）。
// checkProxyContinueWait は 100 Continue を待つ長さ（返さない前段には、待った後に本文を送る）。
var (
	checkProxyTimeout      = 2 * time.Minute
	checkProxyContinueWait = 10 * time.Second
)

// checkProxyCmd は looptrack check-proxy [--url <基点>]。前段のリバースプロキシが会話記録の 1 チャンクの既定の上限
// （store.DefaultTranscriptMaxChunk）を通すかを、公開の URL（LOOPTRACK_PUBLIC_URL + LOOPTRACK_BASE_PATH。.env と同じ変数）へ
// 送って確かめる（internal/proxycheck）。知らせるだけなので、確かめられなくても足りなくても終了コードは 0
// （install.sh の起動の後に呼ぶ。止めない）。引数の誤りだけが 2。
func checkProxyCmd(args []string, stdio cli.IO, e env.Env) int {
	lang := i18n.FromEnv(e.Get)
	fs := flag.NewFlagSet("check-proxy", flag.ContinueOnError)
	fs.SetOutput(stdio.Stderr)
	base := fs.String("url", "", i18n.T(lang, "cli.check_proxy.arg.url"))
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stdio.Stderr, i18n.T(lang, "cli.check_proxy.usage"))
		return 2
	}
	size := store.DefaultTranscriptMaxChunk
	var target string
	var err error
	if *base != "" {
		target, err = proxycheck.Target(*base, "")
	} else {
		bp := e.Get("LOOPTRACK_BASE_PATH")
		if bp == "" {
			bp = defaultBasePath
		}
		target, err = proxycheck.Target(e.Get("LOOPTRACK_PUBLIC_URL"), bp)
	}
	if err != nil {
		fmt.Fprintln(stdio.Stdout, i18n.T(lang, "cli.check_proxy.no_public_url", "env", "LOOPTRACK_PUBLIC_URL"))
		return 0
	}
	r := proxycheck.Check(context.Background(), proxycheck.NewClient(checkProxyTimeout, checkProxyContinueWait), target, size)
	printCheckProxy(stdio.Stdout, lang, r, size)
	return 0
}

// printCheckProxy は判定を 1 行で出す。
func printCheckProxy(w io.Writer, lang i18n.Lang, r proxycheck.Result, size int64) {
	mib := size >> 20
	switch r.Verdict {
	case proxycheck.Enough:
		fmt.Fprintln(w, i18n.T(lang, "cli.check_proxy.enough", "url", r.URL, "size", mib, "status", r.Status))
	case proxycheck.TooSmall:
		fmt.Fprintln(w, i18n.T(lang, "cli.check_proxy.too_small", "url", r.URL, "size", mib))
	default:
		reason := i18n.T(lang, "cli.check_proxy.reason.status", "status", r.Status)
		if r.Err != nil {
			reason = r.Err.Error()
		}
		fmt.Fprintln(w, i18n.T(lang, "cli.check_proxy.unknown", "url", r.URL, "size", mib, "reason", reason))
	}
}
