package loop

// session-start-worktrees: 作業ツリー（worktree）の残骸をセッションの冒頭に知らせる。
//
// 道具（looptrack worktree list / prune）があっても、打つきっかけが無ければ残骸は溜まり続ける
// （本リポジトリでは、誰も一覧を打たないまま取り込み済みの作業ツリーが 60 以上たまった）。きっかけを作るのがこの hook の役目。
//
//   - **何も消さない。**知らせるだけ（prune は呼ばない）。消すのは利用者か、一覧を見た AI が明示的に打ったときだけ。
//   - 片付けられるものも、中身が失われかけているものも無ければ**何も出さない**（毎回出ると読まれなくなる）。
//   - 消せるもの（prune の対象）があるときは、最初の返答で利用者に消すかを問うよう AI に頼む。
//     件数を知らせるだけでは、AI は利用者に聞かないまま作業に入り、ブランチが溜まり続けた。
//     SessionStart の hook は利用者に直接は問えないので、AI への文を足すことしかできない。
//   - git が無い・git のリポジトリでない・git が失敗する・時間がかかるときは、黙って何もしない（fail-open）。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/worktree"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// SessionStartWorktrees は SessionStart で作業ツリーの残骸を知らせる。
func SessionStartWorktrees(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	r := root(ev, e)
	if r == "" {
		return hookio.Result{}, nil
	}
	switch strings.ToLower(strings.TrimSpace(e.env("LOOPTRACK_LOOP_WORKTREE_NOTICE"))) {
	case "0", "off", "false", "no":
		return hookio.Result{}, nil // 利用者が切った
	}
	minAge := worktree.DefaultMinAge
	if v := e.env("LOOPTRACK_LOOP_WORKTREE_MIN_AGE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			minAge = d
		}
	}
	rep, err := worktree.List(worktree.Options{
		Dir:    r,
		MinAge: minAge,
		Git: func(dir string, args ...string) (string, error) {
			if dir == "" {
				dir = r
			}
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			out, code, err := e.run(c, dir, nil, "git", args...)
			if err != nil {
				return string(out), err
			}
			if code != 0 {
				return string(out), errors.New(i18n.T(e.lang(), "loop.worktrees.err.git_exit", "args", strings.Join(args, " "), "code", code))
			}
			return string(out), nil
		},
	})
	if err != nil {
		return hookio.Result{}, nil // git が無い・リポジトリでない。黙る
	}
	note := worktreeNote(e.lang(), rep, minAge, askTool(ev))
	if note == "" {
		return hookio.Result{}, nil
	}
	return hookio.Result{Context: note}, nil
}

// worktreeNote は一覧から出す文面を作る。知らせることが無ければ空を返す。
//
// 出すのは 2 つだけ: 片付けられるものが何件か、中身が失われかけているものが何件か。
// 失われかけているもの（日をまたいだ未コミットの変更）は、消さずに残すだけだと忘れられるので、名前まで出す。
//
// 消せるものが 1 件以上あるときだけ、利用者に消すかを問う指示を足す。tool は質問のツールの名前で、空なら名指ししない。
func worktreeNote(lang i18n.Lang, rep *worktree.Report, minAge time.Duration, tool string) string {
	var can, losing []worktree.Entry
	for _, e := range rep.Entries {
		if e.Main {
			continue
		}
		if e.Losing() {
			losing = append(losing, e)
		}
		if ok, _ := e.Removable(lang, minAge); ok {
			can = append(can, e)
		}
	}
	// prune と同じ判定で数える（追い先の残るブランチは prune が消さないので、数えると件数が合わない）
	merged := 0
	for _, b := range rep.Branches {
		if ok, _ := b.Removable(lang); ok {
			merged++
		}
	}
	if len(can) == 0 && len(losing) == 0 && merged == 0 {
		return ""
	}

	var head []string
	if len(can) > 0 {
		head = append(head, i18n.T(lang, "loop.worktrees.head.removable", "n", len(can)))
	}
	if merged > 0 {
		head = append(head, i18n.T(lang, "loop.worktrees.head.branches", "n", merged))
	}
	if len(losing) > 0 {
		head = append(head, i18n.T(lang, "loop.worktrees.head.losing", "n", len(losing)))
	}
	sep := "・"
	if lang == i18n.EN {
		sep = ", "
	}
	b := &strings.Builder{}
	b.WriteString(i18n.T(lang, "loop.worktrees.head", "parts", strings.Join(head, sep)) + "\n")
	for i, e := range losing {
		if i == 3 {
			b.WriteString(i18n.T(lang, "loop.worktrees.more", "n", len(losing)-3) + "\n")
			break
		}
		b.WriteString(i18n.T(lang, "loop.worktrees.losing_item",
			"name", shortName(e), "n", e.Changes, "age", roughAge(lang, e.Age)) + "\n")
	}
	if len(losing) > 0 {
		b.WriteString(i18n.T(lang, "loop.worktrees.losing_advice") + "\n")
	}
	b.WriteString(i18n.T(lang, "loop.worktrees.how") + "\n")
	b.WriteString(i18n.T(lang, "loop.worktrees.warn") + "\n")
	if len(can) > 0 || merged > 0 {
		if tool != "" {
			b.WriteString(i18n.T(lang, "loop.worktrees.ask.tool", "tool", tool) + "\n")
		} else {
			b.WriteString(i18n.T(lang, "loop.worktrees.ask.plain") + "\n")
		}
		if len(losing) > 0 {
			b.WriteString(i18n.T(lang, "loop.worktrees.ask.losing") + "\n")
		}
		b.WriteString(i18n.T(lang, "loop.worktrees.ask.then") + "\n")
	}
	return b.String()
}

// askTool は利用者に問うときの質問のツールの名前。名前を確かめてある Claude Code の配線だけが返す
// （Codex・Copilot は名前を確かめていないので、名指しせずに質問の形で聞かせる）。
func askTool(ev hookio.Event) string {
	if ev.Agent == hookio.ClaudeCode && !ev.Foreign {
		return "AskUserQuestion"
	}
	return ""
}

// shortName は作業ツリーを短く指す言葉（ブランチ名。無ければパスの末尾）。
func shortName(e worktree.Entry) string {
	if e.Branch != "" {
		return e.Branch
	}
	if i := strings.LastIndexAny(e.Path, `/\`); i >= 0 {
		return e.Path[i+1:]
	}
	return e.Path
}

// roughAge は「3 日」「5 時間」のような長さ（文面に埋める）。
func roughAge(lang i18n.Lang, d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return i18n.T(lang, "loop.worktrees.age.days", "n", int(d.Hours()/24))
	case d >= time.Hour:
		return i18n.T(lang, "loop.worktrees.age.hours", "n", int(d.Hours()))
	default:
		return i18n.T(lang, "loop.worktrees.age.recent")
	}
}
