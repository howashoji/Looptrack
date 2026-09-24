//go:build desktop

package tray

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TestMenuOrder はトレイのメニューの並びを固定する（項目が増減・入れ替わったときに気づくため）。
// 「終了」は必ず最後（区切りの下）で、いちばん上は「画面を開く」。
func TestMenuOrder(t *testing.T) {
	var got []string
	for _, it := range menuOrder(i18n.JA) {
		got = append(got, it.Label)
	}
	want := []string{"画面を開く", "AI の接続設定をコピー", "CLI を使えるようにする", "ログイン時に起動する", "終了"}
	if strings.Join(got, " / ") != strings.Join(want, " / ") {
		t.Errorf("メニューの並び:\n got %v\nwant %v", got, want)
	}
}

// TestMenuLabelsUnique は、同じ文面の項目が 2 つ並ばないことを確かめる（下位のメニューも含める）。
func TestMenuLabelsUnique(t *testing.T) {
	seen := map[string]bool{}
	all := append(menuOrder(i18n.JA), miShowMCP(i18n.JA))
	for _, it := range all {
		if it.Label == "" {
			t.Error("文面の無い項目がある")
		}
		if seen[it.Label] {
			t.Errorf("同じ文面の項目が 2 つある: %q", it.Label)
		}
		seen[it.Label] = true
	}
	for _, l := range desktop.MCPLabels(i18n.JA) {
		if seen[l] {
			t.Errorf("下位のメニューの文面が上の項目と同じ: %q", l)
		}
		seen[l] = true
	}
}

// TestQuitTip は「終了」の説明にアプリ名と、サーバも止まることが入っていることを確かめる
// （トレイを閉じるだけだと思って押す利用者に、サーバが止まると伝える）。
func TestQuitTip(t *testing.T) {
	tip := quitTip(i18n.JA)
	if !strings.Contains(tip, desktop.AppName) {
		t.Errorf("終了の説明にアプリ名がない: %q", tip)
	}
	if !strings.Contains(tip, "サーバ") {
		t.Errorf("終了の説明に、サーバも止まることが書かれていない: %q", tip)
	}
}

// TestMCPLabelsNotEmpty は、下位のメニュー（AI の接続設定のコピー）が空にならないことを確かめる。
// 空だと「AI の接続設定をコピー」を開いても何も出ない。
func TestMCPLabelsNotEmpty(t *testing.T) {
	if len(desktop.MCPLabels(i18n.JA)) == 0 {
		t.Error("AI の接続設定の下位のメニューが空")
	}
}

// TestAvailable は、この環境でトレイを出せるかの判定が、出せないときに理由を返すことを確かめる
// （理由はログに出て、利用者が --quit を知る手がかりになる）。
func TestAvailable(t *testing.T) {
	ok, why := available()
	if ok && why != "" {
		t.Errorf("出せるのに理由がある: %q", why)
	}
	if !ok && why == "" {
		t.Error("出せないのに理由が空（ログから原因が分からない）")
	}
}
