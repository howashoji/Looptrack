package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 記憶の版の本体を消す 4 つの経路（DESIGN.md §9-9）。版の行は memory_versions に残り、本文と要約（memory_bodies）だけが消える。
// どの経路も記憶の行を FOR UPDATE してから消し、同じトランザクションで memory_purges に 1 行を足す。だから本体の無い版には必ず記録がある。
//
//   - 書ける利用者: PurgeMemory。project の範囲はプロジェクトの admin とサーバの管理者、personal は本人
//   - 運用者: PurgeOperatorMemory（looptrack purge-memory。LOOPTRACK_DSN の経路で利用者を持たない。他人の personal にも届く）
//   - 保持期間: PurgeExpiredMemories（サーバの定期の処理。既定は無期限）
//   - 版の数の上限: thinMemoryBodies（WriteMemory の中）

// MaxMemoryVersions は記憶 1 件が本体を持つ版の数の上限。1 件の本体の合計は MaxMemoryBytes × この値（6.4 MiB）で止まる。
const MaxMemoryVersions = 100

// memorySystemVia は利用者も運用者も介さない消去の経路（memory_purges.via の CHECK にある値）。
const memorySystemVia = "system"

// memoryOperatorVia は運用者の消去の経路。
const memoryOperatorVia = "admin"

// memoryVersionVia は消去の経路を版の行の経路に直す（memory_versions.via の CHECK に system は無いので admin にする）。
func memoryVersionVia(via string) string {
	if via == memorySystemVia {
		return memoryOperatorVia
	}
	return via
}

// MemoryPurgeResult は本体を消した（dry run では消す）結果。
type MemoryPurgeResult struct {
	Project string
	Scope   string
	Kind    string
	Name    string
	// Bodies は本体の数、Bytes はその合計（バイト）
	Bodies int
	Bytes  int64
	// Current は今の版の本体を含むか（含むと記憶は消した印になる）
	Current bool
}

// MemoryRetentionResult は保持期間の処理の結果。
type MemoryRetentionResult struct {
	RetentionDays int64
	Memories      int // 本体を消した記憶の数
	Bodies        int
}

// OperatorMemoryTarget は運用者が消す記憶を指す。Owner は personal の持ち主の login（空なら project の範囲）。
type OperatorMemoryTarget struct {
	Project string
	Owner   string
	Kind    string
	Name    string
	MemoryPurgeSelect
}

// MemoryPurgeSelect は消す版の指定。Version（1 以上）か AllVersions のどちらか 1 つだけを指す。
// 全部の版を明示の指定に分けるのは、値が抜けた要求（0）で全部が消えないようにするため。
type MemoryPurgeSelect struct {
	Version     int64
	AllVersions bool
}

// check は指定が 1 つだけかを確かめる。
func (p MemoryPurgeSelect) check() error {
	if p.AllVersions == (p.Version > 0) || p.Version < 0 {
		return errm(Invalid, "invalid_argument", i18n.M("service.err.memory.purge_select"))
	}
	return nil
}

// memoryPurgeVersions は消す版を選ぶ（ロックの中で呼ぶ）。
func memoryPurgeVersions(ctx context.Context, q *sql.Tx, k MemoryKey, m store.Memory, sel MemoryPurgeSelect) ([]store.MemoryBodySize, error) {
	sizes, err := store.MemoryBodySizes(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	return pickMemoryBodies(k, m, sizes, sel)
}

// pickMemoryBodies は本体の一覧（古い順）から消す版を選ぶ。指した版が無い・既に消してあるときは 404、本文が後の版に残るときは 409。
func pickMemoryBodies(k MemoryKey, m store.Memory, sizes []store.MemoryBodySize, sel MemoryPurgeSelect) ([]store.MemoryBodySize, error) {
	if sel.AllVersions {
		return sizes, nil
	}
	version := sel.Version
	if version > m.Version {
		return nil, errm(NotFound, "not_found", i18n.M("service.err.memory.version_not_found", "kind", k.Kind, "name", k.Name, "version", version))
	}
	for i, b := range sizes {
		if b.Version != version {
			continue
		}
		// 追記の版は前の本文を含むので、この版だけを消しても本文は次の版から読める。消えたと思わせないよう、何も消さずに拒む
		text := strings.TrimRight(b.Content, "\r\n")
		for _, later := range sizes[i+1:] {
			if text != "" && strings.Contains(later.Content, text) {
				return nil, errm(Conflict, "memory_purge_text_remains", i18n.M("service.err.memory.purge_text_remains", "kind", k.Kind, "name", k.Name, "version", version, "later", later.Version))
			}
		}
		return []store.MemoryBodySize{b}, nil
	}
	return nil, errm(NotFound, "memory_version_purged", i18n.M("service.err.memory.version_purged", "kind", k.Kind, "name", k.Name, "version", version))
}

// applyMemoryPurge は選んだ版の本体を消して記録を足す（ロックの中で呼ぶ）。今の版を含むなら記憶の行の本文も外し、本体の無い版を 1 つ足して
// 版を進める。進めないと、消す前に読んだ版の置き換えが照合を通り、手元の本文でサーバの本文を戻してしまう。
func (s *Service) applyMemoryPurge(ctx context.Context, tx *sql.Tx, m store.Memory, bodies []store.MemoryBodySize, p store.MemoryPurge) (*MemoryPurgeResult, error) {
	res := &MemoryPurgeResult{Kind: m.Kind, Name: m.Name, Scope: m.Scope}
	if len(bodies) == 0 {
		return res, nil
	}
	versions := make([]int64, 0, len(bodies))
	for _, b := range bodies {
		versions = append(versions, b.Version)
		res.Bytes += b.Bytes
		res.Current = res.Current || b.Version == m.Version
	}
	res.Bodies = len(bodies)
	if err := store.DeleteMemoryBodies(ctx, tx, m.ID, versions); err != nil {
		return nil, err
	}
	now := s.Now()
	through := versions[len(versions)-1]
	if res.Current {
		through = m.Version + 1
		if err := store.InsertMemoryVersionRow(ctx, tx, store.MemoryVersion{MemoryID: m.ID, Version: through, Op: store.MemoryOpDelete, UserID: p.ActorUserID,
			Via: memoryVersionVia(p.Via), CreatedAt: now}); err != nil {
			return nil, err
		}
		if err := store.ClearMemoryContent(ctx, tx, m.ID, through, p.ActorUserID, now); err != nil {
			return nil, err
		}
	}
	p.MemoryID, p.FromVersion, p.ThroughVersion, p.Bodies, p.At = m.ID, versions[0], through, len(versions), now
	if err := store.InsertMemoryPurge(ctx, tx, p); err != nil {
		return nil, err
	}
	return res, nil
}

// cleanMemoryPurgeReason は消す理由を確かめる（必須。記録に残るので資格情報の形は拒む）。
func cleanMemoryPurgeReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", errm(Invalid, "invalid_argument", i18n.M("service.err.memory.purge_reason"))
	}
	if f, ok := domain.FindSecret(reason); ok {
		return "", errm(Rejected, "memory_secret", i18n.M("service.err.memory.secret_field", "field", "reason", "label", f.Label))
	}
	return reason, nil
}

// canPurgeMemory は利用者が本体を消せるか。personal は本人（持ち主はキーの一部なので、引けた時点で本人）、project はプロジェクトの admin か
// サーバの管理者。editor には許さない（書き換えは版が残るが、消去は戻せない）。
func canPurgeMemory(scope, role string, u store.User) bool {
	if scope == store.MemoryScopePersonal {
		return role != ""
	}
	return role == "admin" || u.Role == "admin"
}

// PurgeMemory は権限のある利用者が記憶の版の本体を消す。理由は必須。
func (s *Service) PurgeMemory(ctx context.Context, a Actor, key MemoryKey, sel MemoryPurgeSelect, reason string) (*MemoryPurgeResult, error) {
	k, err := cleanMemoryKey(key)
	if err != nil {
		return nil, err
	}
	if err := sel.check(); err != nil {
		return nil, err
	}
	if reason, err = cleanMemoryPurgeReason(reason); err != nil {
		return nil, err
	}
	u, p, role, err := s.memoryAccess(ctx, a, k.Project)
	if err != nil {
		return nil, err
	}
	if !canPurgeMemory(k.Scope, role, u) {
		return nil, errm(Forbidden, "forbidden", i18n.M("service.err.memory.purge_forbidden", "slug", p.Slug))
	}
	via := a.Via
	if via == "" {
		via = "api"
	}
	return s.purgeMemoryByKey(ctx, k, p, memoryOwner(k.Scope, u), sel, store.MemoryPurge{Cause: store.MemoryPurgeUser, ActorUserID: u.ID, Via: via, Reason: reason})
}

// purgeMemoryByKey はキーの記憶の本体を消す。
func (s *Service) purgeMemoryByKey(ctx context.Context, k MemoryKey, p store.Project, owner int64, sel MemoryPurgeSelect, rec store.MemoryPurge) (*MemoryPurgeResult, error) {
	var res *MemoryPurgeResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		m, err := store.MemoryByKey(ctx, tx, p.ID, k.Scope, owner, k.Kind, k.Name, true)
		if errors.Is(err, store.ErrNotFound) || (err == nil && m.Version == 0) {
			return errMemoryNotFound(k)
		}
		if err != nil {
			return err
		}
		bodies, err := memoryPurgeVersions(ctx, tx, k, m, sel)
		if err != nil {
			return err
		}
		res, err = s.applyMemoryPurge(ctx, tx, m, bodies, rec)
		return err
	})
	if err != nil {
		return nil, err
	}
	res.Project = p.Slug
	return res, nil
}

// operatorMemoryKey は運用者の指す先を、キー・プロジェクト・持ち主の ID に直す。
func (s *Service) operatorMemoryKey(ctx context.Context, t OperatorMemoryTarget) (MemoryKey, store.Project, int64, error) {
	if err := t.check(); err != nil {
		return MemoryKey{}, store.Project{}, 0, err
	}
	scope := store.MemoryScopeProject
	if strings.TrimSpace(t.Owner) != "" {
		scope = store.MemoryScopePersonal
	}
	k, err := cleanMemoryKey(MemoryKey{Project: t.Project, Scope: scope, Kind: t.Kind, Name: t.Name})
	if err != nil {
		return k, store.Project{}, 0, err
	}
	p, err := store.ProjectBySlug(ctx, s.DB, k.Project)
	if errors.Is(err, store.ErrNotFound) {
		return k, p, 0, errm(NotFound, "not_found", i18n.M("service.err.not_found.project", "slug", k.Project))
	}
	if err != nil {
		return k, p, 0, err
	}
	if scope == store.MemoryScopeProject {
		return k, p, 0, nil
	}
	login := strings.TrimSpace(t.Owner)
	u, err := store.UserByLogin(ctx, s.DB, login)
	if errors.Is(err, store.ErrNotFound) {
		return k, p, 0, errm(NotFound, "not_found", i18n.M("service.err.transcript.no_login", "login", login))
	}
	return k, p, u.ID, err
}

// OperatorMemoryPreview は運用者の消去の対象の数と大きさを返す（looptrack purge-memory の dry run。何も変えない）。
func (s *Service) OperatorMemoryPreview(ctx context.Context, t OperatorMemoryTarget) (*MemoryPurgeResult, error) {
	k, p, owner, err := s.operatorMemoryKey(ctx, t)
	if err != nil {
		return nil, err
	}
	m, err := store.MemoryByKey(ctx, s.DB, p.ID, k.Scope, owner, k.Kind, k.Name, false)
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.Version == 0) {
		return nil, errMemoryNotFound(k)
	}
	if err != nil {
		return nil, err
	}
	sizes, err := store.MemoryBodySizes(ctx, s.DB, m.ID)
	if err != nil {
		return nil, err
	}
	bodies, err := pickMemoryBodies(k, m, sizes, t.MemoryPurgeSelect)
	if err != nil {
		return nil, err
	}
	res := &MemoryPurgeResult{Project: p.Slug, Scope: k.Scope, Kind: k.Kind, Name: k.Name, Bodies: len(bodies)}
	for _, b := range bodies {
		res.Bytes += b.Bytes
		res.Current = res.Current || b.Version == m.Version
	}
	return res, nil
}

// PurgeOperatorMemory はサーバの運用者が記憶の版の本体を消す（cause operator。looptrack purge-memory が呼ぶ）。理由は必須。
// 操作した利用者は記録しない（DB の資格情報で動く経路なので、利用者が居ない）。
func (s *Service) PurgeOperatorMemory(ctx context.Context, t OperatorMemoryTarget, reason string) (*MemoryPurgeResult, error) {
	k, p, owner, err := s.operatorMemoryKey(ctx, t)
	if err != nil {
		return nil, err
	}
	if reason, err = cleanMemoryPurgeReason(reason); err != nil {
		return nil, err
	}
	return s.purgeMemoryByKey(ctx, k, p, owner, t.MemoryPurgeSelect, store.MemoryPurge{Cause: store.MemoryPurgeOperator, Via: memoryOperatorVia, Reason: reason})
}

// PurgeExpiredMemories は保持期間（memory_retention_days）が切れた版の本体を消す（サーバの定期の処理が呼ぶ）。
// 消すのは今の版より古い版と、消した印の記憶の今の版。生きている記憶の今の版は残す。0（無期限）なら何も消さない。
// 1 件が失敗しても残りは続け、失敗はまとめて返す。
func (s *Service) PurgeExpiredMemories(ctx context.Context) (*MemoryRetentionResult, error) {
	days, err := store.ReadMemoryRetentionDays(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	res := &MemoryRetentionResult{RetentionDays: days}
	if days == 0 {
		return res, nil
	}
	cutoff := s.Now().AddDate(0, 0, -int(days))
	ids, err := store.ExpiredMemoryIDs(ctx, s.DB, cutoff)
	if err != nil {
		return res, err
	}
	var errs []error
	for _, id := range ids {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			m, err := store.MemoryByIDForUpdate(ctx, tx, id)
			if err != nil {
				return err
			}
			versions, err := store.ExpiredMemoryVersions(ctx, tx, m, cutoff)
			if err != nil {
				return err
			}
			r, err := s.applyMemoryPurge(ctx, tx, m, bodySizesOf(versions), store.MemoryPurge{Cause: store.MemoryPurgeRetention, Via: memorySystemVia,
				Reason: store.SettingMemoryRetentionDays + "=" + strconv.FormatInt(days, 10)})
			if err != nil {
				return err
			}
			if r.Bodies > 0 {
				res.Memories++
				res.Bodies += r.Bodies
			}
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

// bodySizesOf は版の番号だけの一覧を applyMemoryPurge に渡す形にする（大きさは数えない経路で使う）。
func bodySizesOf(versions []int64) []store.MemoryBodySize {
	out := make([]store.MemoryBodySize, 0, len(versions))
	for _, n := range versions {
		out = append(out, store.MemoryBodySize{Version: n})
	}
	return out
}

// thinMemoryBodies は本体を持つ版が MaxMemoryVersions を超えた分を古い版から消す（WriteMemory の行のロックの中で呼ぶ）。
// 今の版は最も新しいので残る。上限より多い版を持つ記憶は、次の書き込みでまとめて間引く。
func (s *Service) thinMemoryBodies(ctx context.Context, tx *sql.Tx, memoryID int64) error {
	versions, err := store.MemoryVersionsBeyond(ctx, tx, memoryID, MaxMemoryVersions)
	if err != nil || len(versions) == 0 {
		return err
	}
	if err := store.DeleteMemoryBodies(ctx, tx, memoryID, versions); err != nil {
		return err
	}
	return store.InsertMemoryPurge(ctx, tx, store.MemoryPurge{MemoryID: memoryID, FromVersion: versions[0], ThroughVersion: versions[len(versions)-1], Bodies: len(versions),
		Cause: store.MemoryPurgeThinning, At: s.Now(), Via: memorySystemVia, Reason: "max_versions=" + strconv.Itoa(MaxMemoryVersions)})
}
