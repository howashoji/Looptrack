-- アプリ用 DB ユーザー im_app の権限（共通 MySQL に root で流す）。
-- ユーザー自体はデプロイ手順で作る（パスワードをファイルに書かないため、このファイルは権限の付与だけ）:
--   CREATE USER 'im_app'@'%' IDENTIFIED BY '<サーバ上で生成した値>';
-- DB 名 im 以外に流すとき（統合テスト等）は、流す側で "im." を置き換える。
--
-- 方針:
--   - DB 単位ではなくテーブル単位で付与する（MySQL は DB 単位の付与からテーブル単位で取り消せないため）。
--   - comments / issue_events / usage_snapshots / usage_reports / usage_report_requests / setting_changes は SELECT・INSERT のみ（追記専用。書き換え・削除させない）。
--   - projects / issues は DELETE を与えない（削除の経路を持たない）。
--   - スキーマ変更（migrate）と取り込み（import）は管理用の資格情報で行う。
-- テーブルを追加したら、このファイルにも追記する。

GRANT SELECT                         ON im.schema_migrations TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.projects          TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.issues            TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON im.issue_values      TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON im.issue_extra       TO 'im_app'@'%';
GRANT SELECT, INSERT                 ON im.comments          TO 'im_app'@'%';
GRANT SELECT, INSERT                 ON im.issue_events      TO 'im_app'@'%';
GRANT SELECT, INSERT                 ON im.usage_snapshots   TO 'im_app'@'%';
GRANT SELECT, INSERT                 ON im.usage_reports     TO 'im_app'@'%';
GRANT SELECT, INSERT                 ON im.usage_report_requests TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.users             TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON im.project_members   TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.api_tokens        TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON im.web_sessions      TO 'im_app'@'%';
GRANT SELECT, INSERT, DELETE         ON im.login_attempts    TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.oauth_clients     TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.oauth_codes       TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.oauth_refresh_tokens TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON im.project_guides    TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON im.mcp_connections   TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.agent_installs    TO 'im_app'@'%';
GRANT SELECT, INSERT, UPDATE         ON im.system_settings   TO 'im_app'@'%';
GRANT SELECT, INSERT                 ON im.setting_changes   TO 'im_app'@'%';
