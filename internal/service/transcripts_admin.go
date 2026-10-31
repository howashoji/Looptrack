package service

import (
	"context"
	"database/sql"
	"sort"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 会話記録の管理者向けの操作（上限と保持期間の変更・利用者ごとの使用量）。管理者かどうかの判定はここに置く
// （画面 /admin/transcripts も入口で管理者以外を止めるが、規則の本体はここにある。添付の attachments_admin.go と同じ形）。
// 管理者に見せるのは数だけ: 利用者ごとの会話の数とバイト数。中身・題名・ファイル名・セッション ID は返さない
// （docs/server/DESIGN.md §2-5。会話記録は持ち主にしか見えず、管理者にも無いものと同じ）。

// transcriptChangesShown は管理者の画面に出す上限の変更の記録の数。
const transcriptChangesShown = 50

// transcriptLimitNames は会話記録の上限と保持期間の設定名（画面と変更の記録の並び順）。
var transcriptLimitNames = []string{store.SettingTranscriptMaxChunk, store.SettingTranscriptMaxUser, store.SettingTranscriptRetentionDays}

// transcriptAdmin は操作した利用者が管理者（利用者の役割 admin）であることを確かめる。
func (s *Service) transcriptAdmin(ctx context.Context, a Actor) (store.User, error) {
	u, err := s.actorUser(ctx, a)
	if err != nil {
		return u, err
	}
	if u.Role != "admin" {
		return u, errm(Forbidden, "forbidden", i18n.M("service.err.transcript.admin_only"))
	}
	return u, nil
}

// SetTranscriptLimits は会話記録の上限（1 チャンク・1 利用者）と保持日数を変える。管理者だけ。
// 変えた設定ごとに setting_changes へ変更前後の値と操作した人を残す（同じ値の項目は記録しない）。変えた設定の名前を返す。
func (s *Service) SetTranscriptLimits(ctx context.Context, a Actor, l store.TranscriptLimits, ip string) ([]string, error) {
	u, err := s.transcriptAdmin(ctx, a)
	if err != nil {
		return nil, err
	}
	for _, v := range []int64{l.MaxChunk, l.MaxUser} {
		if v <= 0 {
			return nil, errm(Invalid, "invalid_argument", i18n.M("store.err.transcript_limit.invalid", "value", v))
		}
	}
	if l.RetentionDays < 0 || l.RetentionDays > store.MaxRetentionDays {
		return nil, errm(Invalid, "invalid_argument", i18n.M("store.err.transcript_limit.retention", "value", l.RetentionDays))
	}
	via := a.Via
	if via == "" {
		via = "web"
	}
	ch := store.SettingChange{ActorUserID: sql.NullInt64{Int64: u.ID, Valid: true}, Via: via, IP: ip}
	var changed []string
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		changed = nil
		for _, it := range []struct {
			name string
			v    int64
		}{{store.SettingTranscriptMaxChunk, l.MaxChunk}, {store.SettingTranscriptMaxUser, l.MaxUser}, {store.SettingTranscriptRetentionDays, l.RetentionDays}} {
			_, ok, err := store.SetTranscriptLimitTx(ctx, tx, it.name, it.v, ch)
			if err != nil {
				return err
			}
			if ok {
				changed = append(changed, it.name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// UserTranscriptUsage は利用者 1 人の会話記録の使用量（管理者の画面の表の 1 行。数だけ）。
type UserTranscriptUsage struct {
	Login, DisplayName string
	Disabled           bool
	store.TranscriptUsage
}

// TranscriptOverview は管理者の画面に出すもの一式。
type TranscriptOverview struct {
	Limits    store.TranscriptLimits
	Usage     []UserTranscriptUsage // 会話記録を 1 つ以上持つ利用者（ログイン名の順）
	Changes   []store.SettingChange // 上限と保持日数の変更の記録（新しい順）
	Available bool                  // 置き場が使えるか（無い・添付の置き場と入れ子なら、会話記録の API は使えない）
}

// TranscriptOverview は管理者の画面に出すもの（上限・保持日数・利用者ごとの使用量・変更の記録）を返す。管理者だけ。
func (s *Service) TranscriptOverview(ctx context.Context, a Actor) (*TranscriptOverview, error) {
	if _, err := s.transcriptAdmin(ctx, a); err != nil {
		return nil, err
	}
	out := &TranscriptOverview{Available: s.TranscriptsAvailable()}
	var err error
	if out.Limits, err = store.ReadTranscriptLimits(ctx, s.DB); err != nil {
		return nil, err
	}
	usage, err := store.TranscriptUsageByUser(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	users, err := store.ListUsers(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if us, ok := usage[u.ID]; ok {
			out.Usage = append(out.Usage, UserTranscriptUsage{Login: u.Login, DisplayName: u.DisplayName, Disabled: u.Disabled, TranscriptUsage: us})
		}
	}
	sort.SliceStable(out.Usage, func(i, j int) bool { return out.Usage[i].Login < out.Usage[j].Login })
	for _, name := range transcriptLimitNames {
		changes, err := store.SettingChanges(ctx, s.DB, name, transcriptChangesShown)
		if err != nil {
			return nil, err
		}
		out.Changes = append(out.Changes, changes...)
	}
	sort.SliceStable(out.Changes, func(i, j int) bool { return out.Changes[i].At.After(out.Changes[j].At) })
	if len(out.Changes) > transcriptChangesShown {
		out.Changes = out.Changes[:transcriptChangesShown]
	}
	return out, nil
}
