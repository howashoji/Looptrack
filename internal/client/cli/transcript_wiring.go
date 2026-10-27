package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// 会話記録の自動送信の配線の有無（DESIGN.md §9-8「hook」）。
//
// 送る hook（looptrack hook transcript の Stop・SessionEnd）は init が配線するときにだけ入る。それより前の版の init で
// 入れたプロジェクトは、CLI だけを上げても配線が無いままで、鍵（端末のオプトインとルール session.transcripts）が両方とも
// 入でも何も送られない。導入が古いかの判定は CLI の版と kit/core の控えで見るので、この欠けは拾えない。だから要約で
// 設定ファイルの配線を読んで知らせる。kitinit の配線の読み書きは使えない（kitinit が cli を使う側なので循環する）。

// transcriptWiringFiles は AI ごとの、配線を置く設定ファイル（Claude Code は PATH に無い端末で settings.local.json に書く）。
// Copilot は会話記録の搬送の対象外なので見ない。
var transcriptWiringFiles = []struct {
	agent string
	files []string
}{
	{"claude-code", []string{".claude/settings.json", ".claude/settings.local.json"}},
	{"codex", []string{".codex/hooks.json"}},
}

// looptrackHookCmd は `looptrack hook <名前> --agent <AI>` の形のコマンド（前置の環境変数・引用した絶対パスも含む）。
var looptrackHookCmd = regexp.MustCompile(`looptrack[^\s"'/\\]*["']? hook ([a-z0-9-]+) --agent [a-z-]+`)

// transcriptUnwired は、looptrack の hook を配線してあるのに会話記録を送る hook が Stop・SessionEnd の
// どちらかに無い AI の名前を返す。agent が空なら Claude Code と Codex の両方を、そうでなければその AI だけを見る。
// looptrack の hook が 1 つも無い AI（その AI には導入していない）と、読めない設定ファイルは数えない。
func transcriptUnwired(root, agent string) []string {
	var out []string
	for _, g := range transcriptWiringFiles {
		if agent != "" && agent != g.agent {
			continue
		}
		wired := false
		events := map[string]bool{}
		for _, rel := range g.files {
			for ev, names := range hookNamesByEvent(filepath.Join(root, filepath.FromSlash(rel))) {
				for _, n := range names {
					wired = true
					if n == "transcript" {
						events[ev] = true
					}
				}
			}
		}
		if wired && (!events["Stop"] || !events["SessionEnd"]) {
			out = append(out, g.agent)
		}
	}
	return out
}

// hookNamesByEvent は設定ファイルの hooks のイベントごとの looptrack の hook の名前（無い・読めなければ nil）。
// 形は Claude Code と Codex で同じ（hooks.<イベント>[].hooks[].command）。
func hookNamesByEvent(fp string) map[string][]string {
	b, err := os.ReadFile(fp)
	if err != nil {
		return nil
	}
	data, err := jsonorder.DecodeObject(b)
	if err != nil {
		return nil
	}
	hooks := data.Object("hooks")
	if hooks == nil {
		return nil
	}
	out := map[string][]string{}
	for _, ev := range hooks.Keys() {
		groups, _ := hooks.Get(ev)
		list, _ := groups.([]any)
		for _, g := range list {
			gobj, ok := g.(*jsonorder.Object)
			if !ok {
				continue
			}
			hv, _ := gobj.Get("hooks")
			hl, _ := hv.([]any)
			for _, h := range hl {
				ho, ok := h.(*jsonorder.Object)
				if !ok {
					continue
				}
				cmd, _ := ho.Get("command")
				if m := looptrackHookCmd.FindStringSubmatch(jsonorder.Str(cmd)); m != nil {
					out[ev] = append(out[ev], m[1])
				}
			}
		}
	}
	return out
}

// transcriptRewireCommand は配線をやり直す init のコマンド。サーバ版の導入（控えの置き方が server）では PATH に
// looptrack が無いことがあり、--url の無い init は手元のローカルモードに向くので、実行中の looptrack の絶対パスに
// --url と --source server を付ける（kitinit の init のやり直しの案内と同じ形）。
func transcriptRewireCommand(e env.Env, root, project string, agents []string) string {
	bin := "looptrack"
	source, _ := installFiles(root)
	if source == "server" {
		if exe, err := executableFn(); err == nil && exe != "" {
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			bin = `"` + exe + `"`
			if runtime.GOOS == "windows" {
				bin = "& '" + strings.ReplaceAll(exe, "'", "''") + "'"
			}
		}
	}
	cmd := bin + " issue init --project " + project
	if source == "server" {
		if u := strings.TrimSuffix(strings.TrimRight(api.NormalizeURL(e.Value(env.APIURL)), "/"), "/api/v1"); u != "" {
			cmd += " --url " + u
		}
	}
	cmd += " --agent " + strings.Join(agents, ",")
	if source == "server" {
		cmd += " --source server"
	}
	return cmd
}
