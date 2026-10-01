package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/updatecheck"
)

// 配布物（クライアントに配る looptrack）がサーバの版にそろっていないことの知らせ（起動時のログと管理者の帯）。
// DB を使わない（distLagStatus は設定と配布ディレクトリだけを読む）。

// distAll は 6 対象を同じ版で並べる（writeDist に渡す形）。
func distAll(version string) map[string]string {
	files := map[string]string{}
	for _, t := range distTargetOrder {
		os, arch, _ := strings.Cut(t, "/")
		name := "looptrack_" + version + "_" + os + "_" + arch
		if os == "windows" {
			name += ".exe"
		}
		files[name] = name
	}
	return files
}

func TestDistLagStatus(t *testing.T) {
	logs := &bytes.Buffer{}
	newSrv := func(cfg Config) *Server {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
		s, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 比べないとき: ローカルモード・比べられない版（dev）・版が空
	for _, cfg := range []Config{{Version: "v1.1.0", LocalMode: true}, {Version: "dev"}, {}} {
		if lag := newSrv(cfg).distLagStatus(); lag != nil {
			t.Errorf("%+v: 比べないはず: %+v", cfg, lag)
		}
	}

	// 置き場が無い（LOOPTRACK_DIST_DIR が未設定）: 警告し、.env に足した後は起動し直すまで配らないことも書く
	unset := newSrv(Config{Version: "v1.1.0"})
	if lag := unset.distLagStatus(); lag == nil || !lag.Unset {
		t.Fatalf("置き場なし: %+v", lag)
	}
	logs.Reset()
	unset.LogDistLag(i18n.JA)
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "起動し直すまで配りません") {
		t.Errorf("未設定の起動時のログ: %s", out)
	}
	// LOOPTRACK_DIST_DIR を空の値で明示（配らないと決めた。install.sh も触らない）: 警告も帯も出さず、Info の 1 行だけ。
	// 対照は上の未設定（同じ DistDir == "" で、DistOff だけが違う）
	off := newSrv(Config{Version: "v1.1.0", DistOff: true})
	if lag := off.distLagStatus(); lag != nil {
		t.Errorf("空の明示で知らせた: %+v", lag)
	}
	logs.Reset()
	off.LogDistLag(i18n.JA)
	if out := logs.String(); strings.Contains(out, "level=WARN") || !strings.Contains(out, "level=INFO") ||
		strings.Count(strings.TrimSpace(out), "\n") != 0 || !strings.Contains(out, "配りません") {
		t.Errorf("空の明示の起動時のログ（Info の 1 行のはず）: %s", out)
	}

	// 対照: 6 対象がサーバと同じ版ならそろっている（知らせない）。サーバより新しい版も古さではない
	dir := t.TempDir()
	writeDist(t, dir, distAll("v1.1.0"))
	if lag := newSrv(Config{Version: "v1.1.0", DistDir: dir}).distLagStatus(); lag != nil {
		t.Fatalf("前提が崩れています（そろった配布物で知らせが出た）: %+v", lag)
	}
	if lag := newSrv(Config{Version: "v1.0.0", DistDir: dir}).distLagStatus(); lag != nil {
		t.Errorf("サーバより新しい配布物: %+v", lag)
	}

	// 足りない・古い（linux/amd64 は古い版だけ・windows/arm64 は無い）
	files := distAll("v1.1.0")
	delete(files, "looptrack_v1.1.0_windows_arm64.exe")
	delete(files, "looptrack_v1.1.0_linux_amd64")
	files["looptrack_v1.0.0_linux_amd64"] = "old"
	dir2 := t.TempDir()
	writeDist(t, dir2, files)
	s := newSrv(Config{Version: "v1.1.0", DistDir: dir2})
	lag := s.distLagStatus()
	if lag == nil || lag.Unset || strings.Join(lag.Missing, ",") != "windows/arm64" || strings.Join(lag.Older, ",") != "linux/amd64 v1.0.0" {
		t.Fatalf("足りない・古い: %+v", lag)
	}
	msg := distLagText(i18n.JA, lag, dir2, updatecheck.ServerUpgradeCommand)
	for _, want := range []string{"v1.1.0", "windows/arm64", "linux/amd64 v1.0.0", dir2, updatecheck.ServerUpgradeCommand} {
		if !strings.Contains(msg, want) {
			t.Errorf("文面に %q が無い: %s", want, msg)
		}
	}

	// 起動時のログ: そろっていなければ Warn を 1 行（更新の 1 行つき）。対照: そろっていれば何も出さない
	logs.Reset()
	s.LogDistLag(i18n.JA)
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "windows/arm64") ||
		!strings.Contains(out, "--upgrade") {
		t.Errorf("起動時のログ: %s", out)
	}
	logs.Reset()
	newSrv(Config{Version: "v1.1.0", DistDir: dir}).LogDistLag(i18n.JA)
	if out := logs.String(); out != "" {
		t.Errorf("そろっているのにログが出た: %s", out)
	}

	// 管理者の帯: admin にだけ出す。対照: そろっていれば admin にも出さない
	render := func(s *Server, role string) string {
		var b bytes.Buffer
		data := map[string]any{"User": store.User{Login: "u", Role: role}, "Lang": i18n.JA}
		if err := s.tmpl.ExecuteTemplate(&b, "dist_notice", data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if out := render(s, "admin"); !strings.Contains(out, "windows/arm64") || !strings.Contains(out, updatecheck.ServerUpgradeCommand) {
		t.Errorf("admin の帯: %s", out)
	}
	if out := render(s, "member"); strings.TrimSpace(out) != "" {
		t.Errorf("member に帯が出た: %s", out)
	}
	if out := render(newSrv(Config{Version: "v1.1.0", DistDir: dir}), "admin"); strings.TrimSpace(out) != "" {
		t.Errorf("そろっているのに帯が出た: %s", out)
	}
	// 帯: 未設定は admin に出し、空の明示は出さない
	if out := render(unset, "admin"); !strings.Contains(out, "LOOPTRACK_DIST_DIR") {
		t.Errorf("未設定の帯: %s", out)
	}
	if out := render(off, "admin"); strings.TrimSpace(out) != "" {
		t.Errorf("空の明示なのに帯が出た: %s", out)
	}
}
