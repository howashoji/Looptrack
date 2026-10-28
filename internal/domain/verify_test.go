package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 検証コマンド節の抽出（DESIGN.md §9-3-1）とマスク（§9-3-2）。例は testdata/verify_commands.json。

type verifyExamples struct {
	Extract []struct {
		Name     string   `json:"name"`
		Body     string   `json:"body"`
		Commands []string `json:"commands"`
	} `json:"extract"`
	Mask []struct {
		Name string `json:"name"`
		In   string `json:"in"`
		Out  string `json:"out"`
	} `json:"mask"`
}

func loadVerifyExamples(t *testing.T) verifyExamples {
	t.Helper()
	b, err := os.ReadFile("testdata/verify_commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var ex verifyExamples
	if err := json.Unmarshal(b, &ex); err != nil {
		t.Fatal(err)
	}
	if len(ex.Extract) < 5 || len(ex.Mask) == 0 {
		t.Fatalf("例が足りない: extract %d / mask %d", len(ex.Extract), len(ex.Mask))
	}
	return ex
}

func TestVerifyCommandsExamples(t *testing.T) {
	for _, c := range loadVerifyExamples(t).Extract {
		got := VerifyCommands(c.Body)
		if strings.Join(got, "\x00") != strings.Join(c.Commands, "\x00") || len(got) != len(c.Commands) {
			t.Errorf("%s: %q, want %q", c.Name, got, c.Commands)
		}
	}
}

func TestMaskSecretsExamples(t *testing.T) {
	for _, c := range loadVerifyExamples(t).Mask {
		got := MaskSecrets(c.In)
		if got != c.Out {
			t.Errorf("%s: %q, want %q", c.Name, got, c.Out)
		}
		if again := MaskSecrets(got); again != got {
			t.Errorf("%s: 2 回目で変わった: %q → %q", c.Name, got, again)
		}
	}
}

func TestVerifyLimits(t *testing.T) {
	var many []string
	for i := 0; i < MaxVerifyCommands; i++ {
		many = append(many, "true")
	}
	if e := CheckVerifyCommands(i18n.JA, "X-1", many); e != nil {
		t.Errorf("20 件は通る: %v", e)
	}
	if e := CheckVerifyCommands(i18n.JA, "X-1", append(many, "true")); e == nil || e.Code != "verify_commands_too_many" {
		t.Errorf("21 件: %v", e)
	}
	if e := CheckVerifyCommands(i18n.JA, "X-1", []string{strings.Repeat("あ", MaxVerifyCommandLen)}); e != nil {
		t.Errorf("1,000 文字は通る: %v", e)
	}
	if e := CheckVerifyCommands(i18n.JA, "X-1", []string{strings.Repeat("a", MaxVerifyCommandLen+1)}); e == nil || e.Code != "verify_command_too_long" {
		t.Errorf("1,001 文字: %v", e)
	}
}

func TestCleanVerifyOutput(t *testing.T) {
	// 末尾を UTF-8 の文字境界で 4,096 バイト以内に切る
	s := strings.Repeat("あ", 2000) // 6,000 バイト
	got := CleanVerifyOutput(s)
	if len(got) > MaxVerifyOutput || !utf8.ValidString(got) || !strings.HasSuffix(s, got) || len(got) < MaxVerifyOutput-3 {
		t.Errorf("長さ %d・有効 %v", len(got), utf8.ValidString(got))
	}
	// マスクしてから切る（前置きが切れる位置に秘密があっても残らない）
	s = strings.Repeat("x", MaxVerifyOutput-8) + " password=0123456789abcdef"
	if got := CleanVerifyOutput(s); strings.Contains(got, "0123456789") {
		t.Errorf("秘密が残った: %q", got[len(got)-40:])
	}
}

func TestBodySHA256(t *testing.T) {
	if BodySHA256("a") == BodySHA256("a ") || len(BodySHA256("")) != 64 {
		t.Error("本文の SHA-256")
	}
}

// MySQL のクライアントのパスワードと、よく知られた秘密の形（DESIGN.md §9-3-2）。値はどれも合成の偽物。
// 伏せる側と伏せない側の対照を同じテストに置く（伏せない側だけだと、規則が死んでいても通る）。
func TestMaskSecretsMySQLAndWellKnownForms(t *testing.T) {
	const pw = "FAKEpw0123" // 合成の値。伏せたら出力に残らない
	gh := "ghp_" + strings.Repeat("A1", 18)
	hits := []struct{ in, out, label string }{
		{"mysql -uroot -p" + pw + " im", "mysql -uroot -p*** im", "mysql -p…"},
		{"mysql -p'" + pw + " x' -h 127.0.0.1", "mysql -p*** -h 127.0.0.1", "mysql -p…"},
		{`MYSQL -p"` + pw + `"`, "MYSQL -p***", "mysql -p…"},
		{"mysqldump -u app --password=" + pw + " im > d.sql", "mysqldump -u app --password=*** im > d.sql", "mysql --password=…"},
		{"/usr/bin/mysqladmin -p" + pw + " status", "/usr/bin/mysqladmin -p*** status", "mysql -p…"},
		// -p の後に空白のある語（DB 名）を先に読み飛ばし、後ろのつなげた -p を伏せる
		{"mysql -p im -p" + pw, "mysql -p im -p***", "mysql -p…"},
		{"MYSQL_PWD=" + pw + " mysql -uroot", "MYSQL_PWD=*** mysql -uroot", "MYSQL_PWD=…"},
		{`MYSQL_PWD="` + pw + ` x" mysql`, "MYSQL_PWD=*** mysql", "MYSQL_PWD=…"},
		{"root:" + pw + "@tcp(127.0.0.1:13306)/im", "root:***@tcp(127.0.0.1:13306)/im", "…:…@tcp("},
		{"git clone https://user:" + pw + "@example.invalid/x.git", "git clone https://user:***@example.invalid/x.git", "://…:…@"},
		{"Authorization: Basic " + pw, "Authorization: Basic ***", "Authorization: Basic …"},
		{"AWS_SECRET_ACCESS_KEY=" + pw, "AWS_SECRET_ACCESS_KEY=***", "AWS_SECRET_ACCESS_KEY=…"},
		{"export STRIPE_KEY='" + pw + "'", "export STRIPE_KEY=***", "STRIPE_KEY=…"},
		{"DB_PASSWORD=" + pw, "DB_PASSWORD=***", "PASSWORD=…"},
		{"x " + gh + " y", "x ghp_*** y", "ghp_…"},
		{"github_pat_" + strings.Repeat("B2", 15), "github_pat_***", "github_pat_…"},
		{"xoxb-" + strings.Repeat("3c", 8), "xoxb-***", "xoxb-…"},
		{"ASIA" + strings.Repeat("Q", 16), "ASIA***", "ASIA…"},
		// OpenAI・Anthropic の API キーは既にある sk- の規則が見る（重ねていないことの確かめ）
		{"sk-proj-" + strings.Repeat("d4", 12), "sk-***", "sk-…"},
		{"sk-ant-api03-" + strings.Repeat("e5", 12), "sk-***", "sk-…"},
	}
	for i, c := range hits {
		got := MaskSecrets(c.in)
		if got != c.out {
			t.Errorf("%d: %q, want %q", i, got, c.out)
		}
		if strings.Contains(got, pw) || strings.Contains(got, "FAKEpw") {
			t.Errorf("値が残った: %q", c.out)
		}
		f, ok := FindSecret(c.in)
		if !ok || f.Label != c.label {
			t.Errorf("FindSecret(%q) = %+v %v, want %q", c.out, f, ok, c.label)
		}
		if strings.Contains(f.Label, "FAKE") || strings.Contains(f.Label, "A1A1") {
			t.Errorf("label に値が入った: %q", f.Label)
		}
		if again, n := MaskSecretsCount(got); again != got || len(n) != 0 {
			t.Errorf("2 回目で変わった/数えた: %q → %q %v", got, again, n)
		}
		if f, ok := FindSecret(got); ok {
			t.Errorf("マスクした後も当たった: %q → %+v", got, f)
		}
	}
	// 伏せない側。どれも上の規則の近くにあるが秘密ではない
	for _, clean := range []string{
		"mysql -p im",                  // -p の後に空白がある語は DB 名（パスワードは対話で聞かれる）
		"mysql -u root -p",             // 値の無い -p
		"mysql --password im",          // = の無い --password は値を取らない
		"mysql --password",             // 値の無い --password
		"mysql -h db -P3306 -u app -p", // 大文字の -P はポート
		"mysql の -p に値をつなげるとパスワードになります",                   // 説明の文中の語（-p の後に空白）
		"mkdir -pv /tmp/x && ssh -p22 host",               // mysql の後ではない -p
		"key=value と sort_key=name",                       // 小文字の key=
		"FOO_KEY=",                                        // 値の無い *_KEY=
		"https://example.invalid:8443/x@y",                // ポートと @ のある URL（利用者情報ではない）
		"ssh://git@example.invalid/x.git",                 // パスワードの無い利用者情報
		"ghp_short",                                       // 接頭辞だけ
		"MYSQL_PWD の値は環境変数で渡します",                          // = の無い変数名
		"mysql -p*** と root:***@tcp(h) と https://u:***@h", // マスク済み
	} {
		if got := MaskSecrets(clean); got != clean {
			t.Errorf("伏せない形を変えた: %q → %q", clean, got)
		}
		if f, ok := FindSecret(clean); ok {
			t.Errorf("伏せない形に当たった: %q → %+v", clean, f)
		}
	}
}
