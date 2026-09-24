-- 0002_agent_installs_self_repo.sql（MySQL）の SQLite 版。ALTER TABLE は 1 列ずつ。
ALTER TABLE agent_installs ADD COLUMN self_repo INTEGER NOT NULL DEFAULT 0;
