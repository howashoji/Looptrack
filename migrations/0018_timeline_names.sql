-- 0017 の AI 間連絡板の表をタイムラインの名前に改める（docs/server/DESIGN.md §9-9。振る舞いは変えない。行はそのまま残る）。
-- bulletins は timeline_posts、bulletin_events は timeline_post_events、bulletin_receipts は timeline_post_receipts、bulletin_heads は timeline_heads にする。
-- timeline_events は、あとでサーバが自動で載せる出来事の表のために空けておく（投稿の改訂・取り下げの記録は timeline_post_events）。
-- 子の表の bulletin_id は post_id に改める。外部キーは MySQL が新しい名前に付け替える。
-- 索引の名前も合わせる。外部キーと CHECK の制約の名前（fk_bulletin_*・chk_bulletin_*）は付け替えず、そのまま残す。
RENAME TABLE bulletins TO timeline_posts,
             bulletin_events TO timeline_post_events,
             bulletin_receipts TO timeline_post_receipts,
             bulletin_heads TO timeline_heads;

ALTER TABLE timeline_post_events RENAME COLUMN bulletin_id TO post_id;
ALTER TABLE timeline_post_receipts RENAME COLUMN bulletin_id TO post_id;

ALTER TABLE timeline_posts
  RENAME INDEX k_bulletins_project TO k_timeline_posts_project,
  RENAME INDEX k_bulletins_to_user TO k_timeline_posts_to_user,
  RENAME INDEX k_bulletins_sender TO k_timeline_posts_sender;
ALTER TABLE timeline_post_events
  RENAME INDEX uk_bulletin_events_seq TO uk_timeline_post_events_seq,
  RENAME INDEX uk_bulletin_events_version TO uk_timeline_post_events_version;
ALTER TABLE timeline_post_receipts
  RENAME INDEX k_bulletin_receipts_user TO k_timeline_post_receipts_user;
