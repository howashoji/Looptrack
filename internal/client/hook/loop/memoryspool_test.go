package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/usagesnap"
)

// writeSpoolN は同じ置き場に退避を n 件書き、書いた順の名前を返す。
func writeSpoolN(t *testing.T, dir string, stamps ...time.Time) []string {
	t.Helper()
	var names []string
	for i, now := range stamps {
		name, err := writeSpool(dir, spoolEntry{Op: spoolAppend, Content: fmt.Sprintf("本文 %d", i)}, now)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func spoolBases(dir string) []string {
	var out []string
	for _, p := range usagesnap.SpoolFiles(dir) {
		out = append(out, filepath.Base(p))
	}
	return out
}

// 時計の刻みが粗いと続けて書いた退避が同じ時刻になる。それでも書いた順に送る。
func TestWriteSpoolSameStampKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 123_000_000)
	stamps := make([]time.Time, 40)
	for i := range stamps {
		stamps[i] = now
	}
	names := writeSpoolN(t, dir, stamps...)
	if got := spoolBases(dir); !slices.Equal(got, names) {
		t.Fatalf("書いた順に並びません:\n書いた順 %v\n並び     %v", names, got)
	}
}

// 時計が戻っても、後から書いた退避は後ろに並ぶ。
func TestWriteSpoolClockBackKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	names := writeSpoolN(t, dir, now, now.Add(-time.Hour), now.Add(-2*time.Hour))
	if got := spoolBases(dir); !slices.Equal(got, names) {
		t.Fatalf("書いた順に並びません:\n書いた順 %v\n並び     %v", names, got)
	}
}

// 送っている最中（.sending）と書きかけ（先頭の .）の退避より後ろに並ぶ。
// どちらも送信や書き込みが終われば名前の順に送られる側に戻るからだ。
func TestWriteSpoolAfterSendingAndPartial(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	later := now.Add(time.Minute)
	stamp := fmt.Sprintf("%d-%08x", later.Unix(), later.Nanosecond())
	for _, other := range []string{stamp + "aaaaaaaa.json" + spoolSendingSuffix, "." + stamp + "bbbbbbbb.json"} {
		t.Run(other, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, other), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			names := writeSpoolN(t, dir, now)
			back := filepath.Base(other)
			if back[0] == '.' {
				back = back[1:]
			}
			if names[0] <= back {
				t.Fatalf("%s が %s より前に並びます", names[0], back)
			}
		})
	}
}

// 対照: 時刻が進んでいれば、渡した時刻のまま名付ける（ずらすのは追い越されたときだけ）。
func TestWriteSpoolKeepsGivenStamp(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 5)
	stamps := []time.Time{now, now.Add(time.Millisecond), now.Add(time.Second)}
	names := writeSpoolN(t, dir, stamps...)
	for i, name := range names {
		want := fmt.Sprintf("%d-%08x", stamps[i].Unix(), stamps[i].Nanosecond())
		if name[:len(want)] != want {
			t.Fatalf("%d 件目の名前 %s が渡した時刻 %s で始まりません", i, name, want)
		}
	}
	if got := spoolBases(dir); !slices.Equal(got, names) {
		t.Fatalf("書いた順に並びません: %v / %v", names, got)
	}
}
