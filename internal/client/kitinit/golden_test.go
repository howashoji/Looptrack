package kitinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// init の golden（AI 4 種 × loop の状態（なし・あり・remove）× dry-run）。
//
// golden には「意図して変わる部分」（配線のコマンド＝looptrack hook …・入口・許可・CLI の呼び方）の期待値がそのまま入る。
// 「以前の CLI（1.0.0 より前）と同じであるべき部分」（.looptrack-kit.json の構造・CLAUDE.md / AGENTS.md の案内節・
// skill と rules の置き場・loop の問いの文面・dry-run の差分の形・--remove-loop）も、この golden に入っている。
//
// kit のファイルをそのまま置くものは名前だけを載せ、AGENTS.md の loop 節の本文も maskLoopBlock がマスクするので、
// kit の文面を直しても golden に本文は出ない。出るのは行数で、AGENTS.md に写る節の行数が動くと
// ハンク見出しの数字が変わり、この golden が落ちる（どの節が写るかは 2 段構え。goldenHint を参照）。
// 更新: go test ./internal/client/kitinit -run TestInitGolden -update（KITINIT_UPDATE=1 でも同じ）。

// goldenHint は golden が食い違ったときに checkGolden が添える手掛かり。
// 本文はマスクされるので、差分だけを見ても kit/loop の rules が原因だとは分からない。
const goldenHint = "手掛かり: AGENTS.md に写る rules の節の行数が変わると、ここが落ちる。対象は 2 段構えで、" +
	"(1) 節の見出しの直後に注入の印 <!-- looptrack:inject session --> があり、" +
	"(2) その rules が kit/loop/manifest.json で \"agents_md\": \"sections\" を持つ、の両方を満たすものだけ。\n" +
	"印だけを持つ rules（manifest で codex・copilot とも null）は AGENTS.md に写らないので、触っても落ちない。" +
	"skill は frontmatter の description だけが写るので、本文を変えても落ちない見込み（未実測）。\n" +
	"本文は maskLoopBlock がマスクするので、差分にはハンク見出し（@@ -0,0 +1,N @@）の数字しか出ない。だから差分を見ても原因が分からない。\n" +
	"取り直し: go test ./internal/client/kitinit -run TestInitGolden -update（KITINIT_UPDATE=1 でも同じ）。" +
	"取り直したら、差分がハンク見出しの行数だけであることを読んで確かめる。"

var goldenAgents = []string{"claude-code", "codex", "copilot", "other"}

func TestInitGolden(t *testing.T) {
	for _, agent := range goldenAgents {
		for _, state := range []string{"none", "loop", "remove"} {
			for _, dry := range []bool{false, true} {
				name := agent + "/" + state
				if dry {
					name += "-dry-run"
				}
				t.Run(name, func(t *testing.T) {
					stubs(t, true)
					root := newRoot(t)
					base := []string{"--project", "demo", "--agent", agent}
					var args []string
					switch state {
					case "none":
						args = base
					case "loop":
						args = append(base, "--loop")
					case "remove":
						if agent != "other" { // 先に loop を入れる（golden には載せない）
							if r := runInit(t, root, "", append(base, "--loop")...); r.code != 0 {
								t.Fatalf("準備の init --loop が失敗: %d %s", r.code, r.stderr)
							}
						}
						args = []string{"--remove-loop"}
					}
					if dry {
						args = append(args, "--dry-run")
					}
					r := runInit(t, root, "", args...)
					title := "init " + strings.Join(args, " ")
					if state == "remove" && agent != "other" {
						title = "init " + strings.Join(append(base, "--loop"), " ") + " の後に " + title
					}
					got := golden(t, root, title, r)
					checkGolden(t, name, maskKitDiffs(got))
					if dry && state != "remove" {
						if ents, _ := os.ReadDir(filepath.Join(root, "ws")); len(ents) != 0 {
							t.Errorf("dry-run なのに書いた: %v", ents)
						}
					}
				})
			}
		}
	}
}

// maskKitDiffs は dry-run の「作成」の差分のうち、kit のファイルの全文を作るもの（本文は kit の文面）を 1 行に縮める。
func maskKitDiffs(s string) string {
	files := embedded()
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "      --- /dev/null") {
			continue
		}
		j := i + 1
		var body []string
		for j < len(lines) && strings.HasPrefix(lines[j], "      ") {
			if l := strings.TrimPrefix(lines[j], "      "); strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++ ") {
				body = append(body, l[1:])
			}
			j++
		}
		if k := kitSource(files, strings.Join(body, "\n")+"\n"); k != "" {
			out = append(out, "      （"+k+" の全文を作成する差分。省略）")
			i = j - 1
		}
	}
	return strings.Join(out, "\n")
}
