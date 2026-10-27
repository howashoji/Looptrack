package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// looptrack repair-transcripts と repair-attachments は、置き場が別なら互いの本体を消さない（両方向）。
// どちらも --apply で自分の側のどこからも指されない本体だけを消す（対照）。本番と同じ権限のアプリ用ユーザーで動かす。
func TestRepairTranscriptsCmd(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	attachDir, trDir := filepath.Join(t.TempDir(), "attachments"), filepath.Join(t.TempDir(), "transcripts")
	pid, err := store.CreateProject(ctx, admin, store.Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "req"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.ProjectByID(ctx, admin, pid)
	uid, err := store.CreateUser(ctx, admin, "ed", "ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, admin, pid, uid, "editor"); err != nil {
		t.Fatal(err)
	}
	svc := service.New(app, nil)
	svc.AttachDir = attachDir
	a := service.Actor{UserID: uid, Via: "cli", Lang: i18n.JA}
	it, err := svc.Create(ctx, a, p, service.CreateInput{Title: "x"}, i18n.JA)
	if err != nil {
		t.Fatal(err)
	}
	at, err := svc.Attach(ctx, a, service.AttachInput{Issue: it.Item.ID, Filename: "keep.txt", Body: strings.NewReader("attachment")})
	if err != nil {
		t.Fatal(err)
	}
	attBody := service.AttachmentBodyPath(attachDir, at.SHA256)

	// 会話記録の本体（チャンクが指すもの・欠けたもの）と、それぞれの置き場のどこからも指されない本体
	put := func(root, sum, content string) string {
		t.Helper()
		p := service.TranscriptBodyPath(root, sum)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tid, err := store.InsertTranscript(ctx, app, store.Transcript{UserID: uid, ProjectID: pid, Agent: store.TranscriptAgentClaude,
		VendorSessionID: "s-1", OriginOS: "linux", Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	chunk := func(seq int64, sum string) int64 {
		t.Helper()
		id, err := store.InsertTranscriptChunk(ctx, app, store.TranscriptChunk{TranscriptID: tid, Seq: seq, FileKey: "s-1.jsonl",
			FileKind: store.TranscriptFileJSONL, SrcOffset: (seq - 1) * 10, SrcLength: 10, LineCount: 1, SHA256: sum, Size: 10,
			Encoding: store.TranscriptEncodingIdentity, Nonce: "0123456789ab", Via: "cli"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	keepSum, goneSum := strings.Repeat("4d", 32), strings.Repeat("5e", 32)
	trBody := put(trDir, keepSum, "transcript")
	chunk(1, keepSum)
	goneID := chunk(2, goneSum) // 本体の欠け
	trOrphan := put(trDir, strings.Repeat("6f", 32), "orphan")
	attOrphan := put(attachDir, strings.Repeat("0f", 32), "orphan")
	old := time.Now().Add(-2 * service.OrphanGrace)
	for _, f := range []string{trBody, trOrphan, attOrphan, attBody} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(when string, paths ...string) {
		t.Helper()
		for _, p := range paths {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: %s が無い: %v", when, p, err)
			}
		}
	}

	var out bytes.Buffer
	if err := repairTranscripts(ctx, app, attachDir, trDir, i18n.JA, &out, false); err != nil {
		t.Fatal(err)
	}
	want := "本体の欠け: 会話記録 " + strconv.FormatInt(tid, 10) + " のチャンク 2（id " + strconv.FormatInt(goneID, 10) + "・sha256 " + goneSum + "）\n" +
		"どこからも指されない本体: " + trOrphan + "\n" +
		"本体の欠け 1 件・どこからも指されない本体 1 件（dry-run。どこからも指されない本体を消すには --apply を付けて再実行）\n"
	if out.String() != want {
		t.Errorf("dry-run:\n%s\nwant\n%s", out.String(), want)
	}
	exists("dry-run", trOrphan, trBody, attOrphan, attBody)

	// 添付の --apply は会話記録の本体（指されているもの・指されていないもの）を消さない
	out.Reset()
	if err := repairAttachments(ctx, app, attachDir, i18n.JA, &out, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), trDir) {
		t.Errorf("添付の検査が会話記録の置き場を見た:\n%s", out.String())
	}
	if _, err := os.Stat(attOrphan); !os.IsNotExist(err) {
		t.Errorf("対照: 添付の --apply が添付のどこからも指されない本体を消さない: %v", err)
	}
	exists("repair-attachments --apply", trBody, trOrphan, attBody)

	// 会話記録の --apply は添付の本体を消さない
	attOrphan2 := put(attachDir, strings.Repeat("7a", 32), "orphan")
	if err := os.Chtimes(attOrphan2, old, old); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := repairTranscripts(ctx, app, attachDir, trDir, i18n.JA, &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "消した: "+trOrphan+"\n本体の欠け 1 件・どこからも指されない本体 1 件のうち 1 件を消しました（本体の欠けは直せないので報告だけ）\n") {
		t.Errorf("apply:\n%s", out.String())
	}
	if _, err := os.Stat(trOrphan); !os.IsNotExist(err) {
		t.Errorf("対照: 会話記録のどこからも指されない本体が残った: %v", err)
	}
	exists("repair-transcripts --apply", trBody, attBody, attOrphan2)
	var n int
	admin.QueryRow("SELECT COUNT(*) FROM transcript_chunks").Scan(&n)
	if n != 2 {
		t.Errorf("チャンクが %d 行（2 のはず。整合の検査は DB を変えない）", n)
	}
}

// 会話記録の置き場が添付の置き場と入れ子なら、repair-transcripts は何も見ずに止める（どちらの向きも・同じ場所も）。
func TestRepairTranscriptsNestedRefuses(t *testing.T) {
	_, app := testutil.AppDB(t)
	base := t.TempDir()
	orphan := service.TranscriptBodyPath(base, strings.Repeat("0f", 32))
	if err := os.MkdirAll(filepath.Dir(orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * service.OrphanGrace)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{base, filepath.Join(base, "transcripts")}, {filepath.Join(base, "attachments"), base}, {base, base}} {
		var out bytes.Buffer
		err := repairTranscripts(context.Background(), app, c[0], c[1], i18n.JA, &out, true)
		if txt := i18n.Text(i18n.JA, err); err == nil || !strings.Contains(txt, "一方がもう一方の中にある") || !strings.Contains(txt, c[1]) {
			t.Errorf("添付 %q・会話記録 %q: err = %v", c[0], c[1], err)
		}
		if out.Len() != 0 {
			t.Errorf("止めたのに出力した: %s", out.String())
		}
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("止めたのに本体を消した: %v", err)
	}
	// 対照: 別の場所なら動く
	var out bytes.Buffer
	if err := repairTranscripts(context.Background(), app, filepath.Join(t.TempDir(), "attachments"), base, i18n.JA, &out, false); err != nil {
		t.Errorf("別の場所: %v", err)
	}
}

// serve の設定: 会話記録の置き場は LOOPTRACK_TRANSCRIPT_DIR か、無ければ添付と同じ順で transcripts に決まる。
func TestServeConfigTranscriptDir(t *testing.T) {
	key, _ := auth.NewSecretKey()
	t.Setenv("LOOPTRACK_SECRET_KEY", key)
	t.Setenv("LOOPTRACK_DSN", "u:p@tcp(127.0.0.1:1)/im")
	t.Setenv("LOOPTRACK_ATTACH_DIR", "")
	t.Setenv("LOOPTRACK_TRANSCRIPT_DIR", "")
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("LOOPTRACK_LOCAL_MODE", "")
	t.Setenv("LOOPTRACK_LISTEN", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if cfg, _, err := serveConfig(logger); err != nil || cfg.TranscriptDir != "" {
		t.Errorf("置き場なし: TranscriptDir=%q err=%v", cfg.TranscriptDir, err)
	}
	t.Setenv("STATE_DIRECTORY", "/var/lib/looptrack")
	if cfg, _, err := serveConfig(logger); err != nil || cfg.TranscriptDir != filepath.Join("/var/lib/looptrack", "transcripts") || cfg.AttachDir != filepath.Join("/var/lib/looptrack", "attachments") {
		t.Errorf("STATE_DIRECTORY: TranscriptDir=%q AttachDir=%q err=%v", cfg.TranscriptDir, cfg.AttachDir, err)
	}
	t.Setenv("LOOPTRACK_TRANSCRIPT_DIR", "/srv/transcripts")
	if cfg, _, err := serveConfig(logger); err != nil || cfg.TranscriptDir != "/srv/transcripts" {
		t.Errorf("LOOPTRACK_TRANSCRIPT_DIR: TranscriptDir=%q err=%v", cfg.TranscriptDir, err)
	}
}

// looptrack repair-transcripts --apply は、途中で止まった削除が置き場の .tmp に外したまま残した本体を拾う:
// 消去していないチャンクが指す本体は本体のパスへ戻り（本体の欠けが消える）、どこからも指されない本体は戻した後に
// ふつうの規則で消える（.tmp にも残らない）。古い印は消し、新しい印は残す。dry-run は数えるだけで何も変えない。
func TestRepairTranscriptsRecoversBodyLeftAside(t *testing.T) {
	admin, app := testutil.AppDB(t)
	ctx := context.Background()
	attachDir, trDir := filepath.Join(t.TempDir(), "attachments"), filepath.Join(t.TempDir(), "transcripts")
	pid, err := store.CreateProject(ctx, admin, store.Project{Slug: "req", Prefix: "REQ", Width: 4, Name: "req"})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := store.CreateUser(ctx, admin, "ed", "ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := store.InsertTranscript(ctx, app, store.Transcript{UserID: uid, ProjectID: pid, Agent: store.TranscriptAgentClaude,
		VendorSessionID: "s-1", OriginOS: "linux", Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(trDir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * service.OrphanGrace)
	// aside は本体を置いてから、止まった削除と同じ名前で .tmp へ外す（本体のパスは空く）
	aside := func(content string) (sum, body, moved string) {
		t.Helper()
		h := sha256.Sum256([]byte(content))
		sum = hex.EncodeToString(h[:])
		body = service.TranscriptBodyPath(trDir, sum)
		if err := os.MkdirAll(filepath.Dir(body), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(body, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(body, old, old); err != nil {
			t.Fatal(err)
		}
		// 名前に入る時刻（外す直前に名前を取った時刻）を、拾うまでの古さ（OrphanGrace）より前にする
		moved = filepath.Join(tmpDir, "remove-"+sum+"-"+strconv.FormatInt(old.UnixNano(), 10)+"-1")
		if err := os.Rename(body, moved); err != nil {
			t.Fatal(err)
		}
		return sum, body, moved
	}
	liveSum, liveBody, liveAside := aside("referenced body")
	if _, err := store.InsertTranscriptChunk(ctx, app, store.TranscriptChunk{TranscriptID: tid, Seq: 1, FileKey: "s-1.jsonl",
		FileKind: store.TranscriptFileJSONL, SrcLength: 15, LineCount: 1, SHA256: liveSum, Size: 15,
		Encoding: store.TranscriptEncodingIdentity, Nonce: "0123456789ab", Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	_, orphanBody, orphanAside := aside("purged body")
	mark := func(name string, at time.Time) string {
		t.Helper()
		p := filepath.Join(tmpDir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	staleMark := mark("inflight-"+strings.Repeat("ab", 32)+"-1", old)
	freshMark := mark("inflight-"+strings.Repeat("cd", 32)+"-1", time.Now())
	present := func(p string) bool { _, err := os.Stat(p); return err == nil }

	var out bytes.Buffer
	if err := repairTranscripts(ctx, app, attachDir, trDir, i18n.JA, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "途中で止まった削除が外したまま残した本体 2 件・落ちた追記が残した古い印 1 件（dry-run。") ||
		!strings.Contains(out.String(), "本体の欠け: 会話記録 "+strconv.FormatInt(tid, 10)+" のチャンク 1") {
		t.Errorf("dry-run:\n%s（外した本体 2・古い印 1・外した本体を指すチャンクは欠けとして出る）", out.String())
	}
	if present(liveBody) || present(orphanBody) || !present(liveAside) || !present(orphanAside) || !present(staleMark) {
		t.Fatal("dry-run が置き場を変えた")
	}

	out.Reset()
	if err := repairTranscripts(ctx, app, attachDir, trDir, i18n.JA, &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "途中で止まった削除が外したまま残した本体 2 件のうち 2 件を本体のパスへ戻し、0 件を捨てました。落ちた追記が残した古い印 1 件を消しました\n") ||
		!strings.Contains(out.String(), "消した: "+orphanBody+"\n") || strings.Contains(out.String(), "本体の欠け: ") {
		t.Errorf("apply:\n%s（2 つを戻し、指されないほうはふつうの規則で消す。欠けは残らない）", out.String())
	}
	if !present(liveBody) {
		t.Errorf("チャンクが指す本体が本体のパスへ戻らなかった")
	}
	if present(orphanBody) || present(liveAside) || present(orphanAside) {
		t.Errorf("対照: どこからも指されない本体か外した本体が残った（本体 %v・外した本体 %v %v）", present(orphanBody), present(liveAside), present(orphanAside))
	}
	if present(staleMark) || !present(freshMark) {
		t.Errorf("印: 古い印が残った %v・新しい印が消えた %v", present(staleMark), !present(freshMark))
	}
}
