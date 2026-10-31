package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 占有（DESIGN.md §9-9）。本番への反映・公開の PR・リリースのタグのような、プロジェクトで同時に 1 本しか動かせない作業を期限つきで取る。
// 規則（名前の形・期限の上限・権限・持ち主の照合・資格情報の検査・記録）はこのファイルの 1 か所に置き、REST・MCP・CLI・Web はここの関数を呼ぶ。
//
// 権限: 参加者が読め、editor 以上が取れる。延ばす・返すのは取っている者だけで、取ったときに返す世代の鍵か、空でない同じセッションで確かめる。
// 外すのは editor 以上の誰でもよく、理由が要る。参加していないプロジェクトは無いものと同じ 404。
//
// 同時に取る: 取る前に行を作り（store.EnsureLeaseRow）、トランザクションではその行を FOR UPDATE してから持ち主を確かめるので、
// 1 つだけが取れる。今の時刻もロックの後で取る（待っている間に切れた期限を、切れたものとして扱うため）。
// 期限切れを自動で返したことにはしない。行に残った持ち主は、次に取った者の記録に was_expired として残る。

// DefaultLeaseTTL は期限を省いたときの長さ。
const DefaultLeaseTTL = time.Hour

// MaxLeaseTTL は 1 回に指定できる期限の上限。
const MaxLeaseTTL = 24 * time.Hour

// maxLeaseText は目的と理由の上限（文字数）。
const maxLeaseText = 200

// maxLeaseEvents は 1 回に返す記録の件数。
const maxLeaseEvents = 200

// leaseNameRe は資源の名前の形（英小文字・数字・-・.。先頭は英小文字か数字。64 文字まで）。
var leaseNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// LeaseInput は占有の操作の入力。
type LeaseInput struct {
	Project string
	Name    string
	// TTLSeconds は期限の長さ（秒）。0 なら既定
	TTLSeconds int64
	// Purpose は目的（取るときに必須）
	Purpose string
	// Reason は外す理由（外すときに必須）
	Reason string
	// MachineLabel は端末のラベル（64 文字まで。無ければ空）
	MachineLabel string
	// Token は取ったときに返した世代の鍵（延ばす・返すで使う。無ければ空）
	Token string
}

// LeaseState は資源 1 つの今の持ち主と、期限が切れているか。
type LeaseState struct {
	store.Lease
	ProjectSlug string
	Expired     bool
}

// LeaseAcquired は取った結果。Token は取った者にだけ返す世代の鍵で、DB には SHA-256 だけを残す。
type LeaseAcquired struct {
	LeaseState
	Token string
}

// LeaseDetail は資源 1 つと記録（新しい順）。
type LeaseDetail struct {
	LeaseState
	Events []store.LeaseEvent
}

func (s *Service) leaseState(l store.Lease, slug string, now time.Time) LeaseState {
	return LeaseState{Lease: l, ProjectSlug: slug, Expired: l.Held() && !now.Before(l.ExpiresAt)}
}

// sameLeaseSession は a が l を取ったセッションか。セッションの無い要求は誰の持ち物とも見ない
// （空の値どうしを同じと見ると、同じ利用者の別の端末や、期限切れの後の遅い要求が次の持ち主の占有を消すため）。
func sameLeaseSession(l store.Lease, a Actor) bool {
	return a.SessionID != "" && l.HolderUserID == a.UserID && l.SessionID == a.SessionID
}

// leaseTokenHash は世代の鍵の保存と照合に使う値を返す。
func leaseTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newLeaseToken は取るたびに作る世代の鍵を返す。
func newLeaseToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isLeaseHolder は延ばす・返す要求が今の持ち主のものか。鍵があれば鍵だけで決める（セッションが同じでも古い鍵は通さない）。
// 鍵で通すので、MCP の接続が張り直されてセッションが変わっても返せる。鍵が無ければ、空でない同じセッションだけを通す。
func isLeaseHolder(l store.Lease, a Actor, token string) bool {
	if token != "" {
		return subtle.ConstantTimeCompare([]byte(leaseTokenHash(token)), []byte(l.TokenHash)) == 1
	}
	return sameLeaseSession(l, a)
}

// leaseError は持ち主を載せた拒否を作る（持ち主・目的・期限を文面と Error.Lease の両方で返す）。
func (s *Service) leaseError(ctx context.Context, q store.Queryer, code string, l store.Lease, now time.Time, slug string) error {
	if u, err := store.UserByID(ctx, q, l.HolderUserID); err == nil {
		l.HolderLogin = u.Login
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	st := s.leaseState(l, slug, now)
	args := []any{"name", l.Name, "holder", l.HolderLogin, "machine", dashIfEmpty(l.MachineLabel), "session", dashIfEmpty(l.SessionID),
		"purpose", l.Purpose, "expires", l.ExpiresAt.In(s.Loc).Format("2006-01-02 15:04:05 MST")}
	var m i18n.Msg
	switch code {
	case "lease_held":
		m = i18n.M("service.err.lease.held", args...)
	case "lease_expired":
		m = i18n.M("service.err.lease.expired", args...)
	default:
		m = i18n.M("service.err.lease.not_holder", args...)
	}
	e := errm(Conflict, code, m)
	e.Lease = &st
	return e
}

func dashIfEmpty(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func errLeaseNotFound(name string) *Error {
	return errm(NotFound, "not_found", i18n.M("service.err.lease.not_found", "name", name))
}

// cleanLeaseName は資源の名前を確かめる。理由には入力の値を載せない（欄に秘密を貼り付けた要求を、そのまま応答やログに返さないため）。
func cleanLeaseName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !leaseNameRe.MatchString(name) {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.lease.name", "max", 64))
	}
	return name, cleanLeaseLine("name", name)
}

// cleanLeaseText は目的・理由を確かめる（必須・1 行・200 文字まで・資格情報を含まない。全員に見えるので資格情報は拒む）。
func cleanLeaseText(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.lease.text_required", "field", field))
	}
	// 目的は summary で AI の文脈に入り、理由は連絡の本文に入るので、使える文字は連絡の 1 行の欄と同じにする
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > maxLeaseText || strings.ContainsFunc(v, messageBadRune) {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.lease.text", "field", field, "max", maxLeaseText))
	}
	if f, ok := domain.FindSecret(v); ok {
		return "", errm(Rejected, "lease_secret", i18n.M("service.err.lease.secret", "field", field, "label", f.Label))
	}
	return v, nil
}

// cleanLeaseLine は 1 行の欄（端末のラベル・セッション ID）の形と資格情報を確かめる（文字の規則は連絡の 1 行の欄と同じ）。
func cleanLeaseLine(field, v string) error {
	if !utf8.ValidString(v) || strings.ContainsFunc(v, messageBadRune) {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.lease.field", "field", field))
	}
	if f, ok := domain.FindSecret(v); ok {
		return errm(Rejected, "lease_secret", i18n.M("service.err.lease.secret", "field", field, "label", f.Label))
	}
	return nil
}

// cleanLeaseLabel は端末のラベルと、記録に残すセッション ID を確かめる（どちらも全員に見える）。
func cleanLeaseLabel(a Actor, v string) (string, error) {
	label, err := cleanTranscriptLabel("machine_label", v)
	if err != nil {
		return "", err
	}
	if err := cleanLeaseLine("machine_label", label); err != nil {
		return "", err
	}
	return label, cleanLeaseLine("session", a.SessionID)
}

// leaseTTL は秒の指定を期限の長さにする（0 は既定。負と上限超えは拒む）。
func leaseTTL(seconds int64) (time.Duration, error) {
	if seconds == 0 {
		return DefaultLeaseTTL, nil
	}
	if seconds < 0 || seconds > int64(MaxLeaseTTL/time.Second) {
		return 0, errm(Invalid, "invalid_argument", i18n.M("service.err.lease.ttl", "max", int64(MaxLeaseTTL/time.Second)))
	}
	return time.Duration(seconds) * time.Second, nil
}

// leaseAccess は利用者と、その利用者が扱えるプロジェクトと役割を引く。write なら editor 以上を求める。
func (s *Service) leaseAccess(ctx context.Context, a Actor, slug string, write bool) (store.User, store.Project, error) {
	if a.UserID == 0 {
		return store.User{}, store.Project{}, errm(Forbidden, "forbidden", i18n.M("service.err.lease.no_user"))
	}
	u, p, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return u, p, err
	}
	if write && role != "editor" && role != "admin" {
		return u, p, errm(Forbidden, "forbidden", i18n.M("service.err.lease.forbidden", "slug", p.Slug))
	}
	return u, p, nil
}

func leaseVia(a Actor) string {
	if a.Via == "" {
		return "api"
	}
	return a.Via
}

// ProjectLeases はプロジェクトの、今取られている占有を返す（期限が切れたものは出さない。summary が使う。権限は呼ぶ側が確かめた後）。
func (s *Service) ProjectLeases(ctx context.Context, p store.Project) ([]LeaseState, error) {
	held, err := store.HeldLeases(ctx, s.DB, p.ID)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	out := []LeaseState{}
	for _, l := range held {
		if st := s.leaseState(l, p.Slug, now); !st.Expired {
			out = append(out, st)
		}
	}
	return out, nil
}

// ListLeases はプロジェクトの、持ち主のいる占有を返す（期限が切れて取り直されていないものも Expired を立てて含む）。
func (s *Service) ListLeases(ctx context.Context, a Actor, slug string) ([]LeaseState, store.Project, error) {
	_, p, err := s.leaseAccess(ctx, a, slug, false)
	if err != nil {
		return nil, p, err
	}
	held, err := store.HeldLeases(ctx, s.DB, p.ID)
	if err != nil {
		return nil, p, err
	}
	now := s.Now()
	out := make([]LeaseState, 0, len(held))
	for _, l := range held {
		out = append(out, s.leaseState(l, p.Slug, now))
	}
	return out, p, nil
}

// GetLease は資源 1 つと記録を返す（空いていても記録があれば返す。一度も取られていなければ 404）。
func (s *Service) GetLease(ctx context.Context, a Actor, slug, name string) (*LeaseDetail, error) {
	name, err := cleanLeaseName(name)
	if err != nil {
		return nil, err
	}
	_, p, err := s.leaseAccess(ctx, a, slug, false)
	if err != nil {
		return nil, err
	}
	l, err := store.LeaseByName(ctx, s.DB, p.ID, name, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errLeaseNotFound(name)
	}
	if err != nil {
		return nil, err
	}
	events, err := store.LeaseEvents(ctx, s.DB, l.ID, maxLeaseEvents)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, errLeaseNotFound(name) // 取る途中で拒まれた行（持ち主も記録も無い）は無いものと同じ
	}
	return &LeaseDetail{LeaseState: s.leaseState(l, p.Slug, s.Now()), Events: events}, nil
}

// AcquireLease は資源を取る。取っている者がいて期限が切れていなければ 409 lease_held（同じセッションでも同じ。長くするなら ExtendLease）。
func (s *Service) AcquireLease(ctx context.Context, a Actor, in LeaseInput) (*LeaseAcquired, error) {
	name, err := cleanLeaseName(in.Name)
	if err != nil {
		return nil, err
	}
	purpose, err := cleanLeaseText("purpose", in.Purpose)
	if err != nil {
		return nil, err
	}
	label, err := cleanLeaseLabel(a, in.MachineLabel)
	if err != nil {
		return nil, err
	}
	ttl, err := leaseTTL(in.TTLSeconds)
	if err != nil {
		return nil, err
	}
	u, p, err := s.leaseAccess(ctx, a, in.Project, true)
	if err != nil {
		return nil, err
	}
	token, err := newLeaseToken()
	if err != nil {
		return nil, err
	}
	if err := store.EnsureLeaseRow(ctx, s.DB, p.ID, name, s.Now()); err != nil {
		return nil, err
	}
	var res LeaseState
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := store.LeaseByName(ctx, tx, p.ID, name, true) // 行のロックをトランザクションの最初の文にする
		if errors.Is(err, store.ErrNotFound) {
			return errLeaseNotFound(name)
		}
		if err != nil {
			return err
		}
		if err := checkProjectActive(ctx, tx, p); err != nil {
			return err
		}
		now := s.Now()
		wasExpired := false
		if cur.Held() {
			if now.Before(cur.ExpiresAt) {
				return s.leaseError(ctx, tx, "lease_held", cur, now, p.Slug)
			}
			wasExpired = true
		}
		next := cur
		next.HolderUserID, next.MachineLabel, next.SessionID, next.Purpose, next.TokenHash = u.ID, label, a.SessionID, purpose, leaseTokenHash(token)
		next.AcquiredAt, next.ExpiresAt, next.UpdatedAt = now, now.Add(ttl), now
		if err := store.UpdateLease(ctx, tx, next); err != nil {
			return err
		}
		if err := store.InsertLeaseEvent(ctx, tx, store.LeaseEvent{LeaseID: cur.ID, Action: store.LeaseActionAcquire, UserID: u.ID, MachineLabel: label,
			SessionID: a.SessionID, TokenID: a.TokenID, Via: leaseVia(a), Purpose: purpose, ExpiresAt: next.ExpiresAt, PrevUserID: cur.HolderUserID,
			PrevMachineLabel: cur.MachineLabel, PrevSessionID: cur.SessionID, WasExpired: wasExpired, CreatedAt: now}); err != nil {
			return err
		}
		next.HolderLogin = u.Login
		res = s.leaseState(next, p.Slug, now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &LeaseAcquired{LeaseState: res, Token: token}, nil
}

// lockHeldLease は持ち主のいる資源の行をロックして読む。行が無い・空いていれば 404。
func (s *Service) lockHeldLease(ctx context.Context, tx *sql.Tx, p store.Project, name string) (store.Lease, error) {
	cur, err := store.LeaseByName(ctx, tx, p.ID, name, true)
	if errors.Is(err, store.ErrNotFound) {
		return cur, errLeaseNotFound(name)
	}
	if err != nil {
		return cur, err
	}
	if err := checkProjectActive(ctx, tx, p); err != nil {
		return cur, err
	}
	if !cur.Held() {
		return cur, errLeaseNotFound(name)
	}
	return cur, nil
}

// leaseRowExists は資源の行があるかをロックなしで確かめる。無い行への FOR UPDATE はギャップロックになり、
// 同時に取る側の INSERT を待たせるので、無ければトランザクションを始めずに 404 にする。行は消さないので、確かめた後に無くならない。
func (s *Service) leaseRowExists(ctx context.Context, p store.Project, name string) error {
	if _, err := store.LeaseByName(ctx, s.DB, p.ID, name, false); errors.Is(err, store.ErrNotFound) {
		return errLeaseNotFound(name)
	} else if err != nil {
		return err
	}
	return nil
}

// ExtendLease は期限を今から TTLSeconds 後にする。取っている者だけで、期限が切れていれば 409 lease_expired（取り直す）。
// TTLSeconds を省いたら今の期限と「今 + 既定」の遅いほうにする（省いた延長で残りが縮まないように）。
func (s *Service) ExtendLease(ctx context.Context, a Actor, in LeaseInput) (*LeaseState, error) {
	name, err := cleanLeaseName(in.Name)
	if err != nil {
		return nil, err
	}
	label, err := cleanLeaseLabel(a, in.MachineLabel)
	if err != nil {
		return nil, err
	}
	ttl, err := leaseTTL(in.TTLSeconds)
	if err != nil {
		return nil, err
	}
	u, p, err := s.leaseAccess(ctx, a, in.Project, true)
	if err != nil {
		return nil, err
	}
	if err := s.leaseRowExists(ctx, p, name); err != nil {
		return nil, err
	}
	var res LeaseState
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := s.lockHeldLease(ctx, tx, p, name)
		if err != nil {
			return err
		}
		now := s.Now()
		if !isLeaseHolder(cur, a, strings.TrimSpace(in.Token)) {
			return s.leaseError(ctx, tx, "lease_not_holder", cur, now, p.Slug)
		}
		if !now.Before(cur.ExpiresAt) {
			return s.leaseError(ctx, tx, "lease_expired", cur, now, p.Slug)
		}
		next := cur
		next.ExpiresAt, next.UpdatedAt = now.Add(ttl), now
		if in.TTLSeconds == 0 && cur.ExpiresAt.After(next.ExpiresAt) {
			next.ExpiresAt = cur.ExpiresAt
		}
		if err := store.UpdateLease(ctx, tx, next); err != nil {
			return err
		}
		if err := store.InsertLeaseEvent(ctx, tx, store.LeaseEvent{LeaseID: cur.ID, Action: store.LeaseActionExtend, UserID: u.ID, MachineLabel: label,
			SessionID: a.SessionID, TokenID: a.TokenID, Via: leaseVia(a), Purpose: cur.Purpose, ExpiresAt: next.ExpiresAt, PrevUserID: cur.HolderUserID,
			PrevMachineLabel: cur.MachineLabel, PrevSessionID: cur.SessionID, CreatedAt: now}); err != nil {
			return err
		}
		next.HolderLogin = u.Login
		res = s.leaseState(next, p.Slug, now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// endLease は返す・外すの共通部分（持ち主を空にして記録を足す）。
func (s *Service) endLease(ctx context.Context, tx *sql.Tx, a Actor, u store.User, cur store.Lease, action, reason, label string, now time.Time) error {
	next := cur
	next.HolderUserID, next.MachineLabel, next.SessionID, next.Purpose, next.TokenHash, next.UpdatedAt = 0, "", "", "", "", now
	if err := store.UpdateLease(ctx, tx, next); err != nil {
		return err
	}
	return store.InsertLeaseEvent(ctx, tx, store.LeaseEvent{LeaseID: cur.ID, Action: action, UserID: u.ID, MachineLabel: label, SessionID: a.SessionID,
		TokenID: a.TokenID, Via: leaseVia(a), Purpose: cur.Purpose, Reason: reason, PrevUserID: cur.HolderUserID, PrevMachineLabel: cur.MachineLabel,
		PrevSessionID: cur.SessionID, WasExpired: !now.Before(cur.ExpiresAt), CreatedAt: now})
}

// ReleaseLease は資源を返す。取っている者だけ（期限が切れた後でも、次に取られるまでは返せる）。ほかの者と古い鍵は 409 lease_not_holder。
func (s *Service) ReleaseLease(ctx context.Context, a Actor, in LeaseInput) error {
	name, err := cleanLeaseName(in.Name)
	if err != nil {
		return err
	}
	label, err := cleanLeaseLabel(a, in.MachineLabel)
	if err != nil {
		return err
	}
	u, p, err := s.leaseAccess(ctx, a, in.Project, true)
	if err != nil {
		return err
	}
	if err := s.leaseRowExists(ctx, p, name); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := s.lockHeldLease(ctx, tx, p, name)
		if err != nil {
			return err
		}
		now := s.Now()
		if !isLeaseHolder(cur, a, strings.TrimSpace(in.Token)) {
			return s.leaseError(ctx, tx, "lease_not_holder", cur, now, p.Slug)
		}
		return s.endLease(ctx, tx, a, u, cur, store.LeaseActionRelease, "", label, now)
	})
}

// LeaseBreakResult は外した結果。
type LeaseBreakResult struct {
	// Holder は外された者の login
	Holder string
	// NoticeFailed は外された者へ連絡を送れなかった（外すことはできている）
	NoticeFailed bool
}

// BreakLease は他人の占有を外す。editor 以上の誰でも、理由を付けて外せる。理由と外される前の持ち主は記録に残り、
// 外された者には user 宛ての連絡で知らせる（取っていた同じセッションが自分で外したときは送らない）。
//
// 連絡は確定の後で送り、送れなくても外した結果は戻さない。止まった占有を外す場面では、持ち主が既に参加者でない・無効になっていることがあり、
// そのときに外せないと占有が残り続けるため。送れなかったことは NoticeFailed で呼んだ側に返す。
func (s *Service) BreakLease(ctx context.Context, a Actor, in LeaseInput) (*LeaseBreakResult, error) {
	name, err := cleanLeaseName(in.Name)
	if err != nil {
		return nil, err
	}
	reason, err := cleanLeaseText("reason", in.Reason)
	if err != nil {
		return nil, err
	}
	label, err := cleanLeaseLabel(a, in.MachineLabel)
	if err != nil {
		return nil, err
	}
	u, p, err := s.leaseAccess(ctx, a, in.Project, true)
	if err != nil {
		return nil, err
	}
	if err := s.leaseRowExists(ctx, p, name); err != nil {
		return nil, err
	}
	var prev store.Lease
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := s.lockHeldLease(ctx, tx, p, name)
		if err != nil {
			return err
		}
		prev = cur
		return s.endLease(ctx, tx, a, u, cur, store.LeaseActionBreak, reason, label, s.Now())
	})
	if err != nil {
		return nil, err
	}
	res := &LeaseBreakResult{}
	holder, err := store.UserByID(ctx, s.DB, prev.HolderUserID)
	if err != nil {
		res.NoticeFailed = true
		return res, nil
	}
	res.Holder = holder.Login
	if sameLeaseSession(prev, a) {
		return res, nil
	}
	lang := a.Lang
	if holder.Lang.Valid {
		lang = i18n.Lang(holder.Lang.String)
	}
	body := i18n.T(lang, "service.lease.broken_notice", "name", prev.Name, "by", u.Login, "reason", reason, "purpose", prev.Purpose)
	if _, err := s.SendMessage(ctx, a, MessageSendInput{Project: p.Slug, To: store.MessageToUser, User: holder.Login, Body: body, MachineLabel: label}); err != nil {
		res.NoticeFailed = true
	}
	return res, nil
}
