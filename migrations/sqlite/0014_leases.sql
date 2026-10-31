-- 0014_leases.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（列の説明は MySQL 版の COMMENT）。
-- lease_events の追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。leases は書き換えるのでトリガを置かない。
CREATE TABLE leases (
  id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id     INTEGER NOT NULL,
  name           TEXT    NOT NULL,
  holder_user_id INTEGER NULL,
  machine_label  TEXT    NOT NULL DEFAULT '',
  session_id     TEXT    NOT NULL DEFAULT '',
  purpose        TEXT    NOT NULL DEFAULT '',
  token_hash     TEXT    NOT NULL DEFAULT '',
  acquired_at    DATETIME NULL,
  expires_at     DATETIME NULL,
  updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_leases_name UNIQUE (project_id, name),
  CONSTRAINT fk_leases_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_leases_holder FOREIGN KEY (holder_user_id) REFERENCES users (id)
);

CREATE TABLE lease_events (
  id                 INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  lease_id           INTEGER NOT NULL,
  action             TEXT    NOT NULL,
  user_id            INTEGER NULL,
  machine_label      TEXT    NOT NULL DEFAULT '',
  session_id         TEXT    NOT NULL DEFAULT '',
  token_id           INTEGER NULL,
  via                TEXT    NOT NULL,
  purpose            TEXT    NOT NULL DEFAULT '',
  reason             TEXT    NOT NULL DEFAULT '',
  expires_at         DATETIME NULL,
  prev_user_id       INTEGER NULL,
  prev_machine_label TEXT    NOT NULL DEFAULT '',
  prev_session_id    TEXT    NOT NULL DEFAULT '',
  was_expired        INTEGER NOT NULL DEFAULT 0,
  created_at         DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_lease_events_lease FOREIGN KEY (lease_id) REFERENCES leases (id),
  CONSTRAINT fk_lease_events_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_lease_events_prev_user FOREIGN KEY (prev_user_id) REFERENCES users (id),
  CONSTRAINT chk_lease_events_action CHECK (action IN ('acquire', 'extend', 'release', 'break')),
  CONSTRAINT chk_lease_events_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX idx_lease_events_lease ON lease_events (lease_id, id);

CREATE TRIGGER trg_lease_events_no_update BEFORE UPDATE ON lease_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: lease_events は書き換えられません'); END;
CREATE TRIGGER trg_lease_events_no_delete BEFORE DELETE ON lease_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: lease_events は削除できません'); END;
