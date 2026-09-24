package privfile

import (
	"os"
	"path/filepath"
	"testing"
)

// 書いたファイルが本人だけで、一時ファイルに書いて rename した後も保護が残る（unix・Windows とも）。
func TestWriteFileAndRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := WriteFile(p, []byte("LOOPTRACK_SECRET_KEY='x'\n")); err != nil {
		t.Fatal(err)
	}
	if err := Check(p); err != nil {
		t.Errorf("WriteFile のファイル: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "LOOPTRACK_SECRET_KEY='x'\n" {
		t.Errorf("中身 = %q", b)
	}
	// 置き換えても保護は残る
	if err := WriteFile(p, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if err := Check(p); err != nil {
		t.Errorf("置き換えたファイル: %v", err)
	}
	// CreateTemp で書いて別の名前に rename（setup の流れ）
	f, err := CreateTemp(dir, ".key.setup-*")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("secret")
	f.Close()
	dst := filepath.Join(dir, "im.db.secret-key")
	if err := os.Rename(f.Name(), dst); err != nil {
		t.Fatal(err)
	}
	if err := Check(dst); err != nil {
		t.Errorf("rename の後: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Errorf("一時ファイルが残った: %v", ents)
	}
	if err := Check(filepath.Join(dir, "none")); !os.IsNotExist(err) {
		t.Errorf("無いファイル: %v", err)
	}
}

// CreateEmpty は本人だけの空のファイルを作り、既にあれば中身も権限も変えない。
func TestCreateEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "im.db")
	created, err := CreateEmpty(p)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if err := Check(p); err != nil {
		t.Errorf("CreateEmpty のファイル: %v", err)
	}
	if st, _ := os.Stat(p); st.Size() != 0 {
		t.Errorf("大きさ = %d, want 0", st.Size())
	}
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err = CreateEmpty(p)
	if err != nil || created {
		t.Errorf("既にあるとき created=%v err=%v, want false・nil", created, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "data" {
		t.Errorf("既にあるファイルの中身が変わった: %q", b)
	}
	if _, err := CreateEmpty(filepath.Join(t.TempDir(), "no", "im.db")); err == nil {
		t.Error("ディレクトリが無いのに作れた")
	}
}
