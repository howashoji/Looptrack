package hookcmd

import "testing"

// eqs は段の出力をそのまま突き合わせる。
func eqs(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", name, got, want)
	}
}

func TestStripCommandPrefixes(t *testing.T) {
	// 落とす側（後ろにコマンドが続く前置の語）
	eqs(t, "sudo", StripCommandPrefixes("sudo git reset --hard"), "     git reset --hard")
	eqs(t, "env", StripCommandPrefixes("env git clean -fd"), "    git clean -fd")
	eqs(t, "xargs", StripCommandPrefixes("xargs cat /p/.env"), "      cat /p/.env")
	eqs(t, "doas", StripCommandPrefixes("doas git clean -fd"), "     git clean -fd")
	eqs(t, "nohup", StripCommandPrefixes("nohup git reset --hard"), "      git reset --hard")
	eqs(t, "time", StripCommandPrefixes("time git clean -fd"), "     git clean -fd")
	eqs(t, "command", StripCommandPrefixes("command git reset"), "        git reset")
	eqs(t, "timeout と秒数", StripCommandPrefixes("timeout 30 git reset"), "           git reset")
	eqs(t, "nice と選択肢", StripCommandPrefixes("nice -n 10 git clean"), "           git clean")
	eqs(t, "stdbuf と選択肢", StripCommandPrefixes("stdbuf -o0 git reset"), "           git reset")
	eqs(t, "前置を 2 つ重ねる", StripCommandPrefixes("sudo nohup cat /p/.env"), "           cat /p/.env")
	eqs(t, "実行ファイルのパス付き", StripCommandPrefixes("/usr/bin/env cat /p/.env"), "             cat /p/.env")
	eqs(t, "区切りの後ろ", StripCommandPrefixes("ls; sudo cat /p/.env"), "ls;      cat /p/.env")
	eqs(t, "変数の指定を飲む", StripCommandPrefixes("env FOO=1 cat /p/.env"), "          cat /p/.env")

	// 落とさない側
	eqs(t, "後ろにコマンドが無い", StripCommandPrefixes("env"), "env")
	eqs(t, "後ろが区切り", StripCommandPrefixes("env | grep PATH"), "env | grep PATH")
	eqs(t, "引数の位置", StripCommandPrefixes("echo sudo git reset --hard"), "echo sudo git reset --hard")
	eqs(t, "引用符の中", StripCommandPrefixes(`git commit -m "sudo git reset --hard"`), `git commit -m "sudo git reset --hard"`)
	eqs(t, "前置でない語", StripCommandPrefixes("go test ./..."), "go test ./...")
	eqs(t, "語の一部", StripCommandPrefixes("sudoers cat /p/.env"), "sudoers cat /p/.env")
	eqs(t, "引用符が閉じていない", StripCommandPrefixes(`echo "sudo cat`), `echo "sudo cat`)
	eqs(t, "前置の語だけが引用されている", StripCommandPrefixes(`'sudo' git reset --hard`), `'sudo' git reset --hard`)
	eqs(t, "タブは残す", StripCommandPrefixes("sudo\tcat /p/.env"), "    \tcat /p/.env")
}

func TestUnwrapNestedShell(t *testing.T) {
	// ほどく側（引用符を区切りに替えるので、中身が 1 つの単純コマンドになる）
	eqs(t, "bash -c", UnwrapNestedShell(`bash -c 'git reset --hard'`, HeadOnly), `bash -c ;git reset --hard;`)
	eqs(t, "sh -c", UnwrapNestedShell(`sh -c "cat /p/.env"`, HeadOnly), `sh -c ;cat /p/.env;`)
	eqs(t, "eval", UnwrapNestedShell(`eval "git clean -fd"`, HeadOnly), `eval ;git clean -fd;`)
	eqs(t, "zsh -c", UnwrapNestedShell(`zsh -c 'ls'`, HeadOnly), `zsh -c ;ls;`)
	eqs(t, "dash -c", UnwrapNestedShell(`dash -c 'ls'`, HeadOnly), `dash -c ;ls;`)
	eqs(t, "bash -lc", UnwrapNestedShell(`bash -lc 'ls'`, HeadOnly), `bash -lc ;ls;`)
	eqs(t, "bash -o pipefail -c", UnwrapNestedShell(`bash -o pipefail -c 'ls'`, HeadOnly), `bash -o pipefail -c ;ls;`)
	eqs(t, "区切りの直後", UnwrapNestedShell(`git status; bash -c 'ls'`, HeadOnly), `git status; bash -c ;ls;`)
	eqs(t, "かっこの直後", UnwrapNestedShell(`(bash -c 'ls')`, HeadOnly), `(bash -c ;ls;)`)
	// シェルの名前は大小を区別しない（macOS・Windows では BASH が実際に実行される。実機で確認）
	eqs(t, "大文字の BASH", UnwrapNestedShell(`BASH -c 'ls'`, HeadOnly), `BASH -c ;ls;`)
	eqs(t, "Bash", UnwrapNestedShell(`Bash -c 'ls'`, HeadOnly), `Bash -c ;ls;`)
	eqs(t, "大文字の SH", UnwrapNestedShell(`SH -c 'ls'`, HeadOnly), `SH -c ;ls;`)
	eqs(t, "大文字の ZSH", UnwrapNestedShell(`ZSH -c 'ls'`, HeadOnly), `ZSH -c ;ls;`)
	// `.exe` の部分も大小を区別しない（`Bash.exe` は拾えて `BASH.EXE` は落ちる、という半端を避ける）
	eqs(t, "BASH.EXE", UnwrapNestedShell(`BASH.EXE -c 'ls'`, HeadOnly), `BASH.EXE -c ;ls;`)
	eqs(t, "Bash.exe", UnwrapNestedShell(`Bash.exe -c 'ls'`, HeadOnly), `Bash.exe -c ;ls;`)
	eqs(t, "BASH.Exe", UnwrapNestedShell(`BASH.Exe -c 'ls'`, HeadOnly), `BASH.Exe -c ;ls;`)
	eqs(t, "sh.EXE", UnwrapNestedShell(`sh.EXE -c 'ls'`, HeadOnly), `sh.EXE -c ;ls;`)

	// ── コマンドの位置（後ろにコマンドが続くものの直後）──────────────────────
	// 基点の秘密のガードが捕まえていた形。頭の位置だけに縮めたときに落としていた
	eqs(t, "if の直後", UnwrapNestedShell(`if bash -c 'ls'; then echo y; fi`, HeadOnly), `if bash -c ;ls;; then echo y; fi`)
	eqs(t, "then の直後", UnwrapNestedShell(`if true; then bash -c 'ls'; fi`, HeadOnly), `if true; then bash -c ;ls;; fi`)
	eqs(t, "else の直後", UnwrapNestedShell(`if true; then :; else bash -c 'ls'; fi`, HeadOnly), `if true; then :; else bash -c ;ls;; fi`)
	eqs(t, "elif の直後", UnwrapNestedShell(`if true; then :; elif bash -c 'ls'; then :; fi`, HeadOnly), `if true; then :; elif bash -c ;ls;; then :; fi`)
	eqs(t, "do の直後", UnwrapNestedShell(`for f in a; do bash -c 'ls'; done`, HeadOnly), `for f in a; do bash -c ;ls;; done`)
	eqs(t, "while の直後", UnwrapNestedShell(`while bash -c 'ls'; do :; done`, HeadOnly), `while bash -c ;ls;; do :; done`)
	eqs(t, "until の直後", UnwrapNestedShell(`until bash -c 'ls'; do :; done`, HeadOnly), `until bash -c ;ls;; do :; done`)
	eqs(t, "{ の直後", UnwrapNestedShell(`{ bash -c 'ls'; }`, HeadOnly), `{ bash -c ;ls;; }`)
	eqs(t, "! の直後", UnwrapNestedShell(`! bash -c 'ls'`, HeadOnly), `! bash -c ;ls;`)
	eqs(t, ") の直後（case）", UnwrapNestedShell(`case a in a) bash -c 'ls';; esac`, HeadOnly), `case a in a) bash -c ;ls;;; esac`)
	eqs(t, "変数の指定の直後", UnwrapNestedShell(`x=1 bash -c 'ls'`, HeadOnly), `x=1 bash -c ;ls;`)
	eqs(t, "変数の指定が 2 つ", UnwrapNestedShell(`FOO=bar BAZ=1 bash -c 'ls'`, HeadOnly), `FOO=bar BAZ=1 bash -c ;ls;`)
	eqs(t, "find -exec の直後", UnwrapNestedShell(`find . -exec bash -c 'ls' \;`, HeadOnly), `find . -exec bash -c ;ls; \;`)
	eqs(t, "find -execdir の直後", UnwrapNestedShell(`find . -execdir bash -c 'ls' \;`, HeadOnly), `find . -execdir bash -c ;ls; \;`)
	// 値を別の語で取る選択肢は、前置の語の後ろの語として飲む
	eqs(t, "env -u FOO", UnwrapNestedShell(`env -u FOO bash -c 'ls'`, HeadOnly), `env -u FOO bash -c ;ls;`)
	eqs(t, "sudo -u deploy", UnwrapNestedShell(`sudo -u deploy bash -c 'ls'`, HeadOnly), `sudo -u deploy bash -c ;ls;`)
	eqs(t, "xargs -a f", UnwrapNestedShell(`xargs -a f bash -c 'ls'`, HeadOnly), `xargs -a f bash -c ;ls;`)
	eqs(t, "setsid", UnwrapNestedShell(`setsid bash -c 'ls'`, HeadOnly), `setsid bash -c ;ls;`)
	eqs(t, "flock <ファイル>", UnwrapNestedShell(`flock /tmp/l bash -c 'ls'`, HeadOnly), `flock /tmp/l bash -c ;ls;`)
	eqs(t, "script -q <ファイル>", UnwrapNestedShell(`script -q /tmp/o bash -c 'ls'`, HeadOnly), `script -q /tmp/o bash -c ;ls;`)
	eqs(t, "前置の語の直後", UnwrapNestedShell(`sudo bash -c 'ls'`, HeadOnly), `sudo bash -c ;ls;`)
	// **ほどく側もコマンド名の前の実行ファイルのパスを外す**（この 1 行が無いと /bin/bash が落ちる）
	eqs(t, "シェルの実行ファイルのパス", UnwrapNestedShell(`/bin/bash -c 'ls'`, HeadOnly), `/bin/bash -c ;ls;`)
	eqs(t, "シェルの実行ファイルのパス（sh）", UnwrapNestedShell(`/usr/bin/sh -c 'ls'`, HeadOnly), `/usr/bin/sh -c ;ls;`)
	// 前置の語は選択肢だけを飲む（選択肢でない語が来たら、そこがコマンド）
	eqs(t, "前置 + 引数の位置のラッパ", UnwrapNestedShell(`timeout 30 ssh host bash -c 'ls'`, HeadOnly),
		`timeout 30 ssh host bash -c 'ls'`)
	eqs(t, "前置 + echo の引数", UnwrapNestedShell(`sudo echo bash -c 'ls'`, HeadOnly), `sudo echo bash -c 'ls'`)
	eqs(t, "前置 + 変数 + docker", UnwrapNestedShell(`env DOCKER_HOST=x docker run img bash -c 'ls'`, HeadOnly),
		`env DOCKER_HOST=x docker run img bash -c 'ls'`)
	// `c` が最後でない結合形は読まない（塞がない限界）
	eqs(t, "bash -cx は読まない", UnwrapNestedShell(`bash -cx 'ls'`, HeadOnly), `bash -cx 'ls'`)

	// ほどかない側
	eqs(t, "echo の引用符", UnwrapNestedShell(`echo 'git reset --hard'`, HeadOnly), `echo 'git reset --hard'`)
	eqs(t, "シェルでない -c", UnwrapNestedShell(`ruby -c 'cat /p/.env'`, HeadOnly), `ruby -c 'cat /p/.env'`)
	eqs(t, "-lc に見える引数", UnwrapNestedShell(`echo -lc 'ls'`, HeadOnly), `echo -lc 'ls'`)
	eqs(t, "コミットメッセージ", UnwrapNestedShell(`git commit -m "reset --hard"`, HeadOnly), `git commit -m "reset --hard"`)
	eqs(t, "引用符が閉じていない", UnwrapNestedShell(`bash -c 'ls`, HeadOnly), `bash -c 'ls`)
	eqs(t, "引用符が無い", UnwrapNestedShell(`bash -c ls`, HeadOnly), `bash -c ls`)
	eqs(t, "grep の引数", UnwrapNestedShell(`grep -r "git clean -fd" docs/`, HeadOnly), `grep -r "git clean -fd" docs/`)
	// **コマンドの位置でないものは、何も実行されない／別の場所で実行される**ので当てない
	eqs(t, "引数の位置（echo）", UnwrapNestedShell(`echo bash -c 'git reset --hard'`, HeadOnly), `echo bash -c 'git reset --hard'`)
	eqs(t, "引数の位置（ls -l）", UnwrapNestedShell(`ls -l bash -c 'ls'`, HeadOnly), `ls -l bash -c 'ls'`)
	eqs(t, "引数の位置（大文字）", UnwrapNestedShell(`echo Bash -c 'ls'`, HeadOnly), `echo Bash -c 'ls'`)
	eqs(t, "遠隔（ssh）", UnwrapNestedShell(`ssh host bash -c 'ls'`, HeadOnly), `ssh host bash -c 'ls'`)
	eqs(t, "コンテナ（docker run）", UnwrapNestedShell(`docker run img bash -c 'ls'`, HeadOnly), `docker run img bash -c 'ls'`)
	eqs(t, "コンテナ（kubectl exec）", UnwrapNestedShell(`kubectl exec p -- bash -c 'ls'`, HeadOnly), `kubectl exec p -- bash -c 'ls'`)
	eqs(t, "引数の位置（大文字の .exe）", UnwrapNestedShell(`echo BASH.EXE -c 'ls'`, HeadOnly), `echo BASH.EXE -c 'ls'`)
	// eval は組込みで、EVAL は実行されない（`command not found` を実機で確認）ので大小を区別する
	eqs(t, "大文字の EVAL", UnwrapNestedShell(`EVAL "ls"`, HeadOnly), `EVAL "ls"`)
	// 2 段の入れ子はほどかない（1 段だけ、という設計。外側だけが区切りに替わる）
	eqs(t, "2 段の入れ子は 1 段だけ", UnwrapNestedShell(`bash -c "bash -c 'ls'"`, HeadOnly), `bash -c ;bash -c 'ls';`)

	// 引用符でない ' を開きと数えない（打ち消し・コメント）。数えると後ろの本物の bash -c '…' を取り違える
	eqs(t, "打ち消した '", UnwrapNestedShell(`echo don\'t ; bash -c 'ls'`, HeadOnly), `echo don\'t ; bash -c ;ls;`)
	eqs(t, "コメントの中の '", UnwrapNestedShell("echo ok # don't\nbash -c 'ls'", HeadOnly), "echo ok # don't\nbash -c ;ls;")
	eqs(t, "二重引用符の中の \\\"", UnwrapNestedShell(`echo "a\"b" ; bash -c 'ls'`, HeadOnly), `echo "a\"b" ; bash -c ;ls;`)
	// 対照: コメントの中の bash -c '…' は実行されないのでほどかない。語の途中の # はコメントではない
	eqs(t, "コメントの中の bash -c", UnwrapNestedShell(`echo x # bash -c 'ls'`, HeadOnly), `echo x # bash -c 'ls'`)
	// AnyPos（ask のガード）はコメントを飛ばさない（広く取る側。コメントの中の bash -c も従来どおりほどく）
	eqs(t, "AnyPos はコメントを飛ばさない", UnwrapNestedShell(`echo x # bash -c 'ls'`, AnyPos), `echo x # bash -c ;ls;`)
	eqs(t, "語の途中の #", UnwrapNestedShell(`x#y; bash -c 'ls'`, HeadOnly), `x#y; bash -c ;ls;`)
	// StripCommandPrefixes も同じ数え方をする
	eqs(t, "前置の語: 打ち消した ' の後ろ", StripCommandPrefixes(`echo it\'s; sudo git status`), `echo it\'s;      git status`)
}

// TestNormalize は「前置の語を落とす → ほどく → もう一度落とす」の順で掛かること。
func TestNormalize(t *testing.T) {
	eqs(t, "入れ子の中の前置の語", Normalize(`bash -c 'sudo git reset --hard'`, HeadOnly), `bash -c ;     git reset --hard;`)
	eqs(t, "前置の語の後ろの入れ子", Normalize(`sudo bash -c 'git reset --hard'`, HeadOnly), `     bash -c ;git reset --hard;`)
	eqs(t, "xargs の後ろの入れ子", Normalize(`xargs bash -c 'git reset --hard'`, HeadOnly), `      bash -c ;git reset --hard;`)
	eqs(t, "前置を 2 つ重ねた入れ子", Normalize(`sudo nohup bash -c 'ls'`, HeadOnly), `           bash -c ;ls;`)
	eqs(t, "大文字のシェル", Normalize(`sudo BASH -c 'git reset --hard'`, HeadOnly), `     BASH -c ;git reset --hard;`)
	// 引数の位置は動かさない
	eqs(t, "引数の位置の bash -c", Normalize(`echo bash -c 'git reset --hard'`, HeadOnly), `echo bash -c 'git reset --hard'`)
	eqs(t, "遠隔の bash -c", Normalize(`ssh host bash -c 'git reset --hard'`, HeadOnly), `ssh host bash -c 'git reset --hard'`)
	eqs(t, "どちらも無ければそのまま", Normalize("git status", HeadOnly), "git status")
}
