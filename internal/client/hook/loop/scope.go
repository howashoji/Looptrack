package loop

// session-scope-guard（kit/loop/hooks/session-scope-guard.sh の Go 版）: 文脈の大きさの警告。
//
// 会話記録の直近の応答のコンテキストが警告の閾値（既定 25 万。LOOPTRACK_LOOP_SCOPE_WARN）を超えたら警告する
// （10 万増えるごとに再警告）。区切りで引き継ぎを書いてセッションを分けることを勧めるだけで、**何も止めない**。
//
// かつてここには「1 課題 = 1 セッション」の束縛（最初に着手したイシュー ID にセッションを束縛し、別の ID への着手・
// 一括の着手を差し戻す）もあった。利用者の指示（2026-09-20）で機能ごと削除した:
// 「やはり 1 課題 1 セッションの hook は作業の邪魔なので廃止したい」。
// 同じ kit の rules/working-discipline.md は並行セッションとサブエージェントへの委譲（複数の課題を同時に進めること）を
// 前提にしていて、取りまとめ役のセッションは本質的に多数の課題にまたがるので、束縛は構造的に噛み合わなかった。
// ガードは実測の価値で残すかを決める（言行一致の Stop hook の前例）。
//
// 状態: <状態>/session-scope/<session_id>（WARNED=<最後に警告したときのコンテキスト>）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// SessionScopeGuard は文脈の大きさを警告する（UserPromptSubmit）。何も止めない。
func SessionScopeGuard(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	prompt := ev.Prompt
	if prompt == "" || notificationRe.MatchString(prompt) {
		return hookio.Result{}, nil
	}
	scopeDir := filepath.Join(stateDir(ev, e), "session-scope")
	warnAt := 250000
	if v := trimSpace(e.env("LOOPTRACK_LOOP_SCOPE_WARN")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			warnAt = n
		}
	}

	// 直近の応答のコンテキスト（末尾 5MB だけ読む）
	cx := transcriptContext(rawTranscript(ev))
	// 日本語は「万」、英語は「k（千）」で数える。単位が言語で違うので両方渡し、対訳表がどちらかを使う。
	man, kilo := cx/10000, cx/1000

	sid := sessionFile(ev)
	if sid == "" {
		sid = "unknown"
	}
	path := filepath.Join(scopeDir, sid)
	warned := 0
	if t, err := os.ReadFile(path); err == nil {
		for _, line := range textLines(decodeReplace(t)) {
			k, v, _ := strings.Cut(trimSpace(line), "=")
			if k != "WARNED" {
				continue
			}
			if v == "" {
				warned = 0
				continue
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				break // 以前の hook もここで読むのをやめた
			}
			warned = n
		}
	}

	if cx > warnAt && cx-warned > 100000 {
		if os.MkdirAll(scopeDir, 0o777) == nil {
			_ = os.WriteFile(path, []byte(fmt.Sprintf("WARNED=%d\n", cx)), 0o666)
		}
		return hookio.Result{Context: i18n.T(e.lang(), "loop.scope.warn",
			"man", man, "k", kilo, "limit_man", floorDiv(warnAt, 10000), "limit_k", floorDiv(warnAt, 1000))}, nil
	}
	return hookio.Result{}, nil
}

// floorDiv は負の数を -∞ 側に丸める整数の割り算。
func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// rawTranscript は入力の transcript_path（bash 版と同じく入力の値だけを見る）。
func rawTranscript(ev hookio.Event) string {
	if s := toStr(ev.Raw["transcript_path"]); s != "" {
		return s
	}
	return toStr(ev.Raw["transcriptPath"])
}

// transcriptContext は会話記録の末尾 5MB から、サブエージェントでない最後の応答のコンテキスト
// （cache_read + input + cache_creation）を返す（読めなければ 0）。
func transcriptContext(tp string) int {
	if tp == "" || !isFile(tp) {
		return 0
	}
	f, err := os.Open(tp)
	if err != nil {
		return 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	off := st.Size() - 5000000
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return 0
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return 0
	}
	ctxTokens := 0.0
	for _, line := range splitlines(decodeReplace(buf)) {
		line = trimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var o map[string]any
		if json.Unmarshal([]byte(line), &o) != nil {
			continue
		}
		if o["type"] != "assistant" || truthy(o["isSidechain"]) {
			continue
		}
		msg, _ := o["message"].(map[string]any)
		u, _ := msg["usage"].(map[string]any)
		c := numPy(u["cache_read_input_tokens"]) + numPy(u["input_tokens"]) + numPy(u["cache_creation_input_tokens"])
		if c != 0 {
			ctxTokens = c
		}
	}
	return int(ctxTokens)
}

func numPy(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// truthy は値の真偽（空でない文字列・0 でない数・true・空でない配列やオブジェクトが真）。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}
