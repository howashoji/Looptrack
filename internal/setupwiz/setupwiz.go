// Package setupwiz はサーバの初回立ち上げの対話ウィザード（looptrack setup）。
//
// 問いを順に聞き（①使い方 ②保存先 ③待ち受け ④最初の管理者 ⑤二段階認証 ⑥最初のプロジェクト）、答えから次を作る:
//
//   - <dir>/.env（LOOPTRACK_DSN・LOOPTRACK_SECRET_KEY ほか。本人だけ: unix は 0600、Windows は本人だけの ACL。privfile）
//   - チームのサーバ（起動の方法 --service compose。既定）なら <dir>/compose.yaml（deploy/compose.yaml を元にした雛形）と、
//     その build が使う <dir>/Dockerfile・<dir>/NOTICE（linux で動いていれば <dir>/looptrack に自分自身も複製する）
//   - 保存先の DB のスキーマ（マイグレーション）と最初の管理者・二段階認証の設定
//
// 途中でやめても壊れないように、ファイルは一時ファイルに書いてから最後に rename し、DB の管理者作成は最後の段で行う。
// 管理者作成が失敗したら、置いたファイルを元に戻す（--force で置き換えた古いファイルも戻す）。
// 2 回目の実行は現状を示して何も書き換えない（Options.Force で作り直す）。
//
// DB への操作は Backend で差し替えられる（cmd/looptrack が store と user add の処理で実装する）。
package setupwiz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	looptrack "github.com/howashoji/looptrack"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/privfile"
)

// 使い方と保存先の値（--mode・--store）。
const (
	ModeLocal   = "local"
	ModeTeam    = "team"
	StoreSQLite = "sqlite"
	StoreMySQL  = "mysql"

	EnvFile     = ".env"
	ComposeFile = "compose.yaml"
	// DockerfileName と NoticeFile・BinaryFile は compose のときに compose.yaml と一緒に置く
	// （compose.yaml の build がこの 3 つからイメージを作る）。
	DockerfileName = "Dockerfile"
	NoticeFile     = "NOTICE"
	BinaryFile     = "looptrack"

	TwoFactorRequired = "required"
	TwoFactorOptional = "optional"

	// 起動の方法（--service。チームのサーバだけ）。既定は compose
	ServiceCompose = "compose" // docker compose（compose.yaml を書く・SQLite はコンテナの /data/im.db）
	ServiceSystemd = "systemd" // systemd のサービス（compose.yaml を書かない・127.0.0.1 で待ち受け・SQLite はホストのパス）
	ServiceNone    = "none"    // 起動の方法は案内しない（ファイルは systemd と同じ）

	DefaultPort     = 8090
	DefaultBasePath = "/looptrack"
	// DefaultAdminLogin は管理者のログイン名の既定値。
	DefaultAdminLogin = "admin"
	// sqliteContainerPath はチームのサーバで SQLite を使うときのコンテナ内のファイル（compose で ./data を /data に置く）。
	sqliteContainerPath = "/data/im.db"
)

var (
	// ErrInterrupted は Ctrl-C などで中断したことを表す（書きかけのものは残さない）。
	ErrInterrupted = i18n.Errorf("setupwiz.err.interrupted")
	// ErrCanceled は確認で「いいえ」と答えたことを表す。
	ErrCanceled = i18n.Errorf("setupwiz.err.canceled")
)

// Admin は最初の管理者。
type Admin struct {
	Login        string
	Name         string
	PasswordHash string
	TwoFactor    string // required / optional
}

// Backend は保存先の DB への操作。dsn は LOOPTRACK_DSN と同じ形（MySQL の DSN か sqlite:<パス>）。
type Backend interface {
	// Inspect は接続を確かめ、利用者の数を返す（テーブルがまだ無ければ 0）。
	Inspect(ctx context.Context, dsn string) (users int, err error)
	// Migrate はスキーマを最新にし、適用したマイグレーションの名前を返す。
	Migrate(ctx context.Context, dsn string) (applied []string, err error)
	// CreateAdmin は管理者を作り、二段階認証の設定を保存し、最初のプロジェクト（p.Slug が空なら作らない）を作って
	// 管理者を admin で参加させる（Provision）。失敗したときは何も残さない。表示するメッセージを返す。
	// lang は結果のメッセージと、設定変更の記録の備考（setting_changes.note。書き込んだときの文面で残る）の言語。
	CreateAdmin(ctx context.Context, dsn string, a Admin, p FirstProject, lang i18n.Lang) (msg string, err error)
}

// Preset は引数・環境変数で渡された答え（空・0 は未指定）。対話では既定値になり、--yes ではそのまま使う。
type Preset struct {
	Mode          string
	Store         string
	DSN           string // MySQL: .env に書く接続先
	MigrateDSN    string // MySQL: このコマンドがマイグレーションと管理者作成に使う接続先（空なら DSN）
	SQLitePath    string // ローカルの SQLite のファイル（空なら <dir>/im.db）
	Port          int
	BasePath      string
	PublicURL     string
	AdminLogin    string
	AdminName     string
	AdminPassword string
	TwoFactor     string
	// Service は起動の方法（compose / systemd / none。空は既定: チームなら compose）。対話では聞かない（引数だけ）
	Service string
	// 最初のプロジェクト（⑥）。--yes では ProjectSlug があるときだけ作る
	ProjectSlug   string
	ProjectPrefix string
	ProjectName   string
}

// Options は Run の設定。
type Options struct {
	Dir   string // 出力先ディレクトリ（無ければ作る）
	Yes   bool   // 対話しない（Preset と既定値で進める）
	Force bool   // 設定済みでも作り直す
	In    io.Reader
	Out   io.Writer
	// ReadSecret は端末でエコーせずに 1 行読む（パスワード用）。nil なら In から 1 行読む。
	ReadSecret func() (string, error)
	Preset     Preset
	Backend    Backend
	Now        func() time.Time
	// Lang は画面に出す文面の言語。ゼロ値は日本語（対訳表の正本）。
	// 利用者の言語は呼び出し元が i18n.FromEnv などで決めて渡す。
	Lang i18n.Lang
	// LinuxBinary は compose のイメージに載せる linux の実行ファイル。空なら置かず、
	// 起動の案内で「自分で置く」ことを示す。scratch のコンテナは linux なので、
	// macOS・Windows で動かしたときの自分自身を載せても動かない（呼び出し元が判断して渡す）。
	LinuxBinary string
}

// Result は Run の結果（テスト・呼び出し元の表示用）。
type Result struct {
	AlreadyConfigured bool     // 設定済みのため何もしなかった
	Wrote             []string // 置いたファイル
	Plan              *Plan
}

// Plan は確定した答え。
type Plan struct {
	Mode       string
	Service    string // 起動の方法（チームのサーバ: compose / systemd / none。ローカル: 空か none）
	Store      string
	DSN        string // .env に書く LOOPTRACK_DSN
	SetupDSN   string // このコマンドが繋ぐ接続先
	SQLiteFile string // ホスト側の SQLite のファイル（SQLite のときだけ）
	Port       int
	BasePath   string
	PublicURL  string // 外から見た URL の基点（パスなし）
	Admin      Admin
	Project    FirstProject // 最初のプロジェクト（Slug が空なら作らない）
	SecretKey  string
}

// compose は compose.yaml を書き、SQLite をコンテナの中のパスで使うか（チームのサーバの既定）。
func (p *Plan) compose() bool { return p.Mode == ModeTeam && p.Service == ServiceCompose }

// URL はブラウザで開く URL の基点（例 http://127.0.0.1:8090/looptrack）。
func (p *Plan) URL() string { return strings.TrimRight(p.PublicURL, "/") + p.BasePath }

// Run はウィザードを実行する。ctx の取り消し（Ctrl-C）は、書き込みの確定前なら ErrInterrupted で戻り、何も残さない。
// 確定の段（ファイルの rename と管理者作成）に入った後は取り消しを待たずに最後まで行う。
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Backend == nil {
		return nil, i18n.Errorf("setupwiz.err.no_backend")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Lang == "" {
		o.Lang = i18n.JA
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.In == nil {
		o.In = strings.NewReader("")
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	o.Dir = dir
	w := o.Out

	existing, err := readEnvFile(filepath.Join(dir, EnvFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, i18n.Wrapf(err, "setupwiz.err.env_unreadable", "path", filepath.Join(dir, EnvFile))
	}
	if existing != nil && !o.Force {
		showConfigured(ctx, w, o.Lang, dir, existing, o.Backend, o.Preset.MigrateDSN)
		return &Result{AlreadyConfigured: true}, nil
	}

	var plan *Plan
	if o.Yes {
		plan, err = planFromPreset(o.Dir, o.Preset, existing)
	} else {
		fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.start.header", "dir", dir))
		fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.start.hint"))
		if existing != nil {
			fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.start.force"))
		}
		p := newPrompter(ctx, o.In, w, o.ReadSecret, o.Lang)
		plan, err = askPlan(p, o.Dir, o.Preset, existing)
	}
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ErrInterrupted
	}

	// 鍵: --force で作り直すときも、今の LOOPTRACK_SECRET_KEY は引き継ぐ（失うと全員の TOTP が使えなくなる）
	if existing != nil && existing["LOOPTRACK_SECRET_KEY"] != "" {
		plan.SecretKey = existing["LOOPTRACK_SECRET_KEY"]
	} else if plan.SecretKey, err = newSecretKey(); err != nil {
		return nil, err
	}
	return apply(ctx, o, plan, existing != nil)
}

// apply は確定した答えを書き出す。
func apply(ctx context.Context, o Options, plan *Plan, replacing bool) (res *Result, err error) {
	w := o.Out
	var undo undoList
	defer func() {
		if err != nil {
			undo.run()
		}
	}()
	interrupted := func() error {
		if ctx.Err() != nil {
			return ErrInterrupted
		}
		return nil
	}

	// 出力先・SQLite のファイル: 新しく作ったものだけ、失敗したら消す
	if _, err := mkdirTracked(o.Dir, &undo); err != nil {
		return nil, err
	}
	if plan.SQLiteFile != "" {
		created, err := mkdirTracked(filepath.Dir(plan.SQLiteFile), &undo)
		if err != nil {
			return nil, err
		}
		if created {
			// 新しく作った DB のディレクトリは本人だけにする（unix 0700・Windows は中のファイルに継承させる本人だけの ACL。
			// Windows の SQLite の -wal・-shm はディレクトリの ACL を継承するため。本体は store.Open が本人だけで作る）
			if err := privfile.ProtectDir(filepath.Dir(plan.SQLiteFile)); err != nil {
				return nil, err
			}
		}
		if _, statErr := os.Stat(plan.SQLiteFile); errors.Is(statErr, os.ErrNotExist) {
			f := plan.SQLiteFile
			undo.add(func() {
				for _, s := range []string{"", "-wal", "-shm", "-journal"} {
					os.Remove(f + s)
				}
			})
		}
	}

	fmt.Fprintf(w, "\n%s\n", i18n.T(o.Lang, "setupwiz.progress.connecting"))
	users, err := o.Backend.Inspect(ctx, plan.SetupDSN)
	if err2 := interrupted(); err2 != nil {
		return nil, err2
	}
	if err != nil {
		return nil, i18n.Wrapf(err, "setupwiz.err.store_unreachable")
	}
	if users > 0 && !o.Force {
		fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.configured.users_exist", "count", users))
		fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.configured.use_force"))
		undo.run() // この実行で作ったディレクトリだけ消す
		return &Result{AlreadyConfigured: true, Plan: plan}, nil
	}

	// ファイルを一時ファイルに書く（確定は最後の rename）
	type staged struct{ tmp, dst string }
	var files []staged
	stage := func(name, content string, private bool) error {
		tmp, err := writeTemp(o.Dir, name, content, private)
		if err != nil {
			return err
		}
		undo.add(func() { os.Remove(tmp) })
		files = append(files, staged{tmp, filepath.Join(o.Dir, name)})
		return nil
	}
	now := o.Now()
	if err := stage(EnvFile, renderEnv(plan, now, o.Lang), true); err != nil {
		return nil, err
	}
	if plan.compose() {
		if err := stage(ComposeFile, renderCompose(plan, now, o.Lang), false); err != nil {
			return nil, err
		}
		// compose.yaml の build が使う材料。3 つそろって初めて docker compose up -d でイメージができる。
		if err := stage(DockerfileName, renderDockerfile(now, o.Lang), false); err != nil {
			return nil, err
		}
		if err := stage(NoticeFile, looptrack.Notice, false); err != nil {
			return nil, err
		}
		if o.LinuxBinary != "" {
			bin, err := os.ReadFile(o.LinuxBinary)
			if err != nil {
				return nil, i18n.Wrapf(err, "setupwiz.err.binary_unreadable")
			}
			if err := stage(BinaryFile, string(bin), false); err != nil {
				return nil, err
			}
			// COPY は元のパーミッションを引き継ぐので、実行できる形にしておく
			if err := os.Chmod(files[len(files)-1].tmp, 0o755); err != nil {
				return nil, err
			}
		}
	}
	if err := interrupted(); err != nil {
		return nil, err
	}

	fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.progress.migrating"))
	applied, err := o.Backend.Migrate(ctx, plan.SetupDSN)
	if err2 := interrupted(); err2 != nil {
		return nil, err2
	}
	if err != nil {
		return nil, i18n.Wrapf(err, "setupwiz.err.migrate_failed")
	}
	if len(applied) > 0 {
		fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.progress.migrated", "count", len(applied), "first", applied[0], "last", applied[len(applied)-1]))
	} else {
		fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.progress.up_to_date"))
	}

	// ここから確定の段。取り消しは待たずに最後まで行う（途中で止めると、ファイルと利用者の片方だけが残るため）
	commit := context.WithoutCancel(ctx)
	stamp := now.Format("20060102-150405")
	var wrote []string
	for _, f := range files {
		if _, err := os.Stat(f.dst); err == nil {
			if !replacing && filepath.Base(f.dst) == EnvFile {
				return nil, i18n.Errorf("setupwiz.err.env_created_midway", "path", f.dst)
			}
			bak := f.dst + ".bak-" + stamp
			if err := os.Rename(f.dst, bak); err != nil {
				return nil, err
			}
			dst := f.dst
			undo.add(func() { os.Rename(bak, dst) })
			fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.progress.backed_up", "path", bak))
		}
		if err := os.Rename(f.tmp, f.dst); err != nil {
			return nil, err
		}
		dst := f.dst
		undo.add(func() { os.Remove(dst) })
		wrote = append(wrote, f.dst)
	}
	syncDir(o.Dir)

	fmt.Fprintln(w, i18n.T(o.Lang, "setupwiz.progress.creating_admin"))
	msg, err := o.Backend.CreateAdmin(commit, plan.SetupDSN, plan.Admin, plan.Project, o.Lang)
	if err != nil {
		return nil, i18n.Wrapf(err, "setupwiz.err.admin_failed")
	}
	fmt.Fprintf(w, "  %s\n", msg)
	undo = nil // 確定
	printDone(w, o.Lang, plan, wrote, plan.compose() && o.LinuxBinary == "")
	return &Result{Wrote: wrote, Plan: plan}, nil
}

// showConfigured は設定済みの内容を示す（何も書き換えない）。
func showConfigured(ctx context.Context, w io.Writer, lang i18n.Lang, dir string, env map[string]string, b Backend, setupDSN string) {
	mode := ModeTeam
	if isTrue(env["LOOPTRACK_LOCAL_MODE"]) {
		mode = ModeLocal
	}
	dsn := env["LOOPTRACK_DSN"]
	store := StoreMySQL
	if strings.HasPrefix(dsn, "sqlite:") {
		store = StoreSQLite
	}
	fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.header", "path", filepath.Join(dir, EnvFile)))
	fmt.Fprintln(w, i18n.T(lang, "setupwiz.confirm.mode", "value", modeLabel(lang, mode)))
	fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.store", "value", storeLabel(lang, store)))
	fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.listen", "value", env["LOOPTRACK_LISTEN"]))
	if u := env["LOOPTRACK_PUBLIC_URL"]; u != "" {
		fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.url", "url", strings.TrimRight(u, "/")+env["LOOPTRACK_BASE_PATH"]))
	}
	if setupDSN == "" {
		setupDSN = dsn
	}
	if n, err := b.Inspect(ctx, setupDSN); err != nil {
		fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.users_unknown", "reason", err))
	} else if n == 0 {
		fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.users_none"))
	} else {
		fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.users_count", "count", n))
	}
	fmt.Fprintln(w, i18n.T(lang, "setupwiz.configured.use_force_detail"))
}

// printDone は最後の案内（ブラウザ・CLI・MCP）を出す。
func printDone(w io.Writer, lang i18n.Lang, p *Plan, wrote []string, needBinary bool) {
	url := p.URL()
	fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "setupwiz.done.header"))
	for _, f := range wrote {
		fmt.Fprintln(w, i18n.T(lang, "setupwiz.done.created", "path", f))
	}
	fmt.Fprintln(w, i18n.T(lang, "setupwiz.done.keep_secret"))
	dir := filepath.Dir(wrote[0])
	switch {
	case p.Service == ServiceNone: // 起動の方法は呼び出し元（インストーラなど）が案内する
	case p.Mode == ModeLocal:
		fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "setupwiz.done.section.start"))
		fmt.Fprintf(w, "  cd %s && looptrack serve --env-file ./.env\n", shellQuote(dir))
	default:
		fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "setupwiz.done.section.start"))
		if p.Service == ServiceSystemd {
			fmt.Fprintln(w, "  systemctl enable --now looptrack")
		} else {
			if needBinary {
				// このウィザードは linux 以外で動いており、自分自身はコンテナ（scratch・linux）では動かない
				fmt.Fprintln(w, i18n.T(lang, "setupwiz.done.put_linux_binary", "path", filepath.Join(dir, BinaryFile)))
				fmt.Fprintln(w, i18n.T(lang, "setupwiz.done.put_linux_binary_detail"))
				fmt.Fprintf(w, "  2. cd %s && docker compose up -d\n", shellQuote(dir))
			} else {
				fmt.Fprintf(w, "  cd %s && docker compose up -d\n", shellQuote(dir))
			}
		}
		fmt.Fprintln(w, i18n.T(lang, "setupwiz.done.reverse_proxy", "url", url, "port", p.Port, "base", p.BasePath))
	}
	fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "setupwiz.done.section.browser"))
	fmt.Fprintf(w, "  %s/\n", url)
	fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "setupwiz.done.section.cli"))
	fmt.Fprintf(w, "  looptrack issue login --browser --url %s\n", url)
	if p.Project.Slug != "" {
		fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "setupwiz.done.section.project"))
		fmt.Fprintf(w, "  %s/p/%s/\n", url, p.Project.Slug)
	}
	fmt.Fprintf(w, "\n%s\n", i18n.T(lang, "setupwiz.done.section.mcp"))
	for _, c := range MCPConfigs(lang, url, p.Project.Slug) {
		fmt.Fprintln(w, i18n.T(lang, "setupwiz.done.mcp_item", "client", c.Client, "where", c.Where))
		for _, l := range strings.Split(c.Text, "\n") {
			fmt.Fprintf(w, "    %s\n", l)
		}
		if c.Note != "" {
			fmt.Fprintln(w, i18n.T(lang, "setupwiz.done.mcp_note", "note", c.Note))
		}
	}
}

func modeLabel(lang i18n.Lang, m string) string {
	if m == ModeLocal {
		return i18n.T(lang, "setupwiz.label.mode.local")
	}
	return i18n.T(lang, "setupwiz.label.mode.team")
}

func storeLabel(lang i18n.Lang, s string) string {
	if s == StoreSQLite {
		return i18n.T(lang, "setupwiz.label.store.sqlite")
	}
	return i18n.T(lang, "setupwiz.label.store.mysql")
}

func twoFactorLabel(lang i18n.Lang, v string) string {
	if v == TwoFactorRequired {
		return i18n.T(lang, "setupwiz.label.two_factor.required")
	}
	return i18n.T(lang, "setupwiz.label.two_factor.optional")
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// undoList は失敗したときに戻す操作（後に積んだものから戻す）。
type undoList []func()

func (u *undoList) add(f func()) { *u = append(*u, f) }

func (u undoList) run() {
	for i := len(u) - 1; i >= 0; i-- {
		u[i]()
	}
}

// mkdirTracked はディレクトリを作り、新しく作った階層を undo に積む（空なら消す）。dir 自体を新しく作ったかを返す。
func mkdirTracked(dir string, undo *undoList) (created bool, err error) {
	var made []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}
		made = append(made, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	// 戻すときは後に積んだものから行うので、浅い階層から積む（深い階層から消える）
	for i := len(made) - 1; i >= 0; i-- {
		d := made[i]
		undo.add(func() { os.Remove(d) })
	}
	return len(made) > 0, nil
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
