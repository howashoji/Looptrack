ALTER TABLE issues ADD COLUMN assignee_user_id INTEGER NULL REFERENCES users (id);
CREATE INDEX k_issues_project_assignee ON issues (project_id, assignee_user_id);

UPDATE issues SET assignee_user_id = (
  SELECT e.actor_user_id FROM issue_events e WHERE e.id = (
    SELECT MAX(x.id) FROM issue_events x WHERE x.issue_id = issues.id AND (
      (x.kind = 'status' AND json_extract(x.detail, '$.to') = 'In Progress') OR
      (x.kind = 'create' AND json_extract(x.detail, '$.status') = 'In Progress'))))
WHERE status = 'In Progress' AND assignee_user_id IS NULL;
