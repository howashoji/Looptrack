CREATE TABLE mcp_connections (
  id               CHAR(32)        NOT NULL COMMENT 'サーバが発行した Mcp-Session-Id',
  user_id          BIGINT UNSIGNED NOT NULL,
  token_id         BIGINT UNSIGNED NULL,
  client_name      VARCHAR(128)    NOT NULL DEFAULT '' COMMENT 'initialize の clientInfo.name',
  client_version   VARCHAR(64)     NOT NULL DEFAULT '',
  agent            VARCHAR(32)     NOT NULL COMMENT 'claude-code / codex / other（client_name から判定）',
  protocol_version VARCHAR(32)     NOT NULL DEFAULT '',
  project          VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '接続時の X-IM-Project',
  user_agent       VARCHAR(255)    NOT NULL DEFAULT '',
  created_at       DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_seen_at     DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  closed_at        DATETIME(6)     NULL COMMENT 'DELETE（接続の終了）を受けた時刻',
  PRIMARY KEY (id),
  KEY k_mcp_connections_user (user_id, created_at),
  KEY k_mcp_connections_seen (last_seen_at),
  CONSTRAINT fk_mcp_connections_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE agent_installs (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id        BIGINT UNSIGNED NOT NULL,
  project_id     BIGINT UNSIGNED NOT NULL,
  agent          VARCHAR(32)     NOT NULL COMMENT 'claude-code / codex / other',
  source         VARCHAR(16)     NOT NULL DEFAULT '' COMMENT 'スクリプトの置き方 link / copy / server',
  files          JSON            NOT NULL COMMENT '{配布ファイル名: SHA-256}（手元の実体のハッシュ）',
  bundle_sha256  CHAR(64)        NOT NULL COMMENT '配布ファイル全体のハッシュ（名前順の「名前 ハッシュ」行の SHA-256）',
  client_version VARCHAR(64)     NOT NULL DEFAULT '',
  host           VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '通知してきた端末のホスト名',
  workspace      VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '導入先ディレクトリの名前（パスは送らない）',
  first_at       DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  reported_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '最後の通知（手動を含む）',
  hook_at        DATETIME(6)     NULL COMMENT '最後にフックから通知された時刻（フックが動いている証拠）',
  PRIMARY KEY (id),
  UNIQUE KEY uk_agent_installs (user_id, project_id, agent),
  CONSTRAINT fk_agent_installs_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_agent_installs_project FOREIGN KEY (project_id) REFERENCES projects (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
