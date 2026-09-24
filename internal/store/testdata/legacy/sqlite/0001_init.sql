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
  CONSTRAINT chk_issues_type CHECK (type IN ('', 'requirement', 'design', 'task', 'bug', 'test', 'epic')),
  CONSTRAINT chk_issues_status CHECK (status IN ('', 'Backlog', 'Todo', 'In Progress', 'In Review', 'Done', 'Canceled')),
  CONSTRAINT chk_issues_priority CHECK (priority IN ('', 'P0', 'P1', 'P2', 'P3'))
);
CREATE INDEX k_issues_project_status ON issues (project_id, status);
CREATE INDEX k_issues_project_type ON issues (project_id, type);

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

CREATE TABLE users (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  login         TEXT    NOT NULL,
  display_name  TEXT    NOT NULL DEFAULT '',
  password_hash TEXT    NOT NULL,
  totp_secret   BLOB    NULL,
  totp_enabled  INTEGER NOT NULL DEFAULT 0,
  role          TEXT    NOT NULL DEFAULT 'member',
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  last_login_at DATETIME NULL,
  disabled_at   DATETIME NULL,
  CONSTRAINT uk_users_login UNIQUE (login),
  CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member'))
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
  id_hash      BLOB    NOT NULL PRIMARY KEY,
  user_id      INTEGER NOT NULL,
  csrf_token   TEXT    NOT NULL,
  mfa_passed   INTEGER NOT NULL DEFAULT 0,
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  expires_at   DATETIME NOT NULL,
  last_seen_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  ip           TEXT    NOT NULL DEFAULT '',
  user_agent   TEXT    NOT NULL DEFAULT '',
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

CREATE TRIGGER trg_projects_updated_at AFTER UPDATE ON projects FOR EACH ROW WHEN NEW.updated_at IS OLD.updated_at BEGIN UPDATE projects SET updated_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE id = NEW.id; END;
CREATE TRIGGER trg_issues_changed_at AFTER UPDATE ON issues FOR EACH ROW WHEN NEW.changed_at IS OLD.changed_at BEGIN UPDATE issues SET changed_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE id = NEW.id; END;

CREATE TRIGGER trg_comments_no_update BEFORE UPDATE ON comments WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: comments は書き換えられません'); END;
CREATE TRIGGER trg_comments_no_delete BEFORE DELETE ON comments WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: comments は削除できません'); END;
CREATE TRIGGER trg_issue_events_no_update BEFORE UPDATE ON issue_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issue_events は書き換えられません'); END;
CREATE TRIGGER trg_issue_events_no_delete BEFORE DELETE ON issue_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issue_events は削除できません'); END;
CREATE TRIGGER trg_projects_no_delete BEFORE DELETE ON projects WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: projects は削除できません'); END;
CREATE TRIGGER trg_issues_no_delete BEFORE DELETE ON issues WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: issues は削除できません'); END;
