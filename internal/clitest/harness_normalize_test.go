package clitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// JST は子プロセスに渡す TZ（JST-9）と同じ時差。時刻の正規化で、時差の無い時刻をこの時差で読む。
var JST = time.FixedZone("JST", 9*3600)

// Normalizer は実行ごとに変わる値を固定の記号に置き換える。規則の一覧は doc.go と Text・JSON の説明。
type Normalizer struct {
	Paths    []string  // 一時ディレクトリ（EvalSymlinks の前後の両方）→ $TMP
	Repo     string    // このリポジトリのルート（配布物の置き場の親）→ $REPO
	APIBase  string    // 偽 API の URL（http://127.0.0.1:<port>/im）→ $API
	Hostname string    // os.Hostname() → JSON の host・client_name の中で $HOST
	Start    time.Time // 実行の開始。Start〜End（前後 2 分の余裕）の時刻を $NOW にする
	End      time.Time
}

var (
	reLoopback   = regexp.MustCompile(`127\.0\.0\.1(:|%3A)\d+`)
	reState      = regexp.MustCompile(`\b(state=)[A-Za-z0-9_\-]{16,}`)
	reChallenge  = regexp.MustCompile(`\b(code_challenge=)[A-Za-z0-9_\-]{16,}`)
	reVerifier   = regexp.MustCompile(`\b(code_verifier=)[A-Za-z0-9_\-]{16,}`)
	reSeconds    = regexp.MustCompile(`\d+\.\d+ 秒`)
	reDurationMS = regexp.MustCompile(`("duration_ms": )\d+`)
	// 接続できないときの理由（OS・実装で文言が違う。以前の CLI は Remote end closed…・[Errno 61]…、今は EOF・connect: …）
	reConnReason = regexp.MustCompile(`(サーバに接続できません（[^）\n]*）): [^"\n]*`)
	// 2026-09-18 12:34・2026-09-18 12:34:56・2026-09-18T03:34:56.123456Z・…+09:00
	reStamp   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?`)
	reRepoRel = regexp.MustCompile(`(?:\.\.[/\\])*\.\.\$REPO`) // Windows は ..\
	reCompact = regexp.MustCompile(`\b\d{8}-\d{6}\b`)          // 20260918-120000（init の控えのディレクトリ名）
	reZone    = regexp.MustCompile(`[+-]\d{2}:?\d{2}$`)
	reZone4   = regexp.MustCompile(`([+-]\d{2})(\d{2})$`)
)

// Text は出力・ファイルの本文を正規化する。規則（この順）:
//  1. 改行 CRLF → LF
//  2. 一時ディレクトリのパス → $TMP（長いものから）、このリポジトリのパス → $REPO（一時ディレクトリからの相対パス → $REPO_REL）
//  3. 偽 API の URL → $API
//  4. ほかの 127.0.0.1 のポート（login --browser の戻り先）→ 127.0.0.1:$PORT（URL エンコードされた %3A も）
//  5. OAuth の state=・code_challenge=・code_verifier= の値（16 文字以上）→ $STATE・$PKCE_CHALLENGE・$PKCE_VERIFIER
//  6. 経過時間「1.2 秒」→「$SEC 秒」、"duration_ms": 123 → "duration_ms": 0
//  7. 「サーバに接続できません（…）: 理由」の理由 → $REASON
//  8. 実行中（前後 2 分）の時刻 → $NOW（時差の無い時刻は JST として読む。フィクスチャの固定の時刻は変えない）。
//     20260918-120000 の形（init の控えのディレクトリ名）も同じ
func (n *Normalizer) Text(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	paths := append([]string(nil), n.Paths...)
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, p := range paths {
		if p != "" {
			s = strings.ReplaceAll(s, p, "$TMP")
		}
	}
	if n.Repo != "" {
		s = strings.ReplaceAll(s, n.Repo, "$REPO")
		// Windows の相対パスはドライブ名を含まない（..\..\a\repo）。ドライブ名を除いた形も $REPO にする
		// （一時ディレクトリとリポジトリが同じドライブで、共通の親がドライブの根だけのとき。CI の Windows のジョブ）
		// files の symlink の中身は / にそろえてから来る（snapshot）ので / の形も
		if vol := filepath.VolumeName(n.Repo); vol != "" {
			rest := strings.TrimPrefix(n.Repo, vol)
			s = strings.ReplaceAll(s, rest, "$REPO")
			s = strings.ReplaceAll(s, filepath.ToSlash(rest), "$REPO")
		}
		// 一時ディレクトリからこのリポジトリへの相対パス（../ の数は一時ディレクトリの深さで変わる）
		s = reRepoRel.ReplaceAllString(s, "$$REPO_REL")
	}
	if n.APIBase != "" {
		s = strings.ReplaceAll(s, n.APIBase, "$API")
		// URL エンコードされた形（redirect_uri・resource の中）
		s = strings.ReplaceAll(s, url.QueryEscape(n.APIBase), "$API")
	}
	s = reLoopback.ReplaceAllString(s, "127.0.0.1${1}$$PORT")
	s = reState.ReplaceAllString(s, "${1}$$STATE")
	s = reChallenge.ReplaceAllString(s, "${1}$$PKCE_CHALLENGE")
	s = reVerifier.ReplaceAllString(s, "${1}$$PKCE_VERIFIER")
	s = reSeconds.ReplaceAllString(s, "$$SEC 秒")
	s = reDurationMS.ReplaceAllString(s, "${1}0")
	s = reConnReason.ReplaceAllString(s, "${1}: $$REASON")
	s = reStamp.ReplaceAllStringFunc(s, func(m string) string {
		if n.isNow(m) {
			return "$NOW"
		}
		return m
	})
	s = reCompact.ReplaceAllStringFunc(s, func(m string) string {
		if n.isNow(m[0:4] + "-" + m[4:6] + "-" + m[6:8] + " " + m[9:11] + ":" + m[11:13] + ":" + m[13:15]) {
			return "$NOW"
		}
		return m
	})
	return s
}

// isNow は時刻の文字列が実行中（前後 2 分）かを見る。
func (n *Normalizer) isNow(stamp string) bool {
	if n.Start.IsZero() {
		return false
	}
	var t time.Time
	var err error
	s := strings.Replace(stamp, " ", "T", 1)
	switch {
	case strings.HasSuffix(s, "Z") || reZone.MatchString(s):
		t, err = time.Parse(time.RFC3339Nano, fixZone(s))
	default:
		layout := "2006-01-02T15:04"
		if len(s) > len(layout) {
			layout = "2006-01-02T15:04:05"
			if len(s) > len(layout) {
				layout += ".999999999"
			}
		}
		t, err = time.ParseInLocation(layout, s, JST)
	}
	if err != nil {
		return false
	}
	return !t.Before(n.Start.Add(-2*time.Minute)) && !t.After(n.End.Add(2*time.Minute))
}

// fixZone は +0900 を +09:00 にする（RFC 3339 で読めるように）。
func fixZone(s string) string {
	if m := reZone4.FindStringSubmatchIndex(s); m != nil && !strings.Contains(s[m[0]:], ":") {
		return s[:m[3]] + ":" + s[m[4]:]
	}
	return s
}

// JSON は JSON の本文（要求の本文・書いたファイル）をキーの順をそろえて整形し、キーごとの規則を当ててから Text を通す。
// JSON でなければ ok=false。キーごとの規則:
//   - host・client_name: ホスト名 → $HOST（値の中の部分文字列）
//   - state・code_challenge・code_verifier: → $STATE・$PKCE_CHALLENGE・$PKCE_VERIFIER
//   - duration_ms: → 0
//   - expires_at（UNIX 秒）: 実行時刻からの差を 1 分単位に丸めて "$NOW+3600s" の形に
//   - files（導入済み通知の配布物の一覧 {名前: SHA-256}）: 値 → $SHA256（配布物の中身は実装ごとに違う）
//   - client（Go 版の導入済み通知の実行ファイル {version, os, arch}）: → $VERSION・$GOOS・$GOARCH
//     （テストに使う looptrack のビルドの版と、流す OS・CPU で変わる）
func (n *Normalizer) JSON(raw []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if dec.More() {
		return "", false
	}
	v = n.jsonValue("", v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", false
	}
	return n.Text(buf.String()), true
}

func (n *Normalizer) jsonValue(key string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if m, ok := e.(map[string]any); ok && k == "client" && len(m) == 3 && m["version"] != nil && m["os"] != nil && m["arch"] != nil {
				x[k] = map[string]any{"version": "$VERSION", "os": "$GOOS", "arch": "$GOARCH"}
				continue
			}
			if k == "files" {
				if m, ok := e.(map[string]any); ok {
					for name := range m {
						m[name] = "$SHA256"
					}
					continue
				}
			}
			x[k] = n.jsonValue(k, e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = n.jsonValue(key, e)
		}
		return x
	case string:
		return n.keyed(key, x)
	case json.Number:
		switch key {
		case "duration_ms":
			return json.Number("0")
		case "expires_at":
			if f, err := x.Float64(); err == nil && !n.Start.IsZero() {
				d := time.Duration(f*float64(time.Second)) - time.Duration(n.Start.UnixNano())
				return fmt.Sprintf("$NOW+%ds", int64(d.Round(time.Minute)/time.Second))
			}
		}
		return x
	}
	return v
}

// keyed は文字列の値にキーごとの規則を当てる（クエリ・フォームの値にも使う）。
func (n *Normalizer) keyed(key, s string) string {
	switch key {
	case "host", "client_name":
		if n.Hostname != "" {
			return strings.ReplaceAll(s, n.Hostname, "$HOST")
		}
	case "state":
		return "$STATE"
	case "code_challenge":
		return "$PKCE_CHALLENGE"
	case "code_verifier":
		return "$PKCE_VERIFIER"
	}
	return s
}

// Query はクエリ・フォームの文字列をキーの順（同じキーは現れた順）にそろえ、値にキーごとの規則を当てる。
// 読めなければそのまま Text を通す。
func (n *Normalizer) Query(raw string) string {
	if raw == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return n.Text(raw)
	}
	for k, vs := range vals {
		for i, v := range vs {
			vs[i] = n.Text(n.keyed(k, v))
		}
		vals[k] = vs
	}
	// Encode はキーの順に並べる。$ は %24 になるので読みやすく戻す
	return strings.ReplaceAll(vals.Encode(), "%24", "$")
}
