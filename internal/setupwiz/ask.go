package setupwiz

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 非対話（--yes）で秘密を渡す環境変数（引数に取ると ps に出るため。looptrack setup（cmd/looptrack/setup.go）が読む）。
const (
	EnvDSN           = "LOOPTRACK_SETUP_DSN"            // MySQL の接続先（.env の LOOPTRACK_DSN になる）
	EnvMigrateDSN    = "LOOPTRACK_SETUP_MIGRATE_DSN"    // マイグレーションと管理者作成に使う接続先（空なら LOOPTRACK_SETUP_DSN）
	EnvAdminPassword = "LOOPTRACK_SETUP_ADMIN_PASSWORD" // 最初の管理者のパスワード（--admin-password-file でも渡せる）
)

// ErrInputEnded は対話の入力が途中で終わった（Ctrl-D・パイプの終わり）ことを表す。
var ErrInputEnded = i18n.Errorf("setupwiz.err.input_ended")

// loginRe は Web の管理画面と同じログイン名の規則（internal/server/account.go の loginNameRe）。
var loginRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var basePathRe = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)

// defaults は既定値を決める（引数 > 今の .env（--force のとき）> 組み込みの既定値）。mode は確定済みの使い方。
type defaults struct {
	store, dsn, migrateDSN, sqlitePath, basePath, publicURL, login, name, twoFactor string
	projectSlug, projectPrefix, projectName                                         string
	port                                                                            int
}

// DefaultLocalProject はローカルの 1 人利用で⑥に Enter だけ押したときに作るプロジェクトの slug
// （チームのサーバの既定は作らない「-」。画面版の初回設定の既定値も同じ）。
const DefaultLocalProject = "main"

func defaultMode(pre Preset, env map[string]string) string {
	if pre.Mode != "" {
		return pre.Mode
	}
	if env != nil && !isTrue(env["LOOPTRACK_LOCAL_MODE"]) {
		return ModeTeam
	}
	return ModeLocal
}

func defaultsFor(dir, mode string, pre Preset, env map[string]string) defaults {
	d := defaults{store: StoreSQLite, port: DefaultPort, basePath: DefaultBasePath, login: DefaultAdminLogin}
	if mode == ModeTeam {
		d.store = StoreMySQL
		d.twoFactor = TwoFactorRequired
		d.projectSlug = "-"
	} else {
		d.twoFactor = TwoFactorOptional
		d.projectSlug = DefaultLocalProject
	}
	d.sqlitePath = filepath.Join(dir, "im.db")
	if mode == ModeTeam {
		d.sqlitePath = filepath.Join(dir, "data", "im.db") // --service systemd / none のとき（compose はコンテナの中のパス）
	}
	if env != nil {
		if dsn := env["LOOPTRACK_DSN"]; strings.HasPrefix(dsn, "sqlite:") {
			d.store = StoreSQLite
			if path := strings.TrimPrefix(dsn, "sqlite:"); mode == ModeLocal || path != sqliteContainerPath {
				d.sqlitePath = path
			}
		} else if dsn != "" {
			d.store, d.dsn = StoreMySQL, dsn
		}
		if l := env["LOOPTRACK_LISTEN"]; l != "" {
			if i := strings.LastIndex(l, ":"); i >= 0 {
				if n, err := strconv.Atoi(l[i+1:]); err == nil {
					d.port = n
				}
			}
		}
		if b := env["LOOPTRACK_BASE_PATH"]; b != "" {
			d.basePath = b
		}
		if mode == ModeTeam {
			d.publicURL = env["LOOPTRACK_PUBLIC_URL"]
		}
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&d.store, pre.Store)
	set(&d.dsn, pre.DSN)
	set(&d.migrateDSN, pre.MigrateDSN)
	set(&d.sqlitePath, pre.SQLitePath)
	set(&d.basePath, pre.BasePath)
	set(&d.publicURL, pre.PublicURL)
	set(&d.login, pre.AdminLogin)
	set(&d.name, pre.AdminName)
	set(&d.twoFactor, pre.TwoFactor)
	set(&d.projectSlug, pre.ProjectSlug)
	set(&d.projectPrefix, pre.ProjectPrefix)
	set(&d.projectName, pre.ProjectName)
	if pre.Port != 0 {
		d.port = pre.Port
	}
	return d
}

// planFromPreset は非対話（--yes）で答えを確定する。
func planFromPreset(dir string, pre Preset, env map[string]string) (*Plan, error) {
	mode, err := checkMode(defaultMode(pre, env))
	if err != nil {
		return nil, err
	}
	d := defaultsFor(dir, mode, pre, env)
	p := &Plan{Mode: mode}
	if p.Service, err = checkService(mode, pre.Service); err != nil {
		return nil, err
	}
	if p.Store, err = checkStore(d.store); err != nil {
		return nil, err
	}
	if p.Store == StoreMySQL {
		if d.dsn == "" {
			return nil, i18n.Errorf("setupwiz.err.need_dsn", "env", EnvDSN)
		}
		if _, err := checkDSN(d.dsn); err != nil {
			return nil, fmt.Errorf("%s: %w", EnvDSN, err)
		}
		if d.migrateDSN != "" {
			if _, err := checkDSN(d.migrateDSN); err != nil {
				return nil, fmt.Errorf("%s: %w", EnvMigrateDSN, err)
			}
		}
	}
	if p.Port, err = checkPort(strconv.Itoa(d.port)); err != nil {
		return nil, err
	}
	if p.BasePath, err = checkBasePath(d.basePath); err != nil {
		return nil, err
	}
	if mode == ModeTeam {
		if d.publicURL == "" {
			return nil, i18n.Errorf("setupwiz.err.need_public_url")
		}
		if p.PublicURL, err = checkPublicURL(d.publicURL); err != nil {
			return nil, err
		}
	}
	setStore(p, dir, d.sqlitePath, d.dsn, d.migrateDSN)
	if p.Admin.Login, err = checkLogin(d.login); err != nil {
		return nil, err
	}
	p.Admin.Name = d.name
	if p.Admin.Name == "" {
		p.Admin.Name = p.Admin.Login
	}
	if pre.AdminPassword == "" {
		return nil, i18n.Errorf("setupwiz.err.need_password", "env", EnvAdminPassword)
	}
	if p.Admin.PasswordHash, err = hashPassword(pre.AdminPassword); err != nil {
		return nil, err
	}
	// 二段階認証は既定値で決めない（必ず指定を求める。looptrack user add と同じ）
	if pre.TwoFactor == "" {
		return nil, i18n.Errorf("setupwiz.err.need_two_factor")
	}
	if p.Admin.TwoFactor, err = checkTwoFactor(pre.TwoFactor); err != nil {
		return nil, err
	}
	// 最初のプロジェクトは --project があるときだけ作る
	if pre.ProjectSlug == "" && (pre.ProjectPrefix != "" || pre.ProjectName != "") {
		return nil, i18n.Errorf("setupwiz.err.need_project_slug")
	}
	if p.Project, err = checkProject(pre.ProjectSlug, pre.ProjectPrefix, pre.ProjectName); err != nil {
		return nil, i18n.Wrapf(err, "setupwiz.err.first_project")
	}
	return p, nil
}

// setStore は保存先の DSN と、このコマンドが繋ぐ接続先・SQLite のファイルを決める。
func setStore(p *Plan, dir, sqlitePath, dsn, migrateDSN string) {
	switch {
	case p.Store == StoreMySQL:
		p.DSN, p.SetupDSN = dsn, dsn
		if migrateDSN != "" {
			p.SetupDSN = migrateDSN
		}
	case p.Mode == ModeLocal:
		abs, err := filepath.Abs(sqlitePath)
		if err == nil {
			sqlitePath = abs
		}
		p.SQLiteFile = sqlitePath
		p.DSN = "sqlite:" + sqlitePath
		p.SetupDSN = p.DSN
	case p.compose(): // チームのサーバ（compose）の SQLite: ホストの <dir>/data/im.db をコンテナの /data/im.db に置く
		p.SQLiteFile = filepath.Join(dir, "data", "im.db")
		p.DSN = "sqlite:" + sqliteContainerPath
		p.SetupDSN = "sqlite:" + p.SQLiteFile
	default: // チームのサーバ（systemd・none）の SQLite: --sqlite-path をそのまま .env に書く（コンテナの中のパスに変えない）
		if abs, err := filepath.Abs(sqlitePath); err == nil {
			sqlitePath = abs
		}
		p.SQLiteFile = sqlitePath
		p.DSN = "sqlite:" + sqlitePath
		p.SetupDSN = p.DSN
	}
	if p.Mode == ModeLocal {
		p.PublicURL = "http://127.0.0.1:" + strconv.Itoa(p.Port)
	}
}

// askPlan は対話で①〜⑥を順に聞き、確認して答えを確定する。
func askPlan(q *prompter, dir string, pre Preset, env map[string]string) (*Plan, error) {
	modeDef, err := checkMode(defaultMode(pre, env))
	if err != nil {
		modeDef = ModeLocal
	}
	q.section(i18n.T(q.lang, "setupwiz.section.mode"))
	mode, err := q.choose([]choice{
		{ModeLocal, i18n.T(q.lang, "setupwiz.opt.mode.local")},
		{ModeTeam, i18n.T(q.lang, "setupwiz.opt.mode.team")},
	}, modeDef)
	if err != nil {
		return nil, err
	}
	d := defaultsFor(dir, mode, pre, env)
	p := &Plan{Mode: mode}
	if p.Service, err = checkService(mode, pre.Service); err != nil {
		return nil, err // 起動の方法は引数だけで決める（対話では聞かない）
	}

	q.section(i18n.T(q.lang, "setupwiz.section.store"))
	if p.Store, err = q.choose([]choice{
		{StoreSQLite, i18n.T(q.lang, "setupwiz.opt.store.sqlite")},
		{StoreMySQL, i18n.T(q.lang, "setupwiz.opt.store.mysql")},
	}, d.store); err != nil {
		return nil, err
	}
	sqlitePath, dsn, migrateDSN := d.sqlitePath, d.dsn, d.migrateDSN
	switch {
	case p.Store == StoreMySQL:
		q.say(i18n.T(q.lang, "setupwiz.say.dsn_example"))
		q.say(i18n.T(q.lang, "setupwiz.say.create_db_first"))
		if dsn, err = q.askShown(i18n.T(q.lang, "setupwiz.ask.dsn"), dsn, maskDSN(dsn), checkDSN); err != nil {
			return nil, err
		}
		q.say(i18n.T(q.lang, "setupwiz.say.migrate_user"))
		mdef := migrateDSN
		if mdef == "" {
			mdef = dsn
		}
		if migrateDSN, err = q.askShown(i18n.T(q.lang, "setupwiz.ask.migrate_dsn"), mdef, maskDSN(mdef), checkDSN); err != nil {
			return nil, err
		}
		if migrateDSN == dsn {
			migrateDSN = ""
		}
	case mode == ModeLocal:
		if sqlitePath, err = q.ask(i18n.T(q.lang, "setupwiz.ask.sqlite_path"), sqlitePath, checkNonEmpty); err != nil {
			return nil, err
		}
	case p.compose():
		q.say(i18n.T(q.lang, "setupwiz.say.sqlite_in_compose", "path", filepath.Join(dir, "data", "im.db"), "container", sqliteContainerPath))
	default:
		q.say(i18n.T(q.lang, "setupwiz.say.sqlite_path", "path", sqlitePath))
	}

	q.section(i18n.T(q.lang, "setupwiz.section.listen"))
	if mode == ModeLocal {
		q.say(i18n.T(q.lang, "setupwiz.say.listen_local_only"))
	}
	ps, err := q.ask(i18n.T(q.lang, "setupwiz.ask.port"), strconv.Itoa(d.port), func(s string) (string, error) {
		n, err := checkPort(s)
		return strconv.Itoa(n), err
	})
	if err != nil {
		return nil, err
	}
	p.Port, _ = strconv.Atoi(ps)
	if p.BasePath, err = q.ask(i18n.T(q.lang, "setupwiz.ask.base_path"), d.basePath, checkBasePath); err != nil {
		return nil, err
	}
	if mode == ModeTeam {
		q.say(i18n.T(q.lang, "setupwiz.say.public_url_help"))
		if p.PublicURL, err = q.ask(i18n.T(q.lang, "setupwiz.ask.public_url"), d.publicURL, checkPublicURL); err != nil {
			return nil, err
		}
	}
	setStore(p, dir, sqlitePath, dsn, migrateDSN)

	q.section(i18n.T(q.lang, "setupwiz.section.admin"))
	if p.Admin.Login, err = q.ask(i18n.T(q.lang, "setupwiz.ask.login"), d.login, checkLogin); err != nil {
		return nil, err
	}
	nameDef := d.name
	if nameDef == "" {
		nameDef = p.Admin.Login
	}
	if p.Admin.Name, err = q.ask(i18n.T(q.lang, "setupwiz.ask.display_name"), nameDef, checkNonEmpty); err != nil {
		return nil, err
	}
	if p.Admin.PasswordHash, err = q.password(); err != nil {
		return nil, err
	}

	q.section(i18n.T(q.lang, "setupwiz.section.two_factor"))
	if mode == ModeLocal {
		q.say(i18n.T(q.lang, "setupwiz.say.two_factor_local"))
	}
	if p.Admin.TwoFactor, err = q.choose([]choice{
		{TwoFactorRequired, i18n.T(q.lang, "setupwiz.opt.two_factor.required")},
		{TwoFactorOptional, i18n.T(q.lang, "setupwiz.opt.two_factor.optional")},
	}, d.twoFactor); err != nil {
		return nil, err
	}

	q.section(i18n.T(q.lang, "setupwiz.section.project"))
	q.say(i18n.T(q.lang, "setupwiz.say.project_slug_help"))
	slug, err := q.ask(i18n.T(q.lang, "setupwiz.ask.project_slug"), d.projectSlug, checkProjectSlug)
	if err != nil {
		return nil, err
	}
	if slug != "-" {
		prefix, err := q.ask(i18n.T(q.lang, "setupwiz.ask.project_prefix", "example", defaultPrefix(slug)), firstNonEmpty(d.projectPrefix, defaultPrefix(slug)), checkProjectPrefix)
		if err != nil {
			return nil, err
		}
		name, err := q.ask(i18n.T(q.lang, "setupwiz.ask.project_name"), firstNonEmpty(d.projectName, slug), checkProjectName)
		if err != nil {
			return nil, err
		}
		if p.Project, err = checkProject(slug, prefix, name); err != nil {
			return nil, err
		}
	}

	q.section(i18n.T(q.lang, "setupwiz.section.confirm"))
	q.say(i18n.T(q.lang, "setupwiz.confirm.mode", "value", modeLabel(q.lang, p.Mode)))
	q.say(i18n.T(q.lang, "setupwiz.confirm.store", "value", storeLabel(q.lang, p.Store), "dsn", maskDSN(p.DSN)))
	if p.SetupDSN != p.DSN && p.Store == StoreMySQL {
		q.say(i18n.T(q.lang, "setupwiz.confirm.migrate_dsn", "value", maskDSN(p.SetupDSN)))
	}
	q.say(i18n.T(q.lang, "setupwiz.confirm.listen", "value", listenLabel(q.lang, p), "url", p.URL()))
	q.say(i18n.T(q.lang, "setupwiz.confirm.admin", "login", p.Admin.Login, "name", p.Admin.Name))
	q.say(i18n.T(q.lang, "setupwiz.confirm.two_factor", "value", twoFactorLabel(q.lang, p.Admin.TwoFactor)))
	q.say(i18n.T(q.lang, "setupwiz.confirm.project", "value", projectLabel(q.lang, p.Project)))
	files := filepath.Join(dir, EnvFile)
	if p.compose() {
		// compose.yaml の build が使う材料（Dockerfile・NOTICE）も同じディレクトリに置く
		files += "・" + filepath.Join(dir, ComposeFile) + "・" + DockerfileName + "・" + NoticeFile
	}
	if mode == ModeTeam {
		q.say(i18n.T(q.lang, "setupwiz.confirm.service", "value", serviceLabel(q.lang, p.Service)))
	}
	q.say(i18n.T(q.lang, "setupwiz.confirm.files", "value", files))
	ok, err := q.ask(i18n.T(q.lang, "setupwiz.ask.confirm"), "y", checkYesNo)
	if err != nil {
		return nil, err
	}
	if ok != "y" {
		return nil, ErrCanceled
	}
	return p, nil
}

func listenLabel(lang i18n.Lang, p *Plan) string {
	if p.Mode == ModeLocal {
		return "127.0.0.1:" + strconv.Itoa(p.Port)
	}
	return i18n.T(lang, "setupwiz.label.listen.port", "port", p.Port)
}

// maskDSN は DSN のパスワードを伏せる。
func maskDSN(dsn string) string {
	if strings.HasPrefix(dsn, "sqlite:") {
		return dsn
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.Passwd == "" {
		return dsn
	}
	cfg.Passwd = "****"
	return cfg.FormatDSN()
}

// ---- 入力の検査（対話と非対話で共通） ----

func checkMode(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ModeLocal:
		return ModeLocal, nil
	case ModeTeam:
		return ModeTeam, nil
	}
	return "", i18n.Errorf("setupwiz.err.mode", "value", s)
}

// checkService は起動の方法（--service）を検査する。チームのサーバの既定は compose。ローカルの 1 人利用は空（looptrack serve を案内）か none。
func checkService(mode, s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if mode == ModeLocal {
		if s == "" || s == ServiceNone {
			return s, nil
		}
		return "", i18n.Errorf("setupwiz.err.service_local_only", "value", s)
	}
	switch s {
	case "":
		return ServiceCompose, nil
	case ServiceCompose, ServiceSystemd, ServiceNone:
		return s, nil
	}
	return "", i18n.Errorf("setupwiz.err.service", "value", s)
}

func serviceLabel(lang i18n.Lang, s string) string {
	switch s {
	case ServiceSystemd:
		return i18n.T(lang, "setupwiz.label.service.systemd")
	case ServiceNone:
		return i18n.T(lang, "setupwiz.label.service.none")
	}
	return "docker compose"
}

func checkStore(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case StoreSQLite:
		return StoreSQLite, nil
	case StoreMySQL:
		return StoreMySQL, nil
	}
	return "", i18n.Errorf("setupwiz.err.store", "value", s)
}

func checkTwoFactor(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case TwoFactorRequired:
		return TwoFactorRequired, nil
	case TwoFactorOptional:
		return TwoFactorOptional, nil
	}
	return "", i18n.Errorf("setupwiz.err.two_factor", "value", s)
}

func checkPort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, i18n.Errorf("setupwiz.err.port", "value", s)
	}
	return n, nil
}

func checkBasePath(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	s = strings.TrimRight(s, "/")
	if !basePathRe.MatchString(s) {
		return "", i18n.Errorf("setupwiz.err.base_path", "value", s)
	}
	return s, nil
}

func checkPublicURL(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", i18n.Errorf("setupwiz.err.public_url_scheme", "value", s)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", i18n.Errorf("setupwiz.err.public_url_path", "value", s)
	}
	return s, checkEnvValue(s)
}

func checkDSN(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", i18n.Errorf("setupwiz.err.dsn_empty")
	}
	if strings.HasPrefix(s, "sqlite:") {
		return "", i18n.Errorf("setupwiz.err.sqlite_needs_store")
	}
	if _, err := mysql.ParseDSN(s); err != nil {
		return "", i18n.Errorf("setupwiz.err.dsn_unreadable", "reason", err)
	}
	return s, checkEnvValue(s)
}

func checkLogin(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !loginRe.MatchString(s) {
		return "", i18n.Errorf("setupwiz.err.login", "value", s)
	}
	return s, nil
}

func checkNonEmpty(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", i18n.Errorf("setupwiz.err.empty")
	}
	return s, checkEnvValue(s)
}

func checkYesNo(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "はい":
		return "y", nil
	case "n", "no", "いいえ":
		return "n", nil
	}
	return "", i18n.Errorf("setupwiz.err.yes_no")
}

// checkEnvValue は .env に単引用符で書けない値（単引用符・改行）を拒否する。
func checkEnvValue(s string) error {
	if strings.ContainsAny(s, "'\r\n") {
		return i18n.Errorf("setupwiz.err.env_value", "value", s)
	}
	return nil
}

// checkProjectSlug は⑥の slug（- は作らない）。規則は store.ValidateNewProject（looptrack project create と同じ）。
func checkProjectSlug(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "-" {
		return s, nil
	}
	return s, store.ValidateNewProject(store.Project{Slug: s, Prefix: "A", Width: DefaultProjectWidth, Name: "x"})
}

func checkProjectPrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	return s, store.ValidateNewProject(store.Project{Slug: "a", Prefix: s, Width: DefaultProjectWidth, Name: "x"})
}

func checkProjectName(s string) (string, error) {
	s = strings.TrimSpace(s)
	return s, store.ValidateNewProject(store.Project{Slug: "a", Prefix: "A", Width: DefaultProjectWidth, Name: s})
}

func projectLabel(lang i18n.Lang, p FirstProject) string {
	if p.Slug == "" {
		return i18n.T(lang, "setupwiz.label.project.none")
	}
	return i18n.T(lang, "setupwiz.label.project.summary",
		"slug", p.Slug, "id", p.Prefix+"-"+strings.Repeat("n", DefaultProjectWidth), "name", p.Name)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func hashPassword(pw string) (string, error) {
	if len([]rune(pw)) < auth.MinPasswordLen {
		return "", auth.ErrPasswordPolicy
	}
	return auth.HashPassword(pw)
}

// ---- 対話の入出力 ----

type choice struct{ key, label string }

type prompter struct {
	ctx    context.Context
	r      *bufio.Reader
	w      io.Writer
	secret func() (string, error)
	lang   i18n.Lang
}

func newPrompter(ctx context.Context, in io.Reader, w io.Writer, secret func() (string, error), lang i18n.Lang) *prompter {
	if lang == "" {
		lang = i18n.JA
	}
	return &prompter{ctx: ctx, r: bufio.NewReader(in), w: w, secret: secret, lang: lang}
}

func (q *prompter) section(title string) { fmt.Fprintf(q.w, "\n%s\n", title) }
func (q *prompter) say(s string)         { fmt.Fprintln(q.w, s) }

// wait は読み込みを ctx の取り消し（Ctrl-C）と競わせる。
func (q *prompter) wait(read func() (string, error)) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := read()
		ch <- res{s, err}
	}()
	select {
	case <-q.ctx.Done():
		return "", ErrInterrupted
	case r := <-ch:
		if q.ctx.Err() != nil {
			return "", ErrInterrupted
		}
		return r.s, r.err
	}
}

func (q *prompter) line() (string, error) {
	s, err := q.wait(func() (string, error) { return q.r.ReadString('\n') })
	if errors.Is(err, ErrInterrupted) {
		return "", err
	}
	if err != nil {
		if errors.Is(err, io.EOF) && s != "" {
			return strings.TrimRight(s, "\r\n"), nil
		}
		if errors.Is(err, io.EOF) {
			return "", ErrInputEnded
		}
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// ask は 1 項目を聞く。空なら既定値。検査に通らなければ理由を示して聞き直す。
func (q *prompter) ask(label, def string, check func(string) (string, error)) (string, error) {
	return q.askShown(label, def, def, check)
}

// askShown は既定値を shown で表示する ask（DSN のパスワードを画面に出さないため）。
func (q *prompter) askShown(label, def, shown string, check func(string) (string, error)) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(q.w, "  %s [%s]: ", label, shown)
		} else {
			fmt.Fprintf(q.w, "  %s: ", label)
		}
		s, err := q.line()
		if err != nil {
			fmt.Fprintln(q.w)
			return "", err
		}
		if strings.TrimSpace(s) == "" {
			s = def
		}
		if strings.TrimSpace(s) == "" {
			fmt.Fprintln(q.w, "  → "+i18n.T(q.lang, "setupwiz.err.empty"))
			continue
		}
		v, err := check(s)
		if err != nil {
			fmt.Fprintf(q.w, "  → %s\n", i18n.Text(q.lang, err))
			continue
		}
		return v, nil
	}
}

// choose は番号（かキー）で選ばせる。
func (q *prompter) choose(items []choice, def string) (string, error) {
	defNo := ""
	for i, c := range items {
		fmt.Fprintf(q.w, "  %d) %s\n", i+1, c.label)
		if c.key == def {
			defNo = strconv.Itoa(i + 1)
		}
	}
	return q.ask(i18n.T(q.lang, "setupwiz.ask.number"), defNo, func(s string) (string, error) {
		s = strings.ToLower(strings.TrimSpace(s))
		for i, c := range items {
			if s == strconv.Itoa(i+1) || s == c.key {
				return c.key, nil
			}
		}
		return "", i18n.Errorf("setupwiz.err.choice", "max", len(items))
	})
}

func (q *prompter) readSecret(label string) (string, error) {
	fmt.Fprintf(q.w, "  %s: ", label)
	if q.secret == nil {
		return q.line()
	}
	s, err := q.wait(q.secret)
	fmt.Fprintln(q.w)
	if err != nil && !errors.Is(err, ErrInterrupted) && errors.Is(err, io.EOF) {
		return "", ErrInputEnded
	}
	return s, err
}

// password は確認入力つきでパスワードを聞き、ハッシュを返す（端末ではエコーしない）。
func (q *prompter) password() (string, error) {
	for {
		a, err := q.readSecret(i18n.T(q.lang, "setupwiz.ask.password", "min", auth.MinPasswordLen))
		if err != nil {
			return "", err
		}
		if len([]rune(a)) < auth.MinPasswordLen {
			fmt.Fprintf(q.w, "  → %v\n", auth.ErrPasswordPolicy)
			continue
		}
		b, err := q.readSecret(i18n.T(q.lang, "setupwiz.ask.password_again"))
		if err != nil {
			return "", err
		}
		if a != b {
			fmt.Fprintln(q.w, "  → "+i18n.T(q.lang, "setupwiz.err.password_mismatch"))
			continue
		}
		return hashPassword(a)
	}
}
