package setupwiz

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

var composePullPolicyRe = regexp.MustCompile(`(?m)^ {4}pull_policy: (\S+)$`)

// composeBuildsLocally は、compose.yaml の looptrack サービスが build を持ち、pull_policy: build でレジストリから引かないかを返す。
// build だけでは、compose v5 は up のときビルドの前に同じ名前のイメージをレジストリから引きにいく（実測で Pulling が出た）。
func composeBuildsLocally(compose string) bool {
	m := composePullPolicyRe.FindStringSubmatch(compose)
	return strings.Contains(compose, "\n    build:\n") && m != nil && m[1] == "build"
}

// composeComments は compose.yaml の注釈（# の行）だけを返す。
func composeComments(compose string) string {
	var b strings.Builder
	for _, line := range strings.Split(compose, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// 生成する compose.yaml は、レジストリから同じ名前のイメージを引かず、手元の Dockerfile からだけ作る。
// 引くようになると、既定のイメージ名（looptrack:latest）と同じ名前のイメージを外で公開した人のものが動くおそれがある。
func TestComposeNeverPullsTheImage(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	for _, store := range []string{StoreSQLite, StoreMySQL} {
		for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
			p := &Plan{Port: 9000, PublicURL: "https://x.example", BasePath: "/looptrack", Store: store}
			got := renderCompose(p, now, lang)
			if !composeBuildsLocally(got) {
				t.Errorf("compose.yaml（%s・%s）が pull_policy: build を持たない:\n%s", store, lang, got)
			}
			// 注釈は実物の指定の名前を挙げる（指定を外したときに、注釈だけが嘘になって残らないようにする）
			if !strings.Contains(composeComments(got), "pull_policy: build") {
				t.Errorf("注釈が pull_policy: build を挙げていない（%s・%s）:\n%s", store, lang, got)
			}
			// 対照: 指定の行を外せば検査が落ちる（上の検査が何も見ていないのではないことを確かめる）
			without := strings.Replace(got, "    pull_policy: build\n", "", 1)
			if without == got || composeBuildsLocally(without) {
				t.Fatalf("pull_policy の行を外しても検査が通る（検査の前提が崩れています）:\n%s", without)
			}
		}
	}
}
