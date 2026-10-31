-- 0012_memory_idempotency.sql（MySQL）の SQLite 版。
ALTER TABLE memory_versions ADD COLUMN idempotency_key TEXT NULL;
ALTER TABLE memory_versions ADD COLUMN written_at DATETIME NULL;
CREATE UNIQUE INDEX uk_memory_versions_idempotency ON memory_versions (memory_id, idempotency_key);
