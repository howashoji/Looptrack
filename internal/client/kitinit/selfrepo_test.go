package kitinit

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// listTree は ws の下のパスを全部並べる（ディレクトリも含める。空の .claude/ ができただけでも差に出す）。
func listTree(t *testing.T, ws string) []string {
	t.Helper()
	var out []string
	if err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == ws {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestInitSelfRepoWritesNothing は、導入先が looptrack 自身のリポジトリ（kit の正本・cli.IsSelfRepo）のとき、init が
// 何も書かずに成功（終了コード 0）で終わり、正本では init が要らない旨を出すことを確かめる。--dry-run・--loop・--remove-loop でも同じ。
// 失敗にしないのは、サーバが正本と判定できずに取得 + init の 1 つのコマンドを返したとき、取得は済んだのにコマンド全体が
// 失敗で終わらないようにするため。対照として、正本でない一時ディレクトリでは同じ init が書き込むことを同じテストで確かめる。
func TestInitSelfRepoWritesNothing(t *testing.T) {
	msgJA := func(ws string) string { return i18n.T(i18n.JA, "kitinit.init.self_repo", "dir", ws) }
	if k := "kitinit.init.self_repo"; i18n.T(i18n.JA, k, "dir", "x") == k || i18n.T(i18n.EN, k, "dir", "x") == i18n.T(i18n.JA, k, "dir", "x") {
		t.Fatalf("前提が崩れています: %s が ja.json / en.json に無いか、日英が同じ文面", k)
	}

	install := []string{"--project", "demo", "--agent", "claude-code", "--no-loop"}

	// 対照: 正本でない一時ディレクトリでは、同じ init が書き込む（書き込みを検知できる土台）
	t.Run("control", func(t *testing.T) {
		stubs(t, true)
		root := newRoot(t)
		ws := filepath.Join(root, "ws")
		before := listTree(t, ws)
		r := runInit(t, root, "", install...)
		if r.code != 0 {
			t.Fatalf("前提が崩れています: 正本でない導入先の init が失敗した: %d\n%s\n%s", r.code, r.stdout, r.stderr)
		}
		after := listTree(t, ws)
		if slices.Equal(before, after) || !slices.Contains(after, ".claude/") {
			t.Fatalf("前提が崩れています: 正本でない導入先で init が何も書かなかった（書き込みを検知できない）: %v", after)
		}
		if strings.Contains(r.stdout+r.stderr, msgJA(ws)) {
			t.Errorf("正本でない導入先で正本の案内が出た:\n%s", r.stdout)
		}
	})

	for _, c := range []struct {
		name string
		args []string
	}{
		{"install", install},
		{"loop", []string{"--project", "demo", "--agent", "claude-code", "--loop"}},
		{"dry-run", append(slices.Clone(install), "--dry-run")},
		{"remove-loop", []string{"--remove-loop"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubs(t, true)
			root := newRoot(t)
			ws := filepath.Join(root, "ws")
			makeSelfRepo(t, ws)
			before := listTree(t, ws)
			snapBefore := snapshot(t, ws)
			r := runInit(t, root, "", c.args...)
			if r.code != 0 {
				t.Fatalf("正本で init %v の終了コードが %d（成功で終わるはず）:\n%s\n%s", c.args, r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout, msgJA(ws)) {
				t.Errorf("正本の案内（%q）が標準出力に無い:\n%s", msgJA(ws), r.stdout)
			}
			if r.stderr != "" {
				t.Errorf("標準エラーに出力がある:\n%s", r.stderr)
			}
			if after := listTree(t, ws); !slices.Equal(before, after) {
				t.Errorf("正本で init %v が書き込んだ:\n前: %v\n後: %v", c.args, before, after)
			}
			if snapAfter := snapshot(t, ws); !slices.Equal(snapBefore, snapAfter) {
				t.Errorf("正本で init %v がファイルの中身を変えた", c.args)
			}
		})
	}
}
