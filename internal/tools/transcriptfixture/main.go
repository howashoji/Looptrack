// transcriptfixture は、AI の会話記録（Claude Code の JSONL と Codex の rollout）から、キーの構造だけを写し、
// 文字列の値をすべて合成の値に置き換えた写しを作る開発用の道具。会話記録の変換（internal/client/transcript）の
// テストに使う固定の材料を作るためのもので、配布物には入らない。この道具は標準ライブラリとリポジトリの中のパッケージだけで書く。
//
//	go run ./internal/tools/transcriptfixture -out <出力のディレクトリ> [-root <作業ツリーのルート>]... [-mask-key <キー>]... [-max-lines <N>] <記録>...
//
// 何を残し、何を置き換えるか:
//   - オブジェクトと配列の入れ子・キーの順・数（字面のまま）・真偽値・null は残す。
//   - 文字列の値はすべて合成の値にする。同じ値は 1 回の実行の中で同じ合成の値になる（等しさだけを残す）。
//     合成の値は出てきた順の番号から作り、元の値からは作らない（短い値を辞書で引き当てられないように）。
//   - UUID の形の値（sessionId・uuid・parentUuid など）は UUID の形の合成の値 00000000-0000-4000-8000-<12 桁の番号> にする。
//     等しさを残すので、親子の鎖（parentUuid → uuid）と、本体とサブエージェントの sessionId の一致がそのまま残る。
//   - 値全体が絶対パスなら、合成の絶対パスにする。作業ツリーのルート → /home/u/proj（2 つ目からは /home/u/proj2 …）、
//     Claude の設定の置き場の projects/<ルートの slug> → /home/u/.claude/projects/-home-u-proj、そのほかの設定の置き場 → /home/u/.claude、
//     Codex の置き場 → /home/u/.codex、ホーム → /home/u、それ以外 → /var/x。ルートの下の名前は 1 つずつ c<番号> に置き換え
//     （同じ名前は同じ番号。先頭の . と、よくある拡張子（下の knownExts）は残す）、ディレクトリの入れ子を保つ。
//   - 値の中に絶対パスが混ざっていれば（Bash の command・散文）、s<番号> の後ろに、中のパスを上と同じ規則で写したものを空白で並べる。
//     変換が「値の中の絶対パス」を書き換え・数える経路を、材料で通すため。
//   - 値が JSON のオブジェクトか配列の文字列なら（Codex の function_call.arguments など）、中身に同じ規則をかけて JSON の文字列に戻す。
//   - キーは、識別子の形（英字・数字・_ $ . -・48 文字まで）のものだけを残す。それ以外（パス・番号の並び・__ を含むもの・秘密らしいもの）と、
//     値から作られたキー（同じ綴りの文字列の値があり、数字を含むもの。JSON の文字列の中では数字の有無を問わない）は、
//     値と同じ規則で合成の値にする（パスのキーは合成のパスに、tool_use の id で引く表のキーは id の写しと同じ値になる）。
//
// 書き出す前に、写しに元の値が残っていないことを確かめる（verify）。入力の文字列の値（と置き換えたキー）のうち 4 文字以上のものを集め、
// 写しのどの文字列の値と置き換えたキーにも含まれていないこと、写しの値に domain.FindSecret が当たらないことを見る。外れれば何も書かずに失敗する。
// 報告は件数だけで、元の値は出さない。道具が自分で出す語（/home/u/proj などの合成の根）に含まれる元の値は、写しから来たものではないので数えない。
//
// 出力の置き場（-out の下）:
//   - Claude Code の本体: claude/<合成の sessionId>.jsonl
//   - Claude Code のサブエージェント（入力のパスに /subagents/ を含むもの）: claude/<合成の sessionId>/subagents/agent-<合成の agentId>.jsonl
//   - Codex の rollout（行に sessionId が無いもの）: codex/rollout-2000-01-01T00-00-00-<合成のスレッド ID>.jsonl
//
// 固定の材料（internal/client/transcript/testdata/）と作り直しの手順:
//
// 材料は Claude Code の 1 セッションの本体（先頭の 800 行）と、そのサブエージェントの 1 本（全行）から作った。
// 置いた名前は次のとおり（本体の sessionId が最初に番号を取るので、合成の sessionId は …0001 になる）。
//
//	claude/00000000-0000-4000-8000-000000000001.jsonl
//	claude/00000000-0000-4000-8000-000000000001/subagents/agent-s002251.jsonl
//
// Claude Code（や Codex）の版が上がって記録の形が変わったら、次の手順で作り直す。
//
//  1. 作り直す人の端末で、作業ツリーのルートで Claude Code のセッションを開き、ツールをいくつか使わせる
//     （Read・Edit・Bash・サブエージェントを 1 つ）。記録は ~/.claude/projects/<ルートの slug>/<sessionId>.jsonl と
//     <sessionId>/subagents/agent-*.jsonl にある（CLAUDE_CONFIG_DIR があればその下）。中身を開いて読まない。
//  2. 作業用のディレクトリ（リポジトリの外）に写す:
//     go run ./internal/tools/transcriptfixture -out <作業用のディレクトリ> -max-lines 800 \
//     ~/.claude/projects/<slug>/<sessionId>.jsonl ~/.claude/projects/<slug>/<sessionId>/subagents/agent-<id>.jsonl
//     （作業ツリーのルートは各行の cwd から決まる。違うときは -root で渡す）
//  3. 出力の最後の行「残したキー」を読む。値から作られた名前（プロジェクト・サーバ・人の名前など、ツールの出力の
//     オブジェクトのキーとしてだけ現れるもの）は規則では見分けられないので、あれば -mask-key <キー> を足して 2 からやり直す。
//  4. 古い材料を消して置き換える: rm -r internal/client/transcript/testdata/claude、
//     cp -r <作業用のディレクトリ>/claude internal/client/transcript/testdata/
//  5. go test ./internal/tools/transcriptfixture/ で、材料の文字列の値とキーが合成の値だけでできていることを確かめ、
//     bash deploy/public-scan.sh を通す。サブエージェントのファイルの名前が変わったら、この注釈の名前を直す。
//  6. 材料を読む変換のテストを走らせ、期待値の件数を新しい材料に合わせる。
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/domain"
)

// 合成の根。テストと材料の検査もこの値を使う。
const (
	fakeHome       = "/home/u"
	fakeRoot       = "/home/u/proj"
	fakeClaude     = "/home/u/.claude"
	fakeCodex      = "/home/u/.codex"
	fakeOther      = "/var/x"
	fakeUUIDPrefix = "00000000-0000-4000-8000-"
)

// knownExts は、パスの名前を置き換えても残す拡張子。道具が出す語の一部として扱う（元の値から写したものではない）。
var knownExts = []string{
	".go", ".md", ".json", ".jsonl", ".txt", ".sh", ".mjs", ".js", ".ts", ".yaml", ".yml",
	".sql", ".html", ".css", ".toml", ".mod", ".sum", ".py", ".log", ".png", ".pdf", ".zst", ".gz",
}

// vocabulary は、道具が元の値と関係なく出す語の並び。ここに含まれる元の値は、写しに現れても漏れではない。
func vocabulary() string {
	return strings.Join(append([]string{
		fakeRoot, fakeClaude + "/projects/-home-u-proj", fakeCodex + "/sessions", fakeOther,
		fakeUUIDPrefix, "rollout-2000-01-01T00-00-00-", "agent-", "claude/", "/subagents/", "codex/",
	}, knownExts...), "\x00")
}

var (
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// 値全体が 1 つの絶対パス（空白を含まない）。
	wholePathRe = regexp.MustCompile(`^/[^\s/][^\s]*$`)
	// 値の中の絶対パス。区切りの直後の / から、パスに使う文字が続く間。// で始まるもの（URL の後ろ）は取らない。
	innerPathRe = regexp.MustCompile("(?:^|[\\s\"'=:(\\[{,<>|;&`])(/[A-Za-z0-9._~+@%-][A-Za-z0-9._~+@%/-]*)")
	keepKeyRe   = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$.-]{0,47}$`)
	digitRunRe  = regexp.MustCompile(`[0-9]{4,}`)
	hexOnlyRe   = regexp.MustCompile(`^[0-9a-fA-F]{8,}$`)
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "transcriptfixture:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("transcriptfixture", flag.ContinueOnError)
	out := fs.String("out", "", "出力のディレクトリ")
	maxLines := fs.Int("max-lines", 0, "1 ファイルから写す行の上限（0 は全行）")
	var roots, masks multiFlag
	fs.Var(&roots, "root", "作業ツリーのルート（繰り返せる。無ければ各行の cwd から決める）")
	fs.Var(&masks, "mask-key", "残さずに合成の値にするキー（繰り返せる。値から作られたのに規則で見分けられないキー）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return errors.New("使い方: transcriptfixture -out <ディレクトリ> [-root <ルート>]... [-mask-key <キー>]... [-max-lines <N>] <記録>...")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	env := Env{Home: home, ClaudeDir: os.Getenv("CLAUDE_CONFIG_DIR"), CodexDir: os.Getenv("CODEX_HOME"), Roots: roots, MaskKeys: masks}
	if env.ClaudeDir == "" {
		env.ClaudeDir = filepath.Join(home, ".claude")
	}
	if env.CodexDir == "" {
		env.CodexDir = filepath.Join(home, ".codex")
	}
	var inputs []Input
	for _, p := range fs.Args() {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		inputs = append(inputs, Input{Path: p, Data: completeLines(b, *maxLines)})
	}
	files, st, err := Convert(env, inputs)
	if err != nil {
		return err
	}
	if err := Verify(env, inputs, files); err != nil {
		return err
	}
	for _, f := range files {
		dst := filepath.Join(*out, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, f.Data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: %d 行・%d バイト\n", f.Name, bytes.Count(f.Data, []byte("\n")), len(f.Data))
	}
	fmt.Fprintf(stdout, "文字列 %d 種・UUID %d 種・パスの名前 %d 種・置き換えたキー %d 種・作業ツリーのルート %d 個\n",
		st.Strings, st.UUIDs, st.Components, st.Keys, st.Roots)
	// 残したキーは材料にそのまま載る。値から作られた名前（プロジェクトの名前など）が混ざっていないかを人が見て、あれば -mask-key で作り直す。
	fmt.Fprintf(stdout, "残したキー %d 種: %s\n", len(st.KeptKeys), strings.Join(st.KeptKeys, " "))
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// completeLines は最後の改行までを返す（書きかけの行は写さない）。max が正なら先頭の max 行まで。
func completeLines(b []byte, max int) []byte {
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[:i+1]
	} else {
		return nil
	}
	if max <= 0 {
		return b
	}
	n, end := 0, 0
	for end < len(b) && n < max {
		i := bytes.IndexByte(b[end:], '\n')
		end += i + 1
		n++
	}
	return b[:end]
}

// Env は元の端末の置き場（合成の根に写す元）。
type Env struct {
	Home      string
	ClaudeDir string
	CodexDir  string
	Roots     []string // 空なら各行の cwd から決める
	MaskKeys  []string // 残さずに合成の値にするキー
}

// Input は 1 つの記録。
type Input struct {
	Path string
	Data []byte
}

// Output は書き出す 1 ファイル（Name は -out からの相対で / 区切り）。
type Output struct {
	Name string
	Data []byte
}

// Stats は置き換えた種類の数（値は持たない）と、残したキーの一覧。
type Stats struct {
	Strings, UUIDs, Components, Keys, Roots int
	KeptKeys                                []string
}

type synth struct {
	env       Env
	roots     []string // 長い順
	rootIndex map[string]int
	strs      map[string]int
	uuids     map[string]int
	comps     map[string]int
	keys      map[string]bool // 置き換えたキー（数えるだけ）
	kept      map[string]bool // 残したキー
	mask      map[string]bool // -mask-key
	vals      map[string]bool // 入力の文字列の値すべて（キーが値から作られたものかを見分ける）
	inner     int             // JSON の文字列の中をたどっている深さ
}

func newSynth(env Env) *synth {
	return &synth{env: env, rootIndex: map[string]int{}, strs: map[string]int{}, uuids: map[string]int{}, comps: map[string]int{},
		keys: map[string]bool{}, kept: map[string]bool{}, vals: map[string]bool{}, mask: maskSet(env)}
}

func maskSet(env Env) map[string]bool {
	m := map[string]bool{}
	for _, k := range env.MaskKeys {
		m[k] = true
	}
	return m
}

// Convert は記録を写す。入力の順と中身だけで出力が決まる。
func Convert(env Env, inputs []Input) ([]Output, Stats, error) {
	parsed := make([][]any, len(inputs))
	for i, in := range inputs {
		lines, err := parseLines(in.Data)
		if err != nil {
			return nil, Stats{}, fmt.Errorf("%d 番目の入力: %w", i+1, err)
		}
		parsed[i] = lines
	}
	s := newSynth(env)
	for _, lines := range parsed {
		for _, v := range lines {
			collect(v, func(x string) { s.vals[x] = true }, func(string) {})
		}
	}
	s.decideRoots(parsed)
	// 本体の sessionId が最初の番号を取るよう、sessionId を先に登録する。
	for _, lines := range parsed {
		for _, v := range lines {
			if o, ok := v.(*jsonorder.Object); ok {
				if id := o.String("sessionId"); id != "" {
					s.str(id)
				}
			}
		}
	}
	var outs []Output
	codexN := 0
	for i, lines := range parsed {
		var buf bytes.Buffer
		sessionID, codexID := "", ""
		for _, v := range lines {
			if o, ok := v.(*jsonorder.Object); ok {
				if sessionID == "" && o.String("sessionId") != "" {
					sessionID = o.String("sessionId")
				}
				if codexID == "" && o.String("type") == "session_meta" {
					if p := o.Object("payload"); p != nil {
						codexID = p.String("id")
					}
				}
			}
			writeJSON(&buf, s.value(v))
			buf.WriteByte('\n')
		}
		var name string
		switch {
		case sessionID != "" && strings.Contains(filepath.ToSlash(inputs[i].Path), "/subagents/"):
			base := strings.TrimSuffix(filepath.Base(inputs[i].Path), ".jsonl")
			name = "claude/" + s.str(sessionID) + "/subagents/agent-" + s.str(strings.TrimPrefix(base, "agent-")) + ".jsonl"
		case sessionID != "":
			name = "claude/" + s.str(sessionID) + ".jsonl"
		case codexID != "":
			name = "codex/rollout-2000-01-01T00-00-00-" + s.str(codexID) + ".jsonl"
		default:
			codexN++
			name = "codex/rollout-2000-01-01T00-00-00-" + fmt.Sprintf("%s%012d", fakeUUIDPrefix, 900000000000+codexN) + ".jsonl"
		}
		outs = append(outs, Output{Name: name, Data: buf.Bytes()})
	}
	kept := make([]string, 0, len(s.kept))
	for k := range s.kept {
		kept = append(kept, k)
	}
	sort.Strings(kept)
	return outs, Stats{Strings: len(s.strs), UUIDs: len(s.uuids), Components: len(s.comps), Keys: len(s.keys), Roots: len(s.roots), KeptKeys: kept}, nil
}

func parseLines(data []byte) ([]any, error) {
	var lines []any
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		n++
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		v, err := jsonorder.Decode(sc.Bytes())
		if err != nil {
			return nil, fmt.Errorf("%d 行目が JSON として読めません", n)
		}
		lines = append(lines, v)
	}
	return lines, sc.Err()
}

// decideRoots は作業ツリーのルートを決める。-root が無ければ、cwd の値を多い順に（同数なら先に出た順に）並べ、
// ほかのルートの下にあるもの・ホームそのもの・/ は除く。
func (s *synth) decideRoots(parsed [][]any) {
	cands := s.env.Roots
	if len(cands) == 0 {
		count, order := map[string]int{}, []string{}
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case *jsonorder.Object:
				for _, m := range x.Members {
					if str, ok := m.Value.(string); ok && m.Key == "cwd" && wholePathRe.MatchString(str) {
						if count[str] == 0 {
							order = append(order, str)
						}
						count[str]++
					}
					walk(m.Value)
				}
			case []any:
				for _, e := range x {
					walk(e)
				}
			}
		}
		for _, lines := range parsed {
			for _, v := range lines {
				walk(v)
			}
		}
		sort.SliceStable(order, func(i, j int) bool { return count[order[i]] > count[order[j]] })
		cands = order
	}
	var kept []string
	for _, c := range cands {
		c = strings.TrimRight(c, "/")
		if c == "" || c == strings.TrimRight(s.env.Home, "/") {
			continue
		}
		under := false
		for _, k := range kept {
			if c == k || strings.HasPrefix(c, k+"/") {
				under = true
			}
		}
		if !under {
			s.rootIndex[c] = len(kept)
			kept = append(kept, c)
		}
	}
	s.roots = append([]string(nil), kept...)
	sort.SliceStable(s.roots, func(i, j int) bool { return len(s.roots[i]) > len(s.roots[j]) })
}

func fakeRootN(i int) string {
	if i == 0 {
		return fakeRoot
	}
	return fakeRoot + strconv.Itoa(i+1)
}

// slug は Claude Code の記録のディレクトリの名前（英数字以外を - に）。
func slug(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func (s *synth) value(v any) any {
	switch x := v.(type) {
	case *jsonorder.Object:
		o := &jsonorder.Object{Members: make([]jsonorder.Member, 0, len(x.Members))}
		for _, m := range x.Members {
			o.Members = append(o.Members, jsonorder.Member{Key: s.key(m.Key), Value: s.value(m.Value)})
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = s.value(e)
		}
		return a
	case string:
		return s.str(x)
	}
	return v // nil・bool・jsonorder.Number はそのまま
}

// keepKey はキーをそのまま残してよいか（識別子の形で、データから作られた名前らしくないもの）。
func keepKey(k string) bool {
	if !keepKeyRe.MatchString(k) || strings.Contains(k, "__") || digitRunRe.MatchString(k) || hexOnlyRe.MatchString(k) {
		return false
	}
	_, hit := domain.FindSecret(k)
	return !hit
}

// key はキーを写す。識別子の形のキーは残す。ただし、値から作られたキー（同じ綴りの文字列の値があるもの）は、
// 値と同じ合成の値にする（tool_use の id で引く表などで、キーと値の対応が残る）。値から作られたかは、
// 外側の記録では数字を含むもの（id・モデルの名前）だけで判断し（type の値 text とキー text のような構造の語は残す）、
// JSON の文字列の中（ツールの出力など）では数字の有無を問わない（プロジェクトの名前などで引く表がそのまま写らないように）。
func (s *synth) key(k string) string {
	if keptKey(k, s.vals, s.inner > 0, s.mask) {
		s.kept[k] = true
		return k
	}
	s.keys[k] = true
	return s.str(k)
}

// keptKey はキーをそのまま残すか（key の規則）。vals は入力の文字列の値すべて、inner は JSON の文字列の中か、mask は -mask-key。
func keptKey(k string, vals map[string]bool, inner bool, mask map[string]bool) bool {
	return keepKey(k) && !mask[k] && !(vals[k] && (inner || strings.ContainsAny(k, "0123456789")))
}

func (s *synth) str(v string) string {
	if v == "" {
		return ""
	}
	if uuidRe.MatchString(v) {
		n, ok := s.uuids[v]
		if !ok {
			n = len(s.uuids) + 1
			s.uuids[v] = n
		}
		return fmt.Sprintf("%s%012d", fakeUUIDPrefix, n)
	}
	if wholePathRe.MatchString(v) {
		return s.path(v)
	}
	if t := strings.TrimSpace(v); len(t) >= 2 && (t[0] == '{' && t[len(t)-1] == '}' || t[0] == '[' && t[len(t)-1] == ']') {
		if inner, err := jsonorder.Decode([]byte(t)); err == nil {
			var buf bytes.Buffer
			s.inner++
			writeJSON(&buf, s.value(inner))
			s.inner--
			return buf.String()
		}
	}
	n, ok := s.strs[v]
	if !ok {
		n = len(s.strs) + 1
		s.strs[v] = n
	}
	out := fmt.Sprintf("s%06d", n)
	for _, m := range innerPathRe.FindAllStringSubmatch(v, -1) {
		p := strings.TrimRight(m[1], ".,:")
		if len(p) > 1 {
			out += " " + s.path(p)
		}
	}
	return out
}

// path は絶対パスを合成の根の下に写す。
func (s *synth) path(p string) string {
	trail := strings.HasSuffix(p, "/") && len(p) > 1
	p = strings.TrimRight(p, "/")
	under := func(base string) (string, bool) {
		base = strings.TrimRight(base, "/")
		if base == "" {
			return "", false
		}
		if p == base {
			return "", true
		}
		if strings.HasPrefix(p, base+"/") {
			return p[len(base)+1:], true
		}
		return "", false
	}
	var out string
	matched := false
	for _, r := range s.roots {
		if rest, ok := under(r); ok {
			out, matched = fakeRootN(s.rootIndex[r])+s.rel(rest), true
			break
		}
	}
	if !matched {
		if rest, ok := under(s.env.ClaudeDir); ok {
			matched = true
			for _, r := range s.roots {
				dir := "projects/" + slug(r)
				if rest == dir || strings.HasPrefix(rest, dir+"/") {
					out = fakeClaude + "/projects/" + slug(fakeRootN(s.rootIndex[r])) + s.rel(strings.TrimPrefix(rest[len(dir):], "/"))
					break
				}
			}
			if out == "" {
				out = fakeClaude + s.rel(rest)
			}
		}
	}
	if !matched {
		if rest, ok := under(s.env.CodexDir); ok {
			out, matched = fakeCodex+s.rel(rest), true
		}
	}
	if !matched {
		if rest, ok := under(s.env.Home); ok {
			out, matched = fakeHome+s.rel(rest), true
		}
	}
	if !matched {
		out = fakeOther + s.rel(strings.TrimPrefix(p, "/"))
	}
	if trail {
		out += "/"
	}
	return out
}

// rel は / 区切りの相対パスの名前を 1 つずつ置き換える（先頭に / を付けて返す。空なら空）。
func (s *synth) rel(rest string) string {
	var b strings.Builder
	for _, c := range strings.Split(rest, "/") {
		if c == "" {
			continue
		}
		b.WriteByte('/')
		if c == "." || c == ".." {
			b.WriteString(c)
			continue
		}
		hidden := strings.HasPrefix(c, ".")
		ext := ""
		if e := filepath.Ext(strings.TrimPrefix(c, ".")); e != "" {
			for _, k := range knownExts {
				if e == k {
					ext = e
				}
			}
		}
		n, ok := s.comps[c]
		if !ok {
			n = len(s.comps) + 1
			s.comps[c] = n
		}
		if hidden {
			b.WriteByte('.')
		}
		fmt.Fprintf(&b, "c%d%s", n, ext)
	}
	return b.String()
}

// writeJSON は区切りに空白を入れない JSON（Claude Code と Codex の記録と同じ形）を書く。数は字面のまま。
func writeJSON(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case jsonorder.Number:
		b.WriteString(string(x))
	case string:
		writeString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSON(b, e)
		}
		b.WriteByte(']')
	case *jsonorder.Object:
		b.WriteByte('{')
		for i, m := range x.Members {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, m.Key)
			b.WriteByte(':')
			writeJSON(b, m.Value)
		}
		b.WriteByte('}')
	}
}

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x2028 || r == 0x2029:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// Verify は写しに元の値が残っていないことを確かめる。失敗の文面は件数だけで、値は出さない。
func Verify(env Env, inputs []Input, outputs []Output) error {
	mask := maskSet(env)
	vocab := vocabulary()
	var parsed [][]any
	vals := map[string]bool{}
	for _, in := range inputs {
		lines, err := parseLines(in.Data)
		if err != nil {
			return err
		}
		for _, v := range lines {
			collect(v, func(x string) { vals[x] = true }, func(string) {})
		}
		parsed = append(parsed, lines)
	}
	// 元の値: 4 文字以上の文字列の値と、置き換えたキー。残したキー（kept）は key の規則で残した構造の語なので、写しの側の検索に入れない。
	originals := map[string]struct{}{}
	kept := map[string]bool{}
	for _, lines := range parsed {
		for _, v := range lines {
			walk(v, false, func(x string) {
				if utf8.RuneCountInString(x) >= 4 && !strings.Contains(vocab, x) {
					originals[x] = struct{}{}
				}
			}, func(k string, inner bool) {
				if keptKey(k, vals, inner, mask) {
					kept[k] = true
				} else if utf8.RuneCountInString(k) >= 4 && !strings.Contains(vocab, k) {
					originals[k] = struct{}{}
				}
			})
		}
	}
	seen := map[string]struct{}{}
	var corpus strings.Builder
	secrets := 0
	for _, out := range outputs {
		lines, err := parseLines(out.Data)
		if err != nil {
			return fmt.Errorf("%s: 写しが JSON として読めません: %w", out.Name, err)
		}
		add := func(str string) {
			if _, ok := seen[str]; ok {
				return
			}
			seen[str] = struct{}{}
			if _, hit := domain.FindSecret(str); hit {
				secrets++
			}
			corpus.WriteString(str)
			corpus.WriteByte(0)
		}
		for _, v := range lines {
			collect(v, add, func(k string) {
				if !kept[k] {
					add(k)
				}
			})
		}
		add(out.Name)
	}
	text := corpus.String()
	left := 0
	for o := range originals {
		if strings.Contains(text, o) {
			left++
		}
	}
	if left > 0 || secrets > 0 {
		return fmt.Errorf("写しに元の値が残っています（元の値 %d 種・秘密らしい値 %d 種。値は出しません）", left, secrets)
	}
	return nil
}

// collect は文字列の値とキーを集める。値が JSON の文字列なら中身もたどる（中身のキーは onKey へ）。
func collect(v any, onStr, onKey func(string)) {
	walk(v, false, onStr, func(k string, _ bool) { onKey(k) })
}

// walk は collect と同じく値とキーをたどり、キーが JSON の文字列の中にあるか（inner）も渡す。
func walk(v any, inner bool, onStr func(string), onKey func(string, bool)) {
	switch x := v.(type) {
	case *jsonorder.Object:
		for _, m := range x.Members {
			onKey(m.Key, inner)
			walk(m.Value, inner, onStr, onKey)
		}
	case []any:
		for _, e := range x {
			walk(e, inner, onStr, onKey)
		}
	case string:
		if t := strings.TrimSpace(x); len(t) >= 2 && (t[0] == '{' && t[len(t)-1] == '}' || t[0] == '[' && t[len(t)-1] == ']') {
			if v, err := jsonorder.Decode([]byte(t)); err == nil {
				walk(v, true, onStr, onKey)
				return
			}
		}
		onStr(x)
	}
}
