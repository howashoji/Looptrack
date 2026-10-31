package service

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト記憶（引き継ぎ・決定・注意。DESIGN.md §9-9）。規則（種類・名前の形・要約と本文の上限・範囲と権限・版の照合・
// 資格情報の検査・版の履歴）はこのファイルの 1 か所に置き、REST・MCP・CLI・Web はここの関数を呼ぶ。
//
// 範囲と権限:
//   - project: プロジェクトの参加者が読め、editor 以上が書ける（他人が書いたものも書き換えられる。版と書き手が履歴に残る）
//   - personal: 本人だけが読み書きする。持ち主はキーの一部（owner_user_id）なので、ほかの利用者（管理者を含む）の要求からは
//     そもそも引けず、無いものと同じ 404 になる
//
// 参加していないプロジェクトは無いものと同じ 404。参加していない管理者は store.AccessibleProjects の扱いどおり viewer として読める。
//
// 同時の書き込み: 書く前に行が無ければ下書きの行（version 0）を作り、トランザクションではその行を FOR UPDATE してから今の本文を
// 読み直して足す。同じ記憶への同時の書き込みは行ロックで 1 つずつになり、両方の節が残る。行を先に作るのは、無い行への FOR UPDATE が
// ギャップロックになり、別の名前の記憶を同時に作るとデッドロックになるため（store.EnsureMemoryRow）。
// 置き換えは If-Match の版が今の版と違えば 409 memory_version で何も書かない。

// MaxMemoryBytes は記憶 1 件の本文の上限（バイト。セッション要約の 4 倍）。
const MaxMemoryBytes = 64 * 1024

// MaxMemoryAppendBytes は追記 1 回の上限（バイト。セッション要約の MaxDigestBytes と同じ）。
const MaxMemoryAppendBytes = MaxDigestBytes

// maxMemorySummary は要約（本文の先頭の 1 行）の上限（文字数）。
const maxMemorySummary = 200

// DefaultHandoffName は handoff の名前を省いたときの名前。
const DefaultHandoffName = "main"

// MemoryKinds は記憶の種類。
var MemoryKinds = []string{store.MemoryKindHandoff, store.MemoryKindDecision, store.MemoryKindCaveat}

// MemoryScopes は記憶の範囲。
var MemoryScopes = []string{store.MemoryScopeProject, store.MemoryScopePersonal}

// memoryIdempotencyKeyRe は冪等の鍵の形。
var memoryIdempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// memoryNameRe は記憶の名前の形（英小文字・数字・-。先頭は英小文字か数字。64 文字まで）。
var memoryNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// MemoryKey は記憶を指すキー。Scope が空なら project、Name が空で Kind が handoff なら main。
type MemoryKey struct {
	Project string
	Scope   string
	Kind    string
	Name    string
}

// MemoryWriteInput は記憶の書き込みの入力。
type MemoryWriteInput struct {
	MemoryKey
	// Op は create / append / replace / delete
	Op string
	// Content は create・replace では新しい本文の全体、append では足す節（delete では使わない）
	Content string
	// IfMatch は読んだ版（"3" / 3 / W/"3"）。replace では必須（行が無いときに作るなら 0）。delete では任意（あれば照合する）
	IfMatch string
	// MachineLabel は書いた端末のラベル（64 文字まで。無ければ空）
	MachineLabel string
	// IdempotencyKey は追記の冪等の鍵（クライアントが作る。英数字・-・_ の 64 文字まで。無ければ空）。
	// 同じ鍵の再送は新しい版を作らずその鍵の版を返す（応答の前に切れた追記を送り直しても節が増えない）
	IdempotencyKey string
	// WrittenAt はクライアントが本文を作った時刻（RFC 3339。無ければ空）。追記と置き換えで版に残す。未来の時刻は今に丸める
	WrittenAt string
}

// MemoryResult は記憶 1 件の今の版。
type MemoryResult struct {
	Memory      store.Memory
	ProjectSlug string
	// Created は、この書き込みで行を作ったか
	Created bool
}

// MemoryDetail は記憶 1 件と版の履歴（新しい順。本文は入れない）。
type MemoryDetail struct {
	MemoryResult
	Versions []store.MemoryVersion
}

func errMemoryNotFound(k MemoryKey) *Error {
	return errm(NotFound, "not_found", i18n.M("service.err.memory.not_found", "scope", k.Scope, "kind", k.Kind, "name", k.Name))
}

func memoryVersionConflict(k MemoryKey, current, given int64) *Error {
	e := errm(Conflict, "memory_version", i18n.M("service.err.memory.version", "kind", k.Kind, "name", k.Name, "current", current, "given", given))
	e.CurrentSeq = &current
	return e
}

// cleanMemoryKey はキーを確かめて既定値を埋める。理由には入力の値を載せない（欄に秘密を貼り付けた要求を、そのまま応答やログに返さないため）。
func cleanMemoryKey(k MemoryKey) (MemoryKey, error) {
	k.Project = strings.TrimSpace(k.Project)
	k.Scope = strings.TrimSpace(k.Scope)
	if k.Scope == "" {
		k.Scope = store.MemoryScopeProject
	}
	if !slices.Contains(MemoryScopes, k.Scope) {
		return k, errm(Invalid, "invalid_argument", i18n.M("service.err.memory.scope", "values", strings.Join(MemoryScopes, " / ")))
	}
	k.Kind = strings.TrimSpace(k.Kind)
	if !slices.Contains(MemoryKinds, k.Kind) {
		return k, errm(Invalid, "invalid_argument", i18n.M("service.err.memory.kind", "values", strings.Join(MemoryKinds, " / ")))
	}
	k.Name = strings.TrimSpace(k.Name)
	if k.Name == "" && k.Kind == store.MemoryKindHandoff {
		k.Name = DefaultHandoffName
	}
	if !memoryNameRe.MatchString(k.Name) {
		return k, errm(Invalid, "invalid_argument", i18n.M("service.err.memory.name", "max", 64))
	}
	if f, ok := domain.FindSecret(k.Name); ok {
		return k, errm(Rejected, "memory_secret", i18n.M("service.err.memory.secret_field", "field", "name", "label", f.Label))
	}
	return k, nil
}

// memoryAccess は利用者と、その利用者が扱えるプロジェクトと役割を引く（参加していなければ無いものと同じ 404）。
func (s *Service) memoryAccess(ctx context.Context, a Actor, slug string) (store.User, store.Project, string, error) {
	if a.UserID == 0 {
		return store.User{}, store.Project{}, "", errm(Forbidden, "forbidden", i18n.M("service.err.memory.no_user"))
	}
	u, err := store.UserByID(ctx, s.DB, a.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return u, store.Project{}, "", errm(Forbidden, "forbidden", i18n.M("service.err.memory.no_user"))
	}
	if err != nil {
		return u, store.Project{}, "", err
	}
	projects, roles, err := store.AccessibleProjects(ctx, s.DB, u)
	if err != nil {
		return u, store.Project{}, "", err
	}
	for _, p := range projects {
		if p.Slug == slug {
			return u, p, roles[p.ID], nil
		}
	}
	return u, store.Project{}, "", errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", slug))
}

// memoryOwner は範囲が personal なら利用者の ID、project なら 0 を返す（キーの owner_user_id）。
func memoryOwner(scope string, u store.User) int64 {
	if scope == store.MemoryScopePersonal {
		return u.ID
	}
	return 0
}

// canWriteMemory は書き込みの権限。project は editor 以上、personal は本人（参加者なら役割を問わない）。
func canWriteMemory(scope, role string) bool {
	if scope == store.MemoryScopePersonal {
		return role != ""
	}
	return role == "editor" || role == "admin"
}

// MemorySummaryOf は本文の要約（先頭の空でない 1 行。前後の空白を除く）を返す。
func MemorySummaryOf(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

// joinMemorySection は今の本文の末尾に節を足す（間は空行 1 つ。末尾は改行 1 つにそろえる）。
func joinMemorySection(cur, section string) string {
	section = strings.TrimRight(section, "\r\n") + "\n"
	if strings.TrimSpace(cur) == "" {
		return section
	}
	return strings.TrimRight(cur, "\r\n") + "\n\n" + section
}

// checkMemoryText は本文（か足す節）の形と資格情報を確かめる。理由には行番号と規則の語だけを載せ、値は載せない。
// 足した後の本文の全体は checkMemoryResult がもう一度見る（節に分けて送ると、別々なら当たらない形がつながるため）。
func checkMemoryText(k MemoryKey, text string, max int) error {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.memory.encoding"))
	}
	if strings.TrimSpace(text) == "" {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.memory.empty"))
	}
	if len(text) > max {
		return errm(Rejected, "memory_too_large", i18n.M("service.err.memory.too_large", "kind", k.Kind, "name", k.Name, "bytes", len(text), "max", max))
	}
	if f, ok := domain.FindSecret(text); ok {
		return errm(Rejected, "memory_secret", i18n.M("service.err.memory.secret", "kind", k.Kind, "name", k.Name, "line", f.Line, "label", f.Label))
	}
	return nil
}

// checkMemoryResult は書いた後の本文の全体（上限・資格情報・要約の長さ）を確かめる。
func checkMemoryResult(k MemoryKey, content string) (string, error) {
	if len(content) > MaxMemoryBytes {
		return "", errm(Rejected, "memory_too_large", i18n.M("service.err.memory.too_large", "kind", k.Kind, "name", k.Name, "bytes", len(content), "max", MaxMemoryBytes))
	}
	if f, ok := domain.FindSecret(content); ok {
		return "", errm(Rejected, "memory_secret", i18n.M("service.err.memory.secret", "kind", k.Kind, "name", k.Name, "line", f.Line, "label", f.Label))
	}
	sum := MemorySummaryOf(content)
	if n := utf8.RuneCountInString(sum); n > maxMemorySummary {
		return "", errm(Rejected, "memory_summary_too_long", i18n.M("service.err.memory.summary", "kind", k.Kind, "name", k.Name, "chars", n, "max", maxMemorySummary))
	}
	return sum, nil
}

// parseMemoryIfMatch は If-Match の版を読む（"3" / 3 / W/"3"。0 以上）。required で空なら 428。空で required でなければ -1。
func parseMemoryIfMatch(v string, required bool) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if required {
			return 0, errm(PreconditionRequired, "memory_version_required", i18n.M("service.err.memory.version_required"))
		}
		return -1, nil
	}
	n, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(v, "W/"), `"`), 10, 64)
	if err != nil || n < 0 {
		return 0, errm(Invalid, "invalid_argument", i18n.M("service.err.memory.version_invalid"))
	}
	return n, nil
}

// ListMemories はプロジェクトの記憶の一覧を返す（project の範囲のものと、自分の personal のもの。本文を含む）。
func (s *Service) ListMemories(ctx context.Context, a Actor, slug string, includeDeleted bool) ([]store.Memory, store.Project, error) {
	u, p, _, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	if err != nil {
		return nil, p, err
	}
	out, err := store.ListMemories(ctx, s.DB, p.ID, u.ID, includeDeleted)
	if out == nil && err == nil {
		out = []store.Memory{}
	}
	return out, p, err
}

// ProjectMemoryAccess は利用者の login と、プロジェクトの範囲の記憶を書けるかを返す（kit の鮮度ガードが、書けない者に書き直しを迫らず、
// 書き手の利用者を比べるため）。
func (s *Service) ProjectMemoryAccess(ctx context.Context, a Actor, slug string) (string, bool) {
	u, _, role, err := s.memoryAccess(ctx, a, strings.TrimSpace(slug))
	return u.Login, err == nil && canWriteMemory(store.MemoryScopeProject, role)
}

// GetMemory は記憶 1 件と版の履歴を返す。消した印のものは無いものと同じ 404（今の版を CurrentSeq に載せる。作り直すときの If-Match）。
func (s *Service) GetMemory(ctx context.Context, a Actor, key MemoryKey) (*MemoryDetail, error) {
	k, err := cleanMemoryKey(key)
	if err != nil {
		return nil, err
	}
	u, p, _, err := s.memoryAccess(ctx, a, k.Project)
	if err != nil {
		return nil, err
	}
	m, err := store.MemoryByKey(ctx, s.DB, p.ID, k.Scope, memoryOwner(k.Scope, u), k.Kind, k.Name, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errMemoryNotFound(k)
	}
	if err != nil {
		return nil, err
	}
	if m.Version == 0 {
		return nil, errMemoryNotFound(k)
	}
	if m.Deleted {
		e := errm(NotFound, "not_found", i18n.M("service.err.memory.deleted", "kind", k.Kind, "name", k.Name, "version", m.Version))
		e.CurrentSeq = &m.Version
		return nil, e
	}
	vs, err := store.MemoryVersions(ctx, s.DB, m.ID)
	if err != nil {
		return nil, err
	}
	return &MemoryDetail{MemoryResult: MemoryResult{Memory: m, ProjectSlug: p.Slug}, Versions: vs}, nil
}

// GetMemoryVersion は記憶の 1 つの版（本文つき）を返す。消した印のものの版も読める（戻すため）。本体を消した版は 404 memory_version_purged。
func (s *Service) GetMemoryVersion(ctx context.Context, a Actor, key MemoryKey, version int64) (*store.MemoryVersion, error) {
	k, err := cleanMemoryKey(key)
	if err != nil {
		return nil, err
	}
	u, p, _, err := s.memoryAccess(ctx, a, k.Project)
	if err != nil {
		return nil, err
	}
	m, err := store.MemoryByKey(ctx, s.DB, p.ID, k.Scope, memoryOwner(k.Scope, u), k.Kind, k.Name, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errMemoryNotFound(k)
	}
	if err != nil {
		return nil, err
	}
	if m.Version == 0 {
		return nil, errMemoryNotFound(k)
	}
	v, err := store.MemoryVersionContent(ctx, s.DB, m.ID, version)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.memory.version_not_found", "kind", k.Kind, "name", k.Name, "version", version))
	}
	if err != nil {
		return nil, err
	}
	if v.Purged {
		return nil, errm(NotFound, "memory_version_purged", i18n.M("service.err.memory.version_purged", "kind", k.Kind, "name", k.Name, "version", version))
	}
	return &v, nil
}

// WriteMemory は記憶を書く（op: create / append / replace / delete）。検査に外れたら何も書かない。
//   - create: 行が無いか消した印のものなら作る。あれば 409 memory_exists
//   - append: 版を問わず節を末尾に足す（行が無いか消した印のものなら、その節だけの本文で作る）
//   - replace: If-Match の版が今の版と同じときだけ本文の全体を置き換える（行が無いときは If-Match 0 で作る）
//   - delete: 消した印の版を足す（本文は履歴に残る。本体を消すのは memory_purge.go）。If-Match があれば照合する
//
// 書いた版の本体が MaxMemoryVersions を超えたら、古い版から本体を間引く（同じトランザクションで memory_purges に残す）。
func (s *Service) WriteMemory(ctx context.Context, a Actor, in MemoryWriteInput) (*MemoryResult, error) {
	k, err := cleanMemoryKey(in.MemoryKey)
	if err != nil {
		return nil, err
	}
	op := strings.TrimSpace(in.Op)
	if !slices.Contains([]string{store.MemoryOpCreate, store.MemoryOpAppend, store.MemoryOpReplace, store.MemoryOpDelete}, op) {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.memory.op"))
	}
	label, err := cleanTranscriptLabel("machine_label", in.MachineLabel)
	if err != nil {
		return nil, err
	}
	if f, ok := domain.FindSecret(label); ok {
		return nil, errm(Rejected, "memory_secret", i18n.M("service.err.memory.secret_field", "field", "machine_label", "label", f.Label))
	}
	if in.IdempotencyKey != "" && !memoryIdempotencyKeyRe.MatchString(in.IdempotencyKey) {
		return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.memory.idempotency_key", "max", 64))
	}
	var writtenAt time.Time
	if in.WrittenAt != "" {
		if writtenAt, err = time.Parse(time.RFC3339, in.WrittenAt); err != nil {
			return nil, errm(Invalid, "invalid_argument", i18n.M("service.err.memory.written_at"))
		}
	}
	expected, err := parseMemoryIfMatch(in.IfMatch, op == store.MemoryOpReplace)
	if err != nil {
		return nil, err
	}
	switch op {
	case store.MemoryOpAppend:
		err = checkMemoryText(k, in.Content, MaxMemoryAppendBytes)
	case store.MemoryOpCreate, store.MemoryOpReplace:
		err = checkMemoryText(k, in.Content, MaxMemoryBytes)
	}
	if err != nil {
		return nil, err
	}
	u, p, role, err := s.memoryAccess(ctx, a, k.Project)
	if err != nil {
		return nil, err
	}
	if !canWriteMemory(k.Scope, role) {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.memory.forbidden", "slug", p.Slug))
	}
	owner := memoryOwner(k.Scope, u)
	via := a.Via
	if via == "" {
		via = "api"
	}
	res := MemoryResult{ProjectSlug: p.Slug}
	// 新しい行を作りうる操作だけ、ロックの前に行を用意する（消す操作と、版を指定した置き換えは、行が無ければ何も書かない）
	if op != store.MemoryOpDelete && !(op == store.MemoryOpReplace && expected != 0) {
		now := s.Now()
		if err := store.EnsureMemoryRow(ctx, s.DB, store.Memory{ProjectID: p.ID, Scope: k.Scope, OwnerUserID: owner, Kind: k.Kind, Name: k.Name,
			CreatedAt: now, UpdatedAt: now}); err != nil {
			return nil, err
		}
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		// 記憶の行のロックをトランザクションの最初の文にする（今の本文と版はこのロックの読みで取る。store.MemoryByKey）
		cur, err := store.MemoryByKey(ctx, tx, p.ID, k.Scope, owner, k.Kind, k.Name, true)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		exists := err == nil && cur.Version > 0 // 下書きの行は無いものと同じに扱う
		if err := checkProjectActive(ctx, tx, p); err != nil {
			return err
		}
		if in.IdempotencyKey != "" && op == store.MemoryOpAppend && cur.ID != 0 {
			// 行のロックの中で見るので、同時の再送も 1 回だけ足す
			prev, err := store.MemoryVersionByIdempotencyKey(ctx, tx, cur.ID, in.IdempotencyKey)
			switch {
			case err == nil:
				// 同じ利用者の同じ節の再送だけを今の版で返す。ほかは鍵の取り違えなので黙って捨てない
				// 本体を消した版は節を照らせないので、書いた利用者だけを見る（送り直しで消した節を戻さないよう、足さずに返す）
				same := prev.Purged || strings.HasSuffix(strings.TrimRight(prev.Content, "\r\n"), strings.TrimRight(in.Content, "\r\n"))
				if prev.UserID != u.ID || !same {
					return errm(Conflict, "memory_idempotency_conflict", i18n.M("service.err.memory.idempotency_conflict"))
				}
				// 再送の後に別の版が足されていても、今の版ではなくその鍵の版を返す（呼んだ側が他人の版を自分の書き込みと見ないように）
				res.Memory = cur
				res.Memory.Version, res.Memory.UpdatedAt = prev.Version, prev.CreatedAt
				if !prev.Purged {
					res.Memory.Summary = prev.Summary
				}
				res.Memory.UpdatedBy, res.Memory.UpdatedByLogin = prev.UserID, prev.UserLogin
				return nil
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
		}
		var content string
		switch op {
		case store.MemoryOpCreate:
			if exists && !cur.Deleted {
				return conflictExists(k, cur.Version)
			}
			content = in.Content
		case store.MemoryOpAppend:
			base := ""
			if exists && !cur.Deleted {
				base = cur.Content
			}
			content = joinMemorySection(base, in.Content)
		case store.MemoryOpReplace:
			if cur.Version != expected {
				return memoryVersionConflict(k, cur.Version, expected)
			}
			content = in.Content
		case store.MemoryOpDelete:
			if !exists || cur.Deleted {
				return errMemoryNotFound(k)
			}
			if expected >= 0 && cur.Version != expected {
				return memoryVersionConflict(k, cur.Version, expected)
			}
			content = cur.Content
		}
		if cur.ID == 0 { // 行を用意した後に指す先が無くなった（プロジェクトの行が無いなど）
			return errMemoryNotFound(k)
		}
		sum := cur.Summary
		if op != store.MemoryOpDelete {
			if sum, err = checkMemoryResult(k, content); err != nil {
				return err
			}
		}
		now := s.Now()
		next := cur
		next.Version = cur.Version + 1
		next.Summary, next.Content, next.Deleted, next.UpdatedAt, next.UpdatedBy = sum, content, op == store.MemoryOpDelete, now, u.ID
		if err := store.UpdateMemory(ctx, tx, next); err != nil {
			return err
		}
		v := store.MemoryVersion{MemoryID: next.ID, Version: next.Version, Op: op, Summary: sum, Content: content, UserID: u.ID,
			MachineLabel: label, SessionID: a.SessionID, TokenID: a.TokenID, Via: via, CreatedAt: now, IdempotencyKey: in.IdempotencyKey}
		if !writtenAt.IsZero() && (op == store.MemoryOpAppend || op == store.MemoryOpReplace) {
			v.WrittenAt = writtenAt
			if writtenAt.After(now) {
				v.WrittenAt = now
			}
		}
		if err := store.InsertMemoryVersion(ctx, tx, v); err != nil {
			return err
		}
		if err := s.thinMemoryBodies(ctx, tx, next.ID); err != nil {
			return err
		}
		next.UpdatedByLogin = u.Login
		res.Memory, res.Created = next, cur.Version == 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func conflictExists(k MemoryKey, current int64) *Error {
	e := errm(Conflict, "memory_exists", i18n.M("service.err.memory.exists", "kind", k.Kind, "name", k.Name, "version", current))
	e.CurrentSeq = &current
	return e
}
