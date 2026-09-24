CREATE TABLE usage_report_requests (
  id           INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id   INTEGER NOT NULL,
  since_last   INTEGER NOT NULL DEFAULT 0,
  period_from  DATETIME NULL,
  period_to    DATETIME NULL,
  target       TEXT    NOT NULL DEFAULT '',
  note         TEXT    NULL,
  requested_by INTEGER NOT NULL,
  token_id     INTEGER NULL,
  via          TEXT    NOT NULL,
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_usage_report_requests_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_report_requests_user FOREIGN KEY (requested_by) REFERENCES users (id),
  CONSTRAINT chk_usage_report_requests_period CHECK (
    (since_last = 1 AND period_from IS NULL) OR (since_last = 0 AND period_from IS NOT NULL)),
  CONSTRAINT chk_usage_report_requests_order CHECK (period_from IS NULL OR period_to IS NULL OR period_from < period_to)
);
CREATE INDEX k_usage_report_requests_project ON usage_report_requests (project_id, id);
CREATE TRIGGER trg_usage_report_requests_no_update BEFORE UPDATE ON usage_report_requests WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_report_requests は書き換えられません'); END;
CREATE TRIGGER trg_usage_report_requests_no_delete BEFORE DELETE ON usage_report_requests WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_report_requests は削除できません'); END;

CREATE UNIQUE INDEX uk_usage_reports_request ON usage_reports (request_id);
