-- 0019_timeline_events.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（列の説明は MySQL 版の COMMENT）。
-- timeline_events・timeline_reads の追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。
-- SQLite は CHECK を付け替えられないので、投稿の種類に update を足すために timeline_posts を作り直す。
-- 子の表（timeline_post_events・timeline_post_receipts）が timeline_posts を外部キーで指すので、3 つとも新しい表へ写してから
-- 子・親の順に消し、親・子の順に名前を戻す。新しい子は timeline_posts_new を指して作り、親の名前を戻すと SQLite が指す先の名前を書き換える。
-- 表を消すとトリガと索引も消えるので、名前を戻した後に作り直す。行と id はそのまま残る。
CREATE TABLE timeline_events (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id    INTEGER NOT NULL,
  seq           INTEGER NOT NULL,
  kind          TEXT    NOT NULL,
  actor_user_id INTEGER NOT NULL,
  actor_machine TEXT    NOT NULL DEFAULT '',
  actor_session TEXT    NOT NULL DEFAULT '',
  issue_id      INTEGER NULL,
  detail        TEXT    NOT NULL,
  token_id      INTEGER NULL,
  via           TEXT    NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_timeline_events_seq UNIQUE (project_id, seq),
  CONSTRAINT fk_timeline_events_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_timeline_events_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT fk_timeline_events_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_timeline_events_kind CHECK (kind IN ('issue_create', 'issue_status', 'issue_assign', 'issue_comment',
    'lease_acquire', 'lease_release', 'lease_break', 'session_start', 'session_end', 'commit')),
  CONSTRAINT chk_timeline_events_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_timeline_events_created ON timeline_events (project_id, created_at);

CREATE TABLE timeline_reads (
  id         INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  session_id TEXT    NOT NULL DEFAULT '',
  seq        INTEGER NOT NULL,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_timeline_reads_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_timeline_reads_user FOREIGN KEY (user_id) REFERENCES users (id)
);
CREATE INDEX k_timeline_reads_session ON timeline_reads (project_id, user_id, session_id, seq);

CREATE TRIGGER trg_timeline_events_no_update BEFORE UPDATE ON timeline_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_events は書き換えられません'); END;
CREATE TRIGGER trg_timeline_events_no_delete BEFORE DELETE ON timeline_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_events は削除できません'); END;
CREATE TRIGGER trg_timeline_reads_no_update BEFORE UPDATE ON timeline_reads WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_reads は書き換えられません'); END;
CREATE TRIGGER trg_timeline_reads_no_delete BEFORE DELETE ON timeline_reads WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_reads は削除できません'); END;

CREATE TABLE timeline_posts_new (
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
  CONSTRAINT chk_timeline_posts_kind CHECK (kind IN ('update', 'info', 'request', 'hold')),
  CONSTRAINT chk_bulletins_to_kind CHECK (to_kind IN ('project', 'user', 'session')),
  CONSTRAINT chk_bulletins_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
INSERT INTO timeline_posts_new (id, project_id, kind, to_kind, to_user_id, to_session, sender_user_id, sender_machine, sender_session, issue_id,
  release_issue_id, release_lease, release_lease_acquired_at, token_id, via, created_at)
SELECT id, project_id, kind, to_kind, to_user_id, to_session, sender_user_id, sender_machine, sender_session, issue_id,
  release_issue_id, release_lease, release_lease_acquired_at, token_id, via, created_at FROM timeline_posts;

CREATE TABLE timeline_post_events_new (
  id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  post_id        INTEGER NOT NULL,
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
  CONSTRAINT uk_bulletin_events_version UNIQUE (post_id, version),
  CONSTRAINT fk_bulletin_events_bulletin FOREIGN KEY (post_id) REFERENCES timeline_posts_new (id),
  CONSTRAINT fk_bulletin_events_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_bulletin_events_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_bulletin_events_action CHECK (action IN ('post', 'revise', 'withdraw')),
  CONSTRAINT chk_bulletin_events_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
INSERT INTO timeline_post_events_new (id, post_id, project_id, seq, action, version, body, expires_at, reason, actor_user_id, actor_machine,
  actor_session, token_id, via, created_at)
SELECT id, post_id, project_id, seq, action, version, body, expires_at, reason, actor_user_id, actor_machine,
  actor_session, token_id, via, created_at FROM timeline_post_events;

CREATE TABLE timeline_post_receipts_new (
  post_id     INTEGER NOT NULL,
  user_id     INTEGER NOT NULL,
  session_id  TEXT    NOT NULL DEFAULT '',
  version     INTEGER NOT NULL,
  kind        TEXT    NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  PRIMARY KEY (post_id, user_id, session_id, version, kind),
  CONSTRAINT fk_bulletin_receipts_bulletin FOREIGN KEY (post_id) REFERENCES timeline_posts_new (id),
  CONSTRAINT fk_bulletin_receipts_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_bulletin_receipts_kind CHECK (kind IN ('receive', 'ack'))
);
INSERT INTO timeline_post_receipts_new (post_id, user_id, session_id, version, kind, created_at)
SELECT post_id, user_id, session_id, version, kind, created_at FROM timeline_post_receipts;

DROP TABLE timeline_post_receipts;
DROP TABLE timeline_post_events;
DROP TABLE timeline_posts;
ALTER TABLE timeline_posts_new RENAME TO timeline_posts;
ALTER TABLE timeline_post_events_new RENAME TO timeline_post_events;
ALTER TABLE timeline_post_receipts_new RENAME TO timeline_post_receipts;

CREATE INDEX k_timeline_posts_project ON timeline_posts (project_id, id);
CREATE INDEX k_timeline_posts_to_user ON timeline_posts (project_id, to_user_id, id);
CREATE INDEX k_timeline_posts_sender ON timeline_posts (project_id, sender_user_id, id);
CREATE INDEX k_timeline_post_receipts_user ON timeline_post_receipts (user_id, session_id);

CREATE TRIGGER trg_timeline_posts_no_update BEFORE UPDATE ON timeline_posts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_posts は書き換えられません'); END;
CREATE TRIGGER trg_timeline_posts_no_delete BEFORE DELETE ON timeline_posts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_posts は削除できません'); END;
CREATE TRIGGER trg_timeline_post_events_no_update BEFORE UPDATE ON timeline_post_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_events は書き換えられません'); END;
CREATE TRIGGER trg_timeline_post_events_no_delete BEFORE DELETE ON timeline_post_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_events は削除できません'); END;
CREATE TRIGGER trg_timeline_post_receipts_no_update BEFORE UPDATE ON timeline_post_receipts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_receipts は書き換えられません'); END;
CREATE TRIGGER trg_timeline_post_receipts_no_delete BEFORE DELETE ON timeline_post_receipts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_receipts は削除できません'); END;

INSERT INTO timeline_heads (project_id, seq, updated_at)
SELECT p.id, 0, (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') FROM projects p WHERE NOT EXISTS (SELECT 1 FROM timeline_heads h WHERE h.project_id = p.id);
