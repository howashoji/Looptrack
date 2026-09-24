package cli

import "testing"

// Windows のエディタで保存した作業コピー（BOM・CRLF・ANSI）を push 前に揃える。
func TestNormalizeWork(t *testing.T) {
	ref := "---\nid: X-0001\n---\n\n# 本文\n"
	cases := []struct {
		name, text, want string
		fail             bool
	}{
		{"そのまま", ref, ref, false},
		{"BOM を外す", "\xef\xbb\xbf" + ref, ref, false},
		{"CRLF を LF に", "---\r\nid: X-0001\r\n---\r\n\r\n# 本文\r\n", ref, false},
		{"BOM と CRLF", "\xef\xbb\xbf---\r\nid: X-0001\r\n---\r\n\r\n# 本文 追記\r\n", "---\nid: X-0001\n---\n\n# 本文 追記\n", false},
		{"ANSI（Shift_JIS）は誤り", "---\n# \x96\x7b\x95\xb6\n", "", true},
	}
	for _, c := range cases {
		got, err := normalizeWork(c.text, ref)
		if (err != nil) != c.fail || got != c.want {
			t.Errorf("%s: %q, %v（期待 %q）", c.name, got, err, c.want)
		}
	}
	// 元の本文が CRLF・BOM を含むなら変えない（サーバの本文をそのまま往復させる）
	crlf := "\xef\xbb\xbf---\r\nid: X-0001\r\n---\r\n"
	if got, err := normalizeWork(crlf, crlf); err != nil || got != crlf {
		t.Errorf("元が CRLF・BOM: %q, %v", got, err)
	}
}
