//go:build windows

package privfile

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows の保護（internal/client/cred/fs_windows.go と同じ方式。cred は別の領域なので import せず同じ形で持つ）。
//
// 書くとき: DACL を「本人に全権」の 1 つだけにし、親からの継承を切る（PROTECTED_DACL）。
// 確かめるとき: 本人・SYSTEM・Administrators 以外に読み取り（または権限の書き換え）を許す ACE があれば PermError。
// SYSTEM と Administrators は OS の既定で利用者のフォルダを読めるので許す。

// readMask は「他の利用者が読める」とみなす権限。
const readMask = windows.FILE_READ_DATA | windows.GENERIC_READ | windows.GENERIC_ALL | windows.WRITE_DAC | windows.WRITE_OWNER

func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid, nil
}

// ownerOnlyACL は「本人に全権」の 1 つだけの DACL。inheritance はその ACE を中のファイル・ディレクトリに継承させるか
// （ファイルは NO_INHERITANCE、ディレクトリは SUB_CONTAINERS_AND_OBJECTS_INHERIT）。
func ownerOnlyACL(inheritance uint32) (*windows.ACL, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
}

// setOwnerOnly は path の DACL を本人だけにして親からの継承を切る。
func setOwnerOnly(path string, inheritance uint32) error {
	acl, err := ownerOnlyACL(inheritance)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func protect(f *os.File) error { return setOwnerOnly(f.Name(), windows.NO_INHERITANCE) }

// protectDir はディレクトリを本人だけにし、その ACE を中に作るファイル・ディレクトリに継承させる
// （Windows の SQLite は -wal・-shm をセキュリティ記述子を指定せずに作るので、ディレクトリの継承する ACE で決まる）。
func protectDir(dir string) error {
	return setOwnerOnly(dir, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
}

func check(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil { // DACL が無い・空の DACL は全員に全権
		return &PermError{path}
	}
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(me) || sid.Equals(system) || sid.Equals(admins) {
			continue
		}
		if uint32(ace.Mask)&readMask != 0 {
			return &PermError{path}
		}
	}
	return nil
}
