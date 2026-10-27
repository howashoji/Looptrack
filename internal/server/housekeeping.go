package server

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/howashoji/looptrack/internal/service"
)

// Housekeeping は期限切れのセッション・古いログイン試行の記録・使われなくなった MCP 接続の記録を定期的に消し、
// 保持期間（transcript_retention_days）が切れた会話記録の本体を消す（svc が nil なら会話記録は扱わない）。
func Housekeeping(ctx context.Context, db *sql.DB, logger *slog.Logger, svc *service.Service) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		housekeepOnce(ctx, db, logger, svc)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// housekeepOnce は Housekeeping の 1 回分（テストが直接呼ぶ）。
func housekeepOnce(ctx context.Context, db *sql.DB, logger *slog.Logger, svc *service.Service) {
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
	// 会話記録の置き場が使えないサーバ（未設定・添付の置き場と入れ子）では何もしない（失敗を毎時ログに出さない）
	if svc == nil || !svc.TranscriptsAvailable() {
		return
	}
	res, err := svc.PurgeExpiredTranscripts(ctx)
	if err != nil && ctx.Err() == nil {
		logger.Warn("housekeeping: transcript retention", "err", err)
	}
	if res != nil && len(res.Purged) > 0 {
		removed := 0
		for _, p := range res.Purged {
			removed += p.Removed
		}
		logger.Info("housekeeping: transcript retention", "retention_days", res.RetentionDays, "transcripts", len(res.Purged), "bodies_removed", removed)
	}
}

// Service はサーバが使う service（定期の処理 Housekeeping に渡す。置き場の設定は New が済ませてある）。
func (s *Server) Service() *service.Service { return s.svc }
