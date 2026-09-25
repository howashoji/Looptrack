package kit

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestNames(t *testing.T) {
	names := Names()
	want := map[string]bool{"kit/core/skills/issue/SKILL.md": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
		if !strings.HasPrefix(n, "kit/core/") && !strings.HasPrefix(n, "kit/loop/") {
			t.Errorf("core / loop の外を配っている: %s", n)
		}
		if b, err := ReadFile(n); err != nil || len(b) == 0 {
			t.Errorf("%s: %v", n, err)
		}
	}
	for n, ok := range want {
		if !ok {
			t.Errorf("%s が一覧に無い: %v", n, names)
		}
	}
	for _, bad := range []string{"kit/core/hooks/stop-verbal-action-mismatch.sh", "kit/README.md", "kit/embed.go", "README.md", "core/skills/issue/SKILL.md", "kit/core/../README.md"} {
		if _, err := ReadFile(bad); err == nil {
			t.Errorf("%s を読めてしまう", bad)
		}
	}
}

// TestNamesSkipsOSJunk: OS の付随ファイル（.DS_Store・._*・Thumbs.db・desktop.ini など）を配らない。
// 「.」「_」で始まるものは go:embed も除くが、Names 自体も除く（fstest で埋め込みを経ずに確かめる）。
func TestNamesSkipsOSJunk(t *testing.T) {
	junk := []byte{0x00, 0x00, 0x00, 0x01, 'B', 'u', 'd', '1', 0xff, 0xfe, 0x80}
	fsys := fstest.MapFS{
		"core/skills/issue/SKILL.md":    {Data: []byte("x")},
		"core/.DS_Store":                {Data: junk},
		"core/skills/._SKILL.md":        {Data: junk},
		"core/skills/Thumbs.db":         {Data: junk},
		"core/skills/issue/desktop.ini": {Data: junk},
		"core/skills/issue/Icon\r":      {Data: junk},
		"core/_hidden/a.md":             {Data: []byte("x")},
		"loop/hooks/ehthumbs.db":        {Data: junk},
		"loop/manifest.json":            {Data: []byte("{}")},
	}
	got := strings.Join(names(fsys), ",")
	if want := "kit/core/skills/issue/SKILL.md,kit/loop/manifest.json"; got != want {
		t.Errorf("一覧: %s（期待 %s）", got, want)
	}
	for _, bad := range []string{"kit/core/Thumbs.db", "kit/core/desktop.ini", "kit/core/.DS_Store"} {
		if _, err := ReadFile(bad); err == nil {
			t.Errorf("%s を読めてしまう", bad)
		}
	}
}

// TestTranslationNames: 訳の名前（<ディレクトリ>/en/<同じ名前>）の出し入れ。
func TestTranslationNames(t *testing.T) {
	const src = "kit/loop/rules/background-process.md"
	tr := TranslatedName(src)
	if want := "kit/loop/rules/en/background-process.md"; tr != want {
		t.Errorf("TranslatedName = %s（期待 %s）", tr, want)
	}
	if IsTranslation(src) || !IsTranslation(tr) {
		t.Errorf("IsTranslation: 正本 %v / 訳 %v", IsTranslation(src), IsTranslation(tr))
	}
	if got := SourceName(tr); got != src {
		t.Errorf("SourceName = %s（期待 %s）", got, src)
	}
	if got := SourceName(src); got != src {
		t.Errorf("SourceName（正本）= %s", got)
	}
}

// untranslatedName は訳（en/）が無い配布物の名前を返す（無ければ t.Fatal）。
// 名前を直書きすると、その文書が訳されたときにテストが「訳の無い場合」を試さなくなるので、実物から選ぶ。
func untranslatedName(t *testing.T) string {
	t.Helper()
	names := map[string]bool{}
	for _, n := range Names() {
		names[n] = true
	}
	for _, n := range Names() {
		if !IsTranslation(n) && !names[TranslatedName(n)] {
			return n
		}
	}
	t.Fatal("訳の無い配布物が 1 つも無い（このテストは fallback を試せない）")
	return ""
}

// TestReadFileLang: en は訳があれば訳、無ければ正本の日本語に戻る。ja は常に正本。
func TestReadFileLang(t *testing.T) {
	const translated = "kit/loop/rules/background-process.md"
	untranslated := untranslatedName(t)
	ja, err := ReadFileLang(translated, "ja")
	if err != nil {
		t.Fatal(err)
	}
	en, err := ReadFileLang(translated, "en")
	if err != nil {
		t.Fatal(err)
	}
	if string(ja) == string(en) {
		t.Error("訳があるのに ja と en で同じ本文が返る")
	}
	if !strings.Contains(string(en), "Key points") {
		t.Errorf("en の本文が訳でない: %.60s", en)
	}
	src, err := ReadFile(untranslated)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := ReadFileLang(untranslated, "en")
	if err != nil || string(fallback) != string(src) {
		t.Errorf("訳の無いファイルが正本に戻らない: %v", err)
	}
}

// TestLoopCountsIgnoresTranslations: 導入の問いの本数は訳を数えない（rules は 5 本のまま）。
func TestLoopCountsIgnoresTranslations(t *testing.T) {
	files := map[string]string{}
	for _, n := range Names() {
		b, err := ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		files[n] = string(b)
	}
	if _, ok := files["kit/loop/rules/en/background-process.md"]; !ok {
		t.Fatal("訳が配布物に入っていない（導入では日英の両方を配る）")
	}
	hooks, rules, skills := LoopCounts(files)
	if hooks != 20 || rules != 5 || skills != 2 {
		t.Errorf("hook %d 本・rules %d 本・skill %d 本（期待 20 / 5 / 2）", hooks, rules, skills)
	}
}
