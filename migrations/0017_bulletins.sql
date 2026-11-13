-- AI 間連絡板（AI のセッションがほかのセッションへ動作や待機を指示する置き場。docs/server/DESIGN.md §9-9）。
-- bulletins は書き込みの変わらない部分、bulletin_events は投稿・改訂・取り下げの出来事、bulletin_receipts はセッションごとの受け取りと了解。
-- この 3 つは追記専用（SELECT・INSERT のみ。deploy/grants.sql）。いまの状態（最新の版・取り下げ済みか）は出来事から引く。
-- bulletin_heads はプロジェクトごとの通し番号の最新の値の 1 行で、出来事を書く取引の中でロックして 1 つ進める（SELECT・INSERT・UPDATE）。
-- AUTO_INCREMENT の値は確定の順と食い違うことがあり、後から確定した小さい番号を読み手が飛ばすので、通し番号には使わない。
-- 期限切れは読む時点で判定し、行は書き換えない。保持期間（message_retention_days）も読む側の条件で効かせ、行は消さない。
CREATE TABLE bulletins (
  id                        BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id                BIGINT UNSIGNED NOT NULL,
  kind                      VARCHAR(16)     NOT NULL COMMENT 'info / request / hold',
  to_kind                   VARCHAR(16)     NOT NULL COMMENT 'project / user / session',
  to_user_id                BIGINT UNSIGNED NULL COMMENT '宛先の利用者（project は NULL）',
  to_session                VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '宛先のセッション（session のときだけ）',
  sender_user_id            BIGINT UNSIGNED NOT NULL,
  sender_machine            VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '出した端末のラベル',
  sender_session            VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '出した AI のセッション（X-Looptrack-Session）',
  issue_id                  BIGINT UNSIGNED NULL COMMENT '添えたイシュー（コメントを残した先）',
  release_issue_id          BIGINT UNSIGNED NULL COMMENT '解除の条件: このイシューが Done か Canceled になる',
  release_lease             VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '解除の条件: この占有が返るか切れる（空は条件なし）',
  release_lease_acquired_at DATETIME(6)     NULL COMMENT '出した時点の占有の取った時刻（取り直されたら返ったと見る）',
  token_id                  BIGINT UNSIGNED NULL,
  via                       VARCHAR(16)     NOT NULL,
  created_at                DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  KEY k_bulletins_project (project_id, id),
  KEY k_bulletins_to_user (project_id, to_user_id, id),
  KEY k_bulletins_sender (project_id, sender_user_id, id),
  CONSTRAINT fk_bulletins_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_bulletins_to_user FOREIGN KEY (to_user_id) REFERENCES users (id),
  CONSTRAINT fk_bulletins_sender FOREIGN KEY (sender_user_id) REFERENCES users (id),
  CONSTRAINT fk_bulletins_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT fk_bulletins_release_issue FOREIGN KEY (release_issue_id) REFERENCES issues (id),
  CONSTRAINT chk_bulletins_kind CHECK (kind IN ('info', 'request', 'hold')),
  CONSTRAINT chk_bulletins_to_kind CHECK (to_kind IN ('project', 'user', 'session')),
  CONSTRAINT chk_bulletins_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE bulletin_events (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  bulletin_id    BIGINT UNSIGNED NOT NULL,
  project_id     BIGINT UNSIGNED NOT NULL,
  seq            BIGINT UNSIGNED NOT NULL COMMENT 'プロジェクトごとの通し番号（1 から）',
  action         VARCHAR(16)     NOT NULL COMMENT 'post / revise / withdraw',
  version        INT UNSIGNED    NOT NULL COMMENT '書き込みの中の出来事の番号（投稿が 1。取り下げも 1 つ進める）',
  body           TEXT            NOT NULL COMMENT '本文（取り下げは空）',
  expires_at     DATETIME(6)     NULL COMMENT 'この版の期限（無ければ NULL）',
  reason         VARCHAR(200)    NOT NULL DEFAULT '' COMMENT '取り下げの理由',
  actor_user_id  BIGINT UNSIGNED NOT NULL,
  actor_machine  VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '行った端末のラベル',
  actor_session  VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '行った AI のセッション',
  token_id       BIGINT UNSIGNED NULL,
  via            VARCHAR(16)     NOT NULL,
  created_at     DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uk_bulletin_events_seq (project_id, seq),
  UNIQUE KEY uk_bulletin_events_version (bulletin_id, version),
  CONSTRAINT fk_bulletin_events_bulletin FOREIGN KEY (bulletin_id) REFERENCES bulletins (id),
  CONSTRAINT fk_bulletin_events_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_bulletin_events_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_bulletin_events_action CHECK (action IN ('post', 'revise', 'withdraw')),
  CONSTRAINT chk_bulletin_events_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE bulletin_receipts (
  bulletin_id BIGINT UNSIGNED NOT NULL,
  user_id     BIGINT UNSIGNED NOT NULL,
  session_id  VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '受け取ったセッション（無ければ空文字）',
  version     INT UNSIGNED    NOT NULL COMMENT '受け取った・了解した出来事の番号',
  kind        VARCHAR(16)     NOT NULL COMMENT 'receive / ack',
  created_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (bulletin_id, user_id, session_id, version, kind),
  KEY k_bulletin_receipts_user (user_id, session_id),
  CONSTRAINT fk_bulletin_receipts_bulletin FOREIGN KEY (bulletin_id) REFERENCES bulletins (id),
  CONSTRAINT fk_bulletin_receipts_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_bulletin_receipts_kind CHECK (kind IN ('receive', 'ack'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE bulletin_heads (
  project_id BIGINT UNSIGNED NOT NULL,
  seq        BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '最後に振った通し番号（0 はまだ無い）',
  updated_at DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (project_id),
  CONSTRAINT fk_bulletin_heads_project FOREIGN KEY (project_id) REFERENCES projects (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
