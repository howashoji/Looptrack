package loop

// session-start-memories（kit/loop/hooks/session-start-memories.sh の Go 版）。
//
// 記憶のディレクトリ（引数か LOOPTRACK_LOOP_MEMORIES_DIR。既定 <プロジェクト>/.claude/memories）の引き継ぎ（LOOPTRACK_LOOP_HANDOFF_FILE の名前。
// 既定 handoff.md）と最終更新時刻、それ以外の *.md（README.md を除く）の `> 要約:` 行の一覧を文脈に入れる。
// ディレクトリが無ければ何も出さない。
//
// 注入の大きさには上限がある（LOOPTRACK_LOOP_MEMORIES_MAX_CHARS。既定 6000 文字）。
// 上限を超える引き継ぎは**末尾（新しい節）だけ**を入れ、全文の在りかと省いた事実を添える。
// 切り出しは必ず `## ` の見出しから始める（範囲の途中で切れたら 1 つ手前の見出しまで戻り、途中からであることを断る）。
// 節の見出しだけを入れて本文を節の途中から始めると、読み手はどの節を読んでいるのか分からないまま断片を信じてしまう。
// 加えて、末尾の枠とは別に「現在地」の節（末尾から最も近いもの 1 つ）の枠を取る。
// この節は追記で育つ引き継ぎでも先頭付近に置かれがちで、末尾だけを入れると必ず落ちるが、
// 落ちた分を読み手は「無い」ではなく「変わっていない」と読むので、古い値が黙って生き延びる。
// 以前は全文をそのまま入れていたが、追記で育ったファイルでは全セッション・全サブエージェントの冒頭で
// 10 万文字を超える量（425b113 の引き継ぎで実測。ファイル本体が 107,311 文字、注入ブロック全体はそれに
// 見出し・最終更新・ほかの記憶の一覧が付くぶんだけ大きい）を使い、肝心の「いまどこまで進んだか」が読み手に埋もれる。
// 上限を 0 以下にすると今までどおり全文を入れる。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

var summaryLine = compatRe(`^>\s*要約:\s*(.*)$`)

// memoriesMaxChars は注入の上限（文字数）。既定 6000・読めない値も既定・0 以下なら上限なし。
//
// 1500（skill session-handoff が引き継ぎの 1 件に課す上限）では、並行セッションが `handoff append` で
// 節を足す運用の実測に届かず、末尾の 1 節すら途中で切れた。上限は「引き継ぎ 1 件の書き方の上限」ではなく
// 「読み手が判断に使える量」で決める。
const memoriesDefaultMaxChars = 6000

func memoriesMaxChars(e *Env) int {
	v := trimSpace(e.env("LOOPTRACK_LOOP_MEMORIES_MAX_CHARS"))
	if v == "" {
		return memoriesDefaultMaxChars
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return memoriesDefaultMaxChars
	}
	return n
}

func runeLen(s string) int { return len([]rune(s)) }

// minHandoffExcerpt は、上限に収めるときでも引き継ぎの本文に必ず回す文字数の目安。
// これを下回るなら、ほかの記憶の一覧（要約の行）を落としてでも本文に回す（読み手が要るのは本文）。
const minHandoffExcerpt = 400

// fitFromTail は、末尾から行単位で budget 文字に収まる最初の行の番号を返す（1 行も入らなければ len(lines)）。
func fitFromTail(lines []string, budget int) int {
	start, used := len(lines), 0
	for i := len(lines) - 1; i >= 0; i-- {
		n := runeLen(lines[i]) + 1 // 改行の分
		if used+n > budget {
			break
		}
		used += n
		start = i
	}
	return start
}

// sectionEnd は、見出しの行 h から始まる節の終わり（次の `## ` の行。無ければ len(lines)）。
func sectionEnd(lines []string, h int) int {
	for i := h + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			return i
		}
	}
	return len(lines)
}

// tailExcerpt は budget 文字に収まるよう text の**末尾**を行単位で切り出す（新しい節を残す）。
//
// 切り出しは必ず `## ` の見出しから始める。収まる範囲が節の途中から始まるときは、**1 つ手前の見出しまで戻り**、
// 見出しの行と「この節の途中からである」断りを入れてから、その節の末尾を入れる。
// 以前は「収まる範囲に見出しがあればそこまで**進める**」だったので、節が予算より長いと見出しごと落ち、
// 読み手にはどの節の断片なのか分からなかった（節が短い引き継ぎでは違いが出ないので、長い実物でだけ壊れる）。
// budget 以上の余裕があれば text をそのまま返す。
func tailExcerpt(lang i18n.Lang, text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if runeLen(text) <= budget {
		return text
	}
	lines := strings.Split(text, "\n")
	start := fitFromTail(lines, budget)
	if start >= len(lines) { // 1 行も入らない（最終行が極端に長い）ときは文字で切る
		r := []rune(text)
		return string(r[len(r)-budget:])
	}
	h := -1
	for i := start; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "## ") {
			h = i
			break
		}
	}
	if h < 0 { // 切り出しの範囲より前に見出しが無い（見出しの前書きの途中）→ 後ろの見出しから始める
		for i := start; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "## ") {
				return strings.Join(lines[i:], "\n")
			}
		}
		return strings.Join(lines[start:], "\n") // 見出しが 1 つも無い引き継ぎ
	}
	if h == start { // ちょうど見出しから始まる
		return strings.Join(lines[start:], "\n")
	}
	// 節の途中で切れる。見出しの行を必ず入れ、途中からであることを断ってから節の末尾を入れる
	notice := i18n.T(lang, "loop.memories.section_partial", "total", strconv.Itoa(runeLen(strings.Join(lines[h:], "\n"))))
	rest := budget - runeLen(lines[h]) - 1 - runeLen(notice) - 1
	s := fitFromTail(lines, rest)
	if s <= h+1 { // 断りを入れても節が丸ごと入る
		return strings.Join(lines[h:], "\n")
	}
	if s >= len(lines) { // 見出しと断りだけで予算が尽きる
		return clampRunes(lines[h]+"\n"+notice, budget)
	}
	return lines[h] + "\n" + notice + "\n" + strings.Join(lines[s:], "\n")
}

// currentMarkers は「現在地」の節を見分ける語（見出しに部分一致・大小無視）。
// 見出しの綴りそのものなので訳さない（引き継ぎは日本語でも英語でも書かれ、読む側の言語とは無関係）。
var currentMarkers = []string{"現在地", "where things stand", "current"}

// currentSection は、`## ` の見出しに currentMarkers のどれかを含む節のうち**末尾に最も近いもの**の範囲を返す。
//
// 先頭のものを拾ってはいけない。追記で育った引き継ぎには古い「現在地」が先頭付近に残っていることがあり
// （実測: 本体の引き継ぎの 12 行目の「現在地」が 6 コミット前の値を指していた）、
// 先頭を拾う実装は**古い嘘を毎回セッションの冒頭に届ける**。
func currentSection(lines []string) (h, end int, ok bool) {
	h = -1
	for i, l := range lines {
		if !strings.HasPrefix(l, "## ") {
			continue
		}
		low := strings.ToLower(l)
		for _, m := range currentMarkers {
			if strings.Contains(low, m) {
				h = i
				break
			}
		}
	}
	if h < 0 {
		return 0, 0, false
	}
	return h, sectionEnd(lines, h), true
}

// currentBlock は「現在地」の節を budget に収めて返す（収められなければ ""）。
// 節が長すぎるときは**先頭**から切る（末尾の枠と違い、現在地は冒頭に要点が並ぶ）。
func currentBlock(lang i18n.Lang, lines []string, h, end, budget int) string {
	if budget <= 0 {
		return ""
	}
	sec := strings.Join(lines[h:end], "\n")
	if runeLen(sec) <= budget {
		return sec
	}
	notice := i18n.T(lang, "loop.memories.section_partial_head", "total", strconv.Itoa(runeLen(sec)))
	rest := budget - runeLen(lines[h]) - 1 - runeLen(notice) - 1
	if rest <= 0 {
		return ""
	}
	return lines[h] + "\n" + notice + "\n" + clampRunes(strings.Join(lines[h+1:end], "\n"), rest)
}

// containsLine は text の行のどれかが line と一致するか。
func containsLine(text, line string) bool {
	for _, l := range strings.Split(text, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

// clampRunes は s を n 文字までに切る（n 以下ならそのまま）。
func clampRunes(s string, n int) string {
	if n <= 0 || runeLen(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// SessionStartMemories はファイル版の記憶（引き継ぎ）をセッション冒頭の文脈に入れる。
func SessionStartMemories(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	arg := ""
	if len(e.Args) > 0 {
		arg = e.Args[0]
	}
	mem := memoriesDir(ev, e, arg)
	if !isDir(mem) {
		return hookio.Result{}, nil
	}
	hf := e.env("LOOPTRACK_LOOP_HANDOFF_FILE")
	if hf == "" {
		hf = "handoff.md"
	}
	hf = baseName(hf)
	handoff := filepath.Join(mem, hf)
	bar := strings.Repeat("=", 64)
	lang := e.lang()
	head := []string{bar, i18n.T(lang, "loop.memories.header", "path", handoff), bar}
	body, hasBody := "", false
	var after []string // 本文の後ろに置くもの（最終更新。引き継ぎが無いときはその知らせ）
	if st, err := os.Stat(handoff); err == nil && st.Mode().IsRegular() {
		t, _ := readText(handoff)
		body, hasBody = trimSpaceRight(t), true
		after = []string{"", i18n.T(lang, "loop.memories.mtime", "time", st.ModTime().Local().Format("2006-01-02 15:04"))}
	} else {
		after = []string{i18n.T(lang, "loop.memories.missing", "name", hf)}
	}
	var others []string
	for _, p := range globMD(mem) {
		name := filepath.Base(p)
		if name == hf || name == "README.md" {
			continue
		}
		t, err := readText(p)
		if err != nil {
			continue
		}
		summary := ""
		for _, line := range textLines(t) {
			if m := summaryLine.FindStringSubmatch(line); m != nil {
				summary = trimSpace(m[1])
				break
			}
		}
		if summary == "" {
			summary = i18n.T(lang, "loop.memories.no_summary")
		}
		others = append(others, fmt.Sprintf("  - %s — %s", name, summary))
	}
	foot := []string{bar, i18n.T(lang, "loop.memories.footer"), bar}
	// assemble は本文（bodyText）に、他の記憶の一覧（withOthers）と運用の注記（withFoot）を付けて組み立てる。
	// 上限が厳しいときは、この 2 つを落として本文の取り分を作る（読み手が要るのは本文）。
	assemble := func(bodyText string, withOthers, withFoot bool) string {
		out := append([]string{}, head...)
		if hasBody {
			out = append(out, bodyText)
		}
		out = append(out, after...)
		if withOthers && len(others) > 0 {
			out = append(out, "", i18n.T(lang, "loop.memories.others_header"))
			out = append(out, others...)
		}
		if withFoot {
			out = append(out, foot...)
		} else {
			out = append(out, bar)
		}
		return strings.Join(out, "\n")
	}
	limit := memoriesMaxChars(e)
	full := assemble(body, true, true)
	if limit <= 0 || runeLen(full) <= limit || !hasBody {
		return hookio.Result{Context: clampRunes(full, limit)}, nil
	}
	// 上限を超えるときは本文の**末尾（新しい節）**だけにする。それでも入らなければ、他の記憶の一覧を落として本文の取り分を作る。
	// 末尾の枠とは別に「現在地」の節の枠を取る（末尾の切り出しに既に入っているなら取らない。同じ節を 2 回入れない）
	notice := i18n.T(lang, "loop.memories.trimmed", "total", strconv.Itoa(runeLen(body)))
	bodyLines := strings.Split(body, "\n")
	type frame struct{ others, foot bool }
	for i, f := range []frame{{true, true}, {false, true}, {false, false}} {
		budget := limit - runeLen(assemble(notice+"\n", f.others, f.foot))
		if budget < minHandoffExcerpt && i < 2 {
			continue // 一覧や注記を落とせば、本文にもっと回せる
		}
		cur, used := "", 0
		if h, end, ok := currentSection(bodyLines); ok && !containsLine(tailExcerpt(lang, body, budget), bodyLines[h]) {
			// 枠の取り分は全体予算の 1/3 まで。末尾の枠には minHandoffExcerpt を必ず残す
			frameCap := limit / 3
			if budget-frameCap < minHandoffExcerpt {
				frameCap = budget - minHandoffExcerpt
			}
			if cur = currentBlock(lang, bodyLines, h, end, frameCap-2); cur != "" { // 2 は区切りの空行
				used = runeLen(cur) + 2
			}
		}
		out := notice + "\n"
		if cur != "" {
			out += cur + "\n\n"
		}
		out += tailExcerpt(lang, body, budget-used)
		return hookio.Result{Context: clampRunes(assemble(out, f.others, f.foot), limit)}, nil
	}
	return hookio.Result{Context: clampRunes(full, limit)}, nil
}

// baseName はパスの最後の要素（最後の区切りの後ろ。末尾が区切りなら ""）。
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/`+string(filepath.Separator)); i >= 0 {
		return p[i+1:]
	}
	return p
}
