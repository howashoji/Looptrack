package auth

import (
	"crypto/rand"
	"io"
	"math/big"
)

// GeneratedPasswordLen は自動生成するパスワードの文字数。MinPasswordLen より長くして、
// 最短の規則を少し上げても生成した値が規則に落ちないようにする（テストが両者の関係を確かめる）。
const GeneratedPasswordLen = 20

// generatedAlphabet は生成に使う文字。読み違えやすい 0 O o 1 l I を外し、記号は入れない
// （口頭やメモで伝えても崩れず、シェルやフォームでも引用が要らない）。56 文字 × 20 桁で約 116 ビット。
const generatedAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

// randReader は生成の乱数の出どころ。暗号論的な乱数（crypto/rand）だけを使う。テストが差し替えて出どころを確かめる。
var randReader io.Reader = rand.Reader

// GeneratePassword は管理者が利用者に渡すパスワードを生成する。各桁を一様に選ぶ（rand.Int は剰余の偏りを出さない）。
// 返す値はハッシュにする前の平文なので、呼び出し側はログ・記録・URL に載せない。
func GeneratePassword() (string, error) {
	limit := big.NewInt(int64(len(generatedAlphabet)))
	b := make([]byte, GeneratedPasswordLen)
	for i := range b {
		n, err := rand.Int(randReader, limit)
		if err != nil {
			return "", err
		}
		b[i] = generatedAlphabet[n.Int64()]
	}
	return string(b), nil
}
