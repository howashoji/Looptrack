CREATE TABLE project_guides (
  project_id  BIGINT UNSIGNED NOT NULL,
  content     MEDIUMTEXT      NOT NULL COMMENT '運用文書（Markdown）',
  source      VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '登録元（例: docs/projects/app.md）',
  updated_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (project_id),
  CONSTRAINT fk_project_guides_project FOREIGN KEY (project_id) REFERENCES projects (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
