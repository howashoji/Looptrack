// Package kit は各プロジェクトへ配る hook / rules / skill（kit/core・kit/loop）を持つ（DESIGN.md §5-7）。
// サーバ（looptrack serve）は配布用に埋め込み、GET /api/v1/dist と /im/setup/<券>/ の一覧に「kit/core/skills/…」のような
// リポジトリからの相対名で SHA-256 つきで出す。looptrack issue init（--source server）はこの一覧から取る。
//
// go:embed はパッケージのディレクトリより上を指せないため、scripts/embed.go とは別のパッケージにしている。
// パターンは「*」（kit/loop が無い版でもビルドが通り、kit/loop が入ればそのまま載る）。
// 配るのは core/ と loop/ の下だけ（この embed.go と README.md は配らない）。「.」「_」で始まるファイル（.DS_Store・._*）は
// go:embed が除く。「.」で始まらない OS の付随ファイル（Thumbs.db・desktop.ini など）は Names が除く。
//
// 本文の言語: 日本語が正本で、今の場所のまま置く。英語は同じディレクトリの en/ に同じファイル名で置く
// （kit/loop/rules/background-process.md ↔ kit/loop/rules/en/background-process.md）。正本を動かさないのは、
// kit のパスが manifest・hook の配線・internal/client/kitinit から参照されているため（DESIGN.md §9-6）。
// 配るのは日英の両方で、どちらを読むかは hook と skill が実行時に決める（i18n.FromEnv と同じ決め方）。
package kit

import (
	"embed"
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *
var files embed.FS

// Layers は配る層（kit/ の直下のディレクトリ）。
var Layers = []string{"core", "loop"}

// LangDir は正本（日本語）の隣に訳を置くディレクトリの名前。
const LangDir = "en"

// TranslatedName は name（正本）の訳の名前を返す（kit/loop/rules/x.md → kit/loop/rules/en/x.md）。
func TranslatedName(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return LangDir + "/" + name
	}
	return name[:i+1] + LangDir + "/" + name[i+1:]
}

// IsTranslation は name が訳（途中に en/ の段がある）かを返す。
func IsTranslation(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if seg == LangDir {
			return true
		}
	}
	return false
}

// SourceName は訳の名前から正本の名前を返す（訳でなければそのまま）。
func SourceName(name string) string {
	parts := strings.Split(name, "/")
	var out []string
	for _, seg := range parts {
		if seg == LangDir {
			continue
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

// ReadFileLang は name（正本の名前）の本体を lang で返す。lang が "ja" 以外なら訳（en/）を先に探し、
// 訳が無ければ正本に戻す（i18n.T と同じ「日本語が正本、英語がそこに追いつく」）。
func ReadFileLang(name, lang string) ([]byte, error) {
	if lang != "ja" {
		if b, err := ReadFile(TranslatedName(name)); err == nil {
			return b, nil
		}
	}
	return ReadFile(name)
}

// osJunk は OS が作る付随ファイルの名前（小文字）。
var osJunk = map[string]bool{"thumbs.db": true, "ehthumbs.db": true, "ehthumbs_vista.db": true, "desktop.ini": true, "icon\r": true}

// skip は一覧から除く名前（「.」「_」で始まるもの・OS の付随ファイル）。
func skip(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || osJunk[strings.ToLower(name)]
}

// Names は配るファイルの名前（「kit/core/skills/issue/SKILL.md」の形・名前順）。
func Names() []string { return names(files) }

func names(fsys fs.FS) []string {
	var out []string
	for _, layer := range Layers {
		fs.WalkDir(fsys, layer, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // その層が無い（kit/loop の無い版）
			}
			if p != layer && skip(d.Name()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				out = append(out, "kit/"+p)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// ReadFile は Names の名前（kit/ から始まる）の本体を返す。
func ReadFile(name string) ([]byte, error) {
	rest, ok := strings.CutPrefix(name, "kit/")
	if !ok {
		return nil, fs.ErrNotExist
	}
	for _, layer := range Layers {
		if strings.HasPrefix(rest, layer+"/") {
			if skip(rest[strings.LastIndex(rest, "/")+1:]) {
				return nil, fs.ErrNotExist
			}
			return files.ReadFile(rest)
		}
	}
	return nil, fs.ErrNotExist
}

// LoopCounts は kit/loop の問いの文面の本数（hook・rules・skill）。files は「kit/loop/…」の名前 → 本体（無ければ ""）。
// hook は manifest.json の kind "hook" の項目を数える（hook の実体は looptrack の中にあり、kit/loop にファイルが無い）。
// manifest を読めなければ kit/loop/hooks/ の下のファイルを数える（以前の bash の hook を配っていた版）。
// rules は kit/loop/rules/ の下のファイル、skill は kit/loop/skills/<名前>/ の名前の数。訳（en/）は数えない。
func LoopCounts(files map[string]string) (hooks, rules, skills int) {
	seen := map[string]bool{}
	fileHooks := 0
	for n := range files {
		if IsTranslation(n) {
			continue // 訳（en/）は正本と同じ 1 本なので数えない
		}
		rest := strings.TrimPrefix(n, "kit/loop/")
		switch {
		case strings.HasPrefix(rest, "hooks/"):
			fileHooks++
		case strings.HasPrefix(rest, "rules/"):
			rules++
		case strings.HasPrefix(rest, "skills/") && strings.Count(rest, "/") >= 2:
			seen[strings.Split(rest, "/")[1]] = true
		}
	}
	hooks = fileHooks
	var m struct {
		Entries []struct {
			Kind  string `json:"kind"`
			Event string `json:"event"`
		} `json:"entries"`
	}
	if body, ok := files["kit/loop/manifest.json"]; ok && json.Unmarshal([]byte(body), &m) == nil && len(m.Entries) > 0 {
		hooks = 0
		for _, e := range m.Entries {
			if e.Kind == "hook" || (e.Kind == "" && e.Event != "") {
				hooks++
			}
		}
	}
	return hooks, rules, len(seen)
}
