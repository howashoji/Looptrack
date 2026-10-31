-- 記憶の版の冪等の鍵と書いた時刻（docs/server/DESIGN.md §9-9）。
-- クライアントが作る鍵を版に残し、同じ鍵の再送は新しい版を作らずにその鍵の版を返す。鍵の無い版は NULL で、一意の制約に掛からない。
-- written_at はクライアントが本文を作った時刻（退避した本文は退避の作成時刻）。無ければ NULL で、created_at を使う。
ALTER TABLE memory_versions
  ADD COLUMN idempotency_key VARCHAR(64) NULL COMMENT '追記の冪等の鍵（クライアントが作る。無ければ NULL）',
  ADD COLUMN written_at DATETIME(6) NULL COMMENT '本文を書いた時刻（クライアントの申告。未来は今に丸める。無ければ NULL）',
  ADD UNIQUE KEY uk_memory_versions_idempotency (memory_id, idempotency_key);
