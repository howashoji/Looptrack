// Package dashstdin は、本文を取る引数に共通の 1 つの規則を持つ:
// 値がちょうど "-" なら、その引数をそのまま使わずに標準入力を読む（`looptrack handoff append` が既に持っていた
// 規則と同じ）。`looptrack issue comment` の本文・`issue new --body`・`issue status`/`issue close` の --comment・
// `issue next --comment` はすべてこの 1 つの実装を呼ぶ。同じ判定をコマンドごとに書き直すと、
// 1 つだけ直し忘れて「- を渡しても標準入力を読まないコマンド」が残る。
package dashstdin

import "io"

// Dash は「標準入力を読む」を意味する値。
const Dash = "-"

// Resolve は v がちょうど Dash でなければ v をそのままのバイト列で返す（used は false）。
// v が Dash なら stdin を最後まで読んで返す（used は true）。stdin が nil（標準入力が無い実行）なら
// 空を返す（誤りにはしない。空かどうかの判定は呼び出し側が used を見て行う）。
func Resolve(v string, stdin io.Reader) (raw []byte, used bool, err error) {
	if v != Dash {
		return []byte(v), false, nil
	}
	if stdin == nil {
		return nil, true, nil
	}
	b, err := io.ReadAll(stdin)
	return b, true, err
}
