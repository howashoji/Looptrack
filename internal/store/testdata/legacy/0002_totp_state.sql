ALTER TABLE users
  ADD COLUMN totp_last_step BIGINT NOT NULL DEFAULT 0 COMMENT '最後に受け入れた TOTP のステップ（再利用防止）' AFTER totp_enabled,
  ADD COLUMN totp_pending VARBINARY(255) NULL COMMENT '登録途中の TOTP シークレット（暗号化）' AFTER totp_last_step;
