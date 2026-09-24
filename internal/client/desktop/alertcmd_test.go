package desktop

import (
	"strings"
	"testing"
)

// TestAlertCommandsPassTextAsArgs は、題と本文が必ず引数として渡る（文に埋め込まれない）ことを確かめる。
// 埋め込むと、引用符や改行を含む本文で文が壊れたり、思わぬものが実行されたりする。
func TestAlertCommandsPassTextAsArgs(t *testing.T) {
	const title = `題" と ' と改行`
	const msg = "本文\n2 行目 $(echo ng) `echo ng`"
	for _, goos := range []string{"darwin", "linux"} {
		for _, isErr := range []bool{true, false} {
			for _, c := range alertCommands(goos, title+"\n", msg, isErr) {
				var found int
				for _, a := range c[1:] {
					if a == title+"\n" || a == msg {
						found++
						continue
					}
					if strings.Contains(a, title) || strings.Contains(a, msg) {
						t.Errorf("%s/%v: 題か本文が別の引数に埋め込まれている: %q", goos, isErr, a)
					}
				}
				if found != 2 {
					t.Errorf("%s/%v: %s に題と本文がそのままの引数で渡っていない（%d 件）: %q", goos, isErr, c[0], found, c)
				}
			}
		}
	}
}

// TestAlertCommandsOrder は、試す順（Linux は zenity → kdialog → notify-send）と、
// macOS が osascript だけであることを確かめる。順番が変わると、より良い出し方が後回しになる。
func TestAlertCommandsOrder(t *testing.T) {
	var mac []string
	for _, c := range alertCommands("darwin", "t", "m", true) {
		mac = append(mac, c[0])
	}
	if len(mac) != 1 || mac[0] != "osascript" {
		t.Errorf("macOS は osascript だけのはず: %v", mac)
	}
	var lin []string
	for _, c := range alertCommands("linux", "t", "m", true) {
		lin = append(lin, c[0])
	}
	want := []string{"zenity", "kdialog", "notify-send"}
	if strings.Join(lin, ",") != strings.Join(want, ",") {
		t.Errorf("Linux の順: %v（%v のはず）", lin, want)
	}
}

// TestAlertCommandsSeverity は、error と情報で見た目の指定が変わることを確かめる
// （どちらも同じだと、利用者が異常に気づけない）。
func TestAlertCommandsSeverity(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		e := alertCommands(goos, "t", "m", true)
		i := alertCommands(goos, "t", "m", false)
		if len(e) != len(i) {
			t.Fatalf("%s: 試すコマンドの数が違う", goos)
		}
		same := true
		for n := range e {
			if strings.Join(e[n], "\x00") != strings.Join(i[n], "\x00") {
				same = false
			}
		}
		if same {
			t.Errorf("%s: error と情報で指定が同じ: %v", goos, e)
		}
	}
}
