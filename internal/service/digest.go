package service

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
)

// セッション要約（DESIGN.md §9-7）。別の端末のセッションに作業の文脈を渡すコメントで、本文の先頭に
// 「セッション要約:」（英語の別名「Session digest:」）を付ける。規則（見出しの検査・パスの正規化・秘密の検査・本文の上限）は
// このファイルの prepareComment の 1 か所に置き、コメントを作る経路（AddComment と setStatus）はすべてここを通る。
// REST・MCP・CLI・Web のハンドラには書かない（経路によって効いたり効かなかったりするため）。
//
// 先頭語の無いコメント（と、ほかの先頭語のコメント）は本文を変えず、秘密の検査だけを当てる
// （拒むか注記で通すかはプロジェクト別ルール comment.reject_secret。既定は拒む。DESIGN.md §9-1）。

// MaxDigestBytes はセッション要約の本文（正規化した後・保存する形）の上限（バイト）。
const MaxDigestBytes = 16 * 1024

// digestHeaderLines は、見出しの必須の行（branch・head・ai）を探す範囲（先頭からの行数。1 行目の印と端末のラベルを含む）。
// 読み方は domain（CLI の looptrack session digest が欠けた項目を足すときも同じ読み方をする）。
const digestHeaderLines = domain.DigestHeaderLines

// maxDigestLabel は端末のラベルの上限（文字数）。
const maxDigestLabel = 64

// maxDigestBranch はブランチの名前の上限（バイト。git の参照名の実用上の長さ）。
const maxDigestBranch = 255

// パスの手がかりの上限（1 つの欄の件数・1 つの値のバイト数）。要求ごとの正規化の手間を抑える。
const (
	maxPathHintEntries = 32
	maxPathHintBytes   = 4096
)

// DigestAgents は見出しの ai に書ける値（X-Looptrack-Agent と同じ綴りに other を足したもの）。
var DigestAgents = []string{"claude-code", "codex", "copilot", "other"}

var digestSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// PathHints は、セッション要約の本文の中の端末のパスを置き換えるための手がかり（クライアントが要求に添える）。
// サーバは端末のパスを知らないので、何をどう置き換えるかはクライアントが送った値だけで決める（無ければ何も置き換えない）。
//   - Roots: git の作業ツリーのルート（`git rev-parse --show-toplevel` の文字列と realpath。macOS の /private/… の形も）→ 「.」
//   - Home: 利用者のホームディレクトリ → 「~」
//   - MachineLocal: その端末にしか無い置き場（AI の scratchpad・会話記録のディレクトリ）→ 「<machine-local>」
//
// 置き換えは長い値から順に、左から 1 回だけ読む（置き換えた結果をもう一度置き換えない）。
type PathHints struct {
	Roots        []string `json:"roots,omitempty"`
	Home         string   `json:"home,omitempty"`
	MachineLocal []string `json:"machine_local,omitempty"`
}

// 置き換え先の語。
const (
	digestRootRepl  = "."
	digestHomeRepl  = "~"
	digestLocalRepl = "<machine-local>"
)

// commentOpts は prepareComment に渡す、本文以外の材料。
type commentOpts struct {
	// Hints はセッション要約の本文のパスを置き換える手がかり（nil なら何も置き換えない。要約でなければ見ない）
	Hints *PathHints
	// OnStatus は状態の変更と同時のコメントか
	OnStatus bool
	// Rules はプロジェクト別ルール（comment.reject_secret を読む。nil は何も設定していないプロジェクト）
	Rules *domain.Rules
	// Lang は注記（SecretNotice）の言語（要求の言語）
	Lang i18n.Lang
}

// preparedComment は prepareComment の結果。
type preparedComment struct {
	// Text は保存する本文
	Text string
	// Digest はセッション要約か
	Digest bool
	// SecretNotice は、comment.reject_secret を切にしたプロジェクトで、本文が秘密の検査に当たったまま保存するときの注記
	// （行番号と規則の語だけ。値は載せない）。経路（REST・MCP・CLI・Web）はこれをそのまま応答に載せる。対象外なら空
	SecretNotice string
}

// prepareComment は、コメントの本文に経路を問わず当てる規則。保存する本文と、セッション要約か、注記を返す。
// セッション要約（先頭語が digest）のときは、次の順で当てる（どれかで止まれば何も残さない）:
//  1. OnStatus（状態の変更と同時のコメント）なら拒む（要約は comment の経路だけで受け、記録の印を 1 種類に保つ）
//  2. Hints でパスを置き換える（Hints が nil なら何も置き換えない）
//  3. 本文の上限（MaxDigestBytes。置き換えた後の長さ）
//  4. 見出し（1 行目の端末のラベルと、8 行目までの branch・head・ai）
//  5. 秘密の検査（domain.FindSecret。理由には行番号と規則の語だけを載せ、値は載せない）。ルールに関わらず拒む
//
// セッション要約でないコメント（状態の変更と同時のコメントを含む）は、本文を変えず（Hints も見ない）、秘密の検査だけを当てる。
// 当たれば comment.reject_secret（既定は入）で拒み、切なら保存して SecretNotice を返す（checkCommentSecret）。
func prepareComment(id, text string, o commentOpts) (preparedComment, error) {
	if domain.LeadWord(text) != domain.LeadDigest {
		notice, err := checkCommentSecret(id, text, o)
		if err != nil {
			return preparedComment{}, err
		}
		return preparedComment{Text: text, SecretNotice: notice}, nil
	}
	if o.OnStatus {
		return preparedComment{}, errm(Rejected, "digest_on_status", i18n.M("service.err.digest.on_status", "id", id))
	}
	out, err := normalizeDigestPaths(text, o.Hints)
	if err != nil {
		return preparedComment{}, err
	}
	if len(out) > MaxDigestBytes {
		return preparedComment{}, errm(Rejected, "comment_too_long", i18n.M("service.err.digest.too_long", "id", id, "bytes", len(out), "max", MaxDigestBytes))
	}
	if err := checkDigestHeader(id, out); err != nil {
		return preparedComment{}, err
	}
	if f, ok := domain.FindSecret(out); ok {
		return preparedComment{}, errm(Rejected, "comment_secret", i18n.M("service.err.digest.secret", "id", id, "line", f.Line, "label", f.Label))
	}
	return preparedComment{Text: out, Digest: true}, nil
}

// checkCommentSecret は、セッション要約でないコメントの本文に秘密の検査（domain.FindSecret）を当てる。
// 当たらなければ何も返さない。当たれば comment.reject_secret が入（既定）なら 422（comment_secret）、
// 切なら注記の文面（o.Lang の言語）を返す。どちらも行番号と規則の語（SecretFinding.Label）だけを載せ、値は載せない。
func checkCommentSecret(id, text string, o commentOpts) (string, error) {
	f, ok := domain.FindSecret(text)
	if !ok {
		return "", nil
	}
	if o.Rules.RejectCommentSecret() {
		return "", errm(Rejected, "comment_secret", i18n.M("service.err.comment.secret", "id", id, "line", f.Line, "label", f.Label))
	}
	return i18n.T(o.Lang, "service.comment.secret_notice", "id", id, "line", f.Line, "label", f.Label), nil
}

// checkDigestHeader は見出しを検査する。形（DESIGN.md §9-7）:
//
//	セッション要約: <端末のラベル>
//	branch: <ブランチ>
//	head: <HEAD の SHA（16 進 7〜40 桁）>
//	ai: <claude-code | codex | copilot | other>
//
// 1 行目は先頭語と端末のラベル（空でなく 64 文字まで・制御文字なし）。branch・head・ai は 8 行目まで
// （1 行目を含む）のどこにあってもよく、順は問わない。キーは ASCII の大小を問わず、コロンは半角・全角のどちらでもよい。
// 同じキーが 2 回あれば拒む（どちらを読むかで食い違わないため）。ほかの行（見出しの続き・本文）は見ない。
func checkDigestHeader(id, text string) error {
	lines := strings.SplitN(text, "\n", digestHeaderLines+1)
	if len(lines) > digestHeaderLines {
		lines = lines[:digestHeaderLines]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	_, rest := domain.LeadWordRest(lines[0])
	label := strings.TrimSpace(rest)
	if label == "" || utf8.RuneCountInString(label) > maxDigestLabel || hasControl(label) {
		return errm(Rejected, "digest_header", i18n.M("service.err.digest.label", "id", id, "max", maxDigestLabel))
	}
	seen := map[string]string{}
	for _, l := range lines[1:] {
		key, val, ok := digestField(l)
		if !ok {
			continue
		}
		if _, dup := seen[key]; dup {
			return errm(Rejected, "digest_header", i18n.M("service.err.digest.field_duplicate", "id", id, "field", key))
		}
		seen[key] = val
	}
	for _, key := range domain.DigestHeaderKeys {
		val, ok := seen[key]
		if !ok {
			return errm(Rejected, "digest_header", i18n.M("service.err.digest.field_missing", "id", id, "field", key, "lines", digestHeaderLines))
		}
		if !digestValueOK(key, val) {
			return errm(Rejected, "digest_header", i18n.M("service.err.digest.field_invalid", "id", id, "field", key))
		}
	}
	return nil
}

// digestField は見出しの 1 行を「キー: 値」として読む（キーが branch・head・ai のどれかのときだけ ok。domain.DigestHeaderField）。
func digestField(line string) (key, val string, ok bool) { return domain.DigestHeaderField(line) }

func digestValueOK(key, val string) bool {
	switch key {
	case "branch":
		return val != "" && len(val) <= maxDigestBranch && !strings.ContainsAny(val, " \t") && !hasControl(val)
	case "head":
		return digestSHA.MatchString(val)
	case "ai":
		for _, a := range DigestAgents {
			if val == a {
				return true
			}
		}
	}
	return false
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// digestPrefixes は hints を検査し、置き換える値を長い順に並べて返す。
// 同じ値が 2 つの欄にあれば、roots・machine_local・home の順に先のものを採る。
func digestPrefixes(h *PathHints) ([]domain.PathPrefix, error) {
	if h == nil {
		return nil, nil
	}
	var out []domain.PathPrefix
	seen := map[string]bool{}
	add := func(field string, paths []string, repl string) error {
		if len(paths) > maxPathHintEntries {
			return errm(Invalid, "invalid_argument", i18n.M("service.err.digest.path_hints", "field", field, "max", maxPathHintEntries, "bytes", maxPathHintBytes))
		}
		for _, p := range paths {
			c, ok := cleanHintPath(p)
			if !ok {
				return errm(Invalid, "invalid_argument", i18n.M("service.err.digest.path_hints", "field", field, "max", maxPathHintEntries, "bytes", maxPathHintBytes))
			}
			if !seen[c] {
				seen[c] = true
				out = append(out, domain.PathPrefix{Path: c, Repl: repl})
			}
		}
		return nil
	}
	if err := add("roots", h.Roots, digestRootRepl); err != nil {
		return nil, err
	}
	if err := add("machine_local", h.MachineLocal, digestLocalRepl); err != nil {
		return nil, err
	}
	var home []string
	if h.Home != "" {
		home = []string{h.Home}
	}
	if err := add("home", home, digestHomeRepl); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Path) > len(out[j].Path) })
	return out, nil
}

var winDrive = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// cleanHintPath は手がかりの値 1 つを検査し、末尾の区切りを除いた形を返す。
// 絶対パス（/…・C:\…・C:/…・\\…）で、ルートそのもの（/・C:\）でなく、制御文字を含まず、maxPathHintBytes 以内であること
// （ルートや相対パスを許すと、本文のほとんどの語を書き換えてしまう）。
func cleanHintPath(p string) (string, bool) {
	if p == "" || len(p) > maxPathHintBytes || hasControl(p) || !utf8.ValidString(p) {
		return "", false
	}
	if !(strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || winDrive.MatchString(p)) {
		return "", false
	}
	c := strings.TrimRight(p, `/\`)
	if c == "" || (len(c) == 2 && c[1] == ':') {
		return "", false
	}
	return c, true
}

// normalizeDigestPaths は本文の中の hints のパスを置き換える。左から 1 回だけ読み、各位置で長い値から順に試す。
// 境界の規則（値の前後がどういう文字のときに当たるか）は domain.ReplacePathPrefixes にあり、
// クライアントの会話記録の変換と同じ関数を使う。
func normalizeDigestPaths(text string, h *PathHints) (string, error) {
	ps, err := digestPrefixes(h)
	if err != nil || len(ps) == 0 {
		return text, err
	}
	return domain.ReplacePathPrefixes(text, ps), nil
}
