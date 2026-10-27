//go:build windows

package cred

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// 資格情報の置き換えと、ロックを取らずに読む側が重なったとき（更新の途中にほかの要求が読む）。

// openWithoutShareDelete は os.Open と同じく削除の共有なしで path を開く（置き換えを妨げる読み手）。
func openWithoutShareDelete(t *testing.T, path string) windows.Handle {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func savedToken(t *testing.T, s *Store) string {
	t.Helper()
	e, err := s.Entry("https://a/im")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := e.Get("token")
	str, _ := v.(string)
	return str
}

// TestWindowsReplaceWaitsForReader: 読み手が開いている間に保存しても、読み手が閉じれば置き換えが通る。
func TestWindowsReplaceWaitsForReader(t *testing.T) {
	s := newStore(t)
	if err := s.SaveEntry("https://a/im", jsonorder.NewObject().Set("token", "imp_old")); err != nil {
		t.Fatal(err)
	}
	h := openWithoutShareDelete(t, s.Paths.Primary)
	go func() {
		time.Sleep(200 * time.Millisecond)
		windows.CloseHandle(h)
	}()
	if err := s.SaveEntry("https://a/im", jsonorder.NewObject().Set("token", "imp_new")); err != nil {
		t.Fatalf("読み手が閉じた後も置き換えられない: %v", err)
	}
	if got := savedToken(t, s); got != "imp_new" {
		t.Errorf("保存した値が残っていない: %q", got)
	}
}

// TestWindowsReplaceGivesUpAfterLimit: 読み手が閉じないままなら上限で諦め、元の内容を残す。
// 開いた読み手が置き換えを本当に妨げることの対照も兼ねる（前提が崩れていれば ERROR_ACCESS_DENIED にならない）。
func TestWindowsReplaceGivesUpAfterLimit(t *testing.T) {
	old := replaceRetryFor
	replaceRetryFor = 100 * time.Millisecond
	t.Cleanup(func() { replaceRetryFor = old })
	s := newStore(t)
	if err := s.SaveEntry("https://a/im", jsonorder.NewObject().Set("token", "imp_old")); err != nil {
		t.Fatal(err)
	}
	h := openWithoutShareDelete(t, s.Paths.Primary)
	err := s.SaveEntry("https://a/im", jsonorder.NewObject().Set("token", "imp_new"))
	windows.CloseHandle(h)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("前提が崩れています: 削除の共有なしで開いたファイルの置き換えが拒否されませんでした（%v）", err)
	}
	if got := savedToken(t, s); got != "imp_old" {
		t.Errorf("諦めた後に元の内容が残っていない: %q", got)
	}
	entries, err := os.ReadDir(dirOf(s.Paths.Primary))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filepath.Base(s.Paths.Primary)+".tmp") {
			t.Errorf("一時ファイルが残っている: %s", e.Name())
		}
	}
}
