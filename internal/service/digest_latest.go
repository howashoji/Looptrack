package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/store"
)

// 最新のセッション要約（DESIGN.md §9-7「summary と next に載せる」）。summary（in_progress のイシューごと）と
// next（返すイシュー）の応答に載せ、別のセッションが書いたものだけを SessionStart・CLI・MCP の文面に出す。
// 「別のセッションか」の判定は着手の判定（next.go・markOtherSession）と同じ ComparableSessions と StarterSession を使う。

// DigestExcerptLines・DigestExcerptChars は抜粋の上限（印の行と見出しの項目を除いた、空でない先頭の行数と、全体の文字数）。
const (
	DigestExcerptLines = 8
	DigestExcerptChars = 400
)

// LatestDigest はイシューの最新のセッション要約。
type LatestDigest struct {
	// Excerpt は印の行（1 行目）と見出しの項目（branch・head・ai）を除いた、空でない先頭の DigestExcerptLines 行で、DigestExcerptChars 文字まで
	// （切ったときは末尾に「…」）
	Excerpt string
	// MachineLabel は 1 行目の端末のラベル
	MachineLabel string
	CreatedAt    time.Time
	// SessionID は要約を書いたセッション（記録の session_id。MCP の要約を会話に結んだものなら、見る側に合わせて結んだ値）
	SessionID string
	// OtherSession は、要求のセッションと比べられるときだけ値を持つ（true = 別のセッション・false = 同じセッション）。
	// 比べられない（どちらかが空・CLI のセッション ID と MCP の接続 ID）なら nil
	OtherSession *bool
}

// Other は別のセッションが書いた要約か（比べられないものは false。表示はこれが true のものだけ）。
func (d *LatestDigest) Other() bool { return d != nil && d.OtherSession != nil && *d.OtherSession }

// LatestDigests は、表示 ID の一覧のイシューごとに最新のセッション要約を返す（要約が無いイシューは含めない）。
// a は要求の主体で、a.SessionID と要約を書いたセッションを比べて OtherSession を決める。
func (s *Service) LatestDigests(ctx context.Context, a Actor, p store.Project, ids []string) (map[string]*LatestDigest, error) {
	evs, err := store.LatestDigests(ctx, s.DB, p.ID, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]*LatestDigest{}
	for _, ev := range evs {
		out[ev.DisplayID] = latestDigestOf(a, ev)
	}
	return out, nil
}

// latestDigestOf は記録 1 件を応答の形にする（判定の 1 か所）。
func latestDigestOf(a Actor, ev store.DigestEvent) *LatestDigest {
	label, excerpt := DigestExcerpt(ev.Head)
	sess := StarterSession(a, store.Starter{SessionID: ev.SessionID, LinkedSessionID: ev.LinkedSessionID})
	d := &LatestDigest{Excerpt: excerpt, MachineLabel: label, CreatedAt: ev.At, SessionID: sess}
	if ComparableSessions(a.SessionID, sess) {
		other := sess != a.SessionID
		d.OtherSession = &other
	}
	return d
}

// DigestExcerpt は要約の本文から端末のラベル（1 行目の印の後ろ）と抜粋を作る。
func DigestExcerpt(content string) (label, excerpt string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	_, rest := domain.LeadWordRest(lines[0])
	label = strings.TrimSpace(rest)
	var kept []string
	for i, l := range lines[1:] {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" { // 空行は数えない（SessionStart の注入文を詰める）
			continue
		}
		// 見出しの項目（branch・head・ai）は抜粋に入れず、数えもしない。見出しの読み方は検査と同じ
		// （domain.DigestHeaderField を先頭の domain.DigestHeaderLines 行の中でだけ当てる）。
		// 数えると見出しだけで 3 行を使い、本文の末尾（次の一手など）が切れる
		if i+1 < domain.DigestHeaderLines {
			if _, _, ok := domain.DigestHeaderField(l); ok {
				continue
			}
		}
		if len(kept) == DigestExcerptLines {
			break
		}
		kept = append(kept, l)
	}
	excerpt = strings.Join(kept, "\n")
	if utf8.RuneCountInString(excerpt) > DigestExcerptChars {
		r := []rune(excerpt)
		excerpt = strings.TrimRight(string(r[:DigestExcerptChars]), " \t\n") + "…"
	}
	return label, excerpt
}
