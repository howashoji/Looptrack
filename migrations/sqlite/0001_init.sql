-- 0001（SQLite 版。MySQL 版は ../0001_init.sql）: 1.0.0 のスキーマ。表・列・索引・外部キーの構成は MySQL 版と同じにする
-- （internal/store の TestSQLiteSchemaMatchesMySQL が確かめる）。列の説明は MySQL 版の COMMENT を参照。
-- 型: 整数 → INTEGER、文字列・JSON → TEXT、バイト列 → BLOB、DATETIME(6) → DATETIME（'YYYY-MM-DD HH:MM:SS.ffffff' の UTC 文字列）。
-- 照合順序は SQLite の既定 BINARY（MySQL の utf8mb4_bin と同じくバイト列で比べる）。
-- CURRENT_TIMESTAMP(6) の既定値は strftime（ミリ秒まで）に '000' を足して 6 桁にそろえる（internal/store/sqlite.go の sqliteNow と同じ式）。
-- ON UPDATE CURRENT_TIMESTAMP(6) はトリガで代える。
-- MySQL の KEY は CREATE INDEX、UNIQUE KEY は UNIQUE 制約（名前付き）で書く。
-- 追記専用（MySQL は deploy/grants.sql の権限で担保）は、SQLite ではトリガで UPDATE / DELETE を拒否する。
-- 管理用の置き換え（transfer の取り込み）だけは、同じトランザクションで append_only_unlock に 1 行入れて外す（store.UnlockAppendOnly）。

CREATE TABLE append_only_unlock (
  id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1)
);

CREATE TABLE projects (
  id          INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  slug        TEXT    NOT NULL,
  prefix      TEXT    NOT NULL,
  width       INTEGER NOT NULL,
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  sort_order  INTEGER NOT NULL DEFAULT 100,
  counter     INTEGER NOT NULL DEFAULT 0,
  rules       TEXT    NULL,
  created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_projects_slug UNIQUE (slug),
  CONSTRAINT uk_projects_prefix UNIQUE (prefix)
);

CREATE TABLE users (
  id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  login          TEXT    NOT NULL,
  display_name   TEXT    NOT NULL DEFAULT '',
  password_hash  TEXT    NOT NULL,
  totp_secret    BLOB    NULL,
  totp_enabled   INTEGER NOT NULL DEFAULT 0,
  totp_last_step INTEGER NOT NULL DEFAULT 0,
  totp_pending   BLOB    NULL,
  role           TEXT    NOT NULL DEFAULT 'member',
  created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  last_login_at  DATETIME NULL,
  disabled_at    DATETIME NULL,
  CONSTRAINT uk_users_login UNIQUE (login),
  CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member'))
);

CREATE TABLE issues (
  id                  INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id          INTEGER NOT NULL,
  number              INTEGER NOT NULL,
  display_id          TEXT    NOT NULL,
  file_name           BLOB    NOT NULL,
  front_keys          TEXT    NOT NULL,
  title               TEXT    NOT NULL DEFAULT '',
  type                TEXT    NOT NULL DEFAULT '',
  status              TEXT    NOT NULL DEFAULT '',
  priority            TEXT    NOT NULL DEFAULT '',
  parent              TEXT    NOT NULL DEFAULT '',
  assignee_user_id    INTEGER NULL,
  created             TEXT    NOT NULL DEFAULT '',
  updated             TEXT    NOT NULL DEFAULT '',
  body_main           TEXT    NOT NULL,
  gap_nl              INTEGER NOT NULL DEFAULT 2,
  has_comment_section INTEGER NOT NULL DEFAULT 1,
  preamble            TEXT    NULL,
  trail_nl            INTEGER NOT NULL DEFAULT 1,
  version             INTEGER NOT NULL DEFAULT 1,
  changed_at          DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_issues_display_id UNIQUE (display_id),
  CONSTRAINT uk_issues_project_number UNIQUE (project_id, number),
  CONSTRAINT fk_issues_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_issues_assignee FOREIGN KEY (assignee_user_id) REFERENCES users (id),
  CONSTRAINT chk_issues_type CHECK (type IN ('', 'requirement', 'design', 'task', 'bug', 'test', 'epic')),
  CONSTRAINT chk_issues_status CHECK (status IN ('', 'Backlog', 'Todo', 'In Progress', 'In Review', 'Done', 'Canceled')),
  CONSTRAINT chk_issues_priority CHECK (priority IN ('', 'P0', 'P1', 'P2', 'P3'))
);
CREATE INDEX k_issues_project_status ON issues (project_id, status);
CREATE INDEX k_issues_project_type ON issues (project_id, type);
CREATE INDEX k_issues_project_assignee ON issues (project_id, assignee_user_id);

CREATE TABLE issue_values (
  issue_id INTEGER NOT NULL,
  field    TEXT    NOT NULL,
  pos      INTEGER NOT NULL,
  value    TEXT    NOT NULL,
  PRIMARY KEY (issue_id, field, pos),
  CONSTRAINT fk_issue_values_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_issue_values_field CHECK (field IN ('labels', 'blocked_by', 'traces', 'refs'))
);
CREATE INDEX k_issue_values_lookup ON issue_values (field, value);

CREATE TABLE issue_extra (
  issue_id INTEGER NOT NULL,
  `key`    TEXT    NOT NULL,
  is_list  INTEGER NOT NULL DEFAULT 0,
  value    TEXT    NOT NULL,
  PRIMARY KEY (issue_id, `key`),
  CONSTRAINT fk_issue_extra_issue FOREIGN KEY (issue_id) REFERENCES issues (id)
);

CREATE TABLE project_members (
  project_id INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  role       TEXT    NOT NULL DEFAULT 'editor',
  PRIMARY KEY (project_id, user_id),
  CONSTRAINT fk_project_members_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_project_members_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_project_members_role CHECK (role IN ('viewer', 'editor', 'admin'))
);
CREATE INDEX k_project_members_user ON project_members (user_id);

CREATE TABLE api_tokens (
  id              INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  user_id         INTEGER NOT NULL,
  kind            TEXT    NOT NULL DEFAULT 'pat',
  name            TEXT    NOT NULL DEFAULT '',
  token_prefix    TEXT    NOT NULL,
  token_hash      BLOB    NOT NULL,
  scopes          TEXT    NULL,
  oauth_client_id TEXT    NULL,
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  expires_at      DATETIME NULL,
  last_used_at    DATETIME NULL,
  revoked_at      DATETIME NULL,
  CONSTRAINT uk_api_tokens_hash UNIQUE (token_hash),
  CONSTRAINT fk_api_tokens_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_api_tokens_kind CHECK (kind IN ('pat', 'oauth'))
);
CREATE INDEX k_api_tokens_user ON api_tokens (user_id);

CREATE TABLE comments (
  id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  issue_id       INTEGER NOT NULL,
  seq            INTEGER NOT NULL,
  ts             TEXT    NOT NULL,
  content        TEXT    NOT NULL,
  created_at     DATETIME NULL,
  author_user_id INTEGER NULL,
  token_id       INTEGER NULL,
  via            TEXT    NOT NULL DEFAULT 'import',
  CONSTRAINT uk_comments_issue_seq UNIQUE (issue_id, seq),
  CONSTRAINT fk_comments_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT fk_comments_author FOREIGN KEY (author_user_id) REFERENCES users (id),
  CONSTRAINT chk_comments_via CHECK (via IN ('import', 'cli', 'web', 'mcp', 'api'))
);

CREATE TABLE issue_events (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id    INTEGER NOT NULL,
  issue_id      INTEGER NULL,
  at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  kind          TEXT    NOT NULL,
  via           TEXT    NOT NULL,
  actor_user_id INTEGER NULL,
  token_id      INTEGER NULL,
  session_id    TEXT    NULL,
  detail        TEXT    NULL,
  CONSTRAINT fk_issue_events_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_issue_events_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_issue_events_via CHECK (via IN ('import', 'cli', 'web', 'mcp', 'api', 'admin'))
);
CREATE INDEX k_issue_events_issue_at ON issue_events (issue_id, at);
CREATE INDEX k_issue_events_project_at ON issue_events (project_id, at);

CREATE TABLE web_sessions (
  id_hash       BLOB    NOT NULL PRIMARY KEY,
  user_id       INTEGER NOT NULL,
  csrf_token    TEXT    NOT NULL,
  mfa_passed    INTEGER NOT NULL DEFAULT 0,
  totp_verified INTEGER NOT NULL DEFAULT 0,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  expires_at    DATETIME NOT NULL,
  last_seen_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  ip            TEXT    NOT NULL DEFAULT '',
  user_agent    TEXT    NOT NULL DEFAULT '',
  CONSTRAINT fk_web_sessions_user FOREIGN KEY (user_id) REFERENCES users (id)
);
CREATE INDEX k_web_sessions_user ON web_sessions (user_id);
CREATE INDEX k_web_sessions_expires ON web_sessions (expires_at);

CREATE TABLE login_attempts (
  id      INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  login   TEXT    NOT NULL,
  ip      TEXT    NOT NULL,
  at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  success INTEGER NOT NULL,
  stage   TEXT    NOT NULL DEFAULT 'password'
);
CREATE INDEX k_login_attempts_login_at ON login_attempts (login, at);
CREATE INDEX k_login_attempts_ip_at ON login_attempts (ip, at);

CREATE TABLE oauth_clients (
  client_id     TEXT NOT NULL PRIMARY KEY,
  client_name   TEXT NOT NULL DEFAULT '',
  redirect_uris TEXT NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000')
);

CREATE TABLE oauth_codes (
  code_hash      BLOB    NOT NULL PRIMARY KEY,
  client_id      TEXT    NOT NULL,
  user_id        INTEGER NOT NULL,
  redirect_uri   TEXT    NOT NULL,
  code_challenge TEXT    NOT NULL,
  scope          TEXT    NOT NULL DEFAULT '',
  resource       TEXT    NOT NULL DEFAULT '',
  expires_at     DATETIME NOT NULL,
  used_at        DATETIME NULL,
  CONSTRAINT fk_oauth_codes_client FOREIGN KEY (client_id) REFERENCES oauth_clients (client_id),
  CONSTRAINT fk_oauth_codes_user FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE TABLE oauth_refresh_tokens (
  id              INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  token_hash      BLOB    NOT NULL,
  family_id       TEXT    NOT NULL,
  user_id         INTEGER NOT NULL,
  client_id       TEXT    NOT NULL,
  access_token_id INTEGER NOT NULL,
  scope           TEXT    NOT NULL DEFAULT '',
  resource        TEXT    NOT NULL DEFAULT '',
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  expires_at      DATETIME NOT NULL,
  used_at         DATETIME NULL,
  revoked_at      DATETIME NULL,
  CONSTRAINT uk_oauth_refresh_hash UNIQUE (token_hash),
  CONSTRAINT fk_oauth_refresh_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_oauth_refresh_client FOREIGN KEY (client_id) REFERENCES oauth_clients (client_id),
  CONSTRAINT fk_oauth_refresh_access FOREIGN KEY (access_token_id) REFERENCES api_tokens (id)
);
CREATE INDEX k_oauth_refresh_family ON oauth_refresh_tokens (family_id);
CREATE INDEX k_oauth_refresh_access ON oauth_refresh_tokens (access_token_id);

CREATE TABLE usage_snapshots (
  id                INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id        INTEGER NOT NULL,
  user_id           INTEGER NOT NULL,
  token_id          INTEGER NULL,
  received_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  client            TEXT    NOT NULL,
  client_version    TEXT    NOT NULL DEFAULT '',
  session_id        TEXT    NOT NULL,
  conversation_id   TEXT    NOT NULL,
  trigger_kind      TEXT    NOT NULL,
  issue_id          INTEGER NULL,
  op                TEXT    NULL,
  issue_status      TEXT    NULL,
  via               TEXT    NULL,
  at                DATETIME NOT NULL,
  main_input        INTEGER NOT NULL DEFAULT 0,
  main_cache_create INTEGER NOT NULL DEFAULT 0,
  main_cache_read   INTEGER NOT NULL DEFAULT 0,
  main_output       INTEGER NOT NULL DEFAULT 0,
  sub_input         INTEGER NOT NULL DEFAULT 0,
  sub_cache_create  INTEGER NOT NULL DEFAULT 0,
  sub_cache_read    INTEGER NOT NULL DEFAULT 0,
  sub_output        INTEGER NOT NULL DEFAULT 0,
  responses         INTEGER NOT NULL DEFAULT 0,
  sub_responses     INTEGER NOT NULL DEFAULT 0,
  by_model          TEXT    NULL,
  io                TEXT    NULL,
  human             TEXT    NULL,
  segments          TEXT    NULL,
  branch            TEXT    NOT NULL DEFAULT '',
  branches          TEXT    NULL,
  cwd_name          TEXT    NOT NULL DEFAULT '',
  excluded          INTEGER NOT NULL DEFAULT 0,
  dedupe_key        TEXT    NOT NULL,
  CONSTRAINT uk_usage_snapshots_dedupe UNIQUE (dedupe_key),
  CONSTRAINT fk_usage_snapshots_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_snapshots_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_usage_snapshots_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_usage_snapshots_trigger CHECK (trigger_kind IN ('issue_op', 'stop', 'session_end', 'manual', 'import'))
);
CREATE INDEX k_usage_snapshots_conversation ON usage_snapshots (project_id, conversation_id);
CREATE INDEX k_usage_snapshots_issue ON usage_snapshots (issue_id);
CREATE INDEX k_usage_snapshots_received ON usage_snapshots (project_id, received_at);

CREATE TABLE usage_report_requests (
  id           INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id   INTEGER NOT NULL,
  since_last   INTEGER NOT NULL DEFAULT 0,
  period_from  DATETIME NULL,
  period_to    DATETIME NULL,
  target       TEXT    NOT NULL DEFAULT '',
  note         TEXT    NULL,
  requested_by INTEGER NOT NULL,
  token_id     INTEGER NULL,
  via          TEXT    NOT NULL,
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_usage_report_requests_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_report_requests_user FOREIGN KEY (requested_by) REFERENCES users (id),
  CONSTRAINT chk_usage_report_requests_period CHECK (
    (since_last = 1 AND period_from IS NULL) OR (since_last = 0 AND period_from IS NOT NULL)),
  CONSTRAINT chk_usage_report_requests_order CHECK (period_from IS NULL OR period_to IS NULL OR period_from < period_to)
);
CREATE INDEX k_usage_report_requests_project ON usage_report_requests (project_id, id);

CREATE TABLE usage_reports (
  id                     INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  project_id             INTEGER NOT NULL,
  name                   TEXT    NOT NULL,
  period_from            DATETIME NULL,
  period_to              DATETIME NOT NULL,
  data_end               DATETIME NOT NULL,
  excluded_conversations TEXT    NULL,
  total_tokens           INTEGER NULL,
  note                   TEXT    NULL,
  request_id             INTEGER NULL,
  created_by             INTEGER NOT NULL,
  token_id               INTEGER NULL,
  via                    TEXT    NOT NULL,
  created_at             DATETIME NOT NULL,
  recorded_at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT uk_usage_reports_name UNIQUE (project_id, name),
  CONSTRAINT fk_usage_reports_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_usage_reports_user FOREIGN KEY (created_by) REFERENCES users (id),
  CONSTRAINT fk_usage_reports_request FOREIGN KEY (request_id) REFERENCES usage_report_requests (id),
  CONSTRAINT chk_usage_reports_period CHECK (period_from IS NULL OR period_from < period_to)
);
CREATE UNIQUE INDEX uk_usage_reports_request ON usage_reports (request_id);
CREATE INDEX k_usage_reports_data_end ON usage_reports (project_id, data_end);

CREATE TABLE project_guides (
  project_id INTEGER NOT NULL PRIMARY KEY,
  content    TEXT    NOT NULL,
  source     TEXT    NOT NULL DEFAULT '',
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_project_guides_project FOREIGN KEY (project_id) REFERENCES projects (id)
);

CREATE TABLE mcp_connections (
  id               TEXT    NOT NULL PRIMARY KEY,
  user_id          INTEGER NOT NULL,
  token_id         INTEGER NULL,
  client_name      TEXT    NOT NULL DEFAULT '',
  client_version   TEXT    NOT NULL DEFAULT '',
  agent            TEXT    NOT NULL,
  protocol_version TEXT    NOT NULL DEFAULT '',
  project          TEXT    NOT NULL DEFAULT '',
  user_agent       TEXT    NOT NULL DEFAULT '',
  created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  last_seen_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  closed_at        DATETIME NULL,
  CONSTRAINT fk_mcp_connections_user FOREIGN KEY (user_id) REFERENCES users (id)
);
CREATE INDEX k_mcp_connections_user ON mcp_connections (user_id, created_at);
CREATE INDEX k_mcp_connections_seen ON mcp_connections (last_seen_at);

CREATE TABLE agent_installs (
  id                 INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  user_id            INTEGER NOT NULL,
  project_id         INTEGER NOT NULL,
  agent              TEXT    NOT NULL,
  source             TEXT    NOT NULL DEFAULT '',
  files              TEXT    NOT NULL,
  bundle_sha256      TEXT    NOT NULL,
  client_version     TEXT    NOT NULL DEFAULT '',
  host               TEXT    NOT NULL DEFAULT '',
  workspace          TEXT    NOT NULL DEFAULT '',
  first_at           DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  reported_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  hook_at            DATETIME NULL,
  core_bundle_sha256 TEXT    NULL,
  loop_state         TEXT    NOT NULL DEFAULT '',
  loop_bundle_sha256 TEXT    NULL,
  loop_version       TEXT    NOT NULL DEFAULT '',
  client_os          TEXT    NOT NULL DEFAULT '',
  client_arch        TEXT    NOT NULL DEFAULT '',
  CONSTRAINT uk_agent_installs UNIQUE (user_id, project_id, agent),
  CONSTRAINT fk_agent_installs_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_agent_installs_project FOREIGN KEY (project_id) REFERENCES projects (id)
);

CREATE TABLE system_settings (
  name       TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000')
);

CREATE TABLE setting_changes (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  name          TEXT    NOT NULL,
  old_value     TEXT    NOT NULL DEFAULT '',
  new_value     TEXT    NOT NULL,
  actor_user_id INTEGER NULL,
  via           TEXT    NOT NULL,
  note          TEXT    NOT NULL DEFAULT '',
  ip            TEXT    NOT NULL DEFAULT '',
  CONSTRAINT fk_setting_changes_actor FOREIGN KEY (actor_user_id) REFERENCES users (id)
);
CREATE INDEX k_setting_changes_name_at ON setting_changes (name, at);

-- ON UPDATE CURRENT_TIMESTAMP(6) の代わり（列を明示して書き換えたときはその値のまま）
CREATE TRIGGER trg_projects_updated_at AFTER UPDATE ON projects FOR EACH ROW WHEN NEW.updated_at IS OLD.updated_at BEGIN UPDATE projects SET updated_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE id = NEW.id; END;
CREATE TRIGGER trg_issues_changed_at AFTER UPDATE ON issues FOR EACH ROW WHEN NEW.changed_at IS OLD.changed_at BEGIN UPDATE issues SET changed_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE id = NEW.id; END;
CREATE TRIGGER trg_project_guides_updated_at AFTER UPDATE ON project_guides FOR EACH ROW WHEN NEW.updated_at IS OLD.updated_at BEGIN UPDATE project_guides SET updated_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE project_id = NEW.project_id; END;
CREATE TRIGGER trg_system_settings_updated_at AFTER UPDATE ON system_settings FOR EACH ROW WHEN NEW.updated_at IS OLD.updated_at BEGIN UPDATE system_settings SET updated_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE name = NEW.name; END;

-- 追記専用（grants.sql の「SELECT・INSERT のみ」）と、削除の経路を持たない表（grants.sql で DELETE を与えない）
CREATE TRIGGER trg_comments_no_update BEFORE UPDATE ON comments WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: comments は書き換えられません'); END;
CREATE TRIGGER trg_comments_no_delete BEFORE DELETE ON comments WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: comments は削除できません'); END;
CREATE TRIGGER trg_issue_events_no_update BEFORE UPDATE ON issue_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issue_events は書き換えられません'); END;
CREATE TRIGGER trg_issue_events_no_delete BEFORE DELETE ON issue_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issue_events は削除できません'); END;
CREATE TRIGGER trg_usage_snapshots_no_update BEFORE UPDATE ON usage_snapshots WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_snapshots は書き換えられません'); END;
CREATE TRIGGER trg_usage_snapshots_no_delete BEFORE DELETE ON usage_snapshots WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_snapshots は削除できません'); END;
CREATE TRIGGER trg_usage_reports_no_update BEFORE UPDATE ON usage_reports WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_reports は書き換えられません'); END;
CREATE TRIGGER trg_usage_reports_no_delete BEFORE DELETE ON usage_reports WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_reports は削除できません'); END;
CREATE TRIGGER trg_usage_report_requests_no_update BEFORE UPDATE ON usage_report_requests WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_report_requests は書き換えられません'); END;
CREATE TRIGGER trg_usage_report_requests_no_delete BEFORE DELETE ON usage_report_requests WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: usage_report_requests は削除できません'); END;
CREATE TRIGGER trg_setting_changes_no_update BEFORE UPDATE ON setting_changes WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: setting_changes は書き換えられません'); END;
CREATE TRIGGER trg_setting_changes_no_delete BEFORE DELETE ON setting_changes WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: setting_changes は削除できません'); END;
CREATE TRIGGER trg_projects_no_delete BEFORE DELETE ON projects WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: projects は削除できません'); END;
CREATE TRIGGER trg_issues_no_delete BEFORE DELETE ON issues WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issues は削除できません'); END;
