package desktop

import "strconv"

// quitEventName は、起動中のインスタンスに終了を頼むための印の名前（Windows の名前付きイベント）。
//
// Windows の GUI のプロセスにはシグナルを送れないので、以前はトレイの隠し窓に WM_CLOSE を送っていた。
// それだと --no-tray やトレイを出せない環境では頼む手立てが無く、必ず強制終了（TerminateProcess）に
// 落ちていた。トレイの有無に依らず同じ印を使う。
//
// Local\ は同じログオンセッションの中だけで見える名前空間（デスクトップ版は頼む側も頼まれる側も同じ
// セッションで動く）。pid を付けるので、別のデータの置き場で動くインスタンスとも混ざらない。
// 非 Windows では使わない（SIGTERM が届く）。
func quitEventName(pid int) string {
	return `Local\looptrack-desktop-quit-` + strconv.Itoa(pid)
}
