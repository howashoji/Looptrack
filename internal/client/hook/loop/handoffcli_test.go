package loop

// looptrack handoff append|compact の検査。
//
// 文面を見るところは LOOPTRACK_LANG=ja を環境に入れて言語を固定する（既定の言語は EN なので、
// 固定しないと「期待値を英語へ書き換えて緑にする」誘惑が生まれる）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// handoffTestEnv は OS の環境を読まない Env（テストがシェルの値に左右されないように）。
func handoffTestEnv(wd string, vars map[string]string) *Env {
	m := map[string]string{"LOOPTRACK_LANG": "ja"}
	for k, v := range vars {
		m[k] = v
	}
	return &Env{
		Getenv:  func(k string) string { return m[k] },
		Environ: func() []string { return nil },
		Getwd:   func() string { return wd },
		Now:     time.Now,
	}
}

// runHandoff は 1 回の呼び出し（終了コードと標準出力・標準エラー）。
func runHandoff(e *Env, stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = Handoff(context.Background(), args, strings.NewReader(stdin), &out, &errb, e)
	return code, out.String(), errb.String()
}

func sha256of(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		b = nil
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestHandoffAppendMark は、追記した節の見出しの直後に印が 1 行入ることを見る。
func TestHandoffAppendMark(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "handoff.md")
	e := handoffTestEnv(dir, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-a"})
	fixed := time.Date(2026, 9, 21, 9, 30, 0, 0, time.UTC)
	e.Now = func() time.Time { return fixed }
	code, out, errOut := runHandoff(e, "", "append", "--file", p, "## セッション A の申し送り\n\n本文 A")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimLeft(string(got), "\n"), "\n")
	if lines[0] != "## セッション A の申し送り" {
		t.Errorf("1 行目が見出しではありません: %q", lines[0])
	}
	if want := writerMarkAt("sess-a", fixed); lines[1] != want {
		t.Errorf("見出しの直後が印ではありません: %q（期待 %q）", lines[1], want)
	}
	// 書いた印を読む側（鮮度ガード）の解釈器に通す（形が食い違えば、ここで時刻が取れなくなる）
	times, timeless := writerMarkTimes(string(got), "sess-a")
	if len(times) != 1 || !times[0].Equal(fixed) || timeless {
		t.Errorf("読む側が印の時刻を読めません: times=%v timeless=%v（期待 [%s] false）", times, timeless, fixed.Format(time.RFC3339))
	}
	t.Logf("往復: 書いた印 %q → 読んだ時刻 %v（時刻なしの印 %v）", lines[1], times, timeless)
	if !strings.Contains(string(got), "本文 A") {
		t.Errorf("本文が入っていません:\n%s", got)
	}
	if n := strings.Count(string(got), "<!-- looptrack:session "); n != 1 {
		t.Errorf("印は 1 つのはずです: %d 件", n)
	}
	// 見出しの無い本文には見出しを作る（節の途中から始まる追記を残さない）
	code, _, errOut = runHandoff(e, "見出しの無い本文", "append", "--file", p)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	got2, _ := os.ReadFile(p)
	if !strings.Contains(string(got2), "## "+fixed.Format("2006-01-02 15:04:05")+" sess-a") {
		t.Errorf("見出しが作られていません:\n%s", got2)
	}
}

// TestHandoffAppendConcurrent は、2 つのセッションが同時に追記しても、どちらの追記も失われないことを見る。
//
// 落ちるとしたら「読んで全文を書き戻す」形にしたときなので、O_APPEND の 1 回書きを守っているかがここで分かる。
func TestHandoffAppendConcurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "handoff.md")
	const rounds = 30
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, sid := range []string{"sess-a", "sess-b"} {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			e := handoffTestEnv(dir, map[string]string{"CLAUDE_CODE_SESSION_ID": sid})
			<-start
			for i := 0; i < rounds; i++ {
				body := fmt.Sprintf("## %s の申し送り %02d\n\n%s の本文 %02d", sid, i, sid, i)
				if code, out, errOut := runHandoff(e, body, "append", "--file", p); code != 0 {
					t.Errorf("%s の %d 回目: exit %d %s %s", sid, i, code, out, errOut)
					return
				}
			}
		}(sid)
	}
	close(start)
	wg.Wait()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	missing := 0
	for _, sid := range []string{"sess-a", "sess-b"} {
		for i := 0; i < rounds; i++ {
			head := fmt.Sprintf("## %s の申し送り %02d", sid, i)
			body := fmt.Sprintf("%s の本文 %02d", sid, i)
			if !strings.Contains(text, head) || !strings.Contains(text, body) {
				missing++
				t.Errorf("同時の追記が失われています: %q", head)
			}
			if n := strings.Count(text, head); n != 1 {
				t.Errorf("%q が %d 件あります（1 件のはず）", head, n)
			}
		}
	}
	// 節と印の数が合っていれば、行が混ざっていないと言える
	if n := strings.Count(text, writerMarkPrefix+"sess-a "); n != rounds {
		t.Errorf("sess-a の印が %d 件（%d 件のはず）", n, rounds)
	}
	if n := strings.Count(text, writerMarkPrefix+"sess-b "); n != rounds {
		t.Errorf("sess-b の印が %d 件（%d 件のはず）", n, rounds)
	}
	t.Logf("同時追記 %d 件（2 セッション × %d 回）・失われたもの %d 件・ファイル %d バイト", rounds*2, rounds, missing, len(b))
}

// TestHandoffCompactConflict は、読んだ時点の SHA-256 と食い違えば拒否し、そのときファイルを変えないことを見る。
func TestHandoffCompactConflict(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "handoff.md")
	if err := os.WriteFile(p, []byte("## もとの節\n\nもとの本文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := sha256of(t, p)
	// ほかのセッションが書いた（＝読んだ時点の SHA-256 が古くなった）
	if err := os.WriteFile(p, []byte("## もとの節\n\nもとの本文\n\n## ほかのセッションの節\n\nその本文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	e := handoffTestEnv(dir, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-a"})
	code, _, errOut := runHandoff(e, "## 圧縮した節\n\n圧縮した本文", "compact", "--file", p, "--sha", stale)
	if code != handoffExitConflict {
		t.Fatalf("exit %d（衝突は %d のはず）: %s", code, handoffExitConflict, errOut)
	}
	if !strings.Contains(errOut, "ほかのセッションが引き継ぎを書き換えています") {
		t.Errorf("拒否の文面が違います:\n%s", errOut)
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(before) {
		t.Errorf("拒否したのにファイルが変わりました:\n%s", after)
	}
	if isFile(p + ".lock") {
		t.Errorf("ロックが残っています: %s", p+".lock")
	}
	t.Logf("拒否の出力: %s", strings.TrimSpace(strings.SplitN(errOut, "\n", 2)[0]))

	// 読み直した SHA-256 なら通る
	code, out, errOut := runHandoff(e, "## 圧縮した節\n\n圧縮した本文", "compact", "--file", p, "--sha", sha256of(t, p))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "## 圧縮した節\n\n圧縮した本文\n" {
		t.Errorf("書き直しの結果が違います: %q", got)
	}
	if !strings.Contains(out, "SHA-256: "+sha256of(t, p)) {
		t.Errorf("書き直した後の SHA-256 が出ていません:\n%s", out)
	}
}

// TestHandoffCompactDropMarks は、--drop-marks で印の行だけが落ち、見出しと本文が残ることを見る。
func TestHandoffCompactDropMarks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "handoff.md")
	e := handoffTestEnv(dir, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-a"})
	for _, sid := range []string{"sess-a", "sess-b"} {
		ev := handoffTestEnv(dir, map[string]string{"CLAUDE_CODE_SESSION_ID": sid})
		body := fmt.Sprintf("## %s の申し送り\n\n%s の本文", sid, sid)
		if code, _, errOut := runHandoff(ev, body, "append", "--file", p); code != 0 {
			t.Fatalf("append: exit %d %s", code, errOut)
		}
	}
	cur, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(cur), writerMarkPrefix); n != 2 {
		t.Fatalf("前提: 印は 2 件のはずです: %d 件", n)
	}
	// 前提: 落とす対象は時刻つきの印（時刻なしの古い印しか落とせない形になっていないことを先に確かめる）
	for _, sid := range []string{"sess-a", "sess-b"} {
		if times, _ := writerMarkTimes(string(cur), sid); len(times) != 1 {
			t.Fatalf("前提: %s の印が時刻つきではありません: %v\n%s", sid, times, cur)
		}
	}
	// 読んだ全文をそのまま渡す（先頭の空行は実装が落とす）
	code, out, errOut := runHandoff(e, string(cur), "compact", "--file", p, "--drop-marks", "--sha", sha256of(t, p))
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	got, _ := os.ReadFile(p)
	if n := strings.Count(string(got), writerMarkPrefix); n != 0 {
		t.Errorf("印が残っています: %d 件\n%s", n, got)
	}
	for _, w := range []string{"## sess-a の申し送り", "sess-a の本文", "## sess-b の申し送り", "sess-b の本文"} {
		if !strings.Contains(string(got), w) {
			t.Errorf("%q が消えました:\n%s", w, got)
		}
	}
	if !strings.HasPrefix(string(got), handoffHeadingPrefix) {
		t.Errorf("見出しから始まっていません: %q", strings.SplitN(string(got), "\n", 2)[0])
	}
	t.Logf("--drop-marks の後: 印 %d 件・節 %d 件・%d バイト",
		strings.Count(string(got), writerMarkPrefix), strings.Count(string(got), "\n## ")+1, len(got))

	// 既定では落とさない（並行作業中に黙って判定を壊さない）
	if code, _, errOut := runHandoff(e, string(got)+"\n"+writerMarkAt("sess-a", time.Date(2026, 9, 21, 9, 30, 0, 0, time.UTC))+"\n", "compact", "--file", p, "--sha", sha256of(t, p)); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	again, _ := os.ReadFile(p)
	if n := strings.Count(string(again), writerMarkPrefix); n != 1 {
		t.Errorf("既定で印が落ちています（%d 件）:\n%s", n, again)
	}
}

// TestHandoffCompactGuards は、見出し・SHA-256・ロックの入口の判定を見る。
func TestHandoffCompactGuards(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "handoff.md")
	if err := os.WriteFile(p, []byte("## もとの節\n\nもとの本文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := handoffTestEnv(dir, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-a"})
	cur := sha256of(t, p)

	// 見出しから始まらない本文は拒否（節の途中で切れた本文を書かせない）
	if code, _, errOut := runHandoff(e, "見出しの無い全文", "compact", "--file", p, "--sha", cur); code != handoffExitUsage {
		t.Errorf("exit %d（%d のはず）: %s", code, handoffExitUsage, errOut)
	} else if !strings.Contains(errOut, "の見出しから始めてください") {
		t.Errorf("文面が違います: %s", errOut)
	}
	// --sha が無ければ拒否
	if code, _, errOut := runHandoff(e, "## 節\n\n本文", "compact", "--file", p); code != handoffExitUsage {
		t.Errorf("exit %d: %s", code, errOut)
	}
	if b, _ := os.ReadFile(p); string(b) != "## もとの節\n\nもとの本文\n" {
		t.Errorf("拒否したのにファイルが変わりました: %q", b)
	}

	// ロック中は待たずに拒否する
	lock := p + ".lock"
	if err := os.WriteFile(lock, []byte("pid=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runHandoff(e, "## 節\n\n本文", "compact", "--file", p, "--sha", cur); code != handoffExitConflict {
		t.Errorf("exit %d（%d のはず）: %s", code, handoffExitConflict, errOut)
	}
	// 失効時間を過ぎたロックは奪える（落ちたセッションのロックで止まり続けないこと）
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runHandoff(e, "## 節\n\n本文", "compact", "--file", p, "--sha", cur, "--lock-ttl", "60"); code != 0 {
		t.Errorf("失効したロックを奪えません: exit %d %s", code, errOut)
	}
	if isFile(lock) {
		t.Errorf("ロックが残っています: %s", lock)
	}
}

// TestWriterMarkLiteralDefinedOnce は、印の形の文字列がこのパッケージの原本に 1 か所しか無いことを見る。
//
// 書く側（handoff append・compact --drop-marks）と読む側（鮮度ガード）が同じ文字列を別々に持つと、
// 片方だけを変えたときに**書いても差し戻される**状態になり、しかも双方のテストは自分の持つ形しか
// 与えないので**緑のまま**になる。実際に、同じ値の const が 2 ファイルに置かれていた。
//
// 同じ値を期待値として書き写すだけのテストでは、2 か所を 3 か所にしただけで意味が無い。ここでは
// **原本を走査して、印の文字列リテラルの出現が 1 回であること**と、その 1 回が定数そのものであることを見る。
// したがって定数の値を変えてもこのテストは緑のままで、**定義を増やしたときだけ落ちる**。
//
// 見るのはこのパッケージのディレクトリの中の、テストでない .go だけ（印を使うのはこのパッケージだけ）。
func TestWriterMarkLiteralDefinedOnce(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	type hit struct {
		where string
		value string
	}
	var marks, suffixes []hit
	files := 0
	fset := token.NewFileSet()
	for _, ent := range ents {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			h := hit{where: fset.Position(lit.Pos()).String(), value: v}
			if strings.Contains(v, "looptrack:session") {
				marks = append(marks, h)
			}
			if v == writerMarkSuffix {
				suffixes = append(suffixes, h)
			}
			return true
		})
	}
	if files == 0 {
		t.Fatal("走査した原本が 0 件です（このテストは何も確かめていません）")
	}
	if len(marks) != 1 {
		t.Errorf("印の前置きの文字列が %d か所にあります（1 か所のはず）: %v", len(marks), marks)
	} else if marks[0].value != writerMarkPrefix {
		t.Errorf("唯一の印の文字列が定数と違います: %q（writerMarkPrefix = %q）@ %s",
			marks[0].value, writerMarkPrefix, marks[0].where)
	}
	if len(suffixes) != 1 {
		t.Errorf("印の閉じの文字列が %d か所にあります（1 か所のはず）: %v", len(suffixes), suffixes)
	}
	t.Logf("原本 %d 件を走査: 印の前置き %v・閉じ %v", files, marks, suffixes)
}

// TestHandoffAppendMarkRoundTrip は、append が書いた印を読む側（鮮度ガードの解釈器）がそのまま読めること、
// 時刻の無い古い印が混ざっていても従来どおり扱えること、--drop-marks がどちらの形も落とすことを見る。
//
// 書く側の形を時刻なしに戻すと「時刻が 1 件」が 0 件になって落ちる。読む側の解釈を変えても落ちる。
func TestHandoffAppendMarkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "handoff.md")
	// 古い形（時刻なし）の印が既に入っているファイルに追記する
	if err := os.WriteFile(p, []byte("## 前の周の節\n"+writerMark("sess-a")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 21, 12, 34, 56, 0, time.UTC)
	e := handoffTestEnv(dir, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-a"})
	e.Now = func() time.Time { return at }
	if code, out, errOut := runHandoff(e, "## いまの周の節\n\n本文 A", "append", "--file", p); code != 0 {
		t.Fatalf("append: exit %d %s %s", code, out, errOut)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	// 往復: 書いた印を、読む側の writerMarkTimes に渡す
	times, timeless := writerMarkTimes(string(got), "sess-a")
	if len(times) != 1 || !times[0].Equal(at) {
		t.Errorf("書いた印の時刻を読めません: %v（期待 [%s] 1 件）", times, at.Format(time.RFC3339))
	}
	if !timeless {
		t.Errorf("時刻の無い古い印が読めなくなっています（後方互換が壊れました）:\n%s", got)
	}
	if !strings.Contains(string(got), writerMarkAt("sess-a", at)) {
		t.Errorf("時刻つきの印が入っていません:\n%s", got)
	}
	t.Logf("往復: 印 %q → 時刻 %v・時刻なしの印 %v", writerMarkAt("sess-a", at), times, timeless)

	// ID の先頭一致では拾わない（sess-a の印は sess の印ではない）
	if ts, tl := writerMarkTimes(string(got), "sess"); len(ts) != 0 || tl {
		t.Errorf("ID の先頭一致で拾っています: times=%v timeless=%v", ts, tl)
	}

	// --drop-marks は、時刻つきも時刻なしも落とす
	if code, out, errOut := runHandoff(e, string(got), "compact", "--file", p, "--drop-marks", "--sha", sha256of(t, p)); code != 0 {
		t.Fatalf("compact: exit %d %s %s", code, out, errOut)
	}
	dropped, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(dropped), writerMarkPrefix); n != 0 {
		t.Errorf("印が %d 件残っています:\n%s", n, dropped)
	}
	for _, w := range []string{"## 前の周の節", "## いまの周の節", "本文 A"} {
		if !strings.Contains(string(dropped), w) {
			t.Errorf("%q が消えました:\n%s", w, dropped)
		}
	}
	t.Logf("--drop-marks の後: 印 %d 件・%d バイト", strings.Count(string(dropped), writerMarkPrefix), len(dropped))
}
