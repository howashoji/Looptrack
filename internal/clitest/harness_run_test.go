package clitest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/howashoji/looptrack/internal/privfile"
)

// Token は既定で LOOPTRACK_TOKEN に入れる偽のアクセストークン。
const Token = "imp_golden_token"

// Project は既定の LOOPTRACK_PROJECT。
const Project = "demo"

// APIPlaceholder は Seed の中身で偽 API の URL（LOOPTRACK_API_URL の値）に置き換える印（資格情報のキーなど）。
const APIPlaceholder = "{{API}}"

// Seed は実行前に置くファイル。Path は ws/…（作業ディレクトリ）か home/…（HOME）で始める。
type Seed struct {
	Path    string
	Content string
	Mode    os.FileMode // 既定 0644
}

// Case は 1 回の CLI の実行。
type Case struct {
	Name    string            // golden のファイル名（testdata/golden/<Name>.golden）。/ で階層を分ける
	Args    []string          // issue の後ろの引数
	Env     map[string]string // 既定の環境変数への上書き。値が "" なら消す
	Stdin   string
	Routes  []Route
	Seeds   []Seed
	Browser bool // BROWSER にブラウザの代わり（authorize を開いて戻り先へ送る）を入れる
	// PosixPerm は POSIX のファイルの権限（chmod）を前提にするケース。Windows には無い（ACL で守る）ので省略する。
	PosixPerm bool
}

// Impl は CLI の実装（実行ファイルと、引数の前に付けるもの）。
type Impl struct {
	Name string   // go
	Argv []string // ["/path/to/looptrack", "issue"]
}

// GoImpl は Go 版（looptrack issue …）。
func GoImpl(bin string) Impl { return Impl{Name: "go", Argv: []string{bin, "issue"}} }

// File は実行後に ws・home に残っていたファイル。
type File struct {
	Path    string // ws/… か home/…
	Mode    os.FileMode
	Content []byte
	Link    string // symlink ならリンク先（辿らない）
}

// Result は 1 回の実行の結果（正規化の前）。
type Result struct {
	Case     Case
	Code     int
	Stdout   string
	Stderr   string
	Requests []Request
	Files    []File
	Norm     *Normalizer
}

// ChildEnv は子プロセスの環境変数を組み立てる。利用者の環境からは PATH（と Windows の起動に要るもの）だけを持ち込み、
// IM_*・CLAUDE_*・CODEX_*・COPILOT_*・HOME・XDG_CONFIG_HOME などは持ち込まない。overrides の値が "" なら消す。
func ChildEnv(apiURL, home, tmp string, overrides map[string]string) []string {
	env := map[string]string{
		"HOME":            home,
		"USERPROFILE":     home, // Windows の ~ は USERPROFILE
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"APPDATA":         filepath.Join(home, ".config"), // Windows の資格情報の置き場（%APPDATA%\looptrack）を XDG と同じ場所に向ける
		"TMPDIR":          tmp,
		"TZ":              "JST-9",

		"LOOPTRACK_API_URL": apiURL,
		"LOOPTRACK_PROJECT": Project,
		"LOOPTRACK_TOKEN":   Token,
		"LOOPTRACK_TIMEOUT": "10",
		// 記録（golden）は日本語の出力なので言語を固定する。指定が無ければ端末の
		// 設定に従い、日本語でなければ英語が出る（i18n.FromEnv）ため、ここで決めておかないと
		// 実行する機械の LANG で記録と食い違う。
		"LOOPTRACK_LANG": "ja",
	}
	for _, k := range []string{"PATH", "SYSTEMROOT", "COMSPEC", "PATHEXT"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for k, v := range overrides {
		if v == "" {
			delete(env, k)
		} else {
			env[k] = v
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// CheckEnv は子プロセスの環境が安全かを確かめる（本番に書き込まない・利用者の資格情報を読まない）。
//   - LOOPTRACK_API_URL は無いか、偽 API の URL だけ
//   - HOME・USERPROFILE・XDG_CONFIG_HOME・APPDATA・TMPDIR は一時ディレクトリの中
//   - IM_*・LOOPTRACK_*・CLAUDE_*・CODEX_*・COPILOT_* は既定（LOOPTRACK_API_URL・LOOPTRACK_PROJECT・LOOPTRACK_TOKEN・LOOPTRACK_TIMEOUT・LOOPTRACK_LANG）とケースが明示したものだけ
func CheckEnv(env []string, apiURL, root string, explicit map[string]string) error {
	// LOOPTRACK_LANG は記録（golden）の言語を固定するために渡す（本番へは書き込まないので安全）
	allowed := map[string]bool{"LOOPTRACK_API_URL": true, "LOOPTRACK_PROJECT": true, "LOOPTRACK_TOKEN": true, "LOOPTRACK_TIMEOUT": true, "LOOPTRACK_LANG": true}
	for k := range explicit {
		allowed[k] = true
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "LOOPTRACK_API_URL":
			if v != apiURL {
				return fmt.Errorf("LOOPTRACK_API_URL が偽 API（%s）以外を指しています: %s", apiURL, v)
			}
		case "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "TMPDIR":
			if rel, err := filepath.Rel(root, v); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("%s が一時ディレクトリ（%s）の外です: %s", k, root, v)
			}
		}
		for _, p := range []string{"IM_", "LOOPTRACK_", "CLAUDE_", "CLAUDECODE", "CODEX_", "COPILOT_"} {
			if strings.HasPrefix(k, p) && !allowed[k] {
				return fmt.Errorf("環境変数 %s を子プロセスに渡しています", k)
			}
		}
	}
	return nil
}

// Run はケースを 1 回実行する。偽 API・一時ディレクトリはこの中で作って片付ける。
func Run(t testing.TB, impl Impl, c Case) Result {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	ws, home, tmp, bin := filepath.Join(root, "ws"), filepath.Join(root, "home"), filepath.Join(root, "tmp"), filepath.Join(root, "bin")
	for _, d := range []string{ws, home, tmp, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	api := NewFakeAPI(c.Routes)
	defer api.Close()
	for _, s := range c.Seeds {
		p := filepath.Join(root, filepath.FromSlash(s.Path))
		if !strings.HasPrefix(s.Path, "ws/") && !strings.HasPrefix(s.Path, "home/") {
			t.Fatalf("Seed のパスは ws/ か home/ で始めてください: %s", s.Path)
		}
		mode := s.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		content := strings.ReplaceAll(s.Content, APIPlaceholder, api.URL())
		if mode&0o077 == 0 {
			// 本人だけのファイル（資格情報など）は、CLI が書くときと同じく privfile で置く。POSIX は 0600、Windows は本人だけの ACL。
			// Windows の os.WriteFile は ACL を親から継承するので、CLI の読み取りの検査（privfile.Check）に「広すぎる」と止められる
			if err := privfile.WriteFile(p, []byte(content)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, mode); err != nil { // 0400 などの指定も POSIX では保つ
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // umask に左右されない
			t.Fatal(err)
		}
	}

	overrides := map[string]string{}
	for k, v := range c.Env {
		overrides[k] = strings.ReplaceAll(v, APIPlaceholder, api.URL())
	}
	if c.Browser {
		helper, err := browserHelper(bin)
		if err != nil {
			t.Fatal(err)
		}
		overrides["BROWSER"] = helper
	}
	// 試す実行ファイルの置き場を PATH の先頭に置く（ケースが PATH を決めていないとき）。init の verify と配線は PATH の
	// looptrack を探すので、置かないと手元の ~/.local/bin の版や有無で出力が変わる。
	if _, ok := overrides["PATH"]; !ok && len(impl.Argv) > 0 && filepath.IsAbs(impl.Argv[0]) {
		overrides["PATH"] = filepath.Dir(impl.Argv[0]) + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	env := ChildEnv(api.URL(), home, tmp, overrides)
	if err := CheckEnv(env, api.URL(), root, c.Env); err != nil {
		t.Fatalf("子プロセスの環境が安全ではありません: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	argv := append([]string(nil), impl.Argv...)
	for _, a := range c.Args {
		argv = append(argv, strings.ReplaceAll(a, APIPlaceholder, api.URL()))
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = ws, env
	cmd.Stdin = strings.NewReader(c.Stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	end := time.Now()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%s を起動できません: %v", impl.Name, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("%s が時間内に終わりませんでした", impl.Name)
	}
	host, _ := os.Hostname()
	norm := &Normalizer{Paths: []string{root, rawTemp(root)}, Repo: repoRoot(), APIBase: api.URL(), Hostname: host, Start: start, End: end}
	return Result{Case: c, Code: code, Stdout: stdout.String(), Stderr: stderr.String(), Requests: api.Requests(),
		Files: snapshot(t, root, "ws", "home"), Norm: norm}
}

// repoRoot はこのリポジトリのルート（internal/clitest の 2 つ上。出力に出るリポジトリのパスを正規化するために使う）。
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if r, err := filepath.EvalSymlinks(root); err == nil {
		return r
	}
	return root
}

// rawTemp は macOS の /private/var/… に対する /var/… のような、symlink を解く前のパス（無ければ空）。
func rawTemp(resolved string) string {
	if p, ok := strings.CutPrefix(resolved, "/private"); ok && runtime.GOOS == "darwin" {
		return p
	}
	return ""
}

// snapshot は root の下の dirs にあるファイルを集める（パス順）。*.lock（資格情報の排他の印）は実装ごとの事情なので除く。
func snapshot(t testing.TB, root string, dirs ...string) []File {
	t.Helper()
	var out []File
	for _, d := range dirs {
		base := filepath.Join(root, d)
		err := filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() || strings.HasSuffix(p, ".lock") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if e.Type()&fs.ModeSymlink != 0 {
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				out = append(out, File{Path: filepath.ToSlash(rel), Link: filepath.ToSlash(target)})
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out = append(out, File{Path: filepath.ToSlash(rel), Mode: info.Mode().Perm(), Content: b})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// recordedHeaders は golden に残す要求ヘッダ（User-Agent・Accept・Content-Length などは実装の都合なので残さない）。
func recordedHeader(name string) bool {
	switch name {
	case "Authorization", "Content-Type", "If-Match":
		return true
	}
	return strings.HasPrefix(name, "X-Looptrack-")
}

// Golden は結果を正規化した golden のテキストにする。
func (r Result) Golden() string {
	n := r.Norm
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", r.Case.Name)
	fmt.Fprintf(&b, "$ issue %s\n", strings.ReplaceAll(n.Text(shellJoin(r.Case.Args)), APIPlaceholder, "$API"))
	keys := make([]string, 0, len(r.Case.Env))
	for k := range r.Case.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "env %s=%s\n", k, strings.ReplaceAll(n.Text(r.Case.Env[k]), APIPlaceholder, "$API"))
	}
	if r.Case.Stdin != "" {
		fmt.Fprintf(&b, "stdin %q\n", r.Case.Stdin)
	}
	fmt.Fprintf(&b, "\n#### exit %d\n", r.Code)
	section(&b, "stdout", n.Text(r.Stdout))
	section(&b, "stderr", n.Text(r.Stderr))
	fmt.Fprintf(&b, "\n#### requests (%d)\n", len(r.Requests))
	for _, q := range r.Requests {
		line := q.Method + " " + n.Text(q.Path)
		if qs := n.Query(q.Query); qs != "" {
			line += "?" + qs
		}
		if !q.Matched {
			line += "  [フィクスチャなし → 404]"
		}
		b.WriteString(line + "\n")
		names := make([]string, 0, len(q.Header))
		for k := range q.Header {
			if recordedHeader(k) {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		for _, k := range names {
			for _, v := range q.Header[k] {
				fmt.Fprintf(&b, "  %s: %s\n", k, n.Text(v))
			}
		}
		if len(q.Body) > 0 {
			b.WriteString("  body:\n")
			b.WriteString(indent(r.body(q), "    "))
		}
	}
	fmt.Fprintf(&b, "\n#### files (%d)\n", len(r.Files))
	for _, f := range r.Files {
		if f.Link != "" {
			fmt.Fprintf(&b, "%s -> %s\n", n.Text(f.Path), n.Text(f.Link))
			continue
		}
		fmt.Fprintf(&b, "%s (%04o)\n", n.Text(f.Path), f.Mode)
		b.WriteString(indent(r.fileText(f), "    "))
	}
	return b.String()
}

func (r Result) body(q Request) string {
	ct := q.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		if s, ok := r.Norm.JSON(q.Body); ok {
			return s
		}
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		return r.Norm.Query(string(q.Body)) + "\n"
	}
	return r.Norm.Text(string(q.Body)) + "\n"
}

func (r Result) fileText(f File) string {
	if !utf8.Valid(f.Content) || bytes.IndexByte(f.Content, 0) >= 0 {
		sum := sha256.Sum256(f.Content)
		return fmt.Sprintf("(バイナリ %d バイト sha256=%s)\n", len(f.Content), hex.EncodeToString(sum[:]))
	}
	if strings.HasSuffix(f.Path, ".json") {
		if s, ok := r.Norm.JSON(f.Content); ok {
			return s
		}
	}
	s := r.Norm.Text(string(f.Content))
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n(末尾に改行なし)\n"
	}
	return s
}

func section(b *strings.Builder, name, s string) {
	fmt.Fprintf(b, "\n#### %s\n", name)
	b.WriteString(s)
	if s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteString("\n(末尾に改行なし)\n")
	}
}

func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.SplitAfter(s, "\n")
	var b strings.Builder
	for _, l := range lines {
		if l == "" {
			continue
		}
		if l == "\n" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix + l)
	}
	return b.String()
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`|&;<>()*?[]#~") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(b.String(), "\n")
}
