package usagesnap

// 移したケースの外側を確かめる差分テスト。以前の CLI（1.0.0 より前）の結果は撤去の前に記録したもの（golden_test.go）。
//   - 部品（時刻の読み方・会話 ID・作業名・対象外の判定・丸め・OTel の時刻）を境界の値で記録と比べる
//   - 乱数（種は固定）で作った合成の会話記録（Claude Code・Codex・Copilot）で payload を記録と比べる
// 記録は固定の種（fuzzParams）で取ったので、種・件数は変えられない（変えると記録に無い呼び出しになる）。

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reportDiff(t *testing.T, calls []call) func(int, string, string) {
	shown := 0
	return func(i int, g, p string) {
		if shown < 5 {
			t.Errorf("以前の CLI の記録と一致しない（%s %s %s %q）\n Go: %s\n 記録: %s", calls[i].Fn, calls[i].Kind, calls[i].Path, calls[i].Arg, g, p)
		}
		shown++
	}
}

func TestPrimitivesMatchGolden(t *testing.T) {
	var calls []call
	isos := []string{
		"2026-09-18T01:00:00.000Z", "2026-09-18T01:00:00Z", "2026-09-18T01:00:00.123456789Z", "2026-09-18T01:00:00.1234565Z",
		"2026-09-18T01:00:00.1Z", "2026-09-18 01:00:00", "2026-09-18", "20260918T010000Z", "2026-09-18T01:00Z", "2026-09-18T01Z",
		"2026-09-18T01:00:00,5Z", "2026-09-18T01:00:00.5+09:00", "2026-09-18T01:00:00.5+0900", "2026-09-18T01:00:00+09",
		"2026-09-18T01:00:00.Z", "2026-09-18X01:00:00", "2026-09-18T24:00:00", "2026-W38-5", "2026W385", "2026-W38", "2026-W53-1", "2020-W53-7",
		"2026-09-18T01:00:00.000z", "2026-09-18T01:00:00 Z", "1789700000", "2026-09-18T01:00:00-00:00", "2026-09-18T01:00:00+09:00:30.5",
		"2026-09-18T010203", "2026-09-18T01:02:03.1234567", "2026-09-18T01:00:00 +09:00", "2026-09-18T01:00:00.", "2026-09-18T01:00:00 ",
		"2026-09-18T01:00:00.5 Z", "2026-09-18T01:00:00Z ", "2026-09-18T01:00:00.12a", "2026-09-18T01:00:00.1234567a", "2026-09-18T01:00:00+",
		"2026-09-18T01:00:00+0", "2026-09-18T01:00:00+090", "2026-09-18T1:00:00", "2026-09-18T01:0", "2026-09-18T0100", "2026-09-18T01:0000",
		"2026-9-18", "+026-09-18", "2026-09-18T01:00:00-", "2026-09-18T01:00:00+00:00Z", "２０２６-09-18", "2026-09-18T12:00:00+23:59",
		"2026-09-18T12:00:00+24:00", "2026-02-30", "2024-02-29T00:00:00Z", "2026-09-18T01:60:00", "2026-09-18T01:00:60", "0001-01-01T00:00:00Z",
		"9999-12-30T23:59:59.999999Z", "2026-03-08T02:30:00", "2026-09-18T01:00:00-05:30", "2026-09-18T01:00:00+00:99", "2026-09-18T01:00:00:5",
		"2026-09-18T0100001", "", "abc", "2026-09-18T", "2026-09-18T01:00:00.000Z\n",
	}
	for _, s := range isos {
		calls = append(calls, call{Fn: "prim", Kind: "iso", Arg: s})
	}
	texts := []string{
		"", "\n\n", "  - 直して\n次", "# 見出し\n本文", ">>> 引用", "　全角の空白で始まる", "\x1c区切り\x1d", "a b", "a\rb", "a\r\nb",
		"  x", "  \t  ", strings.Repeat("あ", 60), strings.Repeat("x", 44) + "y", "-*#> ", "---\n***\n本当の指示",
		"利用制限に達しました", "もう一度試す", "xもう一度試す", "本セッションはレポート対象外", "このセッションはトークンレポートの対象外にして",
		"「本セッションはレポート対象外」", "\"本セッションはレポート対象外", "“本セッションは対象外", "`本セッションはレポート対象外`",
		"本セッションは0123456789012345レポート対象外", "本セッションは012345678901234レポート対象外", "本セッションは\nレポート対象外",
		"「本セッション」は、このセッションはレポート対象外", "本本セッションはレポート対象外", "「このセッションはレポート対象外",
		"この本セッションはレポート対象外", "本セッションは〜レポート対象外", "本セッションはレポートの対象外です", "\x0b\x0c縦タブ",
		"次の行", "a b", " 先頭", "​ゼロ幅", "\x1f単位区切り",
	}
	for _, s := range texts {
		for _, k := range []string{"label", "excluded", "auto"} {
			calls = append(calls, call{Fn: "prim", Kind: k, Arg: s})
		}
	}
	for i := 0; i <= 2000; i++ {
		calls = append(calls, call{Fn: "prim", Kind: "round1", Num: float64(i) / 60}) // 秒の差 / 60
		calls = append(calls, call{Fn: "prim", Kind: "round1", Num: float64(i) * 0.05})
	}
	for _, x := range []float64{0.25, 0.35, 2.675, 0.05, 0.15, 0.45, 1.25, 1e-7, 123456.75, 0} {
		calls = append(calls, call{Fn: "prim", Kind: "round1", Num: x})
	}
	otimes := []string{
		`[1789700000, 0]`, `[1789700000, 500000000]`, `[1789700000, 999999999]`, `[1789700000, 999999500]`, `[1789700000, 1500]`, `[1789700000, 2500]`,
		`[1789700000.5, 0]`, `["1789700000", "5"]`, `[-1, 0]`, `[1, 2, 3]`, `1789700000`, `1789700000123`, `1789700000123456`, `1789700000123456789`,
		`"1789700100000000000"`, `"2026-09-18T01:00:00Z"`, `" 1789700000 "`, `0`, `-5`, `true`, `null`, `{}`, `"abc"`, `1e30`, `99999999999`,
		`100000000000`, `1e14`, `1e17`, `1789700000.0000015`, `[1e300, 0]`, `["x", 0]`, `[true, 0]`,
	}
	for _, s := range otimes {
		calls = append(calls, call{Fn: "prim", Kind: "otel_time", Arg: s})
	}
	if bad := diffMany(t, calls, reportDiff(t, calls)); bad > 0 {
		t.Errorf("%d / %d 件が一致しない", bad, len(calls))
	}
	t.Logf("部品 %d 件が記録と一致", len(calls))
}

// 以前の CLI（1.0.0 より前）が読めずに落ちる入力（Go 版は読み飛ばす・差分テストに入れない）:
//   - 行が JSON のオブジェクトでない（[1, 2]・"str"・NaN）→ 値の取り出しで落ちる
//   - 9999-12-31 の終わりの時刻を現地時刻にすると範囲外で落ちる（会話 ID）

// ---------------------------------------------------------------- 乱数の会話記録

type fuzz struct {
	r *rand.Rand
}

func (f *fuzz) pick(xs ...string) string { return xs[f.r.IntN(len(xs))] }
func (f *fuzz) p(x float64) bool         { return f.r.Float64() < x }

var fuzzTexts = []string{
	"直して", "最初の指示", "  - テストを足して\n詳細", "# 方針\n本文", "利用制限に達しました。再開します", "もう一度試す",
	"本セッションはレポート対象外", "「本セッションはレポート対象外」と書かれたものを外して", "　全角で始まる", "", "\n\n",
	strings.Repeat("長い指示", 20), "a\r\nb", "続けて", "ありがとう", "x",
}

// ts は秒（2026-09-18T00:00:00Z からの秒・ミリ秒）をいろいろな形の時刻にする。
func (f *fuzz) ts(ms int64) string {
	sec := ms / 1000
	frac := ms % 1000
	base := int64(1789689600) // 2026-09-18T00:00:00Z
	u := base + sec
	h, m, s := (u/3600)%24, (u/60)%60, u%60
	day := 18 + (u-base)/86400
	d := fmt.Sprintf("2026-09-%02dT%02d:%02d:%02d", day, h, m, s)
	switch f.r.IntN(12) {
	case 0:
		return d + "Z"
	case 1:
		return fmt.Sprintf("%s.%03d000Z", d, frac)
	case 2:
		return fmt.Sprintf("%s.%03d123456Z", d, frac)
	case 3:
		// +09:00 の表記（同じ時刻）
		h9 := (h + 9) % 24
		d9 := day
		if h+9 >= 24 {
			d9++
		}
		return fmt.Sprintf("2026-09-%02dT%02d:%02d:%02d.%03d+09:00", d9, h9, m, s, frac)
	case 4:
		if f.p(0.3) {
			return ""
		}
	}
	return fmt.Sprintf("%s.%03dZ", d, frac)
}

func (f *fuzz) gap() int64 {
	switch f.r.IntN(8) {
	case 0:
		return int64(f.r.IntN(3)) * 1000 // 同時刻・数秒
	case 1:
		return int64(f.r.IntN(200)) * 1500 // 0.025 分刻み（丸めの境目）
	case 2:
		return int64(120+f.r.IntN(200)) * 60 * 1000 // 長い休み
	case 3:
		return -int64(f.r.IntN(5000)) // 逆行
	}
	return int64(f.r.IntN(120000))
}

func (f *fuzz) claudeUsage() M {
	u := M{"input_tokens": f.r.IntN(5000), "output_tokens": f.r.IntN(3000), "cache_read_input_tokens": f.r.IntN(100000),
		"cache_creation_input_tokens": f.r.IntN(20000)}
	if f.p(0.7) {
		u["cache_creation"] = M{"ephemeral_1h_input_tokens": f.r.IntN(1000), "ephemeral_5m_input_tokens": f.r.IntN(1000)}
	}
	if f.p(0.2) {
		u["output_tokens_details"] = M{"thinking_tokens": f.r.IntN(500)}
	}
	if f.p(0.05) {
		u["input_tokens"] = 1.5 // int でなければ数えない
	}
	return u
}

func (f *fuzz) claudeRows(n int, sid string, sidechainOnly bool) []string {
	var lines []string
	ms := int64(f.r.IntN(3600)) * 1000
	ids := []string{}
	for i := 0; i < n; i++ {
		ms += f.gap()
		ts := f.ts(ms)
		base := M{"timestamp": ts, "sessionId": sid, "version": f.pick("2.1.271", "2.1.280", "")}
		if f.p(0.8) {
			base["gitBranch"] = f.pick("main", "main", "feature/x", "")
		}
		switch k := f.r.IntN(12); {
		case sidechainOnly || k < 5:
			id := fmt.Sprintf("msg_%d", i)
			if len(ids) > 0 && f.p(0.35) {
				id = ids[f.r.IntN(len(ids))] // 同じ応答の別の行
			}
			ids = append(ids, id)
			msg := M{"model": f.pick("claude-opus-5", "claude-fable-5-1", ""), "usage": f.claudeUsage()}
			switch {
			case f.p(0.08):
				msg["usage"] = M{}
			case f.p(0.05):
				delete(msg, "usage")
			}
			if f.p(0.9) {
				msg["id"] = id
			} else {
				base["uuid"] = fmt.Sprintf("uuid-%d", f.r.IntN(5))
			}
			base["type"] = "assistant"
			base["message"] = msg
			if f.p(0.1) {
				base["isSidechain"] = true
			}
		case k < 7:
			base["type"] = "user"
			base["origin"] = M{"kind": f.pick("human", "human", "tool")}
			content := any(f.pick(fuzzTexts...))
			if f.p(0.15) {
				content = []any{M{"type": "text", "text": f.pick(fuzzTexts...)}, M{"type": "image"}}
			}
			base["message"] = M{"role": "user", "content": content}
			if f.p(0.05) {
				base["isSidechain"] = true
			}
		case k < 9:
			base["type"] = "user"
			var tur M
			switch f.r.IntN(5) {
			case 0:
				tur = M{"file": M{"content": f.pick("a\nb\n", "あいう", "", "x\ny"), "numLines": f.r.IntN(5)}}
			case 1:
				tur = M{"file": M{"content": f.pick("a\nb\n", "あいう\r\n", "", "x\ny")}}
			case 2:
				tur = M{"file": M{"originalSize": f.r.IntN(100000), "count": f.r.IntN(9)}}
			case 3:
				tur = M{"structuredPatch": []any{M{"lines": []any{"+a", "-b", " c", "+", ""}}, "x"}, "newString": "ab\ncd"}
			default:
				tur = M{"oldString": "a", "content": f.pick("新しい\n内容", "", "x")}
			}
			base["toolUseResult"] = tur
			base["message"] = M{"role": "user", "content": []any{M{"type": "tool_result"}}}
		case k < 10:
			base["type"] = "attachment"
			att := M{"type": "queued_command", "origin": M{"kind": f.pick("human", "human", "system")}, "prompt": f.pick(fuzzTexts...)}
			if f.p(0.7) {
				att["timestamp"] = ts
			}
			if f.p(0.1) {
				att["prompt"] = []any{M{"type": "text", "text": "割込"}}
			}
			base["attachment"] = att
		default:
			base["type"] = f.pick("system", "summary", "progress")
		}
		lines = append(lines, jsonLine(base))
		if f.p(0.03) {
			lines = append(lines, f.pick("not json", "", "{\"type\": \"assistant\"", "{} {}"))
		}
	}
	return lines
}

func (f *fuzz) writeRaw(t testing.TB, path string, lines []string) string {
	sep := "\n"
	switch f.r.IntN(10) {
	case 0:
		sep = "\r\n"
	case 1:
		sep = "\r"
	}
	body := strings.Join(lines, sep)
	if f.p(0.8) {
		body += sep
	}
	if f.p(0.1) {
		body += "{\"type\": \"user\", \"x\": \"\xe3\x81\"}\n" + "\xff\xfe\n" // 不正な UTF-8
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// promptsEnv は作業名の送信の判定の組み合わせ（覚えた値の有無・0/1 と、環境変数の有無・値）を乱数で選ぶ。
func (f *fuzz) promptsEnv(t testing.TB, home string) map[string]string {
	if f.p(0.2) {
		return homeEnv(home) // API モードでない（覚えた値を探さない）
	}
	return promptsEnv(t, home, f.pick("", "1", "1", "0"), f.pick("", "", "1", "0", " 0", "x"))
}

func fuzzParams() (uint64, int) { return 20260919, 1 }

func TestFuzzClaudeMatchesGolden(t *testing.T) {
	seed, mult := fuzzParams()
	f := &fuzz{r: rand.New(rand.NewPCG(seed, 1))}
	var calls []call
	for i := 0; i < 150*mult; i++ {
		home := t.TempDir()
		sid := fmt.Sprintf("sess-%d", i)
		dir := filepath.Join(home, ".claude", "projects", "-Users-x-proj")
		path := f.writeRaw(t, filepath.Join(dir, sid+".jsonl"), f.claudeRows(1+f.r.IntN(60), sid, false))
		for k := 0; k < f.r.IntN(3); k++ {
			f.writeRaw(t, filepath.Join(dir, sid, "subagents", fmt.Sprintf("agent-%d.jsonl", k)), f.claudeRows(1+f.r.IntN(10), sid, f.p(0.7)))
		}
		env := f.promptsEnv(t, home)
		calls = append(calls, call{Fn: "claude", Path: path, SID: sid, WS: f.p(0.7), Env: env})
		if f.p(0.2) {
			calls = append(calls, call{Fn: "collect", Client: "claude-code", Path: path, WS: true, Env: env})
		}
	}
	if bad := diffMany(t, calls, reportDiff(t, calls)); bad > 0 {
		t.Errorf("%d / %d 件が一致しない", bad, len(calls))
	}
	t.Logf("Claude Code の合成の会話記録 %d 件が記録と一致（seed %d）", len(calls), seed)
}

func (f *fuzz) codexRows(tid string, startMS int64, nTurns int, meta M) []any {
	var rows []any
	ms := startMS
	rows = append(rows, M{"timestamp": f.ts(ms), "type": "session_meta", "payload": merge(meta, M{"timestamp": f.ts(ms)})})
	var in, cached, out, reasoning int
	turn := 0
	for i := 0; i < nTurns; i++ {
		turn++
		tid := fmt.Sprintf("turn-%d", turn)
		ms += f.gap()
		if f.p(0.8) {
			rows = append(rows, codexEv(f.ts(ms), "task_started", M{"turn_id": tid}))
		}
		text := f.pick(fuzzTexts...)
		ms += int64(f.r.IntN(2000))
		ts := f.ts(ms)
		switch f.r.IntN(4) {
		case 0:
			rows = append(rows, codexEv(ts, "user_message", M{"message": text}))
		case 1:
			rows = append(rows, codexUser(ts, tid, text, 1)...)
			if f.p(0.3) {
				rows = append(rows, codexEv(f.ts(ms+int64(f.r.IntN(8000))), "user_message", M{"message": text}))
			}
		case 2:
			rows = append(rows, codexUser(ts, tid, text, 1)...)
		default:
			// 指示なし（再開の続きなど）
		}
		for k := 0; k < f.r.IntN(6); k++ {
			ms += int64(f.r.IntN(30000))
			switch f.r.IntN(10) {
			case 0: // 同じ値の繰り返し
			case 1: // 同じファイルの中で数え直し
				in, cached, out, reasoning = f.r.IntN(1000), 0, f.r.IntN(50), 0
			default:
				d := f.r.IntN(20000)
				in += d
				cached += f.r.IntN(d + 1)
				out += f.r.IntN(500)
				reasoning += f.r.IntN(100)
			}
			u := M{"input_tokens": in, "cached_input_tokens": min(cached, in), "output_tokens": out, "reasoning_output_tokens": reasoning,
				"total_tokens": in + out}
			if f.p(0.05) {
				u = M{"total_tokens": in + out} // 内訳の無い記録
			}
			if f.p(0.03) {
				u = M{}
			}
			rows = append(rows, M{"timestamp": f.ts(ms), "type": "event_msg", "payload": M{"type": "token_count", "info": M{"total_token_usage": u}}})
			if f.p(0.2) {
				rows = append(rows, M{"timestamp": f.ts(ms + 10), "type": "response_item", "payload": M{"type": "message", "role": "assistant", "content": []any{}}})
			}
			if f.p(0.1) && k == 0 {
				rows = append(rows, codexUser(f.ts(ms+20), tid, "割り込みの指示", 2)...)
			}
		}
		if f.p(0.8) {
			rows = append(rows, codexEv(f.ts(ms+100), f.pick("task_complete", "task_complete", "turn_aborted"), M{"turn_id": tid}))
		}
		if f.p(0.05) {
			rows = append(rows, M{"timestamp": f.ts(ms), "type": "event_msg", "payload": "not a dict"})
		}
	}
	return rows
}

func TestFuzzCodexMatchesGolden(t *testing.T) {
	seed, mult := fuzzParams()
	f := &fuzz{r: rand.New(rand.NewPCG(seed, 2))}
	var calls []call
	for i := 0; i < 150*mult; i++ {
		home := t.TempDir()
		tid := fmt.Sprintf("01a0c%03d-1111-7222-8333-444455556666", i)
		day := filepath.Join(home, ".codex", "sessions", "2026", "09", "18")
		meta := M{"session_id": tid, "id": tid, "cwd": "/tmp/x", "cli_version": f.pick("0.155.0-alpha.9", "0.140.0", "")}
		rows := f.codexRows(tid, int64(f.r.IntN(3600))*1000, 1+f.r.IntN(6), meta)
		toLines := func(rs []any) []string {
			out := make([]string, len(rs))
			for k, r := range rs {
				out[k] = jsonLine(r)
			}
			return out
		}
		p1 := f.writeRaw(t, filepath.Join(day, "rollout-2026-09-18T10-00-00-"+tid+".jsonl"), toLines(rows))
		paths := []string{p1}
		if f.p(0.4) {
			// 分割ファイル（同じスレッド）。前のファイルの末尾を複製することもある
			rows2 := f.codexRows(tid, int64(4000+f.r.IntN(3600))*1000, 1+f.r.IntN(4), meta)
			if f.p(0.5) && len(rows) > 2 {
				cp := rows[len(rows)-1-f.r.IntN(2):]
				rows2 = append(append([]any{rows2[0]}, cp...), rows2[1:]...)
			}
			sub := "rollout-2026-09-18T11-00-00-" + tid + "_01a0d000-0000-7000-8000-000000000000.jsonl"
			if f.p(0.2) {
				sub = "rollout-2026-09-19T11-00-00-" + tid + ".jsonl"
				day2 := filepath.Join(home, ".codex", "sessions", "2026", "09", "19")
				paths = append(paths, f.writeRaw(t, filepath.Join(day2, sub), toLines(rows2)))
			} else {
				paths = append(paths, f.writeRaw(t, filepath.Join(day, sub), toLines(rows2)))
			}
		}
		if f.p(0.2) { // 別スレッド（guardian）
			g := "01a0e000-0000-7000-8000-000000000000"
			f.writeRaw(t, filepath.Join(day, "rollout-2026-09-18T10-30-00-"+g+".jsonl"),
				toLines(f.codexRows(g, 1000, 1, M{"id": g, "session_id": tid, "cwd": "/tmp/x"})))
		}
		env := f.promptsEnv(t, home)
		for _, p := range paths {
			sid := tid
			if f.p(0.2) {
				sid = ""
			}
			calls = append(calls, call{Fn: "codex", Path: p, SID: sid, WS: f.p(0.7), Env: env})
		}
	}
	if bad := diffMany(t, calls, reportDiff(t, calls)); bad > 0 {
		t.Errorf("%d / %d 件が一致しない", bad, len(calls))
	}
	t.Logf("Codex の合成の会話記録 %d 件が記録と一致（seed %d）", len(calls), seed)
}

func TestFuzzCopilotMatchesGolden(t *testing.T) {
	seed, mult := fuzzParams()
	f := &fuzz{r: rand.New(rand.NewPCG(seed, 3))}
	var calls []call
	for i := 0; i < 60*mult; i++ {
		home := t.TempDir()
		var lines []string
		sec := int64(1789700000)
		for tr := 0; tr < 1+f.r.IntN(4); tr++ {
			trace := fmt.Sprintf("t%d", tr)
			sec += int64(f.r.IntN(300))
			for c := 0; c < f.r.IntN(5); c++ {
				var u M
				if f.p(0.85) {
					u = chatUsage(f.r.IntN(5000), f.r.IntN(3000), f.r.IntN(500), f.r.IntN(800), f.pick("gpt-5", "claude-sonnet-4.6", ""))
					if u["model"] == "" {
						delete(u, "model")
					}
				}
				var conv *string
				if f.p(0.15) {
					conv = strp("")
				} else if f.p(0.05) {
					conv = strp("other")
				}
				line := otelCLI(trace, fmt.Sprintf("c%d", c), "chat", sec+int64(c), conv, "a", u)
				lines = append(lines, line)
				if f.p(0.1) {
					lines = append(lines, line) // 同じスパンの重複
				}
			}
			if f.p(0.7) {
				ru := merge(chatUsage(f.r.IntN(20000), f.r.IntN(9000), f.r.IntN(900), f.r.IntN(2000), ""), M{"turns": f.r.IntN(6)})
				if f.p(0.5) { // Copilot CLI の根の形（cache の属性が無い）
					ru = M{"input_tokens": f.r.IntN(20000), "output_tokens": f.r.IntN(2000)}
				}
				lines = append(lines, otelCLI(trace, "root", "invoke_agent", sec-1, nil, "", ru))
			}
		}
		if f.p(0.2) {
			lines = append(lines, "not json", `{"name": "chat", "attributes": {"gen_ai.conversation.id": "`+copilotSID+`", "gen_ai.usage.input_tokens": "12"}}`)
		}
		dir := filepath.Join(home, ".copilot", "otel", f.pick("", "sub", ".hidden"))
		f.writeRaw(t, filepath.Join(dir, "a.jsonl"), lines)
		env := homeEnv(home, "COPILOT_HOME", filepath.Join(home, ".copilot"))
		now := float64(1789800000)
		calls = append(calls, call{Fn: "collect", Client: "copilot", SID: copilotSID, CWD: "/x/proj", WS: f.p(0.7), Env: env, Now: &now})
	}
	if bad := diffMany(t, calls, reportDiff(t, calls)); bad > 0 {
		t.Errorf("%d / %d 件が一致しない", bad, len(calls))
	}
	t.Logf("Copilot の合成の OTel %d 件が記録と一致（seed %d）", len(calls), seed)
}
