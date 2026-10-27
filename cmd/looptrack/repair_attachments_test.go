package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// looptrack repair-attachments: --apply 無しでは何も変えずに本体の欠けと、どこからも指されない本体を報告し、
// --apply ではどこからも指されない本体（書かれて OrphanGrace より古いもの）だけを消す。本番と同じ権限のアプリ用ユーザーで動かす。
func TestRepairAttachmentsCmd(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	pid, err := store.CreateProject(ctx, admin, store.Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "req"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.ProjectByID(ctx, admin, pid)
	uid, err := store.CreateUser(ctx, admin, "ed", "ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, admin, pid, uid, "editor"); err != nil {
		t.Fatal(err)
	}
	svc := service.New(app, nil)
	svc.AttachDir = dir
	a := service.Actor{UserID: uid, Via: "cli", Lang: i18n.JA}
	it, err := svc.Create(ctx, a, p, service.CreateInput{Title: "x"}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	attach := func(name, body string) *store.Attachment {
		t.Helper()
		at, err := svc.Attach(ctx, a, service.AttachInput{Issue: it.Item.ID, Filename: name, Body: strings.NewReader(body)})
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	keep := attach("keep.txt", "keep")
	gone := attach("gone.txt", "gone")
	if err := os.Remove(service.AttachmentBodyPath(dir, gone.SHA256)); err != nil {
		t.Fatal(err)
	}
	orphan := service.AttachmentBodyPath(dir, strings.Repeat("0f", 32))
	if err := os.MkdirAll(filepath.Dir(orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * service.OrphanGrace)
	for _, f := range []string{orphan, service.AttachmentBodyPath(dir, keep.SHA256)} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := repairAttachments(ctx, app, dir, i18n.JA, &out, false); err != nil {
		t.Fatal(err)
	}
	want := "本体の欠け: 添付 " + strconv.FormatInt(gone.ID, 10) + "（req " + it.Item.ID + "・gone.txt・sha256 " + gone.SHA256 + "）\n" +
		"どこからも指されない本体: " + orphan + "\n" +
		"本体の欠け 1 件・どこからも指されない本体 1 件（dry-run。どこからも指されない本体を消すには --apply を付けて再実行）\n"
	if out.String() != want {
		t.Errorf("dry-run:\n%s\nwant\n%s", out.String(), want)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("dry-run が本体を消した: %v", err)
	}

	out.Reset()
	if err := repairAttachments(ctx, app, dir, i18n.JA, &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "消した: "+orphan+"\n本体の欠け 1 件・どこからも指されない本体 1 件のうち 1 件を消しました（本体の欠けは直せないので報告だけ）\n") {
		t.Errorf("apply:\n%s", out.String())
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("どこからも指されない本体が残った: %v", err)
	}
	if _, err := os.Stat(service.AttachmentBodyPath(dir, keep.SHA256)); err != nil {
		t.Errorf("指されている本体を消した: %v", err)
	}
	var n int
	admin.QueryRow("SELECT COUNT(*) FROM attachments").Scan(&n)
	if n != 2 {
		t.Errorf("メタデータが %d 行（2 のはず。整合の検査は DB を変えない）", n)
	}
}

// 受け入れ条件 4（serve の設定）: MySQL で LOOPTRACK_ATTACH_DIR と STATE_DIRECTORY が無くても serve の設定は通り（起動する）、
// 添付の置き場だけが空になる。対照: LOOPTRACK_ATTACH_DIR があればそれを使う。
func TestServeConfigWithoutAttachDir(t *testing.T) {
	key, _ := auth.NewSecretKey()
	t.Setenv("LOOPTRACK_SECRET_KEY", key)
	t.Setenv("LOOPTRACK_DSN", "u:p@tcp(127.0.0.1:1)/im")
	t.Setenv("LOOPTRACK_ATTACH_DIR", "")
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("LOOPTRACK_LOCAL_MODE", "")
	t.Setenv("LOOPTRACK_LISTEN", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, _, err := serveConfig(logger)
	if err != nil || cfg.AttachDir != "" {
		t.Errorf("置き場なし: AttachDir=%q err=%v", cfg.AttachDir, err)
	}
	t.Setenv("LOOPTRACK_ATTACH_DIR", "/srv/attachments")
	if cfg, _, err := serveConfig(logger); err != nil || cfg.AttachDir != "/srv/attachments" {
		t.Errorf("LOOPTRACK_ATTACH_DIR: AttachDir=%q err=%v", cfg.AttachDir, err)
	}
}

// looptrack repair-attachments --apply は、途中で止まった削除が置き場の .tmp に外したまま残した本体を拾う:
// 添付が指す本体は本体のパスへ戻り（本体の欠けが消える）、どこからも指されない本体は戻した後にふつうの規則で消える。
// 古い印は消す。dry-run は数えるだけで何も変えない。
func TestRepairAttachmentsRecoversBodyLeftAside(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	pid, err := store.CreateProject(ctx, admin, store.Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "req"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.ProjectByID(ctx, admin, pid)
	uid, err := store.CreateUser(ctx, admin, "ed", "ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, admin, pid, uid, "editor"); err != nil {
		t.Fatal(err)
	}
	svc := service.New(app, nil)
	svc.AttachDir = dir
	a := service.Actor{UserID: uid, Via: "cli", Lang: i18n.JA}
	it, err := svc.Create(ctx, a, p, service.CreateInput{Title: "x"}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	at, err := svc.Attach(ctx, a, service.AttachInput{Issue: it.Item.ID, Filename: "live.txt", Body: strings.NewReader("live")})
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(dir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * service.OrphanGrace)
	// aside は本体を、止まった削除と同じ名前で .tmp へ外す（本体のパスは空く。名前の時刻は OrphanGrace より前）
	aside := func(sum, content string) (body, moved string) {
		t.Helper()
		body = service.AttachmentBodyPath(dir, sum)
		if err := os.MkdirAll(filepath.Dir(body), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(body, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(body, old, old); err != nil {
			t.Fatal(err)
		}
		moved = filepath.Join(tmpDir, "remove-"+sum+"-"+strconv.FormatInt(old.UnixNano(), 10)+"-1")
		if err := os.Rename(body, moved); err != nil {
			t.Fatal(err)
		}
		return body, moved
	}
	liveBody, liveAside := aside(at.SHA256, "live")
	h := sha256.Sum256([]byte("orphan"))
	orphanBody, orphanAside := aside(hex.EncodeToString(h[:]), "orphan")
	staleMark := filepath.Join(tmpDir, "inflight-"+strings.Repeat("ab", 32)+"-1")
	if err := os.WriteFile(staleMark, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleMark, old, old); err != nil {
		t.Fatal(err)
	}
	present := func(p string) bool { _, err := os.Stat(p); return err == nil }

	var out bytes.Buffer
	if err := repairAttachments(ctx, app, dir, i18n.JA, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "途中で止まった削除が外したまま残した本体 2 件・落ちた添付が残した古い印 1 件（dry-run。") ||
		!strings.Contains(out.String(), "本体の欠け: 添付 "+strconv.FormatInt(at.ID, 10)) {
		t.Errorf("dry-run:\n%s（外した本体 2・古い印 1・外した本体を指す添付は欠けとして出る）", out.String())
	}
	if present(liveBody) || present(orphanBody) || !present(liveAside) || !present(orphanAside) || !present(staleMark) {
		t.Fatal("dry-run が置き場を変えた")
	}

	out.Reset()
	if err := repairAttachments(ctx, app, dir, i18n.JA, &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "途中で止まった削除が外したまま残した本体 2 件のうち 2 件を本体のパスへ戻し、0 件を捨てました。落ちた添付が残した古い印 1 件を消しました\n") ||
		!strings.Contains(out.String(), "消した: "+orphanBody+"\n") || strings.Contains(out.String(), "本体の欠け: ") {
		t.Errorf("apply:\n%s（2 つを戻し、指されないほうはふつうの規則で消す。欠けは残らない）", out.String())
	}
	if !present(liveBody) || present(orphanBody) || present(liveAside) || present(orphanAside) || present(staleMark) {
		t.Errorf("apply の後の置き場: 添付の本体=%v 指されない本体=%v .tmp の残り=%v %v %v",
			present(liveBody), present(orphanBody), present(liveAside), present(orphanAside), present(staleMark))
	}
}
