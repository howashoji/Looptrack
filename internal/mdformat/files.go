package mdformat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CollectIssueFiles は root 直下のプロジェクト（config.json を持つディレクトリ）の
// open/*.md と closed/*.md を列挙する。除外規則は以前のビューア（1.0.0 より前）と同じ
// （"." "_" で始まる名前と scripts）。
func CollectIssueFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "scripts" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, name, "config.json")); err != nil {
			continue
		}
		for _, sub := range []string{"open", "closed"} {
			matches, err := filepath.Glob(filepath.Join(root, name, sub, "*.md"))
			if err != nil {
				return nil, err
			}
			out = append(out, matches...)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Stats は往復検査の集計（実データ調査の数値と突き合わせるため）。
type Stats struct {
	Files, Comments, EmptyComments, Preambles, NoCommentSection int
	GapNL, TrailNL                                              map[int]int
	Keys                                                        map[string]int
}

// Add は 1 ファイル分を集計する。
func (s *Stats) Add(d *Document) {
	if s.GapNL == nil {
		s.GapNL, s.TrailNL, s.Keys = map[int]int{}, map[int]int{}, map[string]int{}
	}
	s.Files++
	s.Comments += len(d.Comments)
	for _, c := range d.Comments {
		if c.Content == "" {
			s.EmptyComments++
		}
	}
	if d.Preamble != nil {
		s.Preambles++
	}
	if !d.HasCommentSection {
		s.NoCommentSection++
	}
	s.GapNL[d.GapNL]++
	s.TrailNL[d.TrailNL]++
	for _, f := range d.Front {
		s.Keys[f.Key]++
	}
}

func (s Stats) String() string {
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	return fmt.Sprintf("comments=%d empty_comments=%d preambles=%d no_comment_section=%d gap_nl=%s trail_nl=%s keys=%s",
		s.Comments, s.EmptyComments, s.Preambles, s.NoCommentSection, j(s.GapNL), j(s.TrailNL), j(s.Keys))
}
