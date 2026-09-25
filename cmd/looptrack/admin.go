package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// 管理コマンド（サーバ上で `docker compose exec looptrack looptrack …` として実行する想定）。

func userCmd(args []string) int {
	lang := cmdLang()
	if len(args) == 0 {
		return usageErr("user add|passwd|list|disable|enable|totp-reset")
	}
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("user add", flag.ExitOnError)
		name := fs.String("name", "", i18n.T(lang, "cmd.arg.display_name"))
		admin := fs.Bool("admin", false, i18n.T(lang, "cmd.arg.user.admin"))
		twoFactor := fs.String("two-factor", "", i18n.T(lang, "cmd.arg.user.two_factor"))
		login, err := oneArg(fs, args[1:], i18n.T(lang, "cmd.word.login"))
		if err != nil {
			return fail(err)
		}
		// パスワードを聞く前に、最初の利用者なら二段階認証の指定があるかを確かめる
		if err := checkFirstUserTwoFactor(ctx, db, lang, *twoFactor); err != nil {
			return fail(err)
		}
		hash, err := readNewPassword(lang)
		if err != nil {
			return fail(err)
		}
		role := "member"
		if *admin {
			role = "admin"
		}
		msg, err := addUser(ctx, db, lang, login, *name, hash, role, *twoFactor)
		if err != nil {
			return fail(err)
		}
		fmt.Println(msg)
	case "passwd":
		u, err := userArg(ctx, db, args[1:])
		if err != nil {
			return fail(err)
		}
		hash, err := readNewPassword(lang)
		if err != nil {
			return fail(err)
		}
		if err := store.SetPassword(ctx, db, u.ID, hash); err != nil {
			return fail(err)
		}
		fmt.Println(i18n.T(lang, "cmd.user.password_changed", "login", u.Login))
	case "list":
		users, err := store.ListUsers(ctx, db)
		if err != nil {
			return fail(err)
		}
		fmt.Printf("%-20s %-7s %-5s %-8s %s\n", "LOGIN", "ROLE", "TOTP", "STATE", "NAME")
		for _, u := range users {
			state := "active"
			if u.Disabled {
				state = "disabled"
			}
			fmt.Printf("%-20s %-7s %-5v %-8s %s\n", u.Login, u.Role, u.TOTPEnabled, state, u.DisplayName)
		}
	case "disable", "enable":
		u, err := userArg(ctx, db, args[1:])
		if err != nil {
			return fail(err)
		}
		if err := store.SetDisabled(ctx, db, u.ID, args[0] == "disable"); err != nil {
			return fail(err)
		}
		if args[0] == "disable" {
			fmt.Println(i18n.T(lang, "cmd.user.disabled", "login", u.Login))
		} else {
			fmt.Println(i18n.T(lang, "cmd.user.enabled", "login", u.Login))
		}
	case "totp-reset":
		u, err := userArg(ctx, db, args[1:])
		if err != nil {
			return fail(err)
		}
		if err := store.ResetTOTP(ctx, db, u.ID); err != nil {
			return fail(err)
		}
		fmt.Println(i18n.T(lang, "cmd.user.totp_reset", "login", u.Login))
	default:
		return usageErr("user add|passwd|list|disable|enable|totp-reset")
	}
	return 0
}

func memberCmd(args []string) int {
	const usage = "member set <slug> <login> [--role viewer|editor|admin] [--reassign <login|->] / member remove <slug> <login> [--reassign <login|->] / member list <slug>"
	lang := cmdLang()
	if len(args) < 2 {
		return usageErr(usage)
	}
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	p, err := store.ProjectBySlug(ctx, db, args[1])
	if err != nil {
		return fail(i18n.Wrapf(err, "cmd.err.project", "slug", args[1]))
	}
	// set / remove は画面と同じ service.SetMembership を通す（外す・viewer に下げる相手が担当の未クローズの
	// イシューを持つなら --reassign で代わりの担当者（login か - で未設定）が要る。付け替えは assign として記録する）
	change := func(u store.User, role, reassign string) int {
		svc := service.New(db, nil)
		res, err := svc.SetMembership(ctx, service.Actor{Via: "admin", Lang: lang}, p, u, role, reassign)
		var need *service.ReplacementError
		if errors.As(err, &need) {
			var logins []string
			for _, c := range need.Need.Candidates {
				logins = append(logins, c.Login)
			}
			fmt.Fprintln(os.Stderr, i18n.T(lang, "cmd.member.need_reassign", "message", i18n.Text(lang, need.Err), "candidates", strings.Join(append(logins, "-"), ", ")))
			return 1
		}
		if err != nil {
			return fail(err)
		}
		fmt.Println(res.Message)
		return 0
	}
	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("member set", flag.ExitOnError)
		role := fs.String("role", "editor", "viewer / editor / admin")
		reassign := fs.String("reassign", "", i18n.T(lang, "cmd.arg.member.reassign_set"))
		login, err := oneArg(fs, args[2:], i18n.T(lang, "cmd.word.login"))
		if err != nil {
			return fail(err)
		}
		u, err := store.UserByLogin(ctx, db, login)
		if err != nil {
			return fail(i18n.Wrapf(err, "cmd.err.user", "login", login))
		}
		return change(u, *role, *reassign)
	case "remove":
		fs := flag.NewFlagSet("member remove", flag.ExitOnError)
		reassign := fs.String("reassign", "", i18n.T(lang, "cmd.arg.member.reassign_remove"))
		login, err := oneArg(fs, args[2:], i18n.T(lang, "cmd.word.login"))
		if err != nil {
			return fail(err)
		}
		u, err := store.UserByLogin(ctx, db, login)
		if err != nil {
			return fail(i18n.Wrapf(err, "cmd.err.user", "login", login))
		}
		if r, err := store.MemberRole(ctx, db, p.ID, u.ID); err != nil || r == "" {
			return fail(notMemberError(err, u.Login, p.Slug))
		}
		return change(u, "", *reassign)
	case "list":
		ms, err := store.ListMembers(ctx, db, p.ID)
		if err != nil {
			return fail(err)
		}
		for _, m := range ms {
			fmt.Printf("%-20s %s\n", m.Login, m.Role)
		}
	default:
		return usageErr(usage)
	}
	return 0
}

func tokenCmd(args []string) int {
	lang := cmdLang()
	if len(args) < 2 {
		return usageErr(i18n.T(lang, "cmd.usage.token"))
	}
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("token create", flag.ExitOnError)
		name := fs.String("name", "", i18n.T(lang, "cmd.arg.token.name"))
		days := fs.Int("days", 90, i18n.T(lang, "cmd.arg.token.days"))
		login, err := oneArg(fs, args[1:], i18n.T(lang, "cmd.word.login"))
		if err != nil {
			return fail(err)
		}
		if *name == "" {
			return fail(i18n.Errorf("cmd.err.token_name_required"))
		}
		u, err := store.UserByLogin(ctx, db, login)
		if err != nil {
			return fail(i18n.Wrapf(err, "cmd.err.user", "login", login))
		}
		tok, prefix, err := auth.NewPAT()
		if err != nil {
			return fail(err)
		}
		now := time.Now()
		var exp *time.Time
		if *days > 0 {
			t := now.Add(time.Duration(*days) * 24 * time.Hour)
			exp = &t
		}
		id, err := store.CreateToken(ctx, db, u.ID, "pat", *name, prefix, auth.HashToken(tok), exp, now)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(os.Stderr, i18n.T(lang, "cmd.token.created", "id", int(id), "login", u.Login, "name", *name))
		fmt.Println(tok)
	case "list":
		u, err := store.UserByLogin(ctx, db, args[1])
		if err != nil {
			return fail(err)
		}
		ts, err := store.ListTokens(ctx, db, u.ID)
		if err != nil {
			return fail(err)
		}
		fmt.Printf("%-5s %-6s %-12s %-16s %-16s %-16s %s\n", "ID", "KIND", "PREFIX", "EXPIRES", "LAST_USED", "REVOKED", "NAME")
		for _, t := range ts {
			fmt.Printf("%-5d %-6s %-12s %-16s %-16s %-16s %s\n", t.ID, t.Kind, t.Prefix,
				fmtTime(t.ExpiresAt), fmtTime(t.LastUsedAt), fmtTime(t.RevokedAt), t.Name)
		}
	case "revoke":
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fail(i18n.Errorf("cmd.err.token_id", "value", args[1]))
		}
		if err := store.RevokeToken(ctx, db, id); err != nil {
			return fail(i18n.Wrapf(err, "cmd.err.token_revoke", "id", int(id)))
		}
		fmt.Println(i18n.T(lang, "cmd.token.revoked", "id", int(id)))
	default:
		return usageErr("token create|list|revoke")
	}
	return 0
}

func secretKeyCmd() int {
	k, err := auth.NewSecretKey()
	if err != nil {
		return fail(err)
	}
	fmt.Println(k)
	return 0
}

func usageErr(msg string) int {
	fmt.Fprintln(os.Stderr, i18n.T(cmdLang(), "cmd.prefix.usage", "msg", msg))
	return 2
}

func oneArg(fs *flag.FlagSet, args []string, what string) (string, error) {
	// フラグと位置引数の順序を問わない（`add alice --admin` も `add --admin alice` も受け付ける）
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 1 || pos[0] == "" {
		return "", i18n.Errorf("cmd.err.one_arg", "what", what)
	}
	return pos[0], nil
}

func fmtTime(t sql.NullTime) string {
	if !t.Valid {
		return "-"
	}
	return t.Time.In(time.Local).Format("2006-01-02 15:04")
}

func userArg(ctx context.Context, db store.Queryer, args []string) (store.User, error) {
	if len(args) != 1 {
		return store.User{}, i18n.Errorf("cmd.err.one_login")
	}
	u, err := store.UserByLogin(ctx, db, args[0])
	if err != nil {
		return u, i18n.Wrapf(err, "cmd.err.user", "login", args[0])
	}
	return u, nil
}

// readNewPassword は端末なら 2 回入力（表示しない）、パイプなら 1 行読む。
func readNewPassword(lang i18n.Lang) (string, error) {
	var pw string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, i18n.T(lang, "cmd.prompt.password"))
		a, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, i18n.T(lang, "cmd.prompt.password_again"))
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(a) != string(b) {
			return "", i18n.Errorf("cmd.err.password_mismatch")
		}
		pw = string(a)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", i18n.Errorf("cmd.err.password_stdin")
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	return auth.HashPassword(pw)
}

// projectCmd はプロジェクトの作成・一覧と、プロジェクト別ルールを扱う。
//
//	project create <slug> --prefix <PREFIX> --name <表示名> [--width 4] [--description …] [--order 100]
//	project list
//	project rename <slug> <表示名>    表示名だけを変える（slug・prefix・width は変えない）
//	project rules set <slug> <file>   ルールを検査して保存する（file が - なら標準入力。例: deploy/rules/example.json）
//	project rules show <slug>         保存されているルールを表示する
//	project rules clear <slug>        ルールを解除する
//	project guide set <slug> <file|-> [--source 名前]  運用文書（docs/projects/<slug>.md）を登録する（guide が返す）
//	project guide show <slug>         登録されている運用文書を表示する
//	project guide clear <slug>        運用文書の登録を消す
func projectCmd(args []string) int {
	lang := cmdLang()
	u := i18n.T(lang, "cmd.usage.project")
	if len(args) >= 1 && (args[0] == "create" || args[0] == "list") {
		return projectAdminCmd(args, lang, u)
	}
	if len(args) >= 1 && args[0] == "rename" {
		if len(args) != 3 {
			return usageErr(u)
		}
		ctx := context.Background()
		db, err := openDB()
		if err != nil {
			return fail(err)
		}
		defer db.Close()
		if err := projectRename(ctx, db, lang, os.Stdout, args[1], args[2]); err != nil {
			return fail(err)
		}
		return 0
	}
	if len(args) >= 1 && (args[0] == "archive" || args[0] == "unarchive") {
		if len(args) != 2 {
			return usageErr(u)
		}
		ctx := context.Background()
		db, err := openDB()
		if err != nil {
			return fail(err)
		}
		defer db.Close()
		if err := projectArchive(ctx, db, lang, os.Stdout, args[1], args[0] == "archive"); err != nil {
			return fail(err)
		}
		return 0
	}
	if len(args) >= 3 && args[0] == "guide" {
		return projectGuideCmd(args[1:], lang, u)
	}
	if len(args) < 3 || args[0] != "rules" {
		return usageErr(u)
	}
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	p, err := store.ProjectBySlug(ctx, db, args[2])
	if err != nil {
		return fail(i18n.Wrapf(err, "cmd.err.project", "slug", args[2]))
	}
	switch args[1] {
	case "set":
		if len(args) != 4 {
			return usageErr(u)
		}
		var raw []byte
		if args[3] == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(args[3])
		}
		if err != nil {
			return fail(err)
		}
		rules, err := domain.ParseRules(raw)
		if err != nil {
			return fail(err)
		}
		if rules == nil {
			return fail(i18n.Errorf("cmd.err.rules_empty", "slug", p.Slug))
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return fail(err)
		}
		if err := store.SetRules(ctx, db, p.ID, compact.Bytes()); err != nil {
			return fail(err)
		}
		var names []string
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Println(i18n.T(lang, "cmd.rules.set", "slug", p.Slug, "names", strings.Join(names, ", ")))
	case "show":
		if p.Rules == nil {
			fmt.Println(i18n.T(lang, "cmd.rules.none"))
			return 0
		}
		var out bytes.Buffer
		if err := json.Indent(&out, p.Rules, "", "  "); err != nil {
			return fail(err)
		}
		fmt.Println(out.String())
	case "clear":
		if err := store.SetRules(ctx, db, p.ID, nil); err != nil {
			return fail(err)
		}
		fmt.Println(i18n.T(lang, "cmd.rules.cleared", "slug", p.Slug))
	default:
		return usageErr(u)
	}
	return 0
}

// projectRename は project rename（表示名だけを変える。規則は service.RenameProject）。
func projectRename(ctx context.Context, db *sql.DB, lang i18n.Lang, out io.Writer, slug, name string) error {
	p, err := store.ProjectBySlug(ctx, db, slug)
	if err != nil {
		return i18n.Wrapf(err, "cmd.err.project", "slug", slug)
	}
	res, err := service.New(db, nil).RenameProject(ctx, p, name)
	if err != nil {
		return err
	}
	if !res.Changed {
		fmt.Fprintln(out, i18n.T(lang, "cmd.project.rename_unchanged", "slug", p.Slug, "new", res.NewName))
		return nil
	}
	fmt.Fprintln(out, i18n.T(lang, "cmd.project.renamed", "slug", p.Slug, "old", res.OldName, "new", res.NewName))
	return nil
}

// projectArchive は project archive / unarchive（論理削除と戻す。規則は service.SetProjectArchived）。
func projectArchive(ctx context.Context, db *sql.DB, lang i18n.Lang, out io.Writer, slug string, archived bool) error {
	p, err := store.ProjectBySlug(ctx, db, slug)
	if err != nil {
		return i18n.Wrapf(err, "cmd.err.project", "slug", slug)
	}
	res, err := service.New(db, nil).SetProjectArchived(ctx, p, archived)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, res.Message().In(lang))
	return nil
}

// projectList は project list。既定は使用中のプロジェクトだけ、archived ならアーカイブ済みだけを出す。
func projectList(ctx context.Context, db *sql.DB, out io.Writer, archived bool) error {
	ps, err := store.ListProjects(ctx, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%-16s %-10s %-5s %-6s %-5s %s\n", "SLUG", "PREFIX", "WIDTH", "ORDER", "LAST", "NAME")
	for _, p := range ps {
		if p.Archived != archived {
			continue
		}
		fmt.Fprintf(out, "%-16s %-10s %-5d %-6d %-5d %s\n", p.Slug, p.Prefix, p.Width, p.SortOrder, p.Counter, p.Name)
	}
	return nil
}

// projectAdminCmd は project create / list（ADD-PROJECT.md の手順で使う）。
func projectAdminCmd(args []string, lang i18n.Lang, u string) int {
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if args[0] == "list" {
		fs := flag.NewFlagSet("project list", flag.ExitOnError)
		archived := fs.Bool("archived", false, i18n.T(lang, "cmd.arg.project.archived"))
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return usageErr(u)
		}
		if err := projectList(ctx, db, os.Stdout, *archived); err != nil {
			return fail(err)
		}
		return 0
	}
	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	prefix := fs.String("prefix", "", i18n.T(lang, "cmd.arg.project.prefix"))
	width := fs.Int("width", store.DefaultProjectWidth, i18n.T(lang, "cmd.arg.project.width"))
	name := fs.String("name", "", i18n.T(lang, "cmd.arg.display_name"))
	desc := fs.String("description", "", i18n.T(lang, "cmd.arg.project.description"))
	order := fs.Int("order", 100, i18n.T(lang, "cmd.arg.project.order"))
	slug, err := oneArg(fs, args[1:], "slug")
	if err != nil {
		return fail(err)
	}
	p := store.Project{Slug: slug, Prefix: *prefix, Width: *width, Name: *name, Description: *desc, SortOrder: *order}
	if _, err := store.CreateProject(ctx, db, p); err != nil {
		return fail(err)
	}
	fmt.Println(i18n.T(lang, "cmd.project.created", "slug", p.Slug, "first", fmt.Sprintf("%s-%0*d", p.Prefix, p.Width, 1)))
	return 0
}

// projectGuideCmd は project guide set / show / clear（運用文書）。
func projectGuideCmd(args []string, lang i18n.Lang, u string) int {
	ctx := context.Background()
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	p, err := store.ProjectBySlug(ctx, db, args[1])
	if err != nil {
		return fail(i18n.Wrapf(err, "cmd.err.project", "slug", args[1]))
	}
	switch args[0] {
	case "set":
		if len(args) != 3 && !(len(args) == 5 && args[3] == "--source") {
			return usageErr(u)
		}
		var raw []byte
		source := ""
		if args[2] == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(args[2])
			source = args[2]
		}
		if err != nil {
			return fail(err)
		}
		if len(args) == 5 {
			source = args[4]
		}
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return fail(i18n.Errorf("cmd.err.guide_empty", "slug", p.Slug))
		}
		if !utf8.ValidString(text) {
			return fail(i18n.Errorf("cmd.err.guide_not_utf8"))
		}
		if len(text) > 1<<20 {
			return fail(i18n.Errorf("cmd.err.guide_too_large", "bytes", len(text)))
		}
		if err := store.SetProjectGuide(ctx, db, p.ID, text+"\n", source); err != nil {
			return fail(err)
		}
		fmt.Println(i18n.T(lang, "cmd.guide.set", "slug", p.Slug, "bytes", len(text)+1, "source", source))
	case "show":
		g, err := store.GetProjectGuide(ctx, db, p.ID)
		if errors.Is(err, store.ErrNotFound) {
			fmt.Println(i18n.T(lang, "cmd.guide.none"))
			return 0
		}
		if err != nil {
			return fail(err)
		}
		fmt.Printf("%s\n%s", i18n.T(lang, "cmd.guide.header", "source", g.Source, "updated", g.UpdatedAt.Format(time.RFC3339)), g.Content)
	case "clear":
		if err := store.SetProjectGuide(ctx, db, p.ID, "", ""); err != nil {
			return fail(err)
		}
		fmt.Println(i18n.T(lang, "cmd.guide.cleared", "slug", p.Slug))
	default:
		return usageErr(u)
	}
	return 0
}

// notMemberError は member remove の相手が参加していない（か、参加を調べられなかった）ときのエラー。
// DB のエラーがあればその文言を、無ければ store.ErrNotFound の文面を {reason} に入れる。
// errors.Join で両方を混ぜると、i18n.Text が ErrNotFound の文面だけを選び、DB のエラーの文言が落ちる。
func notMemberError(err error, login, slug string) error {
	if err == nil {
		err = store.ErrNotFound
	}
	return i18n.Wrapf(err, "cmd.err.not_member", "login", login, "slug", slug)
}
