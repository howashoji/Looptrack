-- 0009_transcripts.sql（MySQL）の SQLite 版。表・列・NULL 可否・索引・制約は MySQL 版と 1 対 1 で書き写す（列の説明は MySQL 版の COMMENT）。
-- 追記専用はトリガで守る（MySQL はアプリ用ユーザーの権限で守る）。JSON の列は TEXT。
CREATE TABLE transcripts (
  id                INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  user_id           INTEGER NOT NULL,
  project_id        INTEGER NOT NULL,
  agent             TEXT    NOT NULL,
  vendor_session_id TEXT    NOT NULL,
  machine_label     TEXT    NOT NULL DEFAULT '',
  origin_os         TEXT    NOT NULL,
  agent_version     TEXT    NOT NULL DEFAULT '',
  created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  token_id          INTEGER NULL,
  via               TEXT    NOT NULL,
  CONSTRAINT uk_transcripts_vendor UNIQUE (user_id, agent, vendor_session_id),
  CONSTRAINT fk_transcripts_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_transcripts_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT chk_transcripts_agent CHECK (agent IN ('claude-code', 'codex')),
  CONSTRAINT chk_transcripts_origin_os CHECK (origin_os IN ('linux', 'darwin', 'windows')),
  CONSTRAINT chk_transcripts_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_transcripts_project ON transcripts (project_id);

CREATE TABLE transcript_chunks (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  transcript_id INTEGER NOT NULL,
  seq           INTEGER NOT NULL,
  file_key      TEXT    NOT NULL,
  file_kind     TEXT    NOT NULL,
  src_offset    INTEGER NOT NULL,
  src_length    INTEGER NOT NULL,
  line_count    INTEGER NOT NULL,
  sha256        TEXT    NOT NULL,
  size          INTEGER NOT NULL,
  encoding      TEXT    NOT NULL,
  nonce         TEXT    NOT NULL,
  placeholders  TEXT    NOT NULL,
  stats         TEXT    NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  token_id      INTEGER NULL,
  via           TEXT    NOT NULL,
  CONSTRAINT uk_transcript_chunks_seq UNIQUE (transcript_id, seq),
  CONSTRAINT fk_transcript_chunks_transcript FOREIGN KEY (transcript_id) REFERENCES transcripts (id),
  CONSTRAINT chk_transcript_chunks_kind CHECK (file_kind IN ('jsonl', 'json', 'text')),
  CONSTRAINT chk_transcript_chunks_encoding CHECK (encoding IN ('identity', 'gzip')),
  CONSTRAINT chk_transcript_chunks_seq CHECK (seq >= 1),
  CONSTRAINT chk_transcript_chunks_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_transcript_chunks_file ON transcript_chunks (transcript_id, file_key, id);
CREATE INDEX k_transcript_chunks_sha256 ON transcript_chunks (sha256);

CREATE TABLE transcript_purges (
  id               INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  transcript_id    INTEGER NOT NULL,
  through_chunk_id INTEGER NOT NULL,
  cause            TEXT    NOT NULL,
  at               DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  actor_user_id    INTEGER NULL,
  via              TEXT    NOT NULL,
  reason           TEXT    NOT NULL DEFAULT '',
  CONSTRAINT fk_transcript_purges_transcript FOREIGN KEY (transcript_id) REFERENCES transcripts (id),
  CONSTRAINT fk_transcript_purges_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_transcript_purges_cause CHECK (cause IN ('owner', 'retention', 'operator')),
  CONSTRAINT chk_transcript_purges_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin', 'system'))
);
CREATE INDEX k_transcript_purges_transcript ON transcript_purges (transcript_id, through_chunk_id);

CREATE TRIGGER trg_transcripts_no_update BEFORE UPDATE ON transcripts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: transcripts は書き換えられません'); END;
CREATE TRIGGER trg_transcripts_no_delete BEFORE DELETE ON transcripts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: transcripts は削除できません'); END;
CREATE TRIGGER trg_transcript_chunks_no_update BEFORE UPDATE ON transcript_chunks WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: transcript_chunks は書き換えられません'); END;
CREATE TRIGGER trg_transcript_chunks_no_delete BEFORE DELETE ON transcript_chunks WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: transcript_chunks は削除できません'); END;
CREATE TRIGGER trg_transcript_purges_no_update BEFORE UPDATE ON transcript_purges WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: transcript_purges は書き換えられません'); END;
CREATE TRIGGER trg_transcript_purges_no_delete BEFORE DELETE ON transcript_purges WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: transcript_purges は削除できません'); END;
