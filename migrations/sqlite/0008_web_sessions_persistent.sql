-- 0008_web_sessions_persistent.sql（MySQL）の SQLite 版。
ALTER TABLE web_sessions ADD COLUMN persistent INTEGER NOT NULL DEFAULT 0;
