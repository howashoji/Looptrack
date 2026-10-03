package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// attachEnv は添付のテストの土台。サービスは本番と同じ権限のアプリ用ユーザー（testutil.AppDB）で動かし、
// 追記専用の表への書き込みが権限（MySQL）・トリガ（SQLite）で止まらないことも同時に確かめる。
type attachEnv struct {
	t                             *testing.T
	ctx                           context.Context
	admin, app                    *sql.DB
	s                             *Service
	demo, other                   store.Project
	editor, viewer, root, outside Actor
	issue, issue2, otherIssue     *Issue
}

func newAttachEnv(t *testing.T) *attachEnv {
	t.Helper()
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	e := &attachEnv{t: t, ctx: ctx, admin: admin, app: app, s: New(app, nil)}
	e.s.AttachDir = t.TempDir()
	mk := func(slug, prefix string) store.Project {
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
	e.demo, e.other = mk("demo", "DEMO"), mk("other", "OTHER")
	user := func(login, role string) Actor {
		id, err := store.CreateUser(ctx, admin, login, login, "x", role)
		if err != nil {
			t.Fatal(err)
		}
		return Actor{UserID: id, Via: "cli", Lang: i18n.JA}
	}
	e.editor, e.viewer, e.root, e.outside = user("ed", "member"), user("vi", "member"), user("root", "admin"), user("out", "member")
	for _, m := range []struct {
		p    store.Project
		a    Actor
		role string
	}{{e.demo, e.editor, "editor"}, {e.other, e.editor, "editor"}, {e.demo, e.viewer, "viewer"}, {e.other, e.outside, "editor"}} {
		if err := store.SetMember(ctx, admin, m.p.ID, m.a.UserID, m.role); err != nil {
			t.Fatal(err)
		}
	}
	create := func(p store.Project, title string) *Issue {
		it, err := e.s.Create(ctx, e.editor, p, CreateInput{Title: title}, i18n.JA)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	e.issue, e.issue2, e.otherIssue = create(e.demo, "a"), create(e.demo, "b"), create(e.other, "c")
	return e
}

func (e *attachEnv) attach(a Actor, it *Issue, name, body string) (*store.Attachment, error) {
	return e.s.Attach(e.ctx, a, AttachInput{Issue: it.Item.ID, Filename: name, MediaType: "text/plain", Body: strings.NewReader(body)})
}

func (e *attachEnv) mustAttach(a Actor, it *Issue, name, body string) *store.Attachment {
	e.t.Helper()
	at, err := e.attach(a, it, name, body)
	if err != nil {
		e.t.Fatalf("添付 %s: %v", name, err)
	}
	return at
}

func (e *attachEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.admin.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// files は置き場の通常のファイルを、本体と一時ファイル（.tmp の下）に分けて数える。
func (e *attachEnv) files() (bodies, temps []string) {
	e.t.Helper()
	err := filepath.WalkDir(e.s.AttachDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(e.s.AttachDir, p)
		if strings.HasPrefix(filepath.ToSlash(rel), attachTmpDir+"/") {
			temps = append(temps, rel)
		} else {
			bodies = append(bodies, rel)
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return bodies, temps
}

func errCode(err error) string {
	var se *Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func errKind(err error) Kind {
	var se *Error
	if errors.As(err, &se) {
		return se.Kind
	}
	return 0
}

// 受け入れ条件 1: 新しい 2 つの表は、アプリ用ユーザーでは UPDATE も DELETE もできない（MySQL は deploy/grants.sql の権限、SQLite はトリガ）。
// 対照: 同じ接続で、添付（INSERT）は通り、追記専用でない表（issues）の UPDATE も通る（接続そのものが書けないのではない）。
func TestAttachmentTablesAreAppendOnly(t *testing.T) {
	e := newAttachEnv(t)
	at := e.mustAttach(e.editor, e.issue, "log.txt", "secret")
	if _, err := e.s.PurgeAttachment(e.ctx, e.root, at.ID, "誤って添付した"); err != nil {
		t.Fatal(err)
	}
	// 消去の記録から指されない行を狙う（外部キーで止まったのを追記専用で止まったと読み違えないため）
	free := e.mustAttach(e.editor, e.issue, "free.txt", "free")
	if n := e.count("SELECT COUNT(*) FROM attachments"); n != 2 {
		t.Fatalf("前提が崩れています: attachments が %d 行", n)
	}
	if n := e.count("SELECT COUNT(*) FROM attachment_purges"); n != 1 {
		t.Fatalf("前提が崩れています: attachment_purges が %d 行", n)
	}
	// 止めた理由が追記専用か（SQLite はトリガの文面、MySQL は権限の拒否）
	want := "command denied"
	if testutil.SQLite() {
		want = "append-only"
	}
	for _, stmt := range []string{
		"UPDATE attachments SET filename = 'x' WHERE id = ?", "DELETE FROM attachments WHERE id = ?",
		"UPDATE attachment_purges SET reason = 'x' WHERE id > ?", "DELETE FROM attachment_purges WHERE id > ?",
	} {
		arg := free.ID
		if strings.Contains(stmt, "attachment_purges") {
			arg = 0
		}
		if _, err := e.app.ExecContext(e.ctx, stmt, arg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q で拒否", stmt, err, want)
		}
	}
	if e.count("SELECT COUNT(*) FROM attachments WHERE filename IN ('log.txt', 'free.txt')") != 2 || e.count("SELECT COUNT(*) FROM attachment_purges WHERE reason <> 'x'") != 1 {
		t.Error("行が書き換わった・消えた")
	}
	// 対照
	if _, err := e.app.ExecContext(e.ctx, "UPDATE issues SET title = title WHERE id = ?", e.issue.Row.ID); err != nil {
		t.Errorf("前提が崩れています: アプリ用ユーザーで issues を UPDATE できない: %v", err)
	}
	if _, err := e.attach(e.editor, e.issue, "again.txt", "other"); err != nil {
		t.Errorf("前提が崩れています: アプリ用ユーザーで添付（INSERT）できない: %v", err)
	}
}

// 受け入れ条件 2: 同じ内容を 2 回添付すると、本体は 1 つ、行は 2 つ。対照: 違う内容なら本体が増える。
func TestAttachSameContentSharesBody(t *testing.T) {
	e := newAttachEnv(t)
	a1 := e.mustAttach(e.editor, e.issue, "run1.log", "PASS\n")
	a2 := e.mustAttach(e.editor, e.issue2, "run2.log", "PASS\n")
	if a1.SHA256 != a2.SHA256 || a1.ID == a2.ID || a1.Size != 5 {
		t.Fatalf("2 回の添付: %+v / %+v", a1, a2)
	}
	if n := e.count("SELECT COUNT(*) FROM attachments WHERE sha256 = ?", a1.SHA256); n != 2 {
		t.Errorf("行の数 = %d, want 2", n)
	}
	bodies, temps := e.files()
	if len(bodies) != 1 || len(temps) != 0 {
		t.Errorf("本体 %v・一時ファイル %v, want 本体 1 つ・一時ファイルなし", bodies, temps)
	}
	if want := filepath.Join(a1.SHA256[:2], a1.SHA256[2:4], a1.SHA256); len(bodies) == 1 && bodies[0] != want {
		t.Errorf("本体の置き場 = %s, want %s", bodies[0], want)
	}
	if n := e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'attach'"); n != 2 {
		t.Errorf("attach のイベント = %d, want 2", n)
	}
	// 一覧は添付ごと（イシュー単位）に返る
	if l, err := e.s.Attachments(e.ctx, e.viewer, e.issue.Item.ID, ""); err != nil || len(l) != 1 || l[0].ID != a1.ID || l[0].Filename != "run1.log" || l[0].Purged {
		t.Errorf("一覧: %+v %v", l, err)
	}
	// 対照
	e.mustAttach(e.editor, e.issue, "run3.log", "FAIL\n")
	if bodies, _ := e.files(); len(bodies) != 2 {
		t.Errorf("違う内容で本体が増えない: %v", bodies)
	}
}

// 受け入れ条件 3: 上限（1 ファイル・1 プロジェクト）を超える添付は、本体もメタデータも残さずに拒む。
// 上限は同じ Service のまま（再起動せずに）次の添付から効く。対照: 上限ちょうどは通る。
func TestAttachLimits(t *testing.T) {
	e := newAttachEnv(t)
	l, err := store.ReadAttachLimits(e.ctx, e.app)
	if err != nil || l.MaxFile != 20<<20 || l.MaxProject != 1<<30 {
		t.Fatalf("既定の上限: %+v %v", l, err)
	}
	e.mustAttach(e.editor, e.issue, "before.txt", strings.Repeat("a", 11)) // 既定の上限なら通る
	set := func(name string, v int64) {
		t.Helper()
		if _, changed, err := store.SetAttachLimit(e.ctx, e.app, name, v, store.SettingChange{Via: "command"}); err != nil || !changed {
			t.Fatalf("%s = %d: changed=%v %v", name, v, changed, err)
		}
	}
	set(store.SettingAttachMaxFile, 10)
	if _, changed, err := store.SetAttachLimit(e.ctx, e.app, store.SettingAttachMaxFile, 10, store.SettingChange{Via: "command"}); err != nil || changed {
		t.Errorf("同じ値で変更扱いになった: %v %v", changed, err)
	}
	for _, bad := range []struct {
		name string
		v    int64
	}{{store.SettingAttachMaxFile, 0}, {store.SettingAttachMaxProject, -1}, {"two_factor", 5}} {
		if _, _, err := store.SetAttachLimit(e.ctx, e.app, bad.name, bad.v, store.SettingChange{Via: "command"}); err == nil {
			t.Errorf("%s = %d を受け付けた", bad.name, bad.v)
		}
	}
	if cs, _ := store.SettingChanges(e.ctx, e.admin, store.SettingAttachMaxFile, 10); len(cs) != 1 || cs[0].OldValue != "" || cs[0].NewValue != "10" {
		t.Errorf("setting_changes の記録: %+v", cs)
	}

	rows, events := e.count("SELECT COUNT(*) FROM attachments"), e.count("SELECT COUNT(*) FROM issue_events")
	bodies, _ := e.files()
	rejected := func(label, wantCode string, wantKind Kind, err error) {
		t.Helper()
		if errCode(err) != wantCode || errKind(err) != wantKind {
			t.Errorf("%s: err = %v (code %q), want %s", label, err, errCode(err), wantCode)
		}
		b, temps := e.files()
		if len(b) != len(bodies) || len(temps) != 0 || e.count("SELECT COUNT(*) FROM attachments") != rows || e.count("SELECT COUNT(*) FROM issue_events") != events {
			t.Errorf("%s: 拒んだ添付が残った（本体 %v・一時ファイル %v）", label, b, temps)
		}
	}
	_, err = e.attach(e.editor, e.issue, "big.txt", strings.Repeat("b", 11))
	rejected("1 ファイルの上限", "attachment_too_large", TooLarge, err)
	e.mustAttach(e.editor, e.issue, "ok.txt", strings.Repeat("c", 10)) // 対照: ちょうどは通る

	// プロジェクトの上限: いま 21 バイト（11 + 10）。あと 5 バイトまで
	set(store.SettingAttachMaxProject, 26)
	rows, events = e.count("SELECT COUNT(*) FROM attachments"), e.count("SELECT COUNT(*) FROM issue_events")
	bodies, _ = e.files()
	_, err = e.attach(e.editor, e.issue2, "six.txt", "666666")
	rejected("1 プロジェクトの上限", "attachment_quota", Rejected, err)
	// 対照: 別のプロジェクトは数えない・ちょうどは通る
	e.mustAttach(e.editor, e.otherIssue, "other.txt", "777777")
	five := e.mustAttach(e.editor, e.issue2, "five.txt", "55555")
	// 消去した添付は合計に数えない
	if _, err := e.s.PurgeAttachment(e.ctx, e.root, five.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.mustAttach(e.editor, e.issue2, "five2.txt", "44444")
}

// 受け入れ条件 4: 置き場が決まらない構成でも、添付以外の操作は動き、添付の操作だけが「置き場が設定されていない」を返す。
func TestAttachWithoutDir(t *testing.T) {
	e := newAttachEnv(t)
	at := e.mustAttach(e.editor, e.issue, "x.txt", "x") // 置き場があるうちに 1 件
	e.s.AttachDir = ""
	_, err := e.attach(e.editor, e.issue, "y.txt", "y")
	if errCode(err) != "attachments_unavailable" || !strings.Contains(i18n.Text(i18n.JA, err), "置き場が設定されていない") {
		t.Errorf("添付: %v", err)
	}
	if _, _, err := e.s.OpenAttachment(e.ctx, e.viewer, at.ID); errCode(err) != "attachments_unavailable" {
		t.Errorf("読む: %v", err)
	}
	if _, err := e.s.PurgeAttachment(e.ctx, e.root, at.ID, ""); errCode(err) != "attachments_unavailable" {
		t.Errorf("消去: %v", err)
	}
	if _, err := e.s.CheckAttachments(e.ctx); errCode(err) != "attachments_unavailable" {
		t.Errorf("整合の検査: %v", err)
	}
	// 対照: ほかの操作とメタデータの一覧は動く
	if _, err := e.s.Comment(e.ctx, e.editor, e.demo, e.issue.Row.ID, "置き場なしでもコメントできる"); err != nil {
		t.Errorf("コメント: %v", err)
	}
	if _, err := e.s.Create(e.ctx, e.editor, e.demo, CreateInput{Title: "置き場なし"}, i18n.JA); err != nil {
		t.Errorf("起票: %v", err)
	}
	if l, err := e.s.Attachments(e.ctx, e.viewer, e.issue.Item.ID, "demo"); err != nil || len(l) != 1 {
		t.Errorf("一覧: %v %v", l, err)
	}
}

// AttachDirFromEnv は updateStatePath と同じ順で決める。MySQL で LOOPTRACK_ATTACH_DIR と STATE_DIRECTORY が無ければ空。
func TestAttachDirFromEnv(t *testing.T) {
	sl := string(filepath.ListSeparator)
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"LOOPTRACK_DSN": "u:p@tcp(db:3306)/im"}, ""},
		{map[string]string{}, ""},
		{map[string]string{"LOOPTRACK_DSN": "sqlite::memory:"}, ""},
		{map[string]string{"LOOPTRACK_DSN": "u:p@tcp(db:3306)/im", "LOOPTRACK_ATTACH_DIR": "/srv/att"}, "/srv/att"},
		{map[string]string{"LOOPTRACK_DSN": "u:p@tcp(db:3306)/im", "STATE_DIRECTORY": "/var/lib/looptrack" + sl + "/var/lib/other"}, filepath.Join("/var/lib/looptrack", "attachments")},
		{map[string]string{"LOOPTRACK_DSN": "sqlite:/data/im.db"}, filepath.Join("/data", "attachments")},
		{map[string]string{"LOOPTRACK_DSN": "sqlite:/data/im.db", "STATE_DIRECTORY": "/var/lib/looptrack", "LOOPTRACK_ATTACH_DIR": "/srv/att"}, "/srv/att"},
		{map[string]string{"LOOPTRACK_DSN": "sqlite:/data/im.db", "STATE_DIRECTORY": "/var/lib/looptrack"}, filepath.Join("/var/lib/looptrack", "attachments")},
		{map[string]string{"LOOPTRACK_DSN": "u:p@tcp(db:3306)/im", "LOOPTRACK_ATTACH_DIR": "  "}, ""},
	} {
		if got := AttachDirFromEnv(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: %q, want %q", c.env, got, c.want)
		}
	}
}

// failingReader は n バイト返した後に失敗する（本体の書き込みの途中の失敗）。
type failingReader struct {
	r io.Reader
}

func (f *failingReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		return n, errors.New("接続が切れた")
	}
	return n, err
}

// 受け入れ条件 5: 本体の書き込みの途中で失敗したら、メタデータの行も一時ファイルも残らない。
// 対照: 同じ内容を最後まで読めれば、本体が 1 つ置かれ（数え方が空振りしていない）、行が入る。
func TestAttachWriteFailureLeavesNothing(t *testing.T) {
	e := newAttachEnv(t)
	body := strings.Repeat("z", 100)
	_, err := e.s.Attach(e.ctx, e.editor, AttachInput{Issue: e.issue.Item.ID, Filename: "cut.log", Body: &failingReader{r: strings.NewReader(body)}})
	if err == nil || !strings.Contains(err.Error(), "接続が切れた") {
		t.Fatalf("途中の失敗が返らない: %v", err)
	}
	bodies, temps := e.files()
	if len(bodies) != 0 || len(temps) != 0 {
		t.Errorf("本体 %v・一時ファイル %v が残った", bodies, temps)
	}
	if n := e.count("SELECT COUNT(*) FROM attachments"); n != 0 {
		t.Errorf("メタデータが %d 行残った", n)
	}
	if n := e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'attach'"); n != 0 {
		t.Errorf("attach のイベントが %d 件残った", n)
	}
	// 対照
	at, err := e.s.Attach(e.ctx, e.editor, AttachInput{Issue: e.issue.Item.ID, Filename: "dir/sub\\full.log", Body: strings.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	if at.Filename != "full.log" || at.MediaType != "application/octet-stream" {
		t.Errorf("名前・種類: %q %q", at.Filename, at.MediaType)
	}
	if bodies, temps := e.files(); len(bodies) != 1 || len(temps) != 0 {
		t.Errorf("前提が崩れています: 最後まで読めた添付の本体 %v・一時ファイル %v", bodies, temps)
	}
	// 名前と種類の検査
	for _, in := range []AttachInput{{Filename: ""}, {Filename: "a\nb"}, {Filename: "../"}, {Filename: "ok.txt", MediaType: "not a type"}} {
		in.Issue, in.Body = e.issue.Item.ID, strings.NewReader("x")
		if _, err := e.s.Attach(e.ctx, e.editor, in); errKind(err) != Invalid {
			t.Errorf("%q / %q: %v", in.Filename, in.MediaType, err)
		}
	}
}

// 権限: 読む（一覧・本体）は閲覧できる人、添付は editor 以上、消去は管理者。閲覧できないプロジェクトの添付は「見つからない」。
func TestAttachPermissions(t *testing.T) {
	e := newAttachEnv(t)
	at := e.mustAttach(e.editor, e.issue, "e.txt", "evidence")
	if _, err := e.attach(e.viewer, e.issue, "v.txt", "v"); errKind(err) != Forbidden {
		t.Errorf("viewer の添付: %v", err)
	}
	if _, err := e.attach(e.outside, e.issue, "o.txt", "o"); errKind(err) != NotFound {
		t.Errorf("参加していない人の添付: %v", err)
	}
	if _, err := e.attach(Actor{Via: "cli"}, e.issue, "n.txt", "n"); errKind(err) != Forbidden {
		t.Errorf("利用者なしの添付: %v", err)
	}
	_, f, err := e.s.OpenAttachment(e.ctx, e.viewer, at.ID)
	if err != nil {
		t.Fatalf("viewer が読めない: %v", err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "evidence" {
		t.Errorf("本体 = %q", b)
	}
	if _, _, err := e.s.OpenAttachment(e.ctx, e.outside, at.ID); errKind(err) != NotFound {
		t.Errorf("参加していない人が読めた: %v", err)
	}
	if _, err := e.s.Attachments(e.ctx, e.outside, e.issue.Item.ID, ""); errKind(err) != NotFound {
		t.Errorf("参加していない人が一覧を読めた: %v", err)
	}
	if _, _, err := e.s.OpenAttachment(e.ctx, e.viewer, at.ID+1000); errKind(err) != NotFound {
		t.Errorf("無い添付: %v", err)
	}
	for _, a := range []Actor{e.editor, e.viewer} {
		if _, err := e.s.PurgeAttachment(e.ctx, a, at.ID, ""); errKind(err) != Forbidden {
			t.Errorf("管理者でない人の消去: %v", err)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM attachment_purges"); n != 0 {
		t.Errorf("拒んだ消去が %d 件記録された", n)
	}
}

// 受け入れ条件 6: 消去の後、その sha256 の本体はディスクに無く、メタデータは残り、読むと「消去済み」。issue_events に attach_purge がある。
// 同じ本体を指す添付はまとめて消去済みになる。対照: 違う内容の添付は読める。消去の後に同じ内容を添付し直したものは読める。
func TestPurgeAttachment(t *testing.T) {
	e := newAttachEnv(t)
	a1 := e.mustAttach(e.editor, e.issue, "token.txt", "SECRET")
	a2 := e.mustAttach(e.editor, e.otherIssue, "copy.txt", "SECRET")
	keep := e.mustAttach(e.editor, e.issue, "ok.txt", "fine")
	body := AttachmentBodyPath(e.s.AttachDir, a1.SHA256)
	if _, err := os.Stat(body); err != nil {
		t.Fatalf("前提が崩れています: 本体が無い: %v", err)
	}
	res, err := e.s.PurgeAttachment(e.ctx, Actor{UserID: e.root.UserID, Via: "admin"}, a1.ID, "秘密を誤って添付した")
	if err != nil || !res.Changed || len(res.AttachmentIDs) != 2 || res.SHA256 != a1.SHA256 {
		t.Fatalf("消去: %+v %v", res, err)
	}
	if _, err := os.Stat(body); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("本体が残った: %v", err)
	}
	if n := e.count("SELECT COUNT(*) FROM attachments WHERE sha256 = ?", a1.SHA256); n != 2 {
		t.Errorf("メタデータ = %d 行, want 2（残す）", n)
	}
	for _, at := range []*store.Attachment{a1, a2} {
		if _, _, err := e.s.OpenAttachment(e.ctx, e.editor, at.ID); errCode(err) != "attachment_purged" || !strings.Contains(i18n.Text(i18n.JA, err), "消去しました") {
			t.Errorf("添付 %d を読む: %v", at.ID, err)
		}
	}
	if l, _ := e.s.Attachments(e.ctx, e.editor, e.issue.Item.ID, ""); len(l) != 2 || !l[0].Purged || l[1].Purged {
		t.Errorf("一覧の消去済みの印: %+v", l)
	}
	for _, it := range []*Issue{e.issue, e.otherIssue} {
		if n := e.count("SELECT COUNT(*) FROM issue_events WHERE kind = 'attach_purge' AND issue_id = ? AND via = 'admin'", it.Row.ID); n != 1 {
			t.Errorf("%s の attach_purge = %d 件", it.Item.ID, n)
		}
	}
	if n := e.count("SELECT COUNT(*) FROM attachment_purges WHERE sha256 = ? AND attachment_id = ? AND through_attachment_id = ?", a1.SHA256, a1.ID, a2.ID); n != 1 {
		t.Errorf("attachment_purges = %d 件", n)
	}
	// 対照: 違う内容は読める
	if _, f, err := e.s.OpenAttachment(e.ctx, e.editor, keep.ID); err != nil {
		t.Errorf("消していない添付が読めない: %v", err)
	} else {
		f.Close()
	}
	// 2 回目は何も足さない
	if res, err := e.s.PurgeAttachment(e.ctx, e.root, a2.ID, ""); err != nil || res.Changed || e.count("SELECT COUNT(*) FROM attachment_purges") != 1 {
		t.Errorf("2 回目の消去: %+v %v", res, err)
	}
	// 消去の後に同じ内容を添付し直したものは消去済みにしない（本体を置き直す）
	a3 := e.mustAttach(e.editor, e.issue2, "again.txt", "SECRET")
	if _, f, err := e.s.OpenAttachment(e.ctx, e.editor, a3.ID); err != nil {
		t.Errorf("添付し直したものが読めない: %v", err)
	} else {
		f.Close()
	}
	if _, _, err := e.s.OpenAttachment(e.ctx, e.editor, a1.ID); errCode(err) != "attachment_purged" {
		t.Errorf("添付し直した後に古い添付が読めるようになった: %v", err)
	}
}

// 受け入れ条件 7（service の層）: 整合の検査は何も変えずに本体の欠けと、どこからも指されない本体を返す。
// RemoveOrphanBodies はどこからも指されない本体（古いもの）だけを消し、指されている本体・新しい本体・本体の形でないファイルは消さない。
func TestCheckAttachments(t *testing.T) {
	e := newAttachEnv(t)
	live := e.mustAttach(e.editor, e.issue, "live.txt", "live")
	gone := e.mustAttach(e.editor, e.issue, "gone.txt", "gone")
	purged := e.mustAttach(e.editor, e.issue2, "purged.txt", "purged")
	if _, err := e.s.PurgeAttachment(e.ctx, e.root, purged.ID, ""); err != nil {
		t.Fatal(err)
	}
	dir := e.s.AttachDir
	if err := os.Remove(AttachmentBodyPath(dir, gone.SHA256)); err != nil { // 本体の欠け
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * OrphanGrace)
	write := func(sum, content string, mtime time.Time) string {
		t.Helper()
		p := AttachmentBodyPath(dir, sum)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	orphan := write(strings.Repeat("ab", 32), "orphan", old)
	recent := write(strings.Repeat("cd", 32), "recent", time.Now())
	purgedLeft := write(purged.SHA256, "purged", old) // 消去の後に消し損ねた本体（どこからも指されない）
	stray := filepath.Join(dir, "notes.txt")          // 本体の形でないファイルは見ない
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	liveBody := AttachmentBodyPath(dir, live.SHA256)
	if err := os.Chtimes(liveBody, old, old); err != nil {
		t.Fatal(err)
	}
	purges := e.count("SELECT COUNT(*) FROM attachment_purges")

	c, err := e.s.CheckAttachments(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Missing) != 1 || c.Missing[0].ID != gone.ID || c.Missing[0].ProjectSlug != "demo" || c.Missing[0].IssueDisplayID != e.issue.Item.ID {
		t.Errorf("本体の欠け: %+v", c.Missing)
	}
	got := map[string]bool{}
	for _, o := range c.Orphans {
		got[o.Path] = o.Recent
	}
	if len(got) != 3 || got[orphan] || !got[recent] || got[purgedLeft] {
		t.Errorf("どこからも指されない本体: %+v", c.Orphans)
	}
	for _, p := range []string{orphan, recent, purgedLeft, stray, liveBody} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("検査が %s を変えた: %v", p, err)
		}
	}
	if e.count("SELECT COUNT(*) FROM attachment_purges") != purges || e.count("SELECT COUNT(*) FROM attachments") != 3 {
		t.Error("検査が DB を変えた")
	}

	removed, err := e.s.RemoveOrphanBodies(e.ctx, c)
	if err != nil || len(removed) != 2 {
		t.Fatalf("消した: %v %v", removed, err)
	}
	for p, want := range map[string]bool{orphan: false, purgedLeft: false, recent: true, stray: true, liveBody: true} {
		_, err := os.Stat(p)
		if (err == nil) != want {
			t.Errorf("%s: 残っている=%v, want %v", p, err == nil, want)
		}
	}
	if c2, err := e.s.CheckAttachments(e.ctx); err != nil || len(c2.Missing) != 1 || len(c2.Orphans) != 1 {
		t.Errorf("消した後の検査: %+v %v", c2, err)
	}
}

// 置き場がまだ無い（1 度も添付していない）ときの整合の検査は空の結果を返す。
func TestCheckAttachmentsEmptyDir(t *testing.T) {
	e := newAttachEnv(t)
	e.s.AttachDir = filepath.Join(t.TempDir(), "not-yet")
	c, err := e.s.CheckAttachments(e.ctx)
	if err != nil || len(c.Missing) != 0 || len(c.Orphans) != 0 {
		t.Errorf("%+v %v", c, err)
	}
	var buf bytes.Buffer
	buf.WriteString("x") // 対照: 添付すれば置き場ができる
	if _, err := e.s.Attach(e.ctx, e.editor, AttachInput{Issue: e.issue.Item.ID, Filename: "x", Body: &buf}); err != nil {
		t.Fatal(err)
	}
	if c, err := e.s.CheckAttachments(e.ctx); err != nil || len(c.Missing) != 0 || len(c.Orphans) != 0 {
		t.Errorf("添付の後: %+v %v", c, err)
	}
}

// writeOldBody は内容 content の本体を、OrphanGrace より古い更新時刻で置き場に置き、そのパスと sha256 を返す
// （整合の検査で「どこからも指されない古い本体」と見える状態を作る）。
func (e *attachEnv) writeOldBody(content string) (path, sum string) {
	e.t.Helper()
	h := sha256.Sum256([]byte(content))
	sum = hex.EncodeToString(h[:])
	path = AttachmentBodyPath(e.s.AttachDir, sum)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	old := time.Now().Add(-2 * OrphanGrace)
	if err := os.Chtimes(path, old, old); err != nil {
		e.t.Fatal(err)
	}
	return path, sum
}

// 整合の検査で孤立と見えた本体に、検査の後で同じ内容の添付が来たら、RemoveOrphanBodies はその本体を消さない。
// 守りは 3 つあり、どれか 1 つを外すとこのテストが落ちる形にしてある（壊しても緑のまま通る保護を残さないため）:
//   - 消す直前の LiveAttachmentCount の引き直し → 「記録済み・時刻は古いまま」が守られる
//   - 消す直前の更新時刻の再 stat → 「本体は置いたが記録はまだ（別のプロセスの添付の途中）」が守られる
//   - placeAttachBody が既存の本体の更新時刻を進めること → 同じく「記録はまだ」が守られる
//
// 対照: 検査の後に何も来なかった古い孤立本体は消える（消す経路そのものが生きている）。
func TestRemoveOrphanBodiesKeepsBodyReattachedAfterCheck(t *testing.T) {
	e := newAttachEnv(t)
	attached, _ := e.writeOldBody("attached")      // 検査の後に、ふつうに添付される
	recorded, _ := e.writeOldBody("recorded")      // 検査の後に添付が記録されるが、本体の時刻は古く見える（時計のずれなど）
	inflight, inSum := e.writeOldBody("in-flight") // 検査の後に、別のプロセスの添付が本体を置いたところ（記録はまだ）
	orphan, _ := e.writeOldBody("orphan")          // 対照: 検査の後に何も来ない

	c, err := e.s.CheckAttachments(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, o := range c.Orphans {
		if o.Recent {
			t.Fatalf("前提が崩れています: 古く置いた本体が Recent と見えた: %+v", o)
		}
		seen[o.Path] = true
	}
	for _, p := range []string{attached, recorded, inflight, orphan} {
		if !seen[p] {
			t.Fatalf("前提が崩れています: 検査が %s を孤立と見なかった: %+v", p, c.Orphans)
		}
	}

	e.mustAttach(e.editor, e.issue, "attached.txt", "attached")
	e.mustAttach(e.editor, e.issue, "recorded.txt", "recorded")
	old := time.Now().Add(-2 * OrphanGrace)
	if err := os.Chtimes(recorded, old, old); err != nil {
		t.Fatal(err)
	}
	tmp, sum, _, err := writeAttachTemp(e.s.AttachDir, strings.NewReader("in-flight"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if sum != inSum {
		t.Fatalf("前提が崩れています: sha256 = %s, want %s", sum, inSum)
	}
	if err := placeAttachBody(e.s.AttachDir, tmp, sum); err != nil {
		t.Fatal(err)
	}

	removed, err := e.s.RemoveOrphanBodies(e.ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != orphan {
		t.Errorf("消した = %v, want [%s]", removed, orphan)
	}
	for _, w := range []struct {
		name, path string
		keep       bool
	}{{"attached", attached, true}, {"recorded", recorded, true}, {"in-flight", inflight, true}, {"orphan", orphan, false}} {
		_, err := os.Stat(w.path)
		if (err == nil) != w.keep {
			t.Errorf("%s の本体: 残っている=%v, want %v", w.name, err == nil, w.keep)
		}
	}
}

// 置き場がシンボリックリンクでも、整合の検査はリンクの先を辿って孤立した本体を報告し、RemoveOrphanBodies はそれを消す。
// 報告するパスは設定した置き場（リンク）の下の形のまま。
func TestCheckAttachmentsThroughSymlinkedDir(t *testing.T) {
	e := newAttachEnv(t)
	base := t.TempDir()
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("この環境ではシンボリックリンクを作れない（Windows で権限・開発者モードが無いなど）: %v", err)
	}
	e.s.AttachDir = link
	live := e.mustAttach(e.editor, e.issue, "live.txt", "live")
	orphan, _ := e.writeOldBody("orphan")
	liveBody := AttachmentBodyPath(link, live.SHA256)
	old := time.Now().Add(-2 * OrphanGrace)
	if err := os.Chtimes(liveBody, old, old); err != nil {
		t.Fatal(err)
	}

	c, err := e.s.CheckAttachments(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Missing) != 0 {
		t.Errorf("本体の欠け: %+v", c.Missing)
	}
	if len(c.Orphans) != 1 || c.Orphans[0].Path != orphan || c.Orphans[0].Recent {
		t.Fatalf("どこからも指されない本体 = %+v, want 1 件（%s）", c.Orphans, orphan)
	}
	removed, err := e.s.RemoveOrphanBodies(e.ctx, c)
	if err != nil || len(removed) != 1 || removed[0] != orphan {
		t.Fatalf("消した = %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(target, strings.TrimPrefix(orphan, link))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("リンクの先の孤立本体が残っている: %v", err)
	}
	if _, err := os.Stat(liveBody); err != nil {
		t.Errorf("指されている本体が消えた: %v", err)
	}
	if c2, err := e.s.CheckAttachments(e.ctx); err != nil || len(c2.Orphans) != 0 || len(c2.Missing) != 0 {
		t.Errorf("消した後の検査: %+v %v", c2, err)
	}
}
