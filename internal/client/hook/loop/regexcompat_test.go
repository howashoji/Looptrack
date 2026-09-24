package loop

// 手で移した部品（語の分け方・先読み / 後読みのある正規表現・後方参照のあるヒアドキュメントの判定）を、以前の実装で記録した
// 結果（testdata/regex_golden.json）と突き合わせる。以前の hook（1.0.0 より前）と同じ式で記録した（記録の仕掛けは撤去済み）。
//
// 記録の ids / clean の欄は「1 課題 = 1 セッション」の束縛が使っていたイシュー ID の照合の結果。束縛は
// 機能ごと削除したので、突き合わせる相手のコードが無い（記録は書き換えずにそのまま残してある）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRegexPorts(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "regex_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cases []struct {
			S        string   `json:"s"`
			Prefix   string   `json:"prefix"`
			Width    int      `json:"width"`
			Shlex    []string `json:"shlex"`
			Redirect bool     `json:"redirect"`
			Heredoc  *string  `json:"heredoc"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cases) == 0 {
		t.Fatal("記録が空")
	}
	for _, w := range golden.Cases {
		toks, err := shlexSplit(w.S)
		if (err != nil) != (w.Shlex == nil) || (err == nil && !reflect.DeepEqual(nonNil(toks), nonNil(w.Shlex))) {
			t.Errorf("shlex %q: Go %q (%v) / 旧 %q", w.S, toks, err, w.Shlex)
		}
		if hasRedirect(w.S) != w.Redirect {
			t.Errorf("リダイレクト %q: Go %v / 旧 %v", w.S, hasRedirect(w.S), w.Redirect)
		}
		tag, ok := heredocTag(w.S)
		if ok != (w.Heredoc != nil) || (ok && tag != *w.Heredoc) {
			t.Errorf("ヒアドキュメント %q: Go %q %v / 旧 %v", w.S, tag, ok, w.Heredoc)
		}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestCompatPattern(t *testing.T) {
	for in, want := range map[string]string{
		`a\sb`:    `a[` + spaceClass + `]b`,
		`[^\s|&]`: `[^` + spaceClass + `|&]`,
		`\S+`:     `[^` + spaceClass + `]+`,
		`[]\s]`:   `[]` + spaceClass + `]`,
		`\\s`:     `\\s`,
		`x\.py\b`: `x\.py\b`,
	} {
		if got := compatPattern(in); got != want {
			t.Errorf("compatPattern(%q) = %q, want %q", in, got, want)
		}
	}
	if !compatRe(`^a\sb$`).MatchString("a　b") {
		t.Error("全角空白を \\s として扱う")
	}
}
