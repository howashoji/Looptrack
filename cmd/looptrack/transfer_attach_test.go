package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/transfer"
)

// captureCmd はサーバのサブコマンド（os.Stdout・os.Stderr に直接書く）を動かし、終了コードと出力を返す。
func captureCmd(t *testing.T, cmd func([]string) int, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	outF, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	func() {
		defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
		code = cmd(args)
	}()
	outF.Close()
	errF.Close()
	o, _ := os.ReadFile(outF.Name())
	e, _ := os.ReadFile(errF.Name())
	return code, string(o), string(e)
}

// transferAttachEnv は SQLite の DB（置き場は DB の隣の attachments）に添付つきのイシューを 1 件作る。
func transferAttachEnv(t *testing.T) (attachDir string, body *store.Attachment) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOOPTRACK_DSN", "sqlite:"+filepath.Join(dir, "t.db"))
	t.Setenv("LOOPTRACK_ATTACH_DIR", "")
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("LOOPTRACK_LANG", "ja")
	if code, out, errOut := captureCmd(t, migrateCmd); code != 0 {
		t.Fatalf("migrate: %d\n%s%s", code, out, errOut)
	}
	attachDir = service.AttachDirFromEnv(os.Getenv)
	if attachDir != filepath.Join(dir, "attachments") {
		t.Fatalf("前提が崩れています（置き場が DB の隣に決まらない: %q）", attachDir)
	}
	db, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	pid, err := store.CreateProject(ctx, db, store.Project{Slug: "att", Prefix: "ATT", Width: 4, Name: "att"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.ProjectByID(ctx, db, pid)
	uid, err := store.CreateUser(ctx, db, "ed", "ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, db, pid, uid, "editor"); err != nil {
		t.Fatal(err)
	}
	svc := service.New(db, nil)
	svc.AttachDir = attachDir
	a := service.Actor{UserID: uid, Via: "cli", Lang: i18n.JA}
	it, err := svc.Create(ctx, a, p, service.CreateInput{Title: "x"}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	body, err = svc.Attach(ctx, a, service.AttachInput{Issue: it.Item.ID, Filename: "log.txt", Body: strings.NewReader("full output\n")})
	if err != nil {
		t.Fatal(err)
	}
	return attachDir, body
}

// export の終了コードと NG の行、verify-files の終了コード（CLI の層）。本体がそろっていれば 0（対照）。
func TestExportAndVerifyFilesAttachmentsExitCodes(t *testing.T) {
	attachDir, at := transferAttachEnv(t)

	out := t.TempDir()
	code, stdout, stderr := captureCmd(t, exportCmd, "--out", out)
	if code != 0 || !strings.Contains(stdout, "添付: 1 件") || stderr != "" {
		t.Fatalf("本体がそろった export: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = captureCmd(t, verifyFiles, "--root", out)
	if code != 0 || !strings.Contains(stdout, "添付: att 本体 1 / 1") || stderr != "" {
		t.Fatalf("正しい本体の verify-files: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// 書き出した本体を（同じ大きさで）壊すと、verify-files は NG を出して 1 で終わる
	if err := os.WriteFile(filepath.Join(out, "att", "attachments", at.SHA256), []byte("FULL output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = captureCmd(t, verifyFiles, "--root", out)
	if code != 1 || !strings.Contains(stderr, "NG att: ") || !strings.Contains(stderr, "目録の SHA-256 と一致しません") {
		t.Errorf("壊した本体の verify-files: code=%d stderr=%s", code, stderr)
	}

	// 置き場の本体が欠けていれば、export は最後まで書き出して NG を出し、1 で終わる
	if err := os.Remove(service.AttachmentBodyPath(attachDir, at.SHA256)); err != nil {
		t.Fatal(err)
	}
	out2 := t.TempDir()
	code, stdout, stderr = captureCmd(t, exportCmd, "--out", out2)
	if code != 1 || !strings.Contains(stderr, "NG att: 添付 ") || !strings.Contains(stderr, "本体が置き場にありません") {
		t.Errorf("本体の欠けた export: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(out2, "att", transfer.AttachmentManifestName)); err != nil {
		t.Errorf("本体が欠けても目録は書く: %v", err)
	}
}

// import は添付を運ばず、目録があればそう知らせる（日英。言語は LOOPTRACK_LANG で固定）。目録が無ければ出さない（対照）。
func TestImportSaysAttachmentsAreNotImported(t *testing.T) {
	transferAttachEnv(t)
	mk := func(slug string, manifest bool) string {
		root := t.TempDir()
		d := filepath.Join(root, slug)
		if err := os.MkdirAll(filepath.Join(d, "open"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"prefix":"`+strings.ToUpper(slug)+`","width":4,"name":"`+slug+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if manifest {
			if err := os.WriteFile(filepath.Join(d, transfer.AttachmentManifestName), []byte(`{"format":1,"project":"`+slug+`","attachments":[{"id":1},{"id":2}]}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	for _, c := range []struct{ lang, want string }{
		{"ja", "imp: 添付の目録 attachments.json（2 件）は取り込みません"},
		{"en", "imp: not importing the attachment manifest attachments.json (2 attachments)"},
	} {
		t.Setenv("LOOPTRACK_LANG", c.lang)
		code, stdout, stderr := captureCmd(t, importCmd, "--root", mk("imp", true))
		if code != 0 || !strings.Contains(stdout, c.want) {
			t.Errorf("%s: code=%d\nstdout=%s\nstderr=%s", c.lang, code, stdout, stderr)
		}
	}
	t.Setenv("LOOPTRACK_LANG", "ja")
	code, stdout, _ := captureCmd(t, importCmd, "--root", mk("plain", false))
	if code != 0 || !strings.Contains(stdout, "取り込み: plain") || strings.Contains(stdout, "取り込みません") {
		t.Errorf("目録の無い import: code=%d stdout=%s", code, stdout)
	}
}
