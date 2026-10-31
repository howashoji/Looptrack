package domain

import "regexp"

// presenceTextRe は在席の 1 語の欄（端末のラベル・ブランチ・ディレクトリの名前）の形。空白と句読点を通さないので、文として読める値を入れられない。
// \p{M} は macOS のファイル名の濁点（分解された形）を通すために要る。
var presenceTextRe = regexp.MustCompile(`^[\p{L}\p{M}\p{N}._/-]{0,64}$`)

// PresenceText は在席の 1 語の欄に使える値か（文字・数字・. _ - / だけで 64 文字まで）。
// 在席の値は参加者なら viewer でも名乗れて、ほかの参加者の AI の文脈に入るので、サーバはこの形だけを受け取る。
// hook は通らない値を空にして送る（1 つの欄のせいでセッションごと一覧から消えないように）。
func PresenceText(v string) bool { return presenceTextRe.MatchString(v) }
