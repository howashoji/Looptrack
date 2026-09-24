package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// MCP 接続の記録と導入済み通知（設計は DESIGN.md §5-6）。

// MCPConnection は MCP の initialize で受け取った接続の情報。
type MCPConnection struct {
	ID              string
	UserID          int64
	TokenID         int64 // 0 なら NULL
	ClientName      string
	ClientVersion   string
	Agent           string
	ProtocolVersion string
	Project         string
	UserAgent       string
	CreatedAt       time.Time
	LastSeenAt      time.Time
	Closed          bool
}

// InsertMCPConnection は接続を 1 行記録する。
func InsertMCPConnection(ctx context.Context, q execQuerier, c MCPConnection) error {
	_, err := q.ExecContext(ctx, `INSERT INTO mcp_connections (id, user_id, token_id, client_name, client_version, agent, protocol_version, project, user_agent)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, c.UserID, nullInt(c.TokenID), truncate(c.ClientName, 128), truncate(c.ClientVersion, 64),
		c.Agent, truncate(c.ProtocolVersion, 32), truncate(c.Project, 64), truncate(c.UserAgent, 255))
	return err
}

// MCPConnectionByID は利用者の接続を引く。他の利用者の ID や未知の ID は ErrNotFound。
func MCPConnectionByID(ctx context.Context, q execQuerier, id string, userID int64) (MCPConnection, error) {
	var c MCPConnection
	var tok sql.NullInt64
	var closed sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT id, user_id, token_id, client_name, client_version, agent, protocol_version, project, user_agent,
  created_at, last_seen_at, closed_at FROM mcp_connections WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&c.ID, &c.UserID, &tok, &c.ClientName, &c.ClientVersion, &c.Agent, &c.ProtocolVersion, &c.Project, &c.UserAgent,
			&c.CreatedAt, &c.LastSeenAt, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.TokenID, c.Closed = tok.Int64, closed.Valid
	return c, err
}

// TouchMCPConnection は最終利用時刻を進める（1 分に 1 回まで。要求ごとに書かない）。
func TouchMCPConnection(ctx context.Context, q execQuerier, id string, userID int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE mcp_connections SET last_seen_at = ? WHERE id = ? AND user_id = ? AND last_seen_at < ?`,
		now.UTC(), id, userID, now.UTC().Add(-time.Minute))
	return err
}

// CloseMCPConnection は接続の終了（DELETE）を記録する。
func CloseMCPConnection(ctx context.Context, q execQuerier, id string, userID int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE mcp_connections SET closed_at = ? WHERE id = ? AND user_id = ? AND closed_at IS NULL`, now.UTC(), id, userID)
	return err
}

// AgentInstall は導入済み通知（利用者 × プロジェクト × AI の種類で最後の 1 件）。
type AgentInstall struct {
	UserID        int64
	ProjectID     int64
	Agent         string
	Source        string
	Files         map[string]string // 名前 → SHA-256
	BundleSHA256  string
	ClientVersion string
	Host          string
	Workspace     string
	// SelfRepo は looptrack 自身のリポジトリ（kit の正本）からの通知（マイグレーション 0002）。
	// 立っている導入では kit を配布物と比べない
	SelfRepo   bool
	FirstAt    time.Time
	ReportedAt time.Time
	HookAt     *time.Time // フックからの通知が無ければ nil
	// 導入セット（DESIGN.md §5-7）。.claude/.looptrack-kit.json の控え。空は通知に無い（古い CLI）
	CoreBundle  string // core.bundle_sha256
	LoopState   string // installed / declined / none / ""
	LoopBundle  string // loop が installed のときの bundle_sha256
	LoopVersion string
	// 実行ファイルの OS・CPU（マイグレーション 0013）。空は以前の CLI（配布スクリプトのハッシュで判定する）
	ClientOS   string
	ClientArch string
}

// UpsertAgentInstall は通知を記録する。hook なら hook_at も進める（手動の通知は hook_at を変えない）。
func UpsertAgentInstall(ctx context.Context, q execQuerier, a AgentInstall, hook bool, now time.Time) error {
	files, err := json.Marshal(a.Files)
	if err != nil {
		return err
	}
	var hookAt any
	if hook {
		hookAt = now.UTC()
	}
	_, err = q.ExecContext(ctx, `INSERT INTO agent_installs (user_id, project_id, agent, source, files, bundle_sha256, client_version, host, workspace, self_repo,
  first_at, reported_at, hook_at, core_bundle_sha256, loop_state, loop_bundle_sha256, loop_version, client_os, client_arch)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE source = VALUES(source), files = VALUES(files), bundle_sha256 = VALUES(bundle_sha256),
  client_version = VALUES(client_version), host = VALUES(host), workspace = VALUES(workspace), self_repo = VALUES(self_repo), reported_at = VALUES(reported_at),
  hook_at = COALESCE(VALUES(hook_at), hook_at), core_bundle_sha256 = VALUES(core_bundle_sha256), loop_state = VALUES(loop_state),
  loop_bundle_sha256 = VALUES(loop_bundle_sha256), loop_version = VALUES(loop_version),
  client_os = VALUES(client_os), client_arch = VALUES(client_arch)`,
		a.UserID, a.ProjectID, a.Agent, truncate(a.Source, 16), string(files), a.BundleSHA256, truncate(a.ClientVersion, 64),
		truncate(a.Host, 255), truncate(a.Workspace, 255), a.SelfRepo, now.UTC(), now.UTC(), hookAt,
		nullString(a.CoreBundle), truncate(a.LoopState, 16), nullString(a.LoopBundle), truncate(a.LoopVersion, 64),
		truncate(a.ClientOS, 16), truncate(a.ClientArch, 16))
	return err
}

// AgentInstalls は利用者のそのプロジェクトへの導入（AI の種類ごと）を返す。
func AgentInstalls(ctx context.Context, q execQuerier, userID, projectID int64) ([]AgentInstall, error) {
	rows, err := q.QueryContext(ctx, `SELECT user_id, project_id, agent, source, files, bundle_sha256, client_version, host, workspace, self_repo,
  first_at, reported_at, hook_at, core_bundle_sha256, loop_state, loop_bundle_sha256, loop_version, client_os, client_arch FROM agent_installs WHERE user_id = ? AND project_id = ? ORDER BY agent`, userID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentInstall
	for rows.Next() {
		var a AgentInstall
		var files []byte
		var hookAt sql.NullTime
		var core, loopBundle sql.NullString
		if err := rows.Scan(&a.UserID, &a.ProjectID, &a.Agent, &a.Source, &files, &a.BundleSHA256, &a.ClientVersion, &a.Host, &a.Workspace, &a.SelfRepo,
			&a.FirstAt, &a.ReportedAt, &hookAt, &core, &a.LoopState, &loopBundle, &a.LoopVersion, &a.ClientOS, &a.ClientArch); err != nil {
			return nil, err
		}
		a.CoreBundle, a.LoopBundle = core.String, loopBundle.String
		if err := json.Unmarshal(files, &a.Files); err != nil {
			return nil, err
		}
		if hookAt.Valid {
			t := hookAt.Time
			a.HookAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// nullString は空文字を NULL にする。
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
