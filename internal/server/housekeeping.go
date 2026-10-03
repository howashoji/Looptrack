package server

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// Housekeeping は期限切れのセッション・古いログイン試行の記録・使われなくなった MCP 接続の記録を定期的に消す。
func Housekeeping(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		housekeepOnce(ctx, db, logger)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// housekeepOnce は Housekeeping の 1 回分（テストが直接呼ぶ）。
func housekeepOnce(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	for _, q := range []string{
		// 「ログインしたままにする」のセッション（persistent）は無操作では消さず、期限切れだけで消す
		"DELETE FROM web_sessions WHERE expires_at < CURRENT_TIMESTAMP(6) OR (persistent = FALSE AND last_seen_at < CURRENT_TIMESTAMP(6) - INTERVAL 12 HOUR)",
		"DELETE FROM login_attempts WHERE at < CURRENT_TIMESTAMP(6) - INTERVAL 30 DAY",
		"DELETE FROM mcp_connections WHERE last_seen_at < CURRENT_TIMESTAMP(6) - INTERVAL 30 DAY",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil && ctx.Err() == nil {
			logger.Warn("housekeeping", "err", err)
		}
	}
}
