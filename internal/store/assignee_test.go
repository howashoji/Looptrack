package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/migrations"
)

// 担当者の印（参加者でない・viewer・無効化）と、担当にできる人の判定。
// （統合前は 0008 のマイグレーションが In Progress のイシューの担当を補っていた。1.0.0 で 0001 に統合したため、担当は SetAssignee で入れる）
func TestAssigneeInactiveMarks(t *testing.T) {
	db, _, _ := testDB(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO users (id, login, password_hash) VALUES (1, 'alice', 'x'), (2, 'bob', 'x')")
	pid, err := CreateProject(ctx, db, Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "req"})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(n int, status string, assignee int64) int64 {
		doc := &mdformat.Document{Front: []mdformat.Field{{Key: "id", Value: fmt.Sprintf("REQ-%04d", n)}, {Key: "status", Value: status}}, GapNL: 2, TrailNL: 1}
		id, err := InsertDocument(ctx, db, pid, n, "x.md", doc, "cli")
		if err != nil {
			t.Fatal(err)
		}
		if assignee != 0 {
			if err := SetAssignee(ctx, db, id, assignee); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	a := insert(1, "In Progress", 2)
	b := insert(2, "In Progress", 1)
	c := insert(3, "In Progress", 0)
	d := insert(4, "Todo", 0)

	for _, tc := range []struct {
		id   int64
		want string
	}{{a, "bob"}, {b, "alice"}, {c, ""}, {d, ""}} {
		got, err := IssueAssignee(ctx, db, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Login != tc.want || got.Inactive != (tc.want != "") { // 補った担当は参加者でないので印が付く
			t.Errorf("issue %d: %+v, want %q", tc.id, got, tc.want)
		}
	}
	// 参加者（editor）になれば印は消える。viewer・無効化は印
	mustExec(t, db, fmt.Sprintf("INSERT INTO project_members (project_id, user_id, role) VALUES (%d, 2, 'editor'), (%d, 1, 'viewer')", pid, pid))
	if got, _ := IssueAssignee(ctx, db, a); got.Inactive {
		t.Errorf("editor の担当に印: %+v", got)
	}
	if got, _ := IssueAssignee(ctx, db, b); !got.Inactive {
		t.Errorf("viewer の担当に印が無い: %+v", got)
	}
	// 利用者の役割が admin でも viewer で参加しているプロジェクトでは書けないので印が付く
	// （以前は管理者は参加の有無にかかわらず印なしだった）
	mustExec(t, db, "UPDATE users SET role = 'admin' WHERE id = 1")
	if got, _ := IssueAssignee(ctx, db, b); !got.Inactive {
		t.Errorf("viewer で参加している管理者の担当に印が無い: %+v", got)
	}
	mustExec(t, db, "UPDATE users SET disabled_at = NOW() WHERE id = 1")
	if got, _ := IssueAssignee(ctx, db, b); !got.Inactive {
		t.Errorf("無効化した担当に印が無い: %+v", got)
	}
	// 担当にできる人: 無効化されていない editor / admin の参加者（利用者 admin でも viewer なら除く。
	// 以前は利用者 admin を viewer でも含めていた）
	mustExec(t, db, "UPDATE users SET disabled_at = NULL WHERE id = 1")
	members, err := AssignableMembers(ctx, db, pid)
	if err != nil || len(members) != 1 || members[0].Login != "bob" {
		t.Errorf("担当にできる人: %+v %v", members, err)
	}
}
