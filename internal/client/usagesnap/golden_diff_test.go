package usagesnap

// 以前の CLI（1.0.0 より前・撤去した）との差分テストの仕掛け。
//
// 同じ呼び出しを Go 版に流し、payload の JSON（キーの順・数の形まで）が、撤去の前に記録した以前の CLI の結果
// （testdata/golden。golden_test.go）と文字列として一致することを確かめる。

import (
	"os"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// 現地時刻は +9 固定（会話 ID は現地時刻で作る。UTC 以外で確かめる。以前の CLI の記録も +9 で取った）。
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("JST", 9*3600)
	os.Exit(m.Run())
}

// call は 1 回の呼び出し（記録のキー。以前の CLI に渡したときと同じ形）。
type call struct {
	Fn     string            `json:"fn"` // claude・codex・copilot・collect・detect・codex_find・copilot_files
	Client string            `json:"client,omitempty"`
	Path   string            `json:"path,omitempty"`
	SID    string            `json:"sid,omitempty"`
	CWD    string            `json:"cwd,omitempty"`
	WS     bool              `json:"ws,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Now    *float64          `json:"now,omitempty"`
	Files  []string          `json:"files,omitempty"`
	Kind   string            `json:"kind,omitempty"` // fn = prim のときの部品（iso・label・excluded・auto・round1・otel_time）
	Arg    string            `json:"arg,omitempty"`
	Num    float64           `json:"num,omitempty"`
}

func (c call) options() Options {
	o := Options{WithSegments: c.WS, Env: env.FromMap(c.Env), Home: c.Env["HOME"]}
	if c.Now != nil {
		n := *c.Now
		o.Now = func() time.Time { return time.Unix(0, int64(n*1e9)) }
	}
	return o
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// runGo は Go 版の結果（非 ASCII をそのまま出す JSON の文字列。記録と同じ形）と値。
func runGo(t testing.TB, c call) (string, any) {
	t.Helper()
	o := c.options()
	var v any
	switch c.Fn {
	case "claude":
		p, err := CollectClaudeCode(c.Path, c.SID, o)
		if err != nil {
			return "__error__ " + err.Error(), nil
		}
		v = objAny(p)
	case "codex":
		v = objAny(CollectCodex(c.Path, c.SID, o))
	case "copilot":
		var files []string
		if c.Files != nil {
			files = c.Files
		}
		v = objAny(CollectCopilot(c.SID, files, c.CWD, o))
	case "collect":
		p, err := Collect(c.Client, c.Path, c.SID, c.CWD, o)
		if err != nil {
			return "__error__ " + err.Error(), nil
		}
		v = objAny(p)
	case "detect":
		a, b, s := Detect(c.CWD, o)
		v = []any{nilIfEmpty(a), nilIfEmpty(b), nilIfEmpty(s)}
	case "codex_find":
		v = nilIfEmpty(codexFindTranscript(c.CWD, c.SID, o))
	case "copilot_files":
		fs := CopilotOtelFiles(o)
		a := []any{}
		for _, f := range fs {
			a = append(a, f)
		}
		v = a
	case "prim":
		v = runPrim(c)
	case "allowed":
		v = SendPromptsAllowed(o)
	case "remember":
		res, _ := decodeLine(c.Arg)
		RememberSendPrompts(res, o)
		v = SendPromptsAllowed(o)
	default:
		t.Fatalf("未知の fn: %s", c.Fn)
	}
	return jsonorder.Compact(v), v
}

// runPrim は部品 1 つの結果（以前の CLI の同じ部品と比べる）。
func runPrim(c call) any {
	switch c.Kind {
	case "iso":
		t, ok := isoTime(c.Arg)
		if !ok {
			return nil
		}
		x := "x"
		return []any{utcString(t), ConversationID(c.Arg, &x, "sid-12345678")}
	case "label":
		return label(c.Arg)
	case "excluded":
		return excluded(c.Arg)
	case "auto":
		return autoRE.MatchString(c.Arg)
	case "round1":
		return round1(c.Num)
	case "otel_time":
		v, ok := decodeLine(c.Arg)
		if !ok {
			return nil
		}
		t, ok := otelTime(v)
		if !ok {
			return nil
		}
		return utcString(t)
	}
	return nil
}

func objAny(p *jsonorder.Object) any {
	if p == nil {
		return nil
	}
	return p
}

// snap は Go 版を呼び、記録した以前の CLI の結果（testdata/golden）と完全一致を確かめる。Go の値を返す。
func snap(t testing.TB, c call) any {
	t.Helper()
	got, v := runGo(t, c)
	want, ok := goldenWant(t, c)
	if !ok {
		t.Fatalf("以前の CLI の記録（%s）に無い呼び出し: %s", goldenPath(t.Name()), callKey(c))
	}
	if g := normalize(got); g != want {
		t.Fatalf("以前の CLI の記録と一致しない（%s %s）\n Go: %s\n 記録: %s", c.Fn, c.Path, g, want)
	}
	return v
}

// snapObj は snap の結果をオブジェクトとして返す（nil なら nil）。
func snapObj(t testing.TB, c call) *jsonorder.Object {
	t.Helper()
	v := snap(t, c)
	if v == nil {
		return nil
	}
	return v.(*jsonorder.Object)
}

// diffMany は呼び出しの列を記録した以前の CLI の結果とまとめて比べる。一致しない数を返す。
func diffMany(t testing.TB, calls []call, report func(i int, goOut, wantOut string)) int {
	t.Helper()
	fixed := float64(1789800000) // 会話記録に時刻が無いときの at（現在時刻）をそろえる
	bad := 0
	for i := range calls {
		if calls[i].Now == nil {
			calls[i].Now = &fixed
		}
		c := calls[i]
		want, ok := goldenWant(t, c)
		if !ok {
			t.Fatalf("以前の CLI の記録（%s）に無い呼び出し: %s", goldenPath(t.Name()), callKey(c))
		}
		got, _ := runGo(t, c)
		if g := normalize(got); g != want {
			bad++
			if report != nil {
				report(i, g, want)
			}
		}
	}
	return bad
}
