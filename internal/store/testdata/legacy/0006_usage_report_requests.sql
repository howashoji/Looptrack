CREATE TABLE usage_report_requests (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id   BIGINT UNSIGNED NOT NULL,
  since_last   TINYINT(1)      NOT NULL DEFAULT 0 COMMENT '1 は前回のレポート（台帳の最新のデータ終端）以降。起点は作成するときに決まる',
  period_from  DATETIME(6)     NULL COMMENT '対象期間の始まり（含む・UTC）。since_last のときは NULL',
  period_to    DATETIME(6)     NULL COMMENT '対象期間の終わり（含まない・UTC）。NULL は作成するときの今まで',
  target       VARCHAR(500)    NOT NULL DEFAULT '' COMMENT '対象（空はプロジェクト全体。例: ラベル api のイシュー）',
  note         TEXT            NULL COMMENT 'メモ（AI への指示・提出先など）',
  requested_by BIGINT UNSIGNED NOT NULL,
  token_id     BIGINT UNSIGNED NULL,
  via          VARCHAR(16)     NOT NULL COMMENT 'web / api / cli / mcp',
  created_at   DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  KEY k_usage_report_requests_project (project_id, id),
  CONSTRAINT fk_usage_report_requests_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_report_requests_user FOREIGN KEY (requested_by) REFERENCES users (id),
  CONSTRAINT chk_usage_report_requests_period CHECK (
    (since_last = 1 AND period_from IS NULL) OR (since_last = 0 AND period_from IS NOT NULL)),
  CONSTRAINT chk_usage_report_requests_order CHECK (period_from IS NULL OR period_to IS NULL OR period_from < period_to)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

ALTER TABLE usage_reports
  ADD UNIQUE KEY uk_usage_reports_request (request_id),
  ADD CONSTRAINT fk_usage_reports_request FOREIGN KEY (request_id) REFERENCES usage_report_requests (id);
