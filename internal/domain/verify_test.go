package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 検証コマンド節の抽出（§5-8-1）とマスク（§5-8-2）。例は testdata/verify_commands.json。

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
