package cli

// hook の自動送信（DESIGN.md §9-8「hook」）が呼ぶ push の入口。中身は手で打つ
// `looptrack session transcript push <セッション ID> --agent <AI>` と同じ cmdTranscriptPush で、ここは文脈を作って呼び、
// 結果を hook が扱う種類（送った・ルールが切・変換の止め・書き換えられた・拒否・見つからない・後で送り直す）に分けるだけ。
// 変換・送り方・送った続きの判断を hook の側に写さない（写すと、手で打つ push と hook で送るものが食い違う）。
// 出力（件数・注意）は捨てる。hook は切り離した子プロセスで動き、出しても誰も読まない。

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

// HookPushOutcome は hook から呼んだ push の結果の種類。
type HookPushOutcome int

const (
	// HookPushSent は送った（送るものが無かったときを含む）。
	HookPushSent HookPushOutcome = iota
	// HookPushRuleOff はプロジェクトのルール session.transcripts が切（422 transcript_rule_off。切は覚えた）。
	HookPushRuleOff
	// HookPushRejected は変換が止めた（読めない行・キーの秘密・長すぎる行）か、サーバの値ごとの検査が止めた（422 transcript_secret・
	// transcript_unreadable）。同じ記録は何度試しても同じ結果になる。
	HookPushRejected
	// HookPushRewritten は送った後に記録が書き換えられたファイルがある（ほかのファイルは送った。purge するまで続きを送れない）。
	HookPushRewritten
	// HookPushRefused はサーバが 4xx で拒んだ（再送しても直らない）。
	HookPushRefused
	// HookPushNotFound は記録が見つからない。
	HookPushNotFound
	// HookPushRetry は届かない・5xx・ログインしていない・そのほか送る前の失敗（後で送り直す）。
	HookPushRetry
)

// HookPushResult は hook から呼んだ push の結果。値（中身・パス）は持たない。
type HookPushResult struct {
	Outcome HookPushOutcome
	// Status は HookPushRefused・HookPushRetry（5xx）のときの HTTP の状態（ほかは 0）。
	Status int
	// Files は HookPushRejected のとき、変換が止めたファイルの数。
	Files int
	// ProxyLimit は前段のリバースプロキシが本文の大きさの上限で止めた（JSON でない 413。HookPushRefused のとき）。
	ProxyLimit bool
	// NoToken はログインしていない（HookPushRetry のとき）。
	NoToken bool
	// Conn はサーバに届かない（通信断・名前解決・TLS・時間切れ。HookPushRetry のとき）。
	Conn bool
	// Err は元の誤り（デバッグの表示用。利用者には件数と理由のキーだけを見せる）。
	Err error
}

// PushTranscriptForHook は agent のセッション sid の会話記録を、手で打つ push と同じ本体で送る。
// e は hook の環境（LOOPTRACK_API_URL・LOOPTRACK_PROJECT・資格情報の置き場）。記録を探す作業ディレクトリは
// 呼ぶ側のプロセスの作業ディレクトリ（切り離した子プロセスは hook の入力の cwd で起動する）。
func PushTranscriptForHook(e env.Env, agent, sid string) HookPushResult {
	lang := i18n.FromEnv(e.Get)
	root := e.Get("CLAUDE_PROJECT_DIR")
	if root == "" {
		root, _ = os.Getwd()
	}
	fixLocalZone(e)
	api.SetRoot(root)
	c := &Ctx{IO: IO{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}, Env: e, Root: root, Lang: lang,
		Args: []string{"transcript", "push", sid, "--agent", agent}}
	v := &Values{m: map[string]any{"session": sid, "agent": agent, "current": false, "dry_run": false},
		Path: []string{"transcript", "push"}}
	return classifyHookPush(cmdTranscriptPush(c, v))
}

// classifyHookPush は cmdTranscriptPush の誤りを、hook が扱う種類に分ける。
func classifyHookPush(err error) HookPushResult {
	if err == nil {
		return HookPushResult{Outcome: HookPushSent}
	}
	r := HookPushResult{Err: err}
	var ex *Exit
	var nt *api.NoTokenError
	var ce *api.ConnError
	var ae *api.Error
	var ie *i18n.Error
	switch {
	case errors.As(err, &ex):
		// 送った後の終了コード 1 は「書き換えられたファイルがある」だけ（printPushResult）
		r.Outcome = HookPushRewritten
	case errors.As(err, &nt):
		r.Outcome, r.NoToken = HookPushRetry, true
	case errors.As(err, &ce):
		r.Outcome, r.Conn = HookPushRetry, true
	case errors.As(err, &ae):
		switch {
		case ae.Code == "transcript_rule_off":
			r.Outcome = HookPushRuleOff
		case ae.Code == "transcript_secret" || ae.Code == "transcript_unreadable":
			// サーバの値ごとの検査が止めた（変換を通っても残った秘密・読めない行）。同じチャンクは何度送っても同じ結果。
			// push は最初の拒否で止まるので、止めたのはそのチャンクのファイル 1 つ
			r.Outcome, r.Status, r.Files = HookPushRejected, ae.Status, 1
		case ae.Status >= 400 && ae.Status < 500:
			r.Outcome, r.Status, r.ProxyLimit = HookPushRefused, ae.Status, ae.ProxyBodyLimit()
		default:
			r.Outcome, r.Status = HookPushRetry, ae.Status
		}
	case errors.As(err, &ie):
		switch ie.Msg.ID {
		case "cli.err.transcript.rejected", "cli.err.transcript.rejected_sent":
			r.Outcome, r.Files = HookPushRejected, kvInt(ie.Msg.KV, "count")
		case "cli.err.transcript.not_found", "cli.err.transcript.no_current":
			r.Outcome = HookPushNotFound
		case "cli.err.transcript.unsupported":
			r.Outcome, r.Status = HookPushRefused, 404 // 経路の無い古いサーバ（更新するまで送れない）
		default:
			r.Outcome = HookPushRetry
		}
	default:
		r.Outcome = HookPushRetry // 届かない・時間切れなど
	}
	return r
}

// kvInt は i18n の値の並び（名前, 値, …）から name の整数を取る（無ければ 0）。
func kvInt(kv []any, name string) int {
	for i := 0; i+1 < len(kv); i += 2 {
		if k, _ := kv[i].(string); k == name {
			if n, ok := kv[i+1].(int); ok {
				return n
			}
		}
	}
	return 0
}
