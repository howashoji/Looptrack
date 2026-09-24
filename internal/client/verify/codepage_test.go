package verify

import (
	"strings"
	"testing"
)

// Windows の ANSI コードページの出力（cmd.exe の組み込みコマンドなど）は読み直す。
func TestOutputLegacyCodePage(t *testing.T) {
	old := codePage
	defer func() { codePage = old }()
	// 「ファイルが見つかりません」の cp932
	sjis := []byte("\x83\x74\x83\x40\x83\x43\x83\x8b\x82\xaa\x8c\xa9\x82\xc2\x82\xa9\x82\xe8\x82\xdc\x82\xb9\x82\xf1\r\n")
	codePage = func() int { return 932 }
	if out := Output(sjis); out != "ファイルが見つかりません\r\n" {
		t.Errorf("cp932 の出力: %q", out)
	}
	if out := Output(append([]byte("password=secret "), sjis...)); !strings.HasPrefix(out, "password=*** ファイル") {
		t.Errorf("読み直した後もマスクする: %q", out)
	}
	// UTF-8 の出力・途中で切れた UTF-8 は読み直さない
	if out := Output([]byte("あい")[1:]); out != "い" {
		t.Errorf("途中で切れた UTF-8: %q", out)
	}
	if out := Output([]byte("見つかりません\n")); out != "見つかりません\n" {
		t.Errorf("UTF-8: %q", out)
	}
	// cp932 としても不正なら従来どおり置き換える
	if out := Output([]byte("a\x82")); out != "a�" {
		t.Errorf("cp932 としても不正: %q", out)
	}
	// 他の OS（コードページ 0）・UTF-8（65001）は読み直さない
	for _, cp := range []int{0, 65001} {
		codePage = func() int { return cp }
		if out := Output(sjis); !strings.Contains(out, "�") {
			t.Errorf("コードページ %d: %q", cp, out)
		}
	}
}

func TestLegacyEncodingKnownPages(t *testing.T) {
	for _, cp := range []int{932, 936, 949, 950, 874, 1250, 1251, 1252, 1253, 1254, 1255, 1256, 1257, 1258} {
		if legacyEncoding(cp) == nil {
			t.Errorf("コードページ %d の符号化が無い", cp)
		}
	}
	if legacyEncoding(65001) != nil || legacyEncoding(0) != nil {
		t.Error("UTF-8・0 は読み直さない")
	}
}
