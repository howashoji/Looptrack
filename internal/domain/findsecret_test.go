package domain

import (
	"strings"
	"testing"
)

// 添付のテキストを送る前の検査（FindSecret）は、マスク（MaskSecrets）と同じ規則に当たり、マスク済みの形には当たらない。
func TestFindSecret(t *testing.T) {
	hits := []struct {
		in    string
		line  int
		label string
	}{
		{"ok\nexport TOKEN=abc123\n", 2, "TOKEN=…"},
		{"password: hunter2", 1, "password: …"},
		{"a\nb\nAuthorization: Bearer eyJhbGciOi\n", 3, "Bearer …"},
		{"key imp_AbCdEf123", 1, "imp_…"},
		{"x sk-" + strings.Repeat("a", 24), 1, "sk-…"},
		{"AKIA" + strings.Repeat("A", 16), 1, "AKIA…"},
		{"\n\n-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\n", 3, "-----BEGIN RSA PRIVATE KEY-----"},
		// マスク済みの行の後ろにある生の値は見つける（最初の 1 件で止めない）
		{"token=***\ntoken=raw-value\n", 2, "token=…"},
	}
	for _, c := range hits {
		f, ok := FindSecret(c.in)
		if !ok || f.Line != c.line || f.Label != c.label {
			t.Errorf("%q → %+v %v, want line %d %q", c.in, f, ok, c.line, c.label)
		}
		if strings.Contains(f.Label, "hunter2") || strings.Contains(f.Label, "abc123") || strings.Contains(f.Label, "raw-value") {
			t.Errorf("label に値が入った: %q", f.Label)
		}
		// マスクした後は当たらない（CLI がマスクして送る出力の全文はここを通る）
		if f, ok := FindSecret(MaskSecrets(c.in)); ok {
			t.Errorf("マスクした後も当たった: %q → %+v", MaskSecrets(c.in), f)
		}
	}
	for _, clean := range []string{"", "PASS\nok  \tpkg\t0.1s\n", "tokens are counted", "token=***", "Bearer ***"} {
		if f, ok := FindSecret(clean); ok {
			t.Errorf("%q に当たった: %+v", clean, f)
		}
	}
}
