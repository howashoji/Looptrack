package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	clienv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 記憶の版の本体を消した後は、REST・MCP・CLI・Web のどこからも読めない（対照として、消す前はどの経路でも読める）。消した記録が残る。
// 消す操作は運用者の service の関数（looptrack purge-memory が呼ぶ）で行い、権限の確かめは REST の消去で別に見る。
func TestMemoryPurgeUnreadableOnEveryPath(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("memp")
	u := e.user("memp-ed", "editor-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	ed := e.apiAs(u)
	const word = "消したい合成の語"
	ed.json(http.StatusCreated, "POST", "/projects/memp/memory/decision/leak/append", map[string]any{"content": word + "\n本文"}, nil)
	ed.json(http.StatusOK, "PUT", "/projects/memp/memory/decision/leak", map[string]any{"content": "書き直した"}, nil, "If-Match", `"1"`)

	const rest = "/projects/memp/memory/decision/leak"
	const webPath = "/im/p/memp/memory/project/decision/leak"
	m := e.mcpAs(ed.token, map[string]string{"X-Looptrack-Project": "memp"})
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": "memp", "LOOPTRACK_TOKEN": ed.token, "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"), "CLAUDE_PROJECT_DIR": t.TempDir()}
	runCLI := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := cli.MemoryMain(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
		return code, out.String(), errb.String()
	}
	wc, _, _ := e.boardSession("memp-ed", "editor-password-1", "memp")

	// 対照: 消す前はどの経路でも版 1 が読める
	if code, _, b := ed.do("GET", rest+"?version=1", nil); code != http.StatusOK || !strings.Contains(string(b), word) {
		t.Fatalf("前提が崩れています（REST）: %d %s", code, b)
	}
	if text, _ := m.call("get_memory", map[string]any{"kind": "decision", "name": "leak", "version": 1}, false); !strings.Contains(text, word) {
		t.Fatalf("前提が崩れています（MCP）: %q", text)
	}
	if code, out, errs := runCLI("show", "decision", "leak", "--version", "1"); code != 0 || !strings.Contains(out, word) {
		t.Fatalf("前提が崩れています（CLI）: %d %q %q", code, out, errs)
	}
	if res, page := e.get(wc, webPath+"?version=1"); res.StatusCode != http.StatusOK || !strings.Contains(page, word) {
		t.Fatalf("前提が崩れています（Web）: %d", res.StatusCode)
	}

	if _, err := service.New(e.db, nil).PurgeOperatorMemory(ctx, service.OperatorMemoryTarget{Project: "memp", Kind: "decision", Name: "leak", MemoryPurgeSelect: service.MemoryPurgeSelect{Version: 1}}, "秘密が混ざった"); err != nil {
		t.Fatal(err)
	}

	code, _, b := ed.do("GET", rest+"?version=1", nil)
	if code != http.StatusNotFound || strings.Contains(string(b), word) {
		t.Errorf("REST の版: %d %s", code, b)
	}
	var eb memErrBody
	if err := json.Unmarshal(b, &eb); err != nil || eb.Error.Code != "memory_version_purged" {
		t.Errorf("REST の理由: %s", b)
	}
	code, _, b = ed.do("GET", rest, nil)
	if code != http.StatusOK || strings.Contains(string(b), word) || !strings.Contains(string(b), `"purged":true`) {
		t.Errorf("REST の履歴: %d %s", code, b)
	}
	if text, _ := m.call("get_memory", map[string]any{"kind": "decision", "name": "leak", "version": 1}, true); strings.Contains(text, word) {
		t.Errorf("MCP の版: %q", text)
	}
	if text, _ := m.call("get_memory", map[string]any{"kind": "decision", "name": "leak"}, false); strings.Contains(text, word) {
		t.Errorf("MCP の履歴: %q", text)
	}
	if code, out, errs := runCLI("show", "decision", "leak", "--version", "1"); code == 0 || strings.Contains(out+errs, word) {
		t.Errorf("CLI の版: %d %q %q", code, out, errs)
	}
	if code, out, _ := runCLI("show", "decision", "leak", "--history"); code != 0 || strings.Contains(out, word) || !strings.Contains(out, "本体は消去済み") {
		t.Errorf("CLI の履歴: %d %q", code, out)
	}
	if res, page := e.get(wc, webPath+"?version=1"); res.StatusCode != http.StatusNotFound || strings.Contains(page, word) {
		t.Errorf("Web の版: %d", res.StatusCode)
	}
	if res, page := e.get(wc, webPath); res.StatusCode != http.StatusOK || strings.Contains(page, word) || !strings.Contains(page, "本体は消去済み") {
		t.Errorf("Web の履歴: %d", res.StatusCode)
	}
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM memory_purges WHERE cause = 'operator' AND reason = ?", "秘密が混ざった").Scan(&n); err != nil || n != 1 {
		t.Errorf("消した記録: %d %v", n, err)
	}
}

// REST の消去: viewer・editor は 403 で本体が残り、プロジェクトの admin は消せる（対照）。CLI は --yes が無ければ送らない。
func TestMemoryPurgeREST(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("mempr")
	member := func(login, role string) *apiClient {
		u := e.user(login, login+"-password-1", "member")
		if err := store.SetMember(ctx, e.db, pr.ID, u.ID, role); err != nil {
			t.Fatal(err)
		}
		return e.apiAs(u)
	}
	ed, vi, ad := member("mempr-ed", "editor"), member("mempr-vi", "viewer"), member("mempr-ad", "admin")
	ed.json(http.StatusCreated, "POST", "/projects/mempr/memory/caveat/x/append", map[string]any{"content": "本文"}, nil)
	const path = "/projects/mempr/memory/caveat/x/purge"
	for name, c := range map[string]*apiClient{"viewer": vi, "editor": ed} {
		if code, _, b := c.do("POST", path, map[string]any{"reason": "理由", "all_versions": true}); code != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	if code, _, b := ed.do("GET", "/projects/mempr/memory/caveat/x?version=1", nil); code != http.StatusOK {
		t.Fatalf("拒まれた後に本体が読めない: %d %s", code, b)
	}
	if code, _, b := ad.do("POST", path, map[string]any{"version": 1}); code != http.StatusBadRequest {
		t.Errorf("理由なし: %d %s", code, b)
	}
	for _, body := range []map[string]any{{"reason": "理由"}, {"reason": "理由", "version": 0}, {"reason": "理由", "version": 1, "all_versions": true}} {
		if code, _, b := ad.do("POST", path, body); code != http.StatusBadRequest {
			t.Errorf("版の指定 %v: %d %s", body, code, b)
		}
	}
	if code, _, b := ed.do("GET", "/projects/mempr/memory/caveat/x?version=1", nil); code != http.StatusOK {
		t.Fatalf("拒んだ指定で本体が消えた: %d %s", code, b)
	}
	var res struct {
		Purged struct {
			Bodies  int  `json:"bodies"`
			Current bool `json:"current"`
		} `json:"purged"`
	}
	ad.json(http.StatusOK, "POST", path, map[string]any{"reason": "理由", "version": 1}, &res)
	if res.Purged.Bodies != 1 || !res.Purged.Current {
		t.Errorf("消去の応答: %+v", res)
	}
	if code, _, b := ed.do("GET", "/projects/mempr/memory/caveat/x", nil); code != http.StatusNotFound {
		t.Errorf("今の版を消した後: %d %s", code, b)
	}
	home := t.TempDir()
	vars := map[string]string{"LOOPTRACK_API_URL": e.srv.URL + "/im", "LOOPTRACK_PROJECT": "mempr", "LOOPTRACK_TOKEN": ad.token, "LOOPTRACK_LANG": "ja",
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData"), "CLAUDE_PROJECT_DIR": t.TempDir()}
	ad.json(http.StatusOK, "POST", "/projects/mempr/memory/caveat/x/append", map[string]any{"content": "二度目"}, nil)
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := cli.MemoryMain(args, cli.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, clienv.FromMap(vars))
		return code, out.String() + errb.String()
	}
	if code, out := run("purge", "caveat", "x", "--reason", "理由", "--all-versions"); code == 0 {
		t.Errorf("--yes なしの CLI: %d %q", code, out)
	}
	if code, out := run("purge", "caveat", "x", "--reason", "理由", "--yes", "--version", "0"); code == 0 {
		t.Errorf("--version 0 の CLI: %d %q", code, out)
	}
	if code, out := run("purge", "caveat", "x", "--reason", "理由", "--yes"); code == 0 {
		t.Errorf("版の指定なしの CLI: %d %q", code, out)
	}
	if code, out := run("show", "caveat", "x"); code != 0 || !strings.Contains(out, "二度目") {
		t.Fatalf("前提が崩れています: %d %q", code, out)
	}
	if code, out := run("purge", "caveat", "x", "--reason", "理由", "--yes", "--all-versions"); code != 0 || !strings.Contains(out, "1 件") {
		t.Errorf("CLI の消去: %d %q", code, out)
	}
}

// 定期の処理は、会話記録の置き場が無いサーバでも記憶の保持期間を効かせる。
func TestHousekeepingMemoryRetention(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pr := e.project("memh")
	u := e.user("memh-ed", "editor-password-1", "member")
	if err := store.SetMember(ctx, e.db, pr.ID, u.ID, "editor"); err != nil {
		t.Fatal(err)
	}
	ed := e.apiAs(u)
	ed.json(http.StatusCreated, "POST", "/projects/memh/memory/handoff/main/append", map[string]any{"content": "1"}, nil)
	ed.json(http.StatusOK, "POST", "/projects/memh/memory/handoff/main/append", map[string]any{"content": "2"}, nil)
	if _, _, err := store.SetMemoryRetentionDays(ctx, e.db, 7, store.SettingChange{Via: "command"}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bodies := func() (n int) {
		if err := e.db.QueryRow("SELECT COUNT(*) FROM memory_bodies").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	svc := service.New(e.db, nil)
	housekeepOnce(ctx, e.db, logger, svc)
	if bodies() != 2 {
		t.Fatalf("新しい版を消した: %d", bodies())
	}
	if svc.TranscriptsAvailable() {
		t.Fatal("前提が崩れています: 会話記録の置き場がある")
	}
	svc.Now = func() time.Time { return time.Now().AddDate(0, 0, 8) }
	housekeepOnce(ctx, e.db, logger, svc)
	if bodies() != 1 {
		t.Errorf("保持期間の後の本体: %d", bodies())
	}
}

// 消す前に読んだ版の置き換えと圧縮は通らず、消した本文が戻らない（append → 全部の版の消去 → If-Match: "1"）。
func TestMemoryPurgeStaleWritesAreRefused(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	if code, _, msg := a.handoff("## 節\n消す本文", "append", "--title", "節", "--session", "sess-a"); code != 0 {
		t.Fatalf("追記: exit %d %s", code, msg)
	}
	if _, err := service.New(e.db, nil).PurgeOperatorMemory(context.Background(), service.OperatorMemoryTarget{Project: slug, Kind: "handoff",
		MemoryPurgeSelect: service.MemoryPurgeSelect{AllVersions: true}}, "秘密"); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := a.handoff("## 圧縮\n消す本文", "compact", "--version", "1", "--session", "sess-a"); code == 0 {
		t.Fatalf("消す前の版の compact が通った: %s", msg)
	}
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM memory_bodies WHERE content LIKE '%消す本文%'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("本文が戻った: %d %v", n, err)
	}
	// 対照: 今の版（2）なら作り直せる
	if code, _, msg := a.handoff("## 新しい本文", "compact", "--version", "2", "--session", "sess-a"); code != 0 {
		t.Fatalf("今の版の compact: exit %d %s", code, msg)
	}
}

// 版を指した消去の本文が後の版に残るとき、REST は 409、CLI は終了コード 1 で何も消さない。対照は --all-versions。
func TestMemoryPurgeTextRemainsREST(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	for _, c := range []string{"一つ目の節", "二つ目の節"} {
		if code, _, msg := a.memoryWrite("caveat", "x", c); code != 0 {
			t.Fatalf("追記: exit %d %s", code, msg)
		}
	}
	c := e.apiAs(e.user("memtr-root", "root-password-1", "admin")) // 版を消せる権限
	a.vars["LOOPTRACK_TOKEN"] = c.token
	path := "/projects/" + slug + "/memory/caveat/x/purge"
	code, _, b := c.do("POST", path, map[string]any{"version": 1, "reason": "誤記"})
	if code != http.StatusConflict || !strings.Contains(string(b), "memory_purge_text_remains") || !strings.Contains(string(b), "all_versions") {
		t.Errorf("REST: %d %s", code, b)
	}
	if code, out, msg := a.memory("purge", "caveat", "x", "--version", "1", "--reason", "誤記", "--yes"); code == 0 || !strings.Contains(out+msg, "--all-versions") {
		t.Errorf("CLI: %d %s %s", code, out, msg)
	}
	if _, shown, _ := a.memory("show", "caveat", "x"); !strings.Contains(shown, "一つ目の節") {
		t.Fatalf("前提が崩れています: 拒んだのに本文が変わった: %s", shown)
	}
}

// 資格情報の拒否の行番号は利用者の入力のもの（見出しを足した本文のものではない）。サーバに届かないときも同じ。対照は見出しつきの普通の本文。
func TestHandoffSecretLineIsInputLine(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	body := "一行目\n二行目\n三行目\npassword=Sup3rS3cretValue9\n"
	code, out, msg := a.handoff(body, "append", "--title", "節", "--session", "sess-a")
	if code != 1 || !strings.Contains(msg, "本文の 4 行目") || strings.Contains(out+msg, "Sup3rS3cretValue9") {
		t.Fatalf("追記: exit %d %s %s", code, out, msg)
	}
	code, _, msg = a.handoff("## 圧縮\n二行目\n"+"password=Sup3rS3cretValue9\n", "compact", "--version", "0", "--session", "sess-a")
	if code != 1 || !strings.Contains(msg, "本文の 3 行目") {
		t.Fatalf("圧縮: exit %d %s", code, msg)
	}
	if code, _, msg := a.handoff("普通の本文", "append", "--title", "節", "--session", "sess-a"); code != 0 {
		t.Fatalf("対照: exit %d %s", code, msg)
	}
}

// Stop は完了の印が無くても退避を送る。対照は送る前に退避が残っていること。
func TestStopFlushesSpoolWithoutMark(t *testing.T) {
	e, slug, token := newMemHookEnv(t)
	a := e.memHookBox(slug, token)
	p := newMemFlakyProxy(t, e.srv.URL)
	a.vars["LOOPTRACK_API_URL"] = p.srv.URL + "/im"
	p.down.Store(true)
	if code, out, msg := a.handoff("## 節\n退避する本文", "append", "--title", "節", "--session", "sess-a"); code != 0 || !strings.Contains(out, "退避しました") {
		t.Fatalf("退避: exit %d %s %s", code, out, msg)
	}
	p.down.Store(false)
	if _, list, _ := a.handoff("", "spool"); strings.Contains(list, "ありません") {
		t.Fatalf("前提が崩れています: 退避が無い: %s", list)
	}
	if g := a.hook("stop-handoff-freshness", "Stop", "sess-a", nil); len(g) != 0 {
		t.Fatalf("印の無い Stop が止めた: %v", g)
	}
	if _, list, _ := a.handoff("", "spool"); !strings.Contains(list, "ありません") {
		t.Fatalf("退避が残った: %s", list)
	}
	if _, shown, _ := a.memory("show", "handoff"); !strings.Contains(shown, "退避する本文") {
		t.Fatalf("送られていない: %s", shown)
	}
}
