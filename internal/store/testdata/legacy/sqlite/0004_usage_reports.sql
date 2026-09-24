CREATE TABLE usage_reports (
  id                     INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id             INTEGER NOT NULL,
  name                   TEXT    NOT NULL,
  period_from            DATETIME NULL,
  period_to              DATETIME NOT NULL,
  data_end               DATETIME NOT NULL,
  excluded_conversations TEXT    NULL,
  total_tokens           INTEGER NULL,
  note                   TEXT    NULL,
  request_id             INTEGER NULL,
  created_by             INTEGER NOT NULL,
  token_id               INTEGER NULL,
  via                    TEXT    NOT NULL,
  created_at             DATETIME NOT NULL,
  recorded_at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_usage_reports_name UNIQUE (project_id, name),
  CONSTRAINT fk_usage_reports_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_reports_user FOREIGN KEY (created_by) REFERENCES users (id),
  CONSTRAINT fk_usage_reports_request FOREIGN KEY (request_id) REFERENCES usage_report_requests (id),
  CONSTRAINT chk_usage_reports_period CHECK (period_from IS NULL OR period_from < period_to)
);
CREATE INDEX k_usage_reports_data_end ON usage_reports (project_id, data_end);
CREATE TRIGGER trg_usage_reports_no_update BEFORE UPDATE ON usage_reports WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_reports は書き換えられません'); END;
CREATE TRIGGER trg_usage_reports_no_delete BEFORE DELETE ON usage_reports WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_reports は削除できません'); END;
