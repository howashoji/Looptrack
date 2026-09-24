package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/privfile"
	"github.com/howashoji/looptrack/internal/setupwiz"
	"github.com/howashoji/looptrack/internal/store"
)

// looptrack setup を MySQL で通す（出力先は一時ディレクトリだけ）。

// emptyDB は LOOPTRACK_TEST_DSN の MySQL にマイグレーション前の空の DB を作り、その DSN と接続を返す。
func emptyDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("LOOPTRACK_TEST_DSN")
	if dsn == "" {
		t.Skip("LOOPTRACK_TEST_DSN が未設定のため DB を使うテストを省略（deploy/dev/compose.yaml を参照）")
	}
	cfg, err := store.NormalizeDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	b := make([]byte, 6)
	rand.Read(b)
	name := "im_test_setup_" + hex.EncodeToString(b)
	if _, err := root.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE " + name) })
	c := cfg.Clone()
	c.DBName = name
	db, err := store.Open(c.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return c.FormatDSN(), db
}

type setupRun struct {
	code           int
	stdout, errOut string
}

func doSetup(ctx context.Context, t *testing.T, env map[string]string, stdin string, b setupwiz.Backend, args ...string) setupRun {
	t.Helper()
	var out, errOut strings.Builder
	// 下の検査は日本語の文面を見るので、言語を明示する。指定が無いと setup は
	// 「端末の設定に従い、日本語でなければ英語」で英語を出す（i18n.FromEnv）。
	getenv := func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		if k == "LOOPTRACK_LANG" {
			return "ja"
		}
		return ""
	}
	code := runSetup(ctx, args, getenv, strings.NewReader(stdin), &out, &errOut, nil, b)
	return setupRun{code, out.String(), errOut.String()}
}

const setupPW = "correct-horse-battery"

func interactiveTeamInput(dsn string) string {
	return strings.Join([]string{"2", "2", dsn, "", "9000", "/im", "https://im.example.com", "alice", "Alice", setupPW, setupPW, "1", "", "y"}, "\n") + "\n"
}

func checkAdmin(t *testing.T, db *sql.DB, twoFactor string) {
	t.Helper()
	ctx := context.Background()
	users, err := store.ListUsers(ctx, db)
	if err != nil || len(users) != 1 || users[0].Login != "alice" || users[0].Role != "admin" || users[0].DisplayName != "Alice" {
		t.Fatalf("users = %+v err = %v", users, err)
	}
	if ok, _ := auth.VerifyPassword(users[0].PasswordHash, setupPW); !ok {
		t.Error("パスワードが合わない")
	}
	policy, set, _ := store.TwoFactorPolicy(ctx, db)
	if !set || policy != twoFactor {
		t.Errorf("二段階認証 = %q set=%v", policy, set)
	}
	cs, _ := store.SettingChanges(ctx, db, store.SettingTwoFactor, 10)
	if len(cs) != 1 || cs[0].Via != "command" || cs[0].NewValue != twoFactor {
		t.Errorf("記録 = %+v", cs)
	}
}

// 受け入れ条件: 対話で①〜⑤を聞き、.env と管理者ができる（MySQL）。--yes と引数・環境変数で同じ結果。
func TestSetupMySQLInteractiveAndYes(t *testing.T) {
	ctx := context.Background()
	dsnA, dbA := emptyDB(t)
	dsnB, dbB := emptyDB(t)
	dirA, dirB := t.TempDir(), filepath.Join(t.TempDir(), "srv")

	r := doSetup(ctx, t, nil, interactiveTeamInput(dsnA), setupBackend{}, "--dir", dirA)
	if r.code != 0 {
		t.Fatalf("対話: %d\n%s\n%s", r.code, r.stdout, r.errOut)
	}
	checkAdmin(t, dbA, "required")

	env := map[string]string{setupwiz.EnvDSN: dsnB, setupwiz.EnvAdminPassword: setupPW}
	r = doSetup(ctx, t, env, "", setupBackend{}, "--dir", dirB, "--yes", "--mode", "team", "--store", "mysql", "--port", "9000",
		"--base-path", "/im", "--public-url", "https://im.example.com", "--admin-login", "alice", "--admin-name", "Alice", "--two-factor", "required")
	if r.code != 0 {
		t.Fatalf("--yes: %d\n%s\n%s", r.code, r.stdout, r.errOut)
	}
	checkAdmin(t, dbB, "required")

	norm := func(dir, dsn string) string {
		raw, err := os.ReadFile(filepath.Join(dir, setupwiz.EnvFile))
		if err != nil {
			t.Fatal(err)
		}
		var keep []string
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(l, "# looptrack setup が作成") || strings.HasPrefix(l, "LOOPTRACK_SECRET_KEY=") {
				continue
			}
			keep = append(keep, strings.ReplaceAll(l, dsn, "<DSN>"))
		}
		return strings.Join(keep, "\n")
	}
	if a, b := norm(dirA, dsnA), norm(dirB, dsnB); a != b {
		t.Errorf("対話と --yes で .env が違う:\n%s\n---\n%s", a, b)
	}
	for _, d := range []string{dirA, dirB} {
		if err := privfile.Check(filepath.Join(d, setupwiz.EnvFile)); err != nil {
			t.Errorf(".env のパーミッション: %v", err)
		}
		if _, err := os.Stat(filepath.Join(d, setupwiz.ComposeFile)); err != nil {
			t.Errorf("compose.yaml: %v", err)
		}
	}
	// パスワードはファイルからも渡せる
	dsnC, dbC := emptyDB(t)
	pwFile := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pwFile, []byte(setupPW+"\n"), 0o600)
	r = doSetup(ctx, t, map[string]string{setupwiz.EnvDSN: dsnC}, "", setupBackend{}, "--dir", t.TempDir(), "--yes", "--mode", "team",
		"--public-url", "https://im.example.com", "--admin-login", "alice", "--admin-name", "Alice", "--admin-password-file", pwFile, "--two-factor", "optional")
	if r.code != 0 {
		t.Fatalf("--admin-password-file: %d\n%s", r.code, r.errOut)
	}
	checkAdmin(t, dbC, "optional")
}

// 受け入れ条件: 中断で部分的な .env や利用者が残らない（MySQL）。
func TestSetupMySQLInterrupt(t *testing.T) {
	dsn, db := emptyDB(t)
	dir := filepath.Join(t.TempDir(), "srv")
	ctx, cancel := context.WithCancel(context.Background())
	r := doSetup(ctx, t, nil, interactiveTeamInput(dsn), &cancelAfterMigrate{cancel: cancel}, "--dir", dir)
	if r.code != 130 || !strings.Contains(r.errOut, "中断しました") {
		t.Fatalf("code = %d\n%s", r.code, r.errOut)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("出力先が残った: %v", err)
	}
	if n, err := store.CountUsers(context.Background(), db); err != nil || n != 0 {
		t.Errorf("利用者 = %d err = %v", n, err)
	}
	// やり直せば通る（マイグレーションは適用済みでも構わない）
	if r := doSetup(context.Background(), t, nil, interactiveTeamInput(dsn), setupBackend{}, "--dir", dir); r.code != 0 {
		t.Fatalf("やり直し: %d\n%s", r.code, r.errOut)
	}
	checkAdmin(t, db, "required")
}

type cancelAfterMigrate struct {
	setupBackend
	cancel func()
}

func (c *cancelAfterMigrate) Migrate(ctx context.Context, dsn string) ([]string, error) {
	applied, err := c.setupBackend.Migrate(ctx, dsn)
	c.cancel()
	return applied, err
}

// 受け入れ条件: 2 回目は設定済みを示して書き換えない。--force で作り直す（MySQL）。
func TestSetupMySQLSecondRunAndForce(t *testing.T) {
	ctx := context.Background()
	dsn, db := emptyDB(t)
	dir := t.TempDir()
	if r := doSetup(ctx, t, nil, interactiveTeamInput(dsn), setupBackend{}, "--dir", dir); r.code != 0 {
		t.Fatalf("1 回目: %s", r.errOut)
	}
	envPath := filepath.Join(dir, setupwiz.EnvFile)
	before, _ := os.ReadFile(envPath)

	r := doSetup(ctx, t, nil, interactiveTeamInput(dsn), setupBackend{}, "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stdout, "設定済みです") || !strings.Contains(r.stdout, "利用者: 1 人") {
		t.Fatalf("2 回目: %d\n%s\n%s", r.code, r.stdout, r.errOut)
	}
	if after, _ := os.ReadFile(envPath); string(after) != string(before) {
		t.Error("2 回目で .env を書き換えた")
	}

	// .env が無くても、DB に利用者がいれば設定済み（別の出力先から同じ DB を指した）
	other := filepath.Join(t.TempDir(), "x")
	r = doSetup(ctx, t, map[string]string{setupwiz.EnvDSN: dsn, setupwiz.EnvAdminPassword: setupPW}, "", setupBackend{},
		"--dir", other, "--yes", "--mode", "team", "--public-url", "https://im.example.com", "--two-factor", "optional")
	if r.code != 0 || !strings.Contains(r.stdout, "利用者が 1 人") {
		t.Fatalf("DB に利用者: %d\n%s\n%s", r.code, r.stdout, r.errOut)
	}
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Error("出力先を作った")
	}

	// --force: 作り直す。既存の管理者は残し、二段階認証の変更は記録する。鍵は引き継ぐ
	oldKey := envValue(t, envPath, "LOOPTRACK_SECRET_KEY")
	r = doSetup(ctx, t, map[string]string{setupwiz.EnvDSN: dsn, setupwiz.EnvAdminPassword: setupPW}, "", setupBackend{},
		"--dir", dir, "--yes", "--force", "--mode", "team", "--port", "9100", "--public-url", "https://im.example.com",
		"--admin-login", "alice", "--two-factor", "optional")
	if r.code != 0 {
		t.Fatalf("--force: %d\n%s\n%s", r.code, r.stdout, r.errOut)
	}
	if envValue(t, envPath, "LOOPTRACK_SECRET_KEY") != oldKey || envValue(t, envPath, "LOOPTRACK_LISTEN") != ":9100" {
		t.Error("作り直した .env が違う")
	}
	if !strings.Contains(r.stdout, "すでにいます") || !strings.Contains(r.stdout, "必須 → 任意") {
		t.Errorf("--force の表示:\n%s", r.stdout)
	}
	users, _ := store.ListUsers(ctx, db)
	if len(users) != 1 {
		t.Errorf("利用者 = %+v", users)
	}
	if cs, _ := store.SettingChanges(ctx, db, store.SettingTwoFactor, 10); len(cs) != 2 || cs[0].Note != "looptrack setup --force" {
		t.Errorf("記録 = %+v", cs)
	}
}

func envValue(t *testing.T, path, key string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return strings.Trim(v, "'")
		}
	}
	return ""
}

// 管理者を作った結果の行（作成・二段階認証）と、二段階認証の変更記録の備考（setting_changes.note）は、
// setup の画面の言語（LOOPTRACK_LANG）で出る・残る。備考は書き込んだときの文面のまま DB に残るので、
// 英語で setup した人の記録に日本語が混ざらないことを見る（日本語の側も同じテストで確かめる）。
func TestSetupResultAndRecordFollowLang(t *testing.T) {
	jaRe := regexp.MustCompile(`[ぁ-んァ-ヶ一-龥]`)
	for _, tc := range []struct {
		lang                   string
		created, twoFactor     string
		note, other, otherNote string
	}{
		{"en", "Created: alice (admin).", "Two-factor auth: Optional (setting saved).",
			"Set when the first user alice was created (looptrack setup)", "作成: alice", "最初の利用者"},
		{"ja", "作成: alice（admin）。", "二段階認証: 任意（設定を保存しました）。",
			"最初の利用者 alice の作成時に指定（looptrack setup）", "Created: alice", "Set when"},
	} {
		dir := filepath.Join(t.TempDir(), "local")
		r := doSetup(context.Background(), t, map[string]string{setupwiz.EnvAdminPassword: setupPW, "LOOPTRACK_LANG": tc.lang}, "", setupBackend{},
			"--dir", dir, "--yes", "--two-factor", "optional", "--admin-login", "alice")
		if r.code != 0 {
			t.Fatalf("%s: setup が失敗: code=%d\n%s", tc.lang, r.code, r.errOut)
		}
		if !strings.Contains(r.stdout, tc.created) || !strings.Contains(r.stdout, tc.twoFactor) || strings.Contains(r.stdout, tc.other) {
			t.Errorf("%s: 結果の行が setup の言語でない（want %q と %q）:\n%s", tc.lang, tc.created, tc.twoFactor, r.stdout)
		}
		db, err := store.Open(envValue(t, filepath.Join(dir, setupwiz.EnvFile), "LOOPTRACK_DSN"))
		if err != nil {
			t.Fatal(err)
		}
		cs, err := store.SettingChanges(context.Background(), db, store.SettingTwoFactor, 5)
		db.Close()
		if err != nil || len(cs) != 1 {
			t.Fatalf("%s: 記録 = %+v %v", tc.lang, cs, err)
		}
		if cs[0].Note != tc.note || strings.Contains(cs[0].Note, tc.otherNote) {
			t.Errorf("%s: 記録の備考 = %q, want %q", tc.lang, cs[0].Note, tc.note)
		}
		if tc.lang == "en" && jaRe.MatchString(cs[0].Note) {
			t.Errorf("en: 記録の備考に日本語が残る: %q", cs[0].Note)
		}
	}
}

// SQLite: ローカル利用で .env と管理者ができ、2 回目は「設定済み」で何も書き換えない（SQLite でも通る）。
func TestSetupSQLiteLocal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "local")
	r := doSetup(context.Background(), t, map[string]string{setupwiz.EnvAdminPassword: setupPW}, "", setupBackend{},
		"--dir", dir, "--yes", "--two-factor", "optional")
	if r.code != 0 {
		t.Fatalf("setup が失敗: code=%d\n%s", r.code, r.errOut)
	}
	envPath := filepath.Join(dir, setupwiz.EnvFile)
	if v := envValue(t, envPath, "LOOPTRACK_LOCAL_MODE"); v != "1" {
		t.Errorf("LOOPTRACK_LOCAL_MODE = %q", v)
	}
	dsn := envValue(t, envPath, "LOOPTRACK_DSN")
	if !strings.HasPrefix(dsn, "sqlite:") {
		t.Fatalf("LOOPTRACK_DSN = %q", dsn)
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var admins int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE role = 'admin'").Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Errorf("管理者の数 = %d", admins)
	}
	before, _ := os.ReadFile(envPath)
	r2 := doSetup(context.Background(), t, map[string]string{setupwiz.EnvAdminPassword: setupPW}, "", setupBackend{},
		"--dir", dir, "--yes", "--two-factor", "optional")
	after, _ := os.ReadFile(envPath)
	if r2.code != 0 || string(before) != string(after) {
		t.Errorf("2 回目で書き換わった: code=%d\n%s", r2.code, r2.errOut)
	}
}
