//go:build windows

package cred

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"

	"github.com/howashoji/looptrack/internal/privfile"
)

// Windows のファイルの保護（DESIGN.md §5-11 Q7: ファイル + ACL を本人だけ）。ACL の作り方と確かめ方は internal/privfile
// （サーバの .env・SQLite の DB と同じ）。
//
// 書くとき: DACL を「本人に全権」の 1 つだけにし、親からの継承を切る（PROTECTED_DACL）。ディレクトリは作るときに
// 同じ DACL を子へ継承する形で付ける（中に作る一時ファイル・ロックのファイルも本人だけになる）。
// 読むとき: 本人・SYSTEM・Administrators 以外に読み取り（または権限の書き換え）を許す ACE があれば PermError。
// SYSTEM と Administrators は OS の既定で %USERPROFILE% の下を読めるので、旧い置き場を拒否しないよう許す。

func checkPrivate(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	if err := privfile.Check(path); err != nil {
		var pe *privfile.PermError
		if errors.As(err, &pe) {
			return &PermError{path} // 案内（ログインし直す）は cred の文面
		}
		return err
	}
	return nil
}

// mkdirPrivate は無ければ作り、作ったディレクトリの DACL を本人だけ（子へ継承）にする。既にあるものは変えない。
func mkdirPrivate(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	return privfile.MkdirAll(dir)
}

// lockFile は path を LockFileEx で排他ロックする（待つ）。
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		windows.UnlockFileEx(h, 0, 1, 0, ol)
		f.Close()
	}, nil
}
