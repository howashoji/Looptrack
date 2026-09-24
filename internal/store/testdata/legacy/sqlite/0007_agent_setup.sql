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
  id             INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  user_id        INTEGER NOT NULL,
  project_id     INTEGER NOT NULL,
  agent          TEXT    NOT NULL,
  source         TEXT    NOT NULL DEFAULT '',
  files          TEXT    NOT NULL,
  bundle_sha256  TEXT    NOT NULL,
  client_version TEXT    NOT NULL DEFAULT '',
  host           TEXT    NOT NULL DEFAULT '',
  workspace      TEXT    NOT NULL DEFAULT '',
  first_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  reported_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  hook_at        DATETIME NULL,
  CONSTRAINT uk_agent_installs UNIQUE (user_id, project_id, agent),
  CONSTRAINT fk_agent_installs_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_agent_installs_project FOREIGN KEY (project_id) REFERENCES projects (id)
);
