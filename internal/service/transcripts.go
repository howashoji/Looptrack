package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 会話記録（利用者ごとの置き場。docs/server/DESIGN.md §2-5・§9-8）の規則はこのファイルの 1 か所に置く。
// REST・CLI（管理用のサブコマンド）・定期の処理はここを呼ぶだけで、権限・連番・上限・中身の検査をハンドラごとに書かない。
//
// 見える人: 持ち主だけ。ほかの利用者（同じプロジェクトの管理者・利用者の役割 admin も）には、無いものと同じ not_found を返す
// （store.TranscriptByID が持ち主で絞る。持ち主で絞らない読み出しは保持期間の切れと運用者の消去だけが使う）。
//
// 追記: If-Match の連番（MAX(seq)）が今と同じときだけ seq + 1 を足す。取り合いの最後の砦は (transcript_id, seq) の一意性。
// アプリ用の DB 利用者は追記専用の表に行ロックを取れない（UPDATE の権限が無い）ので、ロックのためだけの行（transcript_locks。
// 1 利用者 1 行）を FOR UPDATE して、同じ利用者の追記と消去を 1 つずつにする。使用量は利用者の全部の会話記録の合計なので、
// 会話記録ごとではなく利用者ごとに並べないと、別々の会話記録への同時の追記が互いの未確定の行を見ずに、合わせて上限を超える。
// users の行は握らない（握ると、同じ利用者のログインの記録・言語の保存・トークンやセッションの発行が追記の間ずっと待つ）。
// 本体の rename と fsync はトランザクションの前に済ませ、ロックの間は読み直しと 1 行の記録だけにする。記録に失敗したら、
// どこからも指されない本体を消す（消せなければ repair-transcripts が拾う）。
// 読み直しはロックの後に行う（MySQL の REPEATABLE READ は最初の非ロックの読みで版を決めるので、ロックより前に読むと古い連番を見る）。
// プロセスの中の排他（transcriptBodies）は本体ごとで、置いてから記録が確定するまでの間に、同じ本体を消去が「どこからも指されない」と
// 数えて消さないためだけにある。同じ内容の本体は利用者をまたいで 1 つにまとまるので、利用者ごとの排他では足りない。
// 別のプロセス（2 つ目のサーバ・purge-transcripts・repair-transcripts）にはこの排他も利用者ごとのロックの行も届かないので、
// 置き場の中の印で順を決める（blobstore.go の removeBlobGuarded）。本体を消すのは removeUnreferencedTranscriptBody と
// RemoveTranscriptOrphans だけで、どちらもそのやり方を通る。
//
// 中身の検査: 伸ばした大きさが transcript_max_chunk_bytes 以内・jsonl は全行が JSON・json は全体が JSON・
// 文字列の値ごとと、キーごとに domain.FindSecret が当たらない・text は全体に当たらない。行全体には当てない
// （値ごとにマスクした行でも、行全体に当てると \S+ が引用符を越えて当たる）。外れたら本体（一時ファイル）もメタデータも残さない。
// 拒否の文面は件数・ファイル・行番号・規則の語だけで、値は入れない。
//
// 消去: 本体のファイルを消し、transcript_purges に 1 行を足す（その時点の最大のチャンクまでが消去済み）。同じ本体を指す、
// 消去していないチャンクか添付が残っていれば本体は消さない。消去の後は送った長さが 0 に戻り、同じ会話を最初から送り直せる。

// transcriptVendorIDRe は vendor_session_id の形（Claude の sessionId・Codex のスレッド ID。file_key の一部になるので、パスに使える字だけ）。
var transcriptVendorIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// transcriptNameRe は file_key の最後の名前の形（隠しファイルは受け付けない）。
const transcriptNameRe = `[A-Za-z0-9][A-Za-z0-9._-]*`

// transcriptCodexKeyRe は Codex の file_key の形（sessions/ からの相対。<年>/<月>/<日>/rollout-….jsonl）。
var transcriptCodexKeyRe = regexp.MustCompile(`^[0-9]{4}/[0-9]{2}/[0-9]{2}/rollout-` + transcriptNameRe + `\.jsonl$`)

// transcriptNonceRe は nonce の形（text のチャンクは空）。
var transcriptNonceRe = regexp.MustCompile(`^[0-9A-Za-z]{0,32}$`)

// transcriptPlaceholderRe はプレースホルダの種類の名前（ROOT0・SDIR・VDIR・HOME・ROOT0J など）。
var transcriptPlaceholderRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,15}$`)

// 入力の上限。
const (
	maxTranscriptFileKey      = 255       // file_key のバイト数（列の幅）
	maxTranscriptLabel        = 64        // machine_label・agent_version の文字数（列の幅）
	maxTranscriptPlaceholders = 64        // placeholders の数
	maxTranscriptStats        = 16 * 1024 // stats の JSON のバイト数
	maxTranscriptReportLines  = 5         // 拒否の文面に出す行番号の数
)

// TranscriptAgents は会話記録の agent に書ける値。
var TranscriptAgents = []string{store.TranscriptAgentClaude, store.TranscriptAgentCodex}

// TranscriptOriginOS は送った端末の OS に書ける値。
var TranscriptOriginOS = []string{"linux", "darwin", "windows"}

// TranscriptFileLength はファイル（file_key）ごとの送った長さ（次のチャンクの src_offset）。
type TranscriptFileLength struct {
	FileKey string `json:"file_key"`
	Length  int64  `json:"length"`
}

// TranscriptState は登録・追記の応答に載せる、会話記録の今の状態。
type TranscriptState struct {
	Transcript  store.Transcript
	ProjectSlug string
	// Created は登録で新しく作ったか（REST は 201。既にあれば false で 200）
	Created bool
	// Seq は今の連番（次の追記の If-Match。チャンクが無ければ 0）
	Seq int64
	// Files はファイルごとの送った長さ（file_key の順。消去の後に送り直していないファイルは載らない＝0 から送る）
	Files []TranscriptFileLength
	// TranscriptsEnabled は記録を送ったプロジェクトのルール session.transcripts（クライアントが覚える）
	TranscriptsEnabled bool
}

// RegisterTranscriptInput は会話記録の登録の入力（POST /projects/{slug}/transcripts）。
type RegisterTranscriptInput struct {
	Project         string // 送るプロジェクトの slug
	Agent           string // claude-code / codex
	VendorSessionID string // Claude の sessionId・Codex のスレッド ID
	MachineLabel    string // 送った端末のラベル（64 文字まで）
	OriginOS        string // linux / darwin / windows
	AgentVersion    string // 記録の中の版（64 文字まで）
}

// AppendTranscriptChunkInput はチャンクの追記の入力（POST /transcripts/{id}/chunks）。
type AppendTranscriptChunkInput struct {
	TranscriptID int64
	// IfMatch は If-Match の値（"3" / 3 / W/"3"）。空なら 428。今の連番と違えば 409 transcript_seq
	IfMatch string
	// FileKey は記録の置き場の中の相対パス（X-Looptrack-Transcript-File をパーセント符号から戻したもの）
	FileKey string
	// FileKind は jsonl / json / text。空なら FileKey の形から決める（与えたなら形と一致すること）
	FileKind string
	// SrcOffset・SrcLength は元のファイルの中の位置と長さ（変換する前のバイト数。SrcOffset はそのファイルの送った長さと同じであること）
	SrcOffset int64
	SrcLength int64
	// Encoding は identity / gzip（Content-Encoding。空は identity）。gzip のまま置く
	Encoding string
	// Nonce・Placeholders はパスの書き換えの印（元の絶対パスは受け取らない）。Stats はクライアントが数えた件数の JSON のオブジェクト
	Nonce        string
	Placeholders []string
	Stats        string
	Body         io.Reader
	// Declared は送る側が申告した本文の大きさ（Content-Length）。上限を超えていれば読まずに拒む。分からなければ 0 以下
	Declared int64
}

// AppendTranscriptChunkResult は追記の結果。
type AppendTranscriptChunkResult struct {
	Chunk store.TranscriptChunk
	State TranscriptState
}

// TranscriptDetail は会話記録 1 つとチャンクの一覧（GET /transcripts/{id}）。
type TranscriptDetail struct {
	TranscriptState
	Chunks []store.TranscriptChunk // seq の順。消去済みも載せる（Purged）
}

// TranscriptPurgeResult は消去 1 回の結果。
type TranscriptPurgeResult struct {
	TranscriptID int64
	Changed      bool    // 記録を足したか（消去していないチャンクが無ければ false）
	ChunkIDs     []int64 // この消去で消去済みになったチャンク
	Removed      int     // 消した本体の数（ほかのチャンクか添付が指す本体は消さない）
}

// TranscriptRetentionResult は保持期間の切れの消去の結果。
type TranscriptRetentionResult struct {
	RetentionDays int64 // 0 は無期限（何も消さない）
	Purged        []TranscriptPurgeResult
}

func errTranscriptNotFound(id int64) *Error {
	return errm(NotFound, "not_found", i18n.M("service.err.transcript.not_found", "id", id))
}

// transcriptUser は操作した利用者を読む（利用者の無い要求は会話記録を持てない）。
func (s *Service) transcriptUser(ctx context.Context, a Actor) (store.User, error) {
	if a.UserID == 0 {
		return store.User{}, errm(Forbidden, "forbidden", i18n.M("service.err.transcript.no_user"))
	}
	u, err := store.UserByID(ctx, s.DB, a.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return u, errm(Forbidden, "forbidden", i18n.M("service.err.transcript.no_user"))
	}
	return u, err
}

// cleanTranscriptLabel は machine_label・agent_version を確かめる（前後の空白を除く。64 文字まで・改行や制御文字は不可）。
func cleanTranscriptLabel(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	bad := !utf8.ValidString(v) || utf8.RuneCountInString(v) > maxTranscriptLabel
	for _, r := range v {
		if unicode.IsControl(r) {
			bad = true
		}
	}
	if bad {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.label", "field", field, "limit", maxTranscriptLabel))
	}
	return v, nil
}

func validRegister(in RegisterTranscriptInput) (RegisterTranscriptInput, error) {
	in.Agent = strings.TrimSpace(in.Agent)
	if !slices.Contains(TranscriptAgents, in.Agent) {
		return in, errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.agent", "values", strings.Join(TranscriptAgents, " / "), "given", strconv.Quote(in.Agent)))
	}
	in.VendorSessionID = strings.TrimSpace(in.VendorSessionID)
	if !transcriptVendorIDRe.MatchString(in.VendorSessionID) || strings.Contains(in.VendorSessionID, "..") {
		return in, errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.session_id"))
	}
	in.OriginOS = strings.TrimSpace(in.OriginOS)
	if !slices.Contains(TranscriptOriginOS, in.OriginOS) {
		return in, errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.origin_os", "values", strings.Join(TranscriptOriginOS, " / "), "given", strconv.Quote(in.OriginOS)))
	}
	var err error
	if in.MachineLabel, err = cleanTranscriptLabel("machine_label", in.MachineLabel); err != nil {
		return in, err
	}
	if in.AgentVersion, err = cleanTranscriptLabel("agent_version", in.AgentVersion); err != nil {
		return in, err
	}
	return in, nil
}

// transcriptGate は会話記録を足せるプロジェクトかを確かめる: 参加者（viewer 以上）・アーカイブされていない・ルール session.transcripts が入。
// 参加していないプロジェクトは、無いものと同じ not_found（存在を確かめる道にしない）。ownerOf は追記のときの会話記録の ID
// （持ち主がそのプロジェクトから外れたときの文面に使う。登録では 0）。
func transcriptGate(ctx context.Context, q store.Queryer, u store.User, p store.Project, ownerOf int64) error {
	role, err := store.MemberRole(ctx, q, p.ID, u.ID)
	if err != nil {
		return err
	}
	if role == "" {
		if ownerOf != 0 {
			return errm(Forbidden, "forbidden", i18n.M("service.err.transcript.not_member", "id", ownerOf, "slug", p.Slug))
		}
		return errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", p.Slug))
	}
	if err := checkProjectActive(ctx, q, p); err != nil {
		return err
	}
	if !SessionTranscriptsEnabled(p) {
		return errm(Rejected, "transcript_rule_off", i18n.M("service.err.transcript.rule_off", "slug", p.Slug))
	}
	return nil
}

// transcriptState は会話記録の今の連番・ファイルごとの送った長さ・ルールの値を読む。
func transcriptState(ctx context.Context, q store.Queryer, t store.Transcript) (TranscriptState, error) {
	st := TranscriptState{Transcript: t}
	p, err := store.ProjectByID(ctx, q, t.ProjectID)
	if err != nil {
		return st, err
	}
	st.ProjectSlug, st.TranscriptsEnabled = p.Slug, SessionTranscriptsEnabled(p)
	if st.Seq, err = store.TranscriptSeq(ctx, q, t.ID); err != nil {
		return st, err
	}
	lens, err := store.TranscriptFileLengths(ctx, q, t.ID)
	if err != nil {
		return st, err
	}
	for k, n := range lens {
		st.Files = append(st.Files, TranscriptFileLength{FileKey: k, Length: n})
	}
	sort.Slice(st.Files, func(i, j int) bool { return st.Files[i].FileKey < st.Files[j].FileKey })
	return st, nil
}

// RegisterTranscript は会話記録を登録する。同じ (利用者, agent, vendor_session_id) が既にあればそれを返す（Created が false）。
// 送るプロジェクトの参加者（viewer 以上）で、プロジェクトがアーカイブされておらず、ルール session.transcripts が入であること。
func (s *Service) RegisterTranscript(ctx context.Context, a Actor, in RegisterTranscriptInput) (*TranscriptState, error) {
	u, err := s.transcriptUser(ctx, a)
	if err != nil {
		return nil, err
	}
	if _, err := s.transcriptRoot(); err != nil {
		return nil, err
	}
	if in, err = validRegister(in); err != nil {
		return nil, err
	}
	p, err := store.ProjectBySlug(ctx, s.DB, strings.TrimSpace(in.Project))
	if errors.Is(err, store.ErrNotFound) {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", in.Project))
	}
	if err != nil {
		return nil, err
	}
	if err := transcriptGate(ctx, s.DB, u, p, 0); err != nil {
		return nil, err
	}
	created := false
	t, err := store.TranscriptByVendor(ctx, s.DB, u.ID, in.Agent, in.VendorSessionID)
	if errors.Is(err, store.ErrNotFound) {
		row := store.Transcript{UserID: u.ID, ProjectID: p.ID, Agent: in.Agent, VendorSessionID: in.VendorSessionID, MachineLabel: in.MachineLabel,
			OriginOS: in.OriginOS, AgentVersion: in.AgentVersion, CreatedAt: s.Now(), TokenID: a.TokenID, Via: a.Via}
		id, ierr := store.InsertTranscript(ctx, s.DB, row)
		switch {
		case ierr == nil:
			created = true
		case store.IsDuplicateKey(ierr): // 同時の登録。先に入ったほうを返す
		default:
			return nil, ierr
		}
		if created {
			t, err = store.TranscriptByID(ctx, s.DB, u.ID, id)
		} else {
			t, err = store.TranscriptByVendor(ctx, s.DB, u.ID, in.Agent, in.VendorSessionID)
		}
	}
	if err != nil {
		return nil, err
	}
	st, err := transcriptState(ctx, s.DB, t)
	if err != nil {
		return nil, err
	}
	st.Created = created
	return &st, nil
}

// parseTranscriptIfMatch は If-Match の連番を読む（"3" / 3 / W/"3"。0 以上）。空なら 428、読めなければ 400。
func parseTranscriptIfMatch(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, errm(PreconditionRequired, "transcript_seq_required", i18n.M("service.err.transcript.seq_required"))
	}
	n, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(v, "W/"), `"`), 10, 64)
	if err != nil || n < 0 {
		return 0, errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.seq_invalid", "value", strconv.Quote(v)))
	}
	return n, nil
}

// TranscriptFileKind は file_key の形から読み方（jsonl / json / text）を決める。形に合わなければ false。
// Claude は projects/<slug>/ からの相対で、記録の sessionId（vendorSessionID）の下に限る:
// <sessionId>.jsonl・<sessionId>/subagents/<名前>.jsonl・<sessionId>/subagents/<名前>.json・<sessionId>/tool-results/<名前>（text）。
// Codex は sessions/ からの相対の <年>/<月>/<日>/rollout-<名前>.jsonl（history_base がたどる元の rollout はスレッド ID が違うので縛らない）。
// どちらも 255 バイトまで・UTF-8・制御文字と \ と : を含まない・空の段と . / .. の段を含まない。
func TranscriptFileKind(agent, vendorSessionID, key string) (string, bool) {
	if key == "" || len(key) > maxTranscriptFileKey || !utf8.ValidString(key) || strings.ContainsAny(key, `\:`) {
		return "", false
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	switch agent {
	case store.TranscriptAgentClaude:
		sid := regexp.QuoteMeta(vendorSessionID)
		for _, f := range []struct {
			re   string
			kind string
		}{
			{`^` + sid + `\.jsonl$`, store.TranscriptFileJSONL},
			{`^` + sid + `/subagents/` + transcriptNameRe + `\.jsonl$`, store.TranscriptFileJSONL},
			{`^` + sid + `/subagents/` + transcriptNameRe + `\.json$`, store.TranscriptFileJSON},
			{`^` + sid + `/tool-results/` + transcriptNameRe + `$`, store.TranscriptFileText},
		} {
			if regexp.MustCompile(f.re).MatchString(key) {
				return f.kind, true
			}
		}
	case store.TranscriptAgentCodex:
		if transcriptCodexKeyRe.MatchString(key) {
			return store.TranscriptFileJSONL, true
		}
	}
	return "", false
}

// cleanChunkMeta は追記の入力のうち本文以外を確かめ、placeholders・stats を保存する JSON にする。
func cleanChunkMeta(t store.Transcript, in AppendTranscriptChunkInput) (kind, encoding, placeholders, stats string, err error) {
	kind, ok := TranscriptFileKind(t.Agent, t.VendorSessionID, in.FileKey)
	if !ok {
		return "", "", "", "", errm(Invalid, "transcript_file_key", i18n.M("service.err.transcript.file_key", "file", strconv.Quote(truncateRunes(in.FileKey, 80)), "agent", t.Agent))
	}
	if in.FileKind != "" && in.FileKind != kind {
		return "", "", "", "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.file_kind", "given", strconv.Quote(in.FileKind), "file", in.FileKey, "want", kind))
	}
	encoding = strings.ToLower(strings.TrimSpace(in.Encoding))
	if encoding == "" {
		encoding = store.TranscriptEncodingIdentity
	}
	if encoding != store.TranscriptEncodingIdentity && encoding != store.TranscriptEncodingGzip {
		return "", "", "", "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.encoding", "given", strconv.Quote(truncateRunes(in.Encoding, 32))))
	}
	if in.SrcOffset < 0 || in.SrcLength < 1 {
		return "", "", "", "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.range"))
	}
	if !transcriptNonceRe.MatchString(in.Nonce) {
		return "", "", "", "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.nonce"))
	}
	ph := in.Placeholders
	if ph == nil {
		ph = []string{}
	}
	if len(ph) > maxTranscriptPlaceholders {
		return "", "", "", "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.placeholders", "limit", maxTranscriptPlaceholders))
	}
	for _, k := range ph {
		if !transcriptPlaceholderRe.MatchString(k) {
			return "", "", "", "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.placeholders", "limit", maxTranscriptPlaceholders))
		}
	}
	b, err := json.Marshal(ph)
	if err != nil {
		return "", "", "", "", err
	}
	stats = strings.TrimSpace(in.Stats)
	if stats == "" {
		stats = "{}"
	}
	if len(stats) > maxTranscriptStats || !utf8.ValidString(stats) || !json.Valid([]byte(stats)) || stats[0] != '{' {
		return "", "", "", "", errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.stats", "limit", maxTranscriptStats))
	}
	// stats は件数だけのはず。値を紛れ込ませた要求も本文と同じ規則で止める
	if hits, labels := jsonSecretHits([]byte(stats)); hits > 0 {
		return "", "", "", "", errm(Rejected, "transcript_secret", i18n.M("service.err.transcript.secret_stats", "count", hits, "labels", strings.Join(labels, ", ")))
	}
	return kind, encoding, string(b), stats, nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// transcriptScan はチャンクの中身の検査の結果（件数と行番号と規則の語だけ。値は持たない）。
type transcriptScan struct {
	lines         int64
	unreadable    int
	unreadableAt  []int
	secrets       int // 当たった値（text は行）の数
	secretLines   int // 当たった行の数
	secretAt      []int
	secretLabels  map[string]bool
	wholeNotValid bool // json: ファイル全体が 1 つの JSON として読めない
}

func (sc *transcriptScan) badLine(n int) {
	sc.unreadable++
	if len(sc.unreadableAt) < maxTranscriptReportLines {
		sc.unreadableAt = append(sc.unreadableAt, n)
	}
}

func (sc *transcriptScan) secretLine(n, hits int, labels []string) {
	sc.secrets += hits
	sc.secretLines++
	if len(sc.secretAt) < maxTranscriptReportLines {
		sc.secretAt = append(sc.secretAt, n)
	}
	if sc.secretLabels == nil {
		sc.secretLabels = map[string]bool{}
	}
	for _, l := range labels {
		sc.secretLabels[l] = true
	}
}

// jsonSecretHits は JSON の値 1 つ（読めることは確かめてあること）の、文字列の値ごととキーごとに domain.FindSecret を当て、
// 当たった数と規則の語を返す。重複したキーも 1 つずつ見る（オブジェクトに読み込むと後の値で前の値が消えるので、字句で歩く）。
func jsonSecretHits(doc []byte) (int, []string) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	hits := 0
	seen := map[string]bool{}
	var labels []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break // io.EOF（読めることは呼ぶ側が json.Valid で確かめている）
		}
		str, ok := tok.(string)
		if !ok {
			continue
		}
		if f, hit := domain.FindSecret(str); hit {
			hits++
			if !seen[f.Label] {
				seen[f.Label] = true
				labels = append(labels, f.Label)
			}
		}
	}
	return hits, labels
}

// scanTranscriptContent はチャンクの中身（伸ばした後）を種類ごとに検査する。
func scanTranscriptContent(kind string, data []byte) transcriptScan {
	var sc transcriptScan
	switch kind {
	case store.TranscriptFileJSONL:
		rest := data
		for n := 1; len(rest) > 0; n++ {
			sc.lines++
			line := rest
			if i := bytes.IndexByte(rest, '\n'); i >= 0 {
				line, rest = rest[:i], rest[i+1:]
			} else {
				rest = nil
				sc.badLine(n) // 改行で終わらない最後の行（書きかけ）は送らない約束（§9-8 の変換の 1）
				continue
			}
			if !json.Valid(line) {
				sc.badLine(n)
				continue
			}
			if hits, labels := jsonSecretHits(line); hits > 0 {
				sc.secretLine(n, hits, labels)
			}
		}
	case store.TranscriptFileJSON:
		sc.lines = 1
		if !json.Valid(data) {
			sc.wholeNotValid = true
			sc.badLine(1)
			break
		}
		if hits, labels := jsonSecretHits(data); hits > 0 {
			sc.secretLine(1, hits, labels)
		}
	case store.TranscriptFileText:
		sc.lines = int64(bytes.Count(data, []byte("\n")))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			sc.lines++
		}
		if f, hit := domain.FindSecret(string(data)); hit {
			// 件数は秘密の当たった行の数（規則が行をまたぐもの（鍵の塊）は 1 件として数える）
			hits, at := 0, []int{}
			for i, l := range strings.Split(string(data), "\n") {
				if _, h := domain.FindSecret(l); h {
					hits++
					if len(at) < maxTranscriptReportLines {
						at = append(at, i+1)
					}
				}
			}
			if hits == 0 {
				hits, at = 1, []int{f.Line}
			}
			sc.secrets, sc.secretLines, sc.secretAt, sc.secretLabels = hits, hits, at, map[string]bool{f.Label: true}
		}
	}
	return sc
}

func joinLines(at []int, total int) string {
	parts := make([]string, len(at))
	for i, n := range at {
		parts[i] = strconv.Itoa(n)
	}
	s := strings.Join(parts, ", ")
	if total > len(at) {
		s += ", …"
	}
	return s
}

// rejectScan は検査の結果を拒否にする（外れていなければ nil）。読めない行を先に見る。
func rejectScan(fileKey string, sc transcriptScan) error {
	if sc.wholeNotValid {
		return errm(Rejected, "transcript_unreadable", i18n.M("service.err.transcript.unreadable_json", "file", fileKey))
	}
	if sc.unreadable > 0 {
		return errm(Rejected, "transcript_unreadable", i18n.M("service.err.transcript.unreadable", "file", fileKey, "count", sc.unreadable,
			"lines", joinLines(sc.unreadableAt, sc.unreadable)))
	}
	if sc.secrets > 0 {
		labels := make([]string, 0, len(sc.secretLabels))
		for l := range sc.secretLabels {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		return errm(Rejected, "transcript_secret", i18n.M("service.err.transcript.secret", "file", fileKey, "count", sc.secrets,
			"lines", joinLines(sc.secretAt, sc.secretLines), "labels", strings.Join(labels, ", ")))
	}
	return nil
}

// maxStoredChunk は置く本体（gzip ならその形）の大きさの上限。伸ばした後の上限 maxChunk に、
// 圧縮できない中身を gzip にしたときの増分（deflate の格納ブロックの見出しと gzip の頭と尾）の余裕を足す。
func maxStoredChunk(encoding string, maxChunk int64) int64 {
	if encoding == store.TranscriptEncodingGzip {
		return maxChunk + maxChunk/1024 + 1024
	}
	return maxChunk
}

// readChunkContent は一時ファイルの本体を伸ばして読む（伸ばした大きさが maxChunk を超えたら 413、gzip として読めなければ 422）。
func readChunkContent(tmp, encoding string, maxChunk int64) ([]byte, error) {
	f, err := os.Open(tmp)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if encoding == store.TranscriptEncodingGzip {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, errm(Rejected, "transcript_unreadable", i18n.M("service.err.transcript.gzip"))
		}
		defer zr.Close()
		r = zr
	}
	data, err := io.ReadAll(io.LimitReader(r, maxChunk+1))
	if err != nil {
		if encoding == store.TranscriptEncodingGzip {
			return nil, errm(Rejected, "transcript_unreadable", i18n.M("service.err.transcript.gzip"))
		}
		return nil, err
	}
	if int64(len(data)) > maxChunk {
		return nil, errTranscriptTooLarge(maxChunk)
	}
	return data, nil
}

func errTranscriptTooLarge(max int64) *Error {
	return errm(TooLarge, "transcript_too_large", i18n.M("service.err.transcript.too_large", "limit", max))
}

func seqConflict(id, current, given int64) *Error {
	e := errm(Conflict, "transcript_seq", i18n.M("service.err.transcript.seq", "id", id, "current", current, "given", given))
	e.CurrentSeq = &current
	return e
}

// errSeqTaken は追記のトランザクションの中で、同じ連番が先に入っていたこと（一意性の違反）を外に知らせる印。
var errSeqTaken = errors.New("transcript seq taken")

// AppendTranscriptChunk は会話記録にチャンクを 1 つ足す（持ち主だけ）。検査に外れたら本体もメタデータも残さない。
func (s *Service) AppendTranscriptChunk(ctx context.Context, a Actor, in AppendTranscriptChunkInput) (*AppendTranscriptChunkResult, error) {
	u, err := s.transcriptUser(ctx, a)
	if err != nil {
		return nil, err
	}
	root, err := s.transcriptRoot()
	if err != nil {
		return nil, err
	}
	t, err := store.TranscriptByID(ctx, s.DB, u.ID, in.TranscriptID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errTranscriptNotFound(in.TranscriptID)
	}
	if err != nil {
		return nil, err
	}
	expected, err := parseTranscriptIfMatch(in.IfMatch)
	if err != nil {
		return nil, err
	}
	kind, encoding, placeholders, stats, err := cleanChunkMeta(t, in)
	if err != nil {
		return nil, err
	}
	p, err := store.ProjectByID(ctx, s.DB, t.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := transcriptGate(ctx, s.DB, u, p, t.ID); err != nil {
		return nil, err
	}
	limits, err := store.ReadTranscriptLimits(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	maxStored := maxStoredChunk(encoding, limits.MaxChunk)
	if in.Declared > maxStored {
		return nil, errTranscriptTooLarge(limits.MaxChunk)
	}
	tmp, sum, size, err := writeBlobTemp(root, in.Body, maxStored)
	if errors.Is(err, errBlobTooLarge) {
		return nil, errTranscriptTooLarge(limits.MaxChunk)
	}
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp) // rename した後は無い（消すものが無いだけ）
	if size == 0 {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.empty"))
	}
	data, err := readChunkContent(tmp, encoding, limits.MaxChunk)
	if err != nil {
		return nil, err
	}
	sc := scanTranscriptContent(kind, data)
	if err := rejectScan(in.FileKey, sc); err != nil {
		return nil, err
	}

	// 本体を先に置き、トランザクションの中には読み直しと 1 行の記録だけを入れる（rename と fsync の間、利用者のロックを握らない）。
	// 同じ本体を消去・整合の検査が消さないよう、置いてから記録が確定するまでその本体（sha256）だけを 1 つずつにする（冒頭のコメント）
	// ロックの行は本体を置く前に作る（作れなかったときに、どこからも指されない本体を残さないため）
	if err := store.EnsureTranscriptLock(ctx, s.DB, u.ID); err != nil {
		return nil, err
	}
	unlock := s.transcriptBodies.lock(sum)
	defer unlock()
	// 別のプロセスの消去に、置いてから記録が確定するまでの追記があることを示す印（記録を確定してから消す）
	placing, err := beginBlobPlace(root, sum)
	if err != nil {
		return nil, err
	}
	if err := placeBlob(root, tmp, sum); err != nil {
		placing()
		return nil, err
	}
	if s.TranscriptPlaced != nil {
		s.TranscriptPlaced(sum)
	}
	now := s.Now()
	chunk := store.TranscriptChunk{TranscriptID: t.ID, FileKey: in.FileKey, FileKind: kind, SrcOffset: in.SrcOffset, SrcLength: in.SrcLength,
		LineCount: sc.lines, SHA256: sum, Size: size, Encoding: encoding, Nonce: in.Nonce, Placeholders: placeholders, Stats: stats,
		CreatedAt: now, TokenID: a.TokenID, Via: a.Via}
	wctx, cancel := context.WithTimeout(ctx, blobPlaceWindow)
	err = s.inTx(wctx, func(tx *sql.Tx) error {
		ctx := wctx
		// 利用者の会話記録のロックの行を握って、同じ利用者の追記と消去を 1 つずつにする（読み直しはこの後。冒頭のコメント）
		if err := store.LockTranscriptUser(ctx, tx, u.ID); err != nil {
			return err
		}
		cur, err := store.ProjectByID(ctx, tx, t.ProjectID)
		if err != nil {
			return err
		}
		if err := transcriptGate(ctx, tx, u, cur, t.ID); err != nil {
			return err
		}
		seq, err := store.TranscriptSeq(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if seq != expected {
			return seqConflict(t.ID, seq, expected)
		}
		lens, err := store.TranscriptFileLengths(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if lens[in.FileKey] != in.SrcOffset {
			return errm(Conflict, "transcript_offset", i18n.M("service.err.transcript.offset", "file", in.FileKey, "length", lens[in.FileKey], "given", in.SrcOffset))
		}
		used, err := store.TranscriptUserBytes(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		if used+size > limits.MaxUser {
			return errm(Rejected, "transcript_quota", i18n.M("service.err.transcript.quota", "limit", limits.MaxUser, "used", used, "size", size))
		}
		chunk.Seq = seq + 1
		id, err := store.InsertTranscriptChunk(ctx, tx, chunk)
		if store.IsDuplicateKey(err) {
			return errSeqTaken
		}
		if err != nil {
			return err
		}
		chunk.ID = id
		return nil
	})
	cancel()
	// 印は確定（か失敗）の後に消す。失敗の片付けより先に消す（自分の印を、置こうとしているほかの追記と数えないため）
	placing()
	if err != nil {
		// 記録できなかった。置いた本体がどこからも指されていなければ消す（同じ内容を指すチャンクか添付があれば残す。
		// 別のプロセスで同じ本体を置いて記録する前の追記があれば、その印を見て残す。
		// 消せなかったときは、どこからも指されない本体として repair-transcripts --apply が拾う）
		_, _ = s.removeUnreferencedTranscriptBody(ctx, root, sum)
	}
	if errors.Is(err, errSeqTaken) {
		// ロックを越えて同じ連番が入った（ロックの行を使わない経路）。一意性が取り合いを決めたので、今の連番を返す
		cur, serr := store.TranscriptSeq(ctx, s.DB, t.ID)
		if serr != nil {
			return nil, serr
		}
		return nil, seqConflict(t.ID, cur, expected)
	}
	if err != nil {
		return nil, err
	}
	st, err := transcriptState(ctx, s.DB, t)
	if err != nil {
		return nil, err
	}
	return &AppendTranscriptChunkResult{Chunk: chunk, State: st}, nil
}

// ListTranscripts は自分の会話記録を新しい順に返す（ほかの利用者のものは載らない）。project が空でなければ、
// その slug のプロジェクトで送ったものだけ（無い slug・参加していないプロジェクトでも、自分の記録が無ければ空を返す。存在を確かめる道にしない）。
func (s *Service) ListTranscripts(ctx context.Context, a Actor, project string) ([]store.TranscriptSummary, error) {
	u, err := s.transcriptUser(ctx, a)
	if err != nil {
		return nil, err
	}
	if _, err := s.transcriptRoot(); err != nil {
		return nil, err
	}
	var pid int64
	if slug := strings.TrimSpace(project); slug != "" {
		p, err := store.ProjectBySlug(ctx, s.DB, slug)
		if errors.Is(err, store.ErrNotFound) {
			return []store.TranscriptSummary{}, nil
		}
		if err != nil {
			return nil, err
		}
		pid = p.ID
	}
	out, err := store.ListTranscripts(ctx, s.DB, u.ID, pid)
	if out == nil && err == nil {
		out = []store.TranscriptSummary{}
	}
	return out, err
}

// ownTranscript は持ち主 a の会話記録を引く（無い・ほかの利用者のものは同じ not_found）。
func (s *Service) ownTranscript(ctx context.Context, a Actor, id int64) (store.User, store.Transcript, string, error) {
	u, err := s.transcriptUser(ctx, a)
	if err != nil {
		return u, store.Transcript{}, "", err
	}
	root, err := s.transcriptRoot()
	if err != nil {
		return u, store.Transcript{}, "", err
	}
	t, err := store.TranscriptByID(ctx, s.DB, u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return u, t, "", errTranscriptNotFound(id)
	}
	return u, t, root, err
}

// GetTranscript は会話記録 1 つとチャンクの一覧を返す（持ち主だけ）。
func (s *Service) GetTranscript(ctx context.Context, a Actor, id int64) (*TranscriptDetail, error) {
	_, t, _, err := s.ownTranscript(ctx, a, id)
	if err != nil {
		return nil, err
	}
	st, err := transcriptState(ctx, s.DB, t)
	if err != nil {
		return nil, err
	}
	chunks, err := store.TranscriptChunks(ctx, s.DB, t.ID)
	if err != nil {
		return nil, err
	}
	if chunks == nil {
		chunks = []store.TranscriptChunk{}
	}
	return &TranscriptDetail{TranscriptState: st, Chunks: chunks}, nil
}

// OpenTranscriptChunk はチャンクの本体を置いた形のまま開く（呼ぶ側が閉じる。持ち主だけ）。
// 消去済みなら transcript_purged、本体が置き場に無ければ transcript_body_missing を返す。
func (s *Service) OpenTranscriptChunk(ctx context.Context, a Actor, id, seq int64) (store.TranscriptChunk, *os.File, error) {
	_, t, root, err := s.ownTranscript(ctx, a, id)
	if err != nil {
		return store.TranscriptChunk{}, nil, err
	}
	c, err := store.TranscriptChunkBySeq(ctx, s.DB, t.ID, seq)
	if errors.Is(err, store.ErrNotFound) {
		return c, nil, errm(NotFound, "not_found", i18n.M("service.err.transcript.chunk_not_found", "id", id, "seq", seq))
	}
	if err != nil {
		return c, nil, err
	}
	if c.Purged {
		return c, nil, errm(Rejected, "transcript_purged", i18n.M("service.err.transcript.purged", "id", id, "seq", seq))
	}
	f, err := os.Open(TranscriptBodyPath(root, c.SHA256))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil, errm(NotFound, "transcript_body_missing", i18n.M("service.err.transcript.body_missing", "id", id, "seq", seq))
	}
	if err != nil {
		return c, nil, err
	}
	return c, f, nil
}

// transcriptPurgeSpec は消去の記録の中身（cause・操作した人・経路・理由）。
type transcriptPurgeSpec struct {
	cause   string
	actorID int64
	via     string
	reason  string
}

// purgeTranscript は会話記録 t の消去していないチャンクを消去済みにし、どこからも指されなくなった本体を消す。
// 記録（transcript_purges）を入れてから本体を消す。本体を消せなかったときは記録を残したまま失敗を返す
// （その本体はどこからも指されないので、repair-transcripts --apply で消せる）。
func (s *Service) purgeTranscript(ctx context.Context, root string, t store.Transcript, spec transcriptPurgeSpec) (TranscriptPurgeResult, error) {
	res := TranscriptPurgeResult{TranscriptID: t.ID}
	if err := store.EnsureTranscriptLock(ctx, s.DB, t.UserID); err != nil {
		return res, err
	}
	var sums []string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res.Changed, res.ChunkIDs, sums = false, nil, nil
		// 持ち主の追記と 1 つずつにする（追記が読んだ送った長さの後に消去が入ると、消去の後に古い位置からのチャンクが残る）
		if err := store.LockTranscriptUser(ctx, tx, t.UserID); err != nil {
			return err
		}
		chunks, err := store.TranscriptChunks(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		var through int64
		seen := map[string]bool{}
		for _, c := range chunks {
			if c.ID > through {
				through = c.ID
			}
			if c.Purged {
				continue
			}
			res.ChunkIDs = append(res.ChunkIDs, c.ID)
			if !seen[c.SHA256] {
				seen[c.SHA256] = true
				sums = append(sums, c.SHA256)
			}
		}
		if len(res.ChunkIDs) == 0 {
			return nil
		}
		if err := store.InsertTranscriptPurge(ctx, tx, store.TranscriptPurge{TranscriptID: t.ID, ThroughChunkID: through, Cause: spec.cause,
			ActorUserID: spec.actorID, Via: spec.via, Reason: spec.reason, At: s.Now()}); err != nil {
			return err
		}
		res.Changed = true
		return nil
	})
	if err != nil {
		return res, err
	}
	var failed int
	var firstErr error
	for _, sum := range sums {
		unlock := s.transcriptBodies.lock(sum)
		removed, err := s.removeUnreferencedTranscriptBody(ctx, root, sum)
		unlock()
		var rerr *bodyRemoveError
		if errors.As(err, &rerr) {
			failed++
			if firstErr == nil {
				firstErr = rerr.err
			}
			continue
		}
		if err != nil {
			return res, err
		}
		if removed {
			res.Removed++
		}
	}
	if firstErr != nil {
		return res, i18n.Wrapf(firstErr, "service.err.transcript.purge_remove", "count", failed)
	}
	return res, nil
}

// bodyRemoveError は本体のファイルを消せなかった失敗（DB の読み出しの失敗と分ける）。
type bodyRemoveError struct{ err error }

func (e *bodyRemoveError) Error() string { return e.err.Error() }
func (e *bodyRemoveError) Unwrap() error { return e.err }

// removeUnreferencedTranscriptBody は本体 sum を指す、消去していないチャンクも添付も無く、別のプロセスで同じ本体を置いて
// 記録する前の追記も無ければ、本体のファイルを消す（removeBlobGuarded。呼ぶ側がその本体のロック（transcriptBodies）を
// 握っている間に呼ぶ）。消したら（初めから無かったときも）true。ファイルを消せなければ *bodyRemoveError。
func (s *Service) removeUnreferencedTranscriptBody(ctx context.Context, root, sum string) (bool, error) {
	r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, func() (int, error) { return s.transcriptBodyRefs(ctx, sum) })
	return r != blobKept, err
}

// transcriptBodyRefs は本体 sum を指す、消去していないチャンクと添付の数（添付は、置き場を取り違えた設定でも消さないために数える）。
func (s *Service) transcriptBodyRefs(ctx context.Context, sum string) (int, error) {
	n, err := store.LiveTranscriptChunkCount(ctx, s.DB, sum)
	if err != nil || n > 0 {
		return n, err
	}
	return store.LiveAttachmentCount(ctx, s.DB, sum)
}

// PurgeTranscript は自分の会話記録の本体を消す（持ち主だけ。ほかの利用者には not_found）。
// その時点の最後のチャンクまでが消去済みになり、同じ会話を src_offset 0 から送り直せる。
func (s *Service) PurgeTranscript(ctx context.Context, a Actor, id int64, reason string) (*TranscriptPurgeResult, error) {
	u, t, root, err := s.ownTranscript(ctx, a, id)
	if err != nil {
		return nil, err
	}
	res, err := s.purgeTranscript(ctx, root, t, transcriptPurgeSpec{cause: store.TranscriptPurgeOwner, actorID: u.ID, via: a.Via, reason: strings.TrimSpace(reason)})
	return &res, err
}

// transcriptRetentionVia は保持期間の切れの消去の経路（transcript_purges.via の CHECK にある値）。
const transcriptRetentionVia = "system"

// PurgeExpiredTranscripts は保持期間（transcript_retention_days）が切れた会話記録の本体を消す（サーバの定期の処理が呼ぶ）。
// 最後のチャンクから日数が経った、消去していないチャンクを持つ会話記録が対象で、cause retention・via system で記録する。
// 0（無期限）なら何も消さない。1 つが失敗しても残りは続け、失敗はまとめて返す。
func (s *Service) PurgeExpiredTranscripts(ctx context.Context) (*TranscriptRetentionResult, error) {
	root, err := s.transcriptRoot()
	if err != nil {
		return nil, err
	}
	limits, err := store.ReadTranscriptLimits(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	res := &TranscriptRetentionResult{RetentionDays: limits.RetentionDays}
	if limits.RetentionDays == 0 {
		return res, nil
	}
	cutoff := s.Now().Add(-time.Duration(limits.RetentionDays) * 24 * time.Hour)
	live, err := store.TranscriptsWithLiveChunks(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, l := range live {
		if !l.LastChunkAt.Before(cutoff) {
			continue
		}
		t, err := store.TranscriptByIDAnyUser(ctx, s.DB, l.TranscriptID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r, err := s.purgeTranscript(ctx, root, t, transcriptPurgeSpec{cause: store.TranscriptPurgeRetention, via: transcriptRetentionVia,
			reason: "transcript_retention_days=" + strconv.FormatInt(limits.RetentionDays, 10)})
		if err != nil {
			errs = append(errs, err)
		}
		if r.Changed {
			res.Purged = append(res.Purged, r)
		}
	}
	return res, errors.Join(errs...)
}

// transcriptOperatorVia は運用者の消去（管理用のサブコマンド。LOOPTRACK_DSN の経路で、利用者を持たない）の経路。
const transcriptOperatorVia = "admin"

// OperatorTranscripts は運用者の消去の対象（利用者 login の会話記録。transcriptID が 0 でなければそのうちの 1 つ）を返す
// （looptrack purge-transcripts が、消す前の件数と大きさを示すのにも使う。何も変えない）。
// 利用者が居ない・会話記録が 1 つも無い・指した会話記録がその利用者のものでない、はいずれも NotFound。
func (s *Service) OperatorTranscripts(ctx context.Context, login string, transcriptID int64) ([]store.TranscriptSummary, error) {
	login = strings.TrimSpace(login)
	u, err := store.UserByLogin(ctx, s.DB, login)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.transcript.no_login", "login", login))
	}
	if err != nil {
		return nil, err
	}
	list, err := store.ListTranscripts(ctx, s.DB, u.ID, 0)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.transcript.operator_none", "login", login))
	}
	if transcriptID == 0 {
		return list, nil
	}
	for _, t := range list {
		if t.ID == transcriptID {
			return []store.TranscriptSummary{t}, nil
		}
	}
	return nil, errTranscriptNotFound(transcriptID)
}

// PurgeUserTranscripts はサーバの運用者が利用者 login の会話記録をすべて消す（PurgeOperatorTranscripts の transcriptID 0）。
func (s *Service) PurgeUserTranscripts(ctx context.Context, login, reason string) ([]TranscriptPurgeResult, error) {
	return s.PurgeOperatorTranscripts(ctx, login, 0, reason)
}

// PurgeOperatorTranscripts はサーバの運用者が利用者 login の会話記録を消す（cause operator。looptrack purge-transcripts が呼ぶ）。
// transcriptID が 0 ならすべて、0 でなければその 1 つだけ（その利用者のものに限る）。
// 理由は必須。操作した利用者は記録しない（DB の資格情報で動く経路なので、利用者が居ない）。
func (s *Service) PurgeOperatorTranscripts(ctx context.Context, login string, transcriptID int64, reason string) ([]TranscriptPurgeResult, error) {
	root, err := s.transcriptRoot()
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.transcript.operator_reason"))
	}
	list, err := s.OperatorTranscripts(ctx, login, transcriptID)
	if err != nil {
		return nil, err
	}
	var out []TranscriptPurgeResult
	for _, t := range list {
		r, err := s.purgeTranscript(ctx, root, t.Transcript, transcriptPurgeSpec{cause: store.TranscriptPurgeOperator, via: transcriptOperatorVia, reason: reason})
		if err != nil {
			return out, err
		}
		if r.Changed {
			out = append(out, r)
		}
	}
	return out, nil
}
