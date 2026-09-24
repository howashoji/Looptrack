//go:build windows

package privfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows: 書いたファイルの DACL が本人だけ（継承なし）であることと、他の利用者に読める ACE の拒否。
// CI の Windows で動かす（internal/client/cred/fs_windows_test.go と同じ要領）。

// aces は許可の ACE の SID と、継承専用（INHERIT_ONLY_ACE）でない対象自身に効くものの数、DACL が保護されているかを返す。
func aces(t *testing.T, path string) (sids []*windows.SID, effective int, protected bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL が無い: %v", err)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE {
			sids = append(sids, (*windows.SID)(unsafe.Pointer(&ace.SidStart)))
			if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 {
				effective++
			}
		}
	}
	return sids, effective, ctrl&windows.SE_DACL_PROTECTED != 0
}

func TestWindowsOwnerOnlyACL(t *testing.T) {
	dir := t.TempDir()
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".env")
	if err := WriteFile(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	f, err := CreateTemp(dir, ".k.setup-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	renamed := filepath.Join(dir, "im.db.secret-key")
	if err := os.Rename(f.Name(), renamed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p, renamed} {
		sids, effective, protected := aces(t, path)
		if !protected {
			t.Errorf("%s の DACL が親から継承している", path)
		}
		if effective != 1 {
			t.Errorf("%s の DACL で対象自身に効く ACE が 1 つでない: %d（%v）", path, effective, sids)
		}
		for _, sid := range sids {
			if !sid.Equals(me) {
				t.Errorf("%s の DACL が本人だけでない: %v", path, sids)
				break
			}
		}
		if err := Check(path); err != nil {
			t.Errorf("本人だけのファイルを拒否した: %v", err)
		}
	}

	// Everyone に読み取りを許すと拒否する
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	var pe *PermError
	if err := Check(p); !errors.As(err, &pe) {
		t.Errorf("Everyone が読めるファイル: err = %v, want PermError", err)
	}
}

// MkdirAll（ProtectDir）したディレクトリに、セキュリティ記述子を指定せずに作ったファイル（Windows の SQLite の
// -wal・-shm と同じ作り方）は、本人だけの ACE を継承して Check を通る。
func TestWindowsProtectDirInherits(t *testing.T) {
	d := filepath.Join(t.TempDir(), "data")
	if err := MkdirAll(d); err != nil {
		t.Fatal(err)
	}
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	// 継承つきの ACE は、Windows が「自身に効く ACE」と「継承専用（INHERIT_ONLY）の ACE」の 2 つに分けて保存することがあるので、
	// 数ではなく「すべて本人・自身に効くものが 1 つ以上」で確かめる。
	sids, effective, protected := aces(t, d)
	if !protected || effective < 1 {
		t.Errorf("ディレクトリの DACL: protected=%v effective=%d, want 継承なし・本人に効く ACE あり", protected, effective)
	}
	for _, sid := range sids {
		if !sid.Equals(me) {
			t.Errorf("ディレクトリに本人以外の ACE: %v", sid)
		}
	}
	f := filepath.Join(d, "im.db-wal")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Check(f); err != nil {
		t.Errorf("継承した ACL のファイル: %v", err)
	}
	sids, _, _ = aces(t, f)
	for _, sid := range sids {
		if !sid.Equals(me) {
			t.Errorf("中のファイルに本人以外の ACE: %v", sid)
		}
	}
}
