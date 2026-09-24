// Package usagesnap は、コーディング AI の会話記録から「会話の累計」のスナップショット（POST /projects/{slug}/usage の本文）を作る
// （DESIGN.md §5-4・§5-11）。
//
// サーバ側の集計（internal/usage）と名前が重ならないよう usagesnap とした。設計は docs/server/DESIGN.md §5-4・§5-11。
//
//   - claude-code … ~/.claude/projects/<slug>/<session>.jsonl の assistant 行の usage（同じ message.id の複数行は 1 回。
//     サブエージェントは <session>/subagents/*.jsonl）
//   - codex       … ~/.codex/sessions/年/月/日/rollout-*.jsonl の token_count（同じスレッドの分割ファイルを古い順に足す・
//     複製を二度数えない）。人の指示は旧形式 user_message と新形式 item_completed（UserMessage）の両方
//   - copilot     … OpenTelemetry のファイル出力（JSON Lines）の chat / invoke_agent
//
// **以前の CLI（1.0.0 より前）と payload が完全に一致すること**が受け入れ条件（ずれるとサーバで二重計上・inconsistent になる）。
// 数の丸め・時刻の読み方・キーの順は以前の CLI に合わせてある（legacycompat.go）。差分テストは usagesnap_test.go・
// sendprompts_test.go（移したケース）・fuzz_diff_test.go（部品と乱数の会話記録）で、比べる相手は撤去の前に記録した結果（golden_test.go）。
// 以前の CLI が読めずに落ちる入力（行が JSON のオブジェクトでない・message が文字列など）は、Go 版は読み飛ばす。
// 設定の環境変数（LOOPTRACK_USAGE_SEND_PROMPTS・LOOPTRACK_USAGE_COPILOT_OTEL・LOOPTRACK_API_URL・LOOPTRACK_PROJECT）は internal/client/env の規則で LOOPTRACK_* も読む。
// 作業名を送るかの判定と応答の send_prompts を覚える処理は sendprompts.go。
//
// CLI（usage attach・変更操作の後の送信）と hook（usage_hook）への配線は T4・T9 で行う。
package usagesnap

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// RecentSec は作業ディレクトリから会話記録を探すときの「直近」（秒）。
const RecentSec = 10 * 60

// Options は読み方の設定。ゼロ値は区間なし・環境変数なし・HOME は OS の値・現在時刻は time.Now。
type Options struct {
	// WithSegments は区間（segments）を付けるか。
	WithSegments bool
	// Env は環境変数（LOOPTRACK_USAGE_SEND_PROMPTS・CLAUDE_CODE_SESSION_ID・CODEX_THREAD_ID・Copilot の OTel の場所など）。
	Env env.Env
	// Home は ~ の展開先。空なら Env の HOME、それも無ければ os.UserHomeDir。
	Home string
	// Now は現在時刻（会話記録に時刻が無いときの at・Codex の「直近」の判定）。
	Now func() time.Time
}

func (o Options) home() string {
	if o.Home != "" {
		return o.Home
	}
	if h := o.Env.Get("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// expandUser は先頭の ~ をホームに置き換える（~ と ~/… だけ）。
func (o Options) expandUser(p string) string {
	if p == "~" {
		return o.home()
	}
	if len(p) >= 2 && p[0] == '~' && (p[1] == '/' || p[1] == os.PathSeparator) {
		return joinPath(o.home(), p[2:])
	}
	return p
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// sendPrompts は作業名（指示の先頭 44 文字）を区間に付けるか（プロジェクト別ルールと LOOPTRACK_USAGE_SEND_PROMPTS・sendprompts.go）。
func (o Options) sendPrompts() bool { return SendPromptsAllowed(o) }

// ---------------------------------------------------------------- 共通の形

// tokens は 4 種（input・cache_create・cache_read・output の順）。
type tokens [4]int64

func (t tokens) object() *jsonorder.Object {
	o := jsonorder.NewObject()
	o.Set("input", t[0])
	o.Set("cache_create", t[1])
	o.Set("cache_read", t[2])
	o.Set("output", t[3])
	return o
}

func (t tokens) sum() int64 { return t[0] + t[1] + t[2] + t[3] }

// segment は人の指示ごとの区間。
type segment struct {
	label, text, kind string
	start, end        string // 会話記録の時刻の字面（読めない・無いときは ""）
	main, sub         tokens
	responses, subRes int64
	auto              bool
	idle              *float64
	work              float64
	codexRaw          *[5]int64
}

func newSeg(text, ts, kind string) *segment {
	return &segment{label: label(text), text: text, kind: kind, start: ts, end: ts}
}

// ConversationID は最初の人の指示の時刻（現地・秒）+ 内容の MD5 先頭 6 桁。指示が無ければセッション ID の先頭 8 文字。
// 再開でセッション ID が変わっても同じになる。
func ConversationID(firstHumanTS string, firstHumanText *string, sessionID string) string {
	if t, ok := isoTime(firstHumanTS); ok && firstHumanText != nil {
		sum := md5.Sum([]byte(label(*firstHumanText)))
		return t.In(time.Local).Format("20060102150405") + "-" + hex.EncodeToString(sum[:])[:6]
	}
	if sessionID == "" {
		sessionID = "unknown"
	}
	return runePrefix(sessionID, 8)
}

// humanMetrics は区間の並びから待ち（idle）・所要（work）・自動再開・割込の指標を出す（_human_metrics）。
func humanMetrics(segs []*segment) *jsonorder.Object {
	var prevEnd time.Time
	havePrev := false
	var idles, works []float64
	longBreak := int64(0)
	for _, s := range segs {
		st, stOK := isoTime(s.start)
		en, enOK := isoTime(s.end)
		s.auto = autoRE.MatchString(s.label)
		s.idle = nil
		if havePrev && stOK {
			d0 := seconds(prevEnd, st)
			if d0 >= 0 {
				v := round1(d0 / 60)
				s.idle = &v
			}
		}
		s.work = 0.0
		if stOK && enOK && en.After(st) {
			s.work = round1(seconds(st, en) / 60)
		}
		if s.kind != "start" {
			if !s.auto && s.idle != nil {
				idles = append(idles, *s.idle)
				if *s.idle >= 120 {
					longBreak++
				}
			}
		}
		if s.work > 0 {
			works = append(works, s.work)
		}
		switch {
		case enOK:
			prevEnd, havePrev = en, true
		case stOK:
			prevEnd, havePrev = st, true
		}
	}
	var pure, auto, intr int64
	for _, s := range segs {
		if s.kind == "start" {
			continue
		}
		if !s.auto && s.kind != "intr" {
			pure++
		}
		if s.auto {
			auto++
		}
		if s.kind == "intr" {
			intr++
		}
	}
	h := jsonorder.NewObject()
	h.Set("pure", pure)
	h.Set("auto", auto)
	h.Set("intr", intr)
	if len(idles) > 0 {
		h.Set("idle_median_m", round1(median(idles)))
		lt5 := 0
		for _, v := range idles {
			if v < 5 {
				lt5++
			}
		}
		h.Set("idle_lt5_pct", round0(100*float64(lt5)/float64(len(idles))))
	} else {
		h.Set("idle_median_m", nil)
		h.Set("idle_lt5_pct", nil)
	}
	h.Set("long_breaks", longBreak)
	total := 0.0
	for _, w := range works {
		total += w
	}
	if len(works) > 0 {
		h.Set("work_m", round1(total))
	} else {
		h.Set("work_m", 0.0)
	}
	return h
}

// finishArgs は _finish の引数。
type finishArgs struct {
	client, version, sessionID string
	segments                   []*segment
	byModel                    *jsonorder.Object // nil なら null
	io                         *jsonorder.Object // nil なら null
	branch                     string
	branches                   []string // nil なら null
	cwdName                    string
	firstTS, lastTS            string
	excluded                   bool
}

func finish(a finishArgs, o Options) *jsonorder.Object {
	var main, sub tokens
	var responses, subResponses int64
	for _, s := range a.segments {
		for k := range main {
			main[k] += s.main[k]
			sub[k] += s.sub[k]
		}
		responses += s.responses
		subResponses += s.subRes
	}
	human := humanMetrics(a.segments)
	var firstHuman *segment
	for _, s := range a.segments {
		if s.kind != "start" {
			firstHuman = s
			break
		}
	}
	var convID string
	if firstHuman != nil {
		text := firstHuman.text
		convID = ConversationID(firstHuman.start, &text, a.sessionID)
	} else {
		convID = ConversationID("", nil, a.sessionID)
	}
	at, ok := isoTime(a.lastTS)
	if !ok {
		at, ok = isoTime(a.firstTS)
	}
	if !ok {
		at = truncUS(o.now())
	}
	p := jsonorder.NewObject()
	p.Set("client", a.client)
	p.Set("client_version", a.version)
	p.Set("session_id", a.sessionID)
	p.Set("conversation_id", convID)
	p.Set("at", utcString(at))
	tk := jsonorder.NewObject()
	tk.Set("main", main.object())
	tk.Set("sub", sub.object())
	p.Set("tokens", tk)
	p.Set("responses", responses)
	p.Set("sub_responses", subResponses)
	p.Set("by_model", objOrNil(a.byModel))
	p.Set("io", objOrNil(a.io))
	p.Set("human", human)
	p.Set("branch", a.branch)
	if len(a.branches) > 0 {
		p.Set("branches", append([]string(nil), a.branches...))
	} else {
		p.Set("branches", nil)
	}
	p.Set("cwd_name", a.cwdName)
	p.Set("excluded", a.excluded)
	if o.WithSegments {
		send := o.sendPrompts()
		rows := []any{}
		for _, s := range a.segments {
			row := jsonorder.NewObject()
			if t, ok := isoTime(s.start); ok {
				row.Set("start", utcString(t))
			} else {
				row.Set("start", nil)
			}
			kind := s.kind
			if s.auto {
				kind = "auto"
			}
			row.Set("kind", kind)
			if s.idle != nil {
				row.Set("idle_m", *s.idle)
			} else {
				row.Set("idle_m", nil)
			}
			row.Set("work_m", s.work)
			row.Set("responses", s.responses)
			row.Set("sub_responses", s.subRes)
			row.Set("main", s.main.sum())
			row.Set("sub", s.sub.sum())
			if send {
				row.Set("label", s.label)
			}
			rows = append(rows, row)
		}
		p.Set("segments", rows)
	}
	return p
}

func objOrNil(o *jsonorder.Object) any {
	if o == nil || len(o.Members) == 0 {
		return nil
	}
	return o
}

// ---------------------------------------------------------------- 入口

// Client の名前（サーバの client 列）。
const (
	ClientClaudeCode = "claude-code"
	ClientCodex      = "codex"
	ClientCopilot    = "copilot"
)

// Detect は環境変数と作業ディレクトリから (client, transcriptPath, sessionID) を返す。分からなければすべて ""。
// Copilot CLI のシェル（COPILOT_CLI=1 と COPILOT_AGENT_SESSION_ID）では (copilot, "", セッション ID)。会話記録ではなく OTel のファイル出力を読む。
func Detect(cwd string, o Options) (client, transcriptPath, sessionID string) {
	sid := o.Env.Get("CLAUDE_CODE_SESSION_ID")
	if sid == "" {
		sid = o.Env.Get("CLAUDE_SESSION_ID")
	}
	if sid != "" {
		if p := claudeFindTranscript(sid, o); p != "" {
			return ClientClaudeCode, p, sid
		}
	}
	// Codex の会話記録のファイル名は rollout-<時刻>-<スレッド ID>.jsonl。CODEX_THREAD_ID が一致する
	// （CODEX_SESSION_ID は親の会話の ID で、サブエージェントでは一致しない）
	for _, name := range []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID"} {
		if v := o.Env.Get(name); v != "" {
			if p := codexFindTranscript(cwd, v, o); p != "" {
				return ClientCodex, p, v
			}
		}
	}
	// Copilot CLI はエージェントが打つシェルコマンドに COPILOT_AGENT_SESSION_ID（hook の session_id・OTel の gen_ai.conversation.id と
	// 同じ値）と COPILOT_CLI=1 を渡す（1.0.86 の実物で確認）。組み合わせたときだけ読む（internal/client/session と同じ判定）。
	// 作業ディレクトリで Codex の会話記録を推測する前に見る（同じディレクトリの Codex の記録を Copilot の操作に付けない）。
	// 以前の CLI（1.0.0 より前・凍結。撤去済み）の detect はこの分岐と下の VS Code の除外を持たない（Go 版だけの差分）。
	if o.Env.Get("COPILOT_CLI") == "1" {
		if sid := strings.TrimSpace(o.Env.Get("COPILOT_AGENT_SESSION_ID")); sid != "" {
			return ClientCopilot, "", sid
		}
	}
	// VS Code の Copilot のエージェント用ターミナル（セッション ID は渡らない）でも Codex を推測しない（付けられるものが無い）
	copilotVSCode := o.Env.Get("AI_AGENT") == "github_copilot_vscode_agent" || o.Env.Get("COPILOT_AGENT") == "1"
	if cwd != "" && o.Env.Get("CLAUDECODE") == "" && !copilotVSCode {
		if p := codexFindTranscript(cwd, "", o); p != "" {
			return ClientCodex, p, ""
		}
	}
	return "", "", ""
}

// Collect はスナップショットを作る。client が空なら Detect で探す。
// どの AI の下でもない・会話記録が読めない・数えるものが無いときは nil（送らない）。
// error は Claude Code の会話記録を開けなかったときだけ（以前の CLI（1.0.0 より前）では例外になっていた）。
func Collect(client, transcriptPath, sessionID, cwd string, o Options) (*jsonorder.Object, error) {
	if client == "" {
		client, transcriptPath, sessionID = Detect(cwd, o)
	}
	if client == ClientCopilot {
		// 会話記録ではなく OpenTelemetry のファイル出力を読む。セッション ID は hook の入力から
		return CollectCopilot(sessionID, nil, cwd, o), nil
	}
	if client == "" || transcriptPath == "" || !isFile(transcriptPath) {
		return nil, nil
	}
	switch client {
	case ClientClaudeCode:
		if sessionID == "" {
			b := baseName(transcriptPath)
			sessionID = b[:max(len(b)-len(".jsonl"), 0)]
		}
		return CollectClaudeCode(transcriptPath, sessionID, o)
	case ClientCodex:
		return CollectCodex(transcriptPath, sessionID, o), nil
	}
	return nil, nil
}
