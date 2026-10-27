-- 会話記録の置き場（利用者ごと。docs/server/DESIGN.md §2-5）。本体は DB ではなく、添付とは別のルートの置き場に sha256 の名前で置き、
-- ここにはメタデータだけを持つ。3 つの表はどれも追記専用（アプリ用の DB ユーザーは SELECT・INSERT のみ。deploy/grants.sql）。
-- 今の連番・ファイルごとの送った長さ・使用量は transcripts の行に持たず、チャンクから数える（行を書き換えないため）。
-- 消去は本体のファイルだけを消し、transcript_purges に 1 行を足す。through_chunk_id 以下のチャンクが消去済みになり、
-- 消去の後に同じ会話を送り直したチャンク（それより大きい id）は消去済みと数えない（添付の through_attachment_id と同じ考え方）。
CREATE TABLE transcripts (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id           BIGINT UNSIGNED NOT NULL COMMENT '持ち主（送った本人だけが読める）',
  project_id        BIGINT UNSIGNED NOT NULL COMMENT '送ったときのプロジェクト（ルールの判定と一覧の絞り込み用。見える範囲は広げない）',
  agent             VARCHAR(16)     NOT NULL COMMENT 'claude-code / codex',
  vendor_session_id VARCHAR(128)    NOT NULL COMMENT 'Claude の sessionId・Codex のスレッド ID',
  machine_label     VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '送った端末のラベル',
  origin_os         VARCHAR(16)     NOT NULL COMMENT 'linux / darwin / windows',
  agent_version     VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '記録の中の版',
  created_at        DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  token_id          BIGINT UNSIGNED NULL,
  via               VARCHAR(16)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_transcripts_vendor (user_id, agent, vendor_session_id),
  KEY k_transcripts_project (project_id),
  CONSTRAINT fk_transcripts_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_transcripts_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT chk_transcripts_agent CHECK (agent IN ('claude-code', 'codex')),
  CONSTRAINT chk_transcripts_origin_os CHECK (origin_os IN ('linux', 'darwin', 'windows')),
  CONSTRAINT chk_transcripts_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE transcript_chunks (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  transcript_id BIGINT UNSIGNED NOT NULL,
  seq           BIGINT UNSIGNED NOT NULL COMMENT '記録の中の連番（1 から隙間なく。楽観ロックは (transcript_id, seq) の一意性で取る）',
  file_key      VARCHAR(255)    NOT NULL COMMENT '記録の置き場の中の相対パス（端末の名前を含まない）',
  file_kind     VARCHAR(8)      NOT NULL COMMENT 'jsonl / json / text',
  src_offset    BIGINT UNSIGNED NOT NULL COMMENT '元のファイルの中の位置（伸ばす前の論理的なバイト数）',
  src_length    BIGINT UNSIGNED NOT NULL COMMENT '元のファイルの中の長さ（伸ばす前の論理的なバイト数）',
  line_count    BIGINT UNSIGNED NOT NULL COMMENT '行の数（LINES は MySQL の予約語なので line_count）',
  sha256        CHAR(64)        NOT NULL COMMENT '置いた本体の SHA-256（16 進の小文字。gzip ならその形）。置き場のファイル名',
  size          BIGINT UNSIGNED NOT NULL COMMENT '置いた本体のバイト数',
  encoding      VARCHAR(16)     NOT NULL COMMENT 'identity / gzip',
  nonce         VARCHAR(32)     NOT NULL COMMENT 'パスの書き換えのプレースホルダの印（チャンクごと）',
  placeholders  JSON            NOT NULL COMMENT '使ったプレースホルダの種類の一覧（元の絶対パスは入れない）',
  stats         JSON            NOT NULL COMMENT 'クライアントが数えた件数だけ（値は入れない）',
  created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  token_id      BIGINT UNSIGNED NULL,
  via           VARCHAR(16)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_transcript_chunks_seq (transcript_id, seq),
  KEY k_transcript_chunks_file (transcript_id, file_key, id),
  KEY k_transcript_chunks_sha256 (sha256),
  CONSTRAINT fk_transcript_chunks_transcript FOREIGN KEY (transcript_id) REFERENCES transcripts (id),
  CONSTRAINT chk_transcript_chunks_kind CHECK (file_kind IN ('jsonl', 'json', 'text')),
  CONSTRAINT chk_transcript_chunks_encoding CHECK (encoding IN ('identity', 'gzip')),
  CONSTRAINT chk_transcript_chunks_seq CHECK (seq >= 1),
  CONSTRAINT chk_transcript_chunks_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE transcript_purges (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  transcript_id    BIGINT UNSIGNED NOT NULL,
  through_chunk_id BIGINT UNSIGNED NOT NULL COMMENT 'この消去が効くチャンクの id の上限（消去の時点でその記録の最大の id）',
  cause            VARCHAR(16)     NOT NULL COMMENT 'owner / retention / operator',
  at               DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  actor_user_id    BIGINT UNSIGNED NULL COMMENT '保持期間の切れ（retention）は NULL',
  via              VARCHAR(16)     NOT NULL COMMENT '保持期間の切れは system',
  reason           VARCHAR(255)    NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY k_transcript_purges_transcript (transcript_id, through_chunk_id),
  CONSTRAINT fk_transcript_purges_transcript FOREIGN KEY (transcript_id) REFERENCES transcripts (id),
  CONSTRAINT fk_transcript_purges_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_transcript_purges_cause CHECK (cause IN ('owner', 'retention', 'operator')),
  CONSTRAINT chk_transcript_purges_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin', 'system'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
