package cli

import (
	"errors"
	"testing"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/i18n"
)

// hook から呼んだ push の誤りの分け方（退避して送り直すか・印を置いて試し直さないか・捨てるかが、これで決まる）。
func TestClassifyHookPush(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		want  HookPushOutcome
		files int
	}{
		{"送った", nil, HookPushSent, 0},
		{"書き換えられた", &Exit{Code: 1}, HookPushRewritten, 0},
		{"変換の止め", i18n.Errorf("cli.err.transcript.rejected", "count", 3), HookPushRejected, 3},
		{"変換の止め（送り済みあり）", i18n.Errorf("cli.err.transcript.rejected_sent", "count", 2, "sent", 1), HookPushRejected, 2},
		{"見つからない", i18n.Errorf("cli.err.transcript.not_found", "session", "s"), HookPushNotFound, 0},
		{"ルールが切", &api.Error{Status: 422, Code: "transcript_rule_off"}, HookPushRuleOff, 0},
		{"サーバの秘密の検査", &api.Error{Status: 422, Code: "transcript_secret"}, HookPushRejected, 1},
		{"サーバの読めない行の検査", &api.Error{Status: 422, Code: "transcript_unreadable"}, HookPushRejected, 1},
		{"上限", &api.Error{Status: 422, Code: "transcript_quota"}, HookPushRefused, 0},
		{"5xx", &api.Error{Status: 503, Code: "x"}, HookPushRetry, 0},
		{"届かない", &api.ConnError{URL: "u", Reason: "r"}, HookPushRetry, 0},
		{"ログインしていない", &api.NoTokenError{URL: "u"}, HookPushRetry, 0},
		{"古いサーバ", i18n.Errorf("cli.err.transcript.unsupported"), HookPushRefused, 0},
		{"読めない", i18n.Errorf("cli.err.transcript.read", "file", "f", "reason", "r"), HookPushRetry, 0},
		{"そのほか", errors.New("x"), HookPushRetry, 0},
	} {
		r := classifyHookPush(c.err)
		if r.Outcome != c.want || r.Files != c.files {
			t.Errorf("%s: %v・%d（%v・%d のはず）", c.name, r.Outcome, r.Files, c.want, c.files)
		}
	}
	if r := classifyHookPush(&api.ConnError{URL: "u"}); !r.Conn {
		t.Error("届かないときは Conn を立てるはず")
	}
	if r := classifyHookPush(&api.NoTokenError{URL: "u"}); !r.NoToken || r.Conn {
		t.Error("ログインしていないときは NoToken だけを立てるはず")
	}
}
