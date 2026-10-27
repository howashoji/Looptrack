package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 本体を sha256 の名前でディスクに置く部品（添付と会話記録で共有する。docs/server/DESIGN.md §2-4・§2-5）。
// どれもルート（置き場）を引数に取り、メタデータ（DB）には触れない。どの表から指されているかの判定は呼び出し元が持つ。
//
// 本体は <ルート>/<sha256 の先頭 2 字>/<次の 2 字>/<sha256> に置く。書き込みは <ルート>/.tmp の一時ファイル → fsync → rename の順で、
// 既にある本体は上書きしない（更新時刻だけを進める）。添付と会話記録はルートを分ける。同じルートに置くと、片方の整合の検査が
// もう片方の本体を「どこからも指されない本体」として消すため（ルートが入れ子の設定は TranscriptRootConflict が見つける）。

// blobTmpDir は書き込み途中の一時ファイルを置く、ルートの中のディレクトリ（rename を同じファイルシステムの中で済ませるため）。
const blobTmpDir = ".tmp"

// errBlobTooLarge は writeBlobTemp が上限を超えた本体を拒んだことを表す（利用者に見せる文面は呼び出し元が決める）。
var errBlobTooLarge = errors.New("blob exceeds the size limit")

// blobBodyPath は本体のパス（<root>/ab/cd/<sha256>）。
func blobBodyPath(root, sum string) string {
	return filepath.Join(root, sum[:2], sum[2:4], sum)
}

// writeBlobTemp は本体をルートの一時ファイルに書き、fsync して閉じる。max を超えたら errBlobTooLarge を返す。
// 失敗したら（上限を超えたときも）一時ファイルを消す。
func writeBlobTemp(root string, r io.Reader, max int64) (path, sum string, size int64, err error) {
	if r == nil {
		r = strings.NewReader("")
	}
	tmpDir := filepath.Join(root, blobTmpDir)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", "", 0, err
	}
	f, err := os.CreateTemp(tmpDir, "upload-*")
	if err != nil {
		return "", "", 0, err
	}
	path = f.Name()
	fail := func(e error) (string, string, int64, error) {
		f.Close()
		os.Remove(path)
		return "", "", 0, e
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, max+1))
	if err != nil {
		return fail(err)
	}
	if n > max {
		return fail(errBlobTooLarge)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", "", 0, err
	}
	return path, hex.EncodeToString(h.Sum(nil)), n, nil
}

// placeBlob は一時ファイルを本体のパスへ rename する。本体が既にあれば上書きせず、一時ファイルを捨てて
// 本体の更新時刻だけを進める（整合の検査が、記録の直前の本体を「どこからも指されない」として消さないように。OrphanGrace）。
// 何度呼んでも結果は同じ（トランザクションの再試行で 2 回目に呼ばれても、本体があれば何もしない）。
// あると確かめた後に別のプロセスが本体を外した（removeBlobGuarded）ときは、更新時刻を進められないので自分の一時ファイルを置く
// （一時ファイルは更新時刻を進められてから捨てる）。
func placeBlob(root, tmp, sum string) error {
	final := blobBodyPath(root, sum)
	if _, err := os.Lstat(final); err == nil {
		if placeBlobAfterLstat != nil {
			placeBlobAfterLstat(final)
		}
		now := time.Now()
		err := os.Chtimes(final, now, now)
		if err == nil {
			os.Remove(tmp)
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	syncDir(filepath.Dir(final))
	return nil
}

// placeBlobAfterLstat は、placeBlob が本体のあることを確かめた直後に呼ばれる（nil なら呼ばない）。テストが、その後に
// 別のプロセスが本体を外した形を作るためだけにある。
var placeBlobAfterLstat func(final string)

// removeBlobAfterAside は、removeBlobGuarded が本体を外した直後に呼ばれる（nil なら呼ばない）。テストが、外す前の確かめの後に
// 追記が印を作った形を作るためだけにある（外した後の確かめが本体を戻すことを、外す前の確かめと分けて確かめる）。
var removeBlobAfterAside func(sum string)

// removeBlobNamedAside は、removeBlobGuarded が外す先の名前を取った後・外す前に呼ばれる（nil なら呼ばない）。テストが、
// その間に整合の検査が .tmp を片付ける形を作るためだけにある。
var removeBlobNamedAside func(aside string)

// recoverAsideAfterRead は、recoverAside が外した本体を開いて読んだ後・捨てるか戻す前に呼ばれる（nil なら呼ばない）。
// テストが、その間に同じ名前へ別のファイルが入った形を作るためだけにある。
var recoverAsideAfterRead func(aside string)

// syncDir は rename をディスクに残すため、ディレクトリを fsync する（できない OS・ファイルシステムでは何もしない）。
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// walkBlobs はルートの中の本体の形（<2 字>/<2 字>/<sha256> の通常のファイル）をしたものだけを fn に渡す。
// 一時ファイルのディレクトリと、本体の形でないもの（ほかのファイル・深さの違うもの）は見ない。ルートがまだ無ければ何もしない。
// fn に渡すパスは設定したルートの下の形（途中のリンクは os.Remove も辿る）。
func walkBlobs(root string, fn func(path, sum string, info fs.FileInfo) error) error {
	// WalkDir は根がシンボリックリンクだとその中へ降りない（黙って 0 件になる）ので、根だけは先に解いてから辿る。
	real := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		real = r
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == real { // ルートがまだ無い（1 度も書いていない）
				return filepath.SkipDir
			}
			return err
		}
		rel, err := filepath.Rel(real, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == blobTmpDir {
				return filepath.SkipDir
			}
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 || !sha256Re.MatchString(parts[2]) || parts[0] != parts[2][:2] || parts[1] != parts[2][2:4] || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return fn(filepath.Join(root, rel), parts[2], info)
	})
}

// removeOrphanBlobs は整合の検査が見つけた、ルート root のどこからも指されない本体を消す（Recent のものは消さない）。
// 1 件ずつ removeBlobGuarded で消す。指す行を数え、本体を .tmp へ外し、置いている別のプロセスの印を探し、数え直し、
// 触れられて OrphanGrace が経っていない本体も残す（検査の後に同じ内容が書かれたとき・別のプロセスが本体を置いて記録を確定する
// 前のときに消さないため。stat してから消すと、その間に「既にある」と見て置くのを飛ばした別のプロセスの本体を消す）。
// live は添付と会話記録の両方の表を見る（ルートを取り違えた設定でも、もう片方が指す本体を消さないため）。
// check はその置き場の整合の検査の名前（removeBlobGuarded）。lock が nil でなければ、本体ごとに数えてから消すまでをそのロックの中で行う（このプロセスの中の追記と交わらないため）。
// 消したパスを返す。
func removeOrphanBlobs(root, check string, orphans []OrphanBody, lock func(sum string) func(), live func(sum string) (int, error)) ([]string, error) {
	var removed []string
	for _, o := range orphans {
		if o.Recent {
			continue
		}
		unlock := func() {}
		if lock != nil {
			unlock = lock(o.SHA256)
		}
		r, err := removeBlobGuarded(root, check, o.SHA256, true, func() (int, error) { return live(o.SHA256) })
		unlock()
		if err != nil {
			return removed, err
		}
		if r == blobRemoved {
			removed = append(removed, o.Path)
		}
	}
	return removed, nil
}

// blobLocks は本体（sha256）ごとの排他（ゼロ値で使える）。使っている間だけ表に載せ、誰も握らなくなったら外す。
type blobLocks struct {
	mu sync.Mutex
	m  map[string]*blobLock
}

type blobLock struct {
	mu   sync.Mutex
	refs int // 握っている・待っている数（表の出入りは blobLocks.mu の中で数える）
}

// lock は本体 sum の排他を取り、外す関数を返す。
func (l *blobLocks) lock(sum string) func() {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*blobLock{}
	}
	e := l.m[sum]
	if e == nil {
		e = &blobLock{}
		l.m[sum] = e
	}
	e.refs++
	l.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(l.m, sum)
		}
		l.mu.Unlock()
	}
}

// 本体を消す側と、別のプロセスで同じ本体を置いてから記録を確定するまでの間にある追記との取り合い。
// プロセスの中の排他（blobLocks）は別のプロセス（2 つ目のサーバ・サーバの隣で動く管理用のサブコマンド）に届かず、
// 利用者ごとの DB のロックの行は、同じ本体を指す別の利用者の追記を待たせない（同じ内容の本体は利用者をまたいで 1 つにまとまる）。
// 取り合うのは置き場のファイルなので、置き場の中に印を置いて順を決める（DB の種類にも、ロックの行の持ち主にも依らない）。
//
//	置く側: 印（.tmp/inflight-<sha256>-…）を作る → 本体を置く（placeBlob）→ 記録を確定する → 印を消す
//	消す側: 指す行を数える → 本体を .tmp へ rename して本体のパスから外す → 印を探す → 指す行を数え直す
//	        → 印か行があれば本体を戻し、どちらも無ければ消す（removeBlobGuarded）
//
// 置く側が本体を置き終えたのが外す前なら、外した後に見る印か、数え直す行のどちらかに必ず現れる（印を消すのは確定の後なので、
// 印が消えていれば確定した行が見える）。外した後に置くなら、置く側は本体が無いのを見て自分の一時ファイルを置く。
// 確かめてから消すまでの間に置かれる隙は、外す rename が 1 回で閉じる（stat してから消すと、その間の placeBlob を見落とす）。
// 印は OrphanGrace より古ければ見ない（落ちたプロセスが残した印で、本体をいつまでも消せなくならないため）。置く側は置いてから
// 確定するまでを OrphanGrace より十分に短い時間に収める（blobPlaceWindow）。

// blobPlaceWindow は、追記と添付が本体を置いてから記録を確定するまでに許す時間。置いている間の印（beginBlobPlace）は
// OrphanGrace（1 時間）より古ければ消す側が見ないので、それより十分に短くする。MySQL のロック待ち（innodb_lock_wait_timeout の
// 既定 50 秒）を inTx の 5 回の試行で重ねても収まる長さ。越えたら記録せずに失敗する（置いた本体は失敗の片付けが扱う）。
const blobPlaceWindow = 10 * time.Minute

// 本体を消す側が失敗の文面で名指しする、その置き場の整合の検査（外した本体を戻しうる管理のサブコマンド）。
const (
	repairTranscriptsCmd = "repair-transcripts"
	repairAttachmentsCmd = "repair-attachments"
)

// 置き場の .tmp に置く印と、外した本体の名前の頭。
const (
	blobInflightPrefix = "inflight-"
	blobRemovePrefix   = "remove-"
)

// beginBlobPlace は本体 sum を置いてから記録を確定するまでの印を作り、印を消す関数を返す（置く前に呼ぶ。作れなければ置かずに止める）。
func beginBlobPlace(root, sum string) (func(), error) {
	tmpDir := filepath.Join(root, blobTmpDir)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(tmpDir, blobInflightPrefix+sum+"-*")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}

// blobPlaceInFlight は本体 sum を置いて記録を確定する前の追記の印があるかを返す。OrphanGrace より古い印は数えずに消す。
func blobPlaceInFlight(root, sum string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(root, blobTmpDir))
	if err != nil {
		return false, err
	}
	prefix := blobInflightPrefix + sum + "-"
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		path := filepath.Join(root, blobTmpDir, e.Name())
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // 確定して消えた（その記録は、この後の数え直しで見える）
		}
		if err != nil {
			found = true // 古さを確かめられない印は、置いている追記として数える（本体を残す側に倒す）
			continue
		}
		if time.Since(info.ModTime()) >= OrphanGrace {
			os.Remove(path)
			continue
		}
		found = true
	}
	return found, nil
}

// blobRemoval は removeBlobGuarded の結果。
type blobRemoval int

const (
	blobKept    blobRemoval = iota // 指す行か置こうとしている追記があった（keepRecent なら新しかった）ので残した
	blobRemoved                    // 消した
	blobAbsent                     // 初めから無かった
)

// removeBlobGuarded は、本体 sum を指す行（live が数える）も、置いてから記録を確定する前の追記も無ければ本体を消す（上のやり方）。
// check はその置き場の整合の検査の名前（repairTranscriptsCmd・repairAttachmentsCmd。外した本体を戻されたときの文面に出す）。
// keepRecent なら、触れられて OrphanGrace が経っていない本体も残す（整合の検査）。ファイルを外せない・消せない・戻せないときは
// *bodyRemoveError（DB の読み出しの失敗はそのまま返す）。どの失敗でも、外した本体は本体のパスへ戻してから返す。
func removeBlobGuarded(root, check, sum string, keepRecent bool, live func() (int, error)) (blobRemoval, error) {
	n, err := live()
	if err != nil || n > 0 {
		return blobKept, err
	}
	// 外す前にも印を探す（置いている追記が見えていれば、外して戻す手間をかけない。外した後の確かめは省かない）
	if inFlight, err := blobPlaceInFlight(root, sum); err == nil && inFlight {
		return blobKept, nil
	}
	final := blobBodyPath(root, sum)
	tmpDir := filepath.Join(root, blobTmpDir)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return blobKept, &bodyRemoveError{err: err}
	}
	f, err := os.CreateTemp(tmpDir, asidePattern(sum, time.Now()))
	if err != nil {
		return blobKept, &bodyRemoveError{err: err}
	}
	aside := f.Name()
	f.Close()
	if removeBlobNamedAside != nil {
		removeBlobNamedAside(aside)
	}
	if err := os.Rename(final, aside); err != nil {
		os.Remove(aside)
		if errors.Is(err, fs.ErrNotExist) {
			return blobAbsent, nil
		}
		return blobKept, &bodyRemoveError{err: err}
	}
	if removeBlobAfterAside != nil {
		removeBlobAfterAside(sum)
	}
	keep, why := false, error(nil)
	if keepRecent {
		if info, err := os.Stat(aside); err == nil && time.Since(info.ModTime()) < OrphanGrace {
			keep = true // 外す前に placeBlob が触れた（記録しようとしている）
		}
	}
	if !keep {
		if keep, why = blobPlaceInFlight(root, sum); why != nil {
			keep, why = true, &bodyRemoveError{err: why}
		}
	}
	if !keep {
		// 印の後に数え直す（印が消えていれば、その追記の記録は確定している）
		if n, why = live(); why != nil || n > 0 {
			keep = true
		}
	}
	if !keep {
		err := os.Remove(aside)
		if err == nil {
			return blobRemoved, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			// 外した本体を、別のプロセスの整合の検査が戻した（recoverBlobLeftovers）。本体はどこからも指されないまま
			// 本体のパスに残るので、消せなかったものとして知らせる（黙って残すと、消去したはずの中身が残る）
			return blobKept, &bodyRemoveError{err: i18n.Errorf("service.err.blob.body_put_back", "check", check)}
		}
		why = &bodyRemoveError{err: err}
	}
	if err := restoreBlob(aside, final); err != nil {
		return blobKept, &bodyRemoveError{err: errors.Join(why, err)}
	}
	return blobKept, why
}

// restoreBlob は外した本体を本体のパスへ戻す。外した後に同じ内容が置かれていれば（中身は sha256 で同じ）、外したほうを捨てる。
// 外した本体がもう無ければ（別のプロセスの整合の検査が戻したか捨てた）何もしない。
func restoreBlob(aside, final string) error {
	if _, err := os.Lstat(final); err == nil {
		if err := os.Remove(aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	if err := os.Rename(aside, final); err != nil {
		// 外した本体が無いのは、別のプロセスの整合の検査が先に戻したときだけ正しい。本体のパスにも無ければ本体を失っている
		if errors.Is(err, fs.ErrNotExist) {
			if _, serr := os.Lstat(final); serr == nil {
				return nil
			}
		}
		return err
	}
	syncDir(filepath.Dir(final))
	return nil
}

// asideMinAge は、外したまま残った本体を整合の検査が拾うまでの古さ。名前の時刻（外す直前に名前を取った時刻）で数える
// （rename は本体の更新時刻を保つので、ファイルの時刻では外した時刻が分からない）。消す側は、名前を取ってから消すか戻すまでの間に
// 外した本体を使う。その間に検査が拾うと、まだ動いている消す側と取り合う（名前だけ取った空のファイルを開いた検査が、
// 後から同じ名前に入った本体を捨てる）。消す側の 1 本体分は DB の数えを 2 回とファイルの操作だけで、文脈の期限も持たないので、
// 追記の窓（blobPlaceWindow）ではなく、印と同じ OrphanGrace（1 時間）を使う。
const asideMinAge = OrphanGrace

// asidePattern は外す本体の名前の型（remove-<sha256>-<名前を取った時刻の UnixNano>-<乱数>。os.CreateTemp に渡す）。
func asidePattern(sum string, at time.Time) string {
	return blobRemovePrefix + sum + "-" + strconv.FormatInt(at.UnixNano(), 10) + "-*"
}

// parseAside は外した本体の名前から sha256 と、名前を取った時刻を読む。型に合わなければ false（拾わない）。
func parseAside(name string) (string, time.Time, bool) {
	rest, ok := strings.CutPrefix(name, blobRemovePrefix)
	if !ok || len(rest) < 66 || rest[64] != '-' || !sha256Re.MatchString(rest[:64]) {
		return "", time.Time{}, false
	}
	stamp, _, ok := strings.Cut(rest[65:], "-")
	if !ok || stamp == "" {
		return "", time.Time{}, false
	}
	ns, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || ns <= 0 {
		return "", time.Time{}, false
	}
	return rest[:64], time.Unix(0, ns), true
}

// blobLeftovers は、途中で止まった削除と追記が置き場の .tmp に残したものの数え（recoverBlobLeftovers）。
type blobLeftovers struct {
	Asides     int // 外したまま残った本体（removeBlobGuarded が外した後に止まった）
	Restored   int // そのうち本体のパスへ戻したもの（本体のパスが空いていた）
	Dropped    int // そのうち捨てたもの（本体のパスに同じ内容があった）
	StaleMarks int // OrphanGrace より古い、置いている間の印（落ちた追記が残した）
}

// recoverBlobLeftovers は .tmp に残った、外したままの本体と古い印を数え、apply なら片付ける。
// 外した本体は、本体のパスが空いていれば戻し（指す行が無ければ、続く整合の検査がふつうの規則で消す）、空いていなければ捨てる。
// .tmp は walkBlobs が見ないので、ここで拾わないと、止まった削除が外した本体はどの検査からも見えない
// （消去したはずの中身が残り、外す前に触れて後から確定した追記の本体はずっと欠けたままになる）。
// lock が nil でなければ、外した本体ごとにそのロックの中で扱う（このプロセスの消去と交わらないため）。
func recoverBlobLeftovers(root string, apply bool, lock func(sum string) func()) (blobLeftovers, error) {
	var res blobLeftovers
	tmpDir := filepath.Join(root, blobTmpDir)
	entries, err := os.ReadDir(tmpDir)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	sumOf := func(name, prefix string) (string, bool) {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok || len(rest) < 65 || rest[64] != '-' || !sha256Re.MatchString(rest[:64]) {
			return "", false
		}
		return rest[:64], true
	}
	for _, e := range entries {
		path := filepath.Join(tmpDir, e.Name())
		if _, ok := sumOf(e.Name(), blobInflightPrefix); ok {
			info, err := os.Stat(path)
			if err != nil || time.Since(info.ModTime()) < OrphanGrace {
				continue
			}
			res.StaleMarks++
			if apply {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return res, err
				}
			}
			continue
		}
		sum, at, ok := parseAside(e.Name())
		if !ok || !e.Type().IsRegular() || time.Since(at) < asideMinAge {
			continue // まだ動いている消す側が使っているかもしれない
		}
		if !apply {
			res.Asides++
			continue
		}
		unlock := func() {}
		if lock != nil {
			unlock = lock(sum)
		}
		got, err := recoverAside(path, blobBodyPath(root, sum), sum)
		unlock()
		if err != nil {
			return res, err
		}
		switch got {
		case asideRestored:
			res.Asides++
			res.Restored++
		case asideDropped:
			res.Asides++
			res.Dropped++
		}
	}
	return res, nil
}

// asideOutcome は recoverAside の結果。
type asideOutcome int

const (
	asideSkipped  asideOutcome = iota // 扱わなかった（無くなっていた・確かめている間に別のファイルに替わった）
	asideRestored                     // 本体のパスへ戻した
	asideDropped                      // 捨てた
)

// recoverAside は外したまま残った本体 1 つを、本体のパスが空いていて中身が sum と合えば戻し、そうでなければ捨てる。
// 中身を確かめるのは、外す前に名前だけ取った空のファイル（removeBlobGuarded の CreateTemp）が、止まった削除の残りとして
// 同じ名前で残りうるため（戻すと、中身の違う本体になる）。開いたファイルと、捨てる・戻す直前にそのパスにあるファイルが
// 同じでなければ何もしない（確かめている間に、別のプロセスが同じ名前へ本体を入れたかもしれない。パスで捨てるとその本体を消す）。
func recoverAside(aside, final, sum string) (asideOutcome, error) {
	f, err := os.Open(aside)
	if errors.Is(err, fs.ErrNotExist) {
		return asideSkipped, nil
	}
	if err != nil {
		return asideSkipped, err
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return asideSkipped, err
	}
	_, finalErr := os.Lstat(final)
	match := false
	if finalErr != nil {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return asideSkipped, err
		}
		match = hex.EncodeToString(h.Sum(nil)) == sum
	}
	f.Close() // 開いたままでは、Windows は名前を変えることも消すこともできない
	if recoverAsideAfterRead != nil {
		recoverAsideAfterRead(aside)
	}
	if now, err := os.Lstat(aside); err != nil || !os.SameFile(opened, now) {
		return asideSkipped, nil
	}
	if finalErr == nil || !match {
		if err := os.Remove(aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return asideSkipped, err
		}
		return asideDropped, nil
	}
	if err := restoreBlob(aside, final); err != nil {
		return asideSkipped, err
	}
	return asideRestored, nil
}
