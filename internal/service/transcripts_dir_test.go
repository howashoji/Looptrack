package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/store"
)

// TranscriptDirFromEnv は AttachDirFromEnv と同じ順で、別の名前（transcripts）に決める。
func TestTranscriptDirFromEnv(t *testing.T) {
	sqliteDir := filepath.Join(t.TempDir(), "data")
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"明示", map[string]string{"LOOPTRACK_TRANSCRIPT_DIR": "/srv/tr", "STATE_DIRECTORY": "/var/lib/looptrack", "LOOPTRACK_DSN": "sqlite:" + sqliteDir + "/im.db"}, "/srv/tr"},
		{"StateDirectory", map[string]string{"STATE_DIRECTORY": "/var/lib/looptrack", "LOOPTRACK_DSN": "sqlite:" + sqliteDir + "/im.db"}, filepath.Join("/var/lib/looptrack", "transcripts")},
		{"SQLite の隣", map[string]string{"LOOPTRACK_DSN": "sqlite:" + sqliteDir + "/im.db"}, filepath.Join(sqliteDir, "transcripts")},
		{"空白だけの明示は無いのと同じ", map[string]string{"LOOPTRACK_TRANSCRIPT_DIR": "  ", "LOOPTRACK_DSN": "sqlite:" + sqliteDir + "/im.db"}, filepath.Join(sqliteDir, "transcripts")},
		{"MySQL で何も無い", map[string]string{"LOOPTRACK_DSN": "u:p@tcp(127.0.0.1:3306)/im"}, ""},
		{"メモリの SQLite", map[string]string{"LOOPTRACK_DSN": "sqlite::memory:"}, ""},
		// 添付の変数は見ない（置き場を分ける）
		{"添付の変数だけ", map[string]string{"LOOPTRACK_ATTACH_DIR": "/srv/att", "LOOPTRACK_DSN": "u:p@tcp(127.0.0.1:3306)/im"}, ""},
	} {
		if got := TranscriptDirFromEnv(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		// 既定の決め方では添付と会話記録は入れ子にならない
		if a, tr := AttachDirFromEnv(func(k string) string { return c.env[k] }), TranscriptDirFromEnv(func(k string) string { return c.env[k] }); BlobRootsNested(a, tr) {
			t.Errorf("%s: 既定の置き場が入れ子（添付 %q・会話記録 %q）", c.name, a, tr)
		}
	}
}

// 入れ子（同じ場所・中・外）を見つけ、名前の先頭が同じだけの兄弟・空は入れ子と見ない。
func TestBlobRootsNested(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(base, link); err != nil {
		t.Skipf("シンボリックリンクを作れない: %v", err)
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{filepath.Join(base, "att"), filepath.Join(base, "att"), true},
		{filepath.Join(base, "att"), filepath.Join(base, "att", "tr"), true},
		{filepath.Join(base, "x", "tr", "att"), filepath.Join(base, "x", "tr"), true},
		{filepath.Join(base, "att"), filepath.Join(base, "att", "..", "att", "tr"), true},
		{filepath.Join(base, "att"), filepath.Join(base, "ATT", "tr"), true}, // 大小を区別しない（入れ子の側に倒す）
		{filepath.Join(base, "att"), filepath.Join(link, "att", "tr"), true}, // リンクを解くと中（まだ無い末尾も）
		{filepath.Join(base, "attachments"), filepath.Join(base, "transcripts"), false},
		{filepath.Join(base, "att"), filepath.Join(base, "att2"), false}, // 名前の先頭が同じだけの兄弟
		{"", filepath.Join(base, "tr"), false},
		{filepath.Join(base, "att"), "", false},
	} {
		if got := BlobRootsNested(c.a, c.b); got != c.want {
			t.Errorf("BlobRootsNested(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// putTranscriptBody は会話記録の本体を置き場に置いてチャンクを 1 つ記録し、本体のパスを返す（会話記録の書き込みの経路の代わり）。
func (e *attachEnv) putTranscriptBody(vendor, content string) (path, sum string) {
	e.t.Helper()
	root, err := e.s.transcriptRoot()
	if err != nil {
		e.t.Fatal(err)
	}
	tmp, sum, size, err := writeBlobTemp(root, strings.NewReader(content), 1<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := placeBlob(root, tmp, sum); err != nil {
		e.t.Fatal(err)
	}
	tid, err := store.InsertTranscript(e.ctx, e.app, store.Transcript{UserID: e.editor.UserID, ProjectID: e.demo.ID, Agent: store.TranscriptAgentClaude,
		VendorSessionID: vendor, OriginOS: "linux", Via: "cli"})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := store.InsertTranscriptChunk(e.ctx, e.app, store.TranscriptChunk{TranscriptID: tid, Seq: 1, FileKey: vendor + ".jsonl",
		FileKind: store.TranscriptFileJSONL, SrcLength: size, LineCount: 1, SHA256: sum, Size: size,
		Encoding: store.TranscriptEncodingIdentity, Nonce: "0123456789ab", Via: "cli"}); err != nil {
		e.t.Fatal(err)
	}
	return TranscriptBodyPath(root, sum), sum
}

// putOldOrphan は root に、どこからも指されない古い本体を置く。
func putOldOrphan(t *testing.T, root, sum string) string {
	t.Helper()
	p := blobBodyPath(root, sum)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * OrphanGrace)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

func ageBody(t *testing.T, paths ...string) {
	t.Helper()
	old := time.Now().Add(-2 * OrphanGrace)
	for _, p := range paths {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// 受け入れ条件: 置き場が入れ子の設定では会話記録が「置き場が設定されていない」になり、添付は動く。
// 入れ子の向き（会話記録が添付の中・添付が会話記録の中・同じ場所）を全部試し、対照として別の場所なら使える。
func TestTranscriptRootNestedUnavailable(t *testing.T) {
	e := newAttachEnv(t)
	attach := e.s.AttachDir
	outer := t.TempDir()
	for _, c := range []struct {
		name          string
		attach, trDir string
	}{
		{"会話記録が添付の中", attach, filepath.Join(attach, "transcripts")},
		{"添付が会話記録の中", filepath.Join(outer, "attachments"), outer},
		{"同じ場所", attach, attach},
		{"置き場なし", attach, ""},
	} {
		e.s.AttachDir, e.s.TranscriptDir = c.attach, c.trDir
		if _, err := e.s.CheckTranscripts(e.ctx); errCode(err) != "transcripts_unavailable" {
			t.Errorf("%s: CheckTranscripts err = %v, want transcripts_unavailable", c.name, err)
		}
		if _, err := e.s.RemoveTranscriptOrphans(e.ctx, &TranscriptCheck{}); errCode(err) != "transcripts_unavailable" {
			t.Errorf("%s: RemoveTranscriptOrphans err = %v, want transcripts_unavailable", c.name, err)
		}
		if e.s.TranscriptsAvailable() {
			t.Errorf("%s: TranscriptsAvailable = true", c.name)
		}
		if at := e.mustAttach(e.editor, e.issue, c.name+".txt", "body "+c.name); at.ID == 0 {
			t.Errorf("%s: 添付が動かない", c.name)
		}
		if _, err := e.s.CheckAttachments(e.ctx); err != nil {
			t.Errorf("%s: 添付の整合の検査が動かない: %v", c.name, err)
		}
	}
	// 対照: 別の場所なら会話記録の置き場は使える（上の拒否が「いつも拒否」でないこと）
	e.s.AttachDir, e.s.TranscriptDir = attach, filepath.Join(outer, "elsewhere", "transcripts")
	if !e.s.TranscriptsAvailable() {
		t.Fatal("前提が崩れています（別の場所の置き場が使えない）")
	}
	if c, err := e.s.CheckTranscripts(e.ctx); err != nil || len(c.Missing) != 0 || len(c.Orphans) != 0 {
		t.Errorf("別の場所: %+v %v", c, err)
	}
}

// 受け入れ条件: 添付と会話記録の置き場が別のとき、添付の整合の検査は会話記録の本体を消さず、会話記録の検査は添付の本体を消さない。
// 対照として、それぞれの検査は自分の側のどこからも指されない本体を消す（検査が何も消さないから通るのではないこと）。
func TestRepairDoesNotCrossRoots(t *testing.T) {
	e := newAttachEnv(t)
	e.s.TranscriptDir = filepath.Join(t.TempDir(), "transcripts")
	at := e.mustAttach(e.editor, e.issue, "keep.txt", "attachment body")
	attBody := AttachmentBodyPath(e.s.AttachDir, at.SHA256)
	trBody, trSum := e.putTranscriptBody("s-1", "transcript body")
	attOrphan := putOldOrphan(t, e.s.AttachDir, strings.Repeat("1a", 32))
	trOrphan := putOldOrphan(t, e.s.TranscriptDir, strings.Repeat("2b", 32))
	ageBody(t, attBody, trBody)

	// 会話記録の本体（指されている・指されていない）は添付の検査からは見えない
	ac, err := e.s.CheckAttachments(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.Orphans) != 1 || ac.Orphans[0].Path != attOrphan || len(ac.Missing) != 0 {
		t.Errorf("添付の検査: %+v", ac)
	}
	if removed, err := e.s.RemoveOrphanBodies(e.ctx, ac); err != nil || len(removed) != 1 || removed[0] != attOrphan {
		t.Errorf("添付の --apply が消した: %v %v", removed, err)
	}
	for _, p := range []string{trBody, trOrphan, attBody} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("添付の --apply の後に %s が無い: %v", p, err)
		}
	}

	tc, err := e.s.CheckTranscripts(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Orphans) != 1 || tc.Orphans[0].Path != trOrphan || len(tc.Missing) != 0 {
		t.Errorf("会話記録の検査: %+v", tc)
	}
	if removed, err := e.s.RemoveTranscriptOrphans(e.ctx, tc); err != nil || len(removed) != 1 || removed[0] != trOrphan {
		t.Errorf("会話記録の --apply が消した: %v %v", removed, err)
	}
	for _, p := range []string{trBody, attBody} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("会話記録の --apply の後に %s が無い: %v", p, err)
		}
	}
	// 会話記録の本体の欠けを報告する（消去していないチャンクが指す本体が無い）
	if err := os.Remove(trBody); err != nil {
		t.Fatal(err)
	}
	if tc, err := e.s.CheckTranscripts(e.ctx); err != nil || len(tc.Missing) != 1 || tc.Missing[0].SHA256 != trSum || tc.Missing[0].Seq != 1 {
		t.Errorf("本体の欠け: %+v %v", tc, err)
	}
}

// 置き場を取り違えて同じ場所に置いた（会話記録は使えない設定の）ときでも、添付の --apply は会話記録のチャンクが指す本体を消さない。
// 対照: 同じ場所のどこからも指されない本体は消す。
func TestRepairAttachmentsKeepsTranscriptBodiesInSharedRoot(t *testing.T) {
	e := newAttachEnv(t)
	e.s.TranscriptDir = filepath.Join(t.TempDir(), "transcripts")
	trBody, _ := e.putTranscriptBody("s-1", "transcript body")
	// 本体を添付の置き場に写し、同じ場所を指す設定にする（以前の設定で書かれた本体が残っている形）
	shared := AttachmentBodyPath(e.s.AttachDir, filepath.Base(trBody))
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(trBody)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, b, 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := putOldOrphan(t, e.s.AttachDir, strings.Repeat("3c", 32))
	ageBody(t, shared)
	e.s.TranscriptDir = e.s.AttachDir

	ac, err := e.s.CheckAttachments(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.Orphans) != 1 || ac.Orphans[0].Path != orphan {
		t.Errorf("添付の検査が会話記録の本体を「どこからも指されない」と数えた: %+v", ac.Orphans)
	}
	// 検査の結果に混ざっても、消す直前の確かめ直しで残す
	ac.Orphans = append(ac.Orphans, OrphanBody{Path: shared, SHA256: filepath.Base(shared), ModTime: time.Now().Add(-2 * OrphanGrace)})
	removed, err := e.s.RemoveOrphanBodies(e.ctx, ac)
	if err != nil || len(removed) != 1 || removed[0] != orphan {
		t.Errorf("消した: %v %v", removed, err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("会話記録のチャンクが指す本体を消した: %v", err)
	}
}
