package textenc

import "testing"

func TestDecode(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
		want string
	}{
		{"UTF-8", []byte(`{"a": "日本"}`), `{"a": "日本"}`},
		{"UTF-8 の BOM", append([]byte{0xEF, 0xBB, 0xBF}, `{"a": 1}`...), `{"a": 1}`},
		// PowerShell 5.1 の `>` が書く形（UTF-16LE・BOM 付き・CRLF）
		{"UTF-16LE", []byte{0xFF, 0xFE, '{', 0, '"', 0, 0xE5, 0x65, '"', 0, '}', 0, '\r', 0, '\n', 0}, "{\"日\"}\r\n"},
		{"UTF-16BE", []byte{0xFE, 0xFF, 0, 'o', 0, 'k', 0xD8, 0x3D, 0xDE, 0x00}, "ok😀"},
		{"半端なバイト", []byte{0xFF, 0xFE, 'a', 0, 'b'}, "a�"},
		{"空", nil, ""},
	} {
		if got := string(Decode(c.in)); got != c.want {
			t.Errorf("%s: %q（期待 %q）", c.name, got, c.want)
		}
	}
}
