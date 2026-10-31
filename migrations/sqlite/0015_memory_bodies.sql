-- 0015_memory_bodies.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（説明は MySQL 版）。
-- memory_purges の追記専用はトリガで守る。memory_bodies は本体を消すのでトリガを置かない。
CREATE TABLE memory_bodies (
  memory_id INTEGER NOT NULL,
  version   INTEGER NOT NULL,
  summary   TEXT    NOT NULL DEFAULT '',
  content   TEXT    NOT NULL,
  PRIMARY KEY (memory_id, version),
  CONSTRAINT fk_memory_bodies_version FOREIGN KEY (memory_id, version) REFERENCES memory_versions (memory_id, version)
);

CREATE TABLE memory_purges (
  id              INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  memory_id       INTEGER NOT NULL,
  from_version    INTEGER NOT NULL,
  through_version INTEGER NOT NULL,
  bodies          INTEGER NOT NULL,
  cause           TEXT    NOT NULL,
  at              DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  actor_user_id   INTEGER NULL,
  via             TEXT    NOT NULL,
  reason          TEXT    NOT NULL DEFAULT '',
  CONSTRAINT fk_memory_purges_memory FOREIGN KEY (memory_id) REFERENCES memories (id),
  CONSTRAINT fk_memory_purges_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_memory_purges_cause CHECK (cause IN ('user', 'operator', 'retention', 'thinning')),
  CONSTRAINT chk_memory_purges_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin', 'system'))
);
CREATE INDEX k_memory_purges_memory ON memory_purges (memory_id, through_version);

CREATE TRIGGER trg_memory_purges_no_update BEFORE UPDATE ON memory_purges WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: memory_purges は書き換えられません'); END;
CREATE TRIGGER trg_memory_purges_no_delete BEFORE DELETE ON memory_purges WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: memory_purges は削除できません'); END;

INSERT INTO memory_bodies (memory_id, version, summary, content)
SELECT memory_id, version, summary, content FROM memory_versions;

ALTER TABLE memory_versions DROP COLUMN summary;
ALTER TABLE memory_versions DROP COLUMN content;
