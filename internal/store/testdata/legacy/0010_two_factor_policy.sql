CREATE TABLE system_settings (
  name       VARCHAR(64)  NOT NULL,
  value      VARCHAR(255) NOT NULL,
  updated_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

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

ALTER TABLE web_sessions
  ADD COLUMN totp_verified BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'TOTP を入力して発行したセッション' AFTER mfa_passed;

UPDATE web_sessions SET totp_verified = mfa_passed;

INSERT INTO system_settings (name, value)
SELECT 'two_factor', 'required' FROM DUAL WHERE EXISTS (SELECT 1 FROM users);

INSERT INTO setting_changes (name, old_value, new_value, via, note)
SELECT 'two_factor', '', 'required', 'migration', '既存の運用（TOTP 必須）を引き継ぐ' FROM DUAL WHERE EXISTS (SELECT 1 FROM users);
