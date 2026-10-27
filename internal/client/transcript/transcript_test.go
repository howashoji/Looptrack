package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// 合成の材料の根（internal/tools/transcriptfixture の合成の値と同じ）。
var fixtureHints = Hints{
	Roots:      []string{"/home/u/proj"},
	SessionDir: "/home/u/.claude/projects/-home-u-proj",
	VendorDir:  "/home/u/.claude",
	Home:       "/home/u",
}

// 送った端末自身の値で戻す（往復のテスト用）。
var fixtureTarget = Target{Root: "/home/u/proj", SessionDir: fixtureHints.SessionDir, VendorDir: fixtureHints.VendorDir, Home: "/home/u"}

func fixtureFiles(t *testing.T) []string {
	t.Helper()
	main := filepath.Join("testdata", "claude", "00000000-0000-4000-8000-000000000001.jsonl")
	subs, err := filepath.Glob(filepath.Join("testdata", "claude", "00000000-0000-4000-8000-000000000001", "subagents", "*.jsonl"))
	if err != nil || len(subs) == 0 {
		t.Fatalf("前提が崩れています: サブエージェントの材料が見つかりません（%v）", err)
	}
	return append([]string{main}, subs...)
}

// stringsAt は JSON の行の文字列の値を、欄の扱い（claudeRule）と経路の名前つきで集める。
type valueAt struct {
	path string
	rule fieldRule
	v    string
}

func stringsAt(t *testing.T, doc []byte, rule func([]seg) fieldRule) []valueAt {
	t.Helper()
	var out []valueAt
	err := scanJSON(doc, func(path []seg, key bool, s, e int) {
		if key {
			return
		}
		v, _, _ := decodeString(doc[s:e])
		out = append(out, valueAt{fieldName(path), rule(path), v})
	})
	if err != nil {
		t.Fatalf("前提が崩れています: 行を読めません（%v）", err)
	}
	return out
}

func joinChunks(res *Result) []byte {
	var b bytes.Buffer
	for _, c := range res.Chunks {
		b.Write(c.Data)
	}
	return b.Bytes()
}

func restoreAll(t *testing.T, res *Result, kind Kind, tg Target) []byte {
	t.Helper()
	var b bytes.Buffer
	for i, c := range res.Chunks {
		got, err := Restore(c.Data, kind, c.Nonce, tg)
		if err != nil {
			t.Fatalf("チャンク %d を戻せません: %v", i, err)
		}
		b.Write(got)
	}
	return b.Bytes()
}

func maskHitsLine(t *testing.T, line []byte) bool {
	t.Helper()
	hit := false
	for _, v := range stringsAt(t, line, claudeRule) {
		if domain.MaskSecrets(v.v) != v.v {
			hit = true
		}
	}
	return hit
}

// 往復: 合成の Claude の記録を同じ手がかりで変換して戻すと、マスクに当たらない行はバイト一致。
// 同じテストに対照: tool_use の入力のパスはプレースホルダになり、本文（text・tool_result の content）の同じパスは変わらない。
func TestRoundTripClaudeFixture(t *testing.T) {
	for _, f := range fixtureFiles(t) {
		for _, max := range []int{0, 64 << 10} {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Convert(data, Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, MaxChunkBytes: max})
			if err != nil {
				t.Fatalf("%s: 変換できません: %v", filepath.Base(f), err)
			}
			if res.Stats.Rewritten == 0 || res.Stats.ChangedLines == 0 {
				t.Fatalf("%s: 前提が崩れています: 書き換えが 0 件（%+v）", filepath.Base(f), res.Stats)
			}
			if max > 0 && strings.Contains(f, "00000001.jsonl") && len(res.Chunks) < 2 {
				t.Fatalf("前提が崩れています: 上限 %d でチャンクが %d 個（複数のチャンクの往復を通っていない）", max, len(res.Chunks))
			}
			conv := joinChunks(res)
			if bytes.Contains(conv, []byte("/home/u/proj")) == false {
				t.Fatalf("%s: 前提が崩れています: 本文に残るはずの /home/u/proj が変換後に 1 つも無い", filepath.Base(f))
			}
			back := restoreAll(t, res, KindJSONL, fixtureTarget)
			origLines, pend := splitLines(data)
			backLines, _ := splitLines(back)
			if pend != res.Pending {
				t.Errorf("Pending %d・実際 %d", res.Pending, pend)
			}
			if len(origLines) != len(backLines) {
				t.Fatalf("%s: 行の数が違う: 元 %d・戻した %d", filepath.Base(f), len(origLines), len(backLines))
			}
			diff, masked := 0, 0
			for i := range origLines {
				m := maskHitsLine(t, origLines[i].body)
				if m {
					masked++
				}
				if !bytes.Equal(origLines[i].body, backLines[i].body) {
					diff++
					if !m {
						t.Errorf("%s: %d 行目がマスクに当たらないのに元のバイトに戻らない", filepath.Base(f), i+1)
					}
				}
			}
			if masked != res.Stats.MaskedLines {
				t.Errorf("%s: マスクに当たる行 %d・Stats.MaskedLines %d", filepath.Base(f), masked, res.Stats.MaskedLines)
			}
			t.Logf("%s max=%d: 行 %d・チャンク %d・書き換え %d・変わった行 %d・戻らない行 %d・書き換えなかった（構造 %d・散文 %d）",
				filepath.Base(f), max, len(origLines), len(res.Chunks), res.Stats.Rewritten, res.Stats.ChangedLines, diff,
				res.Stats.UnrewrittenStructural, res.Stats.UnrewrittenProse)
		}
	}
	// 対照: マスクに当たる行を 1 行足すと、戻らない行はその 1 行だけになる（比べ方が差を見つけられることの確かめ）
	{
		data, err := os.ReadFile(fixtureFiles(t)[1])
		if err != nil {
			t.Fatal(err)
		}
		extra := `{"cwd":"/home/u/proj","message":{"content":[{"type":"text","text":"password: ` + secretLineMarker + `"}]}}` + "\n"
		data = append(data, extra...)
		res, err := Convert(data, Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints})
		if err != nil {
			t.Fatal(err)
		}
		back := restoreAll(t, res, KindJSONL, fixtureTarget)
		o, _ := splitLines(data)
		b, _ := splitLines(back)
		var diffAt []int
		for i := range o {
			if !bytes.Equal(o[i].body, b[i].body) {
				diffAt = append(diffAt, i)
			}
		}
		if len(diffAt) != 1 || diffAt[0] != len(o)-1 || res.Stats.MaskedLines != 1 {
			t.Errorf("対照: 戻らない行 %v（期待: 最後の 1 行だけ）・MaskedLines %d", diffAt, res.Stats.MaskedLines)
		}
	}
	// 対照: 材料の tool_use の入力にあるパスのうち、本文（text・tool_result の content）にも同じものが出るものを探し、
	// 変換後に入力の側からは消え、本文の側には残ることを見る。
	data, err := os.ReadFile(fixtureFiles(t)[0])
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(data, Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints})
	if err != nil {
		t.Fatal(err)
	}
	pathTok := func(s string) []string {
		var out []string
		for _, w := range strings.Fields(s) {
			if strings.HasPrefix(w, "/home/u/proj/") {
				out = append(out, strings.TrimRight(w, ".,;:)\"'"))
			}
		}
		return out
	}
	inputPaths, prosePaths := map[string]bool{}, map[string]bool{}
	ls, _ := splitLines(data)
	for _, l := range ls {
		for _, v := range stringsAt(t, l.body, claudeRule) {
			switch {
			case v.rule == ruleAnywhere && strings.Contains(v.path, ".input."):
				for _, p := range pathTok(v.v) {
					inputPaths[p] = true
				}
			case strings.HasPrefix(v.path, "message.content[].content") || strings.HasSuffix(v.path, ".text"):
				for _, p := range pathTok(v.v) {
					prosePaths[p] = true
				}
			}
		}
	}
	var same []string
	for p := range inputPaths {
		if prosePaths[p] {
			same = append(same, p)
		}
	}
	if len(same) == 0 {
		t.Fatalf("前提が崩れています: 材料の tool_use の入力と本文に同じパスがありません（入力 %d・本文 %d）", len(inputPaths), len(prosePaths))
	}
	convLines, _ := splitLines(joinChunks(res))
	inputLeft, proseKept := 0, 0
	for _, l := range convLines {
		for _, v := range stringsAt(t, l.body, claudeRule) {
			for _, p := range same {
				if !strings.Contains(v.v, p) {
					continue
				}
				switch {
				case v.rule == ruleAnywhere:
					inputLeft++
				case strings.HasPrefix(v.path, "message.content[].content") || strings.HasSuffix(v.path, ".text"):
					proseKept++
				}
			}
		}
	}
	if inputLeft != 0 {
		t.Errorf("tool_use の入力に元のパスが %d 個残った", inputLeft)
	}
	if proseKept == 0 {
		t.Errorf("本文の同じパスが残っていない（対照が崩れた: 本文まで書き換えている）")
	}
	t.Logf("同じパス %d 個: 入力に残った %d・本文に残った %d", len(same), inputLeft, proseKept)
}

// tool_use の file_path・path・notebook_path・Bash の command の中の絶対パスと wireToolInputs はプレースホルダになり、
// message.content[].text と tool_result の content の中の同じパスは変わらない（同じ行の中の対照）。
func TestToolUseFieldsRewrittenProseKept(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"assistant","cwd":"/home/u/proj","message":{"content":[` +
			`{"type":"text","text":"I will edit /home/u/proj/a.go now"},` +
			`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/home/u/proj/a.go","old_string":"x","new_string":"y"}},` +
			`{"type":"tool_use","id":"t2","name":"Grep","input":{"path":"/home/u/proj/sub","pattern":"p"}},` +
			`{"type":"tool_use","id":"t3","name":"NotebookEdit","input":{"notebook_path":"/home/u/proj/n.ipynb"}},` +
			`{"type":"tool_use","id":"t4","name":"Bash","input":{"command":"cd /home/u/proj && cat /home/u/proj/a.go | wc -l"}}]},` +
			`"wireToolInputs":{"t4":{"command":"cd /home/u/proj && cat /home/u/proj/a.go | wc -l"}}}`,
		`{"type":"user","cwd":"/home/u/proj","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"edited /home/u/proj/a.go"},` +
			`{"type":"tool_result","tool_use_id":"t4","content":[{"type":"text","text":"/home/u/proj/a.go: 3"}]}]},` +
			`"toolUseResult":{"filePath":"/home/u/proj/a.go","stdout":"/home/u/proj/a.go: 3"}}`,
	}, "\n") + "\n"
	res, err := Convert([]byte(lines), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, Rand: fixedRand("0123456789ab")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) != 1 {
		t.Fatalf("チャンク %d 個", len(res.Chunks))
	}
	c := res.Chunks[0]
	ph := "{{LT:" + c.Nonce + ":ROOT0}}"
	want := map[string]string{
		"cwd":                                   ph,
		"message.content[].input.file_path":     ph + "/a.go",
		"message.content[].input.path":          ph + "/sub",
		"message.content[].input.notebook_path": ph + "/n.ipynb",
		"message.content[].input.command":       "cd " + ph + " && cat " + ph + "/a.go | wc -l",
		"wireToolInputs.t4.command":             "cd " + ph + " && cat " + ph + "/a.go | wc -l",
		"toolUseResult.filePath":                ph + "/a.go",
		"message.content[].text":                "I will edit /home/u/proj/a.go now",
		"message.content[].content":             "edited /home/u/proj/a.go",
		"message.content[].content[].text":      "/home/u/proj/a.go: 3",
		"toolUseResult.stdout":                  "/home/u/proj/a.go: 3",
	}
	seen := map[string]bool{}
	ls, _ := splitLines(c.Data)
	for _, l := range ls {
		for _, v := range stringsAt(t, l.body, claudeRule) {
			w, ok := want[v.path]
			if !ok {
				continue
			}
			seen[v.path] = true
			if v.v != w {
				t.Errorf("%s: %q（期待 %q）", v.path, v.v, w)
			}
		}
	}
	for p := range want {
		if !seen[p] {
			t.Errorf("前提が崩れています: 欄 %s が行に無い", p)
		}
	}
	if got := strings.Join(c.Placeholders, ","); got != "ROOT0" {
		t.Errorf("Placeholders %q", got)
	}
	if res.Stats.UnrewrittenProse != 4 || res.Stats.UnrewrittenStructural != 0 {
		t.Errorf("書き換えなかったパス: 散文 %d（期待 4）・構造 %d（期待 0）", res.Stats.UnrewrittenProse, res.Stats.UnrewrittenStructural)
	}
	back, err := Restore(c.Data, KindJSONL, c.Nonce, fixtureTarget)
	if err != nil || string(back) != lines {
		t.Errorf("戻した結果が元と違う（%v）", err)
	}
	// 別の端末の値に戻すと、書き換えた欄だけがその端末のパスになる
	other, err := Restore(c.Data, KindJSONL, c.Nonce, Target{Root: "/w/v/clone", SessionDir: "/w/v/.claude/projects/-w-v-clone", VendorDir: "/w/v/.claude", Home: "/w/v"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(other, []byte(`"file_path":"/w/v/clone/a.go"`)) || !bytes.Contains(other, []byte(`"text":"I will edit /home/u/proj/a.go now"`)) {
		t.Errorf("別の端末への戻しが違う")
	}
}

// hook のコマンド行（hookInfos[].command・attachment.command）は、先頭の語（実行ファイルの絶対パス）だけをプレースホルダにする。
// 値に空白があるので値の頭の規則には当たらず、以前は端末のパスがそのまま送られていた。
// 同じテストに対照: text の中の同じ文字列は変えない。先頭の語が手がかりに当たらないコマンドも変えない。
func TestHookCommandHeadRewritten(t *testing.T) {
	const homeBin = "/home/u/.local/bin/looptrack hook stop"
	const rootBin = "/home/u/proj/bin/looptrack hook transcript"
	lines := strings.Join([]string{
		`{"type":"system","subtype":"stop_hook_summary","hookInfos":[{"command":"` + homeBin + `","durationMs":11},` +
			`{"command":"` + rootBin + `","durationMs":12}],"cwd":"/home/u/proj"}`,
		`{"type":"attachment","attachment":{"type":"hook_success","hookName":"Stop","command":"` + homeBin + `","stdout":""},"cwd":"/home/u/proj"}`,
		`{"type":"attachment","attachment":{"type":"hook_success","hookName":"Stop","command":"bin/looptrack hook stop"},"cwd":"/home/u/proj"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` + homeBin + `"}]},"cwd":"/home/u/proj"}`,
	}, "\n") + "\n"
	res, err := Convert([]byte(lines), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, Rand: fixedRand("0123456789ab")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) != 1 {
		t.Fatalf("チャンク %d 個", len(res.Chunks))
	}
	c := res.Chunks[0]
	home := "{{LT:" + c.Nonce + ":HOME}}"
	root := "{{LT:" + c.Nonce + ":ROOT0}}"
	want := map[string][]string{
		"hookInfos[].command":    {home + "/.local/bin/looptrack hook stop", root + "/bin/looptrack hook transcript"},
		"attachment.command":     {home + "/.local/bin/looptrack hook stop", "bin/looptrack hook stop"},
		"message.content[].text": {homeBin},
	}
	got := map[string][]string{}
	homeLeft := 0
	ls, _ := splitLines(c.Data)
	for _, l := range ls {
		for _, v := range stringsAt(t, l.body, claudeRule) {
			if _, ok := want[v.path]; !ok {
				continue
			}
			got[v.path] = append(got[v.path], v.v)
			if strings.HasSuffix(v.path, "command") && strings.Contains(v.v, "/home/u") {
				homeLeft++
			}
		}
	}
	for p, w := range want {
		if strings.Join(got[p], "|") != strings.Join(w, "|") {
			t.Errorf("%s: %q（期待 %q）", p, got[p], w)
		}
	}
	if homeLeft != 0 {
		t.Errorf("command の欄にホームのパスが %d 個残った", homeLeft)
	}
	if res.Stats.Rewritten != 7 || res.Stats.UnrewrittenProse != 1 || res.Stats.UnrewrittenStructural != 0 {
		t.Errorf("件数が違う（期待: 書き換え 7（cwd 4・command 3）・散文 1・構造 0）: %+v", res.Stats)
	}
	back, err := Restore(c.Data, KindJSONL, c.Nonce, fixtureTarget)
	if err != nil || string(back) != lines {
		t.Errorf("戻した結果が元と違う（%v）", err)
	}
	other, err := Restore(c.Data, KindJSONL, c.Nonce, Target{Root: "/w/v/clone", SessionDir: "/w/v/.claude/projects/-w-v-clone", VendorDir: "/w/v/.claude", Home: "/w/v"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(other, []byte(`"command":"/w/v/.local/bin/looptrack hook stop"`)) || !bytes.Contains(other, []byte(`"text":"`+homeBin+`"`)) {
		t.Errorf("別の端末への戻しが違う")
	}

	// 引数の中のパスは書き換えず、散文として数える（先頭の語だけが対象）
	in := `{"hookInfos":[{"command":"/home/u/.local/bin/looptrack hook transcript --dir /home/u/proj"}]}` + "\n"
	res, err = Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, Rand: fixedRand("0123456789ab")})
	if err != nil {
		t.Fatal(err)
	}
	wantCmd := `"command":"{{LT:` + res.Chunks[0].Nonce + `:HOME}}/.local/bin/looptrack hook transcript --dir /home/u/proj"`
	if !bytes.Contains(res.Chunks[0].Data, []byte(wantCmd)) || res.Stats.Rewritten != 1 || res.Stats.UnrewrittenProse != 1 {
		t.Errorf("引数のパスの扱いが違う: %s・%+v", res.Chunks[0].Data, res.Stats)
	}
}

// 新しい欄（書き換える表に無い、値の頭がパスの欄）は「構造の欄の書き換えなかったパス」として数え、欄の名前を出す。
func TestUnrewrittenStructuralCounted(t *testing.T) {
	in := `{"cwd":"/home/u/proj","newVendorField":{"projectDir":"/home/u/proj/x"},"note":"see /home/u/proj/x","/home/u/proj/k":1}` + "\n"
	res, err := Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints})
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.UnrewrittenStructural != 1 || st.UnrewrittenFields["newVendorField.projectDir"] != 1 || st.UnrewrittenProse != 1 || st.UnrewrittenKeys != 1 {
		t.Errorf("件数が違う: %+v", st)
	}
	if st.Rewritten != 1 {
		t.Errorf("対照: cwd の書き換えが %d 件（期待 1）", st.Rewritten)
	}
	for k := range st.UnrewrittenFields {
		if strings.Contains(k, "/home") {
			t.Errorf("欄の名前にパスが入った: %q", k)
		}
	}
}

// 変わらない行は元のバイトのまま。変わった行も、変わった文字列の外（区切り・数の字面・ほかの逃がし方）は元のまま。
func TestBytesKeptOutsideChangedStrings(t *testing.T) {
	in := `{"a": 1.50, "b":[1e-7,  true],"x":"\u0041\/y"}` + "\n" +
		`{"n":1.0,"cwd":"/home/u/proj/s","q":"\u00e9\/z","command":"x"}` + "\n" +
		`{"wireToolInputs":{"t":{"command":"\u0041 cd \"/home/u/proj\" \u00e9"}}}` + "\n"
	res, err := Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, Rand: fixedRand("aaaaaaaaaaaa")})
	if err != nil {
		t.Fatal(err)
	}
	got := string(joinChunks(res))
	want := `{"a": 1.50, "b":[1e-7,  true],"x":"\u0041\/y"}` + "\n" +
		`{"n":1.0,"cwd":"{{LT:aaaaaaaaaaaa:ROOT0}}/s","q":"\u00e9\/z","command":"x"}` + "\n" +
		`{"wireToolInputs":{"t":{"command":"\u0041 cd \"{{LT:aaaaaaaaaaaa:ROOT0}}\" \u00e9"}}}` + "\n"
	if got != want {
		t.Errorf("変換の結果:\n%s\n期待:\n%s", got, want)
	}
	back := restoreAll(t, res, KindJSONL, fixtureTarget)
	if string(back) != in {
		t.Errorf("戻した結果が元と違う:\n%s", back)
	}
}

const secretLineMarker = "FAKE-not-a-real-value-77"

// 読めない行・キーに秘密がある行が 1 つでもあれば、チャンクを返さず件数だけを返す。文面に行の中身は入らない。
func TestFailClosedCountsOnly(t *testing.T) {
	good := `{"cwd":"/home/u/proj","x":"ok"}`
	cases := []struct {
		name, in, id string
		count        int
	}{
		{"unreadable", good + "\n" + `{"cwd":"/home/u/proj","x":"` + secretLineMarker + "\n" + `not json ` + secretLineMarker + "\n" + good + "\n", "transcript.err.unreadable", 2},
		{"secret_key", good + "\n" + `{"password=` + secretLineMarker + `":"v"}` + "\n", "transcript.err.secret_key", 1},
	}
	for _, c := range cases {
		res, err := Convert([]byte(c.in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints})
		if err == nil || res != nil {
			t.Fatalf("%s: エラーにならない", c.name)
		}
		if !IsReject(err) {
			t.Errorf("%s: RejectError でない: %T", c.name, err)
		}
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Msg.ID != c.id {
			t.Errorf("%s: ID が違う: %v", c.name, err)
		}
		for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
			msg := i18n.Text(lang, err) + " " + err.Error()
			if strings.Contains(msg, secretLineMarker) || strings.Contains(msg, "/home/u") || strings.Contains(msg, "password") {
				t.Errorf("%s: 文面に行の中身が入った", c.name)
			}
			if !strings.Contains(msg, " "+itoa(c.count)) && !strings.Contains(msg, itoa(c.count)+" ") {
				t.Errorf("%s(%s): 文面に件数 %d が無い: %s", c.name, lang, c.count, msg)
			}
		}
	}
	// 対照: 同じ良い行だけなら通る
	if _, err := Convert([]byte(good+"\n"), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints}); err != nil {
		t.Errorf("対照の良い行がエラー: %v", err)
	}
}

// 書き換えとマスクの後に読めない行があれば、件数だけのエラーになる（後段を壊す差し込みで確かめる）。
func TestFailClosedAfterRewrite(t *testing.T) {
	in := `{"cwd":"/home/u/proj","x":"` + secretLineMarker + `"}` + "\n" + `{"y":1}` + "\n"
	breakAfter = func(b []byte) []byte { return append(append([]byte{}, b...), ',') }
	defer func() { breakAfter = nil }()
	res, err := Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints})
	if err == nil || res != nil || !IsReject(err) {
		t.Fatalf("エラーにならない: %v", err)
	}
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Msg.ID != "transcript.err.unreadable_after" {
		t.Fatalf("ID が違う: %v", err)
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		msg := i18n.Text(lang, err)
		if strings.Contains(msg, secretLineMarker) || strings.Contains(msg, "/home/u") || !strings.Contains(msg, "2") {
			t.Errorf("文面が違う（中身が入った・件数 2 が無い）: %s", msg)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// マスク: 値ごとにかけ、JSON は保つ。数は規則の語ごと。
func TestMaskPerValue(t *testing.T) {
	in := `{"cwd":"/home/u/proj","message":{"content":[{"type":"text","text":"export TOKEN=` + secretLineMarker + `, ok"}]}}` + "\n"
	res, err := Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints})
	if err != nil {
		t.Fatal(err)
	}
	out := joinChunks(res)
	if bytes.Contains(out, []byte(secretLineMarker)) {
		t.Fatalf("秘密が残った")
	}
	if _, err := jsonorder.Decode(bytes.TrimSpace(out)); err != nil {
		t.Fatalf("マスクの後に読めない: %v", err)
	}
	if res.Stats.MaskedLines != 1 || res.Stats.Masked["TOKEN=…"] != 1 {
		t.Errorf("マスクの件数: %+v", res.Stats)
	}
	for k := range res.Stats.Masked {
		if strings.Contains(k, "FAKE") {
			t.Errorf("件数のキーに値が入った")
		}
	}
}

// fixedRand は同じバイトを繰り返し返す（nonce は hex で 12 桁 = 6 バイト）。
type seqReader struct {
	b []byte
	i int
}

func (r *seqReader) Read(p []byte) (int, error) {
	for k := range p {
		p[k] = r.b[r.i%len(r.b)]
		r.i++
	}
	return len(p), nil
}

func fixedRand(hexNonces ...string) *seqReader {
	var b []byte
	for _, h := range hexNonces {
		for i := 0; i+1 < len(h); i += 2 {
			var v byte
			for _, c := range h[i : i+2] {
				v <<= 4
				switch {
				case c >= '0' && c <= '9':
					v |= byte(c - '0')
				default:
					v |= byte(c-'a') + 10
				}
			}
			b = append(b, v)
		}
	}
	return &seqReader{b: b}
}

// チャンクの元の中身に {{LT:<nonce>: があると nonce を作り直す。8 回で決まらなければエラー。
func TestNonceRegenerated(t *testing.T) {
	first, second := "111111111111", "222222222222"
	in := `{"cwd":"/home/u/proj","message":{"content":[{"type":"text","text":"talks about {{LT:` + first + `:ROOT0}}"}]}}` + "\n"
	res, err := Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, Rand: fixedRand(first, second)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chunks[0].Nonce != second {
		t.Fatalf("nonce %q（期待 %q）", res.Chunks[0].Nonce, second)
	}
	back := restoreAll(t, res, KindJSONL, fixtureTarget)
	if string(back) != in {
		t.Errorf("戻した結果が元と違う")
	}
	// 対照: 重ならなければ最初の nonce をそのまま使う
	res, err = Convert([]byte(`{"cwd":"/home/u/proj"}`+"\n"), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, Rand: fixedRand(first, second)})
	if err != nil || res.Chunks[0].Nonce != first {
		t.Fatalf("対照: nonce が最初のものでない（%v）", err)
	}
	// 8 回とも重なる
	_, err = Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, Rand: fixedRand(first)})
	var ie *i18n.Error
	if !IsReject(err) || !errors.As(err, &ie) || ie.Msg.ID != "transcript.err.nonce" {
		t.Fatalf("8 回で決まらないのにエラーにならない: %v", err)
	}
	// json（ファイル全体）でも同じ
	whole := `{"cwd":"/home/u/proj","x":"{{LT:` + first + `:HOME}}"}`
	res, err = Convert([]byte(whole), Options{Agent: AgentClaude, Kind: KindJSON, Hints: fixtureHints, Rand: fixedRand(first, second)})
	if err != nil || res.Chunks[0].Nonce != second {
		t.Fatalf("json: nonce が作り直されない（%v）", err)
	}
}

// 1 行が上限を超えるとエラー。上限以内なら行の境目でチャンクに分かれ、最後の改行より後ろは返さない。
func TestChunking(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString(`{"cwd":"/home/u/proj","i":` + itoa(i) + `}` + "\n")
	}
	full := b.String()
	tail := `{"cwd":"/home/u/proj","partial":`
	in := full + tail
	lineLen := len(`{"cwd":"{{LT:000000000000:ROOT0}}","i":0}`) + 1
	res, err := Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, MaxChunkBytes: lineLen*3 + 5, BaseOffset: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != len(tail) {
		t.Errorf("Pending %d（期待 %d）", res.Pending, len(tail))
	}
	if len(res.Chunks) != 4 {
		t.Fatalf("チャンク %d 個（期待 4: 3・3・3・1 行）", len(res.Chunks))
	}
	off := int64(100)
	for i, c := range res.Chunks {
		if c.Offset != off {
			t.Errorf("チャンク %d の Offset %d（期待 %d）", i, c.Offset, off)
		}
		if len(c.Data) > lineLen*3+5 || !bytes.HasSuffix(c.Data, []byte("\n")) {
			t.Errorf("チャンク %d: 大きさ %d・改行で終わらない", i, len(c.Data))
		}
		if bytes.Contains(c.Data, []byte("partial")) {
			t.Errorf("書きかけの行が入った")
		}
		off = c.End
	}
	if off != int64(100+len(full)) {
		t.Errorf("最後の End %d（期待 %d）", off, 100+len(full))
	}
	if res.Stats.Lines != 10 {
		t.Errorf("Lines %d", res.Stats.Lines)
	}
	// 1 行が上限を超える
	_, err = Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, MaxChunkBytes: lineLen - 1})
	var ie *i18n.Error
	if !IsReject(err) || !errors.As(err, &ie) || ie.Msg.ID != "transcript.err.too_long" {
		t.Fatalf("上限を超える行でエラーにならない: %v", err)
	}
	// 対照: ちょうど上限なら通る
	if _, err := Convert([]byte(in), Options{Agent: AgentClaude, Kind: KindJSONL, Hints: fixtureHints, MaxChunkBytes: lineLen}); err != nil {
		t.Errorf("ちょうど上限の行がエラー: %v", err)
	}
}

// Codex の合成の rollout。欄の名前は openai/codex のソースと、利用者の端末の実測（codex-cli 0.156.1 の rollout の
// キーと件数）に合わせた。local_shell_call は実物に無かったが、古い版の rollout のために残す。
const codexRollout = `{"timestamp":"2000-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"00000000-0000-4000-8000-0000000000c1","cwd":"/home/u/proj","cli_version":"0.0.0","runtime_workspace_roots":["/home/u/proj"]}}
{"timestamp":"2000-01-01T00:00:01.000Z","type":"turn_context","payload":{"cwd":"/home/u/proj","approval_policy":"on-request","sandbox_policy":{"type":"workspace-write","writable_roots":["/home/u/proj","/home/u/.codex/memories"]},"model":"m"}}
{"timestamp":"2000-01-01T00:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"look at /home/u/proj/a.rs"}]}}
{"timestamp":"2000-01-01T00:00:03.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"cat /home/u/proj/a.rs\"],\"workdir\":\"/home/u/proj\"}","call_id":"call_1"}}
{"timestamp":"2000-01-01T00:00:04.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"/home/u/proj/a.rs: fn main() {}"}}
{"timestamp":"2000-01-01T00:00:05.000Z","type":"response_item","payload":{"type":"local_shell_call","call_id":"c2","status":"completed","action":{"type":"exec","command":["ls","/home/u/proj"],"working_directory":"/home/u/proj"}}}
{"timestamp":"2000-01-01T00:00:06.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","cwd":"/home/u/proj"}}
{"timestamp":"2000-01-01T00:00:07.000Z","type":"turn_context","payload":{"workspace_roots":["/home/u/proj"],"file_system_sandbox_policy":{"entries":[{"path":{"type":"path","path":"/home/u/proj"},"access":"write"},{"path":{"type":"path","path":"/home/u/.codex/memories"},"access":"write"}]},"permission_profile":{"file_system":{"entries":[{"path":{"type":"path","path":"/home/u/proj"},"access":"write"}]}}}}
{"timestamp":"2000-01-01T00:00:08.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"permission_profile":{"file_system":{"entries":[{"path":{"type":"path","path":"/home/u/proj"},"access":"write"}]}}}}}
{"timestamp":"2000-01-01T00:00:09.000Z","type":"world_state","payload":{"state":{"environments":{"environments":{"local":{"cwd":"/home/u/proj"}}},"agents_md":{"directory":"/home/u/proj"},"recent":{"/home/u/proj/c.rs":1}}}}
{"timestamp":"2000-01-01T00:00:10.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"command_execution","command":["/bin/zsh","-lc","cat /home/u/proj/a.rs"],"parsed_cmd":[{"type":"read","cmd":"cat /home/u/proj/a.rs","name":"a.rs","path":"/home/u/proj/a.rs"}],"aggregated_output":"/home/u/proj/a.rs: x"}}}
{"timestamp":"2000-01-01T00:00:11.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"file_change","changes":{"/home/u/proj/a.rs":{"type":"update","unified_diff":"@@ -1 +1 @@\n-a\n+b\n","move_path":"/home/u/proj/b.rs"},"/home/u/proj/x.md":{"type":"add","content":"see /home/u/proj"}}}}}
{"timestamp":"2000-01-01T00:00:12.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_3","output":{"executed_tool_calls":[{"name":"shell","arguments":{"cmd":"ls /home/u/proj","workdir":"/home/u/proj"}}]}}}
{"timestamp":"2000-01-01T00:00:13.000Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"c4","name":"apply_patch","input":"*** Begin Patch\n*** Update File: /home/u/proj/a.rs\n@@\n-a\n+b\n*** End Patch\n"}}
{"timestamp":"2000-01-01T00:00:14.000Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"c5","name":"exec","input":"cd /home/u/proj && ls /home/u/.codex"}}
{"timestamp":"2000-01-01T00:00:15.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"other","input":{"note":"/home/u/proj/n"}}],"meta":{"arguments":{"note":"/home/u/proj/m"}},"entries":[{"path":{"path":"/home/u/proj/e"}}]}}
`

var codexHints = Hints{Roots: []string{"/home/u/proj"}, VendorDir: "/home/u/.codex", Home: "/home/u"}

// Codex: session_meta.payload.cwd・turn_context.payload.cwd・function_call.arguments の中のパスが書き換わる（要実測）。
func TestCodexSyntheticRollout(t *testing.T) {
	res, err := Convert([]byte(codexRollout), Options{Agent: AgentCodex, Kind: KindJSONL, Hints: codexHints, Rand: fixedRand("cccccccccccc")})
	if err != nil {
		t.Fatal(err)
	}
	ph := "{{LT:cccccccccccc:ROOT0}}"
	vd := "{{LT:cccccccccccc:VDIR}}"
	conv := joinChunks(res)
	ls, _ := splitLines(conv)
	got := map[string][]string{}
	for _, l := range ls {
		for _, v := range stringsAt(t, l.body, codexRule) {
			got[v.path] = append(got[v.path], v.v)
		}
	}
	checks := map[string]string{
		"payload.cwd":                             ph, // session_meta・turn_context・thread_settings_applied
		"payload.runtime_workspace_roots[]":       ph,
		"payload.sandbox_policy.writable_roots[]": ph + "|" + vd + "/memories",
		"payload.arguments":                       `{"command":["bash","-lc","cat ` + ph + `/a.rs"],"workdir":"` + ph + `"}`,
		"payload.action.command[]":                "ls|" + ph,
		"payload.action.working_directory":        ph,
		"payload.content[].text":                  "look at /home/u/proj/a.rs",
		"payload.output":                          "/home/u/proj/a.rs: fn main() {}",
		// 実測で足した欄
		"payload.workspace_roots[]":                                                  ph,
		"payload.file_system_sandbox_policy.entries[].path.path":                     ph + "|" + vd + "/memories",
		"payload.permission_profile.file_system.entries[].path.path":                 ph,
		"payload.thread_settings.permission_profile.file_system.entries[].path.path": ph,
		"payload.state.environments.environments.local.cwd":                          ph,
		"payload.state.agents_md.directory":                                          ph,
		"payload.item.command[]":                                                     "/bin/zsh|-lc|cat " + ph + "/a.rs",
		"payload.item.parsed_cmd[].cmd":                                              "cat " + ph + "/a.rs",
		"payload.item.parsed_cmd[].path":                                             ph + "/a.rs",
		"payload.item.changes.*.move_path":                                           ph + "/b.rs",
		"payload.output.executed_tool_calls[].arguments.workdir":                     ph,
		"payload.output.executed_tool_calls[].arguments.cmd":                         "ls " + ph,
		"payload.input": "*** Begin Patch\n*** Update File: " + ph + "/a.rs\n@@\n-a\n+b\n*** End Patch\n|cd " + ph + " && ls " + vd,
		// 対照: 散文の欄（コマンドの出力・追加したファイルの中身）の同じパスは変えない
		"payload.item.aggregated_output": "/home/u/proj/a.rs: x",
		"payload.item.changes.*.content": "see /home/u/proj",
		// 対照: 絞った外側（payload の直下でない input・executed_tool_calls の外の arguments・書き込める根の外の entries）は変えない
		"payload.content[].input.note": "/home/u/proj/n",
		"payload.meta.arguments.note":  "/home/u/proj/m",
		"payload.entries[].path.path":  "/home/u/proj/e",
	}
	for p, w := range checks {
		vals := got[p]
		if len(vals) == 0 {
			t.Errorf("前提が崩れています: 欄 %s が無い", p)
			continue
		}
		uniq := map[string]bool{}
		var list []string
		for _, v := range vals {
			if !uniq[v] {
				uniq[v] = true
				list = append(list, v)
			}
		}
		if strings.Join(list, "|") != w {
			t.Errorf("%s: %q（期待 %q）", p, strings.Join(list, "|"), w)
		}
	}
	if len(got["payload.cwd"]) != 3 {
		t.Errorf("payload.cwd が %d 個（期待 3: session_meta・turn_context・thread_settings_applied）", len(got["payload.cwd"]))
	}
	// item.changes のキー（変えたファイルの絶対パス）は頭を書き換える。対照: changes の外のキーは書き換えずに数える
	if !strings.Contains(string(conv), `"changes":{"`+ph+`/a.rs":{`) || !strings.Contains(string(conv), `,"`+ph+`/x.md":{`) {
		t.Errorf("changes のキーが書き換わっていない")
	}
	if !strings.Contains(string(conv), `"recent":{"/home/u/proj/c.rs":1}`) {
		t.Errorf("changes の外のキーが変わった")
	}
	if res.Stats.UnrewrittenKeys != 1 {
		t.Errorf("書き換えなかったキーのパス %d（期待 1: recent のキー）", res.Stats.UnrewrittenKeys)
	}
	if res.Stats.UnrewrittenStructural != 3 || len(res.Stats.UnrewrittenFields) != 3 {
		t.Errorf("書き換えなかった構造の欄 %d（期待 3: 絞った外側の対照だけ）: %v", res.Stats.UnrewrittenStructural, res.Stats.UnrewrittenFields)
	}
	if back := restoreAll(t, res, KindJSONL, Target{Root: "/home/u/proj", VendorDir: "/home/u/.codex", Home: "/home/u"}); string(back) != codexRollout {
		t.Errorf("戻した結果が元と違う")
	}
	// 別の端末の値に戻すと、changes のキーもその端末のパスになる
	other := restoreAll(t, res, KindJSONL, Target{Root: "/w/v/clone", VendorDir: "/w/v/.codex", Home: "/w/v"})
	if !bytes.Contains(other, []byte(`"changes":{"/w/v/clone/a.rs":{`)) || !bytes.Contains(other, []byte(`"move_path":"/w/v/clone/b.rs"`)) {
		t.Errorf("別の端末への戻しが違う")
	}
}

// item.changes のキーは、すべてが同じ 1 つの種類に書き換わるときだけ書き換える。種類が混ざると（ROOT0 と ROOT1、
// ROOT0 と HOME）取り込む端末で同じキーが 2 つでき、値が黙って失われるので、キーを元のまま残して数える。
// 同じテストに対照: キーがすべて ROOT0 に当たる changes は書き換わり、別の端末へ戻してもキーの数が変わらない。
func TestCodexChangesKeysOverlapKept(t *testing.T) {
	in := `{"type":"event_msg","payload":{"type":"item_completed","item":{"changes":{"/tmp/r/a.rs":{"type":"add","content":"a"},"/private/tmp/r/a.rs":{"type":"add","content":"b"}}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"changes":{"/tmp/r/b.rs":{"type":"add","content":"a"},"/home/u/clone/b.rs":{"type":"add","content":"b"}}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"changes":{"/tmp/r/c.rs":{"type":"add","content":"a"},"/tmp/r/d.rs":{"type":"add","content":"b"}}}}}` + "\n"
	hints := Hints{Roots: []string{"/tmp/r", "/private/tmp/r"}, Home: "/home/u"}
	res, err := Convert([]byte(in), Options{Agent: AgentCodex, Kind: KindJSONL, Hints: hints, Rand: fixedRand("eeeeeeeeeeee")})
	if err != nil {
		t.Fatal(err)
	}
	conv := joinChunks(res)
	ls, _ := splitLines(conv)
	if len(ls) != 3 {
		t.Fatalf("前提が崩れています: 行が %d", len(ls))
	}
	orig, _ := splitLines([]byte(in))
	for i := 0; i < 2; i++ {
		if string(ls[i].body) != string(orig[i].body) {
			t.Errorf("行 %d: 種類の混ざる changes のキーが書き換わった: %s", i+1, ls[i].body)
		}
	}
	ph := "{{LT:eeeeeeeeeeee:ROOT0}}"
	if !bytes.Contains(ls[2].body, []byte(`"changes":{"`+ph+`/c.rs":{`)) || !bytes.Contains(ls[2].body, []byte(`,"`+ph+`/d.rs":{`)) {
		t.Errorf("前提が崩れています: 種類が 1 つの changes のキーが書き換わっていない: %s", ls[2].body)
	}
	if res.Stats.UnrewrittenKeys != 4 {
		t.Errorf("書き換えなかったキーのパス %d（期待 4: 行 1・2 のキー）", res.Stats.UnrewrittenKeys)
	}
	if back := restoreAll(t, res, KindJSONL, Target{Root: "/tmp/r", Home: "/home/u"}); string(back) != in {
		t.Errorf("戻した結果が元と違う:\n%s", back)
	}
	// 別の端末（ルートがホームの下）へ戻しても、どの changes もキーが 2 つのまま
	other := restoreAll(t, res, KindJSONL, Target{Root: "/w/clone", Home: "/w"})
	ols, _ := splitLines(other)
	for i, l := range ols {
		var v struct {
			Payload struct {
				Item struct {
					Changes map[string]any `json:"changes"`
				} `json:"item"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(l.body, &v); err != nil {
			t.Fatal(err)
		}
		if n := len(v.Payload.Item.Changes); n != 2 {
			t.Errorf("行 %d: 戻した changes のキーが %d（期待 2）", i+1, n)
		}
	}
}

// Windows のパス: arguments（JSON の文字列の中の JSON）では \ が 2 重に逃がされた形で現れるので、J の種類で書き換えて戻す。
func TestCodexWindowsEscapedArguments(t *testing.T) {
	in := `{"type":"turn_context","payload":{"cwd":"C:\\Users\\me\\proj"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call","arguments":"{\"command\":\"type C:\\\\Users\\\\me\\\\proj\\\\a.rs\",\"workdir\":\"C:\\\\Users\\\\me\\\\proj\"}"}}` + "\n"
	res, err := Convert([]byte(in), Options{Agent: AgentCodex, Kind: KindJSONL, Hints: Hints{Roots: []string{`C:\Users\me\proj`}, Home: `C:\Users\me`}, Rand: fixedRand("dddddddddddd")})
	if err != nil {
		t.Fatal(err)
	}
	conv := string(joinChunks(res))
	if strings.Contains(conv, `Users`) {
		t.Errorf("Windows のパスが残った: %s", conv)
	}
	if !strings.Contains(conv, "{{LT:dddddddddddd:ROOT0J}}") || !strings.Contains(conv, `"cwd":"{{LT:dddddddddddd:ROOT0}}"`) {
		t.Errorf("種類が違う: %s", conv)
	}
	back := restoreAll(t, res, KindJSONL, Target{Root: `C:\Users\me\proj`, Home: `C:\Users\me`})
	if string(back) != in {
		t.Errorf("戻した結果が元と違う:\n%s", back)
	}
	// 戻した arguments の中身は JSON として読める（別の端末の値でも）
	back = restoreAll(t, res, KindJSONL, Target{Root: `D:\w\clone`, Home: `D:\w`})
	ls, _ := splitLines(back)
	obj, err := jsonorder.DecodeObject(ls[1].body)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := jsonorder.DecodeObject([]byte(obj.Object("payload").String("arguments")))
	if err != nil || inner.String("workdir") != `D:\w\clone` || inner.String("command") != `type D:\w\clone\a.rs` {
		t.Errorf("戻した arguments が違う（%v）", err)
	}
}

// 戻せない種類（値が無い・知らない種類）があれば、何も返さずにエラーにする。
func TestRestoreUnknownOrMissing(t *testing.T) {
	n := "abcdefabcdef"
	if _, err := Restore([]byte(`{"x":"{{LT:`+n+`:SDIR}}"}`+"\n"), KindJSONL, n, Target{Root: "/r", Home: "/h"}); err == nil {
		t.Errorf("SDIR の値が無いのに通った")
	}
	if _, err := Restore([]byte(`{"x":"{{LT:`+n+`:WHAT}}"}`+"\n"), KindJSONL, n, fixtureTarget); err == nil {
		t.Errorf("知らない種類が通った")
	}
	if _, err := Restore([]byte(`{"x":1}`), KindJSONL, "XYZ", fixtureTarget); err == nil {
		t.Errorf("nonce の形が違うのに通った")
	}
	// 対照: 値があれば戻る。違う nonce の字面は触らない
	got, err := Restore([]byte(`{"x":"{{LT:`+n+`:SDIR}}/a","y":"{{LT:000000000000:SDIR}}"}`+"\n"), KindJSONL, n, fixtureTarget)
	if err != nil || string(got) != `{"x":"/home/u/.claude/projects/-home-u-proj/a","y":"{{LT:000000000000:SDIR}}"}`+"\n" {
		t.Errorf("対照の戻しが違う: %s（%v）", got, err)
	}
}

// json（ファイル全体）と text（マスクだけ）。
func TestWholeJSONAndText(t *testing.T) {
	js := "{\n  \"cwd\": \"/home/u/proj\",\n  \"n\": 1.0\n}\n"
	res, err := Convert([]byte(js), Options{Agent: AgentClaude, Kind: KindJSON, Hints: fixtureHints})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) != 1 || bytes.Contains(res.Chunks[0].Data, []byte("/home/u")) || !bytes.Contains(res.Chunks[0].Data, []byte("\"n\": 1.0\n}")) {
		t.Errorf("json の変換が違う: %s", res.Chunks[0].Data)
	}
	if back, err := Restore(res.Chunks[0].Data, KindJSON, res.Chunks[0].Nonce, fixtureTarget); err != nil || string(back) != js {
		t.Errorf("json の戻しが違う（%v）", err)
	}
	if _, err := Convert([]byte(`{"cwd":`), Options{Agent: AgentClaude, Kind: KindJSON, Hints: fixtureHints}); !IsReject(err) {
		t.Errorf("読めない json が通った: %v", err)
	}
	txt := "out /home/u/proj/a\npassword: " + secretLineMarker + "\n"
	res, err = Convert([]byte(txt), Options{Agent: AgentClaude, Kind: KindText, Hints: fixtureHints})
	if err != nil {
		t.Fatal(err)
	}
	d := string(res.Chunks[0].Data)
	if strings.Contains(d, secretLineMarker) || !strings.Contains(d, "out /home/u/proj/a") || res.Chunks[0].Nonce != "" {
		t.Errorf("text の変換が違う（パスは書き換えず、秘密だけをマスクする）")
	}
}
