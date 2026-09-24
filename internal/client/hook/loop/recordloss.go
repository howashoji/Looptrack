package loop

// 記録の消失の検知（stop-handoff-freshness の 1 つ目の段）。
//
// 鮮度ガードは「引き継ぎが作業の完了に追いついていない（更新が遅れている）」しか見ない。ファイルが**丸ごと無くなった**
// ときは、マーカーが積まれていなければ何も言わず、積まれていても「更新が遅れている」としか言わない。
// 2026-09-20 に記憶のディレクトリ（引き継ぎと注意の記録）が丸ごと消え、版管理の外だったので復元できず、
// たまたま全文を読んでいたセッションの文脈から書き戻した。**消えたことに誰も気づかなかった**のが害の本体なので、
// 「更新が遅れている」とは別の文言で「前回あった記録が無い」ことを知らせる。
//
// 控え（前回見た記録の一覧）の置き場が、この仕掛けの肝になる。控えを 1 か所にすると、**その控えごと消えたときに
// 「前回は何も無かった」＝消失なし、と誤って判定する**（実際に消えたのは .claude/ の配下であり、鮮度ガードの
// 状態（.claude/.looptrack-freshness/）も同じ場所にある）。そこで控えを次の 2 か所へ同時に書き、
// **読むときは 2 つの和**を「前回あったもの」とする:
//
//   - <状態>/.looptrack-freshness/memories-seen.json（他の鮮度の状態と同じ場所）
//   - <git のディレクトリ>/looptrack/memories-seen.json（作業ツリーごと。git は .git の中を消さないので、
//     .claude/ を消す操作・git clean -fdx のどちらでも残る）
//
// 片方だけが消えても、もう片方から「前回あったもの」が分かる。控え自体が消えていた（片方が欠けていた）ことも
// 知らせに添える（控えが消えたのに黙っていると、次からは本当に何も分からなくなるため）。
//
// 記録の名前は**辿る前のパス**（<ルート>からの相対）で持つ。記憶のディレクトリが版管理下への symlink に
// 置き換わっても、控えの中の名前は .claude/memories/handoff.md のまま変わらず、移設そのものを「消失」と
// 誤って読まない（実体のパスで持つと、移設した瞬間に全件が「無くなった」ように見える）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// lostListMax は知らせに並べる記録の数の上限。
const lostListMax = 20

// memoriesRoot は記憶・引き継ぎのパスを解決するルート。
//
// root（CLAUDE_PROJECT_DIR → cwd）が作業ツリー（git worktree add で作った場所）を指していても、**本体の作業ツリー**を返す。
// 記憶は 1 本にまとめないと、書いた引き継ぎは作業ツリーに取り残されて誰にも読まれず（worktree prune で消える）、
// 読む側は .gitignore された .claude/memories の symlink が作業ツリーに無いために何も読めない。
// git でない・本体が決められないときは root のまま（今までどおりの挙動に落とす）。
func memoriesRoot(ev hookio.Event, e *Env) string {
	r := root(ev, e)
	m := mainWorktreeRoot(r)
	if m == "" || m == realpath(r) {
		return r // git でない、または既に本体。パスの見え方も変えない
	}
	return m
}

// memoriesDir は記憶のディレクトリ（override → LOOPTRACK_LOOP_MEMORIES_DIR → <本体のルート>/.claude/memories）。
func memoriesDir(ev hookio.Event, e *Env, override string) string {
	mem := override
	if mem == "" {
		mem = e.env("LOOPTRACK_LOOP_MEMORIES_DIR")
	}
	if mem == "" {
		mem = filepath.Join(memoriesRoot(ev, e), ".claude", "memories")
	}
	return e.abs(mem)
}

// handoffFile は file バックエンドの引き継ぎのファイル（LOOPTRACK_LOOP_HANDOFF_FILE → <本体のルート>/.claude/memories/handoff.md）。
func handoffFile(ev hookio.Event, e *Env) string {
	h := e.env("LOOPTRACK_LOOP_HANDOFF_FILE")
	if h == "" {
		h = filepath.Join(memoriesRoot(ev, e), ".claude", "memories", "handoff.md")
	}
	return e.abs(h)
}

// recordSnapshot は控え（前回見た記録の一覧）。
//
// dir・handoff は、そのとき何を見ていたか（設定）。**設定が変わった控えとは突き合わせない。**
// 見る場所が変われば見える記録も変わるので、設定の変更を消失として知らせてしまう
// （LOOPTRACK_LOOP_MEMORIES_DIR・LOOPTRACK_LOOP_HANDOFF_FILE を差し替えた検査で実際に起きた）。
// 設定が変わったときは、その場で新しい設定の控えを取り直すだけにする。
type recordSnapshot struct {
	At      int64    `json:"at"`      // 控えを書いた時刻（UNIX 秒）
	Dir     string   `json:"dir"`     // 記憶のディレクトリ（recordKey）
	Handoff string   `json:"handoff"` // 引き継ぎのファイル（recordKey）
	Files   []string `json:"files"`   // 記録の名前（recordKey）
}

// snapshotPaths は控えの置き場（2 か所。git でなければ mirror は ""）。
func snapshotPaths(ev hookio.Event, e *Env) (primary, mirror string) {
	primary = filepath.Join(stateDir(ev, e), ".looptrack-freshness", "memories-seen.json")
	if _, g := gitDirOf(root(ev, e)); g != "" {
		mirror = filepath.Join(g, "looptrack", "memories-seen.json")
	}
	return primary, mirror
}

// recordKey は控えに書く記録の名前（<ルート>からの相対・/ 区切り。ルートの外なら絶対パス）。
// symlink は辿らない（辿ると移設で名前が変わり、移設が消失に見える）。
func recordKey(p, r string) string {
	p = absClean(p)
	if under(p, absClean(r)) {
		return relpath(p, r)
	}
	return filepath.ToSlash(p)
}

// currentRecords はいま在る記録（記憶のディレクトリの *.md と、file バックエンドの引き継ぎ）。
func currentRecords(ev hookio.Event, e *Env) []string {
	r := root(ev, e)
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		k := recordKey(p, r)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	for _, p := range globMD(memoriesDir(ev, e, "")) {
		if isFile(p) {
			add(p)
		}
	}
	if h := handoffFile(ev, e); isFile(h) {
		add(h)
	}
	sort.Strings(out)
	return out
}

// readSnapshot は控えを読む（無い・壊れている・今と設定が違えば ok = false）。
func readSnapshot(path string, now recordSnapshot) (recordSnapshot, bool) {
	if path == "" {
		return recordSnapshot{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return recordSnapshot{}, false
	}
	var s recordSnapshot
	if json.Unmarshal(b, &s) != nil {
		return recordSnapshot{}, false
	}
	if s.Dir != now.Dir || s.Handoff != now.Handoff {
		return recordSnapshot{}, false
	}
	return s, true
}

// writeSnapshot は控えを書く（書けなくても黙って諦める＝ fail-open）。
func writeSnapshot(path string, s recordSnapshot) {
	if path == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o777) != nil {
		return
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(b, '\n'), 0o666)
}

// StopRecordLoss は、前回あった記録が今回は無いことを知らせる（無ければ何も出さない）。
// 知らせたあとに控えを今の姿で書き直すので、同じ消失で何度も差し戻さない。
func StopRecordLoss(ev hookio.Event, e *Env) hookio.Result {
	r := root(ev, e)
	now := recordSnapshot{
		At:      e.Now().Unix(),
		Dir:     recordKey(memoriesDir(ev, e, ""), r),
		Handoff: recordKey(handoffFile(ev, e), r),
	}
	primary, mirror := snapshotPaths(ev, e)
	ps, pok := readSnapshot(primary, now)
	ms, mok := readSnapshot(mirror, now)
	prev := map[string]bool{}
	for _, f := range append(append([]string(nil), ps.Files...), ms.Files...) {
		prev[f] = true
	}
	at := ps.At
	if ms.At > at {
		at = ms.At
	}

	cur := currentRecords(ev, e)
	have := map[string]bool{}
	for _, f := range cur {
		have[f] = true
	}
	var missing []string
	for f := range prev {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)

	// 控えは今の姿で書き直す（片方しか無かったときは、これで 2 か所にそろう）
	if len(cur) > 0 || pok || mok {
		now.Files = cur
		writeSnapshot(primary, now)
		writeSnapshot(mirror, now)
	}
	if len(missing) == 0 {
		return hookio.Result{}
	}

	lang := e.lang()
	shown := missing
	if len(shown) > lostListMax {
		shown = shown[:lostListMax]
	}
	list := make([]string, 0, len(shown))
	for _, m := range shown {
		list = append(list, "  - "+m)
	}
	when := i18n.T(lang, "loop.handoff.lost.unknown_time")
	if at > 0 {
		when = time.Unix(at, 0).Local().Format("2006-01-02 15:04:05")
	}
	reason := i18n.T(lang, "loop.handoff.lost.reason", "list", strings.Join(list, "\n"), "time", when)
	// 控えそのものが消えていた（残っていた控えから突き合わせた）ことも知らせる。黙って書き直すと、
	// 「控えが消えても検知は生きている」ことが誰にも見えない
	if !pok && mok {
		reason += "\n\n" + i18n.T(lang, "loop.handoff.lost.state_lost",
			"path", recordKey(primary, r), "mirror", recordKey(mirror, r))
	}
	return hookio.Result{Block: reason}
}
