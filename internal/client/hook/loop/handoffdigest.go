package loop

// 引き継ぎ鮮度ガード（stop-handoff-freshness）の backend issue: 引き継ぎをファイルではなく、イシューのセッション要約
// （`looptrack session digest <ID> -` が付ける印付きのコメント。DESIGN.md §9-7）で判定する。
//
// 判定: このセッションが In Progress にしているイシュー（summary の in_progress のうち own_session が付いたもの）の
// どれにも、このセッションが書いた最新の要約（latest_digest。other_session が false か、session_id がこのセッションのもの）が
// 完了のマーカーの時刻より後にあれば「新しい」。どれか 1 つでも欠けていれば「古い」として 1 回だけ差し戻す
// （差し戻しの回数の扱いは file バックエンドと同じ: Claude Code などは stop_hook_active、Copilot は差し戻しの写し）。
//
// サーバに届かない・遅い・応答が読めないときは止めない（fail-open。注意を 1 つ出して通す）。止めると、オフラインの
// 端末ではどのターンも終えられなくなる。summary は 1 要求 2 秒で打ち切り、子プロセス全体も digestFetchTimeout で打ち切る。
//
// 「このセッションが着手した」と言えないもの（着手のセッション ID が分からない・CLI と MCP で比べられない・
// own_session を返さない古いサーバ）は、判定の対象にしない（無いものとして扱い、その旨を注意で出す）。
// 誤って差し戻すと、利用者が着手したイシューに AI が要約を書くよう迫られるからである。
//
// プロジェクト別ルール session.digest_required が入なら、backend が file・auto-memory・command でも同じ判定を足す
// （ruleDigests。backend の判定と両方が通ったときだけ「新しい」）。ルールのための summary は backend の判定の後に、
// hook の持ち時間（入口で決めた ctx の締切）の残りの中でだけ取る（digestBudget）。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	clientenv "github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/session"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// digestFetchTimeout は summary を取る子プロセスの打ち切り（hook 全体の打ち切り 9 秒より十分に短く）。
const digestFetchTimeout = 5 * time.Second

// digestMinBudget は、ルールのための summary を取るのに要る hook の持ち時間の残り。これより短ければ取らない。
// digestBudgetMargin は、summary を打ち切った後の判定と出力のために残す時間。
//
// backend の判定（command は 8 秒まで）の後に固定の 5 秒で取ると、合わせて hook の持ち時間（9 秒）を越え、
// 入口の打ち切りが何も出さずに通す（fail-open）。backend が差し戻すはずの Stop まで黙って通っていた。
const (
	digestMinBudget    = 1500 * time.Millisecond
	digestBudgetMargin = time.Second
)

// digestBudget は ctx の締切（hook の入口で決めた持ち時間）から、ルールのための summary に使ってよい時間を返す。
// ok = false は残り（left）が digestMinBudget より短い（取らない）。締切の無い ctx では digestFetchTimeout。
func digestBudget(ctx context.Context) (limit, left time.Duration, ok bool) {
	dl, has := ctx.Deadline()
	if !has {
		return digestFetchTimeout, 0, true
	}
	left = time.Until(dl)
	if left < digestMinBudget {
		return 0, left, false
	}
	return min(digestFetchTimeout, left-digestBudgetMargin), left, true
}

// digestCheck は backend issue の判定の結果。
type digestCheck struct {
	// fetched は summary を読めたか（false なら notice に理由がある）
	fetched bool
	// fresh はこのセッションが着手中のイシューのすべてに、完了より新しい自分の要約があるか
	// （着手中のイシューが 1 つも無いときも true）
	fresh bool
	// stale は要約が足りないイシュー（表示 ID・理由の行）
	stale []digestStale
	// notice は止めずに知らせる注意（届かない・判定できない）。空なら無し
	notice string
	// keep は、通すがマーカーを畳まない（サーバに届かなかった。つながった後の Stop でもう一度確かめる）
	keep bool
	// required はプロジェクト別ルール session.digest_required の値（summary の応答の session_digest_required。無ければ false）
	required bool
}

// digestStale は要約が足りないイシュー 1 件。
type digestStale struct {
	id   string
	line string
}

// rawSessionID は hook の入力のセッション ID（記号を除かない。サーバへ X-Looptrack-Session として送る値）。
func rawSessionID(ev hookio.Event) string {
	v := toStr(ev.Raw["session_id"])
	if v == "" {
		v = toStr(ev.Raw["sessionId"])
	}
	return strings.TrimSpace(v)
}

// digestSessionID はこのセッションの ID（サーバへ X-Looptrack-Session として送り、要約の書き手と比べる値）。
// 順は 明示の LOOPTRACK_SESSION_ID → hook の入力の session_id → AI の環境変数（session.Detect の順）。
// シェルで打つ looptrack session digest も明示の LOOPTRACK_SESSION_ID を先に読むので、明示したときも書き手と食い違わない。
func digestSessionID(ev hookio.Event, e *Env) string {
	vars := clientenv.FromFunc(func(k string) string { return e.env(k) })
	return strings.TrimSpace(session.Detect(session.WithHookSession(vars, rawSessionID(ev))).Session)
}

// fetchSummary は `looptrack issue summary --limit 0 --json` を、このセッションの ID を明示して呼ぶ（limit で打ち切る）。
// 戻りは応答（読めなければ nil）と、読めなかった理由（注意に添える短い語）と、打ち切りで読めなかったか。
func fetchSummary(ctx context.Context, ev hookio.Event, e *Env, r, sid string, limit time.Duration) (map[string]any, string, bool) {
	lang := e.lang()
	name, pre, ok := issueCLI(e, r)
	if !ok {
		return nil, i18n.T(lang, "loop.handoff.digest.why.no_cli"), false
	}
	c, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, rc, err := e.run(c, e.Getwd(), []string{"CLAUDE_PROJECT_DIR=" + r, "LOOPTRACK_SESSION_ID=" + sid},
		name, append(pre, "summary", "--limit", "0", "--json")...)
	switch {
	case c.Err() != nil:
		return nil, i18n.T(lang, "loop.handoff.digest.why.timeout", "limit", limit.Round(100*time.Millisecond).String()), true
	case err != nil:
		return nil, i18n.T(lang, "loop.handoff.digest.why.start", "reason", err.Error()), false
	case rc != 0:
		return nil, i18n.T(lang, "loop.handoff.digest.why.exit", "code", rc), false
	}
	var doc map[string]any
	if json.Unmarshal(out, &doc) != nil {
		return nil, i18n.T(lang, "loop.handoff.digest.why.bad_json"), false
	}
	if _, ok := doc["in_progress"].([]any); !ok {
		return nil, i18n.T(lang, "loop.handoff.digest.why.bad_json"), false
	}
	return doc, "", false
}

// checkDigests は backend issue の判定（summary を 1 回だけ取る）。pendMT は完了のマーカーの時刻。
func checkDigests(ctx context.Context, ev hookio.Event, e *Env, r string, pendMT time.Time) digestCheck {
	lang := e.lang()
	sid := digestSessionID(ev, e)
	if sid == "" {
		return digestCheck{fresh: true, notice: i18n.T(lang, "loop.handoff.digest.notice.no_session")}
	}
	doc, why, _ := fetchSummary(ctx, ev, e, r, sid, digestFetchTimeout)
	if doc == nil {
		return digestCheck{fresh: true, keep: true, notice: i18n.T(lang, "loop.handoff.digest.notice.unreachable", "why", why)}
	}
	return judgeDigests(lang, doc, sid, pendMT, i18n.T(lang, "loop.handoff.digest.source.backend"))
}

// ruleDigests は、backend が issue でないときの、プロジェクト別ルール session.digest_required による判定。
// ルールが入のときだけ判定の結果を返す（ok = true）。ルールの値は summary の応答（backend issue と同じ 1 本）で知り、
// ルールのためだけの要求は足さない。summary を読めない（サーバに届かない・CLI が無い・セッション ID が無い）ときは
// ルールが分からないので何もしない（ok = false。backend の判定だけが効く。止めずに通す側に倒す）。
// 呼ぶのは完了のマーカーが積まれている Stop で、backend の判定を済ませた後だけ（マーカーが無ければ stopHandoffFreshness が先に戻る）。
//
// summary は ctx の締切（hook の持ち時間）の残りの中でだけ取る（digestBudget）。残りが足りずに取らなかったとき・
// 打ち切ったときは、unchecked にルールを確かめていない旨の注意を返す（ok = false。backend の判定だけが効く）。
func ruleDigests(ctx context.Context, ev hookio.Event, e *Env, r string, pendMT time.Time) (d digestCheck, ok bool, unchecked string) {
	sid := digestSessionID(ev, e)
	if sid == "" {
		return digestCheck{}, false, ""
	}
	if _, _, ok := issueCLI(e, r); !ok {
		return digestCheck{}, false, ""
	}
	lang := e.lang()
	limit, left, ok := digestBudget(ctx)
	if !ok {
		why := i18n.T(lang, "loop.handoff.digest.why.budget", "left", left.Round(100*time.Millisecond).String(),
			"min", digestMinBudget.String())
		return digestCheck{}, false, i18n.T(lang, "loop.handoff.digest.notice.rule_unchecked", "why", why)
	}
	doc, why, timedOut := fetchSummary(ctx, ev, e, r, sid, limit)
	if doc == nil {
		if timedOut {
			return digestCheck{}, false, i18n.T(lang, "loop.handoff.digest.notice.rule_unchecked", "why", why)
		}
		return digestCheck{}, false, ""
	}
	d = judgeDigests(lang, doc, sid, pendMT, i18n.T(lang, "loop.handoff.digest.source.rule"))
	return d, d.required, ""
}

// judgeDigests は summary の応答から判定する（サーバを呼ばない部分。テストで直接与える）。
// src は注意の文面に出す判定の出どころ（backend issue か、プロジェクト別ルール session.digest_required か）。
func judgeDigests(lang i18n.Lang, doc map[string]any, sid string, pendMT time.Time, src string) digestCheck {
	res := digestCheck{fetched: true, fresh: true}
	res.required, _ = doc["session_digest_required"].(bool)
	items, _ := doc["in_progress"].([]any)
	// マーカーは時刻を秒より細かく持つが、要約の時刻（RFC 3339）は秒までなので、秒に丸めて比べる
	// （完了と同じ秒に書いた要約を古いと見ない。freshByWriterMark と同じ扱い）
	pendSec := pendMT.Truncate(time.Second)
	var unknown []string
	for _, x := range items {
		it, _ := x.(map[string]any)
		id := toStr(it["id"])
		if id == "" {
			continue
		}
		own, _ := it["own_session"].(bool)
		other, _ := it["other_session"].(bool)
		switch {
		case other:
			continue // 別のセッションが着手したもの（そちらの要約はそちらが書く）
		case !own:
			unknown = append(unknown, id) // このセッションのものかを言えない（無いものとして扱い、注意で出す）
			continue
		}
		d, _ := it["latest_digest"].(map[string]any)
		if d == nil {
			res.stale = append(res.stale, digestStale{id, i18n.T(lang, "loop.handoff.digest.missing", "id", id)})
			continue
		}
		dOther, dKnown := d["other_session"].(bool)
		mine := (dKnown && !dOther) || toStr(d["session_id"]) == sid
		if !mine {
			res.stale = append(res.stale, digestStale{id, i18n.T(lang, "loop.handoff.digest.other", "id", id)})
			continue
		}
		at, err := time.Parse(time.RFC3339, toStr(d["created_at"]))
		if err != nil {
			res.stale = append(res.stale, digestStale{id, i18n.T(lang, "loop.handoff.digest.missing", "id", id)})
			continue
		}
		if at.Before(pendSec) {
			res.stale = append(res.stale, digestStale{id, i18n.T(lang, "loop.handoff.digest.older", "id", id,
				"time", at.Local().Format("2006-01-02 15:04:05"))})
		}
	}
	res.fresh = len(res.stale) == 0
	if len(unknown) > 0 {
		res.notice = i18n.T(lang, "loop.handoff.digest.notice.unknown", "source", src, "ids", strings.Join(unknown, ", "))
	}
	return res
}

// where は差し戻しの文の「何が足りないか」と、送るコマンド（イシューごと）。
func (d digestCheck) where(lang i18n.Lang) string {
	var lines, cmds []string
	for _, s := range d.stale {
		lines = append(lines, s.line)
		cmds = append(cmds, "  looptrack session digest "+s.id+" -")
	}
	return i18n.T(lang, "loop.handoff.where.digest", "list", strings.Join(lines, "\n"), "commands", strings.Join(cmds, "\n"))
}
