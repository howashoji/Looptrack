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
	if w, ok := bashWork(cmd); ok {
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

// TestRecordQuoteReadings は、記録する側（実作業・コミットメッセージの先頭行・参照した ID・抜け道）が
// 引用符の組を posix と Windows の両方の読み方で読むことを固定する（どちらかで当たれば記録する。
// 抜け道は記録を止める側なので、両方で当たったときだけ抜け道とする）。
func TestRecordQuoteReadings(t *testing.T) {
	pat := &idMatcher{prefix: "TST-", width: 4}
	// 実作業: posix の読み方でだけ当たる（sh では `'` は二重引用符の中の文字で、commit は実行される）
	if w, ok := bashWork(`echo "it's" ; git commit -m 'x'`); !ok || !strings.Contains(w, "commit") {
		t.Errorf("posix の読み方の commit: got %q %v", w, ok)
	}
	// 実作業: Windows の読み方でだけ当たる（PowerShell では `"a\"` で閉じ、push は実行される）
	if w, ok := bashWork(`echo "a\" ; git push \""`); !ok || !strings.Contains(w, "push") {
		t.Errorf("Windows の読み方の push: got %q %v", w, ok)
	}
	// 対照: どちらの読み方でも引用符の中の文
	if w, ok := bashWork(`echo "git push"`); ok {
		t.Errorf("引用符の中の push を実作業と数えた: %q", w)
	}
	// コミットメッセージの先頭行: posix の読み方でだけ commit に当たる形
	if got := commitMessageHead(`echo "it's" ; git commit -m 'TST-0001 head'`); got != "TST-0001 head" {
		t.Errorf("posix の読み方の commit の先頭行: got %q", got)
	}
	// 参照した ID（単純コマンドに分けられない形）: posix の読み方でだけ当たる
	if got := bashEngagedIDs(`echo "it's" ; looptrack issue show TST-0001 ; echo 'x`, pat); !reflect.DeepEqual(got, []string{"TST-0001"}) {
		t.Errorf("posix の読み方の参照: got %q", got)
	}
	// 抜け道: 片方の読み方でだけ当たる形は抜け道にしない（記録を止めない）。対照は両方で当たる形
	if isEscapeCmd(`echo "it's" ; looptrack issue-freshness ack ; echo 'x`) {
		t.Error("posix の読み方だけで当たる抜け道で記録を止めた")
	}
	if !isEscapeCmd(`looptrack issue-freshness ack ; echo 'x`) {
		t.Error("前提が崩れています: 両方の読み方で当たる抜け道を認めない")
	}
}
