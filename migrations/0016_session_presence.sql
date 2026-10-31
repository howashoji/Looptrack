-- 在席（同じプロジェクトで動いているセッションの一覧。docs/server/DESIGN.md §9-9）。
-- 利用者とセッションごとの 1 行で、hook が送るたびに書き換え、セッションの終わりと定期の処理で消す。
-- アプリ用の DB ユーザーには SELECT・INSERT・UPDATE・DELETE を与える（deploy/grants.sql）。
-- 作業ツリーの絶対パスは持たない。ホームディレクトリの利用者名がほかの参加者に見えるため、ディレクトリの名前だけを持つ。
CREATE TABLE session_presence (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id    BIGINT UNSIGNED NOT NULL,
  user_id       BIGINT UNSIGNED NOT NULL,
  session_id    VARCHAR(128)    NOT NULL COMMENT 'AI のセッション（X-Looptrack-Session）',
  agent         VARCHAR(32)     NOT NULL DEFAULT '' COMMENT 'AI の種類',
  machine_label VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '端末のラベル',
  branch        VARCHAR(255)    NOT NULL DEFAULT '' COMMENT 'ブランチ',
  head_sha      VARCHAR(64)     NOT NULL DEFAULT '' COMMENT 'HEAD の SHA',
  directory     VARCHAR(255)    NOT NULL DEFAULT '' COMMENT '作業ツリーのディレクトリの名前（最後の要素だけ）',
  created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_seen_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '最後に送った時刻',
  PRIMARY KEY (id),
  UNIQUE KEY uk_session_presence (project_id, user_id, session_id),
  KEY idx_session_presence_seen (project_id, last_seen_at),
  KEY idx_session_presence_purge (last_seen_at),
  CONSTRAINT fk_session_presence_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_session_presence_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
