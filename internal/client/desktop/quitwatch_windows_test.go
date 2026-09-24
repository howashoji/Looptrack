//go:build windows

package desktop

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// TestWatchQuitWithoutTray は、トレイの窓が無くても終了を頼めることを確かめる。
// このテストはプロセスを終わらせない（askStop が印を立て、watchQuit が受けるところまで）。
func TestWatchQuitWithoutTray(t *testing.T) {
	got := make(chan struct{}, 1)
	stop := watchQuit(func() { got <- struct{}{} })
	defer stop()

	// このテストのプロセスはトレイの窓を持たない（＝ --no-tray で動いているインスタンスと同じ状態）。
	if _, err := findTrayWindow(uint32(os.Getpid())); err == nil {
		t.Fatal("このテストのプロセスにトレイの窓があってはいけない（前提が崩れている）")
	}
	if err := askStop(i18n.JA, os.Getpid()); err != nil {
		t.Fatalf("トレイの窓が無くても終了を頼めるはず: %v", err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("終了の頼みが届かない（印を見張っていない）")
	}
}

// TestAskStopNoInstance は、相手がいないとき（印も窓も無い）に askStop がエラーを返すことを確かめる。
// 呼び出し側（quit）はこれを見て強制終了に進み、理由を出す。
//
// この経路は desktop.err.quit_event_and_window に window・event という 2 つの内側のエラー（どちらも
// i18n のエラー）を埋め込む。i18n.Text で文面を組み立ててから渡す回避が要らないことを、
// 出てきた文面に ID がそのまま出ていないことで確かめる。
func TestAskStopNoInstance(t *testing.T) {
	// 使われていない pid。0 は「自分のプロセスグループ」等の特別な値なので避ける。
	const notRunning = 0x7FFFFFF0
	err := askStop(i18n.JA, notRunning)
	if err == nil {
		t.Fatal("起動していない pid に終了を頼めてはいけない")
	}
	got := i18n.Text(i18n.JA, err)
	if !strings.Contains(got, "トレイの窓") || !strings.Contains(got, "SystrayClass") {
		t.Errorf("文面 = %q（トレイの窓（SystrayClass）… を含むはず）", got)
	}
	if !strings.Contains(got, "終了の印も立てられません") {
		t.Errorf("文面 = %q（終了の印も立てられません… を含むはず）", got)
	}
	if strings.Contains(got, "desktop.err.no_tray_window") || strings.Contains(got, "desktop.err.no_quit_event") {
		t.Errorf("文面に内側のエラーの ID が漏れている: %q", got)
	}
}

// TestWatchQuitStop は、見張りを終えた後に印を立てても呼ばれないことを確かめる（後始末の漏れの検知）。
func TestWatchQuitStop(t *testing.T) {
	got := make(chan struct{}, 1)
	stop := watchQuit(func() { got <- struct{}{} })
	stop()
	stop() // 2 回呼んでも壊れない
	time.Sleep(200 * time.Millisecond)
	if err := askStop(i18n.JA, os.Getpid()); err == nil {
		// 印は閉じられているので開けないのが正しい。開けた場合でも onQuit は呼ばれないこと。
		select {
		case <-got:
			t.Fatal("見張りを終えた後に呼ばれた")
		case <-time.After(500 * time.Millisecond):
		}
	}
}
