package cli

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/i18n"
)

// runSession は looptrack session を、与えた環境だけで動かす。HOME と作業ツリーは呼ぶ側が渡す。
func runSession(t *testing.T, vars map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := SessionMain(args, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, env.FromMap(vars))
	return code, out.String(), errb.String()
}

// trEnv は偽サーバを指す環境と、記録を置く HOME・作業ツリーを作る。
func trEnv(t *testing.T, serverURL string) (vars map[string]string, home, root string) {
	t.Helper()
	home, root = t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"APPDATA": filepath.Join(home, "AppData"), "CLAUDE_PROJECT_DIR": root, "LOOPTRACK_LANG": "ja",
		"LOOPTRACK_API_URL": serverURL + "/im", "LOOPTRACK_PROJECT": "demo", "LOOPTRACK_TOKEN": "imp_lt"}, home, root
}

func writeJSONResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// trFake は会話記録の API の偽サーバ（push 用）。伸ばした大きさが limit を超えるチャンクは 413（transcript_too_large）で拒む。
// 受け付けたチャンクは位置を検べてファイルごとの長さを進める（位置が合わなければ 409 になるので、CLI の取り直しも通る）。
type trFake struct {
	mu       sync.Mutex
	limit    int
	lengths  map[string]int64
	seq      int64
	posts    int // チャンクの POST の回数（拒否を含む）
	accepted int
}

func (f *trFake) state() map[string]any {
	files := []map[string]any{}
	for k, n := range f.lengths {
		files = append(files, map[string]any{"file_key": k, "length": n})
	}
	return map[string]any{"transcript": map[string]any{"id": 1, "seq": f.seq, "files": files, "transcripts_enabled": true}}
}

func (f *trFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transcripts"):
		writeJSONResp(w, 200, f.state())
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/transcripts/1"):
		writeJSONResp(w, 200, f.state())
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transcripts/1/chunks"):
		f.posts++
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			writeJSONResp(w, 400, map[string]any{"error": map[string]any{"code": "bad", "message": "gzip"}})
			return
		}
		body, _ := io.ReadAll(zr)
		if len(body) > f.limit {
			writeJSONResp(w, 413, map[string]any{"error": map[string]any{"code": "transcript_too_large", "message": "chunk too large"}})
			return
		}
		key, _ := url.PathUnescape(r.Header.Get("X-Looptrack-Transcript-File"))
		off, _ := strconv.ParseInt(r.Header.Get("X-Looptrack-Transcript-Offset"), 10, 64)
		n, _ := strconv.ParseInt(r.Header.Get("X-Looptrack-Transcript-Length"), 10, 64)
		if off != f.lengths[key] {
			writeJSONResp(w, 409, map[string]any{"error": map[string]any{"code": "transcript_offset", "message": "offset"}})
			return
		}
		f.lengths[key] = off + n
		f.seq++
		f.accepted++
		writeJSONResp(w, 200, f.state())
	default:
		writeJSONResp(w, 404, map[string]any{"error": map[string]any{"code": "unknown_api", "message": r.URL.Path}})
	}
}

// writeClaudeSession は Claude Code の記録（本体 1 つと、あれば subagents の 1 ファイル）を HOME の下に置く。
func writeClaudeSession(t *testing.T, home string, main string, sub string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-work-proj")
	if err := os.MkdirAll(filepath.Join(dir, trTestSID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, trTestSID+".jsonl"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if sub != "" {
		if err := os.WriteFile(filepath.Join(dir, trTestSID, "subagents", "agent-a.jsonl"), []byte(sub), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// jsonlLine は text を 1 つ持つ 1 行（秘密にも絶対パスにも当たらない、伸びても縮まない文字列）。
func jsonlLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "text": text})
	return string(b) + "\n"
}

// noisy は圧縮されにくい（gzip で縮まない）英数字の列。
func noisy(n int) string {
	var b strings.Builder
	x := uint32(12345)
	for b.Len() < n {
		x = x*1664525 + 1013904223
		b.WriteByte("abcdefghijklmnopqrstuvwxyz"[(x>>16)%26])
	}
	return b.String()
}

// 413 で上限を半分にしていく経路: 常に 413 を返すサーバでも、下限（64KiB）で止まって終了コード 1 になり、
// チャンクの POST は 32MiB から 64KiB までの 10 回で打ち切る。文面には送り済みのチャンク数が出る。
// 本体を受け付けてから副ファイルで拒まれる場合は、送り済みが 1 と出る（「何も送らなかった」と言わない）。
// 副ファイルの 1 行が下限より大きければ、分け直しの止め（rejected）になる。ここでも送り済みの数が出る。
func TestPushHalvesOn413AndReportsSentChunks(t *testing.T) {
	const maxPosts = 10 // 32MiB から 64KiB まで 9 回半分にする + 最初の 1 回
	for _, c := range []struct {
		name         string
		limit        int    // サーバが受け付けるチャンクの大きさ（伸ばした後）
		main, sub    string // 本体と副ファイルの中身
		wantPosts    int
		wantAccepted int
		wantMsg      string // 標準エラーに出る文面の一部
	}{
		{"常に 413（本体だけ）", 0, jsonlLine("hello"), "", maxPosts, 0, "送り済みのチャンクは 0 個です（続きは"},
		{"本体の後に副ファイルが下限でも拒まれる", 20 << 10, jsonlLine("hello"), jsonlLine(noisy(30<<10)) + jsonlLine(noisy(30<<10)),
			1 + maxPosts, 1, "送り済みのチャンクは 1 個です（続きは"},
		{"副ファイルの 1 行が下限より大きい", 50 << 10, jsonlLine("hello"), jsonlLine(noisy(100 << 10)), 0, 1, "送れないファイルが 1 個あります。送り済みのチャンクは 1 個"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &trFake{limit: c.limit, lengths: map[string]int64{}}
			srv := httptest.NewServer(f)
			defer srv.Close()
			vars, home, _ := trEnv(t, srv.URL)
			writeClaudeSession(t, home, c.main, c.sub)
			code, stdout, stderr := runSession(t, vars, "transcript", "push", trTestSID, "--agent", "claude-code")
			if code != 1 {
				t.Fatalf("終了コード %d（1 のはず）\nstdout %q\nstderr %q", code, stdout, stderr)
			}
			if !strings.Contains(stderr, c.wantMsg) {
				t.Errorf("送り済みの数が文面に出ていません（%q のはず）: %q", c.wantMsg, stderr)
			}
			if strings.Contains(stderr, "何も送りませんでした") && c.wantAccepted > 0 {
				t.Errorf("送り済みがあるのに「何も送りませんでした」と言っています: %q", stderr)
			}
			if f.accepted != c.wantAccepted {
				t.Errorf("受け付けたチャンク %d（%d のはず）", f.accepted, c.wantAccepted)
			}
			if c.wantPosts > 0 && f.posts != c.wantPosts {
				t.Errorf("チャンクの POST %d 回（%d 回のはず。半分にして分け直す回数の上限）", f.posts, c.wantPosts)
			}
			if f.posts > 1+maxPosts {
				t.Errorf("POST が上限 %d 回を超えました: %d", 1+maxPosts, f.posts)
			}
		})
	}
}

// list_files の誤りに絶対パスを出さない。探索の後に Lstat が失敗した（ファイルが消えた）ときの OS の誤りは
// パスを持つので、理由だけを出す。対照: 理由（OS の文言）は残る。素の誤りには実際にパスが入る（直さないと出る）。
func TestPushListFilesErrorHasNoAbsolutePath(t *testing.T) {
	const gone = "/nonexistent-root/secret-dir/" + trTestSID + ".jsonl"
	pe := &fs.PathError{Op: "lstat", Path: gone, Err: syscall.ENOENT}
	if !strings.Contains(pe.Error(), "/nonexistent-root") {
		t.Fatalf("前提が崩れています: 素の誤りにパスが入りません: %v", pe)
	}
	prev := discoverTranscriptFiles
	discoverTranscriptFiles = func(agent, sid, cwd string, o usagesnap.Options) (*usagesnap.TranscriptFiles, error) {
		return nil, pe
	}
	defer func() { discoverTranscriptFiles = prev }()

	vars, _, _ := trEnv(t, "http://127.0.0.1:1")
	code, stdout, stderr := runSession(t, vars, "transcript", "push", trTestSID, "--agent", "claude-code")
	if code != 1 {
		t.Fatalf("終了コード %d（1 のはず）: %q %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "会話記録のファイルを並べられません: "+syscall.ENOENT.Error()) {
		t.Errorf("理由が出ていません: %q", stderr)
	}
	for _, leak := range []string{"/nonexistent-root", "secret-dir", trTestSID, "lstat"} {
		if strings.Contains(stderr, leak) {
			t.Errorf("文面に %q が入っています（絶対パスは出さない）: %q", leak, stderr)
		}
	}
}

// pull の OS の系統: 同じ系統（Linux と macOS）なら、送った端末の OS が違っても注意を 1 行出して置く。
// 同じ OS なら注意は出ない。対照: Windows と POSIX の間は、置かずに止める。
func TestPullAcrossOSFamilies(t *testing.T) {
	const content = `{"type":"user","text":"hello"}` + "\n"
	sum := sha256.Sum256([]byte(content))
	serve := func(originOS string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/transcripts/7"):
				writeJSONResp(w, 200, map[string]any{
					"transcript": map[string]any{"id": 7, "agent": "claude-code", "vendor_session_id": trTestSID, "origin_os": originOS,
						"created_at": "2026-10-25T00:00:00Z", "seq": 1},
					"chunks": []map[string]any{{"seq": 1, "file_key": trTestSID + ".jsonl", "file_kind": "jsonl", "src_offset": 0,
						"src_length": len(content), "sha256": hex.EncodeToString(sum[:]), "size": len(content)}},
				})
			case strings.HasSuffix(r.URL.Path, "/transcripts/7/chunks/1"):
				w.Header().Set("Content-Type", "application/octet-stream")
				io.WriteString(w, content)
			default:
				writeJSONResp(w, 404, map[string]any{"error": map[string]any{"code": "unknown_api", "message": r.URL.Path}})
			}
		}))
	}
	pull := func(originOS string) (code int, stdout, stderr string, placed []string) {
		srv := serve(originOS)
		defer srv.Close()
		vars, home, _ := trEnv(t, srv.URL)
		code, stdout, stderr = runSession(t, vars, "transcript", "pull", "7")
		placed, _ = filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
		return
	}
	notice := i18n.T(i18n.JA, "cli.transcript.pull.other_os")
	refused := i18n.Text(i18n.JA, i18n.Errorf("cli.err.transcript.cross_os", "id", 7))
	if !strings.Contains(notice, "注意") || !strings.Contains(refused, "置きません") {
		t.Fatalf("前提が崩れています: 文面を取れません: %q %q", notice, refused)
	}

	host := runtime.GOOS
	if host == "windows" {
		// Windows の端末から見て、同じ系統の別の OS は無い。POSIX の記録が拒まれることだけを見る
		code, _, stderr, placed := pull("linux")
		if code != 1 || !strings.Contains(stderr, refused) || len(placed) != 0 {
			t.Errorf("Windows から POSIX の記録: %d %q %v", code, stderr, placed)
		}
		return
	}
	other := "darwin"
	if host == "darwin" {
		other = "linux"
	}
	code, stdout, stderr, placed := pull(host)
	if code != 0 || len(placed) != 1 || strings.Contains(stderr, notice) {
		t.Fatalf("同じ OS（%s）: 終了コード %d・置いたもの %v・注意が出ないはず\nstdout %q\nstderr %q", host, code, placed, stdout, stderr)
	}
	code, stdout, stderr, placed = pull(other)
	if code != 0 || len(placed) != 1 || !strings.Contains(stderr, notice) {
		t.Errorf("%s で %s の記録: 終了コード %d・置いたもの %v・注意が出るはず\nstdout %q\nstderr %q", host, other, code, placed, stdout, stderr)
	} else if b, err := os.ReadFile(placed[0]); err != nil || !strings.Contains(string(b), `"text":"hello"`) {
		t.Errorf("置いた中身: %v %q", err, b)
	}
	// 対照: Windows と POSIX は置かずに止める（注意も出さない）
	code, stdout, stderr, placed = pull("windows")
	if code != 1 || !strings.Contains(stderr, refused) || len(placed) != 0 || strings.Contains(stderr, notice) {
		t.Errorf("POSIX から Windows の記録: 終了コード %d・置いたもの %v\nstdout %q\nstderr %q", code, placed, stdout, stderr)
	}
}
