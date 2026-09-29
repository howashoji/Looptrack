package dashstdin

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveLiteral(t *testing.T) {
	// "-" 以外の値はそのまま返る（標準入力には触れない）。
	raw, used, err := Resolve("原因が分かった。", strings.NewReader("読まれてはいけない"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if used {
		t.Fatalf("used = true（「-」以外なのに標準入力を読んだ）")
	}
	if string(raw) != "原因が分かった。" {
		t.Fatalf("raw = %q", raw)
	}
}

func TestResolveLiteralEmpty(t *testing.T) {
	// 空文字列（フラグを省略した既定値）も "-" ではないのでそのまま返る。
	raw, used, err := Resolve("", strings.NewReader("読まれてはいけない"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if used {
		t.Fatalf("used = true（空文字列は「-」ではない）")
	}
	if len(raw) != 0 {
		t.Fatalf("raw = %q", raw)
	}
}

func TestResolveDashReadsStdin(t *testing.T) {
	raw, used, err := Resolve(Dash, strings.NewReader("標準入力の本文\n2 行目"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !used {
		t.Fatalf("used = false（「-」なのに標準入力を読まなかった）")
	}
	if string(raw) != "標準入力の本文\n2 行目" {
		t.Fatalf("raw = %q", raw)
	}
}

func TestResolveDashEmptyStdin(t *testing.T) {
	raw, used, err := Resolve(Dash, strings.NewReader(""))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !used {
		t.Fatalf("used = false")
	}
	if len(raw) != 0 {
		t.Fatalf("raw = %q（空の標準入力のはず）", raw)
	}
}

func TestResolveDashNilStdin(t *testing.T) {
	raw, used, err := Resolve(Dash, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !used {
		t.Fatalf("used = false")
	}
	if raw != nil {
		t.Fatalf("raw = %q", raw)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("読めません") }

func TestResolveDashReadError(t *testing.T) {
	_, used, err := Resolve(Dash, errReader{})
	if !used {
		t.Fatalf("used = false")
	}
	if err == nil {
		t.Fatal("err が nil です（標準入力の読み取り失敗を伝えられません）")
	}
}
