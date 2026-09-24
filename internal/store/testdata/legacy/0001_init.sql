CREATE TABLE projects (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  slug        VARCHAR(64)     NOT NULL,
  prefix      VARCHAR(32)     NOT NULL,
  width       TINYINT UNSIGNED NOT NULL,
  name        VARCHAR(255)    NOT NULL,
  description VARCHAR(1024)   NOT NULL DEFAULT '',
  sort_order  INT             NOT NULL DEFAULT 100,
  counter     INT UNSIGNED    NOT NULL DEFAULT 0 COMMENT '最後に採番した番号（旧 counter ファイル）',
  rules       JSON            NULL COMMENT 'プロジェクト別ルール（DESIGN.md §5）',
  created_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uk_projects_slug (slug),
  UNIQUE KEY uk_projects_prefix (prefix)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE issues (
  id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id          BIGINT UNSIGNED NOT NULL,
  number              INT UNSIGNED    NOT NULL,
  display_id          VARCHAR(64)     NOT NULL,
  file_name           VARBINARY(1024) NOT NULL COMMENT '元のファイル名（NFD を含むためバイト列のまま）',
  front_keys          JSON            NOT NULL COMMENT 'frontmatter に出現したキーの順序',
  title               VARCHAR(1024)   NOT NULL DEFAULT '',
  type                VARCHAR(32)     NOT NULL DEFAULT '',
  status              VARCHAR(32)     NOT NULL DEFAULT '',
  priority            VARCHAR(8)      NOT NULL DEFAULT '',
  parent              VARCHAR(64)     NOT NULL DEFAULT '',
  created             CHAR(16)        NOT NULL DEFAULT '' COMMENT 'YYYY-MM-DD HH:MM',
  updated             CHAR(16)        NOT NULL DEFAULT '' COMMENT 'YYYY-MM-DD HH:MM',
  body_main           MEDIUMTEXT      NOT NULL,
  gap_nl              TINYINT UNSIGNED NOT NULL DEFAULT 2,
  has_comment_section BOOLEAN         NOT NULL DEFAULT TRUE,
  preamble            MEDIUMTEXT      NULL,
  trail_nl            TINYINT UNSIGNED NOT NULL DEFAULT 1,
  version             INT UNSIGNED    NOT NULL DEFAULT 1 COMMENT '楽観ロック',
  changed_at          DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uk_issues_display_id (display_id),
  UNIQUE KEY uk_issues_project_number (project_id, number),
  KEY k_issues_project_status (project_id, status),
  KEY k_issues_project_type (project_id, type),
  CONSTRAINT fk_issues_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT chk_issues_type CHECK (type IN ('', 'requirement', 'design', 'task', 'bug', 'test', 'epic')),
  CONSTRAINT chk_issues_status CHECK (status IN ('', 'Backlog', 'Todo', 'In Progress', 'In Review', 'Done', 'Canceled')),
  CONSTRAINT chk_issues_priority CHECK (priority IN ('', 'P0', 'P1', 'P2', 'P3'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE issue_values (
  issue_id BIGINT UNSIGNED NOT NULL,
  field    VARCHAR(16)     NOT NULL COMMENT 'labels / blocked_by / traces / refs',
  pos      SMALLINT UNSIGNED NOT NULL,
  value    VARCHAR(1024)   NOT NULL COMMENT '空白を含む値を分割しない',
  PRIMARY KEY (issue_id, field, pos),
  KEY k_issue_values_lookup (field, value(191)),
  CONSTRAINT fk_issue_values_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_issue_values_field CHECK (field IN ('labels', 'blocked_by', 'traces', 'refs'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE issue_extra (
  issue_id BIGINT UNSIGNED NOT NULL,
  `key`    VARCHAR(64)     NOT NULL COMMENT 'origin など未知の frontmatter キー（位置は issues.front_keys）',
  is_list  BOOLEAN         NOT NULL DEFAULT FALSE,
  value    MEDIUMTEXT      NOT NULL COMMENT 'is_list のときは JSON 配列',
  PRIMARY KEY (issue_id, `key`),
  CONSTRAINT fk_issue_extra_issue FOREIGN KEY (issue_id) REFERENCES issues (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE users (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  login         VARCHAR(64)     NOT NULL,
  display_name  VARCHAR(255)    NOT NULL DEFAULT '',
  password_hash VARCHAR(255)    NOT NULL COMMENT 'argon2id（PHC 文字列）',
  totp_secret   VARBINARY(255)  NULL COMMENT '暗号化した TOTP シークレット',
  totp_enabled  BOOLEAN         NOT NULL DEFAULT FALSE,
  role          VARCHAR(16)     NOT NULL DEFAULT 'member',
  created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_login_at DATETIME(6)     NULL,
  disabled_at   DATETIME(6)     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_users_login (login),
  CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE project_members (
  project_id BIGINT UNSIGNED NOT NULL,
  user_id    BIGINT UNSIGNED NOT NULL,
  role       VARCHAR(16)     NOT NULL DEFAULT 'editor',
  PRIMARY KEY (project_id, user_id),
  KEY k_project_members_user (user_id),
  CONSTRAINT fk_project_members_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_project_members_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_project_members_role CHECK (role IN ('viewer', 'editor', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE api_tokens (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id         BIGINT UNSIGNED NOT NULL,
  kind            VARCHAR(16)     NOT NULL DEFAULT 'pat' COMMENT 'pat / oauth',
  name            VARCHAR(255)    NOT NULL DEFAULT '',
  token_prefix    CHAR(12)        NOT NULL COMMENT '表示・識別用の先頭部分',
  token_hash      BINARY(32)      NOT NULL COMMENT 'SHA-256',
  scopes          JSON            NULL,
  oauth_client_id VARCHAR(64)     NULL,
  created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  expires_at      DATETIME(6)     NULL,
  last_used_at    DATETIME(6)     NULL,
  revoked_at      DATETIME(6)     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_api_tokens_hash (token_hash),
  KEY k_api_tokens_user (user_id),
  CONSTRAINT fk_api_tokens_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_api_tokens_kind CHECK (kind IN ('pat', 'oauth'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE comments (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  issue_id       BIGINT UNSIGNED NOT NULL,
  seq            INT UNSIGNED    NOT NULL COMMENT '並び順（同一分の連続があるため時刻ではなく連番）',
  ts             CHAR(16)        NOT NULL COMMENT '見出しの YYYY-MM-DD HH:MM',
  content        MEDIUMTEXT      NOT NULL,
  created_at     DATETIME(6)     NULL COMMENT '実時刻（取り込み分は NULL）',
  author_user_id BIGINT UNSIGNED NULL,
  token_id       BIGINT UNSIGNED NULL,
  via            VARCHAR(16)     NOT NULL DEFAULT 'import',
  PRIMARY KEY (id),
  UNIQUE KEY uk_comments_issue_seq (issue_id, seq),
  CONSTRAINT fk_comments_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT fk_comments_author FOREIGN KEY (author_user_id) REFERENCES users (id),
  CONSTRAINT chk_comments_via CHECK (via IN ('import', 'cli', 'web', 'mcp', 'api'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE issue_events (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id    BIGINT UNSIGNED NOT NULL,
  issue_id      BIGINT UNSIGNED NULL,
  at            DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  kind          VARCHAR(32)     NOT NULL COMMENT 'import / create / comment / status / update / rule_override …',
  via           VARCHAR(16)     NOT NULL,
  actor_user_id BIGINT UNSIGNED NULL,
  token_id      BIGINT UNSIGNED NULL,
  session_id    VARCHAR(128)    NULL COMMENT 'Claude Code のセッション ID 等（鮮度ガード用）',
  detail        JSON            NULL,
  PRIMARY KEY (id),
  KEY k_issue_events_issue_at (issue_id, at),
  KEY k_issue_events_project_at (project_id, at),
  CONSTRAINT fk_issue_events_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_issue_events_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_issue_events_via CHECK (via IN ('import', 'cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE web_sessions (
  id_hash      BINARY(32)      NOT NULL COMMENT 'Cookie 値の SHA-256',
  user_id      BIGINT UNSIGNED NOT NULL,
  csrf_token   CHAR(64)        NOT NULL,
  mfa_passed   BOOLEAN         NOT NULL DEFAULT FALSE,
  created_at   DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  expires_at   DATETIME(6)     NOT NULL,
  last_seen_at DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  ip           VARCHAR(45)     NOT NULL DEFAULT '',
  user_agent   VARCHAR(255)    NOT NULL DEFAULT '',
  PRIMARY KEY (id_hash),
  KEY k_web_sessions_user (user_id),
  KEY k_web_sessions_expires (expires_at),
  CONSTRAINT fk_web_sessions_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE login_attempts (
  id      BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  login   VARCHAR(64)     NOT NULL,
  ip      VARCHAR(45)     NOT NULL,
  at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  success BOOLEAN         NOT NULL,
  stage   VARCHAR(16)     NOT NULL DEFAULT 'password' COMMENT 'password / totp',
  PRIMARY KEY (id),
  KEY k_login_attempts_login_at (login, at),
  KEY k_login_attempts_ip_at (ip, at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE oauth_clients (
  client_id     VARCHAR(64)  NOT NULL,
  client_name   VARCHAR(255) NOT NULL DEFAULT '',
  redirect_uris JSON         NOT NULL,
  created_at    DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (client_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE oauth_codes (
  code_hash      BINARY(32)      NOT NULL,
  client_id      VARCHAR(64)     NOT NULL,
  user_id        BIGINT UNSIGNED NOT NULL,
  redirect_uri   VARCHAR(2048)   NOT NULL,
  code_challenge VARCHAR(128)    NOT NULL,
  scope          VARCHAR(255)    NOT NULL DEFAULT '',
  resource       VARCHAR(2048)   NOT NULL DEFAULT '',
  expires_at     DATETIME(6)     NOT NULL,
  used_at        DATETIME(6)     NULL,
  PRIMARY KEY (code_hash),
  CONSTRAINT fk_oauth_codes_client FOREIGN KEY (client_id) REFERENCES oauth_clients (client_id),
  CONSTRAINT fk_oauth_codes_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
