package domain

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 節ごとのハッシュは、その節の中身だけで決まる。
func TestNewSectionHashes(t *testing.T) {
	body := "# IM-0001 題\n\n## 背景\n\nあれこれ。\n\n## 受け入れ条件\n\n- [ ] A\n\n## 検証コマンド\n\n```\ngo test ./...\n```\n"
	h := NewSectionHashes(body)
	if h.Acceptance == "" || h.Verify == "" {
		t.Fatalf("節のハッシュが取れていません: %+v", h)
	}
	if h.Empty() {
		t.Errorf("Empty が true です: %+v", h)
	}

	// 節の外を直してもハッシュは変わらない（本文の別の場所の更新を「節の更新」と読まない）
	if g := NewSectionHashes(body + "\n## 追記\n\nメモ。\n"); g.Acceptance != h.Acceptance || g.Verify != h.Verify {
		t.Errorf("節の外の更新でハッシュが変わりました: %+v → %+v", h, g)
	}
	// CRLF と前後の空行では変わらない
	crlf := NewSectionHashes("# IM-0001 題\r\n\r\n## 受け入れ条件\r\n\r\n- [ ] A\r\n\r\n\r\n## 検証コマンド\r\n\r\n```\r\ngo test ./...\r\n```\r\n")
	if crlf.Acceptance != h.Acceptance || crlf.Verify != h.Verify {
		t.Errorf("CRLF でハッシュが変わりました: %+v → %+v", h, crlf)
	}
	// 節の中身を直せば、その節だけが変わる
	g := NewSectionHashes(strings.ReplaceAll(body, "- [ ] A", "- [ ] A（直した）"))
	if g.Acceptance == h.Acceptance {
		t.Errorf("受け入れ条件を直したのにハッシュが同じです: %s", g.Acceptance)
	}
	if g.Verify != h.Verify {
		t.Errorf("受け入れ条件を直したら検証コマンドのハッシュも変わりました: %s → %s", h.Verify, g.Verify)
	}
	// 節が無ければ空・英語の別名でも取れる
	if g := NewSectionHashes("# 題\n\n本文だけ。\n"); !g.Empty() {
		t.Errorf("節が無いのにハッシュが出ました: %+v", g)
	}
	if g := NewSectionHashes("## Acceptance criteria\n\n- [ ] A\n\n## Verify commands\n\n- `go vet ./...`\n"); g.Empty() {
		t.Errorf("英語の別名の見出しを読めていません")
	}
}

// SectionDrift は「受け入れ条件は変わったのに検証コマンドは起票時のまま」だけを true にする。
func TestSectionDrift(t *testing.T) {
	base := SectionHashes{Acceptance: "a1", Verify: "v1"}
	cases := []struct {
		name string
		base SectionHashes
		now  SectionHashes
		want bool
	}{
		{"受け入れ条件だけ変わった（警告）", base, SectionHashes{Acceptance: "a2", Verify: "v1"}, true},
		{"どちらも変わっていない", base, SectionHashes{Acceptance: "a1", Verify: "v1"}, false},
		{"どちらも変わった", base, SectionHashes{Acceptance: "a2", Verify: "v2"}, false},
		{"検証コマンドだけ変わった", base, SectionHashes{Acceptance: "a1", Verify: "v2"}, false},
		{"起票時の記録が無い（古いイシュー。判定しない）", SectionHashes{}, SectionHashes{Acceptance: "a2", Verify: "v1"}, false},
		{"起票時に検証コマンドの節が無い", SectionHashes{Acceptance: "a1"}, SectionHashes{Acceptance: "a2", Verify: "v1"}, false},
		{"いま検証コマンドの節が無い", base, SectionHashes{Acceptance: "a2"}, false},
		{"起票時に受け入れ条件の節が無い", SectionHashes{Verify: "v1"}, SectionHashes{Acceptance: "a2", Verify: "v1"}, false},
		{"いま受け入れ条件の節が無い", base, SectionHashes{Verify: "v1"}, false},
	}
	for _, c := range cases {
		if got := SectionDrift(c.base, c.now); got != c.want {
			t.Errorf("%s: SectionDrift(%+v, %+v) = %v, want %v", c.name, c.base, c.now, got, c.want)
		}
	}
}

// 実際にあった形（起票時の検証コマンドのまま受け入れ条件だけ更新された）を本文で再現する。
func TestSectionDriftAcceptanceOnly(t *testing.T) {
	filed := "# EX-0101 題\n\n## 受け入れ条件\n\n- [ ] 追跡されていないことを示す\n\n## 検証コマンド\n\n```\nls -la .claude/memories/ && git ls-files .claude/memories/\n```\n"
	// 方針が決まって受け入れ条件だけを書き替え、検証コマンドは起票時のまま残した
	updated := strings.ReplaceAll(filed, "- [ ] 追跡されていないことを示す", "- [ ] private/memories/ へ移し、.claude/memories は symlink にする")
	if !SectionDrift(NewSectionHashes(filed), NewSectionHashes(updated)) {
		t.Errorf("受け入れ条件だけを更新したのに警告が出ません")
	}
	// 検証コマンドも直せば警告は消える
	fixed := strings.ReplaceAll(updated, "ls -la .claude/memories/ && git ls-files .claude/memories/", "bash deploy/public-scan.sh && cat .claude/memories/x")
	if SectionDrift(NewSectionHashes(filed), NewSectionHashes(fixed)) {
		t.Errorf("検証コマンドも直したのに警告が出ています")
	}
}

// SectionDriftMsg は対訳表から出る（日本語と英語の両方に文面がある）。
func TestSectionDriftMsg(t *testing.T) {
	m := SectionDriftMsg("EX-0102")
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		got := m.In(lang)
		if got == "" || got == "domain.verify.section_drift" {
			t.Fatalf("%s の文面が対訳表にありません: %q", lang, got)
		}
		if !strings.Contains(got, "EX-0102") || !strings.Contains(got, "looptrack issue verify EX-0102") {
			t.Errorf("%s の文面に ID かコマンドがありません: %q", lang, got)
		}
	}
	if m.In(i18n.JA) == m.In(i18n.EN) {
		t.Errorf("日本語と英語の文面が同じです（英語が対訳表に入っていない）: %q", m.In(i18n.EN))
	}
}
