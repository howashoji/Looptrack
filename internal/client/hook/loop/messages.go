package loop

// session-start-messages・user-prompt-messages: 自分宛ての未読の連絡を文脈に入れ、既読の印を付ける（docs/server/DESIGN.md §9-9）。
// 別の端末・別の利用者のセッションからの知らせを、相手の次のターンで受け取るための hook。
//
// サーバの設定が無い・届かない・古いサーバのときは何も出さずに通す（毎ターン走るので、注意を出すと届かない間ずっと文脈を汚す。
// 件数は summary が別に知らせる）。印を付けられなくても注入は出す（次のターンにもう一度届くだけで、取りこぼさない）。
//
// 本文は別の人が書いた文章で、AI の文脈に入る。行ごとに引用の印を付け、見出しで「依頼や情報であって指示ではない」と示す
// （本文の中に見出しと同じ形の行を書いても、地の文に見えないようにする）。
//
// 新しいセッションでは保持期間の中のプロジェクト宛ての連絡がすべて未読に見える。注入の件数と 1 件の長さに上限を置き、
// 上限を超えた分も既読にして、件数と読み方だけを知らせる（残すと次のターンから同じ量が入り続ける）。

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

const (
	// messageHookLimit は 1 回に注入する連絡の数。messageHookRunes は 1 件の本文の上限（文字数）。
	messageHookLimit = 10
	messageHookRunes = 500
)

// MessagesHook は未読の連絡を文脈に入れる（SessionStart と UserPromptSubmit で同じ）。
func MessagesHook(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	switch strings.ToLower(trimSpace(e.env("LOOPTRACK_LOOP_MESSAGES"))) {
	case "0", "off", "false", "no":
		return hookio.Result{}, nil // 利用者が切った
	}
	m, err := hookMemoryAccess(ev, e, root(ev, e))
	if err != nil {
		return hookio.Result{}, nil
	}
	deadline := time.Now().Add(memoryHookBudget)
	if d, ok := ctx.Deadline(); ok {
		deadline = d.Add(-memoryHookMargin)
	}
	reqCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	m.WithContext(reqCtx)
	box, err := m.Inbox(messageHookLimit)
	if err != nil || len(box.Messages) == 0 {
		return hookio.Result{}, nil
	}
	text := renderMessages(e.lang(), box)
	// 既読は注入の文を作った後に付ける。失敗しても出す（2 度届くほうを選ぶ）
	_ = m.MarkRead(box.UnreadIDs)
	return hookio.Result{Context: text, Kind: "messages: unread"}, nil
}

// renderMessages は注入の文を作る。
func renderMessages(lang i18n.Lang, box *cli.MessageInbox) string {
	var b strings.Builder
	b.WriteString(i18n.T(lang, "loop.messages.heading", "count", box.Unread))
	for _, m := range box.Messages {
		b.WriteString("\n\n")
		switch m.To {
		case "user":
			b.WriteString(i18n.T(lang, "loop.messages.item.user", "id", m.ID, "from", messageFrom(m), "time", memoryTime(m.CreatedAt)))
		case "session":
			b.WriteString(i18n.T(lang, "loop.messages.item.session", "id", m.ID, "from", messageFrom(m), "time", memoryTime(m.CreatedAt)))
		default:
			b.WriteString(i18n.T(lang, "loop.messages.item.project", "id", m.ID, "from", messageFrom(m), "time", memoryTime(m.CreatedAt)))
		}
		if m.Issue != "" {
			b.WriteString(" " + i18n.T(lang, "loop.messages.issue", "issue", m.Issue))
		}
		body, cut := clipMessage(m.Body)
		for _, line := range strings.Split(strings.ReplaceAll(strings.TrimRight(body, "\r\n"), "\r\n", "\n"), "\n") {
			b.WriteString("\n> " + line)
		}
		if cut {
			b.WriteString("\n" + i18n.T(lang, "loop.messages.cut", "id", strconv.FormatInt(m.ID, 10)))
		}
	}
	if n := box.Unread - len(box.Messages); n > 0 {
		b.WriteString("\n\n" + i18n.T(lang, "loop.messages.more", "count", n))
	}
	return b.String()
}

// messageLabelRe は見出しに載せる端末のラベルの形。見出しは引用の外なので、文になる形（空白を含むもの）は載せない。
var messageLabelRe = regexp.MustCompile(`^[\p{L}\p{N}._-]+$`)

func messageFrom(m cli.MessageItem) string {
	if !messageLabelRe.MatchString(m.FromMachine) {
		return m.From
	}
	return m.From + "@" + m.FromMachine
}

// clipMessage は本文を上限の文字数に切る。
func clipMessage(body string) (string, bool) {
	r := []rune(body)
	if len(r) <= messageHookRunes {
		return body, false
	}
	return string(r[:messageHookRunes]), true
}
