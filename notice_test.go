package looptrack

import (
	"strings"
	"testing"
)

// NOTICE に埋め込んだフォントの OFL・Go のライセンス・AppImage の runtime・ソースの入手先が入っていること
// （中身が依存と合っているかは CI の notice ジョブが確かめる）。
func TestNoticeEmbedded(t *testing.T) {
	for _, want := range []string{
		"SIL OPEN FONT LICENSE Version 1.1", "Go standard library and runtime", "go run ./internal/tools/notice",
		"AppImage type2-runtime", // AppImage の先頭に付く runtime（MIT）
		"Source (this version): https://proxy.golang.org/github.com/go-sql-driver/mysql/@v/", // MPL-2.0 のソースの入手先
		"このモジュールは MPL-2.0 です。上の URL からソースを取得できます。",
	} {
		if !strings.Contains(Notice, want) {
			t.Errorf("NOTICE に %q がありません", want)
		}
	}
}
