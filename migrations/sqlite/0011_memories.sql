-- 0011_memories.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（列の説明は MySQL 版の COMMENT）。
-- memory_versions の追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。memories は書き換えるのでトリガを置かない。
CREATE TABLE memories (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id    INTEGER NOT NULL,
  scope         TEXT    NOT NULL,
  owner_user_id INTEGER NOT NULL DEFAULT 0,
  kind          TEXT    NOT NULL,
  name          TEXT    NOT NULL,
  version       INTEGER NOT NULL,
  summary       TEXT    NOT NULL DEFAULT '',
  content       TEXT    NOT NULL,
  deleted       INTEGER NOT NULL DEFAULT 0,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  updated_by    INTEGER NULL,
  CONSTRAINT uk_memories_name UNIQUE (project_id, scope, owner_user_id, kind, name),
  CONSTRAINT fk_memories_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_memories_updated_by FOREIGN KEY (updated_by) REFERENCES users (id),
  CONSTRAINT chk_memories_scope CHECK (scope IN ('project', 'personal')),
  CONSTRAINT chk_memories_kind CHECK (kind IN ('handoff', 'decision', 'caveat')),
  CONSTRAINT chk_memories_version CHECK (version >= 0)
);

CREATE TABLE memory_versions (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  memory_id     INTEGER NOT NULL,
  version       INTEGER NOT NULL,
  op            TEXT    NOT NULL,
  summary       TEXT    NOT NULL DEFAULT '',
  content       TEXT    NOT NULL,
  user_id       INTEGER NULL,
  machine_label TEXT    NOT NULL DEFAULT '',
  session_id    TEXT    NOT NULL DEFAULT '',
  token_id      INTEGER NULL,
  via           TEXT    NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_memory_versions_version UNIQUE (memory_id, version),
  CONSTRAINT fk_memory_versions_memory FOREIGN KEY (memory_id) REFERENCES memories (id),
  CONSTRAINT fk_memory_versions_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_memory_versions_op CHECK (op IN ('create', 'append', 'replace', 'delete')),
  CONSTRAINT chk_memory_versions_version CHECK (version >= 1),
  CONSTRAINT chk_memory_versions_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);

CREATE TRIGGER trg_memory_versions_no_update BEFORE UPDATE ON memory_versions WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: memory_versions は書き換えられません'); END;
CREATE TRIGGER trg_memory_versions_no_delete BEFORE DELETE ON memory_versions WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: memory_versions は削除できません'); END;
