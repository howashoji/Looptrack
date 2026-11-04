package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

// 生成したパスワードは最短の規則を満たし、決めた文字だけでできていて、そのままハッシュにできる。
func TestGeneratePasswordShape(t *testing.T) {
	if GeneratedPasswordLen < MinPasswordLen {
		t.Fatalf("生成の長さ %d が最短 %d より短い", GeneratedPasswordLen, MinPasswordLen)
	}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(pw)); n != GeneratedPasswordLen {
			t.Fatalf("長さ = %d", n)
		}
		for _, r := range pw {
			if !strings.ContainsRune(generatedAlphabet, r) {
				t.Fatalf("決めた文字の外の %q を含む", r)
			}
		}
		if strings.ContainsAny(pw, "0Oo1lI") {
			t.Fatal("読み違えやすい文字を含む")
		}
		if seen[pw] {
			t.Fatal("200 回の生成で同じ値が出た")
		}
		seen[pw] = true
	}
	pw, _ := GeneratePassword()
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("生成した値がハッシュの規則に落ちる: %v", err)
	}
	if ok, err := VerifyPassword(h, pw); err != nil || !ok {
		t.Fatalf("生成した値で照合できない: %v %v", ok, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

// 乱数の出どころは crypto/rand で、生成は必ずそこから読む。差し替えると結果がそれに従う（対照）。
func TestGeneratePasswordRandomSource(t *testing.T) {
	if randReader != rand.Reader {
		t.Fatal("既定の乱数の出どころが crypto/rand.Reader ではない")
	}
	orig := randReader
	t.Cleanup(func() { randReader = orig })

	randReader = failingReader{}
	if pw, err := GeneratePassword(); err == nil || pw != "" {
		t.Fatalf("出どころが失敗しても値を返した: %q %v", pw, err)
	}

	// 同じバイト列なら同じ値になる: 出どころ以外から乱数を取っていない
	src := bytes.Repeat([]byte{0x5a, 0x13, 0xc7, 0x02}, 64)
	randReader = bytes.NewReader(src)
	a, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	randReader = bytes.NewReader(src)
	b, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("同じ乱数の列から違う値が出た（ほかの出どころを使っている）")
	}
}
