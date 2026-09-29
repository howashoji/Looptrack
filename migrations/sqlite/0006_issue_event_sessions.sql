-- 0006_issue_event_sessions.sql（MySQL）の SQLite 版。追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。
CREATE TABLE issue_event_sessions (
  event_id    INTEGER NOT NULL PRIMARY KEY,
  user_id     INTEGER NOT NULL,
  session_id  TEXT    NOT NULL,
  snapshot_id INTEGER NOT NULL,
  linked_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_issue_event_sessions_event FOREIGN KEY (event_id) REFERENCES issue_events (id),
  CONSTRAINT fk_issue_event_sessions_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_issue_event_sessions_snapshot FOREIGN KEY (snapshot_id) REFERENCES usage_snapshots (id)
);
CREATE INDEX k_issue_event_sessions_snapshot ON issue_event_sessions (snapshot_id);
CREATE TRIGGER trg_issue_event_sessions_no_update BEFORE UPDATE ON issue_event_sessions WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issue_event_sessions は書き換えられません'); END;
CREATE TRIGGER trg_issue_event_sessions_no_delete BEFORE DELETE ON issue_event_sessions WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issue_event_sessions は削除できません'); END;
