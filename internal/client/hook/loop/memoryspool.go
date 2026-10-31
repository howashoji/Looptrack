package loop

// サーバに届かなかった引き継ぎの退避（置き場 server）。
//
// 置き場は usage-spool と同じ形で、looptrack の置き場（usagesnap.StateDir）の handoff-spool/<プロジェクトの鍵>/<Unix 秒>-<16 進 8 桁>.json。
// 作業ツリーの外なので版管理に入らない。端末の中のどのセッションも送れる。本文を持つので 0600 で書く。
//
// 同じものを 2 回送らない: 送る前にファイルを .sending へ rename して取り、取った時刻を更新時刻に残す（rename に勝った 1 本だけが送る）。
// 追記の退避は作るときに冪等の鍵を持ち、送り直しても同じ鍵を使う。サーバは同じ鍵の再送で版を増やさないので、
// 応答の前に切れた追記も、落ちたプロセスの .sending を戻して送り直した分も 2 重にならない。
//
// 圧縮の退避: 今の本文が退避の本文と同じなら適用済みとして片付ける。読んだ版が今の版と同じときだけ置き換えで送る。古ければ送らずに残し、注意を出す。
// 追記として送らない。圧縮は全文の書き直しなので、追記にすると本文が 2 重になり、16 KiB の上限にも当たるため。
// 解き方は読み直して compact をやり直し、退避を --drop で捨てる。
//
// サーバが拒んだものも残して注意を出す。理由はサーバの文面（行番号と規則の語だけ）で、値は出ない。
// 資格情報の形の本文は最初から退避しない（handoffSpoolEntry）。
//
// 本文を書いた時刻は退避の時刻で、送るときに版へ渡す（written_at）。どの作業ツリーが送っても、鮮度の判定は書いた時刻で見る。
// 書いた記録（handoff-written）は送れた後にだけ残す。退避しただけの本文で Stop を通すと、--drop や失敗で消えたときに書かれないまま畳まれる。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

const (
	spoolAppend  = "append"
	spoolCompact = "compact"
	// spoolSendingSuffix は送っている最中のファイルの印（SpoolFiles の *.json に当たらない）。
	spoolSendingSuffix = ".sending"
	// spoolHookSuffix は hook の ID を添える横のファイルの印。
	spoolHookSuffix = ".hook"
	// spoolStaleSending を過ぎた .sending は、送っていたプロセスが落ちたものとして戻す。
	spoolStaleSending = 10 * time.Minute
	// spoolFlushBudget は Stop の 1 回の送信の打ち切り（hook の持ち時間を使い切らない。残りは次の機会に送る）。
	spoolFlushBudget = 1500 * time.Millisecond
	// spoolCLIBudget は looptrack handoff の書き込みの前の送信の打ち切り。
	spoolCLIBudget = 10 * time.Second
)

// handoffSpoolName は退避の行に出るファイルの名前。
var handoffSpoolName = regexp.MustCompile(`[0-9]+-[0-9a-f]{8,16}\.json`)

// spoolEntry は退避 1 件。
type spoolEntry struct {
	Op      string `json:"op"`
	Key     string `json:"key"` // 冪等の鍵（作るときに決め、送り直しても変えない）
	Content string `json:"content"`
	Version int64  `json:"version,omitempty"` // compact の読んだ版
	Session string `json:"session,omitempty"` // 書いたセッション（シェルの中の ID）
	At      string `json:"at"`                // 退避した時刻（手元の時計）
	// HookSession は PostToolUse が見た hook の ID（シェルの中の ID と違う器がある）。退避には書かず、横のファイル（spoolHookPath）から読む
	HookSession string `json:"-"`
}

// spoolResult は送信の結果。
type spoolResult struct {
	sent        int // 送った（片付けた）件数
	left        int // 残っている件数（送信中を含む）
	files       []string
	owners      []spoolEntry // 残っている退避（持ち主の判定に使う）
	why         string       // 注意に添える理由（最初の 1 件）
	unreachable error        // 届かなかった、か 5xx（ここで止める。順序を守るので後ろを先に送らない）
}

func (r *spoolResult) keep(path, why string) {
	r.left++
	r.files = append(r.files, filepath.Base(path))
	if en, err := readSpool(path); err == nil {
		r.owners = append(r.owners, en)
	}
	if r.why == "" {
		r.why = why
	}
}

// ownedBy は、残っている退避にこのセッションのものがあるかを返す（own が空なら、持ち主の分からない退避）。
func (r spoolResult) ownedBy(own []string) bool {
	for _, en := range r.owners {
		if len(own) == 0 && en.Session == "" && en.HookSession == "" {
			return true
		}
		if slices.Contains(own, en.Session) || slices.Contains(own, en.HookSession) {
			return true
		}
	}
	return false
}

func (r spoolResult) notice(lang i18n.Lang) string {
	return i18n.T(lang, "loop.handoff.server.spool.notice", "count", r.left, "files", strings.Join(r.files, ", "), "reason", r.why)
}

// newSpoolKey は冪等の鍵を作る。
func newSpoolKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writeSpool は退避を書き、ファイルの名前を返す。
//
// 名前の先頭は時刻なので、同じ秒でも書いた順に並ぶ（古い順に送る）。後ろの乱数と O_EXCL で、同じ時刻の書き込みが互いを消さないようにする。
// 書き終えてから見える名前に移す（書きかけを別のセッションが送らないため）。
func writeSpool(dir string, en spoolEntry, now time.Time) (string, error) {
	if dir == "" {
		return "", os.ErrNotExist
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.Marshal(en)
	if err != nil {
		return "", err
	}
	for try := 0; ; try++ {
		var r [4]byte
		if _, err := rand.Read(r[:]); err != nil {
			return "", err
		}
		name := fmt.Sprintf("%d-%08x%s.json", now.Unix(), now.Nanosecond(), hex.EncodeToString(r[:]))
		tmp := filepath.Join(dir, "."+name)
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if os.IsExist(err) && try < 8 {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(b)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(tmp)
			return "", werr
		}
		return name, os.Rename(tmp, filepath.Join(dir, name))
	}
}

// spoolHookPath は退避の横に置く hook の ID のファイル。退避そのものを書き戻さないので、送信や --drop と競っても退避が生き返らない。
func spoolHookPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), spoolSendingSuffix)
	return filepath.Join(filepath.Dir(path), "."+base+spoolHookSuffix)
}

// noteSpoolHookSession は退避に hook の ID を添える（PostToolUse から。退避が無ければ何もしない）。
func noteSpoolHookSession(ev hookio.Event, e *Env, name string) {
	sid := digestSessionID(ev, e)
	m, err := hookMemoryAccess(ev, e, root(ev, e))
	if err != nil || sid == "" {
		return
	}
	p := filepath.Join(m.SpoolDir(), name)
	if isFile(p) || isFile(p+spoolSendingSuffix) {
		_ = os.WriteFile(spoolHookPath(p), []byte(sid), 0o600)
	}
}

// sendingFiles は送っている最中か、落ちたプロセスが残したファイル。
func sendingFiles(dir string) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, "*"+spoolSendingSuffix))
	return paths
}

// pendingSpool は送らずに今の退避を数える。
func pendingSpool(lang i18n.Lang, dir, why string) spoolResult {
	var res spoolResult
	for _, p := range usagesnap.SpoolFiles(dir) {
		res.keep(p, why)
	}
	for _, p := range sendingFiles(dir) {
		res.keep(p, i18n.T(lang, "loop.handoff.server.spool.why.sending"))
	}
	return res
}

// recoverSending は落ちたプロセスが残した .sending を戻す（更新時刻は取得の時刻）。持ち主の退避が無くなった hook の ID のファイルも片付ける。
func recoverSending(dir string, now time.Time) {
	for _, p := range sendingFiles(dir) {
		if st, err := os.Stat(p); err == nil && now.Sub(st.ModTime()) > spoolStaleSending {
			_ = os.Rename(p, strings.TrimSuffix(p, spoolSendingSuffix))
		}
	}
	hooks, _ := filepath.Glob(filepath.Join(dir, ".*"+spoolHookSuffix))
	for _, h := range hooks {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(h), "."), spoolHookSuffix)
		p := filepath.Join(dir, name)
		st, err := os.Stat(h)
		if err == nil && now.Sub(st.ModTime()) > spoolStaleSending && !isFile(p) && !isFile(p+spoolSendingSuffix) {
			_ = os.Remove(h)
		}
	}
}

// flushHandoffSpool は退避を古い順に送る。ctx の締切で要求ごと打ち切り、残ったものを返す。
func flushHandoffSpool(ctx context.Context, e *Env, m *cli.MemoryAccess, state string) spoolResult {
	dir := m.SpoolDir()
	var res spoolResult
	if dir == "" {
		return res
	}
	m.WithContext(ctx)
	defer m.WithContext(nil)
	recoverSending(dir, e.Now())
	lang := e.lang()
	for _, p := range usagesnap.SpoolFiles(dir) {
		if res.unreachable != nil {
			res.keep(p, res.why)
			continue
		}
		if ctx.Err() != nil {
			res.keep(p, i18n.T(lang, "loop.handoff.server.spool.why.no_time"))
			continue
		}
		claimed := p + spoolSendingSuffix
		if os.Rename(p, claimed) != nil {
			continue // 別のセッションが取った
		}
		// rename は更新時刻を変えないので、取った時刻を入れる（退避が古くても、送信中を落ちたものと見ない）
		_ = os.Chtimes(claimed, e.Now(), e.Now())
		en, why, err := sendSpool(lang, m, claimed)
		switch {
		case err != nil && ctx.Err() != nil: // 打ち切った。届かなかったのではない
			_ = os.Rename(claimed, p)
			res.keep(p, i18n.T(lang, "loop.handoff.server.spool.why.no_time"))
		case err != nil:
			_ = os.Rename(claimed, p)
			res.unreachable = err
			if st := cli.MemoryServerStatus(err); st != 0 {
				res.keep(p, i18n.T(lang, "loop.handoff.server.spool.why.server_error", "status", st))
			} else {
				res.keep(p, i18n.T(lang, "loop.handoff.server.spool.why.unreachable"))
			}
		case why != "":
			_ = os.Rename(claimed, p)
			res.keep(p, why)
		default:
			_ = os.Remove(claimed)
			_ = os.Remove(spoolHookPath(p))
			res.sent++
			at, terr := time.Parse(time.RFC3339, en.At)
			if terr != nil {
				at = e.Now()
			}
			markHandoffWrittenAt(state, en.Session, at)
			if en.HookSession != "" {
				markHandoffWrittenAt(state, en.HookSession, at)
			}
		}
	}
	for _, p := range sendingFiles(dir) {
		res.keep(p, i18n.T(lang, "loop.handoff.server.spool.why.sending"))
	}
	return res
}

// sendSpool は 1 件を送る。届かなければ err。送れなかった理由があれば why。
func sendSpool(lang i18n.Lang, m *cli.MemoryAccess, path string) (en spoolEntry, why string, err error) {
	name := strings.TrimSuffix(filepath.Base(path), spoolSendingSuffix)
	rejected := func(reason string) string {
		return i18n.T(lang, "loop.handoff.server.spool.why.rejected", "name", name, "reason", reason)
	}
	en, rerr := readSpool(path)
	if rerr != nil {
		return en, rejected(i18n.T(lang, "loop.handoff.digest.why.bad_json")), nil
	}
	defer m.AsSession(m.AsSession(en.Session))
	if en.Op == spoolCompact {
		var cur int64
		content := ""
		doc, gerr := m.Handoff()
		switch {
		case gerr == nil:
			cur, content = doc.Memory.Version, doc.Memory.Content
		case cli.IsMemoryUnreachable(gerr):
			return en, "", gerr
		case !cli.IsMemoryMissing(gerr):
			return en, rejected(cli.Message(lang, gerr)), nil
		}
		if strings.TrimSpace(content) == strings.TrimSpace(en.Content) {
			return en, "", nil // 適用済み（応答の前に切れた置き換え）
		}
		if en.Version != cur {
			return en, i18n.T(lang, "loop.handoff.server.spool.why.stale", "name", name), nil
		}
		_, err = m.ReplaceHandoff(en.Content, en.Version, en.At)
	} else {
		_, err = m.AppendHandoff(en.Content, en.Key, en.At)
	}
	switch {
	case err == nil:
		return en, "", nil
	case cli.IsMemoryUnreachable(err):
		return en, "", err
	case en.Op == spoolCompact && cli.IsMemoryConflict(err):
		return en, i18n.T(lang, "loop.handoff.server.spool.why.stale", "name", name), nil
	}
	return en, rejected(cli.Message(lang, err)), nil
}

// handoffSpoolCommand は looptrack handoff spool（一覧・--show・--drop）。
func handoffSpoolCommand(e *Env, lang i18n.Lang, o handoffOpts, stdout, stderr io.Writer) int {
	m, err := handoffServerAccess(e, o)
	if err != nil {
		handoffErr(stderr, lang, cli.Message(lang, err))
		return handoffExitError
	}
	dir := m.SpoolDir()
	target := o.show + o.drop
	if target != "" {
		if filepath.Base(target) != target || !isFile(filepath.Join(dir, target)) {
			handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.spool_bad_name"))
			return handoffExitUsage
		}
		p := filepath.Join(dir, target)
		if o.drop != "" {
			if err := os.Remove(p); err != nil {
				handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", target, "err", err.Error()))
				return handoffExitError
			}
			_ = os.Remove(spoolHookPath(p))
			fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.spool_dropped", "name", target))
			return handoffExitOK
		}
		en, err := readSpool(p)
		if err != nil {
			handoffErr(stderr, lang, i18n.T(lang, "loop.handoff.cli.io_error", "path", target, "err", err.Error()))
			return handoffExitError
		}
		if f, hit := domain.FindSecret(en.Content); hit {
			fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.spool_secret", "line", f.Line, "label", f.Label, "path", p))
			return handoffExitOK
		}
		fmt.Fprint(stdout, en.Content)
		return handoffExitOK
	}
	files := append(usagesnap.SpoolFiles(dir), sendingFiles(dir)...)
	if len(files) == 0 {
		fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.spool_none"))
		return handoffExitOK
	}
	for _, p := range files {
		en, err := readSpool(p)
		if err != nil {
			continue
		}
		fmt.Fprintln(stdout, i18n.T(lang, "loop.handoff.cli.spool_row", "name", filepath.Base(p), "op", en.Op, "at", en.At, "bytes", len(en.Content)))
	}
	return handoffExitOK
}

func readSpool(p string) (spoolEntry, error) {
	var en spoolEntry
	b, err := os.ReadFile(p)
	if err != nil {
		return en, err
	}
	if err := json.Unmarshal(b, &en); err != nil {
		return en, err
	}
	if h, err := os.ReadFile(spoolHookPath(p)); err == nil {
		en.HookSession = string(h)
	}
	return en, nil
}
