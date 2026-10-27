-- 0010_transcript_locks.sql（MySQL）の SQLite 版。SQLite は書き込みのトランザクションを 1 つずつにするので、
-- FOR UPDATE（SQLite の方言では外す）が無くても順は決まる。行は MySQL と同じ手順で作って読む。
CREATE TABLE transcript_locks (
  user_id INTEGER NOT NULL PRIMARY KEY
);
