package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

const attachVerifyBody = "説明\n\n## 検証コマンド\n\n```bash\ngo test ./...\n```\n"

// msgIDs は問題の文面の ID の並び（文面は言語で変わるので ID で比べる）。
func msgIDs(ms []i18n.Msg) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func readManifest(t *testing.T, path string) AttachmentManifest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m AttachmentManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// 書き出しは添付の本体を <slug>/attachments/<sha256> に、目録を <slug>/attachments.json に書き、
// verify-files（VerifyAttachmentFiles）は本体のバイトが目録の SHA-256 と一致するかを確かめる。
// 置き場が無い・本体が欠けている・本体が SHA-256 と合わないときは、目録に missing で載せて問題として返す。
// 本番と同じ権限のアプリ用ユーザーで書き出す（export は LOOPTRACK_DSN の利用者で動く）。
func TestExportAttachments(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	svc := service.New(app, nil)
	svc.AttachDir = dir

	mkProject := func(slug, prefix string) store.Project {
		id, err := store.CreateProject(ctx, admin, store.Project{Slug: slug, Prefix: prefix, Width: 4, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		p, err := store.ProjectByID(ctx, admin, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p, plain := mkProject("att", "ATT"), mkProject("plain", "PLN")
	user := func(login, role string) service.Actor {
		id, err := store.CreateUser(ctx, admin, login, login, "x", role)
		if err != nil {
			t.Fatal(err)
		}
		return service.Actor{UserID: id, Via: "cli", Lang: i18n.JA}
	}
	ed, root := user("ed", "member"), user("root", "admin")
	for _, pr := range []store.Project{p, plain} {
		if err := store.SetMember(ctx, admin, pr.ID, ed.UserID, "editor"); err != nil {
			t.Fatal(err)
		}
	}
	it, err := svc.Create(ctx, ed, p, service.CreateInput{Title: "添付あり", Body: attachVerifyBody}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, ed, plain, service.CreateInput{Title: "添付なし"}, i18n.JA); err != nil {
		t.Fatal(err)
	}
	attach := func(name, body string) *store.Attachment {
		t.Helper()
		at, err := svc.Attach(ctx, ed, service.AttachInput{Issue: it.Item.ID, Filename: name, Body: strings.NewReader(body)})
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	shot := attach("shot.png", "png-bytes")
	same := attach("same.txt", "png-bytes") // 同じ本体を指す 2 件目（本体は 1 つだけ書く）
	logf := attach("log.txt", "full output\n")
	secret := attach("secret.txt", "token=abc")
	gone := attach("gone.txt", "gone")
	if _, err := svc.PurgeAttachment(ctx, root, secret.ID, "誤って添付"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CommentAttach(ctx, ed, p, it.Row.ID, "画面を付けた", []int64{shot.ID}); err != nil {
		t.Fatal(err)
	}
	cur, err := svc.Detail(ctx, p, it.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordVerify(ctx, ed, p, it.Row.ID, service.VerifyInput{BodySHA256: domain.BodySHA256(cur.Row.Doc.BodyMain),
		Results: []store.VerifyResult{{Command: "go test ./...", Status: "ok", ExitCode: new(int)}}, Attachments: []int64{logf.ID}}, i18n.JA); err != nil {
		t.Fatal(err)
	}

	// 1. 置き場がそろっていれば、問題なしで書き出し、verify-files も通る（下の壊した場合の対照）
	out := t.TempDir()
	res, err := Export(ctx, app, out, nil, false, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attachments != 5 || res.Bodies != 3 || len(res.Problems) != 0 {
		t.Fatalf("書き出しの結果: %+v（want 目録 5 件・本体 3 ファイル（同じ本体は 1 つ・消去済みは書かない）・問題 0）", res)
	}
	if _, err := os.Stat(filepath.Join(out, "plain", AttachmentManifestName)); !os.IsNotExist(err) {
		t.Errorf("添付の無いプロジェクトに目録を書いた（stat: %v）", err)
	}
	if _, err := os.Stat(filepath.Join(out, "plain", "counter")); err != nil {
		t.Fatalf("前提が崩れています（添付の無いプロジェクトを書き出していない: %v）", err)
	}
	m := readManifest(t, filepath.Join(out, "att", AttachmentManifestName))
	if m.Format != 1 || m.Project != "att" || len(m.Attachments) != 5 {
		t.Fatalf("目録: %+v", m)
	}
	byID := map[int64]ManifestAttachment{}
	for _, a := range m.Attachments {
		byID[a.ID] = a
	}
	s := byID[shot.ID]
	if s.Issue != it.Item.ID || s.Filename != "shot.png" || s.Size != int64(len("png-bytes")) || s.SHA256 != shot.SHA256 ||
		s.Author != "ed" || s.Via != "cli" || s.CreatedAt == "" || s.File != "attachments/"+shot.SHA256 || s.Purged || s.Missing {
		t.Errorf("shot.png の目録: %+v", s)
	}
	if len(s.Refs) != 1 || s.Refs[0].Kind != "comment" || s.Refs[0].Comment != 1 {
		t.Errorf("shot.png を付けたコメント: %+v（want 1 番目のコメント）", s.Refs)
	}
	if r := byID[logf.ID].Refs; len(r) != 1 || r[0].Kind != "verify" || r[0].Comment != 2 {
		t.Errorf("log.txt を付けた verify の記録: %+v（want 2 番目のコメント）", r)
	}
	if a := byID[same.ID]; a.File != s.File || len(a.Refs) != 0 {
		t.Errorf("同じ本体の 2 件目: %+v", a)
	}
	if a := byID[secret.ID]; !a.Purged || a.File != "" || a.Missing {
		t.Errorf("消去済みの添付: %+v（本体は書き出さない）", a)
	}
	if got, err := os.ReadFile(filepath.Join(out, "att", "attachments", shot.SHA256)); err != nil || string(got) != "png-bytes" {
		t.Errorf("書き出した本体: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(out, "att", "attachments", secret.SHA256)); !os.IsNotExist(err) {
		t.Errorf("消去した本体を書き出した（stat: %v）", err)
	}
	reports, err := VerifyAttachmentFiles(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Slug != "att" || reports[0].Bodies != 4 || reports[0].OK != 4 || len(reports[0].Problems) != 0 {
		t.Fatalf("正しい本体の verify-files: %+v（want att の本体 4 件がすべて一致）", reports)
	}

	// 2. 書き出した本体を壊す・消すと、verify-files が落ちる
	body := filepath.Join(out, "att", "attachments", logf.SHA256)
	if err := os.WriteFile(body, []byte("FULL output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reports, _ = VerifyAttachmentFiles(out)
	if got := msgIDs(reports[0].Problems); !slices.Equal(got, []string{"transfer.verify.attach_differs"}) || reports[0].OK != 3 {
		t.Errorf("壊した本体: %v OK=%d", got, reports[0].OK)
	}
	if err := os.Remove(body); err != nil {
		t.Fatal(err)
	}
	reports, _ = VerifyAttachmentFiles(out)
	if got := msgIDs(reports[0].Problems); !slices.Equal(got, []string{"transfer.verify.attach_no_body"}) {
		t.Errorf("消した本体: %v", got)
	}
	// 目録が attachments/<sha256> の外を指していれば読まない
	m.Attachments[0].File = "../plain/counter"
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(out, "att", AttachmentManifestName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	reports, _ = VerifyAttachmentFiles(out)
	if got := msgIDs(reports[0].Problems); !slices.Contains(got, "transfer.verify.attach_bad_path") {
		t.Errorf("目録の外のパス: %v", got)
	}

	// 3. 置き場の本体が欠けていれば、その添付を missing で載せ、問題として返す（書き出しは最後まで続ける）
	if err := os.Remove(service.AttachmentBodyPath(dir, gone.SHA256)); err != nil {
		t.Fatal(err)
	}
	out2 := t.TempDir()
	res, err = Export(ctx, app, out2, nil, false, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := msgIDs(res.Problems); !slices.Equal(got, []string{"transfer.export.attach_missing"}) || res.Files != 2 {
		t.Errorf("本体の欠け: problems=%v files=%d", got, res.Files)
	}
	m = readManifest(t, filepath.Join(out2, "att", AttachmentManifestName))
	for _, a := range m.Attachments {
		if (a.ID == gone.ID) != a.Missing {
			t.Errorf("missing の印: %+v", a)
		}
	}
	reports, _ = VerifyAttachmentFiles(out2)
	if got := msgIDs(reports[0].Problems); !slices.Equal(got, []string{"transfer.verify.attach_exported_missing"}) {
		t.Errorf("欠けたまま書き出した目録の verify-files: %v", got)
	}

	// 4. 置き場の本体が SHA-256 と合わなければ、書き出さずに missing で載せる
	if err := os.WriteFile(service.AttachmentBodyPath(dir, logf.SHA256), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	out3 := t.TempDir()
	res, _ = Export(ctx, app, out3, nil, false, dir)
	if got := msgIDs(res.Problems); !slices.Equal(got, []string{"transfer.export.attach_corrupt", "transfer.export.attach_missing"}) {
		t.Errorf("置き場の本体が壊れている: %v", got)
	}
	if _, err := os.Stat(filepath.Join(out3, "att", "attachments", logf.SHA256)); !os.IsNotExist(err) {
		t.Errorf("SHA-256 と合わない本体を書き出した（stat: %v）", err)
	}

	// 5. 置き場が決まっていなければ、消去していない添付をすべて missing で載せ、問題を 1 件だけ返す
	out4 := t.TempDir()
	res, err = Export(ctx, app, out4, nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := msgIDs(res.Problems); !slices.Equal(got, []string{"transfer.export.attach_no_dir"}) || res.Bodies != 0 || res.Files != 2 {
		t.Errorf("置き場なし: problems=%v %+v", got, res)
	}
	missing := 0
	for _, a := range readManifest(t, filepath.Join(out4, "att", AttachmentManifestName)).Attachments {
		if a.Missing {
			missing++
		}
	}
	if missing != 4 {
		t.Errorf("置き場なしで missing の添付 = %d（want 消去済みを除く 4）", missing)
	}
}

// import は添付を運ばない。目録があれば ReadSource がそれを返し、呼び出し側が「運ばない」と知らせる。
func TestReadSourceReportsAttachmentManifest(t *testing.T) {
	root := t.TempDir()
	mk := func(slug, manifest string) {
		d := filepath.Join(root, slug)
		if err := os.MkdirAll(filepath.Join(d, "open"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"prefix":"`+strings.ToUpper(slug)+`","width":4}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if manifest != "" {
			if err := os.WriteFile(filepath.Join(d, AttachmentManifestName), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("with", `{"format":1,"project":"with","attachments":[{"id":1},{"id":2}]}`)
	mk("broken", `{`)
	mk("without", "") // 対照: 目録の無いプロジェクト
	src, err := ReadSource(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int{}
	for _, sp := range src {
		has := 0
		if sp.AttachmentManifest {
			has = 1
		}
		got[sp.Project.Slug] = [2]int{has, sp.AttachmentCount}
	}
	want := map[string][2]int{"with": {1, 2}, "broken": {1, -1}, "without": {0, 0}}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: (目録あり, 件数) = %v, want %v", k, got[k], v)
		}
	}
}

// verify-files は通常のファイルだけを、目録の size まで読む。シンボリックリンク（書き出しの外のファイルや、終わりの無いもの）・
// ディレクトリ・大きさの違う本体は読まずに NG にする。対照として、同じ組み立ての正しい本体は通る。
func TestVerifyAttachmentFilesRefusesLinksAndSize(t *testing.T) {
	body := []byte("hello")
	sum := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // sha256("hello")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, body, 0o644); err != nil { // 中身は一致する、書き出しの外のファイル
		t.Fatal(err)
	}
	big := filepath.Join(t.TempDir(), "big") // /dev/zero の代わり（目録の size より大きい、外のファイル）
	if err := os.WriteFile(big, bytes.Repeat([]byte{0}, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(t *testing.T, place func(bodyPath string)) []string {
		t.Helper()
		root := t.TempDir()
		d := filepath.Join(root, "x", "attachments")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		m := AttachmentManifest{Format: 1, Project: "x", Attachments: []ManifestAttachment{{ID: 1, Filename: "h.txt", Size: int64(len(body)), SHA256: sum, File: "attachments/" + sum}}}
		b, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(root, "x", AttachmentManifestName), b, 0o644); err != nil {
			t.Fatal(err)
		}
		place(filepath.Join(d, sum))
		reports, err := VerifyAttachmentFiles(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(reports) != 1 {
			t.Fatalf("前提が崩れています（目録を 1 つ読むはず）: %+v", reports)
		}
		if len(reports[0].Problems) == 0 && reports[0].OK != 1 {
			t.Fatalf("前提が崩れています（問題なしなのに一致 0 件）: %+v", reports[0])
		}
		return msgIDs(reports[0].Problems)
	}
	symlink := func(t *testing.T, target, link string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("シンボリックリンクを作れない環境: %v", err)
		}
	}
	write := func(data []byte) func(string) {
		return func(p string) {
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	if got := mk(t, write(body)); len(got) != 0 { // 対照
		t.Fatalf("正しい本体で NG: %v", got)
	}
	cases := []struct {
		name  string
		place func(t *testing.T) func(string)
		want  string
	}{
		{"外の同じ中身のファイルへのリンク", func(t *testing.T) func(string) { return func(p string) { symlink(t, outside, p) } }, "transfer.verify.attach_not_regular"},
		{"終わりの無いものの代わりの大きなファイルへのリンク", func(t *testing.T) func(string) { return func(p string) { symlink(t, big, p) } }, "transfer.verify.attach_not_regular"},
		{"ディレクトリ", func(t *testing.T) func(string) {
			return func(p string) {
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}, "transfer.verify.attach_not_regular"},
		{"大きさが目録と違う", func(*testing.T) func(string) { return write([]byte("hello, world")) }, "transfer.verify.attach_size_differs"},
		{"大きさは同じで中身が違う", func(*testing.T) func(string) { return write([]byte("HELLO")) }, "transfer.verify.attach_differs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mk(t, c.place(t)); !slices.Equal(got, []string{c.want}) {
				t.Errorf("got %v, want [%s]", got, c.want)
			}
		})
	}

	// 本体のディレクトリ・目録がリンクなら、その先を読まない
	t.Run("本体のディレクトリがリンク", func(t *testing.T) {
		got := mk(t, func(p string) {
			real := filepath.Join(t.TempDir(), "real")
			if err := os.MkdirAll(real, 0o755); err != nil {
				t.Fatal(err)
			}
			write(body)(filepath.Join(real, sum))
			d := filepath.Dir(p)
			if err := os.Remove(d); err != nil {
				t.Fatal(err)
			}
			symlink(t, real, d)
		})
		if !slices.Equal(got, []string{"transfer.verify.attach_dir_not_dir"}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("目録がリンク", func(t *testing.T) {
		got := mk(t, func(p string) {
			write(body)(p)
			mf := filepath.Join(filepath.Dir(filepath.Dir(p)), AttachmentManifestName)
			moved := filepath.Join(t.TempDir(), "m.json")
			if err := os.Rename(mf, moved); err != nil {
				t.Fatal(err)
			}
			symlink(t, moved, mf)
		})
		if !slices.Equal(got, []string{"transfer.verify.attach_not_regular_manifest"}) {
			t.Errorf("got %v", got)
		}
	})
}
