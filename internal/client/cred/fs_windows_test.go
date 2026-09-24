//go:build windows

package cred

import (
	"errors"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// Windows: 書いたファイルとディレクトリの DACL が本人だけ（継承なし）であることと、他の利用者に読める ACE の拒否。
// CI の Windows で動かす。

// aces は許可の ACE の SID を返す。effective は継承専用（INHERIT_ONLY_ACE）でない、その対象自身に効くものの数。
//
// ディレクトリに「GENERIC_ALL・子へ継承」の ACE を 1 つ付けると、Windows は汎用の権限を対象自身には写像できないため、
// 「対象自身に効く FILE_ALL_ACCESS（継承なし）」と「子へ継承するだけの GENERIC_ALL（INHERIT_ONLY）」の 2 つに分けて保存する
// （同じ SID が 2 つ並ぶ。CI の Windows で確認）。どちらも本人なら本人だけ。
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
	s := newStore(t)
	if err := s.SaveEntry("https://a/im", jsonorder.NewObject().Set("token", "imp_x")); err != nil {
		t.Fatal(err)
	}
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Paths.Primary, dirOf(s.Paths.Primary)} {
		sids, effective, protected := aces(t, p)
		if !protected {
			t.Errorf("%s の DACL が親から継承している", p)
		}
		if effective != 1 {
			t.Errorf("%s の DACL で対象自身に効く ACE が 1 つでない: %d（%v）", p, effective, sids)
		}
		for _, sid := range sids {
			if !sid.Equals(me) {
				t.Errorf("%s の DACL が本人だけでない: %v", p, sids)
				break
			}
		}
	}
	if err := CheckPrivate(s.Paths.Primary); err != nil {
		t.Errorf("本人だけのファイルを拒否した: %v", err)
	}
}

func TestWindowsRejectsEveryoneReadable(t *testing.T) {
	s := newStore(t)
	seed(t, s, `{}`)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone)},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(s.Paths.Primary, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.Load()
	var pe *PermError
	if !errors.As(err, &pe) {
		t.Fatalf("Everyone が読めるファイルを拒否しない: %v", err)
	}
}

func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid, nil
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '\\' || p[i] == '/' {
			return p[:i]
		}
	}
	return p
}
