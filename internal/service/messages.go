package service

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 連絡（DESIGN.md §9-9）。規則（宛先・権限・本文の検査・既読・保持期間）はこのファイルの 1 か所に置き、REST・MCP・CLI・Web はここの関数を呼ぶ。
//
// 宛先と読める者:
//   - project: プロジェクトの参加者全員（参加していない管理者も viewer として読める）
//   - user: その利用者だけ
//   - session: そのセッションの持ち主の利用者だけ。セッション ID はクライアントが名乗る値なので、持ち主は送る側が利用者で指定し、
//     読めるかは利用者で決める（他人のセッション ID を名乗っても読めない）
//
// どの宛先も送り手は読める。送れるのは editor 以上で、viewer は読むだけ。追記のみなので取り消しは無い。
//
// 既読はセッションごとに持つ。hook は未読を取り、注入を組み立ててから既読の印を付ける。印を付けられなければ次のターンにもう一度届く
// （取りこぼすより 2 度届くほうを選ぶ）。
//
// 保持期間（message_retention_days）は読みの条件で効かせる。アプリ用の DB 利用者は DELETE を持たないので行は消えない。

// MaxMessageBytes は連絡の本文の上限（バイト）。
const MaxMessageBytes = 4 * 1024

// maxMessageSession は宛先のセッション ID の上限（文字数。X-Looptrack-Session と同じ）。
const maxMessageSession = 128

// defaultInboxLimit・maxInboxLimit は一覧の件数の既定と上限。maxUnreadIDs は未読の id を一度に返す・既読にする上限。
const (
	defaultInboxLimit = 50
	maxInboxLimit     = 200
	maxUnreadIDs      = 1000
)

// MessageIssueLead はイシューに残すコメントの先頭の語。
const MessageIssueLead = "連絡:"

// MessageSendInput は連絡の送信の入力。
type MessageSendInput struct {
	Project string
	// To は宛先の種類（project / user / session）
	To string
	// User は宛先の利用者（user・session で必須。me は自分）
	User string
	// Session は宛先のセッション（session で必須）
	Session string
	Body    string
	// Issue は添えるイシューの ID（任意）。添えるとコメントも残す
	Issue string
	// MachineLabel は送った端末のラベル
	MachineLabel string
}

// InboxQuery は受信箱の読み方。
type InboxQuery struct {
	// Unread は、このセッションに届ける未読だけを読む
	Unread bool
	// Sent は、自分が送ったものを読む（Unread と同時には使わない）
	Sent bool
	// Limit は件数（0 なら既定。上限で丸める）
	Limit int
}

// InboxResult は受信箱。
type InboxResult struct {
	Messages []store.Message
	// Unread はこのセッションの未読の件数（Limit に関わらない）
	Unread int
	// UnreadIDs はこのセッションの未読の id（Limit に関わらない。hook が既読の印を付けるのに使う）
	UnreadIDs []int64
	// CanSend は送れるか
	CanSend bool
	// User は要求した利用者の login
	User        string
	ProjectSlug string
}

func messageReader(a Actor) store.MessageReader {
	return store.MessageReader{UserID: a.UserID, Session: a.SessionID}
}

// canSendMessage は送れる役割か（editor 以上）。
func canSendMessage(role string) bool { return role == "editor" || role == "admin" }

// messageBadRune は連絡に入れられない文字か。連絡は相手の AI の文脈に入るので、行を分ける文字（引用の印を抜ける）と
// 書式の文字（向きを変える・人に見えず AI には読めるタグ文字など）も制御文字と同じに拒む。
// 書式の文字のうち絵文字の結合と語の連結に使う ZWJ・ZWNJ だけは通す。
func messageBadRune(r rune) bool {
	if r == '\u200c' || r == '\u200d' {
		return false
	}
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// checkMessageText は 1 行の欄（端末のラベル・セッション ID）の形と資格情報を確かめる。
func checkMessageText(field, v string) error {
	if !utf8.ValidString(v) || strings.ContainsFunc(v, messageBadRune) {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.message.field", "field", field))
	}
	if f, ok := domain.FindSecret(v); ok {
		return errm(Rejected, "message_secret", i18n.M("service.err.message.secret_field", "field", field, "label", f.Label))
	}
	return nil
}

// checkMessageBody は本文の形と資格情報を確かめる。理由には行番号と規則の語だけを載せ、値は載せない。
// 改行は \n と \r\n だけを、ほかに使えるのはタブだけを受け付ける（messageBadRune）。
func checkMessageBody(body string) error {
	bad := func(r rune) bool { return messageBadRune(r) && r != '\n' && r != '\t' }
	if !utf8.ValidString(body) || strings.ContainsFunc(strings.ReplaceAll(body, "\r\n", "\n"), bad) {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.message.encoding"))
	}
	if strings.TrimSpace(body) == "" {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.message.empty"))
	}
	if len(body) > MaxMessageBytes {
		return errm(Rejected, "message_too_large", i18n.M("service.err.message.too_large", "bytes", len(body), "max", MaxMessageBytes))
	}
	if f, ok := domain.FindSecret(body); ok {
		return errm(Rejected, "message_secret", i18n.M("service.err.message.secret", "line", f.Line, "label", f.Label))
	}
	return nil
}

// cleanMessageSession は宛先のセッション ID を確かめる。
func cleanMessageSession(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || utf8.RuneCountInString(v) > maxMessageSession {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.message.session", "max", maxMessageSession))
	}
	return v, checkMessageText("session", v)
}

// messageRecipient は宛先の利用者を引く。無い利用者・無効の利用者・参加していない利用者を同じ理由で拒む（利用者の有無を探られないため）。
func (s *Service) messageRecipient(ctx context.Context, me store.User, p store.Project, login string) (store.User, error) {
	login = strings.TrimSpace(login)
	if login == "me" || login == me.Login {
		return me, nil
	}
	reject := errm(Rejected, "message_recipient", i18n.M("service.err.message.recipient", "slug", p.Slug))
	if login == "" {
		return store.User{}, errm(Invalid, "invalid_argument", i18n.M("service.err.message.user_required"))
	}
	u, err := store.UserByLogin(ctx, s.DB, login)
	if errors.Is(err, store.ErrNotFound) {
		return u, reject
	}
	if err != nil {
		return u, err
	}
	if u.Disabled {
		return u, reject
	}
	// 参加者だけを見る。AccessibleProjects は管理者に全プロジェクトを返すので、管理者の login を探れてしまう
	role, err := store.MemberRole(ctx, s.DB, p.ID, u.ID)
	if err != nil {
		return u, err
	}
	if role == "" {
		return u, reject
	}
	return u, nil
}

// messageSince は読みの下限（保持期間の切れ目）を返す。無期限ならゼロ値。
func (s *Service) messageSince(ctx context.Context) (time.Time, error) {
	days, err := store.ReadMessageRetentionDays(ctx, s.DB)
	if err != nil || days == 0 {
		return time.Time{}, err
	}
	return s.Now().AddDate(0, 0, -int(days)), nil
}

// SendMessage は連絡を送る。Issue を添えたら先にコメントを残す（拒まれたら連絡も送らない。先に連絡を送ると、コメントの拒否で送り直して 2 度届くため）。
func (s *Service) SendMessage(ctx context.Context, a Actor, in MessageSendInput) (*store.Message, error) {
	to := strings.TrimSpace(in.To)
	if !slices.Contains([]string{store.MessageToProject, store.MessageToUser, store.MessageToSession}, to) {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.message.to"))
	}
	if to == store.MessageToProject && (strings.TrimSpace(in.User) != "" || strings.TrimSpace(in.Session) != "") {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.message.to_project_only"))
	}
	if to == store.MessageToUser && strings.TrimSpace(in.Session) != "" {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.message.to_user_only"))
	}
	if err := checkMessageBody(in.Body); err != nil {
		return nil, err
	}
	label, err := cleanTranscriptLabel("machine_label", in.MachineLabel)
	if err != nil {
		return nil, err
	}
	if err := checkMessageText("machine_label", label); err != nil {
		return nil, err
	}
	// 送り手のセッションも本文と同じく相手に見える（要求のヘッダの値なので、経路に関わらずここで確かめる）
	if err := checkMessageText("sender_session", a.SessionID); err != nil {
		return nil, err
	}
	session := ""
	if to == store.MessageToSession {
		if session, err = cleanMessageSession(in.Session); err != nil {
			return nil, err
		}
	}
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(in.Project))
	if err != nil {
		return nil, err
	}
	if !canSendMessage(role) {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.message.forbidden", "slug", p.Slug))
	}
	if err := checkProjectActive(ctx, s.DB, p); err != nil {
		return nil, err
	}
	m := store.Message{ProjectID: p.ID, ToKind: to, ToSession: session, SenderUserID: u.ID, SenderLogin: u.Login, SenderMachine: label,
		SenderSession: a.SessionID, Body: in.Body, TokenID: a.TokenID, Via: a.Via, CreatedAt: s.Now()}
	if m.Via == "" {
		m.Via = "api"
	}
	if to != store.MessageToProject {
		target, err := s.messageRecipient(ctx, u, p, in.User)
		if err != nil {
			return nil, err
		}
		m.ToUserID, m.ToLogin = target.ID, target.Login
	}
	if id := strings.TrimSpace(in.Issue); id != "" {
		row, err := s.Locate(ctx, id, p.ID)
		if err != nil {
			return nil, err
		}
		if _, err := s.AddComment(ctx, a, p, row.ID, CommentInput{Text: MessageIssueLead + " " + in.Body, Message: true}); err != nil {
			return nil, err
		}
		m.IssueID, m.IssueDisplay = row.ID, row.DisplayID
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectActive(ctx, tx, p); err != nil {
			return err
		}
		id, err := store.InsertMessage(ctx, tx, m)
		m.ID = id
		return err
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Inbox は受信箱を読む（既読の印は付けない）。
func (s *Service) Inbox(ctx context.Context, a Actor, slug string, q InboxQuery) (*InboxResult, error) {
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	since, err := s.messageSince(ctx)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultInboxLimit
	}
	limit = min(limit, maxInboxLimit)
	r := messageReader(a)
	res := &InboxResult{CanSend: canSendMessage(role), User: u.Login, ProjectSlug: p.Slug}
	if res.UnreadIDs, res.Unread, err = store.UnreadMessageIDs(ctx, s.DB, p.ID, r, since, maxUnreadIDs); err != nil {
		return nil, err
	}
	if q.Sent {
		res.Messages, err = store.ListSentMessages(ctx, s.DB, p.ID, r, since, limit)
	} else {
		res.Messages, err = store.ListInbox(ctx, s.DB, p.ID, r, since, q.Unread, limit)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// UnreadMessages はこのセッションの未読の件数を返す（summary が使う）。
func (s *Service) UnreadMessages(ctx context.Context, a Actor, slug string) (int, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return 0, err
	}
	since, err := s.messageSince(ctx)
	if err != nil {
		return 0, err
	}
	_, n, err := store.UnreadMessageIDs(ctx, s.DB, p.ID, messageReader(a), since, 1)
	return n, err
}

// GetMessage は連絡 1 件を返す（宛先と送り手だけ。ほかの利用者には無いものと同じ 404）。
func (s *Service) GetMessage(ctx context.Context, a Actor, slug string, id int64) (*store.Message, error) {
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, err
	}
	since, err := s.messageSince(ctx)
	if err != nil {
		return nil, err
	}
	m, err := store.MessageByID(ctx, s.DB, p.ID, id, messageReader(a), since)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.message.not_found", "id", id))
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// MarkMessagesRead は連絡をこのセッションの既読にし、新しく印を付けた件数を返す。読めない連絡の id は黙って飛ばす。
func (s *Service) MarkMessagesRead(ctx context.Context, a Actor, slug string, ids []int64) (int64, error) {
	if len(ids) > maxUnreadIDs {
		return 0, errm(Invalid, "invalid_argument", i18n.M("service.err.message.too_many_ids", "max", maxUnreadIDs))
	}
	_, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return 0, err
	}
	since, err := s.messageSince(ctx)
	if err != nil {
		return 0, err
	}
	return store.MarkMessagesRead(ctx, s.DB, p.ID, messageReader(a), since, ids, s.Now())
}
