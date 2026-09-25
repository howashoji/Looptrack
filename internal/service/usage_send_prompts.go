package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 指示文の作業名を送るか（プロジェクト別ルール usage.send_prompts。設計は DESIGN.md §5-4「指示文」）。
//
// 管理者が Web のプロジェクト管理画面から切り替える（looptrack project rules set でも設定できる）。変えるのは
// projects.rules の usage.send_prompts だけで、usage の他のキー（require_on_close・case_pattern など）と他のルールには触れない。
// 変更は setting_changes（name usage_send_prompts:<slug>・on / off）に残す（同じ値なら記録しない）。

// SendPromptsResult は切り替えの結果。
type SendPromptsResult struct {
	Project string `json:"project"`
	Enabled bool   `json:"enabled"`
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

// rulesMap は projects.rules をキーごとの生の JSON に解く（空・null は空の map）。
func rulesMap(raw []byte) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}

// UsageSendPromptsEnabled はプロジェクトのルールが指示文の作業名の送信を許すか。
func UsageSendPromptsEnabled(p store.Project) bool {
	r, err := domain.ParseRules(p.Rules)
	return err == nil && r.SendPrompts()
}

// SetUsageSendPrompts は usage.send_prompts を切り替える。管理者以外は Forbidden。
func (s *Service) SetUsageSendPrompts(ctx context.Context, a Actor, u store.User, p store.Project, enabled bool, ip string) (*SendPromptsResult, error) {
	if u.Role != "admin" {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.forbidden.send_prompts", "slug", p.Slug))
	}
	res := &SendPromptsResult{Project: p.Slug, Enabled: enabled}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res.Changed = false
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT rules FROM projects WHERE id = ? FOR UPDATE", p.ID).Scan(&raw); err != nil {
			return err
		}
		m, err := rulesMap([]byte(raw.String))
		if err != nil {
			return i18n.Wrapf(err, "service.err.rules_read", "slug", p.Slug)
		}
		cur, err := domain.ParseRules([]byte(raw.String))
		if err != nil {
			return i18n.Wrapf(err, "service.err.rules_config", "slug", p.Slug)
		}
		had := cur.SendPrompts()
		if had == enabled {
			return nil
		}
		usageKeys, err := rulesMap(m["usage"])
		if err != nil {
			return i18n.Wrapf(err, "service.err.rules_usage_read", "slug", p.Slug)
		}
		if enabled {
			usageKeys["send_prompts"] = json.RawMessage("true")
		} else {
			delete(usageKeys, "send_prompts")
		}
		if len(usageKeys) == 0 {
			delete(m, "usage")
		} else if m["usage"], err = json.Marshal(usageKeys); err != nil {
			return err
		}
		var next []byte
		if len(m) > 0 {
			if next, err = json.Marshal(m); err != nil {
				return err
			}
			if _, err := domain.ParseRules(next); err != nil { // looptrack project rules set と同じ検査
				return i18n.Wrapf(err, "service.err.rules_config", "slug", p.Slug)
			}
		}
		if err := store.SetRules(ctx, tx, p.ID, next); err != nil {
			return err
		}
		old, nv := store.GateOff, store.GateOff
		if had {
			old = store.GateOn
		}
		if enabled {
			nv = store.GateOn
		}
		res.Changed = true
		return store.RecordSettingChange(ctx, tx, store.SettingChange{Name: store.UsageSendPromptsSetting(p.Slug), OldValue: old, NewValue: nv,
			ActorUserID: sql.NullInt64{Int64: a.UserID, Valid: a.UserID != 0}, Via: a.Via, IP: ip})
	})
	if err != nil {
		return nil, err
	}
	switch {
	case enabled && res.Changed:
		res.Message = i18n.T(a.Lang, "service.usage.send_prompts.enabled", "slug", p.Slug)
	case enabled:
		res.Message = i18n.T(a.Lang, "service.usage.send_prompts.already_enabled", "slug", p.Slug)
	case res.Changed:
		res.Message = i18n.T(a.Lang, "service.usage.send_prompts.disabled", "slug", p.Slug)
	default:
		res.Message = i18n.T(a.Lang, "service.usage.send_prompts.already_disabled", "slug", p.Slug)
	}
	return res, nil
}
