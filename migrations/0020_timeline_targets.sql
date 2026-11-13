-- タイムラインの依頼と待機の宛先に、イシュー・ブランチ・端末・自分以外を足す（docs/server/DESIGN.md §9-9「投稿の規則と実装」）。
-- 宛先のセッションは書く時点では決めず、読む時点で着手の記録（issue）と在席（branch・machine・others）から解決する。
-- to_issue_id は issue の宛先のイシュー、to_label は branch のブランチの名前か machine の端末のラベル（在席の値と同じ 64 文字まで）。
-- 0017 の CHECK（chk_bulletins_to_kind）を外し、新しい名前で付け直す。表は追記専用のまま（deploy/grants.sql は変えない）。
ALTER TABLE timeline_posts ADD COLUMN to_issue_id BIGINT UNSIGNED NULL COMMENT '宛先のイシュー（issue のときだけ。着手中のセッションに届く）' AFTER to_session;
ALTER TABLE timeline_posts ADD COLUMN to_label VARCHAR(64) NOT NULL DEFAULT '' COMMENT '宛先のブランチか端末のラベル（branch・machine のときだけ）' AFTER to_issue_id;
ALTER TABLE timeline_posts ADD CONSTRAINT fk_timeline_posts_to_issue FOREIGN KEY (to_issue_id) REFERENCES issues (id);
ALTER TABLE timeline_posts DROP CHECK chk_bulletins_to_kind;
ALTER TABLE timeline_posts ADD CONSTRAINT chk_timeline_posts_to_kind CHECK (to_kind IN ('project', 'user', 'session', 'issue', 'branch', 'machine', 'others'));
