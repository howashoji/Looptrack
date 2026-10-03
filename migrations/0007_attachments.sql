-- イシューの添付（エビデンスのファイル）。本体は DB ではなくディスクの置き場に sha256 の名前で置き、ここにはメタデータだけを持つ。
-- どちらの表も追記専用（アプリ用の DB ユーザーは SELECT・INSERT のみ。deploy/grants.sql）。
-- 消去（秘密を誤って添付したときの逃げ道）は本体のファイルだけを消し、attachment_purges に 1 行を足す。attachments の行は残す。
-- 同じ本体（sha256）を指す添付はまとめて消去済みになる。消去の後に同じ内容を添付し直した行は、その消去の対象にしない
-- （through_attachment_id より後の id なので、消去済みとは数えない）。
CREATE TABLE attachments (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id     BIGINT UNSIGNED NOT NULL,
  issue_id       BIGINT UNSIGNED NOT NULL,
  sha256         CHAR(64)        NOT NULL COMMENT '本体の SHA-256（16 進の小文字）。置き場のファイル名',
  size           BIGINT UNSIGNED NOT NULL COMMENT '本体のバイト数',
  filename       VARCHAR(255)    NOT NULL COMMENT '送られたファイル名（ディレクトリを除いた名前）',
  media_type     VARCHAR(255)    NOT NULL,
  created_at     DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  author_user_id BIGINT UNSIGNED NULL,
  token_id       BIGINT UNSIGNED NULL,
  via            VARCHAR(16)     NOT NULL,
  PRIMARY KEY (id),
  KEY k_attachments_issue (issue_id, id),
  KEY k_attachments_project (project_id),
  KEY k_attachments_sha256 (sha256),
  CONSTRAINT fk_attachments_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_attachments_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT fk_attachments_author FOREIGN KEY (author_user_id) REFERENCES users (id),
  CONSTRAINT chk_attachments_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE attachment_purges (
  id                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  sha256                CHAR(64)        NOT NULL COMMENT '消した本体',
  attachment_id         BIGINT UNSIGNED NOT NULL COMMENT '消去を指示した添付',
  through_attachment_id BIGINT UNSIGNED NOT NULL COMMENT 'この消去が効く添付の id の上限（消去の時点でこの本体を指していた最大の id）',
  at                    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  actor_user_id         BIGINT UNSIGNED NULL,
  via                   VARCHAR(16)     NOT NULL,
  reason                VARCHAR(255)    NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY k_attachment_purges_sha256 (sha256),
  CONSTRAINT fk_attachment_purges_attachment FOREIGN KEY (attachment_id) REFERENCES attachments (id),
  CONSTRAINT fk_attachment_purges_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT chk_attachment_purges_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
