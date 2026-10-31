package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 会話記録の置き場のメタデータ（transcripts・transcript_chunks・transcript_purges。マイグレーション 0009。docs/server/DESIGN.md §2-5）。
// 3 つとも追記専用で、UPDATE・DELETE をしない。今の連番・ファイルごとの送った長さ・使用量は行に持たず、チャンクから数える。
// 見える範囲（持ち主だけ）・本体の置き場と書き込み・変換の検査は service が持つ。ここは持ち主で絞った読み書きだけを置く。

// 会話記録の agent・送った端末の OS・チャンクの種類と形・消去の理由（表の CHECK と同じ値）。
const (
	TranscriptAgentClaude = "claude-code"
	TranscriptAgentCodex  = "codex"

	TranscriptFileJSONL = "jsonl"
	TranscriptFileJSON  = "json"
	TranscriptFileText  = "text"

	TranscriptEncodingIdentity = "identity"
	TranscriptEncodingGzip     = "gzip"

	TranscriptPurgeOwner     = "owner"
	TranscriptPurgeRetention = "retention"
	TranscriptPurgeOperator  = "operator"
)

// Transcript は会話記録 1 つ（transcripts の 1 行）。
type Transcript struct {
	ID              int64
	UserID          int64
	ProjectID       int64
	Agent           string
	VendorSessionID string
	MachineLabel    string
	OriginOS        string
	AgentVersion    string
	CreatedAt       time.Time // 零なら DB の現在時刻
	TokenID         int64
	Via             string
}

const transcriptColumns = `t.id, t.user_id, t.project_id, t.agent, t.vendor_session_id, t.machine_label, t.origin_os, t.agent_version,
  t.created_at, COALESCE(t.token_id, 0), t.via`

func scanTranscript(row interface{ Scan(...any) error }, extra ...any) (Transcript, error) {
	var t Transcript
	dest := append([]any{&t.ID, &t.UserID, &t.ProjectID, &t.Agent, &t.VendorSessionID, &t.MachineLabel, &t.OriginOS, &t.AgentVersion,
		&t.CreatedAt, &t.TokenID, &t.Via}, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// InsertTranscript は会話記録を 1 行足し、その id を返す。同じ (user_id, agent, vendor_session_id) が既にあれば
// 一意性の違反（IsDuplicateKey）を返す（呼び出し元は TranscriptByVendor で既存の行を読む）。
func InsertTranscript(ctx context.Context, q execQuerier, t Transcript) (int64, error) {
	var at any
	if !t.CreatedAt.IsZero() {
		at = t.CreatedAt
	}
	res, err := q.ExecContext(ctx, `INSERT INTO transcripts (user_id, project_id, agent, vendor_session_id, machine_label, origin_os, agent_version, created_at, token_id, via)
VALUES (?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP(6)), ?, ?)`,
		t.UserID, t.ProjectID, t.Agent, t.VendorSessionID, truncate(t.MachineLabel, 64), t.OriginOS, truncate(t.AgentVersion, 64), at, nullID(t.TokenID), t.Via)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TranscriptByID は持ち主 userID の会話記録を 1 つ返す。無い・ほかの利用者のものは同じ ErrNotFound（持ち主にしか見せない）。
func TranscriptByID(ctx context.Context, q execQuerier, userID, id int64) (Transcript, error) {
	return scanTranscript(q.QueryRowContext(ctx, "SELECT "+transcriptColumns+" FROM transcripts t WHERE t.id = ? AND t.user_id = ?", id, userID))
}

// TranscriptByVendor は (利用者, agent, vendor_session_id) の会話記録を返す（無ければ ErrNotFound。登録の冪等のため）。
func TranscriptByVendor(ctx context.Context, q execQuerier, userID int64, agent, vendorSessionID string) (Transcript, error) {
	return scanTranscript(q.QueryRowContext(ctx, "SELECT "+transcriptColumns+` FROM transcripts t
WHERE t.user_id = ? AND t.agent = ? AND t.vendor_session_id = ?`, userID, agent, vendorSessionID))
}

// chunkPurgedExpr はチャンク c が消去済みかを表す式。消去はその時点の最大のチャンク（id が through_chunk_id 以下）にだけ効き、
// 消去の後に送り直したチャンクは消去済みと数えない。
const chunkPurgedExpr = `EXISTS (SELECT 1 FROM transcript_purges p WHERE p.transcript_id = c.transcript_id AND p.through_chunk_id >= c.id)`

// TranscriptSummary は一覧の 1 行。件数・大きさは消去していないチャンクだけを数える。
type TranscriptSummary struct {
	Transcript
	ProjectSlug string
	Seq         int64     // 今の連番（消去済みも含めた MAX(seq)。チャンクが無ければ 0）
	LiveChunks  int64     // 消去していないチャンクの数
	LiveFiles   int64     // 消去していないチャンクのファイル（file_key）の数
	LiveBytes   int64     // 消去していないチャンクの size の合計
	LastChunkAt time.Time // 最後のチャンクの時刻（消去済みも含める。チャンクが無ければ零）
	Purges      int64     // 消去の記録の数
}

// Purged は消去の記録があり、その後に送ったチャンクが無いか（一覧の「消去済み」）を返す。
func (s TranscriptSummary) Purged() bool { return s.Purges > 0 && s.LiveChunks == 0 }

// ListTranscripts は持ち主 userID の会話記録を新しい順に返す。projectID が 0 でなければそのプロジェクトで送ったものだけ。
func ListTranscripts(ctx context.Context, q execQuerier, userID, projectID int64) ([]TranscriptSummary, error) {
	query := "SELECT " + transcriptColumns + `, pr.slug,
  (SELECT COALESCE(MAX(c.seq), 0) FROM transcript_chunks c WHERE c.transcript_id = t.id),
  (SELECT COUNT(*) FROM transcript_chunks c WHERE c.transcript_id = t.id AND NOT ` + chunkPurgedExpr + `),
  (SELECT COUNT(DISTINCT c.file_key) FROM transcript_chunks c WHERE c.transcript_id = t.id AND NOT ` + chunkPurgedExpr + `),
  (SELECT COALESCE(SUM(c.size), 0) FROM transcript_chunks c WHERE c.transcript_id = t.id AND NOT ` + chunkPurgedExpr + `),
  (SELECT MAX(c.created_at) FROM transcript_chunks c WHERE c.transcript_id = t.id),
  (SELECT COUNT(*) FROM transcript_purges p WHERE p.transcript_id = t.id)
FROM transcripts t JOIN projects pr ON pr.id = t.project_id WHERE t.user_id = ?`
	args := []any{userID}
	if projectID != 0 {
		query += " AND t.project_id = ?"
		args = append(args, projectID)
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY t.id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptSummary
	for rows.Next() {
		var s TranscriptSummary
		var last sql.NullTime
		t, err := scanTranscript(rows, &s.ProjectSlug, &s.Seq, &s.LiveChunks, &s.LiveFiles, &s.LiveBytes, &last, &s.Purges)
		if err != nil {
			return nil, err
		}
		s.Transcript = t
		if last.Valid {
			s.LastChunkAt = last.Time
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TranscriptChunk はチャンク 1 つ（transcript_chunks の 1 行）。Placeholders・Stats は JSON の本文（空なら {} / [] を入れる）。
// Purged は読み出しのときだけ埋まる。
type TranscriptChunk struct {
	ID           int64
	TranscriptID int64
	Seq          int64
	FileKey      string
	FileKind     string
	SrcOffset    int64
	SrcLength    int64
	LineCount    int64
	SHA256       string
	Size         int64
	Encoding     string
	Nonce        string
	Placeholders string
	Stats        string
	CreatedAt    time.Time // 零なら DB の現在時刻
	TokenID      int64
	Via          string
	Purged       bool
}

const chunkColumns = `c.id, c.transcript_id, c.seq, c.file_key, c.file_kind, c.src_offset, c.src_length, c.line_count, c.sha256, c.size,
  c.encoding, c.nonce, c.placeholders, c.stats, c.created_at, COALESCE(c.token_id, 0), c.via, ` + chunkPurgedExpr

func scanChunk(row interface{ Scan(...any) error }) (TranscriptChunk, error) {
	var c TranscriptChunk
	err := row.Scan(&c.ID, &c.TranscriptID, &c.Seq, &c.FileKey, &c.FileKind, &c.SrcOffset, &c.SrcLength, &c.LineCount, &c.SHA256, &c.Size,
		&c.Encoding, &c.Nonce, &c.Placeholders, &c.Stats, &c.CreatedAt, &c.TokenID, &c.Via, &c.Purged)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// InsertTranscriptChunk はチャンクを 1 行足し、その id を返す。同じ (transcript_id, seq) が既にあれば一意性の違反
// （IsDuplicateKey）を返す。楽観ロックはこの一意性で取る（先に入れたほうが勝ち、後のほうは今の連番を読み直す）。
func InsertTranscriptChunk(ctx context.Context, q execQuerier, c TranscriptChunk) (int64, error) {
	var at any
	if !c.CreatedAt.IsZero() {
		at = c.CreatedAt
	}
	placeholders, stats := c.Placeholders, c.Stats
	if placeholders == "" {
		placeholders = "[]"
	}
	if stats == "" {
		stats = "{}"
	}
	res, err := q.ExecContext(ctx, `INSERT INTO transcript_chunks (transcript_id, seq, file_key, file_kind, src_offset, src_length, line_count, sha256, size,
  encoding, nonce, placeholders, stats, created_at, token_id, via)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP(6)), ?, ?)`,
		c.TranscriptID, c.Seq, c.FileKey, c.FileKind, c.SrcOffset, c.SrcLength, c.LineCount, c.SHA256, c.Size,
		c.Encoding, c.Nonce, placeholders, stats, at, nullID(c.TokenID), c.Via)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TranscriptChunks は会話記録のチャンクを seq の順に返す（消去済みも含む）。持ち主の確かめは呼び出し元（TranscriptByID）。
func TranscriptChunks(ctx context.Context, q execQuerier, transcriptID int64) ([]TranscriptChunk, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+chunkColumns+" FROM transcript_chunks c WHERE c.transcript_id = ? ORDER BY c.seq", transcriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptChunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TranscriptChunkBySeq は会話記録の seq 番目のチャンクを返す（無ければ ErrNotFound）。
func TranscriptChunkBySeq(ctx context.Context, q execQuerier, transcriptID, seq int64) (TranscriptChunk, error) {
	return scanChunk(q.QueryRowContext(ctx, "SELECT "+chunkColumns+" FROM transcript_chunks c WHERE c.transcript_id = ? AND c.seq = ?", transcriptID, seq))
}

// TranscriptSeq は会話記録の今の連番（消去済みも含めた MAX(seq)。チャンクが無ければ 0）を返す。新しいチャンクは seq + 1。
func TranscriptSeq(ctx context.Context, q execQuerier, transcriptID int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(c.seq), 0) FROM transcript_chunks c WHERE c.transcript_id = ?", transcriptID).Scan(&n)
	return n, err
}

// TranscriptFileLengths はファイル（file_key）ごとの送った長さ（消去していないチャンクの src_offset + src_length の最大）を返す。
// 消去の後は、送り直したチャンクだけを数える（消去したファイルは最初から送れる）。消去していないチャンクの無いファイルは入らない。
func TranscriptFileLengths(ctx context.Context, q execQuerier, transcriptID int64) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.file_key, MAX(c.src_offset + c.src_length) FROM transcript_chunks c
WHERE c.transcript_id = ? AND NOT `+chunkPurgedExpr+` GROUP BY c.file_key`, transcriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var key string
		var n int64
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		out[key] = n
	}
	return out, rows.Err()
}

// TranscriptUserBytes は利用者 userID の、消去していないチャンクの size の合計を返す（1 利用者の上限の判定に使う）。
func TranscriptUserBytes(ctx context.Context, q execQuerier, userID int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(c.size), 0) FROM transcript_chunks c JOIN transcripts t ON t.id = c.transcript_id
WHERE t.user_id = ? AND NOT `+chunkPurgedExpr, userID).Scan(&n)
	return n, err
}

// TranscriptUsage は利用者 1 人の会話記録の使用量（管理者の画面に出すのは数だけ。中身・題名・ファイル名は出さない）。
type TranscriptUsage struct {
	Transcripts int64 // 会話記録の数（消去済みも含む）
	Bytes       int64 // 消去していないチャンクの size の合計
}

// TranscriptUsageByUser は会話記録の使用量を利用者の id ごとに返す（会話記録が 1 つも無い利用者は入らない）。
func TranscriptUsageByUser(ctx context.Context, q execQuerier) (map[int64]TranscriptUsage, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.user_id, COUNT(DISTINCT t.id),
  COALESCE(SUM(CASE WHEN c.id IS NULL OR `+chunkPurgedExpr+` THEN 0 ELSE c.size END), 0)
FROM transcripts t LEFT JOIN transcript_chunks c ON c.transcript_id = t.id GROUP BY t.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]TranscriptUsage{}
	for rows.Next() {
		var id int64
		var u TranscriptUsage
		if err := rows.Scan(&id, &u.Transcripts, &u.Bytes); err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

// LiveTranscriptChunkCount は本体 sha256 を指す、消去していないチャンクの数を返す（ほかの記録が指す本体を消さないため）。
func LiveTranscriptChunkCount(ctx context.Context, q execQuerier, sha256 string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM transcript_chunks c WHERE c.sha256 = ? AND NOT "+chunkPurgedExpr, sha256).Scan(&n)
	return n, err
}

// EnsureTranscriptLock は利用者 userID のロック用の行（transcript_locks）が無ければ作る。トランザクションの外（その文だけで確定する接続）で呼ぶ。
// トランザクションの中で「無ければ足す」と、既にある行への INSERT IGNORE が共有ロックを取り、続く FOR UPDATE で
// 2 つのトランザクションが互いを待つ（デッドロック）ので、作るのと握るのを分ける。
func EnsureTranscriptLock(ctx context.Context, q execQuerier, userID int64) error {
	_, err := q.ExecContext(ctx, "INSERT IGNORE INTO transcript_locks (user_id) VALUES (?)", userID)
	return err
}

// LockTranscriptUser は利用者 userID の会話記録の書き込み（追記と消去）を 1 つずつにする行ロックを取る（EnsureTranscriptLock の後に、
// トランザクションの最初の文として呼ぶ。MySQL の REPEATABLE READ は最初の非ロックの読みで版を決めるので、読み直しはこの後に行う）。
// users の行は握らない（同じ利用者のほかの書き込みを待たせないため）。行が無ければ ErrNotFound。
func LockTranscriptUser(ctx context.Context, tx execQuerier, userID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT user_id FROM transcript_locks WHERE user_id = ? FOR UPDATE", userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// TranscriptPurge は消去の記録 1 件（transcript_purges）。
type TranscriptPurge struct {
	TranscriptID   int64
	ThroughChunkID int64  // この消去が効くチャンクの id の上限（消去の時点でその記録の最大の id）
	Cause          string // owner / retention / operator
	ActorUserID    int64  // 保持期間の切れは 0（NULL）
	Via            string // 保持期間の切れは system
	Reason         string
	At             time.Time // 零なら DB の現在時刻
}

// InsertTranscriptPurge は消去の記録を 1 行足す（追記のみ）。
func InsertTranscriptPurge(ctx context.Context, q execQuerier, p TranscriptPurge) error {
	var at any
	if !p.At.IsZero() {
		at = p.At
	}
	_, err := q.ExecContext(ctx, `INSERT INTO transcript_purges (transcript_id, through_chunk_id, cause, at, actor_user_id, via, reason)
VALUES (?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP(6)), ?, ?, ?)`,
		p.TranscriptID, p.ThroughChunkID, p.Cause, at, nullID(p.ActorUserID), p.Via, truncate(p.Reason, 255))
	return err
}

// 会話記録の上限と保持期間（system_settings）。全利用者に共通の 1 値で、値は 10 進の整数。
const (
	SettingTranscriptMaxChunk      = "transcript_max_chunk_bytes" // 1 チャンクの伸ばした後の大きさの上限
	SettingTranscriptMaxUser       = "transcript_max_user_bytes"  // 1 利用者の、消去していないチャンクの size の合計の上限
	SettingTranscriptRetentionDays = "transcript_retention_days"  // 最後のチャンクからこの日数で本体を消す（0 は無期限）
)

// 上限と保持期間の既定（行が無い・値が壊れているとき）。
const (
	DefaultTranscriptMaxChunk      int64 = 32 << 20 // 32MiB
	DefaultTranscriptMaxUser       int64 = 2 << 30  // 2GiB
	DefaultTranscriptRetentionDays int64 = 90
)

// TranscriptLimits は会話記録の上限と保持期間。
type TranscriptLimits struct {
	MaxChunk      int64
	MaxUser       int64
	RetentionDays int64 // 0 は無期限
}

// ValidTranscriptLimitName は会話記録の上限・保持期間の設定名かを返す。
func ValidTranscriptLimitName(name string) bool {
	return name == SettingTranscriptMaxChunk || name == SettingTranscriptMaxUser || name == SettingTranscriptRetentionDays
}

// validTranscriptLimit は設定 name の値として v を受け付けるかを返す（大きさは 1 以上、保持日数は 0 から MaxRetentionDays）。
func validTranscriptLimit(name string, v int64) bool {
	if name == SettingTranscriptRetentionDays {
		return v >= 0 && v <= MaxRetentionDays
	}
	return v > 0
}

// ReadTranscriptLimits は会話記録の上限と保持期間を読む。行が無い・整数として読めない・範囲の外の値は既定にする。
// 要求ごとに呼ぶ（設定を変えたら、再起動せずに次の要求から効く）。
func ReadTranscriptLimits(ctx context.Context, q execQuerier) (TranscriptLimits, error) {
	l := TranscriptLimits{MaxChunk: DefaultTranscriptMaxChunk, MaxUser: DefaultTranscriptMaxUser, RetentionDays: DefaultTranscriptRetentionDays}
	for _, s := range []struct {
		name string
		v    *int64
	}{{SettingTranscriptMaxChunk, &l.MaxChunk}, {SettingTranscriptMaxUser, &l.MaxUser}, {SettingTranscriptRetentionDays, &l.RetentionDays}} {
		raw, ok, err := GetSetting(ctx, q, s.name)
		if err != nil {
			return l, err
		}
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && validTranscriptLimit(s.name, n) {
			*s.v = n
		}
	}
	return l, nil
}

// SetTranscriptLimit は会話記録の上限・保持期間を変え、変更を setting_changes に記録する（同じ値なら何もしない。SetAttachLimit と同じ形）。
// ch の OldValue / NewValue / Name は無視する（ここで決める）。old は変更前の値（空は未設定）。
func SetTranscriptLimit(ctx context.Context, db *sql.DB, name string, value int64, ch SettingChange) (old string, changed bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck
	old, changed, err = SetTranscriptLimitTx(ctx, tx, name, value, ch)
	if err != nil || !changed {
		return old, false, err
	}
	if err := tx.Commit(); err != nil {
		return old, false, err
	}
	return old, true, nil
}

// SetTranscriptLimitTx は SetTranscriptLimit を呼び出し元のトランザクションの中で行う（コミットは呼び出し元）。
func SetTranscriptLimitTx(ctx context.Context, tx Queryer, name string, value int64, ch SettingChange) (old string, changed bool, err error) {
	if !ValidTranscriptLimitName(name) {
		return "", false, i18n.Errorf("store.err.transcript_limit.name", "name", strconv.Quote(name))
	}
	if !validTranscriptLimit(name, value) {
		if name == SettingTranscriptRetentionDays {
			return "", false, i18n.Errorf("store.err.transcript_limit.retention", "value", value)
		}
		return "", false, i18n.Errorf("store.err.transcript_limit.invalid", "value", value)
	}
	return setSettingValueTx(ctx, tx, name, strconv.FormatInt(value, 10), ch)
}

// LiveTranscriptChunkRef は消去していないチャンクが指す本体（整合の検査が使う。中身・ファイル名は持たない）。
type LiveTranscriptChunkRef struct {
	ChunkID      int64
	TranscriptID int64
	Seq          int64
	SHA256       string
}

// LiveTranscriptChunkRefs は消去していないチャンクすべての本体を id の順に返す（運用者の整合の検査。持ち主で絞らない）。
func LiveTranscriptChunkRefs(ctx context.Context, q execQuerier) ([]LiveTranscriptChunkRef, error) {
	rows, err := q.QueryContext(ctx, "SELECT c.id, c.transcript_id, c.seq, c.sha256 FROM transcript_chunks c WHERE NOT "+chunkPurgedExpr+" ORDER BY c.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveTranscriptChunkRef
	for rows.Next() {
		var r LiveTranscriptChunkRef
		if err := rows.Scan(&r.ChunkID, &r.TranscriptID, &r.Seq, &r.SHA256); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TranscriptLastChunk は消去していないチャンクを持つ会話記録 1 つと、その最後のチャンクの時刻（保持期間の判定に使う）。
type TranscriptLastChunk struct {
	TranscriptID int64
	UserID       int64
	LastChunkAt  time.Time // 消去済みも含めた最後のチャンクの時刻
}

// TranscriptsWithLiveChunks は消去していないチャンクを持つ会話記録すべてを id の順に返す（保持期間の切れの判定。持ち主で絞らない）。
// 日数の比べは呼び出し元が Go の時刻で行う（DB の方言ごとの日付の比べを書かないため）。
func TranscriptsWithLiveChunks(ctx context.Context, q execQuerier) ([]TranscriptLastChunk, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.user_id, (SELECT MAX(c2.created_at) FROM transcript_chunks c2 WHERE c2.transcript_id = t.id)
FROM transcripts t WHERE EXISTS (SELECT 1 FROM transcript_chunks c WHERE c.transcript_id = t.id AND NOT `+chunkPurgedExpr+`) ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptLastChunk
	for rows.Next() {
		var r TranscriptLastChunk
		var last sql.NullTime
		if err := rows.Scan(&r.TranscriptID, &r.UserID, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			r.LastChunkAt = last.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TranscriptByIDAnyUser は会話記録を持ち主で絞らずに返す（保持期間の切れ・運用者の消去だけが使う。利用者の要求の経路では使わない）。
func TranscriptByIDAnyUser(ctx context.Context, q execQuerier, id int64) (Transcript, error) {
	return scanTranscript(q.QueryRowContext(ctx, "SELECT "+transcriptColumns+" FROM transcripts t WHERE t.id = ?", id))
}
