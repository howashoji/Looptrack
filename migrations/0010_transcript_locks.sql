-- 会話記録の書き込み（チャンクの追記と消去）を、利用者ごとに 1 つずつにするための行（1 利用者 1 行。docs/server/DESIGN.md §9-8）。
-- 追記はこの行を FOR UPDATE してから、連番・送った長さ・使用量を読み直して 1 行を足す。users の行はロックしない
-- （users の行を握ると、同じ利用者のログインの記録・言語の保存・トークンやセッションの発行まで、追記の間ずっと待たされる）。
-- users を参照する外部キーは置かない。InnoDB は子の行を足すたびに親の行へ共有ロックを掛けるので、
-- 外部キーがあると、この行を作るだけで同じ利用者の UPDATE users を待たせてしまう（利用者の行は消さないので、指す先が無くなることも無い）。
-- アプリ用の DB ユーザーには SELECT・INSERT・UPDATE を与える（MySQL の FOR UPDATE は UPDATE の権限を要る。値は書き換えない）。
CREATE TABLE transcript_locks (
  user_id BIGINT UNSIGNED NOT NULL COMMENT '会話記録の持ち主',
  PRIMARY KEY (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
