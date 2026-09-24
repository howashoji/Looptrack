-- 0001: 1.0.0 のスキーマ（MySQL 8.0 以降）。SQLite 版は sqlite/0001_init.sql。
-- 照合順序は utf8mb4_bin（大文字小文字・末尾空白を区別する。4 バイト文字を含められる）。
-- 追記専用の表（comments / issue_events / usage_snapshots / usage_reports / usage_report_requests / setting_changes）は、
-- アプリ用の DB ユーザーに SELECT・INSERT だけを与えて書き換え・削除を防ぐ（deploy/grants.sql）。
-- 外部キーの参照先を先に作るため、表は参照される側から順に並べる。

-- プロジェクト。prefix と width で表示用の ID（例: ABC-0001）を作る。発番後は変えない。
CREATE TABLE projects (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  slug        VARCHAR(64)     NOT NULL,
  prefix      VARCHAR(32)     NOT NULL,
  width       TINYINT UNSIGNED NOT NULL,
  name        VARCHAR(255)    NOT NULL,
  description VARCHAR(1024)   NOT NULL DEFAULT '',
  sort_order  INT             NOT NULL DEFAULT 100,
  counter     INT UNSIGNED    NOT NULL DEFAULT 0 COMMENT '最後に採番した番号',
  rules       JSON            NULL COMMENT 'プロジェクト別ルール',
  created_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uk_projects_slug (slug),
  UNIQUE KEY uk_projects_prefix (prefix)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 利用者。TOTP のシークレットは IM_SECRET_KEY で暗号化して持つ。
-- totp_last_step は最後に受け入れたステップ（同じコードの再利用を拒否する）、totp_pending は登録途中のシークレット。
CREATE TABLE users (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  login          VARCHAR(64)     NOT NULL,
  display_name   VARCHAR(255)    NOT NULL DEFAULT '',
  password_hash  VARCHAR(255)    NOT NULL COMMENT 'argon2id（PHC 文字列）',
  totp_secret    VARBINARY(255)  NULL COMMENT '暗号化した TOTP シークレット',
  totp_enabled   BOOLEAN         NOT NULL DEFAULT FALSE,
  totp_last_step BIGINT          NOT NULL DEFAULT 0 COMMENT '最後に受け入れた TOTP のステップ（再利用防止）',
  totp_pending   VARBINARY(255)  NULL COMMENT '登録途中の TOTP シークレット（暗号化）',
  role           VARCHAR(16)     NOT NULL DEFAULT 'member',
  created_at     DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_login_at  DATETIME(6)     NULL,
  disabled_at    DATETIME(6)     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_users_login (login),
  CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- イシュー。Markdown ファイル（frontmatter + 本文 + コメント節）と往復できる形で持つ
-- （front_keys・gap_nl・preamble・trail_nl は元の書式を再現するための情報）。
-- assignee_user_id はサーバだけの項目で、front_keys には入れない。担当の変更は issue_events に残す。
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
  assignee_user_id    BIGINT UNSIGNED NULL COMMENT '担当者（NULL は未設定）',
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
  KEY k_issues_project_assignee (project_id, assignee_user_id),
  CONSTRAINT fk_issues_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_issues_assignee FOREIGN KEY (assignee_user_id) REFERENCES users (id),
  CONSTRAINT chk_issues_type CHECK (type IN ('', 'requirement', 'design', 'task', 'bug', 'test', 'epic')),
  CONSTRAINT chk_issues_status CHECK (status IN ('', 'Backlog', 'Todo', 'In Progress', 'In Review', 'Done', 'Canceled')),
  CONSTRAINT chk_issues_priority CHECK (priority IN ('', 'P0', 'P1', 'P2', 'P3'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- イシューの一覧型の項目（ラベル・依存・関連）。1 要素 1 行。
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

-- 決まった列を持たない frontmatter のキー（位置は issues.front_keys）。
CREATE TABLE issue_extra (
  issue_id BIGINT UNSIGNED NOT NULL,
  `key`    VARCHAR(64)     NOT NULL COMMENT 'origin など未知の frontmatter キー（位置は issues.front_keys）',
  is_list  BOOLEAN         NOT NULL DEFAULT FALSE,
  value    MEDIUMTEXT      NOT NULL COMMENT 'is_list のときは JSON 配列',
  PRIMARY KEY (issue_id, `key`),
  CONSTRAINT fk_issue_extra_issue FOREIGN KEY (issue_id) REFERENCES issues (id)
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

-- API トークン（個人用アクセストークンと OAuth のアクセストークン）。平文は保存しない。
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

-- コメント（追記専用）。
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

-- 操作の記録（追記専用）。kind に CHECK は付けない（種類は増える）。
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

-- ブラウザのセッション。mfa_passed はログインの段階を終えたか、totp_verified は TOTP を入力して発行したか
-- （二段階認証を任意 → 必須に切り替えたとき、TOTP を経ていないセッションを無効にするのに使う）。
CREATE TABLE web_sessions (
  id_hash       BINARY(32)      NOT NULL COMMENT 'Cookie 値の SHA-256',
  user_id       BIGINT UNSIGNED NOT NULL,
  csrf_token    CHAR(64)        NOT NULL,
  mfa_passed    BOOLEAN         NOT NULL DEFAULT FALSE,
  totp_verified BOOLEAN         NOT NULL DEFAULT FALSE COMMENT 'TOTP を入力して発行したセッション',
  created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  expires_at    DATETIME(6)     NOT NULL,
  last_seen_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  ip            VARCHAR(45)     NOT NULL DEFAULT '',
  user_agent    VARCHAR(255)    NOT NULL DEFAULT '',
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

-- OAuth（MCP の接続に使う）。クライアントは動的登録、認可コードは PKCE。
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

-- OAuth の更新トークン。平文は保存しない（SHA-256）。使うたびに入れ替え、同じ系列（family_id）の中で
-- 使用済みの更新トークンが再び出されたら漏えいとみなし、系列の更新トークンとアクセストークンをすべて失効させる。
-- access_token_id は同時に発行したアクセストークン（それを失効させると、この更新トークンも使えない）。
-- 行は消さない（アプリ用 DB ユーザーに DELETE を与えない）。
CREATE TABLE oauth_refresh_tokens (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  token_hash      BINARY(32)      NOT NULL COMMENT 'SHA-256',
  family_id       CHAR(22)        NOT NULL COMMENT '認可コード 1 回から続く入れ替えの系列（乱数 16 バイトの base64url）',
  user_id         BIGINT UNSIGNED NOT NULL,
  client_id       VARCHAR(64)     NOT NULL,
  access_token_id BIGINT UNSIGNED NOT NULL COMMENT '同時に発行したアクセストークン',
  scope           VARCHAR(255)    NOT NULL DEFAULT '',
  resource        VARCHAR(2048)   NOT NULL DEFAULT '',
  created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  expires_at      DATETIME(6)     NOT NULL,
  used_at         DATETIME(6)     NULL COMMENT '入れ替えに使った時刻（以後この値の提示は再利用）',
  revoked_at      DATETIME(6)     NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_oauth_refresh_hash (token_hash),
  KEY k_oauth_refresh_family (family_id),
  KEY k_oauth_refresh_access (access_token_id),
  CONSTRAINT fk_oauth_refresh_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_oauth_refresh_client FOREIGN KEY (client_id) REFERENCES oauth_clients (client_id),
  CONSTRAINT fk_oauth_refresh_access FOREIGN KEY (access_token_id) REFERENCES api_tokens (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- コーディング AI のトークン消費のスナップショット（追記専用）。ある時点の「会話の累計」を 1 行で持つ。
-- 差分とイシューへの帰属は保存せず、問い合わせのときに計算する。
CREATE TABLE usage_snapshots (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id        BIGINT UNSIGNED NOT NULL,
  user_id           BIGINT UNSIGNED NOT NULL,
  token_id          BIGINT UNSIGNED NULL,
  received_at       DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  client            VARCHAR(32)     NOT NULL COMMENT 'claude-code / codex / …',
  client_version    VARCHAR(64)     NOT NULL DEFAULT '',
  session_id        VARCHAR(128)    NOT NULL,
  conversation_id   VARCHAR(64)     NOT NULL COMMENT '再開でセッション ID が変わっても同じ値',
  trigger_kind      VARCHAR(16)     NOT NULL COMMENT 'issue_op / stop / session_end / manual / import',
  issue_id          BIGINT UNSIGNED NULL COMMENT 'issue_op のときの対象',
  op                VARCHAR(16)     NULL COMMENT 'create / update / comment / status（issue_events.kind と同じ語）',
  issue_status      VARCHAR(32)     NULL COMMENT '受け取った時点のイシューの状態（区間の帰属に使う）',
  via               VARCHAR(16)     NULL COMMENT 'その操作の経路 cli / mcp',
  at                DATETIME(6)     NOT NULL COMMENT '会話記録の最後の時刻（クライアント側・UTC）',
  main_input        BIGINT UNSIGNED NOT NULL DEFAULT 0,
  main_cache_create BIGINT UNSIGNED NOT NULL DEFAULT 0,
  main_cache_read   BIGINT UNSIGNED NOT NULL DEFAULT 0,
  main_output       BIGINT UNSIGNED NOT NULL DEFAULT 0,
  sub_input         BIGINT UNSIGNED NOT NULL DEFAULT 0,
  sub_cache_create  BIGINT UNSIGNED NOT NULL DEFAULT 0,
  sub_cache_read    BIGINT UNSIGNED NOT NULL DEFAULT 0,
  sub_output        BIGINT UNSIGNED NOT NULL DEFAULT 0,
  responses         INT UNSIGNED    NOT NULL DEFAULT 0,
  sub_responses     INT UNSIGNED    NOT NULL DEFAULT 0,
  by_model          JSON            NULL,
  io                JSON            NULL,
  human             JSON            NULL,
  segments          JSON            NULL COMMENT '区間（人間の指示ごと）の数値。stop / session_end のときだけ',
  branch            VARCHAR(255)    NOT NULL DEFAULT '',
  branches          JSON            NULL,
  cwd_name          VARCHAR(255)    NOT NULL DEFAULT '',
  excluded          TINYINT(1)      NOT NULL DEFAULT 0 COMMENT 'レポート対象外の指示があった会話',
  dedupe_key        CHAR(64)        NOT NULL COMMENT '同じ内容の再送を 1 行にする',
  PRIMARY KEY (id),
  UNIQUE KEY uk_usage_snapshots_dedupe (dedupe_key),
  KEY k_usage_snapshots_conversation (project_id, conversation_id),
  KEY k_usage_snapshots_issue (issue_id),
  KEY k_usage_snapshots_received (project_id, received_at),
  CONSTRAINT fk_usage_snapshots_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_snapshots_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_usage_snapshots_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_usage_snapshots_trigger CHECK (trigger_kind IN ('issue_op', 'stop', 'session_end', 'manual', 'import'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- トークンレポートの作成依頼（追記専用）。画面の「レポート作成」で 1 行足す。
-- 完了は依頼の行を書き換えず、台帳 usage_reports の request_id がこの行を指していることで判定する（1 依頼に台帳 1 行まで）。
CREATE TABLE usage_report_requests (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id   BIGINT UNSIGNED NOT NULL,
  since_last   TINYINT(1)      NOT NULL DEFAULT 0 COMMENT '1 は前回のレポート（台帳の最新のデータ終端）以降。起点は作成するときに決まる',
  period_from  DATETIME(6)     NULL COMMENT '対象期間の始まり（含む・UTC）。since_last のときは NULL',
  period_to    DATETIME(6)     NULL COMMENT '対象期間の終わり（含まない・UTC）。NULL は作成するときの今まで',
  target       VARCHAR(500)    NOT NULL DEFAULT '' COMMENT '対象（空はプロジェクト全体。例: ラベル api のイシュー）',
  note         TEXT            NULL COMMENT 'メモ（AI への指示・提出先など）',
  requested_by BIGINT UNSIGNED NOT NULL,
  token_id     BIGINT UNSIGNED NULL,
  via          VARCHAR(16)     NOT NULL COMMENT 'web / api / cli / mcp',
  created_at   DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  KEY k_usage_report_requests_project (project_id, id),
  CONSTRAINT fk_usage_report_requests_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_report_requests_user FOREIGN KEY (requested_by) REFERENCES users (id),
  CONSTRAINT chk_usage_report_requests_period CHECK (
    (since_last = 1 AND period_from IS NULL) OR (since_last = 0 AND period_from IS NOT NULL)),
  CONSTRAINT chk_usage_report_requests_order CHECK (period_from IS NULL OR period_to IS NULL OR period_from < period_to)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- トークンレポートの台帳（追記専用）。レポートを作ったら 1 行足す。次回の「前回以降」は、台帳で最も新しい data_end から始まる。
-- レポートの本文・PDF はサーバに置かない。
CREATE TABLE usage_reports (
  id                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id             BIGINT UNSIGNED NOT NULL,
  name                   VARCHAR(200)    NOT NULL COMMENT 'レポート名（プロジェクト内で一意）',
  period_from            DATETIME(6)     NULL COMMENT '対象期間の始まり（含む・UTC）。NULL は最初のデータから',
  period_to              DATETIME(6)     NOT NULL COMMENT '対象期間の終わり（含まない・UTC）',
  data_end               DATETIME(6)     NOT NULL COMMENT 'データ終端（UTC）。次回の「前回以降」はここから',
  excluded_conversations JSON            NULL COMMENT '集計から外した会話 ID の一覧',
  total_tokens           BIGINT UNSIGNED NULL COMMENT 'レポートに載せた合計（控え）',
  note                   TEXT            NULL,
  request_id             BIGINT UNSIGNED NULL COMMENT '画面からの作成依頼（usage_report_requests）の ID',
  created_by             BIGINT UNSIGNED NOT NULL,
  token_id               BIGINT UNSIGNED NULL,
  via                    VARCHAR(16)     NOT NULL COMMENT 'cli / api / web / mcp',
  created_at             DATETIME(6)     NOT NULL COMMENT 'レポートを作った日時（過去の台帳を取り込むときは元の日時）',
  recorded_at            DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '台帳に登録した日時',
  PRIMARY KEY (id),
  UNIQUE KEY uk_usage_reports_name (project_id, name),
  UNIQUE KEY uk_usage_reports_request (request_id),
  KEY k_usage_reports_data_end (project_id, data_end),
  CONSTRAINT fk_usage_reports_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_reports_user FOREIGN KEY (created_by) REFERENCES users (id),
  CONSTRAINT fk_usage_reports_request FOREIGN KEY (request_id) REFERENCES usage_report_requests (id),
  CONSTRAINT chk_usage_reports_period CHECK (period_from IS NULL OR period_from < period_to)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- プロジェクトの運用文書（Markdown）。guide が「共通規則 + プロジェクト別ルール + 運用文書」を 1 回で返すために持つ。
-- 本文を projects に持たせないのは、一覧・権限判定で毎回読む projects の行を軽く保つため。
CREATE TABLE project_guides (
  project_id  BIGINT UNSIGNED NOT NULL,
  content     MEDIUMTEXT      NOT NULL COMMENT '運用文書（Markdown）',
  source      VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '登録元（例: docs/projects/<slug>.md）',
  updated_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (project_id),
  CONSTRAINT fk_project_guides_project FOREIGN KEY (project_id) REFERENCES projects (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- MCP の接続ごとの記録。MCP は要求ごとに一時セッションで動くため、initialize で受け取った clientInfo を
-- サーバが発行した ID（応答ヘッダの Mcp-Session-Id）で引けるようにする（接続してきた AI の種類を知るため）。
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

-- コーディング AI の作業環境への導入状態。利用者 × プロジェクト × AI の種類で 1 行（最後の通知）。
-- core / loop のハッシュは配布スクリプト（files）とは別に比べる（loop を入れていない導入に loop の更新を求めない）。
-- client_os が空の行はスクリプト版の CLI からの通知（files のハッシュで古いかを判定する）、
-- 空でない行は実行ファイル版の CLI からの通知（client_version を配布している最新の版と比べる）。
CREATE TABLE agent_installs (
  id                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id            BIGINT UNSIGNED NOT NULL,
  project_id         BIGINT UNSIGNED NOT NULL,
  agent              VARCHAR(32)     NOT NULL COMMENT 'claude-code / codex / other',
  source             VARCHAR(16)     NOT NULL DEFAULT '' COMMENT 'スクリプトの置き方 link / copy / server',
  files              JSON            NOT NULL COMMENT '{配布ファイル名: SHA-256}（手元の実体のハッシュ）',
  bundle_sha256      CHAR(64)        NOT NULL COMMENT '配布ファイル全体のハッシュ（名前順の「名前 ハッシュ」行の SHA-256）',
  client_version     VARCHAR(64)     NOT NULL DEFAULT '',
  host               VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '通知してきた端末のホスト名',
  workspace          VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '導入先ディレクトリの名前（パスは送らない）',
  first_at           DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  reported_at        DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '最後の通知（手動を含む）',
  hook_at            DATETIME(6)     NULL COMMENT '最後にフックから通知された時刻（フックが動いている証拠）',
  core_bundle_sha256 CHAR(64)        NULL COMMENT '.claude/.im-kit.json の core.bundle_sha256（init が置いた kit/core 一式のハッシュ。NULL は通知に無い）',
  loop_state         VARCHAR(16)     NOT NULL DEFAULT '' COMMENT 'installed / declined / none（未選択）。空は loop を送らない古い CLI',
  loop_bundle_sha256 CHAR(64)        NULL COMMENT 'loop が installed のときの kit/loop 一式のハッシュ',
  loop_version       VARCHAR(64)     NOT NULL DEFAULT '' COMMENT 'kit/loop/manifest.json の version',
  client_os          VARCHAR(16)     NOT NULL DEFAULT '' COMMENT 'looptrack の GOOS（linux / darwin / windows）。空は Python の CLI',
  client_arch        VARCHAR(16)     NOT NULL DEFAULT '' COMMENT 'looptrack の GOARCH（amd64 / arm64）',
  PRIMARY KEY (id),
  UNIQUE KEY uk_agent_installs (user_id, project_id, agent),
  CONSTRAINT fk_agent_installs_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_agent_installs_project FOREIGN KEY (project_id) REFERENCES projects (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- システム全体の設定（名前 → 値）。two_factor は required（必須）/ optional（任意）。
-- 新しい DB では未設定のまま始まり、最初の利用者を作るときに決める（未設定の間の挙動は必須と同じ）。
CREATE TABLE system_settings (
  name       VARCHAR(64)  NOT NULL,
  value      VARCHAR(255) NOT NULL,
  updated_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 設定の変更記録（追記専用）。誰が・いつ・どの値からどの値へ。
CREATE TABLE setting_changes (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  at            DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  name          VARCHAR(64)     NOT NULL,
  old_value     VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '空は未設定',
  new_value     VARCHAR(255)    NOT NULL,
  actor_user_id BIGINT UNSIGNED NULL COMMENT '操作した利用者（管理コマンド・マイグレーションは NULL）',
  via           VARCHAR(16)     NOT NULL COMMENT 'web / command / migration',
  note          VARCHAR(255)    NOT NULL DEFAULT '',
  ip            VARCHAR(45)     NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY k_setting_changes_name_at (name, at),
  CONSTRAINT fk_setting_changes_actor FOREIGN KEY (actor_user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
