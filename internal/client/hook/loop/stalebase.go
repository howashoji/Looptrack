package loop

// user-prompt-stale-base: **いま居る作業ツリー**の枝分かれの基点の古さを毎ターン測り、閾値を超えている間だけ 1 行知らせる。
//
// なぜ毎ターンか: 「古い基点のまま判断を続ける」失敗は走り出しには起きない。SessionStart の 1 回だけでは、
// 古くなった頃には誰も見ていない（走り出しの基点はいつでも新しい）。逆に `git merge` / `git push` の直前で見るのは遅い
// （その時点では、古い前提での判断はもう終わっている）。だから利用者の入力ごと（UserPromptSubmit）に測る。
// 閾値を超えている間は**毎ターン出す**（間引きの印を置かない。見逃しにくいことを優先する）。
//
// 判定は**基点の経過時間だけ**で、遅れのコミット数は使わない（数は文面に添えるだけ）。
// コミット数はプロジェクトの速度で意味が変わる（半日で 200 コミット進むリポジトリもあれば、200 コミットが数か月分の
// リポジトリもある）。kit は全プロジェクトに効くので、共通の閾値として移植できるのは時間のほう。
//
// 基準は origin/main（無ければ origin/master。LOOPTRACK_LOOP_STALE_BASE_REF で変える）。
// **この hook は git fetch を打たない。** UserPromptSubmit は利用者の入力ごとに走るので、ネットワークの待ちが
// そのまま応答の遅れになる。
//
// **既知の穴（この選択の代償）**: 誰も fetch していない間は origin/main 自体が古いので、遅れを実際より小さく見積もる。
// つまりこの hook が言えるのは**遅れの下限**だけで、**何も出ないこと＝基点が新しいこと、ではない**。
// 同じことを注入する文面にも書く（読む側が「出ていないから新しい」と受け取らないように）。
//
// git が無い・git のリポジトリでない・基準のブランチが無い・git が失敗するときは、黙って何もしない（fail-open）。

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// DefaultStaleBaseMaxAge は「基点が古い」とみなす経過時間の既定（LOOPTRACK_LOOP_STALE_BASE_MAX_AGE で変える）。
//
// 4 時間。実測（81 本の作業ツリー）では、遅れは 0〜11 コミットが 12 本・69〜153 が 33 本・185〜260 が 32 本で、
// 半日で約 200 コミット進むリポジトリでは 4 時間がおよそ 65 コミットにあたる。
// 「69〜153 遅れ」の群の手前で出て、日常の短い作業（0〜11 遅れ）には出ない値。
const DefaultStaleBaseMaxAge = 4 * time.Hour

// staleBaseRefs は基準にするブランチを探す順（LOOPTRACK_LOOP_STALE_BASE_REF が空のとき）。
var staleBaseRefs = []string{"origin/main", "origin/master"}

// staleBase は、いま居る作業ツリーの基点について測ったもの。
type staleBase struct {
	Name    string        // 作業ツリーを指す名前（ブランチ名。detached ならディレクトリ名）
	Ref     string        // 基準（origin/main など）
	Base    string        // 基準からの分岐点の短い SHA（文面に出すもの）
	Full    string        // 同じ分岐点の完全な SHA（git に渡すもの）
	Subject string        // 分岐点のコミットの見出し（空のこともある）
	Age     time.Duration // 分岐点のコミットの日時からの経過
	Behind  int           // 分岐点から基準が進んだコミット数（-1 = 数えられなかった）
}

// UserPromptStaleBase は、いま居る作業ツリーの基点が閾値より古いときだけ 1 行を文脈に入れる（何も止めない）。
func UserPromptStaleBase(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	switch strings.ToLower(trimSpace(e.env("LOOPTRACK_LOOP_STALE_BASE_NOTICE"))) {
	case "0", "off", "false", "no":
		return hookio.Result{}, nil // 利用者が切った
	}
	limit := DefaultStaleBaseMaxAge
	if v := trimSpace(e.env("LOOPTRACK_LOOP_STALE_BASE_MAX_AGE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			limit = d
		}
	}
	// 「いま居る作業ツリー」を測る（本体の作業ツリーではない）。ev.CWD は hook を呼んだセッションの居場所。
	dir := ev.CWD
	if dir == "" {
		dir = root(ev, e)
	}
	top := gitTop(dir)
	if top == "" {
		return hookio.Result{}, nil // git のリポジトリでない
	}
	b, ok := measureStaleBase(ctx, e, top)
	if !ok {
		return hookio.Result{}, nil
	}
	// 閾値を超えている間だけ出す（ちょうど閾値は出さない）。ここが判定の 1 か所。
	if b.Age <= limit {
		return hookio.Result{}, nil
	}
	b.Behind = staleBaseBehind(ctx, e, top, b.Full, b.Ref) // 数えるのは出すときだけ（毎ターンの git を増やさない）
	return hookio.Result{Context: staleBaseNote(e.lang(), b, limit)}, nil
}

// measureStaleBase は作業ツリー top の基点を測る（基準が無い・git が失敗するときは ok = false）。
func measureStaleBase(ctx context.Context, e *Env, top string) (staleBase, bool) {
	b := staleBase{Behind: -1}
	cands := staleBaseRefs
	if r := trimSpace(e.env("LOOPTRACK_LOOP_STALE_BASE_REF")); r != "" {
		cands = []string{r}
	}
	tip := ""
	for _, c := range cands {
		// 存在の確認と先端の SHA を 1 回で取る（先端は「そもそも遅れているか」を見るために要る）
		if sha, ok := gitLine(ctx, e, top, "rev-parse", "--verify", "--quiet", c+"^{commit}"); ok && sha != "" {
			b.Ref, tip = c, sha
			break
		}
	}
	if b.Ref == "" {
		return b, false // origin を取っていないリポジトリ。黙る
	}
	base, ok := gitLine(ctx, e, top, "merge-base", "HEAD", b.Ref)
	if !ok || base == "" {
		return b, false // 基準と歴史がつながっていない
	}
	if base == tip {
		// 分岐点が基準の先端＝1 コミットも遅れていない。基準そのものが長く動いていないだけなので黙る
		// （基点の古さは「基準がその後どれだけ進んだか」を伴ってはじめて意味を持つ）。
		return b, false
	}
	// 分岐点のコミットの日時（%ct）と見出し（%s）を 1 回で読む。欄はタブ区切り。
	line, ok := gitLine(ctx, e, top, "log", "-1", "--format=%h%x09%ct%x09%s", base)
	if !ok {
		return b, false
	}
	f := tabFields(line, 3)
	sec, err := strconv.ParseInt(trimSpace(f[1]), 10, 64)
	if err != nil {
		return b, false
	}
	b.Full = base
	b.Base, b.Subject = trimSpace(f[0]), trimSpace(f[2])
	if b.Base == "" {
		b.Base = base
	}
	b.Age = e.Now().Sub(time.Unix(sec, 0))
	b.Name = staleBaseName(ctx, e, top)
	return b, true
}

// tabFields は line をタブで欄に分け、足りない後ろの欄を空で埋めて n 個そろえる。
//
// **欄の数で弾かない。** 最後の欄が空になる行では、その空の欄が行末の空白として削られて欄が 1 つ足りなくなる
// （git の書式では %s＝見出しが空のコミットや %(upstream:track)＝リモートと完全に同期しているブランチで実際に起きる）。
// 欄の数で弾く実装は、たまたま最後の欄が空になった 1 件で判定そのものを黙って落とす。
func tabFields(line string, n int) []string {
	f := strings.Split(line, "\t")
	for len(f) < n {
		f = append(f, "")
	}
	return f
}

// staleBaseBehind は分岐点から基準が進んだコミット数（数えられなければ -1）。判定には使わない（文面に添えるだけ）。
func staleBaseBehind(ctx context.Context, e *Env, top, base, ref string) int {
	out, ok := gitLine(ctx, e, top, "rev-list", "--count", base+".."+ref)
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(trimSpace(out))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// staleBaseName は作業ツリーを指す名前（ブランチ名。detached ならディレクトリ名）。
func staleBaseName(ctx context.Context, e *Env, top string) string {
	if n, ok := gitLine(ctx, e, top, "rev-parse", "--abbrev-ref", "HEAD"); ok && n != "" && n != "HEAD" {
		return n
	}
	return filepath.Base(top)
}

// gitLine は git を dir で動かして最初の行を返す（起動できない・終了コードが 0 でなければ ok = false）。
// 末尾の空白は落とす（git の書式の最後の欄が空のときに何が起きるかは tabFields のとおり）。
func gitLine(ctx context.Context, e *Env, dir string, args ...string) (string, bool) {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, code, err := e.run(c, dir, nil, "git", args...)
	if err != nil || code != 0 {
		return "", false
	}
	line := trimSpaceRight(string(out))
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return line, true
}

// staleBaseNote は注入する 1 行（作業ツリー名・基点の SHA・遅れの量と、fetch を打たないことの断り）。
func staleBaseNote(lang i18n.Lang, b staleBase, limit time.Duration) string {
	base := b.Base
	if b.Subject != "" {
		base += i18n.T(lang, "loop.stalebase.subject", "s", b.Subject)
	}
	behind := i18n.T(lang, "loop.stalebase.behind_unknown")
	if b.Behind >= 0 {
		behind = i18n.T(lang, "loop.stalebase.behind", "n", b.Behind)
	}
	return i18n.T(lang, "loop.stalebase.notice",
		"name", b.Name, "ref", b.Ref, "base", base,
		"age", staleAge(lang, b.Age), "limit", staleAge(lang, limit), "behind", behind)
}

// staleAge は「4 時間 10 分」のような長さ（文面に埋める）。
//
// roughAge（1 時間未満を「しばらく」にし、時間を切り捨てる）だけでは、閾値の前後が読めない
// （4 時間 10 分が「4 時間」と出ると、閾値 4 時間を超えているのに超えていないように読める）。
// 分まで出すのは 1 日未満のときだけで、それより長いものは roughAge（「{n} 日」）に任せる。
func staleAge(lang i18n.Lang, d time.Duration) string {
	if d >= 24*time.Hour {
		return roughAge(lang, d)
	}
	if d < 0 {
		d = 0
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return i18n.T(lang, "loop.stalebase.age.minutes", "n", m)
	case m == 0:
		return roughAge(lang, d)
	default:
		return i18n.T(lang, "loop.stalebase.age.hours_minutes", "h", h, "m", m)
	}
}
