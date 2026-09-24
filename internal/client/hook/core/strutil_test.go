package core

// 鮮度ガードのコマンド文字列の読み取り（正規表現の先読み・後読みの手移植を含む）を、以前の hook（1.0.0 より前）の同じ処理の
// 結果と比べる。乱数で組み立てたシェルらしい入力（種は固定）と手で選んだ入力を以前の実装に通して記録したもの
// （testdata/parse_golden.json。以前の実装は撤去し、記録の仕掛けも消した）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
)

type parsed struct {
	Engaged []string `json:"engaged"`
	Head    string   `json:"head"`
	Work    *string  `json:"work"`
	Escape  bool     `json:"escape"`
	FindAll []string `json:"findall"`
	Full    bool     `json:"full"`
}

func parseGo(cmd string) parsed {
	pat := &idMatcher{prefix: "TST-", width: 4}
	p := parsed{Engaged: bashEngagedIDs(cmd, pat), Head: commitMessageHead(cmd), Escape: isEscapeCmd(cmd), FindAll: pat.findAll(cmd), Full: pat.fullMatch(cmd)}
	run := hookcmd.CommandText(cmd, true)
	if s, _ := gitWork(run, "commit", "merge", "push"); s >= 0 {
		w := workLabel(run, s)
		p.Work = &w
	}
	if p.Engaged == nil {
		p.Engaged = []string{}
	}
	if p.FindAll == nil {
		p.FindAll = []string{}
	}
	return p
}

func TestParseGolden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "parse_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		In   string `json:"in"`
		Want parsed `json:"want"`
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) == 0 {
		t.Fatal("記録が空")
	}
	fails, skipped := 0, 0
	for _, g := range golden {
		// 以前の入口（拡張子 .py のスクリプト）を呼ぶ入力は、その認識をやめたので比べない。
		// 記録（凍結）はそのまま残し、ここで外す。
		if strings.Contains(g.In, ".py") {
			skipped++
			continue
		}
		have := parseGo(g.In)
		if !reflect.DeepEqual(have, g.Want) {
			fails++
			if fails <= 10 {
				hj, _ := json.Marshal(have)
				wj, _ := json.Marshal(g.Want)
				t.Errorf("入力 %q:\n  Go %s\n  旧 %s", g.In, hj, wj)
			}
		}
	}
	t.Logf("以前の実装の記録と比べた入力: %d 件（不一致 %d・以前の入口を含むため比べなかったもの %d）", len(golden)-skipped, fails, skipped)
}
