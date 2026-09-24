package kitinit

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/hook/core"
	"github.com/howashoji/looptrack/internal/client/hook/loop"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/client/selfupdate"
	"github.com/howashoji/looptrack/internal/client/verify"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/relver"
)

// looptrack doctor（DESIGN.md §5-11 の Q2「解決できなければ手元専用の設定に絶対パス（looptrack doctor で確かめる）」）。
//
//	looptrack doctor [--dir <プロジェクト>] [--offline]
//
// 確かめること（読むだけ。何も書かない・サーバに何も送らない。版の確認だけ GET /api/v1/dist を読む）:
//  1. 実行中の looptrack の版・場所
//  2. PATH で looptrack が解決できるか・解決したものが実行中のものと同じか・その版（looptrack version）
//  3. プロジェクトの配線（.claude/settings.json・settings.local.json・.codex/hooks.json・.github/hooks/looptrack.json（旧名 im.json も））:
//     looptrack hook の実行ファイル（PATH の名前なら PATH に・絶対パスならそのファイルがあるか）・hook の名前・--agent
//  4. .claude/.looptrack-kit.json（置き方・loop）と API の設定（URL・トークンの有無）
//  5. 配布の最新の版（--offline でなく、URL とトークンがあるとき）
//
// 問題（NG）が 1 つでもあれば終了コード 1。注意だけなら 0。

// DoctorOptions は doctor の実行の設定。
type DoctorOptions struct {
	Env     env.Env
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
	Lang    i18n.Lang // 画面に出す文面の言語（cmd/looptrack が環境から決めて渡す）
}

type doctorReport struct {
	w       io.Writer
	lang    i18n.Lang
	ng, att int
}

// ok・note・bad は 1 行の判定。行頭の見出しは 6 文字ぶんにそろえる（対訳表の値に余白を含める）。
func (r *doctorReport) ok(text string) {
	fmt.Fprintf(r.w, "%s%s\n", i18n.T(r.lang, "kitinit.doctor.label.ok"), text)
}
func (r *doctorReport) note(text string) {
	r.att++
	fmt.Fprintf(r.w, "%s%s\n", i18n.T(r.lang, "kitinit.doctor.label.note"), text)
}
func (r *doctorReport) bad(text string) {
	r.ng++
	fmt.Fprintf(r.w, "%s%s\n", i18n.T(r.lang, "kitinit.doctor.label.ng"), text)
}

// Doctor は looptrack doctor の本体（終了コードを返す）。
func Doctor(args []string, o DoctorOptions) int {
	dir, offline := "", false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--dir" && i+1 < len(args):
			i++
			dir = args[i]
		case strings.HasPrefix(a, "--dir="):
			dir = strings.TrimPrefix(a, "--dir=")
		case a == "--offline":
			offline = true
		case a == "-h" || a == "--help":
			fmt.Fprint(o.Stdout, i18n.T(o.Lang, "kitinit.doctor.usage"))
			return 0
		default:
			fmt.Fprint(o.Stderr, i18n.T(o.Lang, "kitinit.doctor.err.unknown_arg", "arg", a, "usage", i18n.T(o.Lang, "kitinit.doctor.usage")))
			return 2
		}
	}
	if dir == "" {
		dir = o.Env.Get("CLAUDE_PROJECT_DIR")
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(o.Stderr, i18n.T(o.Lang, "kitinit.doctor.err.no_wd", "reason", err))
			return 1
		}
		dir = gitRoot(wd)
	}
	r := &doctorReport{w: o.Stdout, lang: o.Lang}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. 実行中
	exe, _ := executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	r.ok(i18n.T(o.Lang, "kitinit.doctor.running", "version", o.Version, "os", runtime.GOOS, "arch", runtime.GOARCH, "path", orDash(exe)))

	// 2. PATH
	inPath := false
	if p, err := lookPath("looptrack"); err != nil || p == "" {
		r.note(i18n.T(o.Lang, "kitinit.doctor.path.missing"))
	} else {
		inPath = true
		real := p
		if x, err := filepath.EvalSymlinks(p); err == nil {
			real = x
		}
		out, code, err := runCmd(ctx, dir, cleanEnv(o.Lang), p, "version")
		ver := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		switch {
		case err != nil || code != 0:
			r.bad(i18n.T(o.Lang, "kitinit.doctor.path.err.run", "path", p, "reason", err, "code", code))
		case exe != "" && !sameFile(real, exe):
			r.note(i18n.T(o.Lang, "kitinit.doctor.path.other_file", "path", p, "version", ver, "running", exe))
		default:
			r.ok(i18n.T(o.Lang, "kitinit.doctor.path.ok", "path", p, "version", ver))
		}
	}

	// 2-2. 検証コマンドのシェル（Windows だけ）
	if show, good, msg := verifyShellCheck(o.Lang, runtime.GOOS, lookPath, o.Env.Get, fileExists); show {
		if good {
			r.ok(msg)
		} else {
			r.note(msg)
		}
	}

	// 3. 配線
	r.ok(i18n.T(o.Lang, "kitinit.doctor.project", "dir", dir))
	wired := 0
	for _, f := range []struct{ rel, agent string }{
		{".claude/settings.json", "claude-code"}, {".claude/settings.local.json", "claude-code"},
		{".codex/hooks.json", "codex"}, {copilotHooks, "copilot"}, {legacyCopilotHooks, "copilot"},
	} {
		fp := filepath.Join(dir, filepath.FromSlash(f.rel))
		if !isFile(fp) {
			continue
		}
		entries, err := readEntries(fp, f.agent == "copilot")
		if err != nil {
			r.bad(i18n.T(o.Lang, "kitinit.doctor.err.read_file", "file", f.rel, "reason", err))
			continue
		}
		n := 0
		for _, e := range entries {
			if c := classify(e.cmd); c.kind == kindOther {
				continue
			}
			n++
			m := newCmd.FindStringSubmatch(strings.TrimSpace(e.cmd))
			if m == nil {
				continue
			}
			bin, name, agent := m[1], m[2], m[3]
			if !knownHook(name) {
				r.bad(i18n.T(o.Lang, "kitinit.doctor.wire.unknown_hook", "file", f.rel, "cmd", e.cmd))
			}
			if agent != f.agent && !(f.agent == "claude-code" && agent == "copilot") {
				r.note(i18n.T(o.Lang, "kitinit.doctor.wire.agent_mismatch", "file", f.rel, "agent", f.agent, "cmd", e.cmd))
			}
			switch {
			case bin == "looptrack" && !inPath:
				r.bad(i18n.T(o.Lang, "kitinit.doctor.wire.not_in_path", "file", f.rel, "hook", name))
			case bin != "looptrack":
				p := strings.Trim(bin, `"'`)
				if !isFile(p) {
					r.bad(i18n.T(o.Lang, "kitinit.doctor.wire.missing_bin", "file", f.rel, "hook", name, "path", p))
				}
			}
		}
		wired += n
		if n > 0 {
			r.ok(i18n.T(o.Lang, "kitinit.doctor.wire.count", "file", f.rel, "count", n))
		}
	}
	if wired == 0 {
		r.note(i18n.T(o.Lang, "kitinit.doctor.wire.none"))
	}

	// 4. .looptrack-kit.json と API の設定
	if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(kitJSON))); err == nil {
		if data, err := jsonorder.DecodeObject(b); err == nil {
			lo := data.Object("loop")
			installed, _ := func() (any, bool) {
				if lo == nil {
					return nil, false
				}
				return lo.Get("installed")
			}()
			ls := i18n.T(o.Lang, "kitinit.doctor.kit.loop_unset")
			if installed == true {
				ls = i18n.T(o.Lang, "kitinit.doctor.kit.loop_on", "version", orDash(lo.String("version")))
			} else if lo != nil && lo.String("declined_at") != "" {
				ls = i18n.T(o.Lang, "kitinit.doctor.kit.loop_declined")
			}
			r.ok(i18n.T(o.Lang, "kitinit.doctor.kit.ok", "file", kitJSON, "project", orDash(data.String("project")),
				"source", orDash(data.String("source")), "loop", ls))
		} else {
			r.bad(i18n.T(o.Lang, "kitinit.doctor.err.read_file", "file", kitJSON, "reason", err))
		}
	} else if wired > 0 {
		r.note(i18n.T(o.Lang, "kitinit.doctor.kit.missing", "file", kitJSON))
	}
	cl, err := api.New(o.Env)
	if err != nil {
		r.bad(i18n.T(o.Lang, "kitinit.doctor.api.err.config", "reason", err))
		return r.finish(o.Stdout)
	}
	if cl.BaseURL == "" {
		r.note(i18n.T(o.Lang, "kitinit.doctor.api.no_url", "env", env.Name(env.APIURL)))
		return r.finish(o.Stdout)
	}
	tok, from, _ := cl.TokenSource(cl.BaseURL)
	switch {
	case tok != "":
		r.ok(i18n.T(o.Lang, "kitinit.doctor.api.token", "url", cl.BaseURL, "from", from))
	case cl.LocalMode(cl.BaseURL): // ローカルモードのサーバは認証を省くのでトークンは要らない
		r.ok(i18n.T(o.Lang, "kitinit.doctor.api.local", "url", cl.BaseURL))
	default:
		r.note(i18n.T(o.Lang, "kitinit.doctor.api.no_token", "url", cl.BaseURL))
		return r.finish(o.Stdout)
	}

	// 5. 配布の最新
	if offline {
		return r.finish(o.Stdout)
	}
	l, err := selfupdate.FetchListing(cl)
	if err != nil {
		r.note(i18n.T(o.Lang, "kitinit.doctor.dist.err.listing", "reason", err))
		return r.finish(o.Stdout)
	}
	b := l.For(runtime.GOOS, runtime.GOARCH)
	switch {
	case b == nil:
		r.note(i18n.T(o.Lang, "kitinit.doctor.dist.none", "os", runtime.GOOS, "arch", runtime.GOARCH))
	case relver.Older(o.Version, b.Version):
		r.note(i18n.T(o.Lang, "kitinit.doctor.dist.outdated", "latest", b.Version, "local", o.Version))
	default:
		r.ok(i18n.T(o.Lang, "kitinit.doctor.dist.latest", "latest", b.Version, "local", o.Version))
	}
	return r.finish(o.Stdout)
}

func (r *doctorReport) finish(w io.Writer) int {
	switch {
	case r.ng > 0:
		fmt.Fprintf(w, "\n%s\n", i18n.T(r.lang, "kitinit.doctor.result.bad", "ng", r.ng, "note", r.att))
		return 1
	case r.att > 0:
		fmt.Fprintf(w, "\n%s\n", i18n.T(r.lang, "kitinit.doctor.result.note", "note", r.att))
	default:
		fmt.Fprintf(w, "\n%s\n", i18n.T(r.lang, "kitinit.doctor.result.ok"))
	}
	return 0
}

// verifyShellCheck は「検証コマンドを渡すシェルがあるか」の検査（Windows だけ）。
// Windows の looptrack issue verify は bash でしか動かない（cmd.exe・PowerShell には落とさない。
// POSIX の書き方のコマンドを別の意味で動かさないため。internal/client/verify の WindowsShell）。
// 無いと「## 検証コマンド」節のあるイシューを閉じられないプロジェクトがあるので、閉じようとする前に知らせる。
// NG ではなく注意にする（verify を使わない運用もある）。OS と探し方を引数で渡し、どの OS でも単体テストできるようにする。
func verifyShellCheck(lang i18n.Lang, goos string, lookPath func(string) (string, error), getenv func(string) string,
	exists func(string) bool) (show, good bool, msg string) {
	if goos != "windows" {
		return false, true, "" // macOS・Linux は bash か sh が必ず見つかる（verify/run_unix.go）
	}
	if p, err := verify.WindowsShell(lookPath, getenv, exists); err == nil {
		return true, true, i18n.T(lang, "kitinit.doctor.verify_shell.ok", "path", p)
	}
	return true, false, i18n.T(lang, "kitinit.doctor.verify_shell.missing")
}

// fileExists はファイル（ディレクトリでない）があるか。
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func knownHook(name string) bool {
	if _, ok := core.Lookup(name); ok {
		return true
	}
	_, ok := loop.Lookup(name)
	return ok
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// gitRoot は dir から上へ .git を探す（無ければ dir）。
func gitRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}
