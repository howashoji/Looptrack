package store

import (
	"context"
	"testing"
	"time"
)

// TestTimelinePostSeqInTransaction は、通し番号が出来事を書く取引の中で進み、取り消した取引の番号は戻ること
// （後から確定した番号を飛ばす穴を作らない）と、行を作る処理を何度呼んでも番号が戻らないことを確かめる。
func TestTimelinePostSeqInTransaction(t *testing.T) {
	db := usageTestDB(t)
	ctx := context.Background()
	pid, err := CreateProject(ctx, db, Project{Slug: "bb", Prefix: "BB", Width: 4, Name: "bb"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	if seq, err := TimelinePostHeadSeq(ctx, db, pid); err != nil || seq != 0 {
		t.Fatalf("行の無い通し番号: %d %v", seq, err)
	}
	next := func(commit bool) int64 {
		t.Helper()
		if err := EnsureTimelinePostHead(ctx, db, pid, now); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		seq, err := LockTimelineHead(ctx, tx, pid, 1, now)
		if err != nil {
			tx.Rollback() //nolint:errcheck
			t.Fatal(err)
		}
		seq++
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		} else if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	if got := []int64{next(true), next(false), next(true), next(true)}; got[0] != 1 || got[1] != 2 || got[2] != 2 || got[3] != 3 {
		t.Fatalf("通し番号: %v（1・2（取り消し）・2・3 のはず）", got)
	}
	if seq, err := TimelinePostHeadSeq(ctx, db, pid); err != nil || seq != 3 {
		t.Errorf("最後の通し番号: %d %v", seq, err)
	}
}

// TestTimelinePostReadableAndReceipts は、読める範囲（プロジェクト宛て・自分宛て・自分が出したもの）と、受け取りの記録が
// 2 度目を足さないことを確かめる。宛先でも送り手でもない利用者には利用者宛ての書き込みが見えない（対照: 宛先には見える）。
func TestTimelinePostReadableAndReceipts(t *testing.T) {
	db := usageTestDB(t)
	ctx := context.Background()
	pid, err := CreateProject(ctx, db, Project{Slug: "bb", Prefix: "BB", Width: 4, Name: "bb"})
	if err != nil {
		t.Fatal(err)
	}
	user := func(login string) int64 {
		id, err := CreateUser(ctx, db, login, login, "x", "member")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	sender, to, other := user("sender"), user("to"), user("other")
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	insert := func(toKind string, toUser int64) int64 {
		t.Helper()
		id, err := InsertTimelinePost(ctx, db, TimelinePost{ProjectID: pid, Kind: TimelinePostHold, ToKind: toKind, ToUserID: toUser, SenderUserID: sender, Via: "cli", CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		if err := EnsureTimelinePostHead(ctx, db, pid, now); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck
		seq, err := LockTimelineHead(ctx, tx, pid, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		if err := InsertTimelinePostEvent(ctx, tx, TimelinePostEvent{TimelinePostID: id, ProjectID: pid, Seq: seq, Action: TimelinePostActionPost, Version: 1, Body: "待機",
			ExpiresAt: now.Add(time.Hour), ActorUserID: sender, Via: "cli", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return id
	}
	all, toOnly := insert(MessageToProject, 0), insert(MessageToUser, to)
	count := func(userID int64) int {
		bs, err := ReadableTimelinePosts(ctx, db, pid, userID, time.Time{}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(bs)
	}
	if n := count(other); n != 1 {
		t.Errorf("宛先でも送り手でもない利用者の件数: %d（プロジェクト宛ての 1 件のはず）", n)
	}
	if n := count(to); n != 2 {
		t.Errorf("対照: 宛先の件数: %d", n)
	}
	if n := count(sender); n != 2 {
		t.Errorf("対照: 送り手の件数: %d", n)
	}
	evs, err := TimelinePostEventsAfter(ctx, db, pid, other, time.Time{}, time.Time{}, 0, 10)
	if err != nil || len(evs) != 1 || evs[0].TimelinePostID != all {
		t.Errorf("宛先でない利用者の出来事: %+v %v", evs, err)
	}
	if evs, err := TimelinePostEventsAfter(ctx, db, pid, to, time.Time{}, time.Time{}, 1, 10); err != nil || len(evs) != 1 || evs[0].TimelinePostID != toOnly || evs[0].Seq != 2 {
		t.Errorf("番号 1 より後の出来事: %+v %v", evs, err)
	}
	rs := []TimelinePostReceipt{{TimelinePostID: toOnly, UserID: to, SessionID: "s", Version: 1, Kind: TimelinePostReceive, CreatedAt: now},
		{TimelinePostID: toOnly, UserID: to, SessionID: "s", Version: 1, Kind: TimelinePostAck, CreatedAt: now}}
	if n, err := InsertTimelinePostReceipts(ctx, db, rs); err != nil || n != 2 {
		t.Fatalf("受け取りの記録: %d %v", n, err)
	}
	if n, err := InsertTimelinePostReceipts(ctx, db, rs); err != nil || n != 0 {
		t.Errorf("2 度目の記録: %d %v", n, err)
	}
	marks, err := TimelinePostMarksOf(ctx, db, pid, to, "s")
	if err != nil || marks[toOnly] != (TimelinePostMarks{Received: 1, Acked: 1}) || len(marks) != 1 {
		t.Errorf("受け取りの最大: %+v %v", marks, err)
	}
	if marks, err := TimelinePostMarksOf(ctx, db, pid, to, "other-session"); err != nil || len(marks) != 0 {
		t.Errorf("別のセッションの受け取り: %+v %v", marks, err)
	}
}
