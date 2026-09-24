// Package transfer は旧形式（Markdown ディレクトリ）と DB の間の取り込み・書き出し・一致確認を行う。
package transfer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/mdformat"
	"github.com/howashoji/looptrack/internal/store"
)

// SourceProject は旧形式の 1 プロジェクト。
type SourceProject struct {
	Project store.Project
	Dir     string
	Files   []SourceFile
}

// SourceFile は open/ または closed/ の 1 ファイル。
type SourceFile struct {
	Rel  string // "open/ABC-0001-xxx.md"
	Name string // ファイル名（バイト列そのまま）
	Raw  []byte
}

type configJSON struct {
	Prefix      string `json:"prefix"`
	Width       int    `json:"width"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Order       *int   `json:"order"`
}

// ReadSource は root 直下のプロジェクト（config.json を持つディレクトリ）を読む。slugs を指定するとそれだけ。
func ReadSource(root string, slugs []string) ([]SourceProject, error) {
	want := map[string]bool{}
	for _, s := range slugs {
		want[s] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []SourceProject
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "scripts" {
			continue
		}
		if len(want) > 0 && !want[name] {
			continue
		}
		dir := filepath.Join(root, name)
		raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			continue
		}
		var cfg configJSON
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, i18n.Wrapf(err, "transfer.err.at", "at", name+"/config.json")
		}
		if cfg.Prefix == "" || cfg.Width <= 0 {
			return nil, fmt.Errorf("%s/config.json に prefix / width がありません", name)
		}
		p := SourceProject{Dir: dir, Project: store.Project{Slug: name, Prefix: cfg.Prefix, Width: cfg.Width, Name: cfg.Name, Description: cfg.Description, SortOrder: 100}}
		if cfg.Order != nil {
			p.Project.SortOrder = *cfg.Order
		}
		if p.Project.Name == "" {
			p.Project.Name = name
		}
		if c, err := os.ReadFile(filepath.Join(dir, "counter")); err == nil {
			if s := strings.TrimSpace(string(c)); s != "" {
				if p.Project.Counter, err = strconv.Atoi(s); err != nil {
					return nil, i18n.Wrapf(err, "transfer.err.at", "at", name+"/counter")
				}
			}
		}
		for _, sub := range []string{"open", "closed"} {
			items, err := os.ReadDir(filepath.Join(dir, sub))
			if err != nil {
				continue
			}
			for _, it := range items {
				if it.IsDir() || !strings.HasSuffix(it.Name(), ".md") {
					continue
				}
				b, err := os.ReadFile(filepath.Join(dir, sub, it.Name()))
				if err != nil {
					return nil, err
				}
				p.Files = append(p.Files, SourceFile{Rel: sub + "/" + it.Name(), Name: it.Name(), Raw: b})
			}
		}
		out = append(out, p)
	}
	if len(want) > 0 && len(out) != len(want) {
		return nil, fmt.Errorf("指定したプロジェクトの一部が見つかりません: %v", slugs)
	}
	return out, nil
}

// ImportResult は取り込み件数。
type ImportResult struct {
	Slug     string
	Issues   int
	Comments int
}

// Import は旧形式のプロジェクトを DB に取り込む（プロジェクト単位のトランザクションで置き換え）。
// API / CLI 等からの変更（via が import 以外のイベント）があるプロジェクトは、運用データを消さないよう拒否する。
func Import(ctx context.Context, db *sql.DB, src []SourceProject) ([]ImportResult, error) {
	var results []ImportResult
	for _, sp := range src {
		r, err := importProject(ctx, db, sp)
		if err != nil {
			return results, i18n.Wrapf(err, "transfer.err.at", "at", sp.Project.Slug)
		}
		results = append(results, r)
	}
	return results, nil
}

func importProject(ctx context.Context, db *sql.DB, sp SourceProject) (ImportResult, error) {
	res := ImportResult{Slug: sp.Project.Slug}
	type parsed struct {
		file SourceFile
		doc  *mdformat.Document
		num  int
	}
	var docs []parsed
	for _, f := range sp.Files {
		doc, err := mdformat.Parse(string(f.Raw))
		if err != nil {
			return res, i18n.Wrapf(err, "transfer.err.at", "at", f.Rel)
		}
		it := domain.FromDocument(doc)
		num, err := store.ParseNumber(sp.Project.Prefix, it.ID)
		if err != nil {
			return res, i18n.Wrapf(err, "transfer.err.at", "at", f.Rel)
		}
		if wantDir := map[bool]string{true: "closed", false: "open"}[it.IsClosed()]; !strings.HasPrefix(f.Rel, wantDir+"/") {
			return res, fmt.Errorf("%s: status %q なのに %s/ にありません（取り込むと書き出し先が変わるため拒否）", f.Rel, it.Status, wantDir)
		}
		docs = append(docs, parsed{f, doc, num})
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback() //nolint:errcheck

	projectID, err := store.UpsertProject(ctx, tx, sp.Project)
	if err != nil {
		return res, err
	}
	var live int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM issue_events WHERE project_id = ? AND via <> 'import'", projectID).Scan(&live); err != nil {
		return res, err
	}
	if live > 0 {
		return res, fmt.Errorf("DB 上で %d 件の変更（import 以外）があるため置き換えを拒否しました（運用中のデータを消さないため）", live)
	}
	relock, err := store.UnlockAppendOnly(ctx, db, tx) // SQLite の追記専用のトリガを、この置き換えの間だけ外す
	if err != nil {
		return res, err
	}
	for _, stmt := range []string{ // MySQL・SQLite の両方で通る形（副問い合わせ）
		"DELETE FROM comments WHERE issue_id IN (SELECT id FROM issues WHERE project_id = ?)",
		"DELETE FROM issue_values WHERE issue_id IN (SELECT id FROM issues WHERE project_id = ?)",
		"DELETE FROM issue_extra WHERE issue_id IN (SELECT id FROM issues WHERE project_id = ?)",
		"DELETE FROM issue_events WHERE project_id = ?",
		"DELETE FROM issues WHERE project_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, stmt, projectID); err != nil {
			return res, err
		}
	}
	if err := relock(); err != nil {
		return res, err
	}
	for _, d := range docs {
		issueID, err := store.InsertDocument(ctx, tx, projectID, d.num, d.file.Name, d.doc, "import")
		if err != nil {
			return res, i18n.Wrapf(err, "transfer.err.at", "at", d.file.Rel)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO issue_events (project_id, issue_id, kind, via, detail) VALUES (?, ?, 'import', 'import', JSON_OBJECT('file', ?))",
			projectID, issueID, d.file.Rel); err != nil {
			return res, err
		}
		res.Issues++
		res.Comments += len(d.doc.Comments)
	}
	return res, tx.Commit()
}

// RenderedFile は DB から再生成した 1 ファイル。
type RenderedFile struct {
	Rel string
	Raw []byte
}

// RenderProject は DB のプロジェクトを旧形式のファイル群に戻す。
func RenderProject(ctx context.Context, db *sql.DB, p store.Project) ([]RenderedFile, error) {
	issues, err := store.LoadDocuments(ctx, db, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]RenderedFile, 0, len(issues))
	for _, s := range issues {
		dir := "open"
		if domain.FromDocument(s.Doc).IsClosed() {
			dir = "closed"
		}
		// 担当者（サーバだけの項目）は show と同じく frontmatter に差し込む。取り込んだままのイシューには無いので往復一致は変わらない
		out = append(out, RenderedFile{Rel: dir + "/" + s.FileName, Raw: []byte(mdformat.Render(domain.WithAssignee(s.Doc, s.Assignee.Login)))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// VerifyReport は一致確認の結果。
type VerifyReport struct {
	Slug     string
	Files    int
	Problems []string
}

// Verify は DB から再生成したファイルが旧形式のファイルとバイト一致するかを確認する。
func Verify(ctx context.Context, db *sql.DB, src []SourceProject) ([]VerifyReport, error) {
	projects, err := store.ListProjects(ctx, db)
	if err != nil {
		return nil, err
	}
	bySlug := map[string]store.Project{}
	for _, p := range projects {
		bySlug[p.Slug] = p
	}
	var reports []VerifyReport
	for _, sp := range src {
		r := VerifyReport{Slug: sp.Project.Slug, Files: len(sp.Files)}
		p, ok := bySlug[sp.Project.Slug]
		if !ok {
			r.Problems = append(r.Problems, "DB にプロジェクトがありません")
			reports = append(reports, r)
			continue
		}
		if p.Prefix != sp.Project.Prefix || p.Width != sp.Project.Width || p.Name != sp.Project.Name ||
			p.Description != sp.Project.Description || p.SortOrder != sp.Project.SortOrder {
			r.Problems = append(r.Problems, fmt.Sprintf("config.json と DB の設定が違います: DB=%+v ファイル=%+v", p, sp.Project))
		}
		if p.Counter != sp.Project.Counter {
			r.Problems = append(r.Problems, fmt.Sprintf("counter が違います: DB=%d ファイル=%d", p.Counter, sp.Project.Counter))
		}
		rendered, err := RenderProject(ctx, db, p)
		if err != nil {
			return nil, i18n.Wrapf(err, "transfer.err.at", "at", p.Slug)
		}
		got := map[string][]byte{}
		for _, f := range rendered {
			got[f.Rel] = f.Raw
		}
		for _, f := range sp.Files {
			g, ok := got[f.Rel]
			switch {
			case !ok:
				r.Problems = append(r.Problems, "DB から再生成されないファイル: "+f.Rel)
			case !bytes.Equal(g, f.Raw):
				e := diffAt(f.Raw, g)
				r.Problems = append(r.Problems, fmt.Sprintf("内容が違います: %s（%d バイト目）", f.Rel, e))
			}
			delete(got, f.Rel)
		}
		extra := make([]string, 0, len(got))
		for rel := range got {
			extra = append(extra, rel)
		}
		sort.Strings(extra)
		for _, rel := range extra {
			r.Problems = append(r.Problems, "ファイルに無いが DB にある: "+rel)
		}
		reports = append(reports, r)
	}
	return reports, nil
}

func diffAt(a, b []byte) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// Export は DB のプロジェクトを out/<slug>/{open,closed}/ と counter に書き出す（移行時の確認・一時出力用）。
func Export(ctx context.Context, db *sql.DB, out string, slugs []string) (int, error) {
	projects, err := store.ListProjects(ctx, db)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, s := range slugs {
		want[s] = true
	}
	n := 0
	for _, p := range projects {
		if len(want) > 0 && !want[p.Slug] {
			continue
		}
		files, err := RenderProject(ctx, db, p)
		if err != nil {
			return n, err
		}
		for _, sub := range []string{"open", "closed"} {
			if err := os.MkdirAll(filepath.Join(out, p.Slug, sub), 0o755); err != nil {
				return n, err
			}
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(out, p.Slug, filepath.FromSlash(f.Rel)), f.Raw, 0o644); err != nil {
				return n, err
			}
			n++
		}
		if err := os.WriteFile(filepath.Join(out, p.Slug, "counter"), []byte(strconv.Itoa(p.Counter)+"\n"), 0o644); err != nil {
			return n, err
		}
	}
	return n, nil
}
