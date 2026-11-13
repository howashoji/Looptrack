-- タイムラインの自動の出来事と読んだ位置（docs/server/DESIGN.md §9-9「タイムラインの表と通し番号」）。
-- timeline_events はサーバが元の記録（issue_events・lease_events・在席・コミット）と同じ取引で書く出来事 1 件 1 行。
-- 通し番号（seq）は timeline_post_events と同じ 1 本で、timeline_heads の行を取引の最後に 1 度だけロックして振る。
-- 1 行の文面は読む側で組み立てるので、ここには材料（detail の JSON）だけを持つ。
-- timeline_reads はセッションごとに「どの通し番号まで受け取ったか」。進むたびに行を足し、最大の値を読む。
-- どちらも追記専用（SELECT・INSERT のみ。deploy/grants.sql）。
-- 投稿の種類に update（近況）を足す。0017 の CHECK（chk_bulletins_kind）を外し、新しい名前で付け直す。
-- 通し番号の行が無いプロジェクトには 0 の行を作る（取引の中で無い行をロックしてギャップロックを取らないように）。
CREATE TABLE timeline_events (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id    BIGINT UNSIGNED NOT NULL,
  seq           BIGINT UNSIGNED NOT NULL COMMENT 'プロジェクトごとの通し番号（投稿の出来事と 1 本）',
  kind          VARCHAR(32)     NOT NULL COMMENT '出来事の種類',
  actor_user_id BIGINT UNSIGNED NOT NULL,
  actor_machine VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '行った端末のラベル（在席から引く。無ければ空）',
  actor_session VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '行った AI のセッション',
  issue_id      BIGINT UNSIGNED NULL COMMENT 'イシューの出来事の対象',
  detail        JSON            NOT NULL COMMENT '1 行の文面の材料',
  token_id      BIGINT UNSIGNED NULL,
  via           VARCHAR(16)     NOT NULL,
  created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  UNIQUE KEY uk_timeline_events_seq (project_id, seq),
  KEY k_timeline_events_created (project_id, created_at),
  CONSTRAINT fk_timeline_events_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_timeline_events_actor FOREIGN KEY (actor_user_id) REFERENCES users (id),
  CONSTRAINT fk_timeline_events_issue FOREIGN KEY (issue_id) REFERENCES issues (id),
  CONSTRAINT chk_timeline_events_kind CHECK (kind IN ('issue_create', 'issue_status', 'issue_assign', 'issue_comment',
    'lease_acquire', 'lease_release', 'lease_break', 'session_start', 'session_end', 'commit')),
  CONSTRAINT chk_timeline_events_via CHECK (via IN ('cli', 'web', 'mcp', 'api', 'admin'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE timeline_reads (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  project_id BIGINT UNSIGNED NOT NULL,
  user_id    BIGINT UNSIGNED NOT NULL,
  session_id VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '読んだセッション（無ければ空文字）',
  seq        BIGINT UNSIGNED NOT NULL COMMENT 'ここまで受け取った通し番号',
  created_at DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id),
  KEY k_timeline_reads_session (project_id, user_id, session_id, seq),
  CONSTRAINT fk_timeline_reads_project FOREIGN KEY (project_id) REFERENCES projects (id),
  CONSTRAINT fk_timeline_reads_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

ALTER TABLE timeline_posts DROP CHECK chk_bulletins_kind;
ALTER TABLE timeline_posts ADD CONSTRAINT chk_timeline_posts_kind CHECK (kind IN ('update', 'info', 'request', 'hold'));

INSERT INTO timeline_heads (project_id, seq, updated_at)
SELECT p.id, 0, CURRENT_TIMESTAMP(6) FROM projects p WHERE NOT EXISTS (SELECT 1 FROM timeline_heads h WHERE h.project_id = p.id);
