package loop

import "testing"

func TestPreToolGitGuard(t *testing.T) {
	run := func(cmd string, env map[string]string) func(*sandbox) call {
		return func(s *sandbox) call {
			e := map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")}
			for k, v := range env {
				e[k] = v
			}
			return call{hook: "pre-tool-git-guard", env: e,
				input: jsonInput(map[string]any{"session_id": "s1", "cwd": s.p("proj"), "hook_event_name": "PreToolUse",
					"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})}
		}
	}
	b := func(cmd string) func(*sandbox) call { return run(cmd, nil) }
	// bp は sandbox の絶対パスを埋めたコマンド（ほかの作業ツリーを指す `-C` など）。
	bp := func(f func(*sandbox) string) func(*sandbox) call {
		return func(s *sandbox) call { return b(f(s))(s) }
	}
	ask, quiet, deny := wantAsk, wantQuiet, wantDeny

	scenario{
		name: "巻き込みと取り返しのつかない git の操作を止める",
		// proj = いまの作業ツリー。other = 同じプロジェクトの中に置いた別の作業ツリー（この運用の置き方）。
		// outside = プロジェクトの外。パスの実在で checkout の引数を見分けるので、実物のファイルを置く。
		setup: func(s *sandbox) {
			s.mkdir("proj", ".git")
			s.write(s.p("proj", "internal", "client", "hook", "loop", "gates.go"), "package loop\n")
			s.mkdir("proj", ".claude", "worktrees", "other", ".git")
			s.write(s.p("proj", ".claude", "worktrees", "other", "x.go"), "package x\n")
			s.mkdir("outside", ".git")
		},
		steps: []step{
			// ── 巻き込み（取り返しがつくので ask のまま）────────────────────────────
			{name: "git add -A", mk: b("git add -A"), want: ask},
			{name: "git add --all", mk: b("git add --all"), want: ask},
			{name: "git add .", mk: b("git add ."), want: ask},
			{name: "git add :/", mk: b("git add :/"), want: ask},
			{name: "git -C <同じ作業ツリーの中> add -A（本体のオプションの後ろ）", mk: b("git -C internal add -A"), want: ask},
			{name: "git commit -a", mk: b(`git commit -a -m "x"`), want: ask},
			{name: "git commit -am（短縮フラグの結合）", mk: b(`git commit -am "x"`), want: ask},
			{name: "&& の後ろでも見る", mk: b(`git status --short && git add -A && git commit -m x`), want: ask},

			// 通すもの
			{name: "git add <パス>", mk: b("git add docs/AI-GUIDE.md internal/x.go"), want: quiet},
			{name: "git add -u（追跡中のものだけ・CLAUDE.md が許している）", mk: b("git add -u"), want: quiet},
			{name: "git add -p", mk: b("git add -p"), want: quiet},
			{name: "git commit -m", mk: b(`git commit -m "x"`), want: quiet},
			{name: "git commit --amend（-a を含まない）", mk: b("git commit --amend --no-edit"), want: quiet},
			{name: "パスに . を含むだけのもの", mk: b("git add ./docs/x.md .github/workflows/ci.yml"), want: quiet},

			// ── 履歴を消す（取り返しがつかないので deny）────────────────────────────
			{name: "git push --force", mk: b("git push --force origin main"), want: deny},
			{name: "git push -f", mk: b("git push -f"), want: deny},
			{name: "git push --force-with-lease は通す", mk: b("git push --force-with-lease origin main"), want: quiet},
			{name: "git push は通す", mk: b("git push origin main"), want: quiet},
			// `+` で始まる refspec は、その ref だけを --force と同じく強制で更新する
			{name: "refspec: git push origin +main", mk: b("git push origin +main"), want: deny},
			{name: "refspec: git push origin +HEAD:main", mk: b("git push origin +HEAD:main"), want: deny},
			{name: "refspec: git push origin main +dev（2 つ目）", mk: b("git push origin main +dev"), want: deny},
			{name: "refspec: git push -u origin +topic", mk: b("git push -u origin +topic"), want: deny},
			{name: "refspec: sudo git push origin +main", mk: b("sudo git push origin +main"), want: deny},
			{name: "refspec: bash -c 'git push origin +main'", mk: b(`bash -c 'git push origin +main'`), want: deny},
			{name: "refspec: git push origin +main 2>&1 | tail", mk: b("git push origin +main 2>&1 | tail -5"), want: deny},
			{name: "refspec: git push origin main:main は通す", mk: b("git push origin main:main"), want: quiet},
			{name: "refspec: git push -u origin HEAD は通す", mk: b("git push -u origin HEAD"), want: quiet},
			{name: "refspec: git push --force-with-lease origin main は通す", mk: b("git push --force-with-lease origin main"), want: quiet},
			{name: "refspec: push-option の値の + は refspec ではない", mk: b("git push -o +x origin main"), want: quiet},
			{name: "refspec: 引数の位置の +main（echo）は通す", mk: b("echo git push origin +main"), want: quiet},
			{name: "refspec: git fetch origin +main:main は通す（push ではない）", mk: b("git fetch origin +refs/heads/main:refs/remotes/origin/main"), want: quiet},

			// ── 未コミットの変更を捨てる（deny）────────────────────────────────────
			{name: "git reset --hard", mk: b("git reset --hard HEAD~1"), want: deny},
			{name: "git reset（--hard でない）は通す", mk: b("git reset HEAD~1"), want: quiet},
			{name: "git clean -fdx", mk: b("git clean -fdx"), want: deny},
			{name: "git clean -n は通す", mk: b("git clean -n"), want: quiet},
			{name: "git checkout .", mk: b("git checkout ."), want: deny},
			{name: "git checkout -- . ", mk: b("git checkout -- ."), want: deny},
			{name: "git restore .", mk: b("git restore ."), want: deny},

			// 以前は素通りしていた 5 形（回帰）
			{name: "回帰: git checkout -- <個別のパス>",
				mk: b("git checkout -- internal/client/hook/loop/gates.go"), want: deny},
			{name: "回帰: git restore <ディレクトリ>",
				mk: b("git restore internal/client/hook/loop/"), want: deny},
			{name: "回帰: git checkout HEAD -- <パス>",
				mk: b("git checkout HEAD -- internal/client/hook/loop/gates.go"), want: deny},
			{name: "回帰: git restore --source=HEAD --staged --worktree <パス>",
				mk: b("git restore --source=HEAD --staged --worktree internal/client/hook/loop/gates.go"), want: deny},
			{name: "回帰: git -C <ほかのセッションの作業ツリー> checkout .",
				mk: bp(func(s *sandbox) string {
					return "git -C " + s.p("proj", ".claude", "worktrees", "other") + " checkout ."
				}), want: deny},

			// 位置引数だけのパス指定（実在するものはパス、しないものはブランチ）
			{name: "git checkout <実在するファイル>", mk: b("git checkout internal/client/hook/loop/gates.go"), want: deny},
			{name: "git checkout <glob>", mk: b("git checkout '*.go'"), want: deny},
			{name: "git checkout <ブランチ> は通す", mk: b("git checkout main"), want: quiet},
			{name: "git checkout <スラッシュを含むブランチ> は通す（実在しない）", mk: b("git checkout feature/x"), want: quiet},
			{name: "git checkout -b は通す", mk: b("git checkout -b abc-0123-gitguard"), want: quiet},
			{name: "git checkout -B <名前> <起点> は通す", mk: b("git checkout -B topic origin/main"), want: quiet},
			{name: "git checkout --detach HEAD は通す", mk: b("git checkout --detach HEAD"), want: quiet},
			{name: "git restore --staged <パス> は通す（index だけで作業ツリーは変えない）",
				mk: b("git restore --staged internal/client/hook/loop/gates.go"), want: quiet},
			// リダイレクトの記述子（`2>&1` の `2`）と行き先をパス指定の引数と数えない
			{name: "リダイレクト: git checkout <ブランチ> 2>&1 は通す", mk: b("git checkout main 2>&1"), want: quiet},
			{name: "リダイレクト: git checkout <ブランチ> 2>&1 | tail は通す", mk: b("git checkout main 2>&1 | tail -20"), want: quiet},
			{name: "リダイレクト: git checkout <ブランチ> 2>/dev/null は通す", mk: b("git checkout main 2>/dev/null"), want: quiet},
			{name: "リダイレクト: git checkout <ブランチ> >out.log は通す", mk: b("git checkout main >out.log"), want: quiet},
			{name: "リダイレクト: git checkout <ブランチ> &>/dev/null は通す", mk: b("git checkout main &>/dev/null"), want: quiet},
			{name: "リダイレクト: git checkout -b <名前> 2>&1 は通す", mk: b("git checkout -b topic 2>&1"), want: quiet},
			{name: "リダイレクト: git switch <ブランチ> 2>&1 は通す", mk: b("git switch main 2>&1"), want: quiet},
			// 対照: パス指定はリダイレクトが付いても deny のまま
			{name: "リダイレクト: git checkout -- <パス> 2>&1 は deny", mk: b("git checkout -- internal/client/hook/loop/gates.go 2>&1"), want: deny},
			{name: "リダイレクト: git checkout <ref> -- <パス> 2>/dev/null は deny",
				mk: b("git checkout HEAD -- internal/client/hook/loop/gates.go 2>/dev/null"), want: deny},
			{name: "リダイレクト: git checkout -- <パス> &>/dev/null は deny", mk: b("git checkout -- internal/client/hook/loop/gates.go &>/dev/null"), want: deny},
			{name: "リダイレクト: git checkout . >out.log は deny", mk: b("git checkout . >out.log"), want: deny},
			{name: "リダイレクト: 行き先の後ろのパスも同じコマンドの引数", mk: b("git checkout >out.log HEAD -- internal/client/hook/loop/gates.go"), want: deny},
			{name: "リダイレクト: git reset --hard 2>&1 は deny", mk: b("git reset --hard 2>&1"), want: deny},
			{name: "git switch --discard-changes", mk: b("git switch --discard-changes main"), want: deny},
			{name: "git switch -f", mk: b("git switch -f main"), want: deny},
			{name: "git switch <ブランチ> は通す", mk: b("git switch main"), want: quiet},
			{name: "git switch -c は通す", mk: b("git switch -c topic"), want: quiet},

			// ── ほかの作業ツリーを指す -C / --work-tree / --git-dir ────────────────
			{name: "git -C <ほかの作業ツリー> add -A（プロジェクトの中でも別の作業ツリー）",
				mk: bp(func(s *sandbox) string {
					return "git -C " + s.p("proj", ".claude", "worktrees", "other") + " add -A"
				}), want: deny},
			{name: "git -C <プロジェクトの外> add -A", mk: bp(func(s *sandbox) string {
				return "git -C " + s.p("outside") + " add -A"
			}), want: deny},
			{name: "git --work-tree=<ほかの作業ツリー> commit", mk: bp(func(s *sandbox) string {
				return "git --work-tree=" + s.p("proj", ".claude", "worktrees", "other") + ` commit -m "x"`
			}), want: deny},
			{name: "git --git-dir <ほかの作業ツリーの .git> stash", mk: bp(func(s *sandbox) string {
				return "git --git-dir " + s.p("proj", ".claude", "worktrees", "other", ".git") + " stash"
			}), want: deny},
			{name: "git -C <ほかの作業ツリー> status は通す（読むだけ）", mk: bp(func(s *sandbox) string {
				return "git -C " + s.p("proj", ".claude", "worktrees", "other") + " status --short"
			}), want: quiet},
			{name: "git -C <ほかの作業ツリー> log は通す", mk: bp(func(s *sandbox) string {
				return "git -C " + s.p("proj", ".claude", "worktrees", "other") + " log --oneline -1"
			}), want: quiet},
			{name: "git -C <ほかの作業ツリー> branch --show-current は通す", mk: bp(func(s *sandbox) string {
				return "git -C " + s.p("proj", ".claude", "worktrees", "other") + " branch --show-current"
			}), want: quiet},
			{name: "git -C <ほかの作業ツリー> stash list は通す", mk: bp(func(s *sandbox) string {
				return "git -C " + s.p("proj", ".claude", "worktrees", "other") + " stash list"
			}), want: quiet},
			{name: "git -C <同じ作業ツリーの中のディレクトリ> commit は通す", mk: b(`git -C internal commit -m "x"`), want: quiet},
			{name: "git -C . add -A は巻き込みのまま（外ではない）", mk: b("git -C . add -A"), want: ask},

			// ── Windows の形のパス（どの OS で走らせても同じ結果になる形で入れる）──────
			// 引用符で囲まない `C:\…` は、シェルの語の分け方で `\` がエスケープとして食われ `C:Usersx` になる。
			// 復元できないので「いまの作業ツリーだと確かめられない」＝外として扱う（fail-closed）。
			{name: "Windows: git -C <絶対パス・引用符なし> add -A",
				mk: b(`git -C C:\Users\x\proj\.claude\worktrees\other add -A`), want: deny},
			{name: "Windows: git -C <絶対パス・引用符あり> add -A",
				mk: b(`git -C 'C:\Users\x\proj\.claude\worktrees\other' add -A`), want: deny},
			{name: "Windows: git --work-tree=<絶対パス> commit",
				mk: b(`git --work-tree=C:\Users\x\proj\.claude\worktrees\other commit -m "x"`), want: deny},
			{name: "Windows: git --git-dir <絶対パス> stash",
				mk: b(`git --git-dir 'C:\Users\x\proj\.claude\worktrees\other\.git' stash`), want: deny},
			{name: "Windows: git -C <区切りが / の絶対パス> add -A",
				mk: b("git -C C:/Users/x/proj/.claude/worktrees/other add -A"), want: deny},
			{name: "Windows: git -C <絶対パス> status は通す（読むだけ）",
				mk: b(`git -C 'C:\Users\x\proj\.claude\worktrees\other' status --short`), want: quiet},
			{name: "git -C <実在しないディレクトリ> commit（確かめられないので外として扱う）",
				mk: b(`git -C no-such-dir commit -m "x"`), want: deny},

			// ── 引用符で囲まない Windows の絶対パスで git を呼ぶ。posix の分け方（Git Bash）
			// では `\` がエスケープとして食われ、実行ファイル名が git と一致しなかった（fail-open）。
			// Windows 規則の第 2 の分け方（`\` をエスケープとして扱わない）を posix と両方に掛けて直す。
			// 空白を含むパスはどの規則でもシェルが語を割るので対象外（引用符で囲めば従来どおり ask）。
			{name: "Windows: 引用符なしの絶対パスの git.exe（add -A）", mk: b(`C:\tools\git.exe add -A`), want: ask},
			{name: "Windows: 引用符ありの絶対パスの git.exe は従来どおり ask（posix 側で読める）",
				mk: b(`'C:\tools\git.exe' add -A`), want: ask},
			{name: "Windows: 引用符なしの絶対パスの git.exe（読むだけの status は通す）",
				mk: b(`C:\tools\git.exe status`), want: quiet},
			{name: "Windows: git を名乗らない実行ファイルは素通りする（notgit.exe）",
				mk: b(`C:\tools\notgit.exe add -A`), want: quiet},

			// ── 末尾の欄が空になる形（区切りで分ける実装の落とし穴）──────────────
			{name: "空欄: git checkout --（`--` の後ろに何も無い）", mk: b("git checkout --"), want: quiet},
			{name: "空欄: git checkout HEAD --（`--` の後ろに何も無い）", mk: b("git checkout HEAD --"), want: quiet},
			{name: "空欄: git --git-dir= status（= の後ろが空）", mk: b("git --git-dir= status"), want: quiet},
			{name: "空欄: git -C '' add -A（-C の値が空）", mk: b("git -C '' add -A"), want: ask},
			{name: "空欄: git restore --source= <パス>（= の後ろが空でもパスは見る）",
				mk: b("git restore --source= internal/client/hook/loop/gates.go"), want: deny},
			{name: "空欄: git commit -m ''（メッセージが空）", mk: b("git commit -m ''"), want: quiet},

			// ── 引用符・ヒアドキュメントの中（実際には実行されない語）──────────────
			{name: "引用符の中の git add -A", mk: b(`echo 'git add -A は禁止'`), want: quiet},
			{name: "コミットメッセージの本文", mk: b("git commit -m \"$(cat <<'EOF'\nx を直した\ngit add -A を使わないこと\nEOF\n)\""), want: quiet},
			{name: "git でないコマンド", mk: b("rg -n 'git add -A' docs/"), want: quiet},

			// ── 前置の語（sudo・env・xargs …）────────────────────────────────────
			// 語の並びの先頭しか見ないので、前に 1 語挟まるだけで一度も検査されなかった
			{name: "前置: sudo", mk: b("sudo git reset --hard"), want: deny},
			{name: "前置: env", mk: b("env git clean -fd"), want: deny},
			{name: "前置: xargs", mk: b("xargs git reset --hard"), want: deny},
			{name: "前置: nohup", mk: b("nohup git reset --hard"), want: deny},
			{name: "前置: time", mk: b("time git clean -fd"), want: deny},
			{name: "前置: timeout と秒数", mk: b("timeout 30 git reset --hard"), want: deny},
			{name: "前置: nice と選択肢", mk: b("nice -n 10 git clean -fd"), want: deny},
			{name: "前置: doas", mk: b("doas git clean -fd"), want: deny},
			{name: "前置: command", mk: b("command git reset --hard"), want: deny},
			{name: "前置: stdbuf", mk: b("stdbuf -o0 git reset --hard"), want: deny},
			{name: "前置: 挟んだ add -A（巻き込みは ask のまま）", mk: b("sudo git add -A"), want: ask},
			// 壊さない側
			{name: "前置: 引数の位置の sudo", mk: b("echo sudo git reset --hard"), want: quiet},
			{name: "前置: 引用符の中の sudo", mk: b(`git commit -m "sudo git reset --hard"`), want: quiet},
			{name: "前置: env 単体", mk: b("env"), want: quiet},
			{name: "前置: env の後ろが区切り", mk: b("env | grep PATH"), want: quiet},
			{name: "前置: env -u を挟んだ go test", mk: b("env -u LOOPTRACK_API_URL go test ./..."), want: quiet},
			{name: "前置: timeout を挟んだ go test", mk: b("timeout 30 go test ./..."), want: quiet},
			{name: "前置: command -v git", mk: b("command -v git"), want: quiet},
			{name: "前置: sudo の後ろが読むだけの git", mk: b("sudo git status --short"), want: quiet},
			{name: "前置: 前置に似た語（sudoers）", mk: b("sudoers git status"), want: quiet},
			{name: "前置: xargs 単体", mk: b("xargs"), want: quiet},
			{name: "前置: 引用符の中に前置とコマンド", mk: b(`echo 'sudo git clean -fd は禁止'`), want: quiet},

			// ── 入れ子のシェル（bash -c '…'・eval "…"）──────────────────────────
			// 引用符の中はデータという前提が逆になる形。引用符を区切りに替えて、中身を単純コマンドとして読む
			{name: "入れ子: bash -c の reset --hard", mk: b(`bash -c 'git reset --hard'`), want: deny},
			{name: "入れ子: sh -c の clean -fd", mk: b(`sh -c "git clean -fd"`), want: deny},
			{name: "入れ子: eval の clean -fd", mk: b(`eval "git clean -fd"`), want: deny},
			{name: "入れ子: zsh -c の push --force", mk: b(`zsh -c 'git push --force origin main'`), want: deny},
			{name: "入れ子: dash -c の reset --hard", mk: b(`dash -c 'git reset --hard'`), want: deny},
			{name: "入れ子: bash -lc の reset --hard", mk: b(`bash -lc 'git reset --hard'`), want: deny},
			{name: "入れ子: bash -ec の clean -fd", mk: b(`bash -ec 'git clean -fd'`), want: deny},
			{name: "入れ子: bash -o pipefail -c の checkout <パス>",
				mk: b(`bash -o pipefail -c 'git checkout -- internal/client/hook/loop/gates.go'`), want: deny},
			{name: "入れ子: 前置の語と入れ子の組み合わせ", mk: b(`sudo bash -c 'git reset --hard'`), want: deny},
			{name: "入れ子: bash -c の中の前置の語", mk: b(`bash -c 'sudo git reset --hard'`), want: deny},
			{name: "入れ子: bash -c の add -A（巻き込みは ask のまま）", mk: b(`bash -c 'git add -A'`), want: ask},
			// 壊さない側
			{name: "入れ子: echo の引用符の中", mk: b(`echo 'git reset --hard'`), want: quiet},
			{name: "入れ子: シェルでない -c は展開しない", mk: b(`ruby -c 'git reset --hard'`), want: quiet},
			{name: "入れ子: -lc に見える引数を取る echo", mk: b(`echo -lc 'git clean -fd'`), want: quiet},
			{name: "入れ子: bash -c の ls", mk: b(`bash -c 'ls /tmp'`), want: quiet},
			{name: "入れ子: bash -c の git status", mk: b(`bash -c 'git status --short'`), want: quiet},
			{name: "入れ子: grep の引数の中", mk: b(`grep -r "git clean -fd" docs/`), want: quiet},
			{name: "入れ子: grep -rn の引数の中", mk: b(`grep -rn 'git reset --hard' kit/`), want: quiet},
			{name: "入れ子: 引用符が閉じていない bash -c", mk: b(`bash -c 'git reset --hard`), want: quiet},
			{name: "入れ子: 2 段の入れ子（1 段だけほどく設計なので通る）",
				mk: b(`bash -c "bash -c 'git reset --hard'"`), want: quiet},
			{name: "入れ子: bash -c の commit", mk: b(`bash -c 'git commit -m x'`), want: quiet},

			// ── 入れ子をほどくのはコマンドの位置だけ ──────────────────────────────
			// 引数の位置に当てると、**何も実行されない形**と**別の場所で実行される形**まで deny になる。
			// deny は bypass permissions でも素通りしないので、このガードについて書く作業そのものが止まる
			{name: "位置: 引数の位置の bash -c（echo）", mk: b(`echo bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: 引数の位置の bash -c（ls -l）", mk: b(`ls -l bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: 引数の位置の bash -c（printf）", mk: b(`printf '%s' bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: コミットメッセージの中の bash -c",
				mk: b(`git commit -m "bash -c 'git reset --hard' は止まる"`), want: quiet},
			{name: "位置: 引数の位置の eval", mk: b(`echo eval 'git clean -fd'`), want: quiet},
			{name: "位置: 引数の位置の sh -c", mk: b(`echo sh -c 'git clean -fd'`), want: quiet},
			{name: "位置: 引数の位置の zsh -c", mk: b(`echo zsh -c 'git reset --hard'`), want: quiet},
			{name: "位置: 引数の位置の dash -c", mk: b(`echo dash -c 'git reset --hard'`), want: quiet},
			{name: "位置: rg の引数の中", mk: b(`rg -n "bash -c 'git reset --hard'" docs/`), want: quiet},
			// 遠隔・コンテナは「守る対象の作業ツリーではない場所」で実行されるので止めない
			{name: "位置: ssh の先", mk: b(`ssh host bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: ssh -t の先", mk: b(`ssh -t host sh -c 'git clean -fd'`), want: quiet},
			{name: "位置: docker run の先", mk: b(`docker run img bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: docker exec の先", mk: b(`docker exec c sh -c 'git clean -fd'`), want: quiet},
			{name: "位置: kubectl exec の先", mk: b(`kubectl exec p -- bash -c 'git reset --hard'`), want: quiet},
			// コマンドの位置なら（前置の語を落とした後も含めて）ほどく
			{name: "位置: 区切りの直後", mk: b(`git status; bash -c 'git reset --hard'`), want: deny},
			{name: "位置: 前置の語の後ろ", mk: b(`sudo bash -c 'git reset --hard'`), want: deny},
			{name: "位置: xargs の後ろ", mk: b(`xargs bash -c 'git reset --hard'`), want: deny},
			{name: "位置: 実行ファイルのパス付きの前置",
				mk: b(`/usr/bin/sudo bash -c 'git reset --hard'`), want: deny},
			{name: "位置: 一覧に無いラッパは通す（caffeinate）",
				mk: b(`caffeinate bash -c 'git reset --hard'`), want: quiet},

			// ── 前置の語 + 引数の位置のラッパ ────────────────────────────────────
			// 前置の語は**選択肢だけを飲み、選択肢でない語が来たら止まる**（止まった語がコマンド）。
			// 任意個の語を飲ませると、`timeout 30 ssh host bash -c '…'` の `bash -c` がコマンドの位置に見えて、
			// 遠隔で走る形まで deny になる（実測で 24 形）
			{name: "位置: timeout + ssh の先", mk: b(`timeout 30 ssh host bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: nohup + ssh の先", mk: b(`nohup ssh host bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: env + 変数 + docker の先",
				mk: b(`env DOCKER_HOST=x docker run img bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: timeout + docker の先",
				mk: b(`timeout 300 docker run img bash -c 'git clean -fd'`), want: quiet},
			{name: "位置: sudo + echo の引数", mk: b(`sudo echo bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: sudo + ls -l の引数", mk: b(`sudo ls -l bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: time + ssh の先", mk: b(`time ssh host bash -c 'git clean -fd'`), want: quiet},
			{name: "位置: nice + docker の先",
				mk: b(`nice -n 10 docker run img bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: command + ssh の先", mk: b(`command ssh host bash -c 'git reset --hard'`), want: quiet},
			{name: "位置: sudo + kubectl exec の先",
				mk: b(`sudo kubectl exec p -- bash -c 'git reset --hard'`), want: quiet},
			// 値を別の語で取る短い選択肢は、値の 1 語だけを飲む（次の語がコマンド）
			{name: "位置: env -u FOO の直後", mk: b(`env -u FOO bash -c 'git reset --hard'`), want: deny},
			{name: "位置: sudo -u deploy の直後", mk: b(`sudo -u deploy bash -c 'git clean -fd'`), want: deny},
			{name: "位置: xargs -a f の直後", mk: b(`xargs -a f bash -c 'git reset --hard'`), want: deny},
			{name: "位置: flock <ファイル> の直後", mk: b(`flock /tmp/l bash -c 'git clean -fd'`), want: deny},
			{name: "位置: script -q <ファイル> の直後",
				mk: b(`script -q /tmp/o bash -c 'git reset --hard'`), want: deny},
			// ほどく側もコマンド名の前の実行ファイルのパスを外す
			{name: "位置: シェルの実行ファイルのパス", mk: b(`/bin/bash -c 'git reset --hard'`), want: deny},
			{name: "位置: シェルの実行ファイルのパス（sh）", mk: b(`/bin/sh -c 'git clean -fd'`), want: deny},
			// `c` が最後でない結合形は読めない（基点でも通る。塞がない限界）
			{name: "限界: bash -cx は読めない", mk: b(`bash -cx 'git reset --hard'`), want: quiet},

			// ── シェルの名前の大小 ────────────────────────────────────────────────
			// macOS と Windows は大小を区別しないので BASH は実際に実行される（実機で確認）
			{name: "大小: BASH -c", mk: b(`BASH -c 'git reset --hard'`), want: deny},
			{name: "大小: Bash -c", mk: b(`Bash -c 'git clean -fd'`), want: deny},
			{name: "大小: SH -c", mk: b(`SH -c 'git reset --hard'`), want: deny},
			{name: "大小: ZSH -c", mk: b(`ZSH -c 'git clean -fd'`), want: deny},
			{name: "大小: DASH -c", mk: b(`DASH -c 'git reset --hard'`), want: deny},
			{name: "大小: 引数の位置の Bash は通す", mk: b(`echo Bash -c 'git reset --hard'`), want: quiet},
			// eval は組込みで、EVAL は実行されない（`command not found` を実機で確認）
			{name: "大小: EVAL は通す", mk: b(`EVAL "git clean -fd"`), want: quiet},
			{name: "大小: BASH.EXE -c", mk: b(`BASH.EXE -c 'git reset --hard'`), want: deny},
			{name: "大小: sh.EXE -c", mk: b(`sh.EXE -c 'git clean -fd'`), want: deny},

			// コマンドの位置＝「後ろにコマンドが続くもの」の直後
			{name: "位置: if の直後", mk: b(`if bash -c 'git reset --hard'; then echo y; fi`), want: deny},
			{name: "位置: do の直後", mk: b(`for f in a; do bash -c 'git clean -fd'; done`), want: deny},
			{name: "位置: { の直後", mk: b(`{ bash -c 'git reset --hard'; }`), want: deny},
			{name: "位置: ! の直後", mk: b(`! bash -c 'git reset --hard'`), want: deny},
			{name: "位置: 変数の指定の直後", mk: b(`x=1 bash -c 'git reset --hard'`), want: deny},
			{name: "位置: find -exec の直後", mk: b(`find . -exec bash -c 'git clean -fd' \;`), want: deny},
			{name: "位置: env -u FOO の直後", mk: b(`env -u FOO bash -c 'git reset --hard'`), want: deny},
			{name: "位置: setsid の直後", mk: b(`setsid bash -c 'git reset --hard'`), want: deny},
			{name: "位置: flock の直後", mk: b(`flock /tmp/l bash -c 'git clean -fd'`), want: deny},

			// ── 値を別の語で取る選択肢は読み切れない（塞がない。限界として文書に書いてある）──
			{name: "限界: env -u FOO を挟むと読み切れない", mk: b("env -u FOO git reset --hard"), want: quiet},
			{name: "限界: sudo -u deploy を挟むと読み切れない", mk: b("sudo -u deploy git clean -fd"), want: quiet},

			// ── この回では段を足さない形（基点と同じ判定のまま）──────────────────
			{name: "据え置き: 一重引用符の中のコマンド置換", mk: b(`echo 'x $(git reset --hard) y'`), want: quiet},
			{name: "据え置き: 一重引用符のコミットメッセージ",
				mk: b(`git commit -m '$(git clean -fd) の話'`), want: quiet},
			{name: "据え置き: 二重引用符の中のコマンド置換", mk: b(`echo "$(git reset --hard)"`), want: quiet},
			{name: "据え置き: ANSI-C の引用", mk: b(`echo $'x $(git reset --hard) y'`), want: quiet},
			{name: "据え置き: 引用された区切りのヒアドキュメント",
				mk: b("cat <<'EOF'\n$(git reset --hard) は禁止\nEOF\n"), want: quiet},
			{name: "据え置き: 行末の継続", mk: b("git reset --hard\\\n"), want: quiet},

			// ── 例外 ──────────────────────────────────────────────────────────────
			{name: "LOOPTRACK_LOOP_GIT_GUARD_ALLOW に挙げた語があれば通す（ask のもの）",
				mk: run("git add -A", map[string]string{"LOOPTRACK_LOOP_GIT_GUARD_ALLOW": "git add -A"}), want: quiet},
			{name: "LOOPTRACK_LOOP_GIT_GUARD_ALLOW に挙げた語があれば通す（deny のもの）",
				mk: run("git checkout -- internal/client/hook/loop/gates.go",
					map[string]string{"LOOPTRACK_LOOP_GIT_GUARD_ALLOW": "git checkout --"}), want: quiet},

			// ── 壊れた入力 ────────────────────────────────────────────────────────
			{name: "入力が JSON でなければ何もしない",
				mk: func(s *sandbox) call {
					return call{hook: "pre-tool-git-guard", env: map[string]string{"CLAUDE_PROJECT_DIR": s.p("proj")}, input: "not json"}
				},
				want: all(quiet, func(t *testing.T, _ *sandbox, g got) {
					if g.out.ExitCode != 0 {
						t.Errorf("終了コード %d", g.out.ExitCode)
					}
				})},
		}}.run(t)
}
