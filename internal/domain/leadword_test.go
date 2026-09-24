package domain

import (
	"encoding/json"
	"os"
	"testing"
)

// コメントの先頭語（§5-8-6）。例は testdata/leadword.json。

// LeadWordExamples は testdata/leadword.json（判定の並びは server の DB テストも読む）。
type LeadWordExamples struct {
	Lead []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Kind    string `json:"kind"`
	} `json:"lead"`
	Excerpt []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		N       int    `json:"n"`
		Out     string `json:"out"`
	} `json:"excerpt"`
	// 英語の別名（§5-13）。Go だけが読む
	LeadEn []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Kind    string `json:"kind"`
	} `json:"lead_en"`
	ExcerptEn []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		N       int    `json:"n"`
		Out     string `json:"out"`
	} `json:"excerpt_en"`
}

func TestLeadWordExamples(t *testing.T) {
	b, err := os.ReadFile("testdata/leadword.json")
	if err != nil {
		t.Fatal(err)
	}
	var ex LeadWordExamples
	if err := json.Unmarshal(b, &ex); err != nil {
		t.Fatal(err)
	}
	if len(ex.Lead) < 10 || len(ex.Excerpt) == 0 {
		t.Fatalf("例が足りない: lead %d / excerpt %d", len(ex.Lead), len(ex.Excerpt))
	}
	for _, c := range ex.Lead {
		if got := LeadWord(c.Content); got != c.Kind {
			t.Errorf("%s: LeadWord(%q) = %q, want %q", c.Name, c.Content, got, c.Kind)
		}
	}
	for _, c := range ex.Excerpt {
		if got := FeedbackExcerpt(c.Content, c.N); got != c.Out {
			t.Errorf("%s: FeedbackExcerpt = %q, want %q", c.Name, got, c.Out)
		}
	}
}

// 英語の別名（Feedback: / Decision: / Changes requested:。大小を問わない・半角コロンだけ）。
func TestLeadWordEnglishAliases(t *testing.T) {
	b, err := os.ReadFile("testdata/leadword.json")
	if err != nil {
		t.Fatal(err)
	}
	var ex LeadWordExamples
	if err := json.Unmarshal(b, &ex); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, c := range ex.LeadEn {
		kinds[c.Kind] = true
		if got := LeadWord(c.Content); got != c.Kind {
			t.Errorf("%s: LeadWord(%q) = %q, want %q", c.Name, c.Content, got, c.Kind)
		}
	}
	for _, k := range []string{LeadFeedback, LeadDecision, LeadSendback, ""} {
		if !kinds[k] {
			t.Errorf("lead_en に種別 %q の例が無い", k)
		}
	}
	if len(ex.ExcerptEn) == 0 {
		t.Error("excerpt_en の例が無い")
	}
	for _, c := range ex.ExcerptEn {
		if got := FeedbackExcerpt(c.Content, c.N); got != c.Out {
			t.Errorf("%s: FeedbackExcerpt = %q, want %q", c.Name, got, c.Out)
		}
	}
}
