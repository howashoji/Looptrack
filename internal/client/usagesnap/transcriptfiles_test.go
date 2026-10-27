package usagesnap

// 会話記録の搬送が使う、発見の公開の入口と、運ぶファイルの列挙（transcriptfiles.go）。
// 合成の置き場（CLAUDE_CONFIG_DIR・CODEX_HOME あり／なし）だけを使い、この端末の実際の記録は読まない。

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const tfSID = "11111111-2222-4333-8444-555555555555"

func tfWrite(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tfHome はホームの名前に目印（home-marker-xyz）を入れた一時ディレクトリ。file_key にこの名前が入らないことを確かめる。
func tfHome(t *testing.T) string {
	t.Helper()
	h := filepath.Join(t.TempDir(), "home-marker-xyz")
	if err := os.MkdirAll(h, 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

// tfClaudeTree は slug の下に 1 セッション分の付属のファイルを作り、本体の場所を返す。
func tfClaudeTree(t *testing.T, configDir string) string {
	t.Helper()
	slug := filepath.Join(configDir, "projects", "-home-marker-xyz-proj")
	main := tfWrite(t, filepath.Join(slug, tfSID+".jsonl"), "{}\n")
	tfWrite(t, filepath.Join(slug, "99999999-0000-4000-8000-000000000000.jsonl"), "{}\n") // 別のセッション（運ばない対象にも数えない）
	s := filepath.Join(slug, tfSID)
	tfWrite(t, filepath.Join(s, "subagents", "agent-a1.jsonl"), "{}\n")
	tfWrite(t, filepath.Join(s, "subagents", "agent-a1.meta.json"), "{}")
	tfWrite(t, filepath.Join(s, "subagents", "agent-a1.prefix.json"), "{}")
	tfWrite(t, filepath.Join(s, "subagents", "notes.txt"), "x")           // 知らないファイル
	tfWrite(t, filepath.Join(s, "subagents", ".DS_Store"), "x")           // 隠しファイル
	tfWrite(t, filepath.Join(s, "subagents", "nested", "deep.jsonl"), "") // 下のディレクトリは再帰しない
	tfWrite(t, filepath.Join(s, "tool-results", "toolu_01.txt"), "out")
	tfWrite(t, filepath.Join(s, "tool-results", "bash-2.json"), "{}")
	tfWrite(t, filepath.Join(s, "tool-results", "has space.txt"), "x") // file_key の形に合わない
	tfWrite(t, filepath.Join(s, "workflows", "w1", "a.jsonl"), "{}\n")
	tfWrite(t, filepath.Join(s, "workflows", "b.json"), "{}")
	tfWrite(t, filepath.Join(s, "scratch.bin"), "x")
	return main
}

func tfKeys(f *TranscriptFiles) map[string]TranscriptKind {
	m := map[string]TranscriptKind{}
	for _, x := range f.Files {
		m[x.FileKey] = x.Kind
	}
	return m
}

func tfSkips(f *TranscriptFiles) map[string]SkippedFile {
	m := map[string]SkippedFile{}
	for _, x := range f.Skipped {
		m[x.Name] = x
	}
	return m
}

func TestListClaudeFilesWithAndWithoutConfigDir(t *testing.T) {
	for _, withEnv := range []bool{false, true} {
		home := tfHome(t)
		cfg := filepath.Join(home, ".claude")
		vars := map[string]string(nil)
		if withEnv {
			cfg = filepath.Join(t.TempDir(), "elsewhere-claude")
			vars = map[string]string{"CLAUDE_CONFIG_DIR": cfg}
		}
		main := tfClaudeTree(t, cfg)
		o := optsFor(home, vars)

		if got := FindTranscriptMain(ClientClaudeCode, tfSID, "", o); got != main {
			t.Fatalf("env=%v: FindTranscriptMain = %q, want %q", withEnv, got, main)
		}
		f, err := DiscoverTranscript(ClientClaudeCode, tfSID, "", o)
		if err != nil {
			t.Fatalf("env=%v: %v", withEnv, err)
		}
		want := map[string]TranscriptKind{
			tfSID + ".jsonl":                          KindMain,
			tfSID + "/subagents/agent-a1.jsonl":       KindSubagent,
			tfSID + "/subagents/agent-a1.meta.json":   KindSubagentMeta,
			tfSID + "/subagents/agent-a1.prefix.json": KindSubagentMeta,
			tfSID + "/tool-results/toolu_01.txt":      KindToolResult,
			tfSID + "/tool-results/bash-2.json":       KindToolResult,
		}
		if got := tfKeys(f); !reflect.DeepEqual(got, want) {
			t.Errorf("env=%v: 運ぶファイル\n got %v\nwant %v", withEnv, got, want)
		}
		if f.Files[0].Kind != KindMain || f.Files[0].Path != main || f.Main != main || f.SessionID != tfSID {
			t.Errorf("env=%v: 先頭が本体でない: %+v", withEnv, f.Files[0])
		}
		for _, x := range f.Files {
			if x.Size <= 0 && !strings.HasSuffix(x.FileKey, "nested") {
				// 合成のファイルは 1 バイト以上
				t.Errorf("env=%v: %s の Size = %d", withEnv, x.FileKey, x.Size)
			}
		}
		sk := tfSkips(f)
		wantSkip := map[string]struct {
			r SkipReason
			n int
		}{
			tfSID + "/workflows":                  {SkipWorkflows, 2},
			tfSID + "/scratch.bin":                {SkipUnknown, 1},
			tfSID + "/subagents/notes.txt":        {SkipUnknown, 1},
			tfSID + "/subagents/.DS_Store":        {SkipUnknown, 1},
			tfSID + "/subagents/nested":           {SkipNotRegular, 1},
			tfSID + "/tool-results/has space.txt": {SkipBadKey, 1},
		}
		if len(sk) != len(wantSkip) {
			t.Errorf("env=%v: 運ばないファイルの数 %d, want %d: %+v", withEnv, len(sk), len(wantSkip), f.Skipped)
		}
		for name, w := range wantSkip {
			if g, ok := sk[name]; !ok || g.Reason != w.r || g.Count != w.n {
				t.Errorf("env=%v: 運ばない %q = %+v, want %v x%d", withEnv, name, g, w.r, w.n)
			}
		}
		// 対照: workflows/ の中のファイルは運ぶ一覧に 1 つも入らない
		for k := range tfKeys(f) {
			if strings.Contains(k, "workflows") {
				t.Errorf("env=%v: workflows の下を運ぶ: %q", withEnv, k)
			}
		}
		tfCheckKeys(t, ClientClaudeCode, f, home, cfg)
	}
}

// 置き場の根が違えば見つからない（対照: 環境変数あり／なしの両方で、反対側の記録は拾わない）。
func TestFindTranscriptMainRespectsConfigDirBothWays(t *testing.T) {
	home := tfHome(t)
	cfgEnv := filepath.Join(t.TempDir(), "claude-env")
	defMain := tfWrite(t, filepath.Join(home, ".claude", "projects", "-p", "default-sess.jsonl"), "{}\n")
	envMain := tfWrite(t, filepath.Join(cfgEnv, "projects", "-p", "env-sess.jsonl"), "{}\n")
	none := optsFor(home, nil)
	set := optsFor(home, map[string]string{"CLAUDE_CONFIG_DIR": cfgEnv})
	if FindTranscriptMain(ClientClaudeCode, "default-sess", "", none) != defMain || FindTranscriptMain(ClientClaudeCode, "env-sess", "", set) != envMain {
		t.Fatal("前提が崩れています: 自分の置き場の記録を拾えない")
	}
	if got := FindTranscriptMain(ClientClaudeCode, "env-sess", "", none); got != "" {
		t.Errorf("設定なしで CLAUDE_CONFIG_DIR 側を拾った: %q", got)
	}
	if got := FindTranscriptMain(ClientClaudeCode, "default-sess", "", set); got != "" {
		t.Errorf("設定ありで ~/.claude 側を拾った: %q", got)
	}
	if _, err := DiscoverTranscript(ClientClaudeCode, "env-sess", "", none); !errors.Is(err, ErrTranscriptNotFound) {
		t.Errorf("見つからないときのエラー = %v", err)
	}
}

func TestFindTranscriptMainRefusesUnsafeIDs(t *testing.T) {
	home := tfHome(t)
	tfWrite(t, filepath.Join(home, ".claude", "projects", "-p", "abc.jsonl"), "{}\n")
	o := optsFor(home, nil)
	if FindTranscriptMain(ClientClaudeCode, "abc", "", o) == "" {
		t.Fatal("前提が崩れています: ふつうの ID で見つからない")
	}
	for _, id := range []string{"*", "a*", "../abc", "a/b", "-x/../abc", "a b", "[a]bc", "?bc"} {
		if got := FindTranscriptMain(ClientClaudeCode, id, "", o); got != "" {
			t.Errorf("安全でない ID %q で探した: %q", id, got)
		}
		if got := FindTranscriptMain(ClientCodex, id, "", o); got != "" {
			t.Errorf("安全でない ID %q（Codex）で探した: %q", id, got)
		}
	}
	if got := FindTranscriptMain("copilot", "abc", "", o); got != "" {
		t.Errorf("知らない agent で探した: %q", got)
	}
	if _, err := ListTranscriptFiles("copilot", "/x/abc.jsonl", "", o); err == nil {
		t.Error("知らない agent で列挙できた")
	}
}

func TestListClaudeSymlinksAreNotCarried(t *testing.T) {
	home := tfHome(t)
	cfg := filepath.Join(home, ".claude")
	main := tfClaudeTree(t, cfg)
	outside := tfWrite(t, filepath.Join(t.TempDir(), "outside-secret.txt"), "x")
	s := filepath.Join(filepath.Dir(main), tfSID)
	if err := os.Symlink(outside, filepath.Join(s, "tool-results", "link.txt")); err != nil {
		t.Skipf("symlink を作れない: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(s, "subagents", "agent-link.jsonl")); err != nil {
		t.Fatal(err)
	}
	f, err := ListTranscriptFiles(ClientClaudeCode, main, tfSID, optsFor(home, nil))
	if err != nil {
		t.Fatal(err)
	}
	keys, sk := tfKeys(f), tfSkips(f)
	for _, k := range []string{tfSID + "/tool-results/link.txt", tfSID + "/subagents/agent-link.jsonl"} {
		if _, ok := keys[k]; ok {
			t.Errorf("シンボリックリンクを運ぶ: %q", k)
		}
		if sk[k].Reason != SkipNotRegular {
			t.Errorf("%q の理由 = %q, want not_regular", k, sk[k].Reason)
		}
	}
	if keys[tfSID+"/tool-results/toolu_01.txt"] != KindToolResult { // 対照: ふつうのファイルは運ぶ
		t.Error("前提が崩れています: ふつうの tool-results を運ばない")
	}
	// ディレクトリごとリンクにしても、その先を読まない
	other := t.TempDir()
	tfWrite(t, filepath.Join(other, "x.txt"), "x")
	if err := os.RemoveAll(filepath.Join(s, "tool-results")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(s, "tool-results")); err != nil {
		t.Fatal(err)
	}
	f, err = ListTranscriptFiles(ClientClaudeCode, main, tfSID, optsFor(home, nil))
	if err != nil {
		t.Fatal(err)
	}
	for k := range tfKeys(f) {
		if strings.Contains(k, "tool-results") {
			t.Errorf("リンクのディレクトリの先を運ぶ: %q", k)
		}
	}
	if tfSkips(f)[tfSID+"/tool-results"].Reason != SkipNotRegular {
		t.Errorf("リンクのディレクトリが運ばない一覧に無い: %+v", f.Skipped)
	}
}

func TestListClaudeNoSessionDir(t *testing.T) {
	home := tfHome(t)
	main := tfWrite(t, filepath.Join(home, ".claude", "projects", "-p", tfSID+".jsonl"), "{}\n")
	f, err := ListTranscriptFiles(ClientClaudeCode, main, "", optsFor(home, nil))
	if err != nil || len(f.Files) != 1 || f.Files[0].Kind != KindMain || len(f.Skipped) != 0 {
		t.Errorf("付属が無い: %+v %v", f, err)
	}
	if _, err := ListTranscriptFiles(ClientClaudeCode, filepath.Join(filepath.Dir(main), "x.txt"), "", optsFor(home, nil)); err == nil {
		t.Error("会話記録の名前でないものを列挙できた")
	}
}

// tfCheckKeys は file_key の関所: 形・絶対パスでない・".." が無い・ホームやセッションの置き場の名前が入らない。
func tfCheckKeys(t *testing.T, agent string, f *TranscriptFiles, forbidden ...string) {
	t.Helper()
	for _, x := range f.Files {
		k := x.FileKey
		if !ValidFileKey(agent, k) {
			t.Errorf("file_key の形に合わない: %q", k)
		}
		if filepath.IsAbs(k) || strings.HasPrefix(k, "/") || strings.Contains(k, "..") || strings.Contains(k, `\`) || strings.Contains(k, ":") {
			t.Errorf("file_key に絶対パス・..・区切りが入る: %q", k)
		}
		for _, bad := range append(forbidden, "home-marker-xyz", "-proj", "projects") {
			if bad != "" && strings.Contains(k, bad) {
				t.Errorf("file_key %q に %q が入る", k, bad)
			}
		}
	}
	for _, s := range f.Skipped {
		if filepath.IsAbs(s.Name) || strings.Contains(s.Name, "home-marker-xyz") {
			t.Errorf("運ばない一覧の名前に絶対パス・ホームの名前が入る: %q", s.Name)
		}
	}
}

func TestValidFileKey(t *testing.T) {
	long := strings.Repeat("a", 250)
	ok := map[string][]string{
		ClientClaudeCode: {"s.jsonl", "s/subagents/agent-1.jsonl", "s/subagents/agent-1.meta.json", "s/tool-results/toolu_01.txt", "a.b-c_d/tool-results/x"},
		ClientCodex:      {"2026/09/18/rollout-2026-09-18T01-00-00-tid.jsonl", "2026/09/18/rollout-x_y.jsonl"},
	}
	bad := map[string][]string{
		ClientClaudeCode: {"", "/s.jsonl", "../s.jsonl", "s/../s.jsonl", "s/subagents/../x", "./s.jsonl", "s//x.jsonl", "s.txt", "s\\x.jsonl",
			"s/workflows/a.jsonl", "s/subagents", "s/subagents/a/b", "C:/s.jsonl", "s/subagents/has space", "s/subagents/\x01.jsonl",
			"s/subagents/日本語.jsonl", "s/subagents/" + long + long, "/home/u/s.jsonl", ".hidden.jsonl", "s/tool-results/.x"},
		ClientCodex: {"", "rollout-x.jsonl", "2026/09/rollout-x.jsonl", "2026/09/18/x.jsonl", "2026/09/18/rollout-x.jsonl.zst", "/2026/09/18/rollout-x.jsonl",
			"2026/09/../rollout-x.jsonl", "26/09/18/rollout-x.jsonl", "2026/9/18/rollout-x.jsonl", "2026/09/18/rollout-x.jsonl/y", "a/b/c/rollout-x.jsonl"},
	}
	for agent, keys := range ok {
		for _, k := range keys {
			if !ValidFileKey(agent, k) {
				t.Errorf("%s: %q が通らない", agent, k)
			}
		}
	}
	for agent, keys := range bad {
		for _, k := range keys {
			if ValidFileKey(agent, k) {
				t.Errorf("%s: %q が通る", agent, k)
			}
		}
	}
	if ValidFileKey("copilot", "s.jsonl") {
		t.Error("知らない agent が通る")
	}
}

// ---- Codex ----

const (
	tfTID    = "0199aaaa-1111-7222-8333-444455556666"
	tfRevert = "0199cccc-0000-7000-8000-000000000000"
	tfOther  = "0199eeee-1111-7222-8333-444455556666"
	tfBase1  = "0199b001-1111-7222-8333-444455556666"
	tfBase2  = "0199b002-1111-7222-8333-444455556666"
)

func tfRollout(t *testing.T, root, day, time, tid, suffix, headID string, extra M) string {
	t.Helper()
	name := "rollout-2026-" + time + "-" + tid + suffix + ".jsonl"
	p := filepath.Join(root, "sessions", "2026", "09", day, name)
	payload := M{"id": headID, "cwd": "/home/u/proj", "cli_version": "0.140.0"}
	for k, v := range extra {
		payload[k] = v
	}
	return writeRows(t, p, []any{M{"timestamp": "2026-09-" + day + "T" + strings.ReplaceAll(time[6:], "-", ":") + ".000Z", "type": "session_meta", "payload": payload}})
}

func tfCodexTree(t *testing.T, codexHome string) (first, second string) {
	t.Helper()
	first = tfRollout(t, codexHome, "18", "09-18T01-00-00", tfTID, "", tfTID, nil)
	second = tfRollout(t, codexHome, "18", "09-18T03-00-00", tfTID, "_"+tfRevert, tfTID, nil) // thread/revert の _<別の ID>
	// 運ばない: 同じ名前の圧縮済み・別のスレッド・名前だけ似ていて session_meta.id が違うもの
	tfWrite(t, filepath.Join(codexHome, "sessions", "2026", "09", "17", "rollout-2026-09-17T01-00-00-"+tfTID+".jsonl.zst"), "not-zstd")
	tfRollout(t, codexHome, "18", "09-18T04-00-00", tfOther, "", tfOther, nil)
	tfRollout(t, codexHome, "18", "09-18T05-00-00", tfTID, "_"+tfOther, tfOther, nil)
	return first, second
}

func TestListCodexFilesWithAndWithoutCodexHome(t *testing.T) {
	for _, withEnv := range []bool{false, true} {
		home := tfHome(t)
		ch := filepath.Join(home, ".codex")
		vars := map[string]string(nil)
		if withEnv {
			ch = filepath.Join(t.TempDir(), "elsewhere-codex")
			vars = map[string]string{"CODEX_HOME": ch}
		}
		first, second := tfCodexTree(t, ch)
		o := optsFor(home, vars)
		f, err := DiscoverTranscript(ClientCodex, tfTID, "", o)
		if err != nil {
			t.Fatalf("env=%v: %v", withEnv, err)
		}
		wantKeys := map[string]TranscriptKind{
			"2026/09/18/" + filepath.Base(first):  KindRollout,
			"2026/09/18/" + filepath.Base(second): KindRollout,
		}
		if got := tfKeys(f); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("env=%v: 運ぶファイル\n got %v\nwant %v", withEnv, got, wantKeys)
		}
		if f.SessionID != tfTID {
			t.Errorf("env=%v: SessionID = %q", withEnv, f.SessionID)
		}
		sk := tfSkips(f)
		if g := sk["2026/09/17/rollout-2026-09-17T01-00-00-"+tfTID+".jsonl.zst"]; g.Reason != SkipCompressed || g.Count != 1 {
			t.Errorf("env=%v: .jsonl.zst が運ばない一覧に無い: %+v", withEnv, f.Skipped)
		}
		if g := sk["2026/09/18/rollout-2026-09-18T05-00-00-"+tfTID+"_"+tfOther+".jsonl"]; g.Reason != SkipOtherThread {
			t.Errorf("env=%v: session_meta.id が違うファイルが運ばない一覧に無い: %+v", withEnv, f.Skipped)
		}
		for k := range tfKeys(f) {
			if strings.Contains(k, tfOther) || strings.HasSuffix(k, ".zst") {
				t.Errorf("env=%v: 運んではいけないものを運ぶ: %q", withEnv, k)
			}
		}
		if len(f.Skipped) != 2 {
			t.Errorf("env=%v: 運ばない一覧 = %+v", withEnv, f.Skipped)
		}
		tfCheckKeys(t, ClientCodex, f, ch)
		// より新しい名前だけ似ている別スレッドの rollout（session_meta.id が違う）は、どの入口でも選ばない。
		// 対照: 同じ _<別の ID> 付きでも session_meta.id が一致する second は選ぶ（decoy より古いのに返る）
		decoy := filepath.Join(ch, "sessions", "2026", "09", "18", "rollout-2026-09-18T05-00-00-"+tfTID+"_"+tfOther+".jsonl")
		if got := codexFindTranscript("", tfTID, o); got != second {
			t.Errorf("env=%v: codexFindTranscript = %q, want %q (decoy %q は選ばない)", withEnv, got, second, decoy)
		}
		if _, err := ListTranscriptFiles(ClientCodex, decoy, tfTID, o); err == nil {
			t.Errorf("env=%v: 別のスレッドの rollout を本体として列挙できた", withEnv)
		}
		// _<別の ID> 付きから起こしても同じスレッドが集まる
		f2, err := ListTranscriptFiles(ClientCodex, second, tfTID, o)
		if err != nil || !reflect.DeepEqual(tfKeys(f2), wantKeys) {
			t.Errorf("env=%v: 後ろの rollout から: %v %v", withEnv, tfKeys(f2), err)
		}
	}
}

func TestCodexFindTranscriptByIDSkipsLookalikeThread(t *testing.T) {
	home := tfHome(t)
	ch := filepath.Join(home, ".codex")
	o := optsFor(home, nil)
	real := tfRollout(t, ch, "18", "09-18T01-00-00", tfTID, "", tfTID, nil)
	// 名前に tid を含むが session_meta.id は別のスレッド。mtime は real より新しい
	decoy := tfRollout(t, ch, "18", "09-18T05-00-00", tfTID, "_"+tfOther, tfOther, nil)
	now := time.Now()
	if err := os.Chtimes(real, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(decoy, now, now); err != nil {
		t.Fatal(err)
	}
	if got := codexFindTranscript("", tfTID, o); got != real {
		t.Errorf("おとりを選んだ: got %q, want %q", got, real)
	}
	// 対照: 名前が同じ形でも session_meta.id が tid なら、より新しいそちらを選ぶ（前提: おとりの除外が名前の形ではなく id で決まる）
	same := tfRollout(t, ch, "18", "09-18T06-00-00", tfTID, "_"+tfRevert, tfTID, nil)
	if err := os.Chtimes(same, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := codexFindTranscript("", tfTID, o); got != same {
		t.Errorf("前提が崩れています: id が一致する _<別の ID> 付きを選べない: got %q, want %q", got, same)
	}
}

// TestCodexFindThreadSameMtime は、同じスレッドの rollout の更新時刻がそろったとき（刻みの粗い時計で続けて書いた）、
// glob の順ではなく名前の時刻の新しいほうを選ぶことを確かめる。_<別の ID> 付きは glob で後に来るので、
// 先に見つかったほうを残す選び方では古い rollout を返す。
func TestCodexFindThreadSameMtime(t *testing.T) {
	home := tfHome(t)
	ch := filepath.Join(home, ".codex")
	o := optsFor(home, nil)
	older := tfRollout(t, ch, "18", "09-18T01-00-00", tfTID, "", tfTID, nil)
	newer := tfRollout(t, ch, "18", "09-18T03-00-00", tfTID, "_"+tfRevert, tfTID, nil)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, p := range []string{older, newer} {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if mtime(older) != mtime(newer) {
		t.Fatalf("前提が崩れています: 更新時刻がそろっていません")
	}
	if got := codexFindTranscript("", tfTID, o); got != newer {
		t.Errorf("更新時刻が同じとき: got %q, want %q", got, newer)
	}
	// 対照: 更新時刻が違えば、名前ではなく更新時刻で選ぶ（古い名前でも後から書き足したほう）
	if err := os.Chtimes(older, at.Add(time.Minute), at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := codexFindTranscript("", tfTID, o); got != older {
		t.Errorf("前提が崩れています: 更新時刻の新しいほうを選べない: got %q, want %q", got, older)
	}
}

func TestFindTranscriptMainCodexRespectsHomeBothWays(t *testing.T) {
	home := tfHome(t)
	ch := filepath.Join(t.TempDir(), "codex-env")
	defPath := tfRollout(t, filepath.Join(home, ".codex"), "18", "09-18T01-00-00", tfTID, "", tfTID, nil)
	envPath := tfRollout(t, ch, "18", "09-18T02-00-00", tfOther, "", tfOther, nil)
	none, set := optsFor(home, nil), optsFor(home, map[string]string{"CODEX_HOME": ch})
	if FindTranscriptMain(ClientCodex, tfTID, "", none) != defPath || FindTranscriptMain(ClientCodex, tfOther, "", set) != envPath {
		t.Fatal("前提が崩れています: 自分の置き場の記録を拾えない")
	}
	if got := FindTranscriptMain(ClientCodex, tfOther, "", none); got != "" {
		t.Errorf("設定なしで CODEX_HOME 側を拾った: %q", got)
	}
	if got := FindTranscriptMain(ClientCodex, tfTID, "", set); got != "" {
		t.Errorf("設定ありで ~/.codex 側を拾った: %q", got)
	}
}

func TestListCodexHistoryBase(t *testing.T) {
	home := tfHome(t)
	ch := filepath.Join(home, ".codex")
	o := optsFor(home, nil)
	b1 := tfRollout(t, ch, "16", "09-16T01-00-00", tfBase1, "", tfBase1, nil)
	b1rev := tfRollout(t, ch, "16", "09-16T02-00-00", tfBase1, "_"+tfRevert, tfBase1, nil)
	// 連鎖: base2 は base1 をスレッド ID で指す
	b2 := tfRollout(t, ch, "17", "09-17T01-00-00", tfBase2, "", tfBase2, M{"history_base": M{"thread_id": tfBase1}})
	tfWrite(t, filepath.Join(ch, "sessions", "2026", "09", "15", "rollout-2026-09-15T01-00-00-"+tfBase1+".jsonl.zst"), "z")

	cases := []struct {
		name    string
		hb      any
		want    []string // 運ぶはずの基点側のファイル
		wantSk  SkipReason
		wantNot string
	}{
		{"ファイル名（文字列）", filepath.Base(b1), []string{b1}, "", ""},
		{"絶対パス（別の端末の値）は名前の末尾だけでこの端末の根の下を探す", "/home/someone-else/.codex/sessions/2026/09/16/" + filepath.Base(b1), []string{b1}, "", ""},
		{"Windows の区切り", `C:\Users\x\.codex\sessions\2026\09\16\` + filepath.Base(b1), []string{b1}, "", ""},
		{"オブジェクトの中のスレッド ID は同じスレッドの rollout を全部", M{"thread_id": tfBase1, "kind": "fork"}, []string{b1, b1rev}, "", ""},
		{"連鎖（base2 → base1）", M{"rollout_path": filepath.Base(b2)}, []string{b2, b1, b1rev}, "", ""},
		{"見つからない", "rollout-2026-01-01T00-00-00-0199ffff-0000-7000-8000-000000000000.jsonl", nil, SkipUnresolvedBase, ""},
		{"何も指していない値", M{"note": "x"}, nil, SkipUnresolvedBase, ""},
		{"圧縮済みを指す", "rollout-2026-09-15T01-00-00-" + tfBase1 + ".jsonl.zst", nil, "", ""},
	}
	for i, c := range cases {
		tid := "0199d" + string(rune('a'+i)) + "00-1111-7222-8333-444455556666"
		head := tfRollout(t, ch, "18", "09-18T0"+string(rune('0'+i))+"-00-00", tid, "", tid, M{"history_base": c.hb})
		f, err := ListTranscriptFiles(ClientCodex, head, tid, o)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		keys := tfKeys(f)
		want := map[string]TranscriptKind{"2026/09/18/" + filepath.Base(head): KindRollout}
		for _, w := range c.want {
			rel, _ := filepath.Rel(filepath.Join(ch, "sessions"), w)
			want[filepath.ToSlash(rel)] = KindRollout
		}
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("%s: 運ぶファイル\n got %v\nwant %v", c.name, keys, want)
		}
		sk := tfSkips(f)
		if c.wantSk != "" {
			if g := sk["history_base"]; g.Reason != c.wantSk {
				t.Errorf("%s: history_base の運ばない = %+v, want %v", c.name, f.Skipped, c.wantSk)
			}
		}
		if strings.Contains(c.name, "圧縮済み") {
			if g := sk["2026/09/15/rollout-2026-09-15T01-00-00-"+tfBase1+".jsonl.zst"]; g.Reason != SkipCompressed {
				t.Errorf("%s: 圧縮済みが運ばない一覧に無い: %+v", c.name, f.Skipped)
			}
		}
		tfCheckKeys(t, ClientCodex, f, ch)
	}
}

func TestListCodexHistoryBaseCycleAndDepth(t *testing.T) {
	home := tfHome(t)
	ch := filepath.Join(home, ".codex")
	o := optsFor(home, nil)
	// 自分自身を指す・互いを指す連鎖でも止まる
	a := tfRollout(t, ch, "18", "09-18T01-00-00", tfBase1, "", tfBase1, nil)
	b := tfRollout(t, ch, "18", "09-18T02-00-00", tfBase2, "", tfBase2, M{"history_base": filepath.Base(a)})
	// a の history_base を b にして書き直す（循環）
	writeRows(t, a, []any{M{"timestamp": "2026-09-18T01:00:00.000Z", "type": "session_meta", "payload": M{"id": tfBase1, "cwd": "/x", "history_base": filepath.Base(b)}}})
	f, err := ListTranscriptFiles(ClientCodex, a, tfBase1, o)
	if err != nil || len(f.Files) != 2 {
		t.Fatalf("循環: %+v %v", f, err)
	}
	// 深すぎる連鎖は途中で止めて、名前つきで知らせる
	prev := ""
	var last string
	for i := 0; i < maxBaseDepth+3; i++ {
		tid := "0199f" + string(rune('a'+i)) + "00-1111-7222-8333-444455556666"
		extra := M{}
		if prev != "" {
			extra["history_base"] = prev
		}
		last = tfRollout(t, ch, "19", "09-19T0"+string(rune('0'+i%10))+"-00-"+string(rune('0'+i/10))+"0", tid, "", tid, extra)
		prev = filepath.Base(last)
	}
	f, err = ListTranscriptFiles(ClientCodex, last, "", o)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Files) != maxBaseDepth+1 {
		t.Errorf("深さの上限: 運ぶ数 %d, want %d", len(f.Files), maxBaseDepth+1)
	}
	if tfSkips(f)["history_base"].Reason != SkipDepthLimit {
		t.Errorf("深さの上限が運ばない一覧に無い: %+v", f.Skipped)
	}
}

func TestListCodexOutsideRootAndSymlink(t *testing.T) {
	home := tfHome(t)
	ch := filepath.Join(home, ".codex")
	o := optsFor(home, nil)
	outside := tfRollout(t, t.TempDir(), "18", "09-18T01-00-00", tfTID, "", tfTID, nil) // 置き場の外
	f, err := ListTranscriptFiles(ClientCodex, outside, tfTID, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Files) != 0 || len(f.Skipped) == 0 || f.Skipped[0].Reason != SkipOutsideRoot || filepath.IsAbs(f.Skipped[0].Name) {
		t.Errorf("置き場の外: %+v", f)
	}
	// 置き場の中のリンク（rollout が外の実体を指す）は運ばない
	real := tfRollout(t, t.TempDir(), "18", "09-18T02-00-00", tfOther, "", tfOther, nil)
	main := tfRollout(t, ch, "18", "09-18T01-00-00", tfTID, "", tfTID, nil)
	link := filepath.Join(ch, "sessions", "2026", "09", "18", "rollout-2026-09-18T06-00-00-"+tfTID+"_"+tfRevert+".jsonl")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink を作れない: %v", err)
	}
	f, err = ListTranscriptFiles(ClientCodex, main, tfTID, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := tfKeys(f); len(got) != 1 {
		t.Errorf("リンクを運ぶ: %v", got)
	}
}
