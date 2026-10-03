-- ログイン画面の「ログインしたままにする」で発行したセッションか。
-- TRUE のセッションは無操作の切れ（12 時間）を見ず、期限（expires_at）を最終アクセスから 400 日に延ばし続ける。
-- FALSE（既定）は従来どおり、発行から 7 日・無操作 12 時間で切れる。
ALTER TABLE web_sessions
  ADD COLUMN persistent BOOLEAN NOT NULL DEFAULT FALSE COMMENT '「ログインしたままにする」で発行したセッション';
