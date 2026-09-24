package store

import "context"

// FirstActiveAdmin は無効化されていない管理者のうち ID が最小の利用者を返す（ローカルモードの利用者）。
// いなければ ErrNotFound。
func FirstActiveAdmin(ctx context.Context, q execQuerier) (User, error) {
	return scanUser(q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE role = 'admin' AND disabled_at IS NULL ORDER BY id LIMIT 1"))
}

// ProjectsWithoutMember は、利用者の project_members の行が無いプロジェクトの ID を返す（ローカルモードで
// ローカルの利用者を全プロジェクトの admin にするため）。
func ProjectsWithoutMember(ctx context.Context, q execQuerier, userID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id FROM projects p
WHERE NOT EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = ?) ORDER BY p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AddMemberIfAbsent は利用者をプロジェクトに参加させる。すでに行があれば何もせず false を返す（役割を上書きしない）。
// 同時の要求で先に行ができていたときも、一意制約の違反として false を返す。
func AddMemberIfAbsent(ctx context.Context, q execQuerier, projectID, userID int64, role string) (bool, error) {
	_, err := q.ExecContext(ctx, "INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, ?)", projectID, userID, role)
	if err != nil && IsDuplicateKey(err) {
		return false, nil
	}
	return err == nil, err
}
