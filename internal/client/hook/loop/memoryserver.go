package loop

// 引き継ぎの置き場 server（LOOPTRACK_LOOP_HANDOFF_BACKEND=server）: サーバのプロジェクト記憶（DESIGN.md §9-9）を
// 引き継ぎに使う。端末と利用者をまたいで同じ引き継ぎを読み書きできる。
//
// 書き手の判定: file の置き場は本文の印で「このセッションが完了より後に書いたか」を見る。server は版の履歴に書き手のセッションと
// 時刻が残るので、本文に印を入れない。判定は handoff/main の版のうち、このセッションが書いた版の時刻を完了のマーカーと比べる。
// セッション ID が分からないときは、最新の版の時刻で見る（file の置き場が印なしで更新時刻を見るのと同じ）。
//
// サーバに届かないときは止めない（backend issue と同じ fail-open）。注意を 1 つ出して通し、マーカーは畳まない。

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	clientenv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
)

// backendServer は置き場の名前。
const backendServer = "server"

// memoryHookTimeout は hook からの 1 要求の待ち時間。session-start-memories の持ち時間（4 秒）の中で一覧と本文の 2 要求が収まる長さにする。
// 退避の送信は注入を読んだ後に、持ち時間の内側の context で打ち切りながら行う。
const memoryHookTimeout = 1500 * time.Millisecond

// memoryHookMargin は持ち時間の締切の手前に残す時間（注入を書き出す分）。memoryHookBudget は締切の無い ctx の持ち時間。
const (
	memoryHookMargin = 300 * time.Millisecond
	memoryHookBudget = 3500 * time.Millisecond
)

// handoffBackend は引き継ぎの置き場（未設定なら file）。
func handoffBackend(e *Env) string {
	if b := trimSpace(e.env("LOOPTRACK_LOOP_HANDOFF_BACKEND")); b != "" {
		return b
	}
	return "file"
}

func hookMemoryAccess(ev hookio.Event, e *Env, r string) (*cli.MemoryAccess, error) {
	vars := session.WithHookSession(clientenv.FromFunc(e.env), rawSessionID(ev))
	return cli.NewMemoryAccess(vars, r, e.lang(), memoryHookTimeout)
}

// memoryWhy は注意に添える理由（1 行目だけ）。
func memoryWhy(lang i18n.Lang, err error) string {
	first, _, _ := strings.Cut(cli.Message(lang, err), "\n")
	return first
}

// memoryTime は応答の時刻を手元の時刻の表示にする（読めなければそのまま）。
func memoryTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// handoffServerWrote は handoff の成功の行（書いた版を示す）。失敗と退避の文には当たらない。
//
// 退避の行は 書いた記録にしない（送れた後に残す。memoryspool.go）。
var handoffServerWrote = regexp.MustCompile(`handoff/main\s*[（(](?:版|version)`)

// writtenDir は「このセッションが引き継ぎを書いた」手元の記録の置き場。更新時刻が手元の時計の書いた時刻になる。
// 鮮度の状態と同じ .looptrack-freshness の下なので、kit が足す .gitignore の行で版管理の外になる。
func writtenDir(state string) string {
	return filepath.Join(state, ".looptrack-freshness", "handoff-written")
}

// writtenKeep を過ぎた手元の記録は消す（セッションごとに 1 つ増えるので、消さないと溜まり続ける）。
const writtenKeep = 7 * 24 * time.Hour

// sharedWritten はセッション ID が分からない書き込みの記録の名前。
const sharedWritten = "_shared"

// markHandoffWritten は手元の記録を残す（書けなくても止めない。サーバの時刻の判定に落ちるだけ）。
func markHandoffWritten(state, id string) { markHandoffWrittenAt(state, id, time.Now()) }

// markHandoffWrittenAt は at を書いた時刻として残す。退避から送った本文の時刻は退避した時刻で、送った時刻ではない
// （完了より前に書いた本文が、完了の後に送れただけで通らないため）。既にある記録より古ければ戻さない。
func markHandoffWrittenAt(state, id string, at time.Time) {
	id = nonIDChar.ReplaceAllString(id, "")
	if id == "" {
		id = sharedWritten
	}
	p := filepath.Join(writtenDir(state), id)
	if st, err := os.Stat(p); err == nil && !st.ModTime().Before(at) {
		return
	}
	if os.MkdirAll(writtenDir(state), 0o777) == nil && os.WriteFile(p, []byte(at.Format(time.RFC3339)+"\n"), 0o666) == nil {
		_ = os.Chtimes(p, at, at)
	}
	ents, _ := os.ReadDir(writtenDir(state))
	for _, ent := range ents {
		if st, err := ent.Info(); err == nil && time.Since(st.ModTime()) > writtenKeep {
			_ = os.Remove(filepath.Join(writtenDir(state), ent.Name()))
		}
	}
}

// ownSessions はこのセッションを指す ID（hook の入力の ID と、hook のプロセスの環境の ID。器の窓では違う値になる）。
func ownSessions(ev hookio.Event, e *Env) []string {
	var out []string
	for _, s := range []string{digestSessionID(ev, e), session.Detect(clientenv.FromFunc(e.env)).Session} {
		if s = trimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// newestWritten は手元の記録のうち、このセッションのものの最新の時刻を返す。
func newestWritten(state string, own []string) (time.Time, bool) {
	names := []string{sharedWritten}
	if len(own) > 0 {
		names = names[:0]
		for _, s := range own {
			names = append(names, nonIDChar.ReplaceAllString(s, ""))
		}
	}
	var best time.Time
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(writtenDir(state), n)); err == nil && st.ModTime().After(best) {
			best = st.ModTime()
		}
	}
	return best, !best.IsZero()
}

// checkServerHandoff は置き場 server の鮮度の判定。pendMT は完了のマーカーの時刻。
//
// 順は 退避 → 手元の記録 → サーバ。手元の記録は手元の時計で書くので、サーバとの時計のずれが効かない。
// 手元の記録で新しいと言えなければ、サーバの版の時刻でも見る（MCP の write_memory や looptrack memory append で書いたとき）。どちらかで新しければ通す。
// 書き手は digest の判定と同じく service.ComparableSessions で比べる。比べられない版は、書き手の利用者が自分のときだけ、注意を出して通す
// （セッションを特定できていないのでマーカーは畳まない）。ほかの利用者の版では通さない。
//
// 残った退避で通すのは持ち主のセッションだけ。ほかのセッションの退避は注意に添えるだけで、判定は続ける。
func checkServerHandoff(ctx context.Context, ev hookio.Event, e *Env, r, state string, pendMT time.Time) (fresh bool, where, notice string, keep bool) {
	lang := e.lang()
	pass := func(n string) (bool, string, string, bool) { return true, "", n, true }
	unreachable := func(err error) (bool, string, string, bool) {
		return pass(i18n.T(lang, "loop.handoff.server.notice.unreachable", "why", memoryWhy(lang, err)))
	}
	m, err := hookMemoryAccess(ev, e, r)
	if err != nil {
		return unreachable(err)
	}
	own := ownSessions(ev, e)
	flushCtx, cancel := context.WithTimeout(ctx, spoolFlushBudget)
	res := flushHandoffSpool(flushCtx, e, m, state)
	cancel()
	spoolNotice := ""
	if res.left > 0 {
		if res.ownedBy(own) {
			return pass(res.notice(lang))
		}
		spoolNotice = res.notice(lang)
	}
	// 版の時刻と記録は秒に丸めて比べる（同じ秒の書き込みを落とさない。freshByWriterMark と同じ扱い）
	pendSec := pendMT.Truncate(time.Second)
	if local, ok := newestWritten(state, own); ok && !local.Before(pendSec) {
		return true, "", spoolNotice, false
	}
	list, err := m.List()
	switch {
	case cli.IsMemoryMissing(err):
		return pass(i18n.T(lang, "loop.handoff.server.notice.no_member"))
	case err != nil:
		return unreachable(err)
	case !list.CanWrite:
		return pass(i18n.T(lang, "loop.handoff.server.notice.read_only"))
	}
	doc, err := m.Handoff()
	if cli.IsMemoryMissing(err) {
		return false, i18n.T(lang, "loop.handoff.where.server_missing"), spoolNotice, false
	}
	if err != nil {
		return unreachable(err)
	}
	unknown := false
	mine := func(v cli.MemoryVersionInfo) bool { return list.User != "" && v.User == list.User }
	for _, v := range doc.Versions {
		at, err := v.WrittenTime() // 退避から送った版は退避した時刻で見る（どの作業ツリーが送っても同じ判定になる）
		if err != nil || at.Before(pendSec) {
			continue
		}
		switch {
		case len(own) == 0 && mine(v), slices.Contains(own, v.SessionID):
			return true, "", spoolNotice, false
		case len(own) > 0 && !service.ComparableSessions(own[0], v.SessionID) && mine(v):
			unknown = true
		}
	}
	if unknown {
		return pass(i18n.T(lang, "loop.handoff.server.notice.unknown_writer"))
	}
	version, updated, user := doc.Memory.Version, memoryTime(doc.Memory.UpdatedAt), doc.Memory.UpdatedBy
	if len(own) == 0 {
		return false, i18n.T(lang, "loop.handoff.where.server_stale_any", "version", version, "time", updated, "user", user), spoolNotice, false
	}
	return false, i18n.T(lang, "loop.handoff.where.server_stale_clock", "version", version, "time", updated, "user", user), spoolNotice, false
}

// sessionStartServerMemories はサーバの引き継ぎと、決定・注意の要約の一覧を文脈に入れる。
// 注入を先に確保し、退避は残りの時間で足りるときだけ送る（送れないものは次の機会に回る）。
func sessionStartServerMemories(ctx context.Context, ev hookio.Event, e *Env) hookio.Result {
	lang := e.lang()
	spoolDirOnly := func(m *cli.MemoryAccess) spoolResult {
		return pendingSpool(lang, m.SpoolDir(), i18n.T(lang, "loop.handoff.server.spool.why.unreachable"))
	}
	unreachable := func(m *cli.MemoryAccess, err error) hookio.Result {
		text := i18n.T(lang, "loop.memories.server_unreachable", "why", memoryWhy(lang, err))
		if m != nil {
			if p := spoolDirOnly(m); p.left > 0 {
				text += "\n" + p.notice(lang)
			}
		}
		return hookio.Result{Context: text}
	}
	m, err := hookMemoryAccess(ev, e, root(ev, e))
	if err != nil {
		return unreachable(nil, err)
	}
	start := time.Now()
	v, _, err := serverMemoriesView(lang, m)
	if err != nil {
		return unreachable(m, err)
	}
	// 退避の送信は注入の締切の内側の context で打ち切る。読み直し（送った本文を注入に入れる）の分は、かかった時間から取っておく
	deadline := start.Add(memoryHookBudget)
	if d, ok := ctx.Deadline(); ok {
		deadline = d.Add(-memoryHookMargin)
	}
	reread := time.Since(start)
	spool := pendingSpool(lang, m.SpoolDir(), i18n.T(lang, "loop.handoff.server.spool.why.no_time"))
	if spool.left > 0 {
		flushCtx, cancel := context.WithDeadline(ctx, deadline.Add(-reread))
		spool = flushHandoffSpool(flushCtx, e, m, stateDir(ev, e))
		cancel()
		rereadCtx, cancel := context.WithDeadline(ctx, deadline)
		m.WithContext(rereadCtx)
		if spool.sent > 0 {
			if next, _, err := serverMemoriesView(lang, m); err == nil {
				v = next // 送った本文をこのセッションの注入にも入れる
			}
		}
		m.WithContext(nil)
		cancel()
	}
	res := renderMemories(e, lang, v)
	if spool.left > 0 {
		res.Context += "\n" + spool.notice(lang)
	}
	return res
}

// serverMemoriesView は注入の材料を読む。戻り値の requests は使った要求の数。
func serverMemoriesView(lang i18n.Lang, m *cli.MemoryAccess) (memoriesView, int, error) {
	list, err := m.List()
	if err != nil {
		return memoriesView{}, 1, err
	}
	v := memoriesView{header: i18n.T(lang, "loop.memories.server_header"), othersHeader: i18n.T(lang, "loop.memories.server_others_header"),
		footer:  i18n.T(lang, "loop.memories.server_footer"),
		trimmed: func(total string) string { return i18n.T(lang, "loop.memories.server_trimmed", "total", total) }}
	found := false
	for _, it := range list.Items {
		if it.Kind == "handoff" && it.Name == "main" && it.Scope == "project" {
			found = true
			continue
		}
		name := it.Kind + "/" + it.Name
		if it.Scope != "project" {
			name += " (" + it.Scope + ")"
		}
		v.others = append(v.others, fmt.Sprintf("  - %s — %s", name, it.Summary))
	}
	if !found {
		v.after = []string{i18n.T(lang, "loop.memories.server_missing")}
		return v, 1, nil
	}
	doc, err := m.Handoff()
	if err != nil {
		return v, 2, err
	}
	v.body, v.hasBody = trimSpaceRight(doc.Memory.Content), true
	v.after = []string{"", i18n.T(lang, "loop.memories.server_updated", "time", memoryTime(doc.Memory.UpdatedAt),
		"user", doc.Memory.UpdatedBy, "version", doc.Memory.Version)}
	return v, 2, nil
}

// handoffServerAccess は looptrack handoff の口（--session は明示の LOOPTRACK_SESSION_ID と同じに扱う）。
func handoffServerAccess(e *Env, o handoffOpts) (*cli.MemoryAccess, error) {
	vars := clientenv.FromFunc(func(k string) string { return e.env(k) })
	if s := trimSpace(o.session); s != "" {
		vars = clientenv.Overlay(vars, map[string]string{clientenv.Name(clientenv.SessionID): s})
	}
	return cli.NewMemoryAccess(vars, handoffCLIEvent(e).ProjectDir, e.lang(), 0)
}

// flushServerSpoolOnStop は完了の印が無い Stop でも退避を送る（印があるときは checkServerHandoff が送る）。
// 退避が無ければサーバに問い合わせない。送れなければ次の機会に回し、止めも知らせもしない（SessionStart が知らせる）。
func flushServerSpoolOnStop(ctx context.Context, ev hookio.Event, e *Env, r, state string) {
	m, err := hookMemoryAccess(ev, e, r)
	if err != nil || pendingSpool(e.lang(), m.SpoolDir(), "").left == 0 {
		return
	}
	flushCtx, cancel := context.WithTimeout(ctx, spoolFlushBudget)
	defer cancel()
	flushHandoffSpool(flushCtx, e, m, state)
}

// handoffServerWritten は書けたことを手元の記録に残す（退避では呼ばない。送れた後に memoryspool.go が残す）。
func handoffServerWritten(e *Env, o handoffOpts) {
	markHandoffWritten(stateDir(handoffCLIEvent(e), e), handoffSessionID(e, o.session))
}

// handoffServerAppend は節をサーバの引き継ぎの末尾に足す（版を問わないので、同時に書いても両方が残る）。
// サーバに届かないときは本文を退避して成功で終わる。
func handoffServerAppend(e *Env, lang i18n.Lang, o handoffOpts, body string, stdout, stderr io.Writer) int {
	if o.file != "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.server_file"))
		return handoffExitUsage
	}
	body = trimSpace(body)
	if body == "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.empty_body"))
		return handoffExitUsage
	}
	sid := handoffSessionID(e, o.session)
	heading, rest := handoffHeading(body, o.title, sid, e.Now())
	text := heading + "\n"
	if rest != "" {
		text += "\n" + rest + "\n"
	}
	if sid == "" {
		fmt.Fprintln(stderr, i18n.T(lang, "loop.handoff.cli.server_no_session"))
	}
	m, err := handoffServerAccess(e, o)
	if err != nil {
		handoffErr(stderr, lang, cli.Message(lang, err))
		return handoffExitError
	}
	state := stateDir(handoffCLIEvent(e), e)
	entry := spoolEntry{Op: spoolAppend, Key: newSpoolKey(), Content: text, Session: handoffRawSession(e, o), At: e.Now().Format(time.RFC3339)}
	flushCtx, cancel := context.WithTimeout(context.Background(), spoolCLIBudget)
	res := flushHandoffSpool(flushCtx, e, m, state)
	cancel()
	if res.unreachable != nil {
		return handoffSpoolEntry(e, lang, m, entry, stdout, stderr)
	}
	// 退避へ回っても同じ鍵で送るので、応答だけが切れた追記は 2 重にならない
	v, err := m.AppendHandoff(text, entry.Key, entry.At)
	if cli.IsMemoryUnreachable(err) {
		return handoffSpoolEntry(e, lang, m, entry, stdout, stderr)
	}
	if err != nil {
		handoffErr(stderr, lang, cli.Message(lang, err))
		return handoffExitError
	}
	handoffServerWritten(e, o)
	fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.server_appended", "version", strconv.FormatInt(v, 10)))
	return handoffExitOK
}

// handoffRawSession は書き手のセッション ID（記号を除かない。サーバへ送る値）。
func handoffRawSession(e *Env, o handoffOpts) string {
	if s := trimSpace(o.session); s != "" {
		return s
	}
	return session.Detect(clientenv.FromFunc(e.env)).Session
}

// handoffSpoolEntry は本文を退避し、成功で終わる（退避も書けなければ失敗）。
// 資格情報の形の本文は退避しない（サーバが拒むものを平文で手元に残さない）。
func handoffSpoolEntry(e *Env, lang i18n.Lang, m *cli.MemoryAccess, entry spoolEntry, stdout, stderr io.Writer) int {
	if f, hit := domain.FindSecret(entry.Content); hit {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.spool_secret_refused", "line", f.Line, "label", f.Label))
		return handoffExitError
	}
	name, err := writeSpool(m.SpoolDir(), entry, e.Now())
	if err != nil {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.spool_failed", "reason", err.Error()))
		return handoffExitError
	}
	fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.server_spooled", "name", name))
	return handoffExitOK
}

// handoffServerCompact はサーバの引き継ぎの全体を書き直す。読んだ版を渡し、今の版と違えばサーバが何も書かずに拒む。
func handoffServerCompact(e *Env, lang i18n.Lang, o handoffOpts, body string, stdout, stderr io.Writer) int {
	if o.file != "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.server_file"))
		return handoffExitUsage
	}
	if !o.hasVer || o.sha != "" {
		handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.server_need_version"))
		return handoffExitUsage
	}
	body, code := handoffCompactBody(lang, o, body, stderr)
	if code != handoffExitOK {
		return code
	}
	m, err := handoffServerAccess(e, o)
	if err != nil {
		handoffErr(stderr, lang, cli.Message(lang, err))
		return handoffExitError
	}
	entry := spoolEntry{Op: spoolCompact, Key: newSpoolKey(), Content: body + "\n", Version: o.version, Session: handoffRawSession(e, o), At: e.Now().Format(time.RFC3339)}
	flushCtx, cancel := context.WithTimeout(context.Background(), spoolCLIBudget)
	res := flushHandoffSpool(flushCtx, e, m, stateDir(handoffCLIEvent(e), e))
	cancel()
	if res.unreachable != nil {
		return handoffSpoolEntry(e, lang, m, entry, stdout, stderr)
	}
	v, err := m.ReplaceHandoff(entry.Content, o.version, entry.At)
	switch {
	case cli.IsMemoryConflict(err):
		handoffErr(stderr, lang, cli.Message(lang, err))
		return handoffExitConflict
	case cli.IsMemoryUnreachable(err):
		return handoffSpoolEntry(e, lang, m, entry, stdout, stderr)
	case err != nil:
		handoffErr(stderr, lang, cli.Message(lang, err))
		return handoffExitError
	}
	handoffServerWritten(e, o)
	fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.server_compacted", "version", strconv.FormatInt(v, 10)))
	return handoffExitOK
}
