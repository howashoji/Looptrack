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
