package loop

// タイムラインを場面ごとに読む（docs/server/DESIGN.md §9-9「確認と投稿の場面」「タイムラインの読み飛ばしを無くす仕組み」）。
// 既にある hook に足すだけで、hook の名前と配線は増やさない（導入先が init をやり直さなくても動く）。
//
//   - セッション開始（session-start-messages）と作業開始（user-prompt-messages）: 未読と未了解をサーバの文面のまま入れる。
//     新しいセッションには「いま」と直近、続きのセッションには前の位置からの未読（どちらにするかはサーバが位置で決める）
//   - 作業区切り（post-work-complete-handoff-mark）: コミットを送り（切り離した子プロセスで `looptrack timeline commit`）、
//     未読を入れて近況を促し、区切りの時点の最新の通し番号を印に残す
//   - ターンの終わり（stop-handoff-freshness）: 印の番号までの読み残しか、未了解の待機と依頼があればやり直しを求める
//   - 書き込みの前（pre-tool-scope-guard）: 軽い確認を 1 分に 1 回まで行い、新しく届いた依頼と未了解の待機と、
//     前の確認の後に並びから外れた待機（取り下げは理由つきで「解けた」、それ以外は「このセッションには効かなくなった」）だけを出す（止めない）。
//     ほかの場面が取り下げを知らせたら、その待機を手元の並びから外す（同じ待機を書き込みの前で重ねて知らせない）
//
// 何が未読かはサーバが決める。読んだ位置は注入の文を作り終えた後に送り、締切に余裕が無ければ文も出さず位置も進めない。
// 位置を進めるのは、足した文が AI に届く場面と AI の組み合わせ（timelineAdvances）だけ。
// サーバに届かないときは作業を止めず、最後に確かめた時刻を 10 分に 1 回まで知らせる。経路の無い古いサーバには何も出さない。
// 切る手段は連絡と同じ LOOPTRACK_LOOP_MESSAGES=0。セッション ID の無い呼び出しは読まない（位置を持てず、毎回「いま」と直近が入るため）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// timelineScene は確認の場面。
type timelineScene string

const (
	sceneSessionStart timelineScene = "session-start"
	scenePrompt       timelineScene = "prompt"
	sceneBoundary     timelineScene = "boundary"
	sceneStop         timelineScene = "stop"
	scenePreTool      timelineScene = "pre-tool"
)

// timelineAdvances は、足した文が AI に届くとみて読んだ位置を進める（と受け取りを記録する）場面と AI の組み合わせ。
// 届かない組み合わせでは進めず、未読のまま次の場面で出す（黙って消えるものを作らない）。実機で確かめたらここだけを直す。
//
//   - セッション開始と作業開始: additionalContext が届く（連絡の hook が同じ前提で既読を付けている）
//   - 作業区切り（PostToolUse）: Claude Code と Codex。Codex は 2026-11-12 に Codex 0.165.0 の実機で届くと確かめた。
//     Copilot は届くかを実機で確かめていない
//   - ターンの終わり（Stop）: やり直しの理由が AI に渡る AI だけ。Codex は配線が --no-block で、利用者への知らせに落ちる
//   - 書き込みの前（PreToolUse）: 位置は進めない（出来事を出さない）。ここで進めるのは出した投稿の受け取りの記録だけ。
//     配線は Claude Code だけ（kit/loop/manifest.json で pre-tool-scope-guard の codex と copilot は null）
//
// 配線の無い組み合わせを true にしない（timelineSceneHooks と manifest を突き合わせる試験がある）。
var timelineAdvances = map[timelineScene]map[hookio.Agent]bool{
	sceneSessionStart: {hookio.ClaudeCode: true, hookio.Codex: true, hookio.Copilot: true},
	scenePrompt:       {hookio.ClaudeCode: true, hookio.Codex: true, hookio.Copilot: true},
	sceneBoundary:     {hookio.ClaudeCode: true, hookio.Codex: true},
	sceneStop:         {hookio.ClaudeCode: true, hookio.Copilot: true},
	scenePreTool:      {hookio.ClaudeCode: true},
}

// timelineSceneHooks は場面を読む hook（manifest の名前）。
var timelineSceneHooks = map[timelineScene]string{
	sceneSessionStart: "session-start-messages",
	scenePrompt:       "user-prompt-messages",
	sceneBoundary:     "post-work-complete-handoff-mark",
	sceneStop:         "stop-handoff-freshness",
	scenePreTool:      "pre-tool-scope-guard",
}

const (
	// timelineHookRows は 1 回に入れる出来事と近況の行の数（サーバの上限と同じ）。
	timelineHookRows = 20
	// timelineCheckEvery は書き込みの前の軽い確認の間隔。timelineNoticeEvery は届かない知らせの間隔。
	timelineCheckEvery  = time.Minute
	timelineNoticeEvery = 10 * time.Minute
	// timelineCommitTimeout は送ったコミットの位置を git に問う持ち時間。
	timelineCommitTimeout = 2 * time.Second
	// timelineRetryMax は 1 回の区切りで送り直す送りかけの範囲の数（子の本数の上限）。
	timelineRetryMax = 3
	// timelineSeenMin は位置を送り始めるのに要る締切までの残り（足りなければ送らず、文も出さない）。
	timelineSeenMin = 200 * time.Millisecond
	// timelineKeep を過ぎた手元の記録（セッションごとの状態・印・送ったコミット）はセッション開始で消す。
	timelineKeep = 7 * 24 * time.Hour
)

// timelineBeforeSeen は位置を送る直前に呼ぶ（テストが「出力の前に時間切れになる」形を作る）。
var timelineBeforeSeen = func(context.Context) {}

// messagesOff は利用者が連絡とタイムラインの注入を切ったか（LOOPTRACK_LOOP_MESSAGES=0）。
func messagesOff(e *Env) bool {
	switch strings.ToLower(trimSpace(e.env("LOOPTRACK_LOOP_MESSAGES"))) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

// timelineAdvanceOK はこの場面と AI で位置を進めるか。--no-block か LOOPTRACK_LOOP_NO_BLOCK=1 の Stop は理由が利用者への知らせに落ちるので進めない。
func timelineAdvanceOK(ev hookio.Event, e *Env, scene timelineScene) bool {
	if scene == sceneStop && (e.noBlock || e.env("LOOPTRACK_LOOP_NO_BLOCK") == "1") {
		return false
	}
	return timelineAdvances[scene][ev.Agent]
}

// timelineState はセッションごとの手元の記録（.looptrack-freshness の下。版管理の外）。
type timelineState struct {
	// CheckedAt・Seq・Holds は書き込みの前の軽い確認（前に確かめた時刻・最新の通し番号・有効な待機の ID の並び）
	CheckedAt time.Time `json:"checked_at"`
	Seq       int64     `json:"seq"`
	Holds     []int64   `json:"holds"`
	// Pending は前に出した未了解の待機の ID（了解されるまで確認のたびに出し直す）
	Pending []int64 `json:"pending"`
	// OKAt は最後にサーバで確かめた時刻。NoticeAt は届かない知らせを出した時刻
	OKAt     time.Time `json:"ok_at"`
	NoticeAt time.Time `json:"notice_at"`
}

func timelineDir(state string) string {
	return filepath.Join(state, ".looptrack-freshness", "timeline")
}

func timelineStatePath(state, sid string) string {
	return filepath.Join(timelineDir(state), sid+".json")
}

// timelineMarkPath は作業区切りの印（区切りの時点の最新の通し番号。引き継ぎの handoff-pending とは別のファイル）。
func timelineMarkPath(state, sid string) string {
	return filepath.Join(timelineDir(state), sid+".mark")
}

func loadTimelineState(p string) timelineState {
	var st timelineState
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// writeAtomic は同じ置き場の一時ファイルに書いてから置き換える（並んで動いた hook が読み書きしても、壊れた中身を読ませない）。
func writeAtomic(p string, b []byte) {
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*.tmp")
	if err != nil {
		return
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr != nil || cerr != nil || os.Rename(f.Name(), p) != nil {
		_ = os.Remove(f.Name())
	}
}

// saveTimelineState は一時ファイルから置き換える（同じセッションの hook が並んで動いても壊れた JSON を残さない）。
func saveTimelineState(p string, st timelineState) {
	b, err := json.Marshal(st)
	if err != nil || os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	writeAtomic(p, b)
}

// updateTimelineState は手元の記録を読み、fn で直して書き戻す（場面ごとに読み書きを写さない）。
func updateTimelineState(state, sid string, fn func(*timelineState)) {
	p := timelineStatePath(state, sid)
	st := loadTimelineState(p)
	fn(&st)
	saveTimelineState(p, st)
}

// forgetEnded は文に入れた投稿のうち効力を失ったもの（取り下げ）の ID を、書き込みの前の確認が持つ待機の並びから外す。
// 書き込みの前以外の場面が取り下げを知らせた後で、書き込みの前の確認が同じ待機を別の文面で重ねて知らせないため。
func (st *timelineState) forgetEnded(posts []cli.TimelinePost) {
	st.Holds = slices.DeleteFunc(st.Holds, func(id int64) bool {
		return slices.ContainsFunc(posts, func(v cli.TimelinePost) bool { return v.ID == id && v.State != "active" })
	})
}

// timelineCall は 1 回の場面の確認に使うもの。
type timelineCall struct {
	ev       hookio.Event
	e        *Env
	scene    timelineScene
	sid      string
	state    string
	m        *cli.MemoryAccess
	reqCtx   context.Context
	cancel   context.CancelFunc
	deadline time.Time
}

// newTimelineCall はサーバの口を作る。切られている・セッション ID が無い・サーバの設定が無いときは nil。
func newTimelineCall(ctx context.Context, ev hookio.Event, e *Env, scene timelineScene) *timelineCall {
	if messagesOff(e) {
		return nil
	}
	sid := sessionFile(ev)
	if sid == "" {
		return nil
	}
	m, err := hookMemoryAccess(ev, e, root(ev, e))
	if err != nil {
		return nil
	}
	deadline := time.Now().Add(memoryHookBudget)
	if d, ok := ctx.Deadline(); ok {
		deadline = d.Add(-memoryHookMargin)
	}
	reqCtx, cancel := context.WithDeadline(ctx, deadline)
	m.WithContext(reqCtx)
	return &timelineCall{ev: ev, e: e, scene: scene, sid: sid, state: stateDir(ev, e), m: m, reqCtx: reqCtx, cancel: cancel, deadline: deadline}
}

func (t *timelineCall) close() {
	if t != nil {
		t.cancel()
	}
}

// failed は要求の失敗を扱う。届かなければ 10 分に 1 回まで知らせの文を返す。古いサーバ・サーバが応答した誤りには何も出さない。
func (t *timelineCall) failed(err error) string {
	if !cli.Unreachable(err) || cli.TimelineUnsupported(err) {
		return ""
	}
	p := timelineStatePath(t.state, t.sid)
	st := loadTimelineState(p)
	now := t.e.Now()
	if !st.NoticeAt.IsZero() && now.Sub(st.NoticeAt) < timelineNoticeEvery && !now.Before(st.NoticeAt) {
		return ""
	}
	st.NoticeAt = now
	saveTimelineState(p, st)
	lang := t.e.lang()
	if st.OKAt.IsZero() {
		return i18n.T(lang, "loop.timeline.unreachable_never")
	}
	return i18n.T(lang, "loop.timeline.unreachable", "time", st.OKAt.Local().Format("2006-01-02 15:04:05"))
}

// reached は確かめられた時刻を残す（届かない知らせに添える）。
func (t *timelineCall) reached(st *timelineState) {
	st.OKAt = t.e.Now()
}

// commit は文を出すと決めた後の最後に呼ぶ。締切まで timelineSeenMin を切っていれば false を返し、呼ぶ側は何も出さない（位置も進めない）。
// 余裕があれば、この場面と AI で文が届くときだけ位置を advance まで進め、出した投稿の受け取りを記録して true を返す。
// 位置と受け取りの送信は締切（reqCtx。hook の持ち時間の memoryHookMargin 手前）で打ち切り、送りかけた後は結果に関わらず文を出す。
// サーバが受け取ったかは応答が来るまで分からないので、ここで文を捨てると「位置だけ進んで文が消える」ことが起きる。
// 送れなかったときは同じものがもう一度届くだけで、抜けない。
func (t *timelineCall) commit(advance int64, posts []cli.TimelinePost) bool {
	timelineBeforeSeen(t.reqCtx)
	if t.reqCtx.Err() != nil || time.Until(t.deadline) < timelineSeenMin {
		return false
	}
	if !timelineAdvanceOK(t.ev, t.e, t.scene) {
		return true
	}
	if advance > 0 {
		_ = t.m.TimelineSeen(advance)
	}
	_ = t.m.TimelineReceipts(posts)
	return true
}

// feedPosts は文に入った投稿（未了解の待機と依頼・未達の知らせ）。
func feedPosts(f *cli.TimelineFeed) []cli.TimelinePost {
	var out []cli.TimelinePost
	for _, l := range [][]cli.TimelinePost{f.UnackedHolds, f.UnackedRequests, f.Undelivered} {
		out = append(out, l...)
	}
	return out
}

// read は場面の未読と未了解を読み、文脈に入れる文を返す（無ければ空）。ok が偽なら締切に間に合わなかったので何も出さない。
// head は読んだ時点の最新の通し番号（読めなければ 0）。
func (t *timelineCall) read() (text string, head int64, ok bool) {
	f, err := t.m.TimelineUnread(timelineHookRows)
	if err != nil {
		return t.failed(err), 0, true
	}
	updateTimelineState(t.state, t.sid, t.reached)
	posts := feedPosts(f)
	if !t.commit(f.AdvanceTo, posts) {
		return "", 0, false
	}
	updateTimelineState(t.state, t.sid, func(st *timelineState) { st.forgetEnded(posts) })
	return strings.TrimSpace(f.Text), f.HeadSeq, true
}

// timelineSceneText は場面の未読と未了解の文（セッション開始・作業開始）。ok が偽なら何も出さない。
func timelineSceneText(ctx context.Context, ev hookio.Event, e *Env, scene timelineScene) (string, bool) {
	if scene == sceneSessionStart && !messagesOff(e) {
		timelineStartSession(ctx, ev, e)
	}
	t := newTimelineCall(ctx, ev, e, scene)
	if t == nil {
		return "", true
	}
	defer t.close()
	text, _, ok := t.read()
	return text, ok
}

// timelineBoundary は作業区切りの処理。コミットを送り、未読と近況の促しの文を返し、区切りの時点の最新の通し番号を印に残す。
// commitDir はコミットした作業ツリー（コミットでなければ空）。n はその呼び出しのコミットの成功の行の数（取れなければ 0）。
func timelineBoundary(ctx context.Context, ev hookio.Event, e *Env, commitDir string, n int) (string, bool) {
	t := newTimelineCall(ctx, ev, e, sceneBoundary)
	if t == nil {
		return "", true
	}
	defer t.close()
	if commitDir != "" {
		t.sendCommits(ctx, commitDir, n)
	}
	text, head, ok := t.read()
	if !ok {
		return "", false
	}
	if head > 0 { // 読めたときだけ印を積む（番号の分からない印では読み終えたかを決められない）
		t.addMark(head)
		nudge := i18n.T(e.lang(), "loop.timeline.boundary.update")
		if text == "" {
			return nudge, true
		}
		return text + "\n\n" + nudge, true
	}
	return text, true
}

// addMark は印の番号を大きいほうに置き換える（区切りが重なっても、いちばん後の区切りまでを見る）。
func (t *timelineCall) addMark(seq int64) {
	p := timelineMarkPath(t.state, t.sid)
	if cur := readTimelineMark(p); cur >= seq {
		return
	}
	writeAtomic(p, []byte(strconv.FormatInt(seq, 10)+"\n"))
}

func readTimelineMark(p string) int64 {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// timelineStop はターンの終わりの確認。印の番号までの読み残しか、その番号までに出た未了解の待機と依頼があれば、やり直しの理由を返す。
// 読み終えていれば印を畳む。区切りの後に載った分では求めない（次の場面に回す）。届かないときは知らせ（notice）だけを返す。
func timelineStop(ctx context.Context, ev hookio.Event, e *Env) (block, notice string) {
	state := stateDir(ev, e)
	sid := sessionFile(ev)
	if sid == "" {
		return "", ""
	}
	markPath := timelineMarkPath(state, sid)
	mark := readTimelineMark(markPath)
	if mark <= 0 {
		return "", ""
	}
	if messagesOff(e) {
		_ = os.Remove(markPath)
		return "", ""
	}
	t := newTimelineCall(ctx, ev, e, sceneStop)
	if t == nil {
		return "", ""
	}
	defer t.close()
	f, err := t.m.TimelineUnread(timelineHookRows)
	if err != nil {
		if cli.TimelineUnsupported(err) {
			_ = os.Remove(markPath)
		}
		return "", t.failed(err)
	}
	updateTimelineState(state, sid, t.reached)
	left := f.ReadSeq < mark && len(f.Rows) > 0 && f.Rows[0].Seq <= mark
	for _, l := range [][]cli.TimelinePost{f.UnackedHolds, f.UnackedRequests} {
		for _, v := range l {
			if v.Seq <= mark {
				left = true
			}
		}
	}
	if !left {
		_ = os.Remove(markPath)
		return "", ""
	}
	if ev.StopHookActive { // やり直しの後の停止では重ねて求めない（印は残し、次のターンの終わりにまた見る）
		return "", ""
	}
	posts := feedPosts(f)
	if !t.commit(f.AdvanceTo, posts) {
		return "", ""
	}
	updateTimelineState(state, sid, func(st *timelineState) { st.forgetEnded(posts) })
	return i18n.T(e.lang(), "loop.timeline.stop.reason") + "\n\n" + strings.TrimSpace(f.Text), ""
}

// timelinePreTool は書き込みの前の軽い確認。1 分に 1 回までサーバに問い、新しく届いた依頼と未了解の待機、前の確認から解けた待機があれば文を返す。
// 出来事と近況は出さず、位置も進めない。止めない。
func timelinePreTool(ctx context.Context, ev hookio.Event, e *Env) string {
	if messagesOff(e) || sessionFile(ev) == "" {
		return ""
	}
	state, sid := stateDir(ev, e), sessionFile(ev)
	p := timelineStatePath(state, sid)
	st := loadTimelineState(p)
	now := e.Now()
	if !st.CheckedAt.IsZero() && now.Sub(st.CheckedAt) < timelineCheckEvery && !now.Before(st.CheckedAt) {
		return "" // 1 分以内に確かめた。サーバに問わない
	}
	// 判定の直後に確かめた時刻を書く（同時に動いた書き込みの前の確認が、そろってサーバに問う間を小さくする。完全な排他はしない）
	st.CheckedAt = now
	saveTimelineState(p, st)
	t := newTimelineCall(ctx, ev, e, scenePreTool)
	if t == nil {
		return ""
	}
	defer t.close()
	c, err := t.m.TimelineCheck()
	if err != nil {
		return t.failed(err)
	}
	st = loadTimelineState(p) // failed が書いた知らせの時刻を落とさない
	t.reached(&st)
	holds := slices.Clone(c.Holds)
	slices.Sort(holds)
	// 前に確かめてから何も載っていない・待機の並びが同じ・了解待ちの待機も無い → 取りに行かない
	if c.Seq <= max(st.Seq, c.ReadSeq) && slices.Equal(holds, st.Holds) && len(st.Pending) == 0 {
		st.Seq = max(st.Seq, c.Seq)
		saveTimelineState(p, st)
		return ""
	}
	f, err := t.m.TimelineUnread(1)
	if err != nil {
		saveTimelineState(p, st)
		return t.failed(err)
	}
	prevSeq, prevHolds := st.Seq, st.Holds
	st.Seq, st.Holds = max(st.Seq, c.Seq, f.HeadSeq), holds
	st.Pending = st.Pending[:0]
	var shown []cli.TimelinePost
	for _, v := range f.UnackedHolds {
		shown = append(shown, v)
		st.Pending = append(st.Pending, v.ID)
	}
	for _, v := range f.UnackedRequests {
		if v.Undelivered { // 依頼は新しく届いたものだけ（了解待ちの依頼は作業開始と区切りで出る）
			shown = append(shown, v)
		}
	}
	lifted, liftedPosts := timelineLiftedHolds(e.lang(), prevHolds, holds, f.Undelivered)
	if len(shown) == 0 && len(lifted) == 0 {
		saveTimelineState(p, st)
		return ""
	}
	if !t.commit(0, append(slices.Clone(shown), liftedPosts...)) {
		// 出せなかったので、確かめた通し番号と並びを進めない（次の確認で同じ依頼と待機と解けた待機をもう一度取りに行く）
		st.Seq, st.Holds = prevSeq, prevHolds
		saveTimelineState(p, st)
		return ""
	}
	saveTimelineState(p, st)
	var parts []string
	if len(shown) > 0 {
		parts = append(parts, i18n.T(e.lang(), "loop.timeline.pretool.heading")+"\n\n"+t.m.TimelinePostsText(shown, f.Timezone))
	}
	return strings.Join(append(parts, lifted...), "\n\n")
}

// timelineLiftedHolds は、前の確認で手元に持っていた待機の ID のうち今の並びに無いものを 1 件 1 行で知らせる文と、
// 受け取りを記録する投稿（取り下げの知らせが未達のもの）を返す。了解しただけでは待機は効き続け、並びから外れない。
// 並びは呼ぶ側が今の値に置き換えるので、同じ待機の知らせは 1 度だけ出る。ほかの場面が取り下げを知らせたときは forgetEnded が並びから外す。
// 取り下げは未達の一覧に理由が載るので「解けた」と理由を出す。それ以外（期限・解除の条件・宛先の変化）はサーバから区別できず、
// 宛先の変化では待機がほかのセッションに効いたままなので、「このセッションには効かなくなった」と出す。
func timelineLiftedHolds(lang i18n.Lang, prev, now []int64, undelivered []cli.TimelinePost) (lines []string, posts []cli.TimelinePost) {
	for _, id := range prev {
		if slices.Contains(now, id) {
			continue
		}
		i := slices.IndexFunc(undelivered, func(v cli.TimelinePost) bool { return v.ID == id && v.State == "withdrawn" })
		if i < 0 {
			lines = append(lines, i18n.T(lang, "loop.timeline.pretool.no_longer_applies", "id", strconv.FormatInt(id, 10)))
			continue
		}
		reason := strings.TrimSpace(undelivered[i].WithdrawReason)
		if reason == "" {
			reason = "-"
		}
		lines = append(lines, i18n.T(lang, "loop.timeline.pretool.withdrawn", "id", strconv.FormatInt(id, 10))+"\n"+cli.TimelineQuote(reason))
		posts = append(posts, undelivered[i])
	}
	return lines, posts
}

// sendCommits は、このセッションがその作業ツリーで最後に観測した HEAD（timelineObserve）から今の HEAD までのコミットを、
// 切り離した子プロセス（`looptrack timeline commit`）で送る。観測はシェルの呼び出しのたびに進むので、pull・rebase で
// 取り込んだほかの人のコミットは範囲に入らない。n はこの呼び出しのコミットの成功の行の数で、0 より大きければ範囲の件数を n で上限する
// （`git pull && git commit` のように 1 回の呼び出しで取り込みとコミットが混ざったとき）。観測の記録が無い作業ツリーでは HEAD から
// n 件遡る（n が取れなければ HEAD の 1 件）。
//
// 送る範囲は始点と終点（--since・--until）で決め、送り終えるまで範囲ごとに「送りかけ」の記録に残す。子は送り終えたとき
// （恒久の拒否でも）自分の範囲の記録だけを消す。残っている範囲は次の区切りで同じ始点と終点のまま送り直す（その後に取り込んだ
// ほかの人のコミットは入らない。子がまだ動いていて重なっても、サーバが同じ SHA を重ねて載せない）。
// 送りかけの記録は観測を進める前に書く（観測が先に進むと、送れなかった範囲がどこにも残らない）。
// 規則（--first-parent・20 件まで・件名の検査）は CLI と同じ 1 か所。
func (t *timelineCall) sendCommits(ctx context.Context, dir string, n int) {
	c, cancel := context.WithTimeout(ctx, timelineCommitTimeout)
	defer cancel()
	git := func(args ...string) (string, bool) {
		out, code, err := t.e.run(c, t.e.Getwd(), nil, "git", append([]string{"-C", dir}, args...)...)
		return trimSpace(string(out)), err == nil && code == 0
	}
	top, head := gitTopHead(git)
	if top == "" || head == "" {
		return
	}
	spawn := func(since, until string) {
		pend := timelinePendingPath(t.state, t.sid, top, until)
		args := []string{"timeline", "commit", "--dir", top, "--until", until, "--pending", pend}
		if since != "" {
			args = append(args, "--since", since)
		}
		if t.e.spawn(args, top, []string{"LOOPTRACK_SESSION_ID=" + rawSessionID(t.ev)}) != nil {
			_ = os.Remove(pend) // 子を起こせなかった（looptrack でない実行ファイル）。送りかけは無い
		}
	}
	// 送り直し: 残っている範囲を、同じ始点と終点のまま古い順に timelineRetryMax 本まで送る（届かない間に溜まった分で、1 回の区切りに
	// 子を何本も起こさない。残りは次の区切りに回す）
	for _, r := range timelinePendingRanges(t.state, t.sid, top, timelineRetryMax) {
		spawn(r[0], r[1])
	}
	headPath := timelineHeadPath(t.state, t.sid, top)
	observed := readLine(headPath)
	back := func(k int) string { // HEAD から k 件遡った SHA（--first-parent の線。無ければ空）
		v, ok := git("rev-parse", "--verify", "-q", "HEAD~"+strconv.Itoa(k))
		if !ok {
			return ""
		}
		return v
	}
	since := ""
	switch {
	case observed != "":
		since = observed
		if cand := back(n); n > 0 && cand != "" && cand != since {
			if _, ok := git("merge-base", "--is-ancestor", since, cand); ok {
				since = cand
			}
		}
	case n > 1:
		since = back(n)
	}
	if since != head {
		writeAtomic(timelinePendingPath(t.state, t.sid, top, head), []byte(since+"\t"+head+"\n"))
	}
	writeAtomic(headPath, []byte(head+"\n"))
	if since != head { // 観測の後にコミットが無ければ送らない（amend の失敗・空の呼び出し）
		spawn(since, head)
	}
}

// timelinePendingRanges はこのセッションのその作業ツリーの送りかけの範囲（始点・終点。始点は空のこともある）を、記録の古い順に limit 本まで返す。
func timelinePendingRanges(state, sid, top string, limit int) [][2]string {
	paths, _ := filepath.Glob(timelinePendingPath(state, sid, top, "*"))
	mtime := func(p string) time.Time {
		if fi, err := os.Stat(p); err == nil {
			return fi.ModTime()
		}
		return time.Time{}
	}
	sort.SliceStable(paths, func(i, j int) bool { return mtime(paths[i]).Before(mtime(paths[j])) })
	var out [][2]string
	for _, p := range paths {
		if len(out) >= limit {
			break
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		since, until, ok := strings.Cut(strings.TrimRight(string(b), "\r\n"), "\t") // 始点は空のことがあるので、前の空白を落とさない
		if ok && until != "" {
			out = append(out, [2]string{since, until})
		}
	}
	return out
}

// gitTopHead は作業ツリーのルートと HEAD を 1 回の git で問う。
func gitTopHead(git func(args ...string) (string, bool)) (top, head string) {
	out, ok := git("rev-parse", "--show-toplevel", "HEAD")
	if !ok {
		return "", ""
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		return "", ""
	}
	return trimSpace(lines[0]), trimSpace(lines[1])
}

func readLine(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return trimSpace(string(b))
}

// worktreeKey は作業ツリーのパスを記録のファイル名に使える形にする。
func worktreeKey(top string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(top)))
	return hex.EncodeToString(sum[:8])
}

// timelineHeadPath はこのセッションがその作業ツリーで最後に観測した HEAD の記録。
func timelineHeadPath(state, sid, top string) string {
	return filepath.Join(timelineDir(state), sid+".head-"+worktreeKey(top))
}

// timelinePendingPath はこのセッションのその作業ツリーの、終点 until の送りかけの範囲の記録（「始点<TAB>終点」。子が送り終えたら消す）。
// 範囲ごとに別のファイルなので、ある子が送り終えても、ほかの範囲の記録は消えない。
func timelinePendingPath(state, sid, top, until string) string {
	return filepath.Join(timelineDir(state), sid+".pending-"+worktreeKey(top)+"-"+until)
}

// timelineObserve は dir の作業ツリーの HEAD をこのセッションの観測として記録する（シェルの呼び出しのたび・セッション開始。git rev-parse だけ）。
func timelineObserve(ctx context.Context, ev hookio.Event, e *Env, dir string) {
	sid := sessionFile(ev)
	if dir == "" || sid == "" || messagesOff(e) {
		return
	}
	c, cancel := context.WithTimeout(ctx, timelineCommitTimeout)
	defer cancel()
	top, head := gitTopHead(func(args ...string) (string, bool) {
		out, code, err := e.run(c, e.Getwd(), nil, "git", append([]string{"-C", dir}, args...)...)
		return trimSpace(string(out)), err == nil && code == 0
	})
	if top != "" && head != "" {
		writeAtomic(timelineHeadPath(stateDir(ev, e), sid, top), []byte(head+"\n"))
	}
}

// timelineStartSession はセッション開始の処理: 古い手元の記録を消し、いまの作業ツリーの HEAD を観測として記録する（初めての区切りの始点）。
func timelineStartSession(ctx context.Context, ev hookio.Event, e *Env) {
	expireTimelineState(stateDir(ev, e), e.Now())
	dir := ev.CWD
	if dir == "" {
		dir = root(ev, e)
	}
	timelineObserve(ctx, ev, e, dir)
}

// expireTimelineState は timelineKeep より古い手元の記録（状態・印・観測した HEAD・送りかけ・書きかけの一時ファイル）を消す。
func expireTimelineState(state string, now time.Time) {
	entries, err := os.ReadDir(timelineDir(state))
	if err != nil {
		return
	}
	for _, de := range entries {
		fi, err := de.Info()
		if err == nil && fi.Mode().IsRegular() && now.Sub(fi.ModTime()) > timelineKeep {
			_ = os.Remove(filepath.Join(timelineDir(state), de.Name()))
		}
	}
}
