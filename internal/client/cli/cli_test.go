package cli

import (
	"bytes"
	"github.com/howashoji/looptrack/internal/i18n"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
)

// 引数の解釈（以前の CLI と互換）と Main の終了コード。config・guide の出力は internal/clitest の golden で確かめる。
// テストは env.FromMap の環境だけを使う（OS の LOOPTRACK_API_URL・トークンを読まない）。

func parse(t *testing.T, args ...string) (*Values, *Command, error) {
	t.Helper()
	return Parse(IssueCommand(i18n.JA), "looptrack issue", args)
}

func TestParseOptionsAndPositionals(t *testing.T) {
	v, leaf, err := parse(t, "status", "--comment=着手", "DEMO-0001", "--over", "理由", "In Progress")
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Name != "status" || v.Str("id") != "DEMO-0001" || v.Str("status") != "In Progress" || v.Str("comment") != "着手" ||
		v.Str("override") != "理由" || v.Str("assignee") != "" {
		t.Errorf("status: %+v", v.m)
	}
	v, _, err = parse(t, "activity", "A", "--since", "-5.5", "B", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := v.List("ids"); len(got) != 2 || got[1] != "B" || v.Float("since") != -5.5 || !v.Bool("json") {
		t.Errorf("activity: %+v", v.m)
	}
	v, _, err = parse(t, "usage", "ledger", "add", "名前", "--from-report", "-", "--total-tokens", "12")
	if err != nil {
		t.Fatal(err)
	}
	if v.Str("action") != "ledger" || v.Str("ledger_action") != "add" || v.Str("from_report") != "-" || v.Int("total_tokens") != 12 ||
		strings.Join(v.Path, " ") != "usage ledger add" {
		t.Errorf("usage ledger add: %+v %v", v.m, v.Path)
	}
	v, _, err = parse(t, "summary")
	if err != nil || v.Int("limit") != 10 || v.IsSet("agent") {
		t.Errorf("既定値: %+v %v", v.m, err)
	}
	v, _, err = parse(t, "comment", "DEMO-0001", "--", "--json は文字")
	if err != nil || v.Str("text") != "--json は文字" {
		t.Errorf("-- の後: %+v %v", v.m, err)
	}
	v, _, err = parse(t, "list", "--assignee", "-", "--status", "Todo", "--status", "Done")
	if err != nil || v.Str("assignee") != "-" || v.Str("status") != "Done" {
		t.Errorf("- の値・後の指定が勝つ: %+v %v", v.m, err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string][]string{
		"the following arguments are required: cmd":                               {},
		"argument cmd: invalid choice: 'nope' (choose from 'new', 'list'":         {"nope"},
		"the following arguments are required: id, text":                          {"comment"},
		"argument --limit: invalid int value: 'x'":                                {"summary", "--limit", "x"},
		"argument --sort: invalid choice: 'bad'":                                  {"list", "--sort", "bad"},
		"argument --status: expected one argument":                                {"list", "--status"},
		"unrecognized arguments: --nope extra":                                    {"config", "--nope", "extra"},
		"the following arguments are required: --agent":                           {"installed"},
		"ambiguous option: --re could match --ref, --reverse":                     {"list", "--re", "x"},
		"argument --json: ignored explicit argument 'yes'":                        {"config", "--json=yes"},
		"the following arguments are required: action":                            {"usage"},
		"argument action: invalid choice: 'on' (choose from 'show', 'attach'":     {"usage", "on"},
		"argument ledger_action: invalid choice: 'x' (choose from 'list', 'add')": {"usage", "ledger", "x"},
	}
	for want, args := range cases {
		_, _, err := parse(t, args...)
		ue, ok := err.(*UsageError)
		if !ok || !strings.Contains(ue.Msg, want) {
			t.Errorf("%v: %v（期待 %s）", args, err, want)
		}
	}
}

func run(t *testing.T, m map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	if m == nil {
		m = map[string]string{}
	}
	if _, ok := m["CLAUDE_PROJECT_DIR"]; !ok {
		m["CLAUDE_PROJECT_DIR"] = t.TempDir()
	}
	if _, ok := m["HOME"]; !ok {
		m["HOME"] = t.TempDir()
	}
	// 下の検査は日本語の文面を見るので、指定が無ければ日本語にする。
	// 指定が無いときに英語が出るのが本来の振る舞い（端末の設定に従い、日本語でなければ英語）。
	if _, ok := m["LOOPTRACK_LANG"]; !ok {
		m["LOOPTRACK_LANG"] = "ja"
	}
	code := Main(args, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(m))
	return code, out.String(), errb.String()
}

func TestMainExitCodes(t *testing.T) {
	if code, _, stderr := run(t, nil, "list", "--sort"); code != 2 || !strings.HasPrefix(stderr, "usage: looptrack issue list [-h]") ||
		!strings.Contains(stderr, "\nlooptrack issue list: error: argument --sort: expected one argument\n") {
		t.Errorf("引数の誤り: %d %q", code, stderr)
	}
	if code, stdout, _ := run(t, nil, "config", "-h"); code != 0 || !strings.Contains(stdout, "--json") {
		t.Errorf("-h: %d %q", code, stdout)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		// init は internal/client/kitinit が cli.InitRunner に登録する（cmd/looptrack）。登録の無いこのテストでは使えないと言う
		{[]string{"init", "--dry-run"}, "エラー: looptrack issue init はこのビルドにありません"},
	} {
		code, _, stderr := run(t, nil, c.args...)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("未実装 %v: %d %q", c.args, code, stderr)
		}
	}
}

func TestNoAPIURL(t *testing.T) {
	root := t.TempDir()
	code, _, stderr := run(t, map[string]string{"CLAUDE_PROJECT_DIR": root}, "config", "--json")
	// URL は固定値（本番）を出さない
	if code != 1 || !strings.Contains(stderr, "LOOPTRACK_API_URL=<サーバの URL> LOOPTRACK_PROJECT=<プロジェクト> looptrack issue config --json") ||
		strings.Contains(stderr, DefaultURL) {
		t.Errorf("URL が無いときの案内: %d %q", code, stderr)
	}
	// .claude/settings.json の env にあれば、その値を付けた実行例を出す
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"env":{"LOOPTRACK_API_URL":"https://x/im/","LOOPTRACK_PROJECT":"my proj"}}`), 0o644)
	code, _, stderr = run(t, map[string]string{"CLAUDE_PROJECT_DIR": root}, "config")
	if code != 1 || !strings.Contains(stderr, "  LOOPTRACK_API_URL=https://x/im LOOPTRACK_PROJECT='my proj' looptrack issue config") {
		t.Errorf("settings.json の値: %d %q", code, stderr)
	}
	code, _, stderr = run(t, nil, "guide")
	if code != 1 || stderr != "エラー: guide は API モードだけで使えます（LOOPTRACK_API_URL / LOOPTRACK_PROJECT を設定してください。導入は looptrack issue init）\n" {
		t.Errorf("guide: %d %q", code, stderr)
	}
}

func TestProjectFromSymlinkAndLooptrackEnv(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/markdown")
		w.Write([]byte("# ガイド\n"))
	}))
	defer srv.Close()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	if err := os.Symlink(filepath.Join(root, "x", "web")+"/", filepath.Join(root, ".claude", "issues")); err != nil {
		t.Skip("symlink を作れない:", err)
	}
	code, stdout, stderr := run(t, map[string]string{"CLAUDE_PROJECT_DIR": root, "LOOPTRACK_API_URL": srv.URL + "/im", "LOOPTRACK_TOKEN": "imp_lt"}, "guide")
	if code != 0 || stdout != "# ガイド\n" || len(got) != 1 || got[0] != "/im/api/v1/projects/web/guide Bearer imp_lt" {
		t.Errorf("%d %q %q %v", code, stdout, stderr, got)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"": "''", "abc-1.2/x": "abc-1.2/x", "a b": "'a b'", "it's": `'it'"'"'s'`, "日本": "'日本'"} {
		if got := shellQuote(in); got != want {
			t.Errorf("%q: %s（期待 %s）", in, got, want)
		}
	}
}

// localTimeIn は時間帯を引数で受け取る（既定へ倒すかは呼ぶ側が決める）。既定（応答に timezone が無いとき）は日本時間。
func TestLocalTime(t *testing.T) {
	for in, want := range map[string]string{"2026-09-18T03:04:05Z": "2026-09-18 12:04", "2026-09-18T03:04:05.123456+00:00": "2026-09-18 12:04",
		"2026-09-18T12:04:05+09:00": "2026-09-18 12:04", "not a time": "not a time"} {
		if got := localTimeIn(in, defaultTZ); got != want {
			t.Errorf("%s: %s（期待 %s）", in, got, want)
		}
	}
}

func TestKitSummary(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	write := func(s string) { os.WriteFile(filepath.Join(root, ".claude", ".looptrack-kit.json"), []byte(s), 0o644) }
	write(`{"core":{"bundle_sha256":"0123456789abcdef"},"loop":{"installed":true,"bundle_sha256":"beef","installed_at":"2026-09-17T16:00:00Z"}}`)
	if _, line, _ := kitSummary(i18n.JA, root); line != "core 0123456789ab / loop あり beef（2026-09-18）" {
		t.Errorf("loop あり: %s", line)
	}
	write(`{"loop":{"declined_at":"2026-09-01T00:00:00Z"}}`)
	if _, line, _ := kitSummary(i18n.JA, root); line != "core なし（配布物に kit が無い） / loop なし（辞退 2026-09-01）" {
		t.Errorf("辞退: %s", line)
	}
	write(`{broken`)
	if kit, line, _ := kitSummary(i18n.JA, root); kit != nil || line != "不明（.claude/.looptrack-kit.json を読めません）" {
		t.Errorf("壊れた: %s", line)
	}
}

// TestIsSelfRepo: looptrack 自身のリポジトリ（kit の正本）の判定。init の拒否と導入済み通知の self_repo が
// 同じこの 1 つを使う。見るのは配置だけで、ディレクトリの名前は見ない。
func TestIsSelfRepo(t *testing.T) {
	self := t.TempDir()
	if err := os.MkdirAll(filepath.Join(self, "cmd", "looptrack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(self, "kit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(self, "kit", "embed.go"), []byte("package kit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsSelfRepo(self) {
		t.Errorf("looptrack 自身のリポジトリを自身と判定しない: %s", self)
	}
	// kit だけ・cmd/looptrack だけ・どちらも無いディレクトリは普通のプロジェクト
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "kit", "embed.go"), 0o755); err != nil { // ファイルではなくディレクトリ
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(other, "cmd", "looptrack"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{other, t.TempDir(), ""} {
		if IsSelfRepo(dir) {
			t.Errorf("普通のプロジェクトを自身と判定した: %q", dir)
		}
	}
}
