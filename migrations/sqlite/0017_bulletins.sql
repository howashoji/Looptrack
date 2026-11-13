-- 0017_bulletins.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（列の説明は MySQL 版の COMMENT）。
-- bulletins・bulletin_events・bulletin_receipts の追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。
-- bulletin_heads は通し番号を進めるたびに書き換えるのでトリガを置かない。
CREATE TABLE bulletins (
  id                        INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id                INTEGER NOT NULL,
  kind                      TEXT    NOT NULL,
  to_kind                   TEXT    NOT NULL,
  to_user_id                INTEGER NULL,
  to_session                TEXT    NOT NULL DEFAULT '',
  sender_user_id            INTEGER NOT NULL,
  sender_machine            TEXT    NOT NULL DEFAULT '',
  sender_session            TEXT    NOT NULL DEFAULT '',
  issue_id                  INTEGER NULL,
  release_issue_id          INTEGER NULL,
  release_lease             TEXT    NOT NULL DEFAULT '',
  release_lease_acquired_at DATETIME NULL,
  token_id                  INTEGER NULL,
  via                       TEXT    NOT NULL,
  created_at                DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_bulletins_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_bulletins_to_user FOREIGN KEY (to_user_id) REFERENCES users (id),
  CONSTRAINT fk_bulletins_sender FOREIGN KEY (sender_user_id) REFERENCES users (id),
  CONSTRAINT fk_bulletins_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT fk_bulletins_release_issue FOREIGN KEY (release_issue_id) REFERENCES issues (id),
  CONSTRAINT chk_bulletins_kind CHECK (kind IN ('info', 'request', 'hold')),
  CONSTRAINT chk_bulletins_to_kind CHECK (to_kind IN ('project', 'user', 'session')),
  CONSTRAINT chk_bulletins_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_bulletins_project ON bulletins (project_id, id);
CREATE INDEX k_bulletins_to_user ON bulletins (project_id, to_user_id, id);
CREATE INDEX k_bulletins_sender ON bulletins (project_id, sender_user_id, id);

CREATE TABLE bulletin_events (
  id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  bulletin_id    INTEGER NOT NULL,
  project_id     INTEGER NOT NULL,
  seq            INTEGER NOT NULL,
  action         TEXT    NOT NULL,
  version        INTEGER NOT NULL,
  body           TEXT    NOT NULL,
  expires_at     DATETIME NULL,
  reason         TEXT    NOT NULL DEFAULT '',
  actor_user_id  INTEGER NOT NULL,
  actor_machine  TEXT    NOT NULL DEFAULT '',
  actor_session  TEXT    NOT NULL DEFAULT '',
  token_id       INTEGER NULL,
  via            TEXT    NOT NULL,
  created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_bulletin_events_seq UNIQUE (project_id, seq),
  CONSTRAINT uk_bulletin_events_version UNIQUE (bulletin_id, version),
  CONSTRAINT fk_bulletin_events_bulletin FOREIGN KEY (bulletin_id) REFERENCES bulletins (id),
  CONSTRAINT fk_bulletin_events_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_bulletin_events_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_bulletin_events_action CHECK (action IN ('post', 'revise', 'withdraw')),
  CONSTRAINT chk_bulletin_events_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);

CREATE TABLE bulletin_receipts (
  bulletin_id INTEGER NOT NULL,
  user_id     INTEGER NOT NULL,
  session_id  TEXT    NOT NULL DEFAULT '',
  version     INTEGER NOT NULL,
  kind        TEXT    NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  PRIMARY KEY (bulletin_id, user_id, session_id, version, kind),
  CONSTRAINT fk_bulletin_receipts_bulletin FOREIGN KEY (bulletin_id) REFERENCES bulletins (id),
  CONSTRAINT fk_bulletin_receipts_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_bulletin_receipts_kind CHECK (kind IN ('receive', 'ack'))
);
CREATE INDEX k_bulletin_receipts_user ON bulletin_receipts (user_id, session_id);

CREATE TABLE bulletin_heads (
  project_id INTEGER NOT NULL PRIMARY KEY,
  seq        INTEGER NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_bulletin_heads_project FOREIGN KEY (project_id) REFERENCES projects (id)
);

CREATE TRIGGER trg_bulletins_no_update BEFORE UPDATE ON bulletins WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: bulletins は書き換えられません'); END;
CREATE TRIGGER trg_bulletins_no_delete BEFORE DELETE ON bulletins WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: bulletins は削除できません'); END;
CREATE TRIGGER trg_bulletin_events_no_update BEFORE UPDATE ON bulletin_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: bulletin_events は書き換えられません'); END;
CREATE TRIGGER trg_bulletin_events_no_delete BEFORE DELETE ON bulletin_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: bulletin_events は削除できません'); END;
CREATE TRIGGER trg_bulletin_receipts_no_update BEFORE UPDATE ON bulletin_receipts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: bulletin_receipts は書き換えられません'); END;
CREATE TRIGGER trg_bulletin_receipts_no_delete BEFORE DELETE ON bulletin_receipts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: bulletin_receipts は削除できません'); END;
