ALTER TABLE agent_installs
  ADD COLUMN core_bundle_sha256 CHAR(64)    NULL COMMENT '.claude/.im-kit.json の core.bundle_sha256（init が置いた kit/core 一式のハッシュ。NULL は通知に無い）',
  ADD COLUMN loop_state         VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'installed / declined / none（未選択）。空は loop を送らない古い CLI',
  ADD COLUMN loop_bundle_sha256 CHAR(64)    NULL COMMENT 'loop が installed のときの kit/loop 一式のハッシュ',
  ADD COLUMN loop_version       VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'kit/loop/manifest.json の version';
