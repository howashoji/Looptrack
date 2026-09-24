package server

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/howashoji/looptrack/internal/store"
)

// hasJapanese は、かな・漢字が混ざっているか（英語の側に訳し漏れが残っていないかの検査）。
func hasJapanese(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			return true
		}
	}
	return false
}

// TestServiceErrorLanguageViaCLI は、internal/service が返すエラーが **利用者の言語で** CLI に届くことを、
// 実際に looptrack を子プロセスで起動して確かめる（サーバのエラーが利用者の言語で CLI に届くことの受け入れ条件）。
//
// 経路: service の errm（i18n の ID を持つ *service.Error）→ serviceError（issues_api.go）が
// i18n.Text(reqLang(r), se) で要求ごとの言語の文面にする → CLI が error.message をそのまま出す。
// CLI は LOOPTRACK_LANG を Accept-Language として送る（internal/client/api）。
//
// **日本語と英語の両方**を見る。片方だけだと「サーバがまだ日本語一択」の状態と区別できないため。
func TestServiceErrorLanguageViaCLI(t *testing.T) {
	e := newEnv(t)
	pr := e.project("lang")
	u := e.user("lang-ed", "editor-password-1", "member")
	if err := store.SetMember(context.Background(), e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	a := e.apiAs(u)
	a.json(201, "POST", "/projects/lang/issues", map[string]any{"title": "一つ目"}, nil)
	a.json(201, "POST", "/projects/lang/issues", map[string]any{"title": "二つ目"}, nil)
	a.json(200, "POST", "/issues/LANG-0002/status", map[string]any{"status": "Done"}, nil)

	// LOOPTRACK_LANG は cliAPIEnv（cliHomeEnv）の既定を上書きするため最後に足す
	// （os/exec は同じ名前が重なったら後のほうを採る）。
	run := func(lang string, args ...string) cliResult {
		t.Helper()
		dir, home := t.TempDir(), t.TempDir()
		env := append(cliAPIEnv(e.srv.URL+"/im", "lang", a.token, home), "CLAUDE_PROJECT_DIR="+dir, "LOOPTRACK_LANG="+lang)
		return runCLI(t, dir, env, "", append([]string{"issue"}, args...)...)
	}
	for _, c := range []struct {
		name string
		args []string
		ja   string // LOOPTRACK_LANG=ja のときの標準エラー出力（全文）
		en   string // LOOPTRACK_LANG=en のときの標準エラー出力（全文）
	}{
		{
			// internal/service/service.go の Locate → errm(NotFound, service.err.not_found.issue_id)
			name: "not_found",
			args: []string{"show", "LANG-9999"},
			ja:   "エラー: イシューが見つかりません: LANG-9999\n",
			en:   "Error: Issue not found: LANG-9999\n",
		},
		{
			// internal/service/assignee.go の checkClosed → errm(Rejected, service.err.closed.assignee)
			name: "closed",
			args: []string{"assign", "LANG-0002", "lang-ed"},
			ja:   "エラー: LANG-0002 はクローズ済み（Done）のため担当を変えられません。蒸し返すなら新規起票して参照してください\n",
			en:   "Error: LANG-0002 is closed (Done), so its assignee can no longer be changed. To reopen the subject, file a new issue that refers to it\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			ja := run("ja", c.args...)
			if ja.code == 0 || ja.stderr != c.ja {
				t.Errorf("LOOPTRACK_LANG=ja: exit %d stderr %q, want exit != 0 と %q", ja.code, ja.stderr, c.ja)
			}
			en := run("en", c.args...)
			if en.code == 0 || en.stderr != c.en {
				t.Errorf("LOOPTRACK_LANG=en: exit %d stderr %q, want exit != 0 と %q", en.code, en.stderr, c.en)
			}
			// 実出力を残す（go test -v で、どの文面が返ったかがそのまま読める）。
			t.Logf("LOOPTRACK_LANG=ja -> %s", strings.TrimRight(ja.stderr, "\n"))
			t.Logf("LOOPTRACK_LANG=en -> %s", strings.TrimRight(en.stderr, "\n"))
			// 出し分けていること自体を明示で見る（両方が同じなら訳が効いていない）。
			if ja.stderr == en.stderr {
				t.Errorf("ja と en が同じ文面です: %q", ja.stderr)
			}
			if hasJapanese(en.stderr) {
				t.Errorf("英語の側に日本語が混ざっています: %q", en.stderr)
			}
		})
	}
}
