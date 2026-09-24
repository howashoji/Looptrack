// Package privfile は本人だけが読み書きできるファイル（.env・秘密鍵のファイル・SQLite の DB）を書く・確かめる。
//
// unix ではパーミッション 0600。Windows では DACL を「本人に全権」の 1 つだけにし、親からの継承を切る
// （PROTECTED_DACL。internal/client/cred の Windows の保護と同じ方式・DESIGN.md §5-11 Q7）。
// 書くときは同じディレクトリの一時ファイルを先に保護してから中身を書き、rename で置く（rename しても保護は保たれる）。
package privfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PermError は本人以外が読める（または権限を書き換えられる）ファイルであること。
type PermError struct{ Path string }

func (e *PermError) Error() string {
	return fmt.Sprintf("%s は本人以外も読めます（unix はパーミッション 600、Windows は本人だけの ACL にしてください）", e.Path)
}

// CreateTemp は dir に本人だけが読み書きできる一時ファイルを作る（os.CreateTemp と同じ pattern）。
// 中身を書く前に保護するので、書きかけの秘密が他の利用者に読まれない。
func CreateTemp(dir, pattern string) (*os.File, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	if err := protect(f); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// WriteFile は data を本人だけが読み書きできるファイルとして path に置く（一時ファイルに書いて rename。既存は置き換える）。
func WriteFile(path string, data []byte) error {
	f, err := CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Check は path が本人だけのファイルか確かめる（本人以外が読めれば *PermError）。
func Check(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return check(path)
}

// CreateEmpty は path に本人だけが読み書きできる空のファイルを作る（既にあれば何もせず false を返す。権限も変えない）。
// SQLite の DB を開く前に使う（空のファイルは SQLite が新しい DB として扱う）。
func CreateEmpty(path string) (created bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := protect(f); err != nil {
		f.Close()
		os.Remove(path)
		return false, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return false, err
	}
	return true, nil
}

// ProtectDir は dir を本人だけにする。unix は 0700。Windows は DACL を「本人に全権」の 1 つだけ（中に作るファイル・
// ディレクトリにも継承させる）にして親からの継承を切る。中に後から作られるファイルのうち、作る側が権限を指定しないもの
// （Windows の SQLite の -wal・-shm）も本人だけになる。新しく作ったディレクトリにだけ使う（既存の共有の場所を狭めない）。
func ProtectDir(dir string) error { return protectDir(dir) }

// MkdirAll は dir を作る（途中の階層も）。dir を新しく作ったときは本人だけにする（ProtectDir）。既にあるディレクトリの権限は変えない。
func MkdirAll(dir string) error {
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s はディレクトリではありません", dir)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return protectDir(dir)
}
