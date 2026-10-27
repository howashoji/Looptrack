package service

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// 添付の整合の検査（repair-attachments --apply）と、別のプロセスで同じ本体を置いてから記録を確定するまでの添付との取り合い
// （blobstore.go の removeBlobGuarded）。別のプロセスは、DB と置き場だけを共有する別の Service で作る。

// holdAttach は s の添付を、本体を置いた後・記録を確定する前で止めて返す。返した関数で続きを走らせ、添付の結果を受け取る。
// 待つのはどちらも 30 秒まで（越えたらテストを止める）。
func (e *attachEnv) holdAttach(s *Service, a Actor, it *Issue, name, body string) func() (*store.Attachment, error) {
	e.t.Helper()
	placed, release := make(chan struct{}), make(chan struct{})
	type result struct {
		at  *store.Attachment
		err error
	}
	done := make(chan result, 1)
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	e.t.Cleanup(let) // テストが途中で止まっても、止めた添付を残さない
	s.AttachPlaced = func(string) {
		close(placed)
		<-release
	}
	go func() {
		at, err := s.Attach(e.ctx, a, AttachInput{Issue: it.Item.ID, Filename: name, MediaType: "text/plain", Body: strings.NewReader(body)})
		done <- result{at, err}
	}()
	select {
	case <-placed:
	case r := <-done:
		e.t.Fatalf("前提が崩れています: 添付が本体を置く前に終わった: %v", r.err)
	case <-time.After(30 * time.Second):
		e.t.Fatal("前提が崩れています: 添付が 30 秒以内に本体を置かなかった")
	}
	return func() (*store.Attachment, error) {
		e.t.Helper()
		let()
		select {
		case r := <-done:
			s.AttachPlaced = nil
			return r.at, r.err
		case <-time.After(30 * time.Second):
			e.t.Fatal("止めた添付が、続けてから 30 秒以内に終わらなかった")
			return nil, nil
		}
	}
}

// 整合の検査で孤立と見えた本体に、別のプロセスの添付が「既にある」と見て置くのを飛ばし、記録を確定する前で止まっている。
// その間に RemoveOrphanBodies を回しても本体は消えず、確定した添付の本体を読める。本体の更新時刻は古く戻しておく
// （検査が更新時刻を見た後に添付が触れた形。印だけが本体を守る）。
// 対照: 添付が確定した後は、どこからも指されない本体だけが消える。
func TestRemoveOrphanBodiesKeepsBodyOfAttachInAnotherProcess(t *testing.T) {
	e := newAttachEnv(t)
	shared, sharedSum := e.writeOldBody("shared")
	orphan, _ := e.writeOldBody("orphan")
	c, err := e.s.CheckAttachments(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, o := range c.Orphans {
		if o.Recent {
			t.Fatalf("前提が崩れています: 古く置いた本体が Recent と見えた: %+v", o)
		}
		seen[o.Path] = true
	}
	if !seen[shared] || !seen[orphan] {
		t.Fatalf("前提が崩れています: 検査が孤立と見なかった: %+v", c.Orphans)
	}

	other := New(e.app, nil)
	other.AttachDir = e.s.AttachDir
	finish := e.holdAttach(other, e.editor, e.issue, "shared.txt", "shared")
	old := time.Now().Add(-2 * OrphanGrace)
	if err := os.Chtimes(shared, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := e.s.RemoveOrphanBodies(e.ctx, &AttachmentCheck{Orphans: []OrphanBody{{Path: shared, SHA256: sharedSum}}})
	if err != nil || len(removed) != 0 {
		t.Fatalf("別のプロセスの添付が確定する前の整合の検査 = %v, %v（何も消さない）", removed, err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("別のプロセスの添付が確定する前に本体が消えた: %v", err)
	}
	at, err := finish()
	if err != nil {
		t.Fatalf("止めた添付: %v", err)
	}
	if _, f, err := e.s.OpenAttachment(e.ctx, e.editor, at.ID); err != nil {
		t.Fatalf("確定した添付の本体: %v", err)
	} else {
		f.Close()
	}

	// 対照: 添付が確定した後は、どこからも指されない本体だけが消える（消す経路そのものが生きている）
	c2, err := e.s.CheckAttachments(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed, err = e.s.RemoveOrphanBodies(e.ctx, c2)
	if err != nil || len(removed) != 1 || removed[0] != orphan {
		t.Fatalf("確定した後の整合の検査 = %v, %v, want [%s]", removed, err, orphan)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("確定した添付の本体が消えた: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(e.s.AttachDir, blobTmpDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, d := range entries {
		if strings.HasPrefix(d.Name(), blobInflightPrefix) || strings.HasPrefix(d.Name(), blobRemovePrefix) {
			t.Errorf(".tmp に残った: %s（印も外した本体も残さない）", d.Name())
		}
	}
}

// 添付の消去（PurgeAttachment）は、別のプロセスの添付が同じ本体を「既にある」と見て置くのを飛ばし、記録を確定する前で
// 止まっている間に回しても本体を消さない（その添付が確定した後に本体を読める）。消去した添付の行は消去済みになる。
// 対照: 置いている添付が無ければ、消去は本体を消す（.tmp に印も外した本体も残さない）。
func TestPurgeAttachmentKeepsBodyOfAttachInAnotherProcess(t *testing.T) {
	e := newAttachEnv(t)
	first := e.mustAttach(e.editor, e.issue, "first.txt", "secret-ish")
	body := AttachmentBodyPath(e.s.AttachDir, first.SHA256)

	other := New(e.app, nil)
	other.AttachDir = e.s.AttachDir
	finish := e.holdAttach(other, e.editor, e.issue2, "again.txt", "secret-ish")
	res, err := e.s.PurgeAttachment(e.ctx, e.root, first.ID, "while another process attaches")
	if err != nil || !res.Changed || len(res.AttachmentIDs) != 1 || res.AttachmentIDs[0] != first.ID {
		t.Fatalf("別のプロセスの添付が確定する前の消去 = %+v, %v（最初の添付だけを消去済みにする）", res, err)
	}
	if n := e.count("SELECT COUNT(*) FROM attachment_purges"); n != 1 {
		t.Fatalf("消去の記録 = %d 行, want 1", n)
	}
	if _, err := os.Stat(body); err != nil {
		t.Fatalf("別のプロセスの添付が確定する前に本体が消えた: %v", err)
	}
	again, err := finish()
	if err != nil {
		t.Fatalf("止めた添付: %v", err)
	}
	if _, f, err := e.s.OpenAttachment(e.ctx, e.editor, again.ID); err != nil {
		t.Fatalf("確定した添付の本体: %v", err)
	} else {
		f.Close()
	}

	// 対照: 置いている添付が無ければ、消去は本体を消す
	if _, err := e.s.PurgeAttachment(e.ctx, e.root, again.ID, "after commit"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(body); !os.IsNotExist(err) {
		t.Fatalf("置いている添付が無いのに本体が残った: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(e.s.AttachDir, blobTmpDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, d := range entries {
		if strings.HasPrefix(d.Name(), blobInflightPrefix) || strings.HasPrefix(d.Name(), blobRemovePrefix) {
			t.Errorf(".tmp に残った: %s（印も外した本体も残さない）", d.Name())
		}
	}
}
