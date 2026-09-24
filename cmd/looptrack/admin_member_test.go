package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// member remove の相手が参加していないときの理由（store.ErrNotFound）は利用者の言語で出る。
// store.ErrNotFound が日本語の素の error だったので、英語の文の中に「見つかりません」が混ざっていた。
// DB のエラーがあるときはその文言を理由に残す（errors.Join で混ぜると i18n.Text が ErrNotFound の文面だけを選ぶ）。
func TestNotMemberErrorInLang(t *testing.T) {
	jaRe := regexp.MustCompile(`[ぁ-んァ-ヶ一-龥]`)
	err := notMemberError(nil, "alice", "web")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("errors.Is(ErrNotFound) が効かない: %v", err)
	}
	if got, want := i18n.Text(i18n.EN, err), "alice is not a member of web: Not found"; got != want {
		t.Errorf("en = %q, want %q", got, want)
	}
	if got := i18n.Text(i18n.EN, err); jaRe.MatchString(got) {
		t.Errorf("en に日本語が混ざる: %q", got)
	}
	if got, want := i18n.Text(i18n.JA, err), "alice は web に参加していません: 見つかりません"; got != want {
		t.Errorf("ja = %q, want %q", got, want)
	}
	// DB のエラーの文言は落とさない
	dbErr := errors.New("dial tcp: connection refused")
	if got := i18n.Text(i18n.EN, notMemberError(dbErr, "alice", "web")); !strings.Contains(got, "connection refused") || strings.Contains(got, "store.err.") {
		t.Errorf("DB のエラーの文言が落ちた: %q", got)
	}
}
