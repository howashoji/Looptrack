package setupwiz

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 初回設定の DB の処理。ターミナル版（looptrack setup）と画面版（ローカルモードの初回設定の画面）の
// 両方がこの Provision を呼ぶ（同じ答えから同じ設定になるように）。

// FirstProject は最初のプロジェクト（⑥）。Slug が空なら作らない。
type FirstProject struct {
	Slug   string
	Prefix string
	Name   string
}

// DefaultProjectWidth は最初のプロジェクトの番号の桁数（looptrack project create の既定と同じ。値は store に置く）。
const DefaultProjectWidth = store.DefaultProjectWidth

// ProvisionOptions は Provision の呼び出し元ごとの違い（記録の経路と、既存の利用者の扱い）。
type ProvisionOptions struct {
	Via    string // 二段階認証の変更記録の経路（command / web）
	Source string // 同じく備考に入れる出所（例 looptrack setup）。利用者 0 人のときは「最初の利用者 <login> の作成時に指定（<Source>）」
	Note   string // 利用者がすでにいるとき（--force で作り直す）の備考
	IP     string
	// FirstRun は画面版の初回設定。有効な管理者がすでにいれば ErrAlreadySetUp、同じログイン名の利用者がいれば
	// （無効化された管理者なども）ErrLoginTaken で止める（そのままでは初回設定の画面が出続けるため）
	FirstRun bool
	// Lang は結果のメッセージと、二段階認証の変更記録の備考（setting_changes.note）の言語。ゼロ値は日本語。
	// 備考は書き込んだときの文面のまま DB に残るので、設定した人の言語を渡す（後から表示の言語を変えても書き換わらない）。
	Lang i18n.Lang
}

var (
	// ErrAlreadySetUp は有効な管理者がすでにいること（画面版の初回設定を受け付けない）。
	ErrAlreadySetUp = i18n.Errorf("provision.err.already_set_up")
	// ErrLoginTaken は同じログイン名の利用者がすでにいること（画面版の初回設定）。
	ErrLoginTaken = i18n.Errorf("provision.err.login_taken")
)

// Provision は最初の管理者・二段階認証の設定・最初のプロジェクト（と管理者の admin の参加）を 1 つのトランザクションで作る。
// 失敗したら何も残さない。表示するメッセージを返す。
//
// 利用者が 0 人なら管理者を作る。利用者がいる（ターミナル版の --force）なら、同じログイン名がいればそのまま残し
// （パスワードと役割は変えない）、いなければ管理者として足す。プロジェクトは slug がすでにあればそれを使い、
// 管理者の参加の行が無ければ admin で足す（既存の行は変えない）。
func Provision(ctx context.Context, db *sql.DB, a Admin, p FirstProject, o ProvisionOptions) (string, error) {
	if !store.ValidTwoFactor(a.TwoFactor) {
		return "", i18n.Errorf("provision.err.two_factor", "value", a.TwoFactor)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck

	if o.FirstRun {
		n, err := store.CountActiveAdmins(ctx, tx)
		if err != nil {
			return "", err
		}
		if n > 0 {
			return "", ErrAlreadySetUp
		}
	}
	users, err := store.CountUsers(ctx, tx)
	if err != nil {
		return "", err
	}
	var msg string
	var adminID int64
	switch u, err := store.UserByLogin(ctx, tx, a.Login); {
	case err == nil:
		if o.FirstRun {
			return "", ErrLoginTaken
		}
		adminID = u.ID
		msg = i18n.T(o.Lang, "provision.user_exists", "login", u.Login, "role", u.Role)
	case errors.Is(err, store.ErrNotFound):
		if adminID, err = store.CreateUser(ctx, tx, a.Login, a.Name, a.PasswordHash, "admin"); err != nil {
			return "", err
		}
		msg = i18n.T(o.Lang, "provision.created", "login", a.Login)
	default:
		return "", err
	}

	note := o.Note
	if users == 0 {
		note = i18n.T(o.Lang, "provision.note.first_user", "login", a.Login, "source", o.Source)
	}
	ch := store.SettingChange{Via: o.Via, Note: note, IP: o.IP}
	if o.Via == "web" {
		ch.ActorUserID = sql.NullInt64{Int64: adminID, Valid: true}
	}
	res, err := store.SetTwoFactorPolicyTx(ctx, tx, a.TwoFactor, ch)
	if err != nil {
		return "", err
	}
	switch {
	case users == 0:
		msg += i18n.T(o.Lang, "provision.two_factor.saved", "value", store.TwoFactorLabel(o.Lang, a.TwoFactor))
	case res.Changed:
		msg += i18n.T(o.Lang, "provision.two_factor.changed", "from", store.TwoFactorLabel(o.Lang, res.Old), "to", store.TwoFactorLabel(o.Lang, a.TwoFactor))
	default:
		msg += i18n.T(o.Lang, "provision.two_factor.unchanged", "value", store.TwoFactorLabel(o.Lang, a.TwoFactor))
	}

	if p.Slug != "" {
		pr, err := store.ProjectBySlug(ctx, tx, p.Slug)
		switch {
		case err == nil:
			msg += i18n.T(o.Lang, "provision.project.exists", "slug", pr.Slug)
		case errors.Is(err, store.ErrNotFound):
			pr = store.Project{Slug: p.Slug, Prefix: p.Prefix, Width: DefaultProjectWidth, Name: p.Name}
			if pr.ID, err = store.CreateProject(ctx, tx, pr); err != nil {
				return "", i18n.Wrapf(err, "provision.err.project", "slug", p.Slug)
			}
			msg += i18n.T(o.Lang, "provision.project.created", "slug", pr.Slug, "id", pr.Prefix+"-"+strings.Repeat("n", pr.Width))
		default:
			return "", err
		}
		added, err := store.AddMemberIfAbsent(ctx, tx, pr.ID, adminID, "admin")
		if err != nil {
			return "", err
		}
		if added {
			msg += i18n.T(o.Lang, "provision.project.joined", "login", a.Login)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return msg, nil
}

// checkProject は最初のプロジェクトの答えを検査して揃える（slug が空か - なら作らない。接頭辞・名前の既定は slug から作る）。
func checkProject(slug, prefix, name string) (FirstProject, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || slug == "-" {
		return FirstProject{}, nil
	}
	p := FirstProject{Slug: slug, Prefix: strings.TrimSpace(prefix), Name: strings.TrimSpace(name)}
	if p.Prefix == "" {
		p.Prefix = defaultPrefix(slug)
	}
	if p.Name == "" {
		p.Name = slug
	}
	if err := store.ValidateNewProject(store.Project{Slug: p.Slug, Prefix: p.Prefix, Width: DefaultProjectWidth, Name: p.Name}); err != nil {
		return FirstProject{}, err
	}
	return p, nil
}

// defaultPrefix は slug から接頭辞の既定値を作る（英大文字にするだけ。例 my-app → MY-APP）。
func defaultPrefix(slug string) string { return strings.ToUpper(strings.TrimSpace(slug)) }
