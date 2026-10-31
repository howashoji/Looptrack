-- セッション・端末・利用者の間の連絡（docs/server/DESIGN.md §9-9）。
-- messages は送った連絡の 1 行、message_reads はセッションごとの既読の 1 行。どちらも追記専用（SELECT・INSERT のみ。deploy/grants.sql）。
-- 取り消しも既読の取り消しも無い。保持期間（message_retention_days）は読む側の条件で効かせ、行は消さない。
-- to_kind が project なら to_user_id は NULL、user と session なら宛先の利用者を持つ（session はその持ち主で、持ち主だけが読める）。
-- message_reads の session_id は読んだセッション（セッションの無い経路は空文字。主キーに NULL を入れられないため）。
CREATE TABLE messages (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id        BIGINT UNSIGNED NOT NULL,
  to_kind           VARCHAR(16)     NOT NULL COMMENT 'project / user / session',
  to_user_id        BIGINT UNSIGNED NULL COMMENT '宛先の利用者（project は NULL）',
  to_session        VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '宛先のセッション（session のときだけ）',
  sender_user_id    BIGINT UNSIGNED NOT NULL,
  sender_machine    VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '送った端末のラベル',
  sender_session    VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '送った AI のセッション（X-Looptrack-Session）',
  issue_id          BIGINT UNSIGNED NULL COMMENT '添えたイシュー',
  body              TEXT            NOT NULL,
  token_id          BIGINT UNSIGNED NULL,
  via               VARCHAR(16)     NOT NULL,
  created_at        DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  KEY k_messages_project (project_id, id),
  KEY k_messages_to_user (project_id, to_user_id, id),
  KEY k_messages_sender (project_id, sender_user_id, id),
  CONSTRAINT fk_messages_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_messages_to_user FOREIGN KEY (to_user_id) REFERENCES users (id),
  CONSTRAINT fk_messages_sender FOREIGN KEY (sender_user_id) REFERENCES users (id),
  CONSTRAINT fk_messages_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_messages_to_kind CHECK (to_kind IN ('project', 'user', 'session')),
  CONSTRAINT chk_messages_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE message_reads (
  message_id BIGINT UNSIGNED NOT NULL,
  user_id    BIGINT UNSIGNED NOT NULL,
  session_id VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '読んだセッション（無ければ空文字）',
  read_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (message_id, user_id, session_id),
  KEY k_message_reads_user (user_id, session_id),
  CONSTRAINT fk_message_reads_message FOREIGN KEY (message_id) REFERENCES messages (id),
  CONSTRAINT fk_message_reads_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
