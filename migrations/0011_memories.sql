-- プロジェクト記憶（引き継ぎ・決定・注意。docs/server/DESIGN.md §9-9）。
-- memories は今の版の 1 行（記憶 1 件に 1 行）。追記と置き換えはこの行を FOR UPDATE してから版を 1 つ進めて書き換えるので、
-- アプリ用の DB ユーザーには SELECT・INSERT・UPDATE を与える（行は消さない。消すのは deleted の印と版で表す）。
-- memory_versions は書くたびに足す版の履歴で、追記専用（SELECT・INSERT のみ。deploy/grants.sql）。
-- 範囲が project の記憶は owner_user_id を 0 にする（一意性の索引に NULL を入れると同じ名前が 2 行入るため。
-- そのため owner_user_id には外部キーを置かない）。
-- version 0 の行はまだ何も書いていない下書きの行で、deleted を立てて持つ（どの読みにも出さない）。行が無いまま FOR UPDATE すると
-- ギャップロックを取り合うので、書く前に INSERT IGNORE でこの行を作ってから握る（store.EnsureMemoryRow）。
CREATE TABLE memories (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id    BIGINT UNSIGNED NOT NULL,
  scope         VARCHAR(16)     NOT NULL COMMENT 'project / personal',
  owner_user_id BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'personal の持ち主（project は 0）',
  kind          VARCHAR(16)     NOT NULL COMMENT 'handoff / decision / caveat',
  name          VARCHAR(64)     NOT NULL COMMENT '種類と範囲の中で一意の短い名前（英小文字・数字・-）',
  version       BIGINT UNSIGNED NOT NULL COMMENT '今の版（0 は下書きの行。書くたびに 1 進める）',
  summary       VARCHAR(200)    NOT NULL DEFAULT '' COMMENT '本文の先頭の 1 行',
  content       MEDIUMTEXT      NOT NULL,
  deleted       BOOLEAN         NOT NULL DEFAULT FALSE COMMENT '消した印（行は残す）',
  created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_by    BIGINT UNSIGNED NULL COMMENT '最後に書いた利用者',
  PRIMARY KEY (id),
  UNIQUE KEY uk_memories_name (project_id, scope, owner_user_id, kind, name),
  CONSTRAINT fk_memories_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_memories_updated_by FOREIGN KEY (updated_by) REFERENCES users (id),
  CONSTRAINT chk_memories_scope CHECK (scope IN ('project', 'personal')),
  CONSTRAINT chk_memories_kind CHECK (kind IN ('handoff', 'decision', 'caveat')),
  CONSTRAINT chk_memories_version CHECK (version >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE memory_versions (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  memory_id     BIGINT UNSIGNED NOT NULL,
  version       BIGINT UNSIGNED NOT NULL,
  op            VARCHAR(16)     NOT NULL COMMENT 'create / append / replace / delete',
  summary       VARCHAR(200)    NOT NULL DEFAULT '',
  content       MEDIUMTEXT      NOT NULL COMMENT 'この版の本文の全体（delete の版は消す前の本文）',
  user_id       BIGINT UNSIGNED NULL COMMENT '書いた利用者',
  machine_label VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '書いた端末のラベル',
  session_id    VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '書いた AI のセッション（X-Looptrack-Session）',
  token_id      BIGINT UNSIGNED NULL,
  via           VARCHAR(16)     NOT NULL,
  created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uk_memory_versions_version (memory_id, version),
  CONSTRAINT fk_memory_versions_memory FOREIGN KEY (memory_id) REFERENCES memories (id),
  CONSTRAINT fk_memory_versions_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_memory_versions_op CHECK (op IN ('create', 'append', 'replace', 'delete')),
  CONSTRAINT chk_memory_versions_version CHECK (version >= 1),
  CONSTRAINT chk_memory_versions_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
