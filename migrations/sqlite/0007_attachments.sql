-- 0007_attachments.sql（MySQL）の SQLite 版。追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。
CREATE TABLE attachments (
  id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id     INTEGER NOT NULL,
  issue_id       INTEGER NOT NULL,
  sha256         TEXT    NOT NULL,
  size           INTEGER NOT NULL,
  filename       TEXT    NOT NULL,
  media_type     TEXT    NOT NULL,
  created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  author_user_id INTEGER NULL,
  token_id       INTEGER NULL,
  via            TEXT    NOT NULL,
  CONSTRAINT fk_attachments_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_attachments_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT fk_attachments_author FOREIGN KEY (author_user_id) REFERENCES users (id),
  CONSTRAINT chk_attachments_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_attachments_issue ON attachments (issue_id, id);
CREATE INDEX k_attachments_project ON attachments (project_id);
CREATE INDEX k_attachments_sha256 ON attachments (sha256);

CREATE TABLE attachment_purges (
  id                    INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  sha256                TEXT    NOT NULL,
  attachment_id         INTEGER NOT NULL,
  through_attachment_id INTEGER NOT NULL,
  at                    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  actor_user_id         INTEGER NULL,
  via                   TEXT    NOT NULL,
  reason                TEXT    NOT NULL DEFAULT '',
  CONSTRAINT fk_attachment_purges_attachment FOREIGN KEY (attachment_id) REFERENCES attachments (id),
  CONSTRAINT fk_attachment_purges_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_attachment_purges_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_attachment_purges_sha256 ON attachment_purges (sha256);

CREATE TRIGGER trg_attachments_no_update BEFORE UPDATE ON attachments WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: attachments は書き換えられません'); END;
CREATE TRIGGER trg_attachments_no_delete BEFORE DELETE ON attachments WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: attachments は削除できません'); END;
CREATE TRIGGER trg_attachment_purges_no_update BEFORE UPDATE ON attachment_purges WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: attachment_purges は書き換えられません'); END;
CREATE TRIGGER trg_attachment_purges_no_delete BEFORE DELETE ON attachment_purges WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: attachment_purges は削除できません'); END;
