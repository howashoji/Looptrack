package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 会話記録の管理者の画面（/admin/transcripts）。上限と保持日数の設定（setting_changes に記録）と、利用者ごとの使用量（数だけ）。

// transcriptAdminPage は利用者でログイン済みのクライアントで /admin/transcripts を開き、状態・本文・CSRF を返す。
func (e *env) transcriptAdminPage(c *http.Client) (int, string, string) {
	e.t.Helper()
	res, page := e.get(c, "/im/admin/transcripts")
	m := adminCSRFRe.FindStringSubmatch(page)
	if m == nil {
		return res.StatusCode, page, ""
	}
	return res.StatusCode, page, m[1]
}

// 画面に出るのは利用者ごとの会話の数とバイト数だけで、会話の中身・ファイル名（file_key）・セッション ID・端末のラベルは出ない。
// 対照: 同じ画面に使用量の行（利用者・会話の数）が実際に出ている（数を出す経路が通った上で、中身が無いことを確かめる）。
func TestAdminTranscriptsShowsOnlyCounts(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	tr := a.mustRegister(a.owner, trSID, http.StatusCreated)
	const marker = "ContentMarkerQ7"
	sub := trSID + "/subagents/agent-zeta.jsonl"
	a.mustAppend(a.owner, tr.ID, `"0"`, trSID+".jsonl", 0, []byte(trLine(marker+" main")))
	a.mustAppend(a.owner, tr.ID, `"1"`, sub, 0, []byte(trLine(marker+" sub")))
	a.mustRegister(a.owner, trSID2, http.StatusCreated)

	ac := a.client()
	a.enroll(ac, "tr-root", "root-password-1")
	code, page, csrf := a.transcriptAdminPage(ac)
	if code != http.StatusOK || csrf == "" {
		t.Fatalf("管理者の画面: %d\n%s", code, page)
	}
	// 対照: 使用量の行（持ち主・会話 2 つ）と上限が出ている
	row := regexp.MustCompile(`(?s)<tr data-usage="tr-owner"[^>]*>.*?</tr>`).FindString(page)
	if row == "" || !strings.Contains(row, "<td>2</td>") || !strings.Contains(page, `<strong data-limit="chunk">32 MiB</strong>`) ||
		!strings.Contains(page, `<strong data-limit="user">2 GiB</strong>`) || !strings.Contains(page, `<strong data-limit="retention">90 日</strong>`) {
		t.Fatalf("前提が崩れています: 使用量の行か上限が出ていない\n%s", page)
	}
	if strings.Contains(page, "data-transcripts-unavailable") {
		t.Errorf("置き場があるのに「使えない」が出た")
	}
	for _, leak := range []string{marker, trSID, trSID2, "agent-zeta", "subagents", ".jsonl", "box-1", trNonce} {
		if strings.Contains(page, leak) {
			t.Errorf("管理者の画面に %q が出た", leak)
		}
	}
	// 利用者メニューから辿れる
	if !strings.Contains(page, `href="/im/admin/transcripts"`) {
		t.Errorf("利用者メニューに会話記録の管理が無い")
	}
}

// 上限と保持日数を変えると setting_changes に変更前後の値と操作した人が残り、次の送信から効く。管理者でない利用者は
// 画面にも POST にも入れず、何も変わらない（service の規則も直接の呼び出しを拒む）。対照: 同じテストの管理者は変えられる。
func TestAdminTranscriptsLimits(t *testing.T) {
	a := newTranscriptAPIEnv(t)
	ctx := context.Background()
	limits := func() store.TranscriptLimits {
		l, err := store.ReadTranscriptLimits(ctx, a.db)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	changes := func() int {
		return a.count("SELECT COUNT(*) FROM setting_changes WHERE name IN (?, ?, ?)",
			store.SettingTranscriptMaxChunk, store.SettingTranscriptMaxUser, store.SettingTranscriptRetentionDays)
	}
	before := limits()
	form := func(csrf, chunk, user, days string) url.Values {
		return url.Values{"csrf": {csrf}, "max_chunk_mib": {chunk}, "max_user_mib": {user}, "retention_days": {days}}
	}

	// 管理者でない利用者（プロジェクトの管理者でも）: 画面も POST も 403 で、何も変わらない
	pc := a.client()
	a.enroll(pc, "tr-padmin", "tr-padmin-password-1")
	_, acct := a.get(pc, "/im/account")
	pcsrf := csrfOf(t, acct)
	if code, page, _ := a.transcriptAdminPage(pc); code != http.StatusForbidden || strings.Contains(page, `data-limit="chunk"`) {
		t.Errorf("管理者でない人の画面: %d, want 403 で中身を出さない", code)
	}
	if res, _ := a.post(pc, "/im/admin/transcripts/limits", form(pcsrf, "1", "2", "3")); res.StatusCode != http.StatusForbidden {
		t.Errorf("管理者でない人の POST: %d, want 403", res.StatusCode)
	}
	if limits() != before || changes() != 0 {
		t.Errorf("管理者でない人の POST で変わった: %+v・記録 %d 件", limits(), changes())
	}
	svc := service.New(a.db, nil)
	var se *service.Error
	if _, err := svc.SetTranscriptLimits(ctx, service.Actor{UserID: a.padminUser.ID, Via: "web"}, store.TranscriptLimits{MaxChunk: 1, MaxUser: 2, RetentionDays: 3}, ""); !errors.As(err, &se) || se.Kind != service.Forbidden {
		t.Errorf("service.SetTranscriptLimits（管理者でない）: %v, want Forbidden", err)
	}
	if _, err := svc.TranscriptOverview(ctx, service.Actor{UserID: a.padminUser.ID, Via: "web"}); !errors.As(err, &se) || se.Kind != service.Forbidden {
		t.Errorf("service.TranscriptOverview（管理者でない）: %v, want Forbidden", err)
	}

	// 管理者（対照）
	ac := a.client()
	a.enroll(ac, "tr-root", "root-password-1")
	_, page, csrf := a.transcriptAdminPage(ac)
	if csrf == "" {
		t.Fatalf("管理者の画面に CSRF が無い\n%s", page)
	}
	// CSRF が違えば 403 で何も変わらない
	if res, _ := a.post(ac, "/im/admin/transcripts/limits", form("wrong", "1", "2", "3")); res.StatusCode != http.StatusForbidden || changes() != 0 {
		t.Errorf("CSRF 違い: %d・記録 %d 件", res.StatusCode, changes())
	}
	// 範囲の外は 400 で変えない
	for _, f := range [][3]string{{"0", "10", "30"}, {"1", "x", "30"}, {"1", "10", "-1"}, {"1", "10", strconv.Itoa(maxRetentionDays + 1)}} {
		res, body := a.post(ac, "/im/admin/transcripts/limits", form(csrf, f[0], f[1], f[2]))
		if res.StatusCode != http.StatusBadRequest || limits() != before || changes() != 0 || !strings.Contains(body, `class="error"`) {
			t.Errorf("範囲の外 %v: %d・上限 %+v・記録 %d 件", f, res.StatusCode, limits(), changes())
		}
	}
	res, body := a.post(ac, "/im/admin/transcripts/limits", form(csrf, "1", "3", "0"))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `class="notice"`) || !strings.Contains(body, `<strong data-limit="retention">無期限</strong>`) {
		t.Fatalf("上限の変更: %d\n%s", res.StatusCode, body)
	}
	if got := limits(); got != (store.TranscriptLimits{MaxChunk: 1 << 20, MaxUser: 3 << 20, RetentionDays: 0}) {
		t.Errorf("変えた後の上限: %+v", got)
	}
	rows, err := a.db.Query(`SELECT name, old_value, new_value, COALESCE(actor_user_id, 0), via FROM setting_changes WHERE name IN (?, ?, ?) ORDER BY id`,
		store.SettingTranscriptMaxChunk, store.SettingTranscriptMaxUser, store.SettingTranscriptRetentionDays)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	rootID := int64(0)
	a.db.QueryRow("SELECT id FROM users WHERE login = 'tr-root'").Scan(&rootID)
	for rows.Next() {
		var name, old, nv, via string
		var actor int64
		rows.Scan(&name, &old, &nv, &actor, &via)
		if actor != rootID || via != "web" {
			t.Errorf("%s の記録: 操作した人 %d（want %d）・経路 %q", name, actor, rootID, via)
		}
		got = append(got, name+":"+old+"->"+nv)
	}
	rows.Close()
	want := []string{store.SettingTranscriptMaxChunk + ":->1048576", store.SettingTranscriptMaxUser + ":->3145728", store.SettingTranscriptRetentionDays + ":->0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("setting_changes = %v, want %v", got, want)
	}
	if strings.Count(body, "data-setting-change") != 3 || !strings.Contains(body, "tr-root") {
		t.Errorf("画面に変更の記録（3 件・操作した人）が出ない:\n%s", body)
	}
	// 同じ値の再送は記録を増やさない
	if res, body := a.post(ac, "/im/admin/transcripts/limits", form(csrf, "1", "3", "0")); res.StatusCode != http.StatusOK || changes() != 3 || !strings.Contains(body, `class="notice"`) {
		t.Errorf("同じ値の再送: %d・記録 %d 件", res.StatusCode, changes())
	}
	// 次の送信から効く: 1 チャンクの上限 1 MiB を超える本文は 413
	tr := a.mustRegister(a.owner, trSID, http.StatusCreated)
	big := []byte(trLine(strings.Repeat("x", 1<<20)))
	if code, _, b := a.appendChunk(a.owner, tr.ID, `"0"`, trSID+".jsonl", 0, big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("新しい上限を超える本文: %d %s, want 413", code, b)
	}
}

// 置き場が無いサーバでも画面は開け、「使えない」を出す（上限は変えられる）。
func TestAdminTranscriptsUnavailable(t *testing.T) {
	e := newEnvWith(t, func(c *Config) { c.AllowNoAdmin = true })
	e.user("tr-root2", "root-password-1", "admin")
	ac := e.client()
	e.enroll(ac, "tr-root2", "root-password-1")
	code, page, _ := e.transcriptAdminPage(ac)
	if code != http.StatusOK || !strings.Contains(page, "data-transcripts-unavailable") || !strings.Contains(page, `data-limit="chunk"`) {
		t.Fatalf("置き場の無いサーバの画面: %d\n%s", code, page)
	}
	if !strings.Contains(page, "会話記録はまだありません") {
		t.Errorf("使用量の空の表示が無い")
	}
}
