-- プロジェクト記憶の版の本体を消せるようにする（docs/server/DESIGN.md §9-9）。
-- memory_versions は追記専用なので、アプリ用の DB ユーザーは本文を書き換えも削除もできない。そこで本文と要約（本文の先頭の 1 行）を
-- memory_bodies へ移す。版の行（誰が・いつ・どの操作で書いたか）は memory_versions に残り、本体だけを消せる。
-- memory_bodies は SELECT・INSERT・DELETE、memory_purges は SELECT・INSERT のみ（deploy/grants.sql）。
-- memory_purges は消した記録で、本体の無い版にはどれかの行が必ず対応する。
-- cause: user（書ける利用者の消去）/ operator（運用者の消去）/ retention（保持期間）/ thinning（版の数の上限）。
CREATE TABLE IF NOT EXISTS memory_bodies (
  memory_id BIGINT UNSIGNED NOT NULL,
  version   BIGINT UNSIGNED NOT NULL,
  summary   VARCHAR(200)    NOT NULL DEFAULT '',
  content   MEDIUMTEXT      NOT NULL COMMENT 'この版の本文の全体（delete の版は消す前の本文）',
  PRIMARY KEY (memory_id, version),
  CONSTRAINT fk_memory_bodies_version FOREIGN KEY (memory_id, version) REFERENCES memory_versions (memory_id, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS memory_purges (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  memory_id       BIGINT UNSIGNED NOT NULL,
  from_version    BIGINT UNSIGNED NOT NULL COMMENT '消した版のうち最も古いもの',
  through_version BIGINT UNSIGNED NOT NULL COMMENT '消した版のうち最も新しいもの',
  bodies          INT UNSIGNED    NOT NULL COMMENT '消した本体の数',
  cause           VARCHAR(16)     NOT NULL,
  at              DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  actor_user_id   BIGINT UNSIGNED NULL COMMENT '利用者の消去だけ。運用者・保持期間・上限は NULL',
  via             VARCHAR(16)     NOT NULL,
  reason          VARCHAR(255)    NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY k_memory_purges_memory (memory_id, through_version),
  CONSTRAINT fk_memory_purges_memory FOREIGN KEY (memory_id) REFERENCES memories (id),
  CONSTRAINT fk_memory_purges_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_memory_purges_cause CHECK (cause IN ('user', 'operator', 'retention', 'thinning')),
  CONSTRAINT chk_memory_purges_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin', 'system'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 移す段と列を落とす段は、memory_versions に本文の列があるときだけ流す。MySQL の DDL は暗黙に確定して巻き戻らないので、
-- どの文の後で止まっても、このファイルを流し直せば続きから通る（途中まで移した行は INSERT IGNORE が飛ばす）。
SET @memory_bodies_copy = (SELECT IF(COUNT(*) = 1,
  'INSERT IGNORE INTO memory_bodies (memory_id, version, summary, content) SELECT memory_id, version, summary, content FROM memory_versions',
  'DO 0') FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'memory_versions' AND column_name = 'content');
PREPARE memory_bodies_step FROM @memory_bodies_copy;
EXECUTE memory_bodies_step;
DEALLOCATE PREPARE memory_bodies_step;

-- ALGORITHM=INPLACE で表を作り直す。MySQL 8.0.29 以降の既定の INSTANT は行の中の値を残すので、古い本文が表のファイルに残る。
SET @memory_bodies_drop = (SELECT IF(COUNT(*) = 1,
  'ALTER TABLE memory_versions DROP COLUMN summary, DROP COLUMN content, ALGORITHM=INPLACE',
  'DO 0') FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'memory_versions' AND column_name = 'content');
PREPARE memory_bodies_step FROM @memory_bodies_drop;
EXECUTE memory_bodies_step;
DEALLOCATE PREPARE memory_bodies_step;
