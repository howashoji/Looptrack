package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// fixedValues は fillDigestHeader に渡す値（呼ばれたキーを記録する）。
func fixedValues(called *[]string) func(string) (string, error) {
	vals := map[string]string{"label": "desk-b", "branch": "feature/x", "head": "0123abc", "ai": "codex"}
	return func(k string) (string, error) {
		*called = append(*called, k)
		return vals[k], nil
	}
}

func TestFillDigestHeader(t *testing.T) {
	cases := []struct {
		name, lang, in, want string
		called               []string
	}{
		{"見出しなし（日本語）", "ja", "現在地: 途中。\n",
			"セッション要約: desk-b\nbranch: feature/x\nhead: 0123abc\nai: codex\n\n現在地: 途中。\n", []string{"label", "branch", "head", "ai"}},
		{"見出しなし（英語）", "en", "Where: halfway.",
			"Session digest: desk-b\nbranch: feature/x\nhead: 0123abc\nai: codex\n\nWhere: halfway.", []string{"label", "branch", "head", "ai"}},
		{"先頭の空行は除く", "ja", "\n\n  現在地。",
			"セッション要約: desk-b\nbranch: feature/x\nhead: 0123abc\nai: codex\n\n現在地。", []string{"label", "branch", "head", "ai"}},
		{"そろっていれば何も呼ばない", "ja", "セッション要約: desk-a\nbranch: main\nhead: abcdef0\nai: other\n\n本文",
			"セッション要約: desk-a\nbranch: main\nhead: abcdef0\nai: other\n\n本文", nil},
		{"英語の印・大小と全角コロン", "ja", "SESSION DIGEST: pc\nBranch： main\n\n本文",
			"SESSION DIGEST: pc\nhead: 0123abc\nai: codex\nBranch： main\n\n本文", []string{"head", "ai"}},
		{"空のラベルを足す", "ja", "セッション要約：  \nbranch: main\nhead: abcdef0\nai: other\n",
			"セッション要約： desk-b\nbranch: main\nhead: abcdef0\nai: other\n", []string{"label"}},
		{"印だけの 1 行", "ja", "セッション要約:",
			"セッション要約: desk-b\nbranch: feature/x\nhead: 0123abc\nai: codex", []string{"label", "branch", "head", "ai"}},
		{"CRLF はそのまま", "ja", "セッション要約: pc\r\nai: codex\r\n\r\n本文\r\n",
			"セッション要約: pc\r\nbranch: feature/x\r\nhead: 0123abc\r\nai: codex\r\n\r\n本文\r\n", []string{"branch", "head"}},
		{"値の形が違っても書き換えない", "ja", "セッション要約: pc\nbranch: main\nhead: xyz\nai: robot\n",
			"セッション要約: pc\nbranch: main\nhead: xyz\nai: robot\n", nil},
		// 8 行目より後ろの行は見出しとして数えない（サーバと同じ範囲）。足すので、サーバは重なりではなく 8 行目までの値を読む
		{"9 行目の branch は数えない", "ja", "セッション要約: pc\nhead: abcdef0\nai: other\n\n1\n2\n3\n4\nbranch: late\n",
			"セッション要約: pc\nbranch: feature/x\nhead: abcdef0\nai: other\n\n1\n2\n3\n4\nbranch: late\n", []string{"branch"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var called []string
			got, err := fillDigestHeader(c.in, i18n.Lang(c.lang), fixedValues(&called))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
			if strings.Join(called, ",") != strings.Join(c.called, ",") {
				t.Fatalf("呼んだキー %v（%v のはず。要らない値で git を起動しない）", called, c.called)
			}
			if domain.LeadWord(got) != domain.LeadDigest {
				t.Fatalf("足した結果が要約の印で始まりません: %q", got)
			}
		})
	}
}

// 足した見出しは、サーバと同じ読み方（domain.DigestHeaderField）で 8 行目までに 1 つずつそろう。
func TestFillDigestHeaderReadsLikeServer(t *testing.T) {
	var called []string
	got, err := fillDigestHeader("セッション要約: pc\n本文", i18n.JA, fixedValues(&called))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for i, l := range strings.Split(got, "\n") {
		if i >= domain.DigestHeaderLines {
			break
		}
		if k, _, ok := domain.DigestHeaderField(l); ok {
			seen[k]++
		}
	}
	for _, k := range domain.DigestHeaderKeys {
		if seen[k] != 1 {
			t.Fatalf("%s: が %d 回（1 回のはず）: %q", k, seen[k], got)
		}
	}
}

func TestFillDigestHeaderValueError(t *testing.T) {
	boom := errors.New("git が無い")
	_, err := fillDigestHeader("本文", i18n.JA, func(k string) (string, error) {
		if k == "branch" {
			return "", boom
		}
		return "x", nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v（値を決められない誤りをそのまま返すはず）", err)
	}
}

// 印の文面（i18n の cli.session.digest.lead）は、どの言語でも要約の先頭語として読まれる（訳を変えて印が壊れないように）。
func TestDigestLeadIsDigest(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		if lead := i18n.T(lang, "cli.session.digest.lead"); domain.LeadWord(lead+" pc") != domain.LeadDigest {
			t.Errorf("%s: %q は要約の印として読まれません", lang, lead)
		}
	}
}

func TestDigestValuesLabel(t *testing.T) {
	defer func(h func() (string, error)) { hostname = h }(hostname)
	hostname = func() (string, error) { return "desk-c.local", nil }
	c := &Ctx{Env: env.FromMap(nil)}
	if got, err := c.digestValues("")("label"); err != nil || got != "desk-c" {
		t.Fatalf("ホスト名の最初の . より前のはず: %q %v", got, err)
	}
	c.Env = env.FromMap(map[string]string{"LOOPTRACK_MACHINE_LABEL": "  自宅の机  "})
	if got, _ := c.digestValues("")("label"); got != "自宅の机" {
		t.Fatalf("LOOPTRACK_MACHINE_LABEL が先のはず: %q", got)
	}
	hostname = func() (string, error) { return "", errors.New("x") }
	c.Env = env.FromMap(nil)
	if _, err := c.digestValues("")("label"); err == nil {
		t.Fatal("ラベルを決められないのに誤りになりません")
	}
	c.Env = env.FromMap(map[string]string{"CODEX_THREAD_ID": "t"})
	if got, _ := c.digestValues("")("ai"); got != "codex" {
		t.Fatalf("ai = %q", got)
	}
}

func TestDigestValuesGit(t *testing.T) {
	defer func(g func(string, ...string) (string, error)) { gitOutput = g }(gitOutput)
	var gotArgs [][]string
	gitOutput = func(dir string, args ...string) (string, error) {
		gotArgs = append(gotArgs, args)
		if args[len(args)-1] == "HEAD" && len(args) == 3 {
			return "feature/x\n", nil
		}
		return "0123456789abcdef0123456789abcdef01234567\n", nil
	}
	c := &Ctx{Env: env.FromMap(nil)}
	v := c.digestValues("/work")
	if b, err := v("branch"); err != nil || b != "feature/x" {
		t.Fatalf("branch = %q %v", b, err)
	}
	if h, err := v("head"); err != nil || h != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("head = %q %v", h, err)
	}
	if strings.Join(gotArgs[0], " ") != "rev-parse --abbrev-ref HEAD" || strings.Join(gotArgs[1], " ") != "rev-parse HEAD" {
		t.Fatalf("git の引数: %v", gotArgs)
	}
	gitOutput = func(string, ...string) (string, error) { return "", errors.New("fatal: not a git repository") }
	_, err := v("head")
	if err == nil || !strings.Contains(i18n.Text(i18n.JA, err), "head:") {
		t.Fatalf("git の外で head を決められない誤りになりません: %v", err)
	}
}
