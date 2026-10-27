package clitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// looptrack session digest / pull（セッション要約。DESIGN.md §9-7）。
//
// digest は issue comment と同じ経路（POST /issues/{id}/comments・本文 text と path_hints）で送る。
// 見出しの検査・パスの置き換え・秘密の検査はサーバ（service）の 1 か所なので、偽 API はその結果（保存の応答か 422）を返すだけ。
// 422 の文面はサーバの規則（domain.FindSecret と service.err.digest.secret）から作り、規則と食い違わないようにする。

// digestFull は見出しをそろえた要約（git を起動しない）。
const digestFull = "セッション要約: desk-a\nbranch: feature/x\nhead: 0123abc\nai: claude-code\n\n現在地: CLI の session digest を足した。\n次の一手: golden を取り直す。\n"

// digestSecretValue は秘密の検査に当たる値（偽物）。出力に出ないことを確かめる。
const digestSecretValue = "zq8fakevalueforgolden"

var digestSecret = "セッション要約: desk-a\nbranch: feature/x\nhead: 0123abc\nai: codex\n\n試したこと: 設定に token: " + digestSecretValue + " を書いて確かめた。\n"

// digestSecretRes は 422 comment_secret（サーバの文面。行番号と規則の語だけで、値は含まない）。
func digestSecretRes() Response {
	f, ok := domain.FindSecret(digestSecret)
	if !ok {
		panic("前提が崩れています: digestSecret が domain.FindSecret に当たりません")
	}
	return ErrorResponse(422, "comment_secret", i18n.T(i18n.JA, "service.err.digest.secret", "id", "DEMO-0001", "line", f.Line, "label", f.Label))
}

// gitSeeds は作業ディレクトリを git の作業ツリーに見せる最小の .git（ブランチ feature/x・HEAD の SHA は固定）。
// git rev-parse はオブジェクトの中身を読まずに参照を解くので、コミットを作らなくても branch・head が決まる。
var gitSeeds = []Seed{
	{Path: "ws/.git/HEAD", Content: "ref: refs/heads/feature/x\n"},
	{Path: "ws/.git/refs/heads/feature/x", Content: "0123456789abcdef0123456789abcdef01234567\n"},
	{Path: "ws/.git/objects/.keep", Content: ""},
}

// detailWithComments は detail1 のコメントを差し替えたもの。
func detailWithComments(comments ...string) string {
	return strings.Replace(detail1, `"comments":[{"seq":1,"ts":"2024-05-02 11:30","content":"着手。"}]`,
		`"comments":[`+strings.Join(comments, ",")+`]`, 1)
}

func commentJSON(seq int, ts, content string) string {
	b, _ := json.Marshal(map[string]any{"seq": seq, "ts": ts, "content": content})
	return string(b)
}

// 保存された形（サーバがパスを ./ と ~ に置き換えた後）の要約 2 件。
var (
	pulledOld = "セッション要約: desk-a\nbranch: feature/x\nhead: 0123abc\nai: claude-code\n\n現在地: ./internal/client/cli/session.go を足した。\n"
	pulledNew = "Session digest: laptop-b\nbranch: feature/x\nhead: 89abcdef\nai: codex\n\nWhere things stand: ~/notes/plan.md is the plan.\n"
	detail2D  = detailWithComments(
		commentJSON(1, "2024-05-02 11:30", "着手。"),
		commentJSON(2, "2024-05-02 12:00", pulledOld),
		commentJSON(3, "2024-05-02 12:30", "フィードバック: 動いた"),
		commentJSON(4, "2024-05-03 09:00", pulledNew),
	)
)

// sessionCases は golden に記録するケース。
func sessionCases() []Case {
	return []Case{
		{Name: "session-digest/stdin", Group: "session", Args: []string{"digest", "DEMO-0001", "-"}, Stdin: digestFull,
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes))}},
		{Name: "session-digest/file", Group: "session", Args: []string{"digest", "DEMO-0001", "digest.md"},
			Seeds:  []Seed{{Path: "ws/digest.md", Content: digestFull}},
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes))}},
		{Name: "session-digest/empty", Group: "session", Args: []string{"digest", "DEMO-0001", "-"}, Stdin: " \n"},
		{Name: "session-digest/secret", Group: "session", Args: []string{"digest", "DEMO-0001", "-"}, Stdin: digestSecret,
			Routes: []Route{r("POST", pIssue1+"/comments", digestSecretRes())}},
		{Name: "session-pull/none", Group: "session", Args: []string{"pull", "DEMO-0001"},
			Routes: []Route{r("GET", pIssue1, ok(detail1))}},
		{Name: "session-pull/two", Group: "session", Args: []string{"pull", "DEMO-0001"},
			Routes: []Route{r("GET", pIssue1, ok(detail2D))}},
		{Name: "session-pull/limit-1", Group: "session", Args: []string{"pull", "DEMO-0001", "--limit", "1"},
			Routes: []Route{r("GET", pIssue1, ok(detail2D))}},
		{Name: "session-pull/bad-limit", Group: "session", Args: []string{"pull", "DEMO-0001", "--limit", "0"}},
		{Name: "session/help", Group: "session", Args: []string{"-h"}},
	}
}

// sessionFillCases は git の作業ツリーで見出しを足すケース。golden には記録しない（path_hints の roots に入る
// 作業ツリーの形が OS で変わる。macOS では一時ディレクトリの実体が /private/… なので、literal と realpath がどちらも $TMP/ws に
// 正規化される。git の誤りの文面も git の版で変わる）。送った text を TestSessionDigestFill で直に比べる。
func sessionFillCases() []Case {
	label := map[string]string{"LOOPTRACK_MACHINE_LABEL": "desk-b", "CLAUDE_CODE_SESSION_ID": "sess-digest-1"}
	return []Case{
		// 見出しの無い本文: 印・端末のラベル（LOOPTRACK_MACHINE_LABEL）・branch・head（git）・ai（環境変数）を足す
		{Name: "session-digest/fill", Group: "session", Args: []string{"digest", "DEMO-0001"}, Env: label, Seeds: gitSeeds,
			Stdin:  "現在地: テストを足した。\n次の一手: push する。\n",
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes))}},
		// 印だけ書いた本文: 空のラベルと、8 行目までに無い head・ai を 1 行目の直後に足す（書いてある branch は動かさない）
		{Name: "session-digest/fill-partial", Group: "session", Args: []string{"digest", "DEMO-0001", "-"}, Env: label, Seeds: gitSeeds,
			Stdin:  "セッション要約:\nbranch: feature/y\n\n現在地: 途中。\n",
			Routes: []Route{r("POST", pIssue1+"/comments", created(commentRes))}},
		// git の作業ツリーの外で見出しが欠けていれば、要求を送らずに止める
		{Name: "session-digest/no-git", Group: "session", Args: []string{"digest", "DEMO-0001", "-"}, Env: label,
			Stdin: "現在地: テストを足した。\n"},
	}
}

func findCase(t *testing.T, name string) Case {
	t.Helper()
	for _, c := range append(sessionCases(), sessionFillCases()...) {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("ケースがありません: %s", name)
	return Case{}
}

// TestSessionDigestSameAsComment は、同じ本文を session digest と issue comment で送ったとき、
// 要求の本文（text と path_hints）がバイト一致することを確かめる（サーバに保存される本文が一致する前提）。
func TestSessionDigestSameAsComment(t *testing.T) {
	impl := GoImpl(goBin(t))
	routes := []Route{r("POST", pIssue1+"/comments", created(commentRes))}
	viaSession := Run(t, impl, Case{Name: "x", Group: "session", Args: []string{"digest", "DEMO-0001", "-"}, Stdin: digestFull, Seeds: gitSeeds, Routes: routes})
	viaComment := Run(t, impl, Case{Name: "x", Args: []string{"comment", "DEMO-0001", "-"}, Stdin: digestFull, Seeds: gitSeeds, Routes: routes})
	for _, res := range []Result{viaSession, viaComment} {
		if res.Code != 0 || len(res.Requests) != 1 {
			t.Fatalf("前提が崩れています: exit %d・要求 %d 件\n%s", res.Code, len(res.Requests), res.Stderr)
		}
	}
	a := viaSession.Norm.Text(string(viaSession.Requests[0].Body))
	b := viaComment.Norm.Text(string(viaComment.Requests[0].Body))
	if a != b {
		t.Fatalf("session digest と issue comment で送る本文が違います:\nsession: %s\ncomment: %s", a, b)
	}
	// 対照: path_hints が実際に付いている（付かない経路どうしの一致では根拠にならない）
	if !strings.Contains(a, `"path_hints"`) || !strings.Contains(a, `"roots"`) {
		t.Fatalf("前提が崩れています: path_hints（roots）が付いていません: %s", a)
	}
}

// TestSessionDigestSecretNoValue は、秘密の検査で拒まれたとき終了コード 1 で、出力に行番号と規則の語が出て、値が出ないことを確かめる。
func TestSessionDigestSecretNoValue(t *testing.T) {
	res := Run(t, GoImpl(goBin(t)), findCase(t, "session-digest/secret"))
	if res.Code != 1 {
		t.Fatalf("exit = %d（1 のはず）\n%s", res.Code, res.Stderr)
	}
	out := res.Stdout + res.Stderr
	if strings.Contains(out, digestSecretValue) {
		t.Fatalf("出力に秘密の値が出ています:\n%s", out)
	}
	f, _ := domain.FindSecret(digestSecret)
	// 対照: 拒否の理由（行番号と規則の語）は出ている。値は要求の本文には載っている（送った側の確認）
	if line := fmt.Sprintf("%d 行目", f.Line); !strings.Contains(out, f.Label) || !strings.Contains(out, line) {
		t.Fatalf("前提が崩れています: 拒否の理由（%s・%s）が出ていません:\n%s", line, f.Label, out)
	}
	if len(res.Requests) != 1 || !bytes.Contains(res.Requests[0].Body, []byte(digestSecretValue)) {
		t.Fatalf("前提が崩れています: 値を含む本文を送っていません")
	}
}

// TestSessionPullFile は、pull が書くファイルが作業コピーの置き場（.claude/.looptrack-work/）にあり、新しい順で、
// 端末の絶対パス（作業ディレクトリ・HOME の一時パス）を含まないことを確かめる。
func TestSessionPullFile(t *testing.T) {
	res := Run(t, GoImpl(goBin(t)), findCase(t, "session-pull/two"))
	if res.Code != 0 {
		t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
	}
	got, ok := fileContent(res, "ws/.claude/.looptrack-work/DEMO-0001-digest.md")
	if !ok {
		t.Fatalf("ws/.claude/.looptrack-work/DEMO-0001-digest.md がありません")
	}
	text := string(got)
	if i, j := strings.Index(text, "laptop-b"), strings.Index(text, "desk-a"); i < 0 || j < 0 || i > j {
		t.Fatalf("新しい順になっていません:\n%s", text)
	}
	if strings.Contains(text, "フィードバック:") || strings.Contains(text, "着手。") {
		t.Fatalf("要約でないコメントが混ざっています:\n%s", text)
	}
	for _, p := range res.Norm.Paths {
		if p != "" && strings.Contains(text, p) {
			t.Fatalf("端末の絶対パス（%s）が残っています:\n%s", p, text)
		}
	}
	if res.Stdout != text {
		t.Fatalf("標準出力とファイルの中身が違います:\nstdout: %q\nfile: %q", res.Stdout, text)
	}
	if shown := strings.ReplaceAll(res.Stderr, `\`, "/"); !strings.Contains(shown, ".claude/.looptrack-work/DEMO-0001-digest.md") ||
		strings.Contains(res.Stderr, res.Norm.Paths[0]) {
		t.Fatalf("書き出したパスの表示がプロジェクトからの相対ではありません: %q", res.Stderr)
	}
}

// TestSessionDigestFill は、欠けた見出しの行を足した本文（送った text）と、git の外では要求を送らずに止まることを確かめる。
func TestSessionDigestFill(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	want := map[string]string{
		"session-digest/fill": "セッション要約: desk-b\nbranch: feature/x\nhead: " + sha + "\nai: claude-code\n\n現在地: テストを足した。\n次の一手: push する。\n",
		// 書いてある branch（feature/y）は動かさず、足りない head・ai を 1 行目の直後に足す
		"session-digest/fill-partial": "セッション要約: desk-b\nhead: " + sha + "\nai: claude-code\nbranch: feature/y\n\n現在地: 途中。\n",
	}
	impl := GoImpl(goBin(t))
	for name, text := range want {
		t.Run(name, func(t *testing.T) {
			res := Run(t, impl, findCase(t, name))
			if res.Code != 0 || len(res.Requests) != 1 {
				t.Fatalf("exit %d・要求 %d 件\n%s", res.Code, len(res.Requests), res.Stderr)
			}
			var body struct {
				Text      string `json:"text"`
				PathHints struct {
					Roots []string `json:"roots"`
					Home  string   `json:"home"`
				} `json:"path_hints"`
			}
			if err := json.Unmarshal(res.Requests[0].Body, &body); err != nil {
				t.Fatal(err)
			}
			if body.Text != text {
				t.Fatalf("送った本文が違います:\n got %q\nwant %q", body.Text, text)
			}
			if len(body.PathHints.Roots) == 0 || body.PathHints.Home == "" {
				t.Fatalf("path_hints に roots・home がありません: %s", res.Requests[0].Body)
			}
			if res.Requests[0].Header.Get("X-Looptrack-Session") != "sess-digest-1" {
				t.Fatalf("X-Looptrack-Session がありません: %v", res.Requests[0].Header)
			}
		})
	}
	t.Run("session-digest/no-git", func(t *testing.T) {
		res := Run(t, impl, findCase(t, "session-digest/no-git"))
		if res.Code != 1 || len(res.Requests) != 0 {
			t.Fatalf("exit %d・要求 %d 件（1・0 件のはず）\n%s", res.Code, len(res.Requests), res.Stderr)
		}
		if !strings.Contains(res.Stderr, "branch:") || !strings.Contains(res.Stderr, "git") {
			t.Fatalf("branch を git から決められない旨が出ていません: %q", res.Stderr)
		}
	})
}
