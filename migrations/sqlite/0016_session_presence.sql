-- 0016_session_presence.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（列の説明は MySQL 版の COMMENT）。
CREATE TABLE session_presence (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id    INTEGER NOT NULL,
  user_id       INTEGER NOT NULL,
  session_id    TEXT    NOT NULL,
  agent         TEXT    NOT NULL DEFAULT '',
  machine_label TEXT    NOT NULL DEFAULT '',
  branch        TEXT    NOT NULL DEFAULT '',
  head_sha      TEXT    NOT NULL DEFAULT '',
  directory     TEXT    NOT NULL DEFAULT '',
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  last_seen_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_session_presence UNIQUE (project_id, user_id, session_id),
  CONSTRAINT fk_session_presence_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_session_presence_user FOREIGN KEY (user_id) REFERENCES users (id)
);
CREATE INDEX idx_session_presence_seen ON session_presence (project_id, last_seen_at);
CREATE INDEX idx_session_presence_purge ON session_presence (last_seen_at);
