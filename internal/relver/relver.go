// Package relver は looptrack の版（リリースの版）の読み取りと比較。サーバ（導入済み通知の版の判定・
// 配布ディレクトリの一覧）とクライアント（self-update）の両方が使うので、internal/client の外に置く。
//
// 比べられる版は semver の形だけ: vMAJOR.MINOR.PATCH と、その後ろの -<プレリリース>（v1.0.0-rc.1）と +<ビルド情報>（比較に使わない）。
// サーバの配布ディレクトリに置く試験の配布は、Go の擬似版と同じ v0.0.0-<UTC の年月日時分秒>-<コミット ID> にする（RELEASE.md §2-1）。
// プレリリースの比較は semver の規則（数字だけの識別子は数として、それ以外は ASCII 順。数字は英字より前。識別子が多い方が後）なので、
// 擬似版は時刻の順に並ぶ。
//
// それ以外（ビルドの既定の "dev"・deploy/build.sh の「日付-コミット ID」）は比べられない版として扱い、呼ぶ側は判定を省く
// （古いとも新しいとも言わない。手元のビルドに更新を迫らない）。
package relver

import (
	"strconv"
	"strings"
)

// Version は読み取った版。
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // プレリリースの識別子（無ければ nil）
	Raw                 string
}

// Parse は版を読む。先頭の v は無くてもよい。semver の形でなければ ok = false。
func Parse(s string) (Version, bool) {
	raw := s
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
		if pre == "" {
			return Version{}, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var nums [3]int
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	v := Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Raw: raw}
	if pre != "" {
		for _, id := range strings.Split(pre, ".") {
			if id == "" {
				return Version{}, false
			}
			for _, r := range id {
				if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
					return Version{}, false
				}
			}
			v.Pre = append(v.Pre, id)
		}
	}
	return v, true
}

// Valid は比べられる版か。
func Valid(s string) bool {
	_, ok := Parse(s)
	return ok
}

// Compare は a と b を比べる（a < b なら -1、等しければ 0、a > b なら 1）。どちらかが比べられない版なら ok = false。
func Compare(a, b string) (int, bool) {
	va, ok1 := Parse(a)
	vb, ok2 := Parse(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	return va.Compare(vb), true
}

// Compare は semver の順序で比べる。
func (a Version) Compare(b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			return sign(d[0] - d[1])
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1 // 正式版はプレリリースより後
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := compareIdent(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	return sign(len(a.Pre) - len(b.Pre))
}

func compareIdent(a, b string) int {
	na, ea := strconv.ParseUint(a, 10, 64)
	nb, eb := strconv.ParseUint(b, 10, 64)
	switch {
	case ea == nil && eb == nil:
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		}
		return 0
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Older は a が b より古いか（どちらかが比べられない版なら false）。
func Older(a, b string) bool {
	c, ok := Compare(a, b)
	return ok && c < 0
}
