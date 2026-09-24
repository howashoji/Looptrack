package store

import "testing"

func TestValidateNewProject(t *testing.T) {
	ok := Project{Slug: "my-proj2", Prefix: "APP-DEV", Width: 4, Name: "表示名"}
	if err := ValidateNewProject(ok); err != nil {
		t.Fatalf("正しい入力を拒否した: %v", err)
	}
	for name, mod := range map[string]func(*Project){
		"slug 大文字":      func(p *Project) { p.Slug = "MyProj" },
		"slug 先頭ハイフン":   func(p *Project) { p.Slug = "-x" },
		"slug ドット":      func(p *Project) { p.Slug = ".hidden" },
		"slug 空":        func(p *Project) { p.Slug = "" },
		"prefix 小文字":    func(p *Project) { p.Prefix = "myp" },
		"prefix 末尾ハイフン": func(p *Project) { p.Prefix = "MYP-" },
		"prefix 数字始まり":  func(p *Project) { p.Prefix = "1AB" },
		"width 0":       func(p *Project) { p.Width = 0 },
		"width 10":      func(p *Project) { p.Width = 10 },
		"表示名なし":         func(p *Project) { p.Name = " " },
	} {
		p := ok
		mod(&p)
		if err := ValidateNewProject(p); err == nil {
			t.Errorf("%s: 拒否されなかった", name)
		}
	}
}
