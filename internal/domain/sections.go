package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 本文の節ごとのハッシュ（DESIGN.md §5-8-4「節ごとのハッシュと「検証コマンドが起票時のまま」の警告」）。
//
// verify の関門は「記録があること」と「終了コードが 0 であること」しか見ないので、
// 起票時に書いた検証コマンドが、その後に更新された受け入れ条件を実証していなくても通る
// （実際に起きた: 受け入れ条件は「別の場所へ移す」に書き替わったのに検証コマンドは起票時のままで、
// 移設の前後で出力の形が同じだったため 1/1 成功として記録された）。
//
// そこで create / update のイベントに「受け入れ条件の節」と「検証コマンドの節」のハッシュを残し、
// **受け入れ条件は変わったのに検証コマンドは起票時のまま**という食い違いを警告する。
// 判定は SectionDrift の 1 か所だけに置く（経路ごとに書かない）。
//
// 既存のイシューのイベントには節のハッシュが無いので、判定できない（Empty なら警告しない）。
// この仕組みより前に起票されたイシューは、これから create / update されるものから効く。

// SectionHashes は issue_events kind create / update の detail.sections。
// 値は節の中身の SHA-256（16 進）。その節が本文に無ければ空。
type SectionHashes struct {
	Acceptance string `json:"acceptance,omitempty"`
	Verify     string `json:"verify,omitempty"`
}

// Empty は節のハッシュを 1 つも持たないか（この仕組みより前の記録は必ず Empty）。
func (h SectionHashes) Empty() bool { return h.Acceptance == "" && h.Verify == "" }

// NewSectionHashes は本文（frontmatter とコメント節を除いた body_main）から節ごとのハッシュを作る。
func NewSectionHashes(bodyMain string) SectionHashes {
	return SectionHashes{
		Acceptance: sectionSHA256(bodyMain, AcceptanceHeading),
		Verify:     sectionSHA256(bodyMain, VerifyHeading),
	}
}

// sectionSHA256 は節の中身の SHA-256（16 進）。節が無ければ空。
// 改行の種類（CRLF / LF）と前後の空行では変わらない（SectionLines が CRLF をそろえ、ここで前後の空白を落とす）。
// 本文の別の場所を直しただけで「節が更新された」と読まないよう、節の中身だけを見る。
func sectionSHA256(bodyMain string, heading *regexp.Regexp) string {
	lines, found := SectionLines(bodyMain, heading)
	if !found {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.Join(lines, "\n"))))
	return hex.EncodeToString(sum[:])
}

// SectionDrift は「受け入れ条件の節は起票の後に更新されたのに、検証コマンドの節は起票時のまま」か。
// base は節のハッシュを持つ最初の記録（起票時）、now は現在の本文。
//
// 次のどれかに当たれば判定しない（false）。警告を出すのは、食い違いが確実に読み取れるときだけにする:
//   - base が空（この仕組みより前に起票されたイシュー。過去の分は判定しない）
//   - どちらかの側で検証コマンドの節が無い（verify の関門そのものが働かない）
//   - どちらかの側で受け入れ条件の節が無い（更新されたかを比べられない）
func SectionDrift(base, now SectionHashes) bool {
	if base.Empty() {
		return false
	}
	if base.Verify == "" || now.Verify == "" || base.Acceptance == "" || now.Acceptance == "" {
		return false
	}
	return now.Acceptance != base.Acceptance && now.Verify == base.Verify
}

// SectionDriftMsg は食い違いの注記の文面（CLI・サーバ・MCP で同じ 1 か所から出す）。
func SectionDriftMsg(id string) i18n.Msg {
	return i18n.M("domain.verify.section_drift", "id", id, "command", VerifyCommand(id))
}
