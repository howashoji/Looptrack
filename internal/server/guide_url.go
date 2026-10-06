package server

import (
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// guideSiteBase は利用者ガイドのサイトの土台の URL（末尾の / を含む）。サイトは版ごとに <土台>v<版>/ に置く。
// 行き先の URL を作るのはこの定数と guideSiteURL だけなので、土台を変えるときはここだけを直す。
const guideSiteBase = "https://howashoji.github.io/Looptrack/"

// guideSiteTag はサイトに版の置き場がある版の形（公開のリリースのタグ）。
// relver の版より狭い: 試験の配布（v0.0.0-<時刻>-<コミット ID>）や dev にはサイトの置き場が無い。
var guideSiteTag = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-rc\.[1-9][0-9]*)?$`)

// guideSiteURL は利用者メニューの「ガイド」の行き先。公開のリリースの版（プレリリースを含む）ならその版のガイド、
// それ以外（dev・手元のビルド・試験の配布）は最新のリリースへ転送する latest を指す。
// 日本語の利用者には日本語のガイド（末尾の ja/）を返す。
func guideSiteURL(version string, lang i18n.Lang) string {
	dir := "latest/"
	if guideSiteTag.MatchString(version) {
		dir = "v" + strings.TrimPrefix(version, "v") + "/"
	}
	u := guideSiteBase + dir
	if lang == i18n.JA {
		u += "ja/"
	}
	return u
}
