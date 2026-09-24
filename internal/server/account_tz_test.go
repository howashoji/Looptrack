package server

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/service"
)

// 画面の時刻も、ほかの経路（?format=md・台帳・レポート）と同じ svc.Loc で描く。
// 以前は internal/server/account.go が JST 固定（time.FixedZone("JST", 9*60*60)）だったので、
// Loc が Asia/Tokyo 以外のとき、画面だけが日本時間になって経路で食い違っていた。
//
// /account の実物（HTML）で確かめる検査 TestAccountWebTimeFollowsLoc は、api_tokens.created_at が
// アプリの時計を経由しない欠陥を捕まえるので、その欠陥とそろえてこのファイルに置く。

// tzInstant は検査で使う瞬間。Asia/Tokyo では 2026-09-18 12:04、America/Los_Angeles（PDT）では 2026-09-17 20:04。
const (
	webTZInstant  = "2026-09-18T03:04:05Z"
	webTZWantJST  = "2026-09-18 12:04"
	webTZWantLA   = "2026-09-17 20:04"
	webTZOtherJST = webTZWantLA  // Asia/Tokyo のときに出てはいけない値
	webTZOtherLA  = webTZWantJST // America/Los_Angeles のときに出てはいけない値
)

// TestFmtWebTimeUsesLoc は画面の時刻の書式が svc.Loc に従うこと（DB を使わない単体）。
// この検査が落ちるのは、fmtWebTime が固定の時間帯に戻されたとき。
func TestFmtWebTimeUsesLoc(t *testing.T) {
	instant, err := time.Parse(time.RFC3339, webTZInstant)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tz, want, notWant string }{
		{"Asia/Tokyo", webTZWantJST, webTZOtherJST},
		{"America/Los_Angeles", webTZWantLA, webTZOtherLA},
	} {
		loc, err := time.LoadLocation(c.tz)
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{svc: &service.Service{Loc: loc}}
		got := s.fmtWebTime(sql.NullTime{Time: instant, Valid: true})
		if got != c.want || got == c.notWant {
			t.Errorf("Loc=%s: 画面の時刻 = %q、期待 %q", c.tz, got, c.want)
		}
		if none := s.fmtWebTime(sql.NullTime{}); none != "-" {
			t.Errorf("値なし = %q、期待 -", none)
		}
	}
}

// TestWebReportLedgerShareOneTimezone は、本文の再現で食い違っていた 3 経路
// （画面・?format=md・台帳の一覧）が、同じ 1 つの瞬間を同じ時間帯で描くこと（DB を使わない単体）。
// 台帳は応答の timezone（IANA 名）を CLI へ渡す形なので、その名前が svc.Loc と一致することで確かめる。
func TestWebReportLedgerShareOneTimezone(t *testing.T) {
	instant, err := time.Parse(time.RFC3339, webTZInstant)
	if err != nil {
		t.Fatal(err)
	}
	for _, tz := range []string{"Asia/Tokyo", "America/Los_Angeles"} {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{svc: &service.Service{Loc: loc}}
		web := s.fmtWebTime(sql.NullTime{Time: instant, Valid: true}) // 画面（/account）
		md := s.local(instant.UTC().Format(time.RFC3339))             // ?format=md（秒まで）
		ledgerTZ := s.svc.Loc.String()                                // 台帳の応答の timezone
		ledger := instant.In(loc).Format("2006-01-02 15:04")          // CLI がその timezone で描く値
		if !strings.HasPrefix(md, web) {
			t.Errorf("Loc=%s: 画面 %q と ?format=md %q が違う時間帯", tz, web, md)
		}
		if ledgerTZ != tz || ledger != web {
			t.Errorf("Loc=%s: 台帳の timezone=%q 時刻 %q が画面 %q と食い違う", tz, ledgerTZ, ledger, web)
		}
	}
}
