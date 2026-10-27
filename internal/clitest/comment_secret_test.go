package clitest

import (
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// コメントの秘密の検査（プロジェクト別ルール comment.reject_secret）。判定はサーバの 1 か所で、CLI は
// 拒否（422 comment_secret）ならサーバの文面だけを `エラー: …` で出し、切のプロジェクトの注記（secret_notice）は
// 「コメント追記」の行の後ろに出す。状態の変更は同じ注記を messages の行で受け取る。

// commentSecretValue は秘密の検査に当たる値（偽物）。出力に出ないことを golden で確かめる。
const commentSecretValue = "zq8fakecommentvalue"

const commentSecretText = "原因が分かった。\n設定に token: " + commentSecretValue + " を書いて確かめた。"

// commentSecretFinding はサーバが返す文面に入れる行番号と規則の語（値は入らない）。
func commentSecretFinding() domain.SecretFinding {
	f, ok := domain.FindSecret(commentSecretText)
	if !ok {
		panic("前提が崩れています: commentSecretText が domain.FindSecret に当たりません")
	}
	return f
}

func commentSecretCases() []Case {
	f := commentSecretFinding()
	rejected := ErrorResponse(422, "comment_secret", i18n.T(i18n.JA, "service.err.comment.secret", "id", "DEMO-0001", "line", f.Line, "label", f.Label))
	noticeJSON := func(id string) string {
		return jsonString(i18n.T(i18n.JA, "service.comment.secret_notice", "id", id, "line", f.Line, "label", f.Label))
	}
	commentNoticeRes := `{"issue":` + item1 + `,"message":"コメント追記: DEMO-0001","secret_notice":` + noticeJSON("DEMO-0001") + `,"seq":2}`
	statusNoticeRes := `{"from":"Todo","issue":` + item("DEMO-0002", "bug", "In Progress", "P0", "保存でエラーになる", nil, `"assignee":"alice"`) +
		`,"messages":["DEMO-0002: Todo → In Progress","コメント追記: DEMO-0002",` + noticeJSON("DEMO-0002") + `],"secret_notice":` + noticeJSON("DEMO-0002") + `,"to":"In Progress"}`
	return []Case{
		{Name: "comment/secret-rejected", Args: []string{"comment", "DEMO-0001", commentSecretText},
			Routes: []Route{r("POST", pIssue1+"/comments", rejected)}},
		{Name: "comment/secret-notice", Args: []string{"comment", "DEMO-0001", commentSecretText},
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentNoticeRes))}},
		{Name: "status/secret-notice", Args: []string{"status", "DEMO-0002", "In Progress", "--comment", commentSecretText},
			Routes: []Route{r("POST", pIssue2+"/status", ok(statusNoticeRes))}},
	}
}
