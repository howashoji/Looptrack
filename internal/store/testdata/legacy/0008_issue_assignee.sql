ALTER TABLE issues
  ADD COLUMN assignee_user_id BIGINT UNSIGNED NULL COMMENT '担当者（NULL は未設定）' AFTER parent,
  ADD KEY k_issues_project_assignee (project_id, assignee_user_id),
  ADD CONSTRAINT fk_issues_assignee FOREIGN KEY (assignee_user_id) REFERENCES users (id);

UPDATE issues i
JOIN issue_events e ON e.id = (
  SELECT MAX(x.id) FROM issue_events x WHERE x.issue_id = i.id AND (
    (x.kind = 'status' AND JSON_UNQUOTE(JSON_EXTRACT(x.detail, '$.to')) = 'In Progress') OR
    (x.kind = 'create' AND JSON_UNQUOTE(JSON_EXTRACT(x.detail, '$.status')) = 'In Progress')))
SET i.assignee_user_id = e.actor_user_id
WHERE i.status = 'In Progress' AND e.actor_user_id IS NOT NULL AND i.assignee_user_id IS NULL;
