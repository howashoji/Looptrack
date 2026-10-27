package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
)

// 本体を消す側（消去・purge-transcripts・失敗した追記の片付け・repair-transcripts）と、別のプロセスで同じ本体を置いてから
// 記録を確定するまでの追記との取り合い（blobstore.go の removeBlobGuarded）。別のプロセスは、DB と置き場だけを共有する
// 別の Service で作る（プロセスの中の排他を共有しないので、別のプロセスと同じ形になる）。

// otherProcess は e.s と DB と置き場だけを共有する別の Service を返す。
func (e *transcriptEnv) otherProcess() *Service {
	o := New(e.app, nil)
	o.AttachDir, o.TranscriptDir = e.s.AttachDir, e.s.TranscriptDir
	return o
}

// holdAppend は s の追記を、本体を置いた後・記録を確定する前で止めて返す。返した関数で続きを走らせ、追記の結果を受け取る。
// 待つのはどちらも 30 秒まで（越えたらテストを止める）。
func (e *transcriptEnv) holdAppend(s *Service, a Actor, in AppendTranscriptChunkInput) func() error {
	e.t.Helper()
	placed, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	e.t.Cleanup(let) // テストが途中で止まっても、止めた追記を残さない
	s.TranscriptPlaced = func(string) {
		close(placed)
		<-release
	}
	go func() {
		_, err := s.AppendTranscriptChunk(e.ctx, a, in)
		done <- err
	}()
	select {
	case <-placed:
	case err := <-done:
		e.t.Fatalf("前提が崩れています: 追記が本体を置く前に終わった: %v", err)
	case <-time.After(30 * time.Second):
		e.t.Fatal("前提が崩れています: 追記が 30 秒以内に本体を置かなかった")
	}
	return func() error {
		e.t.Helper()
		let()
		select {
		case err := <-done:
			s.TranscriptPlaced = nil
			return err
		case <-time.After(30 * time.Second):
			e.t.Fatal("止めた追記が、続けてから 30 秒以内に終わらなかった")
			return nil
		}
	}
}

func bodySum(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// tmpEntries は置き場の .tmp に残ったもの（置いている間の印・外した本体・一時ファイル）の名前。
func (e *transcriptEnv) tmpEntries() []string {
	e.t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.s.TranscriptDir, blobTmpDir))
	if err != nil && !os.IsNotExist(err) {
		e.t.Fatal(err)
	}
	var names []string
	for _, d := range entries {
		names = append(names, d.Name())
	}
	return names
}

// 消去（service の経路）は、別のプロセスで同じ本体を置いてから記録を確定する前の追記があれば、本体を消さない。
// その追記が確定した後は本体を読める。対照: 追記が確定した後の消去（置こうとしている追記が無い）は本体を消す。
func TestTranscriptPurgeKeepsBodyOfAppendInAnotherProcess(t *testing.T) {
	e := newTranscriptEnv(t)
	shared := line("shared body")
	body := TranscriptBodyPath(e.s.TranscriptDir, bodySum(shared))
	mine := e.mustRegister(e.editor, tSID)
	e.mustAppend(e.editor, chunkIn(mine.Transcript.ID, `"0"`, tSID+".jsonl", 0, shared))
	const sid2 = "00000000-0000-4000-8000-0000000000bb"
	theirs := e.mustRegister(e.padmin, sid2)

	finish := e.holdAppend(e.otherProcess(), e.padmin, chunkIn(theirs.Transcript.ID, `"0"`, sid2+".jsonl", 0, shared))
	res, err := e.s.PurgeTranscript(e.ctx, e.editor, mine.Transcript.ID, "while another process appends")
	if err != nil || !res.Changed || res.Removed != 0 {
		t.Fatalf("別のプロセスの追記が確定する前の消去 = %+v, %v（チャンクは消去済みになり、本体は消さない）", res, err)
	}
	if _, err := os.Stat(body); err != nil {
		t.Fatalf("別のプロセスの追記が確定する前に本体が消えた: %v", err)
	}
	if err := finish(); err != nil {
		t.Fatalf("止めた追記: %v", err)
	}
	if _, f, err := e.s.OpenTranscriptChunk(e.ctx, e.padmin, theirs.Transcript.ID, 1); err != nil {
		t.Fatalf("確定した追記の本体: %v", err)
	} else {
		f.Close()
	}

	// 対照: 置こうとしている追記が無ければ、どこからも指されなくなった本体は消える
	res, err = e.s.PurgeTranscript(e.ctx, e.padmin, theirs.Transcript.ID, "after commit")
	if err != nil || !res.Changed || res.Removed != 1 {
		t.Fatalf("確定した後の消去 = %+v, %v（本体を 1 つ消す）", res, err)
	}
	if _, err := os.Stat(body); !os.IsNotExist(err) {
		t.Fatalf("確定した後の消去で本体が残った: %v", err)
	}
	if left := e.tmpEntries(); len(left) != 0 {
		t.Fatalf(".tmp に残った: %v（印も外した本体も残さない）", left)
	}
}

// 2 つのプロセスが同じ内容を追記し、本体を作った側（P1）の追記が拒まれたとき、P1 の片付けは、本体を置かずに使った
// P2 の追記が確定する前に本体を消さない。対照: 置こうとしているほかの追記が無ければ、拒まれた追記の本体は残らない。
func TestTranscriptFailedAppendKeepsBodyOfAppendInAnotherProcess(t *testing.T) {
	e := newTranscriptEnv(t)
	shared := line("same content from two processes")
	body := TranscriptBodyPath(e.s.TranscriptDir, bodySum(shared))
	mine := e.mustRegister(e.editor, tSID)
	const sid2 = "00000000-0000-4000-8000-0000000000bb"
	theirs := e.mustRegister(e.padmin, sid2)

	// P1: 連番の違う追記（本体を置いた後、トランザクションの中で拒まれる）。P2: 同じ内容の正しい追記
	finishP1 := e.holdAppend(e.otherProcess(), e.editor, chunkIn(mine.Transcript.ID, `"5"`, tSID+".jsonl", 0, shared))
	if _, err := os.Stat(body); err != nil {
		t.Fatalf("前提が崩れています: P1 が本体を置いていない: %v", err)
	}
	finishP2 := e.holdAppend(e.otherProcess(), e.padmin, chunkIn(theirs.Transcript.ID, `"0"`, sid2+".jsonl", 0, shared))
	if err := finishP1(); errCode(err) != "transcript_seq" {
		t.Fatalf("前提が崩れています: P1 の追記 = %v（transcript_seq で拒まれること）", err)
	}
	if _, err := os.Stat(body); err != nil {
		t.Fatalf("P1 の片付けが、P2 の追記が確定する前に本体を消した: %v", err)
	}
	if err := finishP2(); err != nil {
		t.Fatalf("P2 の追記: %v", err)
	}
	if _, f, err := e.s.OpenTranscriptChunk(e.ctx, e.padmin, theirs.Transcript.ID, 1); err != nil {
		t.Fatalf("P2 の本体: %v", err)
	} else {
		f.Close()
	}

	// 対照: ほかに置こうとしている追記が無ければ、拒まれた追記が置いた本体は片付けで消える
	alone := line("rejected and alone")
	if _, err := e.appendChunk(e.editor, chunkIn(mine.Transcript.ID, `"5"`, tSID+".jsonl", 0, alone)); errCode(err) != "transcript_seq" {
		t.Fatalf("前提が崩れています: 対照の追記 = %v", err)
	}
	if _, err := os.Stat(TranscriptBodyPath(e.s.TranscriptDir, bodySum(alone))); !os.IsNotExist(err) {
		t.Fatalf("拒まれた追記の本体が残った: %v", err)
	}
	if left := e.tmpEntries(); len(left) != 0 {
		t.Fatalf(".tmp に残った: %v（印も外した本体も残さない）", left)
	}
}

// removeBlobGuarded の判定（DB を使わない）: 置いている間の印があれば残し、古い印は数えずに消す。数え直しで指す行が
// 現れれば残す。keepRecent なら新しい本体を残す。外した本体を .tmp に残さない。
func TestRemoveBlobGuarded(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * OrphanGrace)
	put := func(content string) (string, string) {
		t.Helper()
		sum := bodySum(content)
		p := blobBodyPath(root, sum)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return sum, p
	}
	none := func() (int, error) { return 0, nil }
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	tmpLeft := func() []string {
		t.Helper()
		entries, _ := os.ReadDir(filepath.Join(root, blobTmpDir))
		var names []string
		for _, d := range entries {
			names = append(names, d.Name())
		}
		return names
	}

	// 対照: 指す行も印も無い古い本体は消える
	sum, p := put("plain")
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, true, none); err != nil || r != blobRemoved || exists(p) {
		t.Fatalf("指す行も印も無い = %v, %v, 残った %v", r, err, exists(p))
	}
	// 初めから無い
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, none); err != nil || r != blobAbsent {
		t.Fatalf("無い本体 = %v, %v", r, err)
	}
	// 置いている間の印があれば残す
	sum, p = put("in flight")
	end, err := beginBlobPlace(root, sum)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, none); err != nil || r != blobKept || !exists(p) {
		t.Fatalf("印がある = %v, %v, 残った %v", r, err, exists(p))
	}
	end()
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, none); err != nil || r != blobRemoved || exists(p) {
		t.Fatalf("印を消した後 = %v, %v, 残った %v", r, err, exists(p))
	}
	// 古い印（落ちたプロセスが残したもの）は数えずに消す
	sum, p = put("stale mark")
	if _, err := beginBlobPlace(root, sum); err != nil {
		t.Fatal(err)
	}
	for _, n := range tmpLeft() {
		if err := os.Chtimes(filepath.Join(root, blobTmpDir, n), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, none); err != nil || r != blobRemoved || exists(p) {
		t.Fatalf("古い印 = %v, %v, 残った %v", r, err, exists(p))
	}
	// 外した後の数え直しで指す行が現れたら残す（数えてから外すまでの間に記録が確定した）
	sum, p = put("committed meanwhile")
	calls := 0
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, func() (int, error) { calls++; return calls - 1, nil }); err != nil || r != blobKept || !exists(p) || calls != 2 {
		t.Fatalf("数え直しで行が現れた = %v, %v, 残った %v, 数えた回数 %d", r, err, exists(p), calls)
	}
	// keepRecent: 触れられて OrphanGrace が経っていない本体は残す。keepRecent でなければ消す
	sum, p = put("recent")
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		t.Fatal(err)
	}
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, true, none); err != nil || r != blobKept || !exists(p) {
		t.Fatalf("新しい本体（keepRecent）= %v, %v, 残った %v", r, err, exists(p))
	}
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, none); err != nil || r != blobRemoved || exists(p) {
		t.Fatalf("新しい本体（消去）= %v, %v, 残った %v", r, err, exists(p))
	}
	if left := tmpLeft(); len(left) != 0 {
		t.Fatalf(".tmp に残った: %v", left)
	}
}

// placeBlob は、本体があると確かめた後に別のプロセスが本体を外していたら（更新時刻を進められない）、自分の一時ファイルを置く。
// 対照: 外されなければ一時ファイルを捨て、本体はそのまま。
func TestPlaceBlobPlacesWhenBodyMovedAsideAfterCheck(t *testing.T) {
	root := t.TempDir()
	const content = "placed by another process"
	tmpFile := func() (string, string) {
		t.Helper()
		p, sum, _, err := writeBlobTemp(root, strings.NewReader(content), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return p, sum
	}
	tmp, sum := tmpFile()
	if err := placeBlob(root, tmp, sum); err != nil {
		t.Fatal(err)
	}
	final := blobBodyPath(root, sum)
	aside := filepath.Join(root, blobTmpDir, blobRemovePrefix+sum+"-x")
	placeBlobAfterLstat = func(p string) {
		if err := os.Rename(p, aside); err != nil {
			t.Errorf("前提が崩れています: 本体を外せない: %v", err)
		}
	}
	t.Cleanup(func() { placeBlobAfterLstat = nil })
	tmp, _ = tmpFile()
	if err := placeBlob(root, tmp, sum); err != nil {
		t.Fatalf("外された後の placeBlob: %v", err)
	}
	if b, err := os.ReadFile(final); err != nil || string(b) != content {
		t.Fatalf("本体のパス = %q, %v（自分の一時ファイルが置かれること）", b, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("一時ファイルが残った: %v", err)
	}
	// 対照: 外されなければ一時ファイルを捨て、本体はそのまま
	placeBlobAfterLstat = nil
	before, err := os.Stat(final)
	if err != nil {
		t.Fatal(err)
	}
	tmp, _ = tmpFile()
	if err := placeBlob(root, tmp, sum); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(final)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("対照: 本体が置き換わった（同じファイル %v）: %v", err == nil && os.SameFile(before, after), err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("対照: 一時ファイルが残った: %v", err)
	}
}

// restoreBlob は、本体のパスに既に同じ内容があれば外した本体を捨てる（ファイルは 1 つだけ残る）。外した本体が無ければ何もしない。
// 対照: 本体のパスが空いていれば戻す。
func TestRestoreBlobWhenBodyAlreadyPlaced(t *testing.T) {
	root := t.TempDir()
	sum := bodySum("x")
	final := blobBodyPath(root, sum)
	aside := filepath.Join(root, blobTmpDir, blobRemovePrefix+sum+"-x")
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := func() int {
		t.Helper()
		n := 0
		_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return err
		})
		return n
	}
	write(final)
	write(aside)
	if err := restoreBlob(aside, final); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(final); err != nil || files() != 1 {
		t.Fatalf("本体のパスにあるとき: 本体 %v・ファイル %d（1 つだけ残る）", err, files())
	}
	if err := restoreBlob(aside, final); err != nil {
		t.Fatalf("外した本体が無いとき: %v", err)
	}
	// 対照: 本体のパスが空いていれば戻す
	if err := os.Rename(final, aside); err != nil {
		t.Fatal(err)
	}
	if err := restoreBlob(aside, final); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(final); err != nil || files() != 1 {
		t.Fatalf("対照: 空いた本体のパスへ戻らない: %v・ファイル %d", err, files())
	}
}

// recoverBlobLeftovers（DB を使わない）: 外した本体は、本体のパスが空いていて中身が合えば戻し、本体のパスにあれば捨て、
// 中身が合わない（外す前に名前だけ取った空のファイル）なら捨てる。古い印は消し、新しい印は残す。apply でなければ数えるだけ。
func TestRecoverBlobLeftovers(t *testing.T) {
	root := t.TempDir()
	tmpDir := filepath.Join(root, blobTmpDir)
	old := time.Now().Add(-2 * OrphanGrace)
	write := func(p, content string, at time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	freeSum, takenSum, emptySum := bodySum("free"), bodySum("taken"), bodySum("never moved")
	oldName := func(sum string) string {
		return filepath.Join(tmpDir, strings.Replace(asidePattern(sum, old), "*", "1", 1))
	}
	freeAside, takenAside, emptyAside := oldName(freeSum), oldName(takenSum), oldName(emptySum)
	write(freeAside, "free", old)
	write(takenAside, "taken", old)
	write(blobBodyPath(root, takenSum), "taken", old)
	write(emptyAside, "", time.Now())
	staleMark := filepath.Join(tmpDir, blobInflightPrefix+freeSum+"-1")
	freshMark := filepath.Join(tmpDir, blobInflightPrefix+takenSum+"-1")
	write(staleMark, "", old)
	write(freshMark, "", time.Now())
	present := func(p string) bool { _, err := os.Stat(p); return err == nil }

	if r, err := recoverBlobLeftovers(root, false, nil); err != nil || r != (blobLeftovers{Asides: 3, StaleMarks: 1}) {
		t.Fatalf("dry-run = %+v, %v", r, err)
	}
	if !present(freeAside) || !present(staleMark) || present(blobBodyPath(root, freeSum)) {
		t.Fatal("dry-run が置き場を変えた")
	}
	r, err := recoverBlobLeftovers(root, true, (&blobLocks{}).lock)
	if err != nil || r != (blobLeftovers{Asides: 3, Restored: 1, Dropped: 2, StaleMarks: 1}) {
		t.Fatalf("apply = %+v, %v（1 つ戻し、本体のパスにあるものと中身の合わないものを捨てる）", r, err)
	}
	if b, err := os.ReadFile(blobBodyPath(root, freeSum)); err != nil || string(b) != "free" {
		t.Fatalf("戻した本体 = %q, %v", b, err)
	}
	if present(blobBodyPath(root, emptySum)) {
		t.Fatal("中身の合わない外した本体を本体のパスへ戻した")
	}
	if present(freeAside) || present(takenAside) || present(emptyAside) || present(staleMark) || !present(freshMark) {
		t.Fatalf(".tmp: 外した本体 %v %v %v・古い印 %v・新しい印 %v", present(freeAside), present(takenAside), present(emptyAside), present(staleMark), present(freshMark))
	}
}

// removeBlobGuarded の外した後の確かめ: 外す前の確かめの後・外した後に印が現れたら（追記が外す直前に本体を置き終えた）、
// 本体を本体のパスへ戻す。対照: 印が現れなければ消す。
func TestRemoveBlobGuardedChecksAfterMovingAside(t *testing.T) {
	root := t.TempDir()
	put := func(content string) (string, string) {
		t.Helper()
		sum := bodySum(content)
		p := blobBodyPath(root, sum)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return sum, p
	}
	none := func() (int, error) { return 0, nil }
	sum, p := put("marked after the first check")
	var end func()
	removeBlobAfterAside = func(s string) {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("前提が崩れています: 外した直後に本体のパスに本体がある: %v", err)
		}
		var err error
		if end, err = beginBlobPlace(root, s); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { removeBlobAfterAside = nil })
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, none); err != nil || r != blobKept {
		t.Fatalf("外した後に印が現れた = %v, %v", r, err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("外した後に印が現れたのに本体が戻らない: %v", err)
	}
	end()
	// 対照: 印が現れなければ消す
	removeBlobAfterAside = nil
	if r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, none); err != nil || r != blobRemoved {
		t.Fatalf("対照: 印が無い = %v, %v", r, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("対照: 本体が残った: %v", err)
	}
}

// 外す先の名前を取った後・外す前の消す側（P）と、整合の検査（R）の取り合い: R は名前の時刻が新しい外した本体を拾わない
// （P の空のファイルを開いた R が、後から同じ名前に入った本体を捨てない）。その間に追記（A）が印を作って本体を置き、
// P がそれを外しても、P は印を見て戻す。対照: 名前の時刻が asideMinAge より古い外した本体は R が拾う。
func TestRecoverSkipsAsideOfRunningRemoval(t *testing.T) {
	root := t.TempDir()
	const content = "placed by an append during the removal"
	sum := bodySum(content)
	final := blobBodyPath(root, sum)
	var recovered blobLeftovers
	var named string
	var endA func()
	removeBlobNamedAside = func(aside string) {
		named = aside
		var err error
		if recovered, err = recoverBlobLeftovers(root, true, nil); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(aside); err != nil {
			t.Errorf("R が、まだ動いている消す側の名前を片付けた: %v", err)
		}
		// A: 印を作ってから本体を置く
		if endA, err = beginBlobPlace(root, sum); err != nil {
			t.Error(err)
		}
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(final, []byte(content), 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { removeBlobNamedAside = nil })
	r, err := removeBlobGuarded(root, repairTranscriptsCmd, sum, false, func() (int, error) { return 0, nil })
	removeBlobNamedAside = nil
	if named == "" {
		t.Fatal("前提が崩れています: 消す側が外す先の名前を取る前に終わった")
	}
	if err != nil || r != blobKept {
		t.Fatalf("P = %v, %v（A の印を見て戻す）", r, err)
	}
	if recovered != (blobLeftovers{}) {
		t.Fatalf("R が新しい外した本体を拾った: %+v", recovered)
	}
	if b, err := os.ReadFile(final); err != nil || string(b) != content {
		t.Fatalf("A の本体 = %q, %v（本体のパスに残ること）", b, err)
	}
	if _, err := os.Stat(named); !os.IsNotExist(err) {
		t.Fatalf("外す先の名前が残った: %v", err)
	}
	endA()

	// 対照: 名前の時刻が古い外した本体（止まった削除の残り）は拾って戻す
	old := time.Now().Add(-2 * asideMinAge)
	const other = "left by a crashed removal"
	osum := bodySum(other)
	aside := filepath.Join(root, blobTmpDir, strings.Replace(asidePattern(osum, old), "*", "1", 1))
	if err := os.WriteFile(aside, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := recoverBlobLeftovers(root, true, nil); err != nil || got != (blobLeftovers{Asides: 1, Restored: 1}) {
		t.Fatalf("対照: 古い外した本体 = %+v, %v", got, err)
	}
	if b, err := os.ReadFile(blobBodyPath(root, osum)); err != nil || string(b) != other {
		t.Fatalf("対照: 戻した本体 = %q, %v", b, err)
	}
}

// recoverAside は、読んだ後に同じ名前へ別のファイル（消す側が外した本物の本体）が入っていたら、捨ても戻しもしない。
// 対照: 替わっていなければ、中身の合わない空のファイルは捨てる。
func TestRecoverAsideSkipsReplacedFile(t *testing.T) {
	root := t.TempDir()
	const content = "real body"
	sum := bodySum(content)
	final := blobBodyPath(root, sum)
	tmpDir := filepath.Join(root, blobTmpDir)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(tmpDir, strings.Replace(asidePattern(sum, time.Now().Add(-2*asideMinAge)), "*", "1", 1))
	real := filepath.Join(tmpDir, "real")
	for p, c := range map[string]string{aside: "", real: content} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recoverAsideAfterRead = func(p string) {
		if err := os.Rename(real, p); err != nil {
			t.Errorf("前提が崩れています: 本体を同じ名前へ入れられない: %v", err)
		}
	}
	t.Cleanup(func() { recoverAsideAfterRead = nil })
	if got, err := recoverAside(aside, final, sum); err != nil || got != asideSkipped {
		t.Fatalf("読んだ後に替わった = %v, %v（扱わない）", got, err)
	}
	if b, err := os.ReadFile(aside); err != nil || string(b) != content {
		t.Fatalf("同じ名前に入った本体 = %q, %v（捨てないこと）", b, err)
	}
	// 対照: 替わっていなければ、中身の合わないファイルは捨てる
	recoverAsideAfterRead = nil
	if err := os.WriteFile(aside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := recoverAside(aside, final, sum); err != nil || got != asideDropped {
		t.Fatalf("対照: 空のファイル = %v, %v", got, err)
	}
	if _, err := os.Stat(aside); !os.IsNotExist(err) || func() bool { _, err := os.Stat(final); return err == nil }() {
		t.Fatalf("対照: 空のファイルが残った・本体のパスに置かれた: %v", err)
	}
}

// 外した本体の名前の読み方: remove-<sha256>-<UnixNano>-<乱数> だけを読み、時刻を返す。型の違う名前は読まない。
func TestParseAside(t *testing.T) {
	sum := bodySum("x")
	at := time.Unix(1700000000, 123)
	name := strings.Replace(asidePattern(sum, at), "*", "987", 1)
	if got, when, ok := parseAside(name); !ok || got != sum || !when.Equal(at) {
		t.Fatalf("parseAside(%q) = %q, %v, %v", name, got, when, ok)
	}
	for _, bad := range []string{
		blobRemovePrefix + sum + "-123",                    // 乱数の欄が無い（時刻を持たない以前の名前）
		blobRemovePrefix + sum + "--1",                     // 時刻が空
		blobRemovePrefix + sum + "-12a-1",                  // 時刻が数でない
		blobRemovePrefix + sum + "-0-1",                    // 時刻が 0
		blobRemovePrefix + sum[:63] + "-123-1",             // sha256 が短い
		blobInflightPrefix + sum + "-123-1",                // 印
		blobRemovePrefix + strings.ToUpper(sum) + "-123-1", // 大文字
	} {
		if _, _, ok := parseAside(bad); ok {
			t.Errorf("parseAside(%q) を読んだ", bad)
		}
	}
}

// restoreBlob: 外した本体が無く、本体のパスにも無ければ失敗を返す（本体を失っている）。対照: 本体のパスにあれば何もしない。
func TestRestoreBlobMissingAside(t *testing.T) {
	root := t.TempDir()
	sum := bodySum("gone")
	final := blobBodyPath(root, sum)
	aside := filepath.Join(root, blobTmpDir, "missing")
	if err := restoreBlob(aside, final); err == nil {
		t.Fatal("外した本体も本体も無いのに失敗を返さない")
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreBlob(aside, final); err != nil {
		t.Fatalf("対照: 本体のパスにあるとき = %v", err)
	}
}

// 外した本体を別のプロセスの整合の検査が戻していたら、removeBlobGuarded は消せなかったものとして失敗を返す
// （黙って残したことにすると、消去の出力が「本体 0」のまま中身が残る）。文面は呼んだ経路の検査を名指しする
// （会話記録なら repair-transcripts、添付なら repair-attachments。もう片方の名前は出さない）。
func TestRemoveBlobGuardedReportsBodyPutBack(t *testing.T) {
	for _, c := range []struct{ check, other string }{
		{repairTranscriptsCmd, repairAttachmentsCmd},
		{repairAttachmentsCmd, repairTranscriptsCmd},
	} {
		root := t.TempDir()
		const content = "put back by repair"
		sum := bodySum(content)
		final := blobBodyPath(root, sum)
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(final, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		removeBlobNamedAside = func(aside string) {
			removeBlobNamedAside = nil
			removeBlobAfterAside = func(string) {
				if err := os.Rename(aside, final); err != nil {
					t.Errorf("前提が崩れています: 戻せない: %v", err)
				}
			}
		}
		t.Cleanup(func() { removeBlobNamedAside, removeBlobAfterAside = nil, nil })
		r, err := removeBlobGuarded(root, c.check, sum, false, func() (int, error) { return 0, nil })
		removeBlobAfterAside = nil
		var rerr *bodyRemoveError
		if r != blobKept || !errors.As(err, &rerr) {
			t.Fatalf("%s: 戻された = %v, %v（消せなかったものとして失敗を返す）", c.check, r, err)
		}
		ja, en := i18n.Text(i18n.JA, err), i18n.Text(i18n.EN, err)
		if !strings.Contains(ja, "本体を本体のパスへ戻しました") || !strings.Contains(ja, "（"+c.check+"）") || strings.Contains(ja, c.other) {
			t.Errorf("%s: 日本語の文面 = %q（%s を名指しし、%s は出さない）", c.check, ja, c.check, c.other)
		}
		if !strings.Contains(en, "put the body back") || !strings.Contains(en, "("+c.check+")") || strings.Contains(en, c.other) {
			t.Errorf("%s: 英語の文面 = %q（%s を名指しし、%s は出さない）", c.check, en, c.check, c.other)
		}
		if _, err := os.Stat(final); err != nil {
			t.Fatalf("%s: 本体が本体のパスに無い: %v", c.check, err)
		}
	}
}
