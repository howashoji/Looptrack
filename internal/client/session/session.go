// Package session は、シェルから打った looptrack issue を動かしている AI のセッション ID と種類を環境変数から決める
// （以前の CLI（1.0.0 より前）と同じ順・同じ判定）。
//
//   - Claude Code が Bash に渡すのは CLAUDE_CODE_SESSION_ID（2.1 系で確認。CLAUDE_SESSION_ID は予備）。
//     器（デスクトップ版のセッションの窓）によっては CLAUDE_CODE_SESSION_ID が渡らず、CLAUDE_CODE_HOST_SESSION_ID
//     （その窓を通して一定の ID）だけが渡ることがある。並行して動くセッションを見分けるには、これでも足りるので最後に読む
//     （会話記録のファイル名とは一致しないので、トークン情報を引くのには使えない。usagesnap.Detect は読まない）。
//     この ID を送るときは X-Looptrack-Session-Kind: host も送り、サーバがトークン情報の付与の対象から外せるようにする。
//   - Codex がシェルに渡すのは CODEX_THREAD_ID（会話記録の session_meta.id と同じ）と、新しい版では CODEX_SESSION_ID
//     （サブエージェントも共有する親の会話の ID）。会話記録と突き合わせる THREAD を先に読む。
//   - Copilot CLI はエージェントが実行するシェルコマンドに COPILOT_AGENT_SESSION_ID と COPILOT_CLI=1 を渡す。
//     COPILOT_CLI=1 と組み合わせたときだけ読む。
//   - VS Code の Copilot はセッション ID を渡さないが、エージェント用のターミナルにだけ AI_AGENT=github_copilot_vscode_agent と
//     COPILOT_AGENT=1 を付ける。セッション ID なしの AI の操作（X-Looptrack-Agent: copilot）として送る。
//
// 推測（状態ファイル・作業ディレクトリ）では読まない（DESIGN.md §5-4「Copilot のセッション ID」）。
package session

import (
	"strings"

	"github.com/howashoji/looptrack/internal/client/env"
)

// MaxSessionLen は X-Looptrack-Session に入れる長さの上限（文字数。以前の CLI と同じ）。
const MaxSessionLen = 128

// KindHost は、セッション ID が器（デスクトップ版のセッションの窓）の ID で、会話記録と結び付かないことを表す印
// （X-Looptrack-Session-Kind の値）。並行するセッションは見分けられるが、トークン情報は引けない
// （usagesnap.Detect はこの環境変数を読まない）ので、サーバはこの印が付いた操作を付与の対象から外す。
// 印を送らない古い CLI・読まない古いサーバでは、今までどおり付与の対象になるだけで、要求は失敗しない。
const KindHost = "host"

// Mark は API の要求に付ける AI の操作の印。
type Mark struct {
	Session string // X-Looptrack-Session（空なら送らない）
	Agent   string // X-Looptrack-Agent（サーバが clientInfo から判定できない AI だけ。今は copilot だけ。空なら送らない）
	Kind    string // X-Looptrack-Session-Kind（今は host だけ。空＝会話のセッション ID で、トークン情報を引ける）
}

// Detect は環境変数から印を決める。
func Detect(e env.Env) Mark {
	explicit := e.Value(env.SessionID) // LOOPTRACK_SESSION_ID
	aiSession, aiName := first(e, "CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "CLAUDE_CODE_HOST_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID")
	session := explicit
	if session == "" {
		session = aiSession
	}
	copilotCLI := ""
	if e.Get("COPILOT_CLI") == "1" {
		copilotCLI = strings.TrimSpace(e.Get("COPILOT_AGENT_SESSION_ID"))
	}
	copilotVSCode := e.Get("AI_AGENT") == "github_copilot_vscode_agent" || e.Get("COPILOT_AGENT") == "1"
	if session != "" {
		// 明示の LOOPTRACK_SESSION_ID を Copilot の下で前置したときも、Copilot の操作と分かるようにする
		agent := ""
		if explicit != "" && aiSession == "" && (copilotCLI != "" || copilotVSCode) {
			agent = "copilot"
		}
		// 器の ID を使うときだけ印を付ける（明示の LOOPTRACK_SESSION_ID が勝ったときは、利用者が選んだ値なので付けない）
		kind := ""
		if explicit == "" && aiName == "CLAUDE_CODE_HOST_SESSION_ID" {
			kind = KindHost
		}
		return Mark{Session: session, Agent: agent, Kind: kind}
	}
	if copilotCLI != "" {
		return Mark{Session: copilotCLI, Agent: "copilot"}
	}
	if copilotVSCode {
		return Mark{Agent: "copilot"}
	}
	return Mark{}
}

// first は names の順に最初に値のある環境変数を返す（値と、その環境変数の名前）。
func first(e env.Env, names ...string) (string, string) {
	for _, n := range names {
		if v := e.Get(n); v != "" {
			return v, n
		}
	}
	return "", ""
}

// IsAI は AI の下で動いているか（セッション ID か AI の種類が分かる）。
func (m Mark) IsAI() bool { return m.Session != "" || m.Agent != "" }

// Headers は要求に付けるヘッダ（X-Looptrack-Session は 128 文字で切る）。
func (m Mark) Headers() map[string]string {
	h := map[string]string{}
	if m.Session != "" {
		s := m.Session
		if r := []rune(s); len(r) > MaxSessionLen {
			s = string(r[:MaxSessionLen])
		}
		h["X-Looptrack-Session"] = s
	}
	if m.Agent != "" {
		h["X-Looptrack-Agent"] = m.Agent
	}
	if m.Session != "" && m.Kind != "" {
		h["X-Looptrack-Session-Kind"] = m.Kind
	}
	return h
}
