package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// 合成の入力（実在しない利用者 alice の端末を模したもの）。実際の記録はテストに入れない。
var testEnv = Env{Home: "/home/alice", ClaudeDir: "/home/alice/.claude", CodexDir: "/home/alice/.codex"}

const (
	sess = "11111111-2222-4333-8444-555555555555"
	u1   = "aaaaaaaa-0000-4000-8000-000000000001"
	u2   = "aaaaaaaa-0000-4000-8000-000000000002"
	u3   = "aaaaaaaa-0000-4000-8000-000000000003"
	u4   = "aaaaaaaa-0000-4000-8000-000000000004"
	u5   = "aaaaaaaa-0000-4000-8000-000000000005"
	u6   = "aaaaaaaa-0000-4000-8000-000000000006"
)

func line(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

type obj = map[string]any

func syntheticInputs(t *testing.T) []Input {
	t.Helper()
	main := line(t, obj{"type": "user", "uuid": u1, "parentUuid": nil, "sessionId": sess, "cwd": "/home/alice/work/app",
		"timestamp": "2026-01-02T03:04:05.678Z", "version": "9.9.9", "isSidechain": false,
		"message": obj{"role": "user", "content": "please fix /home/alice/work/app/internal/x.go and mail alice@example.com"}}) +
		line(t, obj{"type": "assistant", "uuid": u2, "parentUuid": u1, "sessionId": sess, "cwd": "/home/alice/work/app",
			"message": obj{"id": "msg_abcdef123", "role": "assistant", "content": []any{
				obj{"type": "tool_use", "id": "toolu_first01", "name": "Bash", "input": obj{
					"command": "cd /home/alice/work/app && go test ./... > /tmp/alice-out.log", "description": "run the tests"}},
				obj{"type": "tool_use", "id": "toolu_edit02", "name": "Edit", "input": obj{
					"file_path": "/home/alice/work/app/internal/x.go", "old_string": "foo", "new_string": "barbaz"}},
			}},
			"usage": obj{"input_tokens": 1234}, "wireToolInputs": obj{"toolu_first01": obj{"timeout": 5}}}) +
		`{"type":"user","uuid":"` + u3 + `","parentUuid":"` + u2 + `","sessionId":"` + sess + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_first01","content":"ok  example.com/app 0.12s\npassword: hunter2-swordfish"}]},"toolUseResult":{"persistedOutputPath":"/home/alice/.claude/projects/-home-alice-work-app/` + sess + `/tool-results/abc.txt","stdout":"xyz1","ratio":0.50}}` + "\n" +
		line(t, obj{"type": "attachment", "uuid": u4, "parentUuid": u3, "sessionId": sess, "attachment": obj{
			"files":                 []any{obj{"path": "/home/alice/work/app/README.md"}},
			"trackedFileBackups":    obj{"/home/alice/work/app/internal/x.go": obj{"backupFileName": "deadbeef01@v2", "version": 1}},
			"mcp__alice_host__tool": true,
			"block":                 obj{"type": "text", "text": "hello there"},
		}}) +
		line(t, obj{"type": "system", "uuid": u5, "parentUuid": u4, "sessionId": sess, "level": "info",
			"content": `{"cwd":"/home/alice/work/app","note":"internal plan","name":"alphaproj","byProject":{"alphaproj":1}}`})
	sub := line(t, obj{"type": "user", "uuid": u6, "parentUuid": nil, "sessionId": sess, "agentId": "a1b2c3d4e5f6a7b8c",
		"isSidechain": true, "cwd": "/home/alice/work/app", "message": obj{"role": "user", "content": "look at /home/alice/work/app/docs"}})
	codex := line(t, obj{"timestamp": "2026-01-02T03:04:05.678Z", "type": "session_meta", "payload": obj{
		"id": "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", "cwd": "/home/alice/work/app", "cli_version": "0.1.0"}}) +
		line(t, obj{"type": "response_item", "payload": obj{"type": "function_call", "name": "shell",
			"arguments": `{"command":["bash","-lc","cat /home/alice/work/app/go.mod"],"workdir":"/home/alice/work/app"}`}})
	return []Input{
		{Path: "/home/alice/.claude/projects/-home-alice-work-app/" + sess + ".jsonl", Data: []byte(main)},
		{Path: "/home/alice/.claude/projects/-home-alice-work-app/" + sess + "/subagents/agent-a1b2c3d4e5f6a7b8c.jsonl", Data: []byte(sub)},
		{Path: "/home/alice/.codex/sessions/2026/01/02/rollout-x.jsonl", Data: []byte(codex)},
	}
}

func decodeLines(t *testing.T, data []byte) []*jsonorder.Object {
	t.Helper()
	var out []*jsonorder.Object
	for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		o, err := jsonorder.DecodeObject([]byte(l))
		if err != nil {
			t.Fatalf("写しの行が JSON として読めません: %v", err)
		}
		out = append(out, o)
	}
	return out
}

// get は . 区切りの道筋で値を取る（数字は配列の位置）。
func get(t *testing.T, v any, path string) any {
	t.Helper()
	for _, p := range strings.Split(path, ".") {
		switch x := v.(type) {
		case *jsonorder.Object:
			v, _ = x.Get(p)
		case []any:
			var i int
			for _, c := range p {
				i = i*10 + int(c-'0')
			}
			v = x[i]
		default:
			t.Fatalf("%s: %s の手前で入れ子が終わっています", path, p)
		}
	}
	return v
}

func convertSynthetic(t *testing.T) ([]Input, []Output) {
	t.Helper()
	inputs := syntheticInputs(t)
	outs, _, err := Convert(testEnv, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return inputs, outs
}

func TestConvertSynthetic(t *testing.T) {
	inputs, outs := convertSynthetic(t)
	if err := Verify(testEnv, inputs, outs); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(outs) != 3 {
		t.Fatalf("出力のファイル %d 個、期待は 3", len(outs))
	}
	mainName := "claude/" + fakeUUIDPrefix + "000000000001.jsonl"
	if outs[0].Name != mainName {
		t.Errorf("本体の名前 %q、期待は %q", outs[0].Name, mainName)
	}
	for i, in := range inputs {
		if a, b := bytes.Count(in.Data, []byte("\n")), bytes.Count(outs[i].Data, []byte("\n")); a != b {
			t.Errorf("%s: 行数が %d → %d に変わりました", outs[i].Name, a, b)
		}
	}
	m := decodeLines(t, outs[0].Data)
	sub := decodeLines(t, outs[1].Data)
	cx := decodeLines(t, outs[2].Data)

	// 等しさの関係（親子の鎖・sessionId・tool_use と tool_result）が残る。
	if get(t, m[1], "parentUuid") != get(t, m[0], "uuid") || get(t, m[2], "parentUuid") != get(t, m[1], "uuid") {
		t.Error("parentUuid → uuid の鎖が切れました")
	}
	if get(t, m[0], "parentUuid") != nil {
		t.Error("null の parentUuid が null のままではありません")
	}
	if get(t, sub[0], "sessionId") != get(t, m[0], "sessionId") || get(t, m[0], "sessionId") != fakeUUIDPrefix+"000000000001" {
		t.Errorf("sessionId が本体とサブエージェントでそろっていません: %v / %v", get(t, m[0], "sessionId"), get(t, sub[0], "sessionId"))
	}
	if get(t, m[2], "message.content.0.tool_use_id") != get(t, m[1], "message.content.0.id") {
		t.Error("tool_result の tool_use_id が tool_use の id と一致しません")
	}
	if get(t, m[0], "type") != get(t, m[2], "type") || get(t, m[0], "type") == get(t, m[1], "type") {
		t.Error("type の値の等しさ（user と user・user と assistant）が残っていません")
	}
	wantSub := "claude/" + fakeUUIDPrefix + "000000000001/subagents/agent-" + get(t, sub[0], "agentId").(string) + ".jsonl"
	if outs[1].Name != wantSub {
		t.Errorf("サブエージェントの名前 %q、期待は %q（agentId の写しと同じ）", outs[1].Name, wantSub)
	}

	// パス: 値全体のパス・値の中のパス・設定の置き場の slug・JSON の文字列の中。
	if get(t, m[0], "cwd") != fakeRoot || get(t, sub[0], "cwd") != fakeRoot {
		t.Errorf("cwd の写し %v、期待は %s", get(t, m[0], "cwd"), fakeRoot)
	}
	edit := get(t, m[1], "message.content.1.input.file_path").(string)
	if !regexp.MustCompile(`^/home/u/proj/c\d+/c\d+\.go$`).MatchString(edit) {
		t.Errorf("Edit の file_path の写し %q", edit)
	}
	if got := get(t, m[0], "message.content").(string); got != regexp.MustCompile(`^s\d{6} `).FindString(got)+edit {
		t.Errorf("散文の中のパスの写し %q（Edit の file_path %q と同じ写しになるはず）", got, edit)
	}
	cmd := get(t, m[1], "message.content.0.input.command").(string)
	if !regexp.MustCompile(`^s\d{6} /home/u/proj /var/x/c\d+/c\d+\.log$`).MatchString(cmd) {
		t.Errorf("Bash の command の写し %q", cmd)
	}
	pop := get(t, m[2], "toolUseResult.persistedOutputPath").(string)
	if !regexp.MustCompile(`^/home/u/\.claude/projects/-home-u-proj/c\d+/c\d+/c\d+\.txt$`).MatchString(pop) {
		t.Errorf("persistedOutputPath の写し %q", pop)
	}
	if p := get(t, m[3], "attachment.files.0.path").(string); !regexp.MustCompile(`^/home/u/proj/c\d+\.md$`).MatchString(p) {
		t.Errorf("attachment.files[].path の写し %v", p)
	}
	inner, err := jsonorder.DecodeObject([]byte(get(t, m[4], "content").(string)))
	if err != nil || inner.String("cwd") != fakeRoot || !regexp.MustCompile(`^s\d{6}$`).MatchString(inner.String("note")) {
		t.Errorf("JSON の文字列の中身が写されていません: %v", get(t, m[4], "content"))
	}
	args, err := jsonorder.DecodeObject([]byte(get(t, cx[1], "payload.arguments").(string)))
	if err != nil || args.String("workdir") != fakeRoot || !regexp.MustCompile(`^s\d{6} /home/u/proj/c\d+\.mod$`).MatchString(get(t, args, "command.2").(string)) {
		t.Errorf("Codex の arguments の写し: %v", get(t, cx[1], "payload.arguments"))
	}
	if get(t, cx[0], "payload.cwd") != fakeRoot || outs[2].Name != "codex/rollout-2000-01-01T00-00-00-"+get(t, cx[0], "payload.id").(string)+".jsonl" {
		t.Errorf("Codex の cwd・名前: %v・%s", get(t, cx[0], "payload.cwd"), outs[2].Name)
	}

	// 数・真偽値・null は字面のまま。
	for _, want := range []string{`"input_tokens":1234`, `"ratio":0.50`, `"parentUuid":null`, `"isSidechain":false`, `"version":1`} {
		if !bytes.Contains(outs[0].Data, []byte(want)) {
			t.Errorf("写しに %s がありません", want)
		}
	}
	if bytes.Contains(outs[0].Data, []byte(`"mcp__`)) {
		t.Error("__ を含むキーが置き換わっていません")
	}
	// データから作られたキーは値と同じ規則で写る（パスのキーは同じパスの値の写しと、id で引くキーは id の写しと一致する）。
	keys := get(t, m[3], "attachment.trackedFileBackups").(*jsonorder.Object).Keys()
	if len(keys) != 1 || keys[0] != edit {
		t.Errorf("パスのキーの写し %q、期待は file_path の写し %q", keys, edit)
	}
	if k := get(t, m[1], "wireToolInputs").(*jsonorder.Object).Keys(); len(k) != 1 || k[0] != get(t, m[1], "message.content.0.id") {
		t.Errorf("id で引くキーの写し %q が tool_use の id の写し %v と一致しません", k, get(t, m[1], "message.content.0.id"))
	}
	// 構造の語は、同じ綴りの値（type の text）があってもキーとして残る（対照: 値の側は置き換わる）。
	if b := get(t, m[3], "attachment.block").(*jsonorder.Object); !b.Has("text") || b.String("type") == "text" {
		t.Errorf("構造の語のキーと値の扱い: %v", b.Keys())
	}
	// JSON の文字列の中では、値と同じ綴りのキーは数字が無くても置き換わり、値の写しと一致する。
	if bp := inner.Object("byProject"); bp == nil || len(bp.Keys()) != 1 || bp.Keys()[0] != inner.String("name") || inner.String("name") == "alphaproj" {
		t.Errorf("JSON の文字列の中の値から作られたキーの写し: %v", get(t, m[4], "content"))
	}
	if b, _ := get(t, m[3], "attachment.trackedFileBackups").(*jsonorder.Object).Get(keys[0]); get(t, b, "version") != jsonorder.Number("1") {
		t.Error("置き換えたキーの下の構造が残っていません")
	}
}

// 実装と違う読み方（encoding/json）で、元の値（4 文字以上）が写しのどの文字列の値にも含まれないことを確かめる。
func TestNoOriginalValueRemains(t *testing.T) {
	inputs, outs := convertSynthetic(t)
	var walk func(v any, onStr, onKey func(string))
	walk = func(v any, onStr, onKey func(string)) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				onKey(k)
				walk(e, onStr, onKey)
			}
		case []any:
			for _, e := range x {
				walk(e, onStr, onKey)
			}
		case string:
			var inner any
			if t := strings.TrimSpace(x); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
				if json.Unmarshal([]byte(t), &inner) == nil {
					walk(inner, onStr, onKey)
					return
				}
			}
			onStr(x)
		}
	}
	each := func(data []byte, onStr, onKey func(string)) {
		for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			var v any
			if err := json.Unmarshal([]byte(l), &v); err != nil {
				t.Fatal(err)
			}
			walk(v, onStr, onKey)
		}
	}
	var originals []string
	for _, in := range inputs {
		each(in.Data, func(s string) {
			if len([]rune(s)) >= 4 {
				originals = append(originals, s)
			}
		}, func(string) {})
	}
	var outVals []string
	for _, o := range outs {
		each(o.Data, func(s string) { outVals = append(outVals, s) }, func(k string) {
			if isSynthetic(k) {
				outVals = append(outVals, k)
			}
		})
	}
	if len(originals) < 30 || len(outVals) < 30 {
		t.Fatalf("前提が崩れています: 元の値 %d 個・写しの値 %d 個（入力の読み方が壊れている）", len(originals), len(outVals))
	}
	for _, o := range originals {
		for _, v := range outVals {
			if strings.Contains(v, o) {
				t.Errorf("元の値が写しに残っています（元の値の長さ %d）", len(o))
			}
		}
	}
	// 対照: 写しに元の値を 1 つ戻すと Verify が失敗し、文面に値が出ない。
	bad := append([]Output(nil), outs...)
	bad[0].Data = bytes.Replace(append([]byte(nil), outs[0].Data...), []byte(`"`+fakeRoot+`"`), []byte(`"/home/alice/work/app"`), 1)
	if bytes.Equal(bad[0].Data, outs[0].Data) {
		t.Fatal("前提が崩れています: 対照の写しに元の値を戻せませんでした")
	}
	err := Verify(testEnv, inputs, bad)
	if err == nil {
		t.Fatal("元の値を戻した写しを Verify が通しました")
	}
	if strings.Contains(err.Error(), "alice") {
		t.Errorf("Verify の文面に元の値が出ています: %v", err)
	}
	// 対照: 写しに秘密らしい値があれば失敗する（元の値でなくても）。
	bad[0].Data = bytes.Replace(append([]byte(nil), outs[0].Data...), []byte(`"`+fakeRoot+`"`), []byte(`"token=zzzzzzzz"`), 1)
	if err := Verify(testEnv, inputs, bad); err == nil {
		t.Fatal("秘密らしい値を持つ写しを Verify が通しました")
	}
}

// 値としては現れず、キーとしてだけ現れる名前（ツールの出力の表のキーなど）は規則では見分けられない。-mask-key で置き換える。
func TestMaskKey(t *testing.T) {
	in := []Input{{Path: "/home/alice/x.jsonl", Data: []byte(line(t, obj{"sessionId": sess, "projects": obj{"alphaproj": obj{"open": 1}}}))}}
	for _, c := range []struct {
		mask []string
		keep bool
	}{{nil, true}, {[]string{"alphaproj"}, false}} {
		env := testEnv
		env.MaskKeys = c.mask
		outs, st, err := Convert(env, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := Verify(env, in, outs); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		has := bytes.Contains(outs[0].Data, []byte("alphaproj"))
		listed := strings.Contains(strings.Join(st.KeptKeys, " "), "alphaproj")
		if has != c.keep || listed != c.keep {
			t.Errorf("mask %v: 写しに残る %v・残したキーの一覧に出る %v、期待はどちらも %v", c.mask, has, listed, c.keep)
		}
	}
}

func TestConvertDeterministic(t *testing.T) {
	_, a := convertSynthetic(t)
	_, b := convertSynthetic(t)
	for i := range a {
		if a[i].Name != b[i].Name || !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("%s: 同じ入力から違う写しができました", a[i].Name)
		}
	}
}

func TestPathMapping(t *testing.T) {
	s := newSynth(testEnv)
	s.env.Roots = []string{"/home/alice/work/app", "/srv/other", "/home/alice/work/app/sub"}
	s.decideRoots(nil)
	if len(s.roots) != 2 {
		t.Fatalf("ルート %d 個、期待は 2（ほかのルートの下にあるものは除く）", len(s.roots))
	}
	// 名前の番号は出てきた順に決まるので、表は順に並べる。
	for _, c := range [][2]string{
		{"/home/alice/work/app", "/home/u/proj"},
		{"/home/alice/work/app/", "/home/u/proj/"},
		{"/home/alice/work/app/sub/a.go", "/home/u/proj/c1/c2.go"},
		{"/home/alice/work/app/.git", "/home/u/proj/.c3"},
		{"/srv/other/a.go", "/home/u/proj2/c2.go"},
		{"/home/alice/.claude/projects/-srv-other/x.json", "/home/u/.claude/projects/-home-u-proj2/c4.json"},
		{"/home/alice/.claude/settings.json", "/home/u/.claude/c5.json"},
		{"/home/alice/.codex/sessions/2026", "/home/u/.codex/c6/c7"},
		{"/home/alice/notes/a.go", "/home/u/c8/c2.go"},
		{"/tmp/alice/a.weird", "/var/x/c9/c10/c11"},
		{"/home/alice/work/app/../app", "/home/u/proj/../c12"},
	} {
		if got := s.path(c[0]); got != c[1] {
			t.Errorf("path(%q) = %q、期待は %q", c[0], got, c[1])
		}
	}
}

func TestCompleteLines(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"a\nb\nc", 0, "a\nb\n"},
		{"a\nb\nc\n", 2, "a\nb\n"},
		{"a\nb\n", 5, "a\nb\n"},
		{"abc", 0, ""},
	} {
		if got := string(completeLines([]byte(c.in), c.max)); got != c.want {
			t.Errorf("completeLines(%q, %d) = %q、期待は %q", c.in, c.max, got, c.want)
		}
	}
}

func TestKeepKey(t *testing.T) {
	for k, want := range map[string]bool{
		"cwd": true, "parentUuid": true, "input_tokens": true, "$schema": true, "content-type": true,
		"/home/alice/x.go": false, "mcp__a__b": false, "a1b2c3d4e5f6": false, "item20261025": false,
		"has space": false, "token=abcdef": false, "": false,
	} {
		if got := keepKey(k); got != want {
			t.Errorf("keepKey(%q) = %v、期待は %v", k, got, want)
		}
	}
}

// ── 固定の材料の検査 ──

const materialDir = "../../client/transcript/testdata"

var (
	synthPathRe = `(?:/home/u/proj\d*|/home/u/\.claude/projects/-home-u-proj\d*|/home/u/\.claude|/home/u/\.codex|/home/u|/var/x)` +
		`(?:/(?:\.\.?|\.?c\d+(?:` + strings.ReplaceAll(strings.Join(knownExts, "|"), ".", `\.`) + `)?))*/?`
	synthValueRe = regexp.MustCompile(`^(?:|s\d{6}(?: ` + synthPathRe + `)*|` + fakeUUIDPrefix + `\d{12}|` + synthPathRe + `)$`)
)

// isSynthetic は、文字列の値が道具の出す合成の値だけでできているか（JSON の文字列なら中身をたどる）。
func isSynthetic(s string) bool {
	if t := strings.TrimSpace(s); len(t) >= 2 && (t[0] == '{' || t[0] == '[') {
		if v, err := jsonorder.Decode([]byte(t)); err == nil {
			ok := true
			collect(v, func(x string) { ok = ok && synthValueRe.MatchString(x) }, func(k string) { ok = ok && (keepKey(k) || synthValueRe.MatchString(k)) })
			return ok
		}
	}
	return synthValueRe.MatchString(s)
}

func TestIsSynthetic(t *testing.T) {
	for s, want := range map[string]bool{
		"": true, "s000001": true, fakeUUIDPrefix + "000000000042": true, "/home/u/proj": true, "/home/u/proj2/c1/.c2/c3.go": true,
		"/home/u/.claude/projects/-home-u-proj/c1/c2.jsonl": true, "s000003 /home/u/proj/c1 /var/x/c2": true, `{"cwd":"/home/u/proj","n":1}`: true,
		"user": false, "/home/alice/x": false, "s000001 /etc/passwd": false, "/home/u/proj/main.go": false, `{"cwd":"/root"}`: false,
		"11111111-2222-4333-8444-555555555555": false, `{"/home/alice":"s000001"}`: false,
	} {
		if got := isSynthetic(s); got != want {
			t.Errorf("isSynthetic(%q) = %v、期待は %v", s, got, want)
		}
	}
	// 道具の写しは必ず合成の値になる（対照: 上の判定が写しを通すこと）。
	_, outs := convertSynthetic(t)
	for _, o := range outs {
		for _, l := range decodeLines(t, o.Data) {
			collect(l, func(x string) {
				if !isSynthetic(x) {
					t.Errorf("%s: 写しの値 %q が合成の値の形に合いません", o.Name, x)
				}
			}, func(string) {})
		}
	}
}

// 固定の材料（実際の記録から作ったもの）の文字列の値とキーが、道具の出す合成の値だけでできていることを確かめる。
func TestMaterialIsSynthetic(t *testing.T) {
	var files []string
	err := filepath.WalkDir(materialDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mainFile := filepath.Join(materialDir, "claude", fakeUUIDPrefix+"000000000001.jsonl")
	nameRe := regexp.MustCompile(`^claude/` + fakeUUIDPrefix + `\d{12}(?:\.jsonl|/subagents/agent-s\d{6}\.jsonl)$`)
	var lines, values, cwdRoot, subs int
	for _, f := range files {
		rel, _ := filepath.Rel(materialDir, f)
		rel = filepath.ToSlash(rel)
		if !nameRe.MatchString(rel) {
			t.Errorf("材料のファイル名 %q が合成の名前の形に合いません", rel)
			continue
		}
		if strings.Contains(rel, "/subagents/") {
			subs++
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range decodeLines(t, data) {
			lines++
			if l.String("cwd") == fakeRoot {
				cwdRoot++
			}
			collect(l, func(x string) {
				values++
				if !isSynthetic(x) {
					t.Errorf("%s の %d 行目: 合成の値の形に合わない文字列の値があります（長さ %d）", rel, i+1, len(x))
				}
			}, func(k string) {
				if !keepKey(k) && !isSynthetic(k) {
					t.Errorf("%s の %d 行目: 合成の形でないキーがあります", rel, i+1)
				}
			})
		}
	}
	if _, err := os.Stat(mainFile); err != nil {
		t.Errorf("本体の材料 %s がありません", mainFile)
	}
	if subs == 0 || lines < 100 || values < 1000 || cwdRoot == 0 {
		t.Fatalf("前提が崩れています: サブエージェント %d 本・%d 行・文字列の値 %d 個・cwd が %s の行 %d（材料が欠けている）", subs, lines, values, fakeRoot, cwdRoot)
	}
	t.Logf("材料 %d ファイル・%d 行・文字列の値 %d 個", len(files), lines, values)
}
