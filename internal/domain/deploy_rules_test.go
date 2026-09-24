package domain

import (
	"os"
	"path/filepath"
	"testing"
)

// deploy/rules/*.json は利用者が本番へ project rules set で登録する。登録の前にサーバと同じ検査で読めることを確かめる。
func TestDeployRulesParse(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "deploy", "rules", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("deploy/rules/*.json が見つからない: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseRules(raw); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
}
