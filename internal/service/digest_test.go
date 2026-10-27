package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// セッション要約の規則（DESIGN.md §9-7・digest.go）。見出し・パスの正規化・秘密の検査・本文の上限は
// prepareComment の 1 か所にあり、REST・MCP・CLI の経路はすべて AddComment からそこを通る。

// digestHead は見出しの正しい形（端末のラベル・branch・head・ai）。
const digestHead = "セッション要約: home-mac\nbranch: feature/digest\nhead: ce768dc\nai: claude-code\n"

func TestNormalizeDigestPaths(t *testing.T) {
	hints := &PathHints{
		Roots:        []string{"/Users/me/proj", "/private/tmp/wt", "/tmp/wt", `C:\Users\me\proj`},
		Home:         "/Users/me",
		MachineLocal: []string{"/tmp/claude-501/scratch", "/Users/me/.claude/projects/"},
	}
	for _, c := range []struct{ name, in, want string }{
		{"literal のルート", "読んだ: /Users/me/proj/internal/service/digest.go", "読んだ: ./internal/service/digest.go"},
		{"realpath のルート（macOS の /private/…）", "cd /private/tmp/wt && go test", "cd . && go test"},
		{"/private の無い形", "/tmp/wt/bin/looptrack", "./bin/looptrack"},
		{"Windows のルート（区切りは残す）", `C:\Users\me\proj\cmd\x.go`, `.\cmd\x.go`},
		{"$HOME", "設定は /Users/me/.config/x にある", "設定は ~/.config/x にある"},
		{"ホームそのもの", "HOME=/Users/me", "HOME=~"},
		{"machine-local（末尾の区切りは無視）", "記録: /Users/me/.claude/projects/-Users-me-proj/a.jsonl", "記録: <machine-local>/-Users-me-proj/a.jsonl"},
		{"scratchpad", "/tmp/claude-501/scratch/out.txt", "<machine-local>/out.txt"},
		{"長い値が先（ルートはホームの下）", "/Users/me/proj/a と /Users/me/b", "./a と ~/b"},
		{"文末の句点（ルートそのものは「.」になり、句点は残る）", "場所は /Users/me/proj.", "場所は .."},
		{"かっこ・引用符の中", "(`/Users/me/proj/x`)", "(`./x`)"},
		{"日本語の直後", "パス/Users/me/proj/x", "パス./x"},
		{"1 行に複数", "/Users/me/proj/a /Users/me/proj/b", "./a ./b"},
		// 当たらない形
		{"続きのある名前（proj2）", "/Users/me/proj2/a", "~/proj2/a"},
		{"拡張子の付いた名前", "/Users/me/proj.bak/a", "~/proj.bak/a"},
		{"別のパスの途中", "/x/Users/me/proj/a", "/x/Users/me/proj/a"},
		{"相対パスの途中", "a/Users/me/proj", "a/Users/me/proj"},
		{"大小は区別する", "/users/me/proj/a", "/users/me/proj/a"},
	} {
		got, err := normalizeDigestPaths(c.in, hints)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: %q → %q, want %q", c.name, c.in, got, c.want)
		}
	}
	// 手がかりが無ければ何も置き換えない（サーバは端末のパスを知らない）
	for _, h := range []*PathHints{nil, {}} {
		if got, _ := normalizeDigestPaths("/Users/me/proj/a", h); got != "/Users/me/proj/a" {
			t.Errorf("手がかりなし（%v）で置き換わった: %q", h, got)
		}
	}
	// 置き換えた結果をもう一度置き換えない（左から 1 回だけ読む）
	got, _ := normalizeDigestPaths("/a/b/c", &PathHints{Roots: []string{"/a/b"}, Home: "/a"})
	if got != "./c" {
		t.Errorf("左から 1 回: %q", got)
	}
}

func TestDigestPathHintsRejected(t *testing.T) {
	for _, c := range []struct {
		name string
		h    *PathHints
	}{
		{"相対パス", &PathHints{Roots: []string{"proj"}}},
		{"ルートそのもの", &PathHints{Roots: []string{"/"}}},
		{"ドライブそのもの", &PathHints{MachineLocal: []string{`C:\`}}},
		{"ホームがルート", &PathHints{Home: "//"}},
		{"改行を含む", &PathHints{Home: "/Users/me\n/x"}},
		{"件数が多すぎる", &PathHints{Roots: make([]string, maxPathHintEntries+1)}},
		{"長すぎる", &PathHints{Home: "/" + strings.Repeat("a", maxPathHintBytes)}},
	} {
		_, err := prepareComment("DEMO-0001", digestHead, commentOpts{Hints: c.h})
		var se *Error
		if !errors.As(err, &se) || se.Kind != Invalid || se.Code != "invalid_argument" {
			t.Errorf("%s: err = %v, want Invalid invalid_argument", c.name, err)
		}
	}
	// 対照: 正しい手がかりは通る
	if _, err := prepareComment("DEMO-0001", digestHead, commentOpts{Hints: &PathHints{Roots: []string{"/Users/me/proj/"}, Home: "/Users/me"}}); err != nil {
		t.Errorf("正しい手がかりが拒まれた（前提が崩れています）: %v", err)
	}
}

func TestDigestHeader(t *testing.T) {
	ok := []struct{ name, text string }{
		{"日本語の印", digestHead + "\n## 現在地\n…"},
		{"英語の印", "Session digest: work-pc\nbranch: main\nhead: 0123456789abcdef0123456789abcdef01234567\nai: codex\n"},
		{"全角コロン（印とキー）", "セッション要約：home-mac\nbranch：main\nhead：CE768DC\nai：copilot"},
		{"順は問わない・キーの大小は問わない", "セッション要約: home-mac\nAI: other\nHead: ce768dc\nBranch: feature/x-1"},
		{"8 行目まで（間にほかの行）", "セッション要約: home-mac\nmachine: mac-mini\nsession: abc\n\nbranch: main\nnote: x\nhead: ce768dc\nai: claude-code\n本文"},
		{"CRLF", "セッション要約: home-mac\r\nbranch: main\r\nhead: ce768dc\r\nai: claude-code\r\n"},
		{"見出しより後の重複は見ない", digestHead + "\n\n\n\nbranch: other"},
	}
	for _, c := range ok {
		if err := checkDigestHeader("DEMO-0001", c.text); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	ng := []struct{ name, text, msg string }{
		{"ラベルが空", "セッション要約:\nbranch: main\nhead: ce768dc\nai: codex", "service.err.digest.label"},
		{"ラベルが空白だけ", "セッション要約:   \nbranch: main\nhead: ce768dc\nai: codex", "service.err.digest.label"},
		{"ラベルが長すぎる", "セッション要約: " + strings.Repeat("あ", maxDigestLabel+1) + "\nbranch: main\nhead: ce768dc\nai: codex", "service.err.digest.label"},
		{"branch が無い", "セッション要約: m\nhead: ce768dc\nai: codex", "service.err.digest.field_missing"},
		{"head が 9 行目", "セッション要約: m\nbranch: main\nai: codex\n\n\n\n\n\nhead: ce768dc", "service.err.digest.field_missing"},
		{"ai が無い", "セッション要約: m\nbranch: main\nhead: ce768dc", "service.err.digest.field_missing"},
		{"head が短い", "セッション要約: m\nbranch: main\nhead: ce768d\nai: codex", "service.err.digest.field_invalid"},
		{"head が 16 進でない", "セッション要約: m\nbranch: main\nhead: ce768dz\nai: codex", "service.err.digest.field_invalid"},
		{"head が 41 桁", "セッション要約: m\nbranch: main\nhead: " + strings.Repeat("a", 41) + "\nai: codex", "service.err.digest.field_invalid"},
		{"ai が知らない値", "セッション要約: m\nbranch: main\nhead: ce768dc\nai: gemini", "service.err.digest.field_invalid"},
		{"ai の大小違い", "セッション要約: m\nbranch: main\nhead: ce768dc\nai: Codex", "service.err.digest.field_invalid"},
		{"branch に空白", "セッション要約: m\nbranch: my branch\nhead: ce768dc\nai: codex", "service.err.digest.field_invalid"},
		{"branch が空", "セッション要約: m\nbranch:\nhead: ce768dc\nai: codex", "service.err.digest.field_invalid"},
		{"head が 2 つ", "セッション要約: m\nbranch: main\nhead: ce768dc\nhead: 0123456\nai: codex", "service.err.digest.field_duplicate"},
	}
	for _, c := range ng {
		err := checkDigestHeader("DEMO-0001", c.text)
		var se *Error
		if !errors.As(err, &se) || se.Kind != Rejected || se.Code != "digest_header" {
			t.Errorf("%s: err = %v, want Rejected digest_header", c.name, err)
			continue
		}
		var ie *i18n.Error
		if !errors.As(se.msg, &ie) || ie.Msg.ID != c.msg {
			t.Errorf("%s: 文面の ID = %v, want %s", c.name, se.msg, c.msg)
		}
		if ja, en := i18n.Text(i18n.JA, se), i18n.Text(i18n.EN, se); !strings.Contains(ja, "セッション要約") || !strings.Contains(en, "session digest") {
			t.Errorf("%s: 文面 ja %q / en %q", c.name, ja, en)
		}
	}
}

func TestPrepareCommentRules(t *testing.T) {
	const id = "DEMO-0001"
	// 秘密: 行番号と規則の語だけを出し、値は出さない（日英とも）
	secret := digestHead + "\n手順:\nAuthorization: Bearer abcDEF123secretvalue\n"
	_, err := prepareComment(id, secret, commentOpts{})
	var se *Error
	if !errors.As(err, &se) || se.Kind != Rejected || se.Code != "comment_secret" {
		t.Fatalf("秘密: err = %v, want Rejected comment_secret", err)
	}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		msg := i18n.Text(lang, se)
		if strings.Contains(msg, "abcDEF123secretvalue") {
			t.Errorf("%s: 拒否の文面に値が出た: %q", lang, msg)
		}
		if !strings.Contains(msg, "7") || !strings.Contains(msg, "Bearer …") || !strings.Contains(msg, "***") {
			t.Errorf("%s: 文面に行番号（7）・規則の語・書き直しの案内が無い: %q", lang, msg)
		}
	}
	// 散文の誤検知の形（規律の文章の引用）も当たる。*** に置き換えれば通る
	prose := digestHead + "token: の値は出さない\n"
	if _, err := prepareComment(id, prose, commentOpts{}); errCode(err) != "comment_secret" {
		t.Errorf("散文の token: が当たらない: %v", err)
	}
	if _, err := prepareComment(id, digestHead+"token: *** の値は出さない\n", commentOpts{}); err != nil {
		t.Errorf("*** に置き換えた形が拒まれた: %v", err)
	}
	// 秘密の検査は置き換えた後の本文に当てる（置き換えで行は変わらない）
	_, err = prepareComment(id, digestHead+"/Users/me/proj/x\npassword=hunter2\n", commentOpts{Hints: &PathHints{Roots: []string{"/Users/me/proj"}}})
	if !errors.As(err, &se) || se.Code != "comment_secret" || !strings.Contains(i18n.Text(i18n.JA, se), "6") {
		t.Errorf("置き換えの後の秘密の行番号: %v", err)
	}

	// 上限: 置き換えた後の長さで数える（ちょうどは通る）
	pad := func(n int) string { return digestHead + strings.Repeat("a", n-len(digestHead)) }
	if _, err := prepareComment(id, pad(MaxDigestBytes), commentOpts{}); err != nil {
		t.Errorf("上限ちょうどが拒まれた: %v", err)
	}
	if _, err := prepareComment(id, pad(MaxDigestBytes+1), commentOpts{}); errCode(err) != "comment_too_long" {
		t.Errorf("上限を 1 バイト超えた: %v", err)
	}
	long := digestHead + strings.Repeat("/Users/me/proj/a\n", (MaxDigestBytes)/17+1) // 置き換える前は上限を超え、置き換えた後は収まる
	if len(long) <= MaxDigestBytes {
		t.Fatalf("前提が崩れています: 置き換える前の長さ %d が上限以下", len(long))
	}
	if pc, err := prepareComment(id, long, commentOpts{Hints: &PathHints{Roots: []string{"/Users/me/proj"}}}); err != nil || len(pc.Text) > MaxDigestBytes {
		t.Errorf("置き換えた後に収まる要約が拒まれた: %v（%d バイト）", err, len(pc.Text))
	}

	// 状態の変更と同時の要約は拒む
	if _, err := prepareComment(id, digestHead, commentOpts{OnStatus: true}); errCode(err) != "digest_on_status" {
		t.Errorf("状態の変更と同時の要約: %v", err)
	}

	// 要約でないコメントは本文を変えない（手がかり・長さ・見出しを見ない）。対照として、同じ本文に印を付けると拒まれる
	plain := "作業メモ: /Users/me/proj/x " + strings.Repeat("x", MaxDigestBytes)
	for _, onStatus := range []bool{false, true} {
		pc, err := prepareComment(id, plain, commentOpts{Hints: &PathHints{Roots: []string{"/Users/me/proj"}}, OnStatus: onStatus})
		if err != nil || pc.Digest || pc.Text != plain || pc.SecretNotice != "" {
			t.Errorf("要約でないコメント（onStatus=%v）が変わった: %+v err=%v", onStatus, pc.Digest, err)
		}
	}
	for _, lead := range []string{"フィードバック: ", "判断: ", "Decision: "} {
		if pc, err := prepareComment(id, lead+plain, commentOpts{}); err != nil || pc.Text != lead+plain {
			t.Errorf("%q のコメントが変わった: %v", lead, err)
		}
	}
	if _, err := prepareComment(id, "セッション要約: m\n"+plain, commentOpts{}); err == nil {
		t.Errorf("印を付けた同じ本文が通った（前提が崩れています）")
	}
}

// 要約でないコメントの秘密の検査（comment.reject_secret）。既定（ルールなし・キーなし）と true は拒み、false は本文を変えずに
// 保存して注記を返す。要約はルールに関わらず拒む。当たらない本文はどの設定でも変わらない（対照）。
func TestPrepareCommentSecretRule(t *testing.T) {
	const id = "DEMO-0001"
	const value = "abcDEF123secretvalue"
	secret := "作業メモ\n手順:\nAuthorization: Bearer " + value + "\n"
	parse := func(raw string) *domain.Rules {
		t.Helper()
		r, err := domain.ParseRules([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return r
	}
	on := []struct {
		name  string
		rules *domain.Rules
	}{
		{"ルールなし", nil},
		{"comment のキーなし", parse(`{"session":{"digest_required":true}}`)},
		{"reject_secret のキーなし", parse(`{"comment":{}}`)},
		{"true", parse(`{"comment":{"reject_secret":true}}`)},
	}
	off := parse(`{"comment":{"reject_secret":false}}`)
	for _, c := range on {
		for _, onStatus := range []bool{false, true} {
			_, err := prepareComment(id, secret, commentOpts{Rules: c.rules, OnStatus: onStatus, Lang: i18n.JA})
			var se *Error
			if !errors.As(err, &se) || se.Kind != Rejected || se.Code != "comment_secret" {
				t.Fatalf("%s（onStatus=%v）: err = %v, want Rejected comment_secret", c.name, onStatus, err)
			}
			var ie *i18n.Error
			if !errors.As(se.msg, &ie) || ie.Msg.ID != "service.err.comment.secret" {
				t.Errorf("%s: 文面の ID = %v（要約の文面でなくコメントの文面のはず）", c.name, se.msg)
			}
			for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
				msg := i18n.Text(lang, se)
				if strings.Contains(msg, value) {
					t.Errorf("%s %s: 拒否の文面に値が出た: %q", c.name, lang, msg)
				}
				if !strings.Contains(msg, "3") || !strings.Contains(msg, "Bearer …") || !strings.Contains(msg, "***") {
					t.Errorf("%s %s: 文面に行番号（3）・規則の語・書き直しの案内が無い: %q", c.name, lang, msg)
				}
			}
		}
	}
	// 切: 本文を変えずに通し、注記（行番号と規則の語だけ。値なし。要求の言語）を返す
	for _, onStatus := range []bool{false, true} {
		for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
			pc, err := prepareComment(id, secret, commentOpts{Rules: off, OnStatus: onStatus, Lang: lang})
			if err != nil || pc.Text != secret || pc.Digest {
				t.Fatalf("切（onStatus=%v）: err=%v text 不変=%v", onStatus, err, pc.Text == secret)
			}
			want := i18n.T(lang, "service.comment.secret_notice", "id", id, "line", 3, "label", "Bearer …")
			if pc.SecretNotice != want || strings.Contains(pc.SecretNotice, value) || !strings.Contains(pc.SecretNotice, "comment.reject_secret") {
				t.Errorf("切 %s: 注記 %q, want %q", lang, pc.SecretNotice, want)
			}
		}
	}
	// 要約はルールに関わらず拒む（切でも）。文面は要約のもの
	_, err := prepareComment(id, digestHead+"token=abc123\n", commentOpts{Rules: off})
	var se *Error
	var ie *i18n.Error
	if !errors.As(err, &se) || se.Code != "comment_secret" || !errors.As(se.msg, &ie) || ie.Msg.ID != "service.err.digest.secret" {
		t.Errorf("切のプロジェクトの要約の秘密: %v", err)
	}
	// 対照: 当たらない本文（既にマスクした形を含む）はどの設定でも本文も注記も変わらない
	clean := "作業メモ: token=*** で確かめた（値は書かない）"
	for _, r := range []*domain.Rules{nil, parse(`{"comment":{"reject_secret":true}}`), off} {
		pc, err := prepareComment(id, clean, commentOpts{Rules: r})
		if err != nil || pc.Text != clean || pc.SecretNotice != "" {
			t.Errorf("当たらない本文が変わった（%v）: %+v err=%v", r, pc, err)
		}
	}
}

// 3 経路（REST は via=api、MCP、CLI）から同じ要約を同じ手がかりで送ると、保存される本文がバイト一致し、
// 記録に kind: "digest" が付く。拒んだ要約はコメントも記録も残さない。
func TestAddCommentDigestSameBytesAcrossPaths(t *testing.T) {
	e := newAttachEnv(t)
	hints := &PathHints{
		Roots:        []string{"/Users/me/proj", "/private/tmp/proj", "/tmp/proj"},
		Home:         "/Users/me",
		MachineLocal: []string{"/tmp/claude-501/sp"},
	}
	text := digestHead + "\n## 現在地\n- /Users/me/proj/internal/service/digest.go を直した\n- /private/tmp/proj/bin を作った\n" +
		"- 記録は /tmp/claude-501/sp/out.txt\n- 設定は /Users/me/.config/x\n"
	want := digestHead + "\n## 現在地\n- ./internal/service/digest.go を直した\n- ./bin を作った\n" +
		"- 記録は <machine-local>/out.txt\n- 設定は ~/.config/x" // 末尾の改行はコメントの保存の形（AppendComment）で落ちる
	var stored []string
	for _, via := range []string{"api", "mcp", "cli"} {
		a := e.editor
		a.Via = via
		it, err := e.s.AddComment(e.ctx, a, e.demo, e.issue.Row.ID, CommentInput{Text: text, PathHints: hints})
		if err != nil {
			t.Fatalf("%s: %v", via, err)
		}
		cs := it.Row.Doc.Comments
		stored = append(stored, cs[len(cs)-1].Content)
		if d := e.lastEventDetail(e.issue.Row.ID, "comment"); d["kind"] != "digest" {
			t.Errorf("%s: 記録の detail に kind: digest が無い: %v", via, d)
		}
	}
	for i, s := range stored {
		if s != want {
			t.Errorf("経路 %d の本文 %q, want %q", i, s, want)
		}
		if s != stored[0] {
			t.Errorf("経路 %d の本文がバイト一致しない", i)
		}
	}

	// 要約でないコメントの記録には kind を付けない（対照: 上の要約には付いた）
	if _, err := e.s.AddComment(e.ctx, e.editor, e.demo, e.issue.Row.ID, CommentInput{Text: "作業メモ /Users/me/proj", PathHints: hints}); err != nil {
		t.Fatal(err)
	}
	if d := e.lastEventDetail(e.issue.Row.ID, "comment"); d["kind"] != nil {
		t.Errorf("要約でないコメントの記録に kind が付いた: %v", d)
	}
	cur, err := e.s.Detail(e.ctx, e.demo, e.issue.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := cur.Row.Doc.Comments[len(cur.Row.Doc.Comments)-1].Content; got != "作業メモ /Users/me/proj" {
		t.Errorf("要約でないコメントが置き換わった: %q", got)
	}

	// 拒んだ要約（秘密・見出し・上限・状態の変更と同時）はコメントも記録も残さず、状態も変えない
	baseC, baseE := e.emptyCommentCounts(e.issue)
	for _, c := range []struct{ name, text, code string }{
		{"秘密", digestHead + "api_key=abcdef\n", "comment_secret"},
		{"見出し", "セッション要約: m\n本文だけ", "digest_header"},
		{"上限", digestHead + strings.Repeat("x", MaxDigestBytes), "comment_too_long"},
	} {
		if _, err := e.s.AddComment(e.ctx, e.editor, e.demo, e.issue.Row.ID, CommentInput{Text: c.text}); errCode(err) != c.code {
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
	if _, err := e.s.SetStatus(e.ctx, e.editor, e.demo, e.issue.Row.ID, "In Progress", digestHead, ""); errCode(err) != "digest_on_status" {
		t.Errorf("状態の変更と同時の要約: %v", err)
	}
	if c, ev := e.emptyCommentCounts(e.issue); c != baseC || ev != baseE {
		t.Fatalf("拒んだ要約が残った: コメント %d → %d・記録 %d → %d", baseC, c, baseE, ev)
	}
	if cur, _ := e.s.Detail(e.ctx, e.demo, e.issue.Row.ID); cur.Item.Status != e.issue.Item.Status {
		t.Errorf("拒んだ状態の変更が通った: %s", cur.Item.Status)
	}
	// 対照: 要約でないコメントを付けた状態の変更は通る
	if res, err := e.s.SetStatus(e.ctx, e.editor, e.demo, e.issue.Row.ID, "In Progress", "着手", ""); err != nil || !res.CommentAdded {
		t.Errorf("対照の状態の変更が通らない（前提が崩れています）: %v", err)
	}
}
