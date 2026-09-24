package kitinit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestFetchKit は --source server / --dist: 配布物の一覧から kit/ だけを取り、SHA-256 を確かめる。
func TestFetchKit(t *testing.T) {
	files := map[string]string{
		"SHA256SUMS":                     "# 取らない\n",
		"kit/core/skills/issue/SKILL.md": "---\nname: issue\ndescription: 偽\n---\n\n# issue\n" + skillMark + "\n",
	}
	for _, c := range []struct {
		name, broken string
		dist, token  bool
		wantErr      string
	}{
		{name: "dist（トークンなし）", dist: true},
		{name: "/dist（トークンあり）", token: true},
		{name: "SHA-256 の不一致", token: true, broken: "kit/core/skills/issue/SKILL.md", wantErr: "kit/core/skills/issue/SKILL.md の SHA-256 が一覧と一致しません"},
		{name: "トークンが無い", wantErr: "サーバから取得するにはトークンが要ります。looptrack issue login"},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubs(t, true)
			var mu sync.Mutex
			var got []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				got = append(got, r.URL.Path+" auth="+fmt.Sprint(r.Header.Get("Authorization") != ""))
				mu.Unlock()
				p := r.URL.Path
				for _, pre := range []string{"/im/api/v1/dist", "/im/setup/tkt"} {
					if p == pre || p == pre+"/" {
						var items []string
						names := make([]string, 0, len(files))
						for n := range files {
							names = append(names, n)
						}
						sort.Strings(names)
						for _, n := range names {
							sum := sha(files[n])
							if n == c.broken {
								sum = sha("別物")
							}
							items = append(items, fmt.Sprintf(`{"name":%q,"sha256":%q}`, n, sum))
						}
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{"files":[%s]}`, strings.Join(items, ","))
						return
					}
					if n, ok := strings.CutPrefix(p, pre+"/"); ok {
						fmt.Fprint(w, files[n])
						return
					}
				}
				http.NotFound(w, r)
			}))
			defer srv.Close()
			root := newRoot(t)
			url := srv.URL + "/im"
			args := []string{"--project", "demo", "--url", url, "--source", "server"}
			if c.dist {
				args = []string{"--project", "demo", "--url", url, "--dist", url + "/setup/tkt"}
			}
			m := map[string]string{}
			if c.token {
				m["LOOPTRACK_TOKEN"] = "imp_test"
			}
			r := runInitEnv(t, root, m, args...)
			if c.wantErr != "" {
				if r.code != 1 || !strings.Contains(r.stderr, c.wantErr) {
					t.Fatalf("%d %q", r.code, r.stderr)
				}
				return
			}
			if r.code != 0 {
				t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout, "kit の取得元: サーバ（"+url+"/api/v1/dist・SHA-256 を確認）") {
				t.Errorf("取得元の表示: %s", r.stdout)
			}
			if body := read(t, filepath.Join(root, "ws", ".claude", "skills", "issue", "SKILL.md")); body != files["kit/core/skills/issue/SKILL.md"] {
				t.Errorf("サーバの kit を置いていない: %q", body)
			}
			for _, g := range got {
				if strings.Contains(g, "SHA256SUMS") {
					t.Errorf("kit 以外の配布物を取った: %v", got)
				}
				if c.dist && strings.HasSuffix(g, "auth=true") {
					t.Errorf("--dist にトークンを送った: %v", got)
				}
			}
		})
	}
}
