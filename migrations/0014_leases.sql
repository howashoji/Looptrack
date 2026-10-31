-- 占有（本番への反映・公開の PR・リリースのタグのように、プロジェクトで同時に 1 本しか動かせない作業。docs/server/DESIGN.md §9-9）。
-- leases は資源ごとの今の持ち主の 1 行で、取る・延ばす・返す・外すたびに書き換える。アプリ用の DB ユーザーには SELECT・INSERT・UPDATE を与える。
-- 行は消さない。空いている資源は holder_user_id を NULL にして持つ。
-- まだ誰も取っていない資源の行も持つ（行が無いまま FOR UPDATE するとギャップロックを取り合うので、取る前に INSERT IGNORE で作ってから握る。store.EnsureLeaseRow）。
-- lease_events は取る・延ばす・返す・外すの記録で、追記専用（SELECT・INSERT のみ。deploy/grants.sql）。
CREATE TABLE leases (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id     BIGINT UNSIGNED NOT NULL,
  name           VARCHAR(64)     NOT NULL COMMENT 'プロジェクトの中で一意の資源の名前（英小文字・数字・-・.）',
  holder_user_id BIGINT UNSIGNED NULL COMMENT '取っている利用者（空いていれば NULL）',
  machine_label  VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '取った端末のラベル',
  session_id     VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '取った AI のセッション（X-Looptrack-Session）',
  purpose        VARCHAR(200)    NOT NULL DEFAULT '' COMMENT '目的',
  token_hash     CHAR(64)        NOT NULL DEFAULT '' COMMENT '取るたびに作る世代の鍵の SHA-256（鍵そのものは取った者にだけ返す）',
  acquired_at    DATETIME(6)     NULL COMMENT '取った時刻',
  expires_at     DATETIME(6)     NULL COMMENT '期限（過ぎると誰でも取れる）',
  updated_at     DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uk_leases_name (project_id, name),
  CONSTRAINT fk_leases_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_leases_holder FOREIGN KEY (holder_user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE lease_events (
  id                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  lease_id           BIGINT UNSIGNED NOT NULL,
  action             VARCHAR(16)     NOT NULL COMMENT 'acquire / extend / release / break',
  user_id            BIGINT UNSIGNED NULL COMMENT '操作した利用者',
  machine_label      VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '操作した端末のラベル',
  session_id         VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '操作した AI のセッション',
  token_id           BIGINT UNSIGNED NULL,
  via                VARCHAR(16)     NOT NULL,
  purpose            VARCHAR(200)    NOT NULL DEFAULT '' COMMENT '操作の後の目的',
  reason             VARCHAR(200)    NOT NULL DEFAULT '' COMMENT '外した理由（break だけ）',
  expires_at         DATETIME(6)     NULL COMMENT '操作の後の期限（返す・外すは NULL）',
  prev_user_id       BIGINT UNSIGNED NULL COMMENT '操作の前に取っていた利用者',
  prev_machine_label VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '操作の前の端末のラベル',
  prev_session_id    VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '操作の前のセッション',
  was_expired        BOOLEAN         NOT NULL DEFAULT FALSE COMMENT '操作の時点で前の占有の期限が切れていた（acquire なら期限切れから取った）',
  created_at         DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  KEY idx_lease_events_lease (lease_id, id),
  CONSTRAINT fk_lease_events_lease FOREIGN KEY (lease_id) REFERENCES leases (id),
  CONSTRAINT fk_lease_events_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_lease_events_prev_user FOREIGN KEY (prev_user_id) REFERENCES users (id),
  CONSTRAINT chk_lease_events_action CHECK (action IN ('acquire', 'extend', 'release', 'break')),
  CONSTRAINT chk_lease_events_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
