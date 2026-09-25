//go:build desktop

package tray

import (
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/desktop"
	"github.com/howashoji/looptrack/internal/i18n"
)

// TestMenuOrder はトレイのメニューの並びを固定する（項目が増減・入れ替わったときに気づくため）。
// 「終了」は必ず最後（区切りの下）で、いちばん上は「画面を開く」。「設定」はその次
// （ブラウザで開く操作をひとまとめにする）。
func TestMenuOrder(t *testing.T) {
	var got []string
	for _, it := range menuOrder(i18n.JA) {
		got = append(got, it.Label)
	}
	want := []string{"画面を開く", "設定", "AI の接続設定をコピー", "CLI を使えるようにする", "ログイン時に起動する", "終了"}
	if strings.Join(got, " / ") != strings.Join(want, " / ") {
		t.Errorf("メニューの並び:\n got %v\nwant %v", got, want)
	}
}

// TestMenuSettingsOpensAccountPage は、onReady の settings.ClickedCh が呼ぶ a.OpenSettings の経路が
// /account（アカウント設定の画面）であることを確かめる（systray の実物のクリックは追えないので、
// tray.go が呼ぶ関数と同じ desktop.SettingsPath を見る。URL の組み立て自体は desktop_test.go の
// TestOpenSettings が確かめる）。
func TestMenuSettingsOpensAccountPage(t *testing.T) {
	if desktop.SettingsPath != "/account" {
		t.Fatalf("desktop.SettingsPath = %q, want /account", desktop.SettingsPath)
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

// TestVersionItemLabel は、版の表示（miVersion）が main.version をそのまま含むことを、日本語・英語の
// 両方で確かめる（利用者の決定: 画面にも rc を含む版を出す。CFBundleShortVersionString と違い、こちらは
// 畳み込みをしない生の文字列なので rc.2・rc.10 のどちらも素通しになる）。
func TestVersionItemLabel(t *testing.T) {
	for _, c := range []struct {
		lang    i18n.Lang
		version string
	}{
		{i18n.JA, "v1.0.0-rc.2"},
		{i18n.JA, "v1.0.0-rc.10"},
		{i18n.JA, "v1.0.0"},
		{i18n.EN, "v1.0.0-rc.2"},
		{i18n.EN, "v1.0.0"},
	} {
		label := miVersion(c.lang, c.version).Label
		if !strings.Contains(label, c.version) {
			t.Errorf("miVersion(%v, %q).Label = %q に版の文字列が入っていない", c.lang, c.version, label)
		}
	}
	// 対照: 別の版を渡せば文面も変わる（version の埋め込みが実際に効いていることの確認）
	if miVersion(i18n.JA, "v1.0.0-rc.1").Label == miVersion(i18n.JA, "v1.0.0-rc.2").Label {
		t.Error("版が違うのに miVersion の文面が同じ")
	}
	if miVersion(i18n.JA, "v1.0.0").Label == miVersion(i18n.EN, "v1.0.0").Label {
		t.Error("ja と en で miVersion の文面が同じ（訳が入っていない）")
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
