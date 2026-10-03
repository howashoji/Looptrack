package server

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// wrongArticleRe は「a」の後ろに母音で始まる語が来る形（a admin・a editor）。
var wrongArticleRe = regexp.MustCompile(`(?i)\ba\s+[aeiou]\w*`)

// mutedSpanRe はプロジェクトの見出しの注釈（slug・接頭辞・自分の役割）。
var mutedSpanRe = regexp.MustCompile(`<span class="muted">[^<]*</span>`)

// 管理画面のプロジェクト一覧が自分の役割を出す文面は、役割の名前（viewer・editor・admin）に関わらず英語として正しい。
// 冠詞を付けた文面（a {role}）は、母音で始まる admin・editor で誤る。日本語の文面は変わらない。
func TestAdminProjectsMyRoleReadsCorrectlyInEnglish(t *testing.T) {
	e := newEnv(t)
	root := e.user("root", "root-password-12", "admin")
	for _, role := range service.MemberRoles {
		pr := e.project("p-" + role)
		if err := store.SetMember(context.Background(), e.db, pr.ID, root.ID, role); err != nil {
			t.Fatal(err)
		}
	}

	// 対照: 検査の正規表現は、直す前の文面（a admin）を誤りとして拾う（上の検査が何も見ていないのではないことを確かめる）
	if !wrongArticleRe.MatchString("you are a admin here") || wrongArticleRe.MatchString("your role here: admin") {
		t.Fatal("冠詞の検査の前提が崩れています")
	}

	en := enClient()
	e.enroll(en, "root", "root-password-12")
	res, page := e.get(en, "/im/admin/projects")
	if res.StatusCode != 200 {
		t.Fatalf("英語の管理画面: %d", res.StatusCode)
	}
	for _, role := range service.MemberRoles {
		want := "your role here: " + role
		if !strings.Contains(page, want) {
			t.Errorf("英語の画面に %q が出ない:\n%s", want, page)
		}
	}
	// プロジェクトの見出しの注釈（<span class="muted">…</span>）に、誤った冠詞が無い
	spans := mutedSpanRe.FindAllString(page, -1)
	if len(spans) < len(service.MemberRoles) {
		t.Fatalf("見出しの注釈が %d 件しか取れない:\n%s", len(spans), page)
	}
	for _, sp := range spans {
		if wrongArticleRe.MatchString(sp) {
			t.Errorf("見出しの注釈に誤った冠詞がある: %s", sp)
		}
	}

	// 日本語は変えない
	res, page = e.get(en, "/im/admin/projects", "Accept-Language", "ja") // 同じ利用者・同じ画面を、要求の言語だけ日本語にする
	if res.StatusCode != 200 {
		t.Fatalf("日本語の管理画面: %d", res.StatusCode)
	}
	for _, role := range service.MemberRoles {
		if want := "自分は " + role + " で参加"; !strings.Contains(page, want) {
			t.Errorf("日本語の画面に %q が出ない:\n%s", want, page)
		}
	}
}
