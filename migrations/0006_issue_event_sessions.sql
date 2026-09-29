-- MCP の操作（issue_events）を、後から届いたトークン情報のスナップショットの会話（session_id）に結ぶ記録（追記専用）。
-- MCP の接続はクライアントが名乗るセッション ID を送れないことが多く、issue_events.session_id は接続 ID（mcp-conn:…）か空になる。
-- ツール呼び出しの ID（ハッシュ。issue_events.detail の "tool_use"）が同じスナップショットが同じ利用者から届いたら、
-- そのスナップショットの session_id をここに残す。読む側（着手したセッションの判定）は、この値で session_id を差し替えるだけで、
-- 操作の利用者・トークン・権限は変えない。issue_events は書き換えない（追記専用のまま）。
CREATE TABLE issue_event_sessions (
  event_id    BIGINT UNSIGNED NOT NULL,
  user_id     BIGINT UNSIGNED NOT NULL COMMENT '結んだスナップショットを送った利用者（イベントの actor_user_id と同じ）',
  session_id  VARCHAR(128)    NOT NULL COMMENT 'スナップショットの session_id',
  snapshot_id BIGINT UNSIGNED NOT NULL,
  linked_at   DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (event_id),
  KEY k_issue_event_sessions_snapshot (snapshot_id),
  CONSTRAINT fk_issue_event_sessions_event FOREIGN KEY (event_id) REFERENCES issue_events (id),
  CONSTRAINT fk_issue_event_sessions_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_issue_event_sessions_snapshot FOREIGN KEY (snapshot_id) REFERENCES usage_snapshots (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
