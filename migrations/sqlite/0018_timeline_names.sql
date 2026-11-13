-- 0018_timeline_names.sql（MySQL）の SQLite 版。表と列の名前を同じに改める（行はそのまま残る）。
-- 外部キーと索引の定義は SQLite が新しい名前に書き換える。索引は名前も合わせるために作り直す。
-- 外部キーと CHECK の制約の名前（fk_bulletin_*・chk_bulletin_*）は表の定義の中にあり、付け替えない。
-- 追記専用のトリガは表の名前を変えても残るので、新しい名前で作り直す。
DROP TRIGGER trg_bulletins_no_update;
DROP TRIGGER trg_bulletins_no_delete;
DROP TRIGGER trg_bulletin_events_no_update;
DROP TRIGGER trg_bulletin_events_no_delete;
DROP TRIGGER trg_bulletin_receipts_no_update;
DROP TRIGGER trg_bulletin_receipts_no_delete;

ALTER TABLE bulletins RENAME TO timeline_posts;
ALTER TABLE bulletin_events RENAME TO timeline_post_events;
ALTER TABLE bulletin_receipts RENAME TO timeline_post_receipts;
ALTER TABLE bulletin_heads RENAME TO timeline_heads;

ALTER TABLE timeline_post_events RENAME COLUMN bulletin_id TO post_id;
ALTER TABLE timeline_post_receipts RENAME COLUMN bulletin_id TO post_id;

DROP INDEX k_bulletins_project;
DROP INDEX k_bulletins_to_user;
DROP INDEX k_bulletins_sender;
DROP INDEX k_bulletin_receipts_user;
CREATE INDEX k_timeline_posts_project ON timeline_posts (project_id, id);
CREATE INDEX k_timeline_posts_to_user ON timeline_posts (project_id, to_user_id, id);
CREATE INDEX k_timeline_posts_sender ON timeline_posts (project_id, sender_user_id, id);
CREATE INDEX k_timeline_post_receipts_user ON timeline_post_receipts (user_id, session_id);

CREATE TRIGGER trg_timeline_posts_no_update BEFORE UPDATE ON timeline_posts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_posts は書き換えられません'); END;
CREATE TRIGGER trg_timeline_posts_no_delete BEFORE DELETE ON timeline_posts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_posts は削除できません'); END;
CREATE TRIGGER trg_timeline_post_events_no_update BEFORE UPDATE ON timeline_post_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_events は書き換えられません'); END;
CREATE TRIGGER trg_timeline_post_events_no_delete BEFORE DELETE ON timeline_post_events WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_events は削除できません'); END;
CREATE TRIGGER trg_timeline_post_receipts_no_update BEFORE UPDATE ON timeline_post_receipts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_receipts は書き換えられません'); END;
CREATE TRIGGER trg_timeline_post_receipts_no_delete BEFORE DELETE ON timeline_post_receipts WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: timeline_post_receipts は削除できません'); END;
