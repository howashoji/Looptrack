package setupwiz

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/privfile"
)

// fakeBackend は DB の代わり（呼ばれた順と作った管理者を記録する）。
type fakeBackend struct {
	users      int
	calls      []string
	dsns       []string
	admins     []Admin
	projects   []FirstProject
	inspectErr error
	createErr  error
	onMigrate  func()
	langs      []i18n.Lang // CreateAdmin に渡された言語（結果の行と設定変更の記録の備考の言語）
}

func (f *fakeBackend) Inspect(_ context.Context, dsn string) (int, error) {
	f.calls = append(f.calls, "inspect")
	f.dsns = append(f.dsns, dsn)
	return f.users, f.inspectErr
}

func (f *fakeBackend) Migrate(_ context.Context, dsn string) ([]string, error) {
	f.calls = append(f.calls, "migrate")
	if f.onMigrate != nil {
		f.onMigrate()
	}
	return []string{"0001_init.sql", "0013_x.sql"}, nil
}

func (f *fakeBackend) CreateAdmin(ctx context.Context, dsn string, a Admin, p FirstProject, lang i18n.Lang) (string, error) {
	f.calls = append(f.calls, "create")
	f.langs = append(f.langs, lang)
	if ctx.Err() != nil {
		return "", errors.New("確定の段で取り消しが伝わった")
	}
	if f.createErr != nil {
		return "", f.createErr
	}
	f.admins = append(f.admins, a)
	f.projects = append(f.projects, p)
	f.users++
	return "作成: " + a.Login, nil
}

var fixedNow = func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }

func lines(ss ...string) io.Reader { return strings.NewReader(strings.Join(ss, "\n") + "\n") }

const pw = "correct-horse-battery"

// localAnswers はローカル・SQLite・既定値の対話の入力（①〜⑥と確認）。
func localAnswers() io.Reader {
	return lines("", "", "", "", "", "alice", "Alice", pw, pw, "", "", "", "", "")
}

// teamAnswers はチーム・MySQL の対話の入力。
func teamAnswers() io.Reader {
	return lines("2", "2", "im_app:pw@tcp(mysql:3306)/im?parseTime=true", "root:rootpw@tcp(127.0.0.1:3306)/im?parseTime=true",
		"9000", "/tracker/", "https://im.example.com/", "alice", "Alice", pw, pw, "1", "web", "", "Web サイト", "y")
}

func run(t *testing.T, o Options) (*Result, string, error) {
	t.Helper()
	var out strings.Builder
	o.Out = &out
	if o.Now == nil {
		o.Now = fixedNow
	}
	res, err := Run(context.Background(), o)
	return res, out.String(), err
}

func envOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	env, err := readEnvFile(filepath.Join(dir, EnvFile))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// mustBePrivate は本人だけのファイルか確かめる（unix は 0600。Windows は本人だけの ACL を privfile.Check で。
// Windows の DACL そのものは internal/privfile の windows のテストで確かめる）。
func mustBePrivate(t *testing.T, path string) {
	t.Helper()
	if err := privfile.Check(path); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", path, st, err)
		}
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s が残っている（%v）", path, err)
	}
}

// 受け入れ条件: 対話で①〜⑤を順に聞き、.env（0600）と管理者ができる。
func TestInteractiveLocal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	fb := &fakeBackend{}
	res, out, err := run(t, Options{Dir: dir, In: localAnswers(), Backend: fb})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	last := -1
	for _, h := range []string{"① 使い方", "② 保存先", "③ 待ち受け", "④ 最初の管理者", "⑤ 二段階認証", "⑥ 最初のプロジェクト", "この内容で書き込みます", "セットアップが終わりました"} {
		i := strings.Index(out, h)
		if i < 0 || i < last {
			t.Fatalf("%q が順に出ていない:\n%s", h, out)
		}
		last = i
	}
	if strings.Contains(out, pw) {
		t.Error("パスワードが画面に出た")
	}
	mustBePrivate(t, filepath.Join(dir, EnvFile))
	env := envOf(t, dir)
	db := filepath.Join(dir, "im.db")
	want := map[string]string{"LOOPTRACK_DSN": "sqlite:" + db, "LOOPTRACK_LISTEN": "127.0.0.1:8090", "LOOPTRACK_BASE_PATH": "/looptrack",
		"LOOPTRACK_PUBLIC_URL": "http://127.0.0.1:8090", "LOOPTRACK_COOKIE_SECURE": "false", "LOOPTRACK_LOCAL_MODE": "1"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(env["LOOPTRACK_SECRET_KEY"]) != 44 {
		t.Errorf("LOOPTRACK_SECRET_KEY = %q", env["LOOPTRACK_SECRET_KEY"])
	}
	mustNotExist(t, filepath.Join(dir, ComposeFile))
	if got := strings.Join(fb.calls, ","); got != "inspect,migrate,create" {
		t.Errorf("calls = %s", got)
	}
	if len(fb.admins) != 1 || fb.admins[0].Login != "alice" || fb.admins[0].Name != "Alice" || fb.admins[0].TwoFactor != TwoFactorOptional ||
		!strings.HasPrefix(fb.admins[0].PasswordHash, "$argon2id$") {
		t.Errorf("admins = %+v", fb.admins)
	}
	// ⑥ は Enter だけで既定のプロジェクト（main）を作る
	if want := (FirstProject{Slug: "main", Prefix: "MAIN", Name: "main"}); len(fb.projects) != 1 || fb.projects[0] != want {
		t.Errorf("projects = %+v, want %+v", fb.projects, want)
	}
	for _, s := range []string{"http://127.0.0.1:8090/looptrack/", "looptrack issue login --browser --url http://127.0.0.1:8090/looptrack",
		"claude mcp add --transport http looptrack http://127.0.0.1:8090/looptrack/mcp", `"url": "http://127.0.0.1:8090/looptrack/mcp"`} {
		if !strings.Contains(out, s) {
			t.Errorf("最後の案内に %q が無い:\n%s", s, out)
		}
	}
	if res.Plan.SQLiteFile != db {
		t.Errorf("SQLiteFile = %s", res.Plan.SQLiteFile)
	}
	// 一時ファイルが残っていない
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".setup-") {
			t.Errorf("一時ファイル %s が残っている", e.Name())
		}
	}
}

// チームのサーバ・MySQL: compose.yaml ができ、テーブル作成用の接続先で繋ぐ。入力の誤りは理由を示して聞き直す。
func TestInteractiveTeam(t *testing.T) {
	dir := t.TempDir()
	fb := &fakeBackend{}
	in := lines("3", "2", "2", "not a dsn", "im_app:pw@tcp(mysql:3306)/im?parseTime=true", "",
		"70000", "9000", "/tracker/", "im.example.com", "https://im.example.com/x", "https://im.example.com/", "-bad", "alice", "", "short", pw, pw, "", "Bad", "", "y")
	_, out, err := run(t, Options{Dir: dir, In: in, Backend: fb})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []string{"1〜2 の番号", "DSN として読めません", "1〜65535", "http(s)://", "パスを付けない", "ログイン名は英数字", "12 文字以上", "slug は英小文字"} {
		if !strings.Contains(out, s) {
			t.Errorf("聞き直しの理由 %q が無い", s)
		}
	}
	if strings.Contains(out, ":pw@") {
		t.Error("DSN のパスワードが確認の表示に出た")
	}
	env := envOf(t, dir)
	want := map[string]string{"LOOPTRACK_DSN": "im_app:pw@tcp(mysql:3306)/im?parseTime=true", "LOOPTRACK_LISTEN": ":9000", "LOOPTRACK_BASE_PATH": "/tracker",
		"LOOPTRACK_PUBLIC_URL": "https://im.example.com", "LOOPTRACK_COOKIE_SECURE": "true"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["LOOPTRACK_LOCAL_MODE"]; ok {
		t.Error("チームのサーバに LOOPTRACK_LOCAL_MODE が付いた")
	}
	compose, err := os.ReadFile(filepath.Join(dir, ComposeFile))
	if err != nil || !strings.Contains(string(compose), `"127.0.0.1:9000:9000"`) || !strings.Contains(string(compose), "env_file: .env") {
		t.Errorf("compose.yaml: %v\n%s", err, compose)
	}
	if fb.admins[0].TwoFactor != TwoFactorRequired || fb.admins[0].Name != "alice" {
		t.Errorf("admin = %+v", fb.admins[0])
	}
	if fb.projects[0] != (FirstProject{}) { // チームのサーバの⑥の既定は作らない（-）
		t.Errorf("project = %+v", fb.projects[0])
	}
	if !strings.Contains(out, "https://im.example.com/tracker/mcp") {
		t.Error("MCP の接続先が無い")
	}
}

// 受け入れ条件: --yes と引数・環境変数で対話と同じ結果。
func TestYesMatchesInteractive(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	fa, fbk := &fakeBackend{}, &fakeBackend{}
	if _, out, err := run(t, Options{Dir: a, In: teamAnswers(), Backend: fa}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pre := Preset{Mode: "team", Store: "mysql", DSN: "im_app:pw@tcp(mysql:3306)/im?parseTime=true",
		MigrateDSN: "root:rootpw@tcp(127.0.0.1:3306)/im?parseTime=true", Port: 9000, BasePath: "/tracker/",
		PublicURL: "https://im.example.com/", AdminLogin: "alice", AdminName: "Alice", AdminPassword: pw, TwoFactor: "required",
		ProjectSlug: "web", ProjectName: "Web サイト"}
	if _, out, err := run(t, Options{Dir: b, Yes: true, Preset: pre, Backend: fbk}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ea, eb := envOf(t, a), envOf(t, b)
	delete(ea, "LOOPTRACK_SECRET_KEY")
	delete(eb, "LOOPTRACK_SECRET_KEY")
	if strings.Join(sortedKV(ea), "\n") != strings.Join(sortedKV(eb), "\n") {
		t.Errorf(".env が違う:\n%v\n%v", sortedKV(ea), sortedKV(eb))
	}
	ca, _ := os.ReadFile(filepath.Join(a, ComposeFile))
	cb, _ := os.ReadFile(filepath.Join(b, ComposeFile))
	if string(ca) != string(cb) {
		t.Error("compose.yaml が違う")
	}
	x, y := fa.admins[0], fbk.admins[0]
	x.PasswordHash, y.PasswordHash = "", ""
	if want := (FirstProject{Slug: "web", Prefix: "WEB", Name: "Web サイト"}); fa.projects[0] != want || fbk.projects[0] != want {
		t.Errorf("最初のプロジェクトが違う: %+v %+v", fa.projects, fbk.projects)
	}
	if x != y || fa.dsns[0] != fbk.dsns[0] || fa.dsns[0] != pre.MigrateDSN {
		t.Errorf("管理者・接続先が違う: %+v %+v %v %v", x, y, fa.dsns, fbk.dsns)
	}
}

func sortedKV(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// 受け入れ条件: 二段階認証の必須 / 任意を必ず聞く（対話では両方の使い方で⑤を出す。--yes では指定が無ければ作らない）。
func TestTwoFactorAlwaysAsked(t *testing.T) {
	for _, in := range []io.Reader{localAnswers(), teamAnswers()} {
		_, out, err := run(t, Options{Dir: t.TempDir(), In: in, Backend: &fakeBackend{}})
		if err != nil || !strings.Contains(out, "⑤ 二段階認証") || !strings.Contains(out, "1) 必須") || !strings.Contains(out, "2) 任意") {
			t.Errorf("⑤ を聞いていない: %v\n%s", err, out)
		}
	}
	// 引数で指定していても対話では聞く（既定値として示す）
	_, out, err := run(t, Options{Dir: t.TempDir(), In: localAnswers(), Backend: &fakeBackend{}, Preset: Preset{TwoFactor: "required"}})
	if err != nil || !strings.Contains(out, "⑤ 二段階認証") {
		t.Errorf("引数があると⑤を飛ばした: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	fb := &fakeBackend{}
	_, _, err = run(t, Options{Dir: dir, Yes: true, Backend: fb, Preset: Preset{AdminPassword: pw}})
	if msg := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(msg, "--two-factor required") || !strings.Contains(msg, "--two-factor optional") {
		t.Errorf("--yes で二段階認証なし: %v（%s）", err, msg)
	}
	if len(fb.calls) != 0 {
		t.Errorf("DB に触った: %v", fb.calls)
	}
	mustNotExist(t, dir)
	_, _, err = run(t, Options{Dir: dir, Yes: true, Backend: fb, Preset: Preset{AdminPassword: pw, TwoFactor: "maybe"}})
	if msg := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(msg, "required") {
		t.Errorf("不正な値: %v（%s）", err, msg)
	}
}

// 受け入れ条件: 中断で部分的な .env や利用者が残らない。
func TestInterruptLeavesNothing(t *testing.T) {
	t.Run("入力の途中で終わる", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "out")
		fb := &fakeBackend{}
		_, _, err := run(t, Options{Dir: dir, In: lines("", "", "", "", "", "alice"), Backend: fb})
		if !errors.Is(err, ErrInputEnded) {
			t.Fatalf("err = %v", err)
		}
		mustNotExist(t, dir)
		if len(fb.calls) != 0 {
			t.Errorf("calls = %v", fb.calls)
		}
	})
	t.Run("入力待ちで Ctrl-C", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "out")
		pr, pw := io.Pipe()
		defer pw.Close()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			pw.Write([]byte("2\n"))
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		_, err := Run(ctx, Options{Dir: dir, In: pr, Backend: &fakeBackend{}})
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("err = %v", err)
		}
		mustNotExist(t, dir)
	})
	t.Run("確認で n", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "out")
		_, _, err := run(t, Options{Dir: dir, In: lines("", "", "", "", "", "alice", "", pw, pw, "", "-", "n"), Backend: &fakeBackend{}})
		if !errors.Is(err, ErrCanceled) {
			t.Fatalf("err = %v", err)
		}
		mustNotExist(t, dir)
	})
	t.Run("マイグレーション中に Ctrl-C", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "out")
		ctx, cancel := context.WithCancel(context.Background())
		fb := &fakeBackend{onMigrate: cancel}
		_, err := Run(ctx, Options{Dir: dir, In: localAnswers(), Backend: fb, Now: fixedNow})
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("err = %v", err)
		}
		mustNotExist(t, dir) // 一時ファイルも、作ったディレクトリも消える
		if len(fb.admins) != 0 {
			t.Error("管理者を作った")
		}
	})
	t.Run("確定の段の Ctrl-C は最後まで行う", func(t *testing.T) {
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		fb := &fakeBackend{}
		// Migrate の後・確定の前には取り消しを見るので、取り消しは CreateAdmin の直前に起きたことにする
		_, err := apply(ctx, Options{Dir: dir, Out: io.Discard, Now: fixedNow, Backend: &cancelOnCreate{fb, cancel}}, testPlan(dir), false)
		if err != nil || len(fb.admins) != 1 {
			t.Fatalf("err = %v admins = %v", err, fb.admins)
		}
		if _, err := os.Stat(filepath.Join(dir, EnvFile)); err != nil {
			t.Error(".env が無い")
		}
	})
	t.Run("管理者の作成に失敗", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "out")
		fb := &fakeBackend{createErr: errors.New("DB が落ちた")}
		_, _, err := run(t, Options{Dir: dir, In: localAnswers(), Backend: fb})
		if msg := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(msg, "元に戻しました") {
			t.Fatalf("err = %v（%s）", err, msg)
		}
		mustNotExist(t, dir)
	})
	t.Run("接続できない", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "out")
		fb := &fakeBackend{inspectErr: errors.New("connection refused")}
		_, _, err := run(t, Options{Dir: dir, In: localAnswers(), Backend: fb})
		if msg := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(msg, "接続できません") {
			t.Fatalf("err = %v（%s）", err, msg)
		}
		mustNotExist(t, dir)
	})
	t.Run("--force の作り直しで失敗したら前の .env に戻す", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, err := run(t, Options{Dir: dir, In: localAnswers(), Backend: &fakeBackend{}}); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(dir, EnvFile))
		fb := &fakeBackend{createErr: errors.New("x")}
		if _, _, err := run(t, Options{Dir: dir, Force: true, In: localAnswers(), Backend: fb}); err == nil {
			t.Fatal("失敗しなかった")
		}
		after, _ := os.ReadFile(filepath.Join(dir, EnvFile))
		if string(before) != string(after) {
			t.Error(".env が元に戻っていない")
		}
		ents, _ := os.ReadDir(dir)
		if len(ents) != 1 {
			t.Errorf("余計なファイル: %v", ents)
		}
	})
}

type cancelOnCreate struct {
	*fakeBackend
	cancel func()
}

func (c *cancelOnCreate) CreateAdmin(ctx context.Context, dsn string, a Admin, p FirstProject, lang i18n.Lang) (string, error) {
	c.cancel()
	return c.fakeBackend.CreateAdmin(ctx, dsn, a, p, lang)
}

func testPlan(dir string) *Plan {
	p := &Plan{Mode: ModeLocal, Store: StoreSQLite, Port: 8090, BasePath: "/im", SecretKey: "k",
		Admin: Admin{Login: "a", Name: "a", PasswordHash: "h", TwoFactor: TwoFactorOptional}}
	setStore(p, dir, filepath.Join(dir, "im.db"), "", "")
	return p
}

// 受け入れ条件: 2 回目は設定済みを示して書き換えない。--force で作り直す（鍵は引き継ぐ）。
func TestSecondRun(t *testing.T) {
	dir := t.TempDir()
	fb := &fakeBackend{}
	if _, _, err := run(t, Options{Dir: dir, In: localAnswers(), Backend: fb}); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, EnvFile)
	before, _ := os.ReadFile(envPath)
	st1, _ := os.Stat(envPath)

	for _, yes := range []bool{false, true} {
		res, out, err := run(t, Options{Dir: dir, Yes: yes, In: localAnswers(), Backend: fb,
			Preset: Preset{AdminPassword: pw, TwoFactor: "required"}})
		if err != nil || !res.AlreadyConfigured || !strings.Contains(out, "設定済みです") || !strings.Contains(out, "利用者: 1 人") ||
			!strings.Contains(out, "--force") {
			t.Fatalf("yes=%v: %v %+v\n%s", yes, err, res, out)
		}
	}
	after, _ := os.ReadFile(envPath)
	st2, _ := os.Stat(envPath)
	if string(before) != string(after) || !st1.ModTime().Equal(st2.ModTime()) || len(fb.admins) != 1 {
		t.Error("2 回目で書き換えた")
	}

	// --force: 作り直す（鍵は引き継ぎ、前の .env は退避）
	old := envOf(t, dir)
	res, out, err := run(t, Options{Dir: dir, Yes: true, Force: true, Backend: fb,
		Preset: Preset{Port: 8123, AdminLogin: "alice", AdminPassword: pw, TwoFactor: "required"}, Now: func() time.Time { return fixedNow().Add(time.Hour) }})
	if err != nil || res.AlreadyConfigured {
		t.Fatalf("%v\n%s", err, out)
	}
	env := envOf(t, dir)
	if env["LOOPTRACK_SECRET_KEY"] != old["LOOPTRACK_SECRET_KEY"] || env["LOOPTRACK_LISTEN"] != "127.0.0.1:8123" || env["LOOPTRACK_DSN"] != old["LOOPTRACK_DSN"] {
		t.Errorf("作り直した .env: %v", env)
	}
	if b, err := os.ReadFile(envPath + ".bak-20260919-130000"); err != nil || string(b) != string(before) {
		t.Errorf("退避: %v", err)
	}
	if len(fb.admins) != 2 || fb.admins[1].TwoFactor != TwoFactorRequired {
		t.Errorf("admins = %+v", fb.admins)
	}
}

// .env が無くても、保存先にすでに利用者がいれば設定済みとして何も書かない。
func TestExistingUsersInDB(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	fb := &fakeBackend{users: 3}
	res, out, err := run(t, Options{Dir: dir, Yes: true, Backend: fb, Preset: Preset{AdminPassword: pw, TwoFactor: "optional"}})
	if err != nil || !res.AlreadyConfigured || !strings.Contains(out, "利用者が 3 人") {
		t.Fatalf("%v %+v\n%s", err, res, out)
	}
	mustNotExist(t, dir)
	if strings.Join(fb.calls, ",") != "inspect" {
		t.Errorf("calls = %v", fb.calls)
	}
}

// チームのサーバの SQLite は、ホストの <dir>/data/im.db に繋ぎ、.env にはコンテナの中のパスを書く。
func TestTeamSQLite(t *testing.T) {
	dir := t.TempDir()
	fb := &fakeBackend{}
	_, out, err := run(t, Options{Dir: dir, Yes: true, Backend: fb, Preset: Preset{Mode: "team", Store: "sqlite",
		PublicURL: "http://im.lan:8090", AdminPassword: pw, TwoFactor: "optional"}})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if env := envOf(t, dir); env["LOOPTRACK_DSN"] != "sqlite:/data/im.db" || env["LOOPTRACK_COOKIE_SECURE"] != "false" {
		t.Errorf("env = %v", env)
	}
	if fb.dsns[0] != "sqlite:"+filepath.Join(dir, "data", "im.db") {
		t.Errorf("dsn = %v", fb.dsns)
	}
	if c, _ := os.ReadFile(filepath.Join(dir, ComposeFile)); !strings.Contains(string(c), "./data:/data") {
		t.Errorf("compose:\n%s", c)
	}
}

// 非対話の入力の誤りは、何も書かずに理由を返す。
func TestYesValidation(t *testing.T) {
	base := Preset{AdminPassword: pw, TwoFactor: "optional"}
	cases := map[string]func(p *Preset){
		"使い方は local":          func(p *Preset) { p.Mode = "solo" },
		"保存先は sqlite か":       func(p *Preset) { p.Store = "pg" },
		"LOOPTRACK_SETUP_DSN": func(p *Preset) { p.Store = "mysql" },
		"--public-url":        func(p *Preset) { p.Mode = "team"; p.Store = "sqlite" },
		"12 文字以上":             func(p *Preset) { p.AdminPassword = "short" },
		"ポート番号":               func(p *Preset) { p.Port = 70000 },
		"ログイン名":               func(p *Preset) { p.AdminLogin = "a b" },
		"単引用符":                func(p *Preset) { p.Store = "mysql"; p.DSN = "u:p'x@tcp(h:3306)/im" },
		"--admin-password":    func(p *Preset) { p.AdminPassword = "" },
		"--project で slug":    func(p *Preset) { p.ProjectPrefix = "X" },
		"prefix は英大文字":        func(p *Preset) { p.ProjectSlug = "9x" },
	}
	for want, mod := range cases {
		p := base
		mod(&p)
		dir := filepath.Join(t.TempDir(), "out")
		_, _, err := run(t, Options{Dir: dir, Yes: true, Backend: &fakeBackend{}, Preset: p})
		// 利用者に見えるのは i18n.Text が作る文面（err.Error() は ID を返す）
		if err == nil || !strings.Contains(i18n.Text(i18n.JA, err), want) {
			t.Errorf("%s: err = %v（%s）", want, err, i18n.Text(i18n.JA, err))
		}
		mustNotExist(t, dir)
	}
}

// compose を選ぶと、compose.yaml の build が使う材料（Dockerfile・NOTICE）も置く。
// イメージの出どころを外の looptrack:latest に頼らないための確認（同じ名前のイメージを他人が公開していても引かない）。
func TestComposeWritesBuildContext(t *testing.T) {
	dir := t.TempDir()
	if _, out, err := run(t, Options{Dir: dir, In: teamAnswers(), Backend: &fakeBackend{}}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	compose, err := os.ReadFile(filepath.Join(dir, ComposeFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"build:", "context: .", "dockerfile: Dockerfile"} {
		if !strings.Contains(string(compose), s) {
			t.Errorf("compose.yaml に %q が無い:\n%s", s, compose)
		}
	}
	df, err := os.ReadFile(filepath.Join(dir, DockerfileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"FROM scratch", "COPY looptrack /looptrack", "COPY NOTICE /NOTICE", "USER 65534:65534"} {
		if !strings.Contains(string(df), s) {
			t.Errorf("Dockerfile に %q が無い:\n%s", s, df)
		}
	}
	notice, err := os.ReadFile(filepath.Join(dir, NoticeFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(notice) == 0 {
		t.Error("NOTICE が空")
	}
	// 実行ファイルを渡していないので置かない（このテストの実行環境に関わらず）
	mustNotExist(t, filepath.Join(dir, BinaryFile))
}

// LinuxBinary を渡すと、イメージに載せる実行ファイルを複製し、実行できる形にする。
func TestComposeCopiesLinuxBinary(t *testing.T) {
	src := filepath.Join(t.TempDir(), "looptrack")
	if err := os.WriteFile(src, []byte("\x7fELF dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, out, err := run(t, Options{Dir: dir, In: teamAnswers(), Backend: &fakeBackend{}, LinuxBinary: src}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dst := filepath.Join(dir, BinaryFile)
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x7fELF dummy" {
		t.Errorf("複製した実行ファイルの中身が違う: %q", got)
	}
	if runtime.GOOS != "windows" { // Windows にパーミッションの概念が無い
		fi, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("実行できる形になっていない: %v", fi.Mode().Perm())
		}
	}
}

// 実行ファイルを置けないとき（linux 以外で動かしたとき）は、起動の案内でそれを求める。
func TestComposeAsksForLinuxBinaryWhenMissing(t *testing.T) {
	dir := t.TempDir()
	_, out, err := run(t, Options{Dir: dir, In: teamAnswers(), Backend: &fakeBackend{}})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "linux/amd64 の looptrack を") {
		t.Errorf("linux の実行ファイルを置く案内が無い:\n%s", out)
	}
	// 渡したときは求めない
	src := filepath.Join(t.TempDir(), "looptrack")
	if err := os.WriteFile(src, []byte("dummy"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, out2, err := run(t, Options{Dir: t.TempDir(), In: teamAnswers(), Backend: &fakeBackend{}, LinuxBinary: src})
	if err != nil {
		t.Fatalf("%v\n%s", err, out2)
	}
	if strings.Contains(out2, "linux/amd64 の looptrack を") {
		t.Errorf("実行ファイルを置いたのに案内が出た:\n%s", out2)
	}
}

// healthcheck は scratch のイメージの中で 2 つ目の looptrack を起動する（シェルも curl も無いため）。
// mem_limit がサーバの GOMEMLIMIT の 2 倍に足りないと、2 つ目が cgroup の上限に当たって SIGKILL され、
// /healthz が 200 を返していてもコンテナは unhealthy のまま再起動を繰り返す。
// 値を下げる変更をこのテストで止める（実測: 96m・112m・128m は unhealthy、160m で healthy）。
func TestComposeMemLimitFitsHealthcheck(t *testing.T) {
	dir := t.TempDir()
	if _, out, err := run(t, Options{Dir: dir, In: teamAnswers(), Backend: &fakeBackend{}}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, ComposeFile))
	if err != nil {
		t.Fatal(err)
	}
	compose := string(b)
	if !strings.Contains(compose, `test: ["CMD", "/looptrack", "healthcheck"]`) {
		t.Fatal("healthcheck が自分自身を起動する形ではない（この検査の前提が変わった）")
	}
	mem := mustMiB(t, compose, `mem_limit:\s*([0-9]+)m`)
	goMem := mustMiB(t, compose, `GOMEMLIMIT:\s*([0-9]+)MiB`)
	if mem < goMem*2 {
		t.Errorf("mem_limit が %d MiB で、GOMEMLIMIT %d MiB の 2 倍に足りない（healthcheck の 2 つ目のプロセスが入らない）", mem, goMem)
	}
}

func mustMiB(t *testing.T, s, pattern string) int {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("%q が compose.yaml に無い", pattern)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// 受け入れ条件「英語を選んだ利用者に、setup の手順が英語で出る」の確認。
// Lang を渡すと、問い・選択肢・確認・完了の案内がすべて英語になる。
func TestWizardInEnglish(t *testing.T) {
	dir := t.TempDir()
	fb := &fakeBackend{}
	_, out, err := run(t, Options{Dir: dir, In: localAnswers(), Backend: fb, Lang: i18n.EN})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// 管理者を作る段（結果の行と二段階認証の変更記録の備考）にも同じ言語を渡す
	if len(fb.langs) != 1 || fb.langs[0] != i18n.EN {
		t.Errorf("CreateAdmin に渡した言語 = %v, want [en]", fb.langs)
	}
	for _, want := range []string{
		"(1) Choose how you will use it",   // 節
		"Local, single user (this machine", // 選択肢
		"Port number",                      // 問い
		"About to write the following",     // 確認
		"Setup is complete.",               // 完了
		"* Browser URL",                    // 完了の中の見出し
	} {
		if !strings.Contains(out, want) {
			t.Errorf("英語の文面 %q が出ていない", want)
		}
	}
	// 日本語の文面が混ざっていないこと（訳し忘れの検出）
	for _, ja := range []string{
		"使い方を選んでください", "ポート番号", "この内容で書き込みます", "セットアップが終わりました", "ブラウザで開く URL",
	} {
		if strings.Contains(out, ja) {
			t.Errorf("英語を選んだのに日本語の文面 %q が出ている", ja)
		}
	}
	// 生成した .env のコメントも英語になる
	b, err := os.ReadFile(filepath.Join(dir, EnvFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Written by looptrack setup") {
		t.Errorf(".env のコメントが英語になっていない:\n%s", firstLines(string(b), 3))
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
