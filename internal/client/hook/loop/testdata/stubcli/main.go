// Command stubcli は hook のテストで使うイシューの CLI のスタブ（小さな実行ファイル）。
//
// 応答は、自分の実行ファイルのパスに ".rules.json" を足したファイルの規則で決める（引数と環境変数で最初に一致した
// 規則の out を標準出力・err を標準エラーに出し、exit で終わる。どれにも一致しなければ 2 で終わる）。
// hook は LOOPTRACK_LOOP_ISSUE_CLI にこの実行ファイルのパスを受け取って起動する。
//
// シェルのスクリプト（sh / cmd）ではなく実行ファイルにしているのは、Windows で cmd の解釈（行の区切り・引数の分かれ方・
// バッチファイルをコードページで読むこと）に引きずられて、スタブが書いたとおりに動かなかったため。
// 規則の型は internal/client/hook/loop の harness_test.go の stubRule と同じ形にしておく。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type rule struct {
	Args string `json:"args"` // 引数を空白でつないだもの（空ならどの引数にも一致）
	Env  string `json:"env"`  // "名前=値"（空ならどの環境にも一致）
	Out  string `json:"out"`  // 標準出力に出す 1 行（空なら何も出さない）
	Err  string `json:"err"`  // 標準エラーに出す 1 行
	Exit int    `json:"exit"` // 終了コード
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stubcli:", err)
		os.Exit(3)
	}
	b, err := os.ReadFile(exe + ".rules.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "stubcli:", err)
		os.Exit(3)
	}
	var rules []rule
	if err := json.Unmarshal(b, &rules); err != nil {
		fmt.Fprintln(os.Stderr, "stubcli:", err)
		os.Exit(3)
	}
	args := strings.Join(os.Args[1:], " ")
	for _, r := range rules {
		if r.Args != "" && r.Args != args {
			continue
		}
		if r.Env != "" {
			k, v, _ := strings.Cut(r.Env, "=")
			if os.Getenv(k) != v {
				continue
			}
		}
		if r.Out != "" {
			fmt.Println(r.Out)
		}
		if r.Err != "" {
			fmt.Fprintln(os.Stderr, r.Err)
		}
		os.Exit(r.Exit)
	}
	fmt.Fprintf(os.Stderr, "stubcli: 規則に無い呼び出し: %s\n", args)
	os.Exit(2)
}
