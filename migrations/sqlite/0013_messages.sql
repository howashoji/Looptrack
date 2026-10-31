-- 0013_messages.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（列の説明は MySQL 版の COMMENT）。
-- 追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。
CREATE TABLE messages (
  id                INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id        INTEGER NOT NULL,
  to_kind           TEXT    NOT NULL,
  to_user_id        INTEGER NULL,
  to_session        TEXT    NOT NULL DEFAULT '',
  sender_user_id    INTEGER NOT NULL,
  sender_machine    TEXT    NOT NULL DEFAULT '',
  sender_session    TEXT    NOT NULL DEFAULT '',
  issue_id          INTEGER NULL,
  body              TEXT    NOT NULL,
  token_id          INTEGER NULL,
  via               TEXT    NOT NULL,
  created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_messages_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_messages_to_user FOREIGN KEY (to_user_id) REFERENCES users (id),
  CONSTRAINT fk_messages_sender FOREIGN KEY (sender_user_id) REFERENCES users (id),
  CONSTRAINT fk_messages_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_messages_to_kind CHECK (to_kind IN ('project', 'user', 'session')),
  CONSTRAINT chk_messages_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_messages_project ON messages (project_id, id);
CREATE INDEX k_messages_to_user ON messages (project_id, to_user_id, id);
CREATE INDEX k_messages_sender ON messages (project_id, sender_user_id, id);

CREATE TABLE message_reads (
  message_id INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  session_id TEXT    NOT NULL DEFAULT '',
  read_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  PRIMARY KEY (message_id, user_id, session_id),
  CONSTRAINT fk_message_reads_message FOREIGN KEY (message_id) REFERENCES messages (id),
  CONSTRAINT fk_message_reads_user FOREIGN KEY (user_id) REFERENCES users (id)
);
CREATE INDEX k_message_reads_user ON message_reads (user_id, session_id);

CREATE TRIGGER trg_messages_no_update BEFORE UPDATE ON messages WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: messages は書き換えられません'); END;
CREATE TRIGGER trg_messages_no_delete BEFORE DELETE ON messages WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: messages は削除できません'); END;
CREATE TRIGGER trg_message_reads_no_update BEFORE UPDATE ON message_reads WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: message_reads は書き換えられません'); END;
CREATE TRIGGER trg_message_reads_no_delete BEFORE DELETE ON message_reads WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: message_reads は削除できません'); END;
