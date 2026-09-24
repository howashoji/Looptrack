package cred

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

func TestPathsPerOS(t *testing.T) {
	cases := []struct {
		goos    string
		env     map[string]string
		primary string
	}{
		{"linux", map[string]string{"HOME": "/home/u"}, "/home/u/.config/looptrack/credentials.json"},
		{"linux", map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": "/x"}, "/x/looptrack/credentials.json"},
		{"darwin", map[string]string{"HOME": "/Users/u"}, "/Users/u/.config/looptrack/credentials.json"},
		{"windows", map[string]string{"USERPROFILE": `C:\Users\u`, "APPDATA": `C:\Users\u\AppData\Roaming`},
			`C:\Users\u\AppData\Roaming\looptrack\credentials.json`},
		{"windows", map[string]string{"USERPROFILE": `C:\Users\u`}, // APPDATA が無い
			`C:\Users\u\AppData\Roaming\looptrack\credentials.json`},
	}
	for _, c := range cases {
		if c.goos != "windows" && filepath.Separator != '/' {
			continue
		}
		p, err := pathsFor(env.FromMap(c.env), c.goos)
		if err != nil {
			t.Fatal(err)
		}
		if p.Primary != c.primary {
			t.Errorf("%s %v: %+v", c.goos, c.env, p)
		}
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Paths: Paths{Primary: filepath.Join(t.TempDir(), AppDir, FileName)}}
}

func seed(t *testing.T, s *Store, content string) {
	t.Helper()
	if err := writePrivate(s.Paths.Primary, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

func TestReadNothing(t *testing.T) {
	s := newStore(t)
	e, err := s.Entry("https://a/im")
	if err != nil || len(e.Members) != 0 {
		t.Fatalf("空でない: %v %v", e, err)
	}
	if _, err := os.Stat(s.Paths.Primary); !errors.Is(err, os.ErrNotExist) {
		t.Error("読むだけで置き場を作った")
	}
}

func TestSaveAndRead(t *testing.T) {
	s := newStore(t)
	seed(t, s, `{"https://a/im": {"token": "imp_old", "x-unknown": 1}}`)
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Entry("https://a/im")
	if err != nil || e.String("token") != "imp_old" {
		t.Fatalf("読めない: %v %v", e, err)
	}
	e.Set("token", "imp_new").Set("refresh_token", "imr_3")
	if err := s.SaveEntry("https://a/im", e); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveEntry("https://b/im", jsonorder.NewObject().Set("token", "imp_b")); err != nil {
		t.Fatal(err)
	}
	unlock()
	o, err := readFile(s.Paths.Primary)
	if err != nil {
		t.Fatal(err)
	}
	if a := o.Object("https://a/im"); a.String("token") != "imp_new" || a.String("refresh_token") != "imr_3" {
		t.Errorf("書いていない: %s", jsonorder.Compact(o))
	}
	if !strings.Contains(jsonorder.Compact(o), `"x-unknown": 1`) {
		t.Errorf("知らない項目を落とした: %s", jsonorder.Compact(o))
	}
	if o.Object("https://b/im").String("token") != "imp_b" {
		t.Errorf("新しい URL の項目が無い: %s", jsonorder.Compact(o))
	}
	if err := CheckPrivate(s.Paths.Primary); err != nil {
		t.Errorf("本人だけの形でない: %v", err)
	}
	b, _ := os.ReadFile(s.Paths.Primary)
	if strings.HasSuffix(string(b), "\n") || !strings.Contains(string(b), "\n  \"https://a/im\": {\n    \"token\"") {
		t.Errorf("書き方（indent=2・末尾の改行なし）が違う:\n%s", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Paths.Primary))
	for _, ent := range entries {
		if strings.Contains(ent.Name(), ".tmp") {
			t.Errorf("一時ファイルが残った: %s", ent.Name())
		}
	}
}

func TestBrokenFile(t *testing.T) {
	s := newStore(t)
	seed(t, s, `{"broken`)
	_, err := s.Load()
	var be *BrokenError
	if !errors.As(err, &be) || be.Path != s.Paths.Primary {
		t.Fatalf("壊れた JSON を拒否しない: %v", err)
	}
	if !strings.Contains(err.Error(), "を読めません（JSON の形式が壊れています）。削除して looptrack issue login --browser をやり直してください") {
		t.Errorf("文面が違う: %s", err)
	}
	s2 := newStore(t)
	seed(t, s2, `[1, 2]`)
	if _, err := s2.Load(); !errors.As(err, &be) {
		t.Errorf("オブジェクトでない JSON を拒否しない: %v", err)
	}
}

func TestLockExcludesOthers(t *testing.T) {
	s := newStore(t)
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u, err := s.Lock()
		if err != nil {
			t.Error(err)
		} else {
			u()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("ロック中に別のロックが取れた")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("ロックを放しても取れない")
	}
	if _, err := os.Stat(s.Paths.Primary + ".lock"); err != nil {
		t.Errorf("credentials.json.lock を使っていない: %v", err)
	}
}
