package cli

// looptrack session transcript pull（DESIGN.md §9-8「取り込み（常に fork）」）。
//
// 順:
//  1. GET /transcripts/{id} でチャンクの一覧を取る。最後の消去より後のチャンクだけを使い、1 つも無ければ「消去済み」で止める。
//     送った端末と OS の系統（Windows とそれ以外）が違う記録は置かない（区切りが混ざるため。v1 の対象外）。
//  2. 取り込む端末の値（作業ツリーのルートの realpath・記録のディレクトリ・設定の置き場・ホーム）を決める。
//     ルートが見つからなければ置かない（プレースホルダを戻せない）。
//  3. 元のセッション ID のファイルがこの端末にあれば置かない（送った端末自身・既に同じ記録がある）。
//  4. チャンクの本体を seq の順に取り、sha256 を確かめて伸ばし、transcript.Restore でこの端末の値に戻して、ファイルごとにつなぐ。
//  5. 新しいセッション ID で置く（元の ID は使わない。行の中の元の ID も新しい ID に書き換える）。新しい ID は記録ごとに決まった値
//     （forkedID）なので、2 回目の pull は同じ場所を見て、同じ中身なら何も変えずに再開のコマンドだけを出す。違えば置かずに止める。
//  6. 再開のコマンドを表示する（Claude Code は `claude --resume <置いた本体> --fork-session`。Windows で本体のパスが ' を含むときは本体の代わりに新しい ID。Codex は `codex fork <ID>`）。AI は起動しない。
//
// 置く場所:
//   - Claude Code: <設定の置き場>/projects/<ルートの realpath の slug（agentdirs.ClaudeSlug）>/<新しい ID>.jsonl と <新しい ID>/ の下。
//   - Codex（要実測。手元に codex が無く、置いた rollout を codex fork が見つけるかは確かめていない）:
//     <CODEX_HOME>/sessions/<年>/<月>/<日>/rollout-<時刻>-<新しい ID>.jsonl（名前の中のスレッド ID と session_meta.id を新しい ID に）。
//
// --out <dir> は AI の置き場ではなく dir に同じ形で書き出す（中身を見るため。再開のコマンドは出さない）。
// --print-resume は置くのは同じで、標準出力に再開のコマンドの 1 行だけを出す（ほかの表示は標準エラーへ）。

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/agentdirs"
	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/transcript"
	"github.com/howashoji/looptrack/internal/client/usagesnap"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// trMaxInflate は本体 1 つを伸ばす大きさの上限（サーバの上限の既定 32MiB に十分な余裕。壊れた本体で手元を埋めない）。
const trMaxInflate = 1 << 30

// trSafeID はパスと glob に使ってよいセッション ID の形（usagesnap の探索と同じ）。
var trSafeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// osFamily は OS の系統（区切りの流儀）。
func osFamily(goos string) string {
	if goos == "windows" {
		return "windows"
	}
	return "posix"
}

// forkedID は取り込んだ写しに付ける新しいセッション ID（UUID の形）。記録（agent・元の ID・サーバの ID・作った時刻）ごとに決まった値なので、
// 同じ記録を 2 回 pull しても同じ場所を見る。元の ID とは別の値になる（写しが元の会話と同じ ID を名乗らない）。
func forkedID(agent, sid string, id int64, created string) string {
	sum := sha256.Sum256([]byte("looptrack-transcript-pull\x00" + agent + "\x00" + sid + "\x00" + strconv.FormatInt(id, 10) + "\x00" + created))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// trPlaced は置くファイル 1 つ（置き場の中の相対パスと中身）。
type trPlaced struct {
	rel  string
	data []byte
}

// pullLayout は置く場所と再開のコマンド。
type pullLayout struct {
	base    string // 置く置き場（Claude は記録のディレクトリ、Codex は sessions、--out ならその dir）
	main    string // 置いた本体の絶対パス
	resume  string // 再開のコマンド（--out なら ""）
	dupGlob []string
}

// liveChunks は最後の消去より後のチャンク（seq の順）。消去済みしか無ければ purged が true。
func liveChunks(chunks []trChunk) (live []trChunk, purged bool) {
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Seq < chunks[j].Seq })
	last := -1
	for i, ch := range chunks {
		if ch.Purged {
			last = i
		}
	}
	return chunks[last+1:], last >= 0
}

// cmdTranscriptPull は会話記録を取り込む。
func cmdTranscriptPull(c *Ctx, v *Values) error {
	id, err := trID(v.Str("id"))
	if err != nil {
		return err
	}
	out := v.Str("out")
	printOnly := v.Bool("print_resume")
	if out != "" && printOnly {
		return i18n.Errorf("cli.err.transcript.out_and_resume")
	}
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	var got struct {
		Transcript trState   `json:"transcript"`
		Chunks     []trChunk `json:"chunks"`
	}
	if _, err := trDo(cl, api.Request{Method: http.MethodGet, Path: trPath(id, "")}, &got); err != nil {
		return err
	}
	t := got.Transcript
	sid := t.VendorSessionID
	if (t.Agent != trAgentClaude && t.Agent != trAgentCodex) || !trSafeID.MatchString(sid) {
		return i18n.Errorf("cli.err.transcript.pull_agent", "id", id, "agent", t.Agent)
	}
	if osFamily(t.OriginOS) != osFamily(runtime.GOOS) {
		return i18n.Errorf("cli.err.transcript.cross_os", "id", id)
	}
	live, purged := liveChunks(got.Chunks)
	if len(live) == 0 {
		if purged {
			return i18n.Errorf("cli.err.transcript.purged", "id", id)
		}
		return i18n.Errorf("cli.err.transcript.empty", "id", id)
	}
	// 表示は標準出力（--print-resume のときは再開のコマンドの 1 行だけを標準出力に出し、ほかは標準エラーへ）
	info := c.Stdout
	if printOnly {
		info = c.Stderr
	}
	if t.OriginOS != runtime.GOOS {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.transcript.pull.other_os"))
	}

	target, lay, err := c.pullTarget(t, id, out)
	if err != nil {
		return err
	}
	newID := forkedID(t.Agent, sid, id, t.CreatedAt)
	// 元の ID の記録がこの端末にあれば置かない（送った端末自身で pull した・既に同じ会話がある）
	for _, pat := range lay.dupGlob {
		if hits, _ := filepath.Glob(pat); len(hits) > 0 {
			sort.Strings(hits)
			return i18n.Errorf("cli.err.transcript.exists_original", "id", id, "path", hits[0])
		}
	}
	files, prose, err := c.downloadRestore(cl, id, t, live, target)
	if err != nil {
		return err
	}
	placed, mainRel, err := forkFiles(t.Agent, sid, newID, files)
	if err != nil {
		return err
	}
	lay.main = filepath.Join(lay.base, filepath.FromSlash(mainRel))
	if out == "" {
		if t.Agent == trAgentClaude {
			lay.resume = "claude --resume " + claudeResumeArg(runtime.GOOS, lay.main, newID) + " --fork-session"
			// 同じ写しが別の slug にあれば置かない（別の作業ツリーで取り込んだもの）
			if hits, _ := filepath.Glob(filepath.Join(filepath.Dir(lay.base), "*", newID+".jsonl")); len(hits) > 0 {
				for _, h := range hits {
					if filepath.Clean(h) != filepath.Clean(lay.main) {
						return i18n.Errorf("cli.err.transcript.exists_other", "id", id, "path", h)
					}
				}
			}
		} else {
			lay.resume = "codex fork " + newID
		}
	}
	state, where := comparePlaced(lay.base, placed)
	switch state {
	case placedDiffer:
		return i18n.Errorf("cli.err.transcript.exists_differ", "id", id, "path", where)
	case placedSame:
		fmt.Fprintln(info, i18n.T(c.Lang, "cli.transcript.pull.already", "id", id, "path", lay.main))
	default:
		n, err := writePlaced(lay.base, placed)
		if err != nil {
			return err
		}
		if out != "" {
			fmt.Fprintln(info, i18n.T(c.Lang, "cli.transcript.pull.written_out", "id", id, "count", n, "path", lay.base))
		} else {
			fmt.Fprintln(info, i18n.T(c.Lang, "cli.transcript.pull.placed", "id", id, "count", n, "path", lay.main))
		}
	}
	if prose > 0 {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.transcript.pull.prose_notice", "count", prose))
	}
	if lay.resume == "" {
		return nil
	}
	if printOnly {
		c.Println(lay.resume)
	} else {
		c.Println(i18n.T(c.Lang, "cli.transcript.pull.resume", "command", lay.resume))
	}
	if t.Agent == trAgentCodex {
		fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.transcript.pull.codex_root", "root", target.Root))
	}
	fmt.Fprintln(c.Stderr, i18n.T(c.Lang, "cli.transcript.pull.fork_only"))
	return nil
}

// pullTarget は戻す先の値と置く場所を決める。
func (c *Ctx) pullTarget(t trState, id int64, out string) (transcript.Target, pullLayout, error) {
	var tg transcript.Target
	var lay pullLayout
	top := hookio.GitRoot(c.Root)
	if top == "" {
		return tg, lay, i18n.Errorf("cli.err.transcript.no_root", "id", id)
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	home := agentdirs.Home(c.Env.Get)
	tg.Root, tg.Home = top, home
	if t.Agent == trAgentClaude {
		projects := agentdirs.ClaudeProjectsDir(c.Env.Get, home)
		if projects == "" {
			return tg, lay, i18n.Errorf("cli.err.transcript.no_vendor_dir", "agent", t.Agent)
		}
		tg.VendorDir = agentdirs.ClaudeConfigDir(c.Env.Get, home)
		tg.SessionDir = filepath.Join(projects, agentdirs.ClaudeSlug(top))
		lay.base = tg.SessionDir
		lay.dupGlob = []string{filepath.Join(projects, "*", t.VendorSessionID+".jsonl")}
	} else {
		sessions := agentdirs.CodexSessionsDir(c.Env.Get, home)
		if sessions == "" {
			return tg, lay, i18n.Errorf("cli.err.transcript.no_vendor_dir", "agent", t.Agent)
		}
		tg.VendorDir = agentdirs.CodexHome(c.Env.Get, home)
		lay.base = sessions
		lay.dupGlob = []string{filepath.Join(sessions, "*", "*", "*", "rollout-*"+t.VendorSessionID+"*.jsonl")}
	}
	if out != "" {
		dir, err := absUserPath(c, out)
		if err != nil {
			return tg, lay, err
		}
		lay.base = dir
		lay.dupGlob = nil // 書き出しは AI の置き場ではないので、元の記録があっても書ける
	}
	return tg, lay, nil
}

// downloadRestore はチャンクの本体を順に取り、この端末の値に戻してファイルごとにつなぐ（file_key → 中身）。
// 散文の中に残った「書き換えなかった絶対パス」の数（チャンクの stats の合計）も返す。
func (c *Ctx) downloadRestore(cl *api.Client, id int64, t trState, live []trChunk, target transcript.Target) (map[string][]byte, int, error) {
	files := map[string][]byte{}
	next := map[string]int64{}
	prose := 0
	for _, ch := range live {
		if !usagesnap.ValidFileKey(t.Agent, ch.FileKey) {
			return nil, 0, i18n.Errorf("cli.err.transcript.bad_key", "id", id, "seq", ch.Seq)
		}
		if ch.SrcOffset != next[ch.FileKey] {
			return nil, 0, i18n.Errorf("cli.err.transcript.gap", "id", id, "seq", ch.Seq)
		}
		res, err := trDo(cl, api.Request{Method: http.MethodGet, Path: trPath(id, "/chunks/"+strconv.FormatInt(ch.Seq, 10)),
			Headers: map[string]string{"Accept": "*/*"}}, nil)
		if err != nil {
			return nil, 0, err
		}
		sum := sha256.Sum256(res.Raw)
		if ch.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(sum[:]), ch.SHA256) {
			return nil, 0, i18n.Errorf("cli.err.transcript.sha", "id", id, "seq", ch.Seq)
		}
		body := res.Raw
		enc := res.Header.Get("X-Looptrack-Transcript-Encoding")
		if enc == "" {
			enc = ch.Encoding
		}
		if enc == "gzip" {
			if body, err = gunzipLimit(body); err != nil {
				return nil, 0, i18n.Errorf("cli.err.transcript.inflate", "id", id, "seq", ch.Seq)
			}
		}
		restored, err := transcript.Restore(body, transcript.Kind(ch.FileKind), ch.Nonce, target)
		if err != nil {
			return nil, 0, i18n.Wrapf(err, "cli.err.transcript.restore", "id", id, "seq", ch.Seq)
		}
		files[ch.FileKey] = append(files[ch.FileKey], restored...)
		next[ch.FileKey] = ch.SrcOffset + ch.SrcLength
		var st struct {
			Prose int `json:"unrewritten_prose"`
		}
		if json.Unmarshal(ch.Stats, &st) == nil {
			prose += st.Prose
		}
	}
	return files, prose, nil
}

func gunzipLimit(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(zr, trMaxInflate+1))
	if err != nil {
		return nil, err
	}
	if len(out) > trMaxInflate {
		return nil, errors.New("too large")
	}
	return out, nil
}

// forkFiles は戻した中身を、新しいセッション ID の名前と中身にする（置き場の中の相対パス・本体の相対パス）。
// Claude Code は <元の ID>.jsonl → <新しい ID>.jsonl、<元の ID>/… → <新しい ID>/…。Codex は名前に元の ID を含む rollout の名前だけを変える
// （history_base がたどる別のスレッドの rollout は元の名前のまま）。中身は jsonl と json の中の元の ID をすべて新しい ID にする
// （sessionId・session_meta.id・tool-results を指すパス。text（tool-results）は散文なので変えない）。
func forkFiles(agent, sid, newID string, files map[string][]byte) ([]trPlaced, string, error) {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []trPlaced
	mainRel := ""
	for _, k := range keys {
		data := files[k]
		var rel string
		switch agent {
		case trAgentClaude:
			switch {
			case k == sid+".jsonl":
				rel = newID + ".jsonl"
				mainRel = rel
			case strings.HasPrefix(k, sid+"/"):
				rel = newID + "/" + strings.TrimPrefix(k, sid+"/")
			default:
				return nil, "", i18n.Errorf("cli.err.transcript.foreign_key", "file", k)
			}
			if !strings.Contains(rel, "/tool-results/") {
				data = bytes.ReplaceAll(data, []byte(sid), []byte(newID))
			}
		case trAgentCodex:
			dir, name := k[:strings.LastIndex(k, "/")+1], k[strings.LastIndex(k, "/")+1:]
			if strings.Contains(name, sid) {
				name = strings.Replace(name, sid, newID, 1)
				if mainRel == "" || strings.HasSuffix(name, newID+".jsonl") {
					mainRel = dir + name
				}
			}
			rel = dir + name
			data = bytes.ReplaceAll(data, []byte(sid), []byte(newID))
		}
		out = append(out, trPlaced{rel: rel, data: data})
	}
	if mainRel == "" {
		return nil, "", i18n.Errorf("cli.err.transcript.no_main", "session", sid)
	}
	return out, mainRel, nil
}

type placedState int

const (
	placedNone   placedState = iota // 1 つも無い・一部だけ（同じ中身）ある
	placedSame                      // 全部が同じ中身である
	placedDiffer                    // 中身の違うファイルがある
)

// comparePlaced は置く先に既にあるファイルを比べる。違うものがあればその場所も返す。
func comparePlaced(base string, placed []trPlaced) (placedState, string) {
	same := 0
	for _, p := range placed {
		full := filepath.Join(base, filepath.FromSlash(p.rel))
		b, err := os.ReadFile(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !bytes.Equal(b, p.data) {
			return placedDiffer, full
		}
		same++
	}
	if same == len(placed) {
		return placedSame, ""
	}
	return placedNone, ""
}

// writePlaced は無いファイルだけを書く（O_EXCL。途中で失敗したら、この回に書いたものを消す）。書いた数を返す。
func writePlaced(base string, placed []trPlaced) (int, error) {
	var written []string
	undo := func() {
		for _, p := range written {
			os.Remove(p)
		}
	}
	for _, p := range placed {
		full := filepath.Join(base, filepath.FromSlash(p.rel))
		if _, err := os.Stat(full); err == nil {
			continue // 前の回に置いた同じ中身（comparePlaced が確かめた）
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			undo()
			return 0, i18n.Errorf("cli.err.transcript.write", "path", full, "reason", errReason(err))
		}
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			undo()
			return 0, i18n.Errorf("cli.err.transcript.write", "path", full, "reason", errReason(err))
		}
		_, werr := f.Write(p.data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		written = append(written, full)
		if werr != nil {
			undo()
			return 0, i18n.Errorf("cli.err.transcript.write", "path", full, "reason", errReason(werr))
		}
	}
	return len(written), nil
}

// claudeResumeArg は claude --resume に渡す値を、利用者がそのまま貼って動く形で返す。
// ふだんは置いた本体の絶対パスを shellQuote で囲む（--resume はパスを直接読む）。
// Windows で ' を含むパスだけは、1 つの引用で PowerShell と Git Bash の両方を満たせない
// （POSIX の '"'"' は PowerShell では閉じた後の文字になり、PowerShell の ' を 2 つ重ねる形は Git Bash で連結されて ' が消える）。
// そこではパスの代わりに新しいセッション ID を渡す。ID は UUID の形で引用が要らず、
// --resume は ID なら設定の置き場の projects の下から探す（どの cwd からでも見つかる。実測）。
// 同じ ID の写しを別の slug に置かないので、ID で選ぶ本体は置いたものに 1 つに決まる。
func claudeResumeArg(goos, path, id string) string {
	if goos == "windows" && strings.Contains(path, "'") {
		return id
	}
	return shellQuote(path)
}
