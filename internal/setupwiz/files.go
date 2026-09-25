package setupwiz

import (
	"bufio"
	"fmt"
	"github.com/howashoji/looptrack/internal/i18n"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/auth"
	"github.com/howashoji/looptrack/internal/privfile"
)

func newSecretKey() (string, error) { return auth.NewSecretKey() }

// renderEnv は .env の中身を作る。値は単引用符で囲む（docker compose の env_file と sh の `. ./.env` の両方で
// 括弧などを含む DSN をそのまま読めるように。単引用符・改行を含む値は validEnvValue で拒否済み）。
func renderEnv(p *Plan, now time.Time, lang i18n.Lang) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", i18n.T(lang, "files.env.header", "time", now.Format("2006-01-02 15:04")))
	fmt.Fprintf(&b, "# %s\n", i18n.T(lang, "files.env.summary", "mode", modeLabel(lang, p.Mode), "store", storeLabel(lang, p.Store)))
	kv := func(k, v string) { fmt.Fprintf(&b, "%s='%s'\n", k, v) }
	b.WriteString("# " + i18n.T(lang, "files.env.store") + "\n")
	kv("LOOPTRACK_DSN", p.DSN)
	b.WriteString("# " + i18n.T(lang, "files.env.secret_key") + "\n")
	kv("LOOPTRACK_SECRET_KEY", p.SecretKey)
	b.WriteString("# " + i18n.T(lang, "files.env.listen") + "\n")
	if p.compose() {
		kv("LOOPTRACK_LISTEN", ":"+strconv.Itoa(p.Port)) // コンテナの中（公開は compose の 127.0.0.1:<port>）
	} else {
		kv("LOOPTRACK_LISTEN", "127.0.0.1:"+strconv.Itoa(p.Port))
	}
	kv("LOOPTRACK_BASE_PATH", p.BasePath)
	kv("LOOPTRACK_PUBLIC_URL", p.PublicURL)
	kv("LOOPTRACK_COOKIE_SECURE", strconv.FormatBool(strings.HasPrefix(p.PublicURL, "https://")))
	if p.Mode == ModeLocal {
		b.WriteString("# " + i18n.T(lang, "files.env.local_mode") + "\n")
		kv("LOOPTRACK_LOCAL_MODE", "1")
	}
	return b.String()
}

// renderCompose はチームのサーバ用の compose.yaml の雛形を作る（deploy/compose.yaml を元にする）。
// 注釈は setup を動かした人の言語で書く（.env と同じ）。設定の行は言語に依らない。
func renderCompose(p *Plan, now time.Time, lang i18n.Lang) string {
	port := strconv.Itoa(p.Port)
	var b strings.Builder
	writeComment(&b, "", i18n.T(lang, "files.compose.header", "time", now.Format("2006-01-02 15:04"), "port", port, "url", p.URL()))
	b.WriteString(`services:
  looptrack:
    image: ${LOOPTRACK_IMAGE:-looptrack:latest}
`)
	writeComment(&b, "    ", i18n.T(lang, "files.compose.image"))
	fmt.Fprintf(&b, `    build:
      context: .
      dockerfile: Dockerfile
    container_name: looptrack
    env_file: .env
    environment:
      GOMEMLIMIT: 64MiB
    ports:
      - "127.0.0.1:%s:%s"
`, port, port)
	if p.Store == StoreSQLite {
		writeComment(&b, "    ", i18n.T(lang, "files.compose.sqlite"))
		b.WriteString(`    volumes:
      - ./data:/data
`)
	} else {
		writeComment(&b, "    ", i18n.T(lang, "files.compose.mysql"))
	}
	writeComment(&b, "    ", i18n.T(lang, "files.compose.mem_limit"))
	b.WriteString(`    mem_limit: 160m
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    restart: unless-stopped
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
    healthcheck:
      test: ["CMD", "/looptrack", "healthcheck"]
      interval: 60s
      timeout: 5s
      retries: 3
      start_period: 10s
`)
	return b.String()
}

// renderDockerfile は compose が使うイメージの Dockerfile を作る（deploy/Dockerfile と同じ形）。
// ここではビルドをしない。同じディレクトリに置いた linux の実行ファイルを scratch に載せるだけ
// （シェルも curl も無い。健全性確認は looptrack healthcheck が自分自身の /healthz を叩く）。
func renderDockerfile(now time.Time, lang i18n.Lang) string {
	var b strings.Builder
	writeComment(&b, "", i18n.T(lang, "files.dockerfile.header", "time", now.Format("2006-01-02 15:04")))
	b.WriteString("FROM scratch\nCOPY looptrack /looptrack\n")
	writeComment(&b, "", i18n.T(lang, "files.dockerfile.notice"))
	fmt.Fprintf(&b, "COPY NOTICE /NOTICE\nUSER 65534:65534\nEXPOSE %d\nENTRYPOINT [\"/looptrack\"]\nCMD [\"serve\"]\n", DefaultPort)
	return b.String()
}

// writeComment は text の各行を indent + "# " の注釈にして書く（空の行は indent + "#"）。
func writeComment(b *strings.Builder, indent, text string) {
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			b.WriteString(indent + "#\n")
			continue
		}
		b.WriteString(indent + "# " + line + "\n")
	}
}

// readEnvFile は .env を読む（KEY=VALUE。# の行と空行は飛ばす。値の前後の引用符は外す）。
// ファイルが無ければ os.ErrNotExist を包んだエラーを返す。
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env, sc.Err()
}

// writeTemp は dir に一時ファイルを作って書き、fsync して閉じる（パーミッションは書く前に付ける）。
// private なら本人だけのファイル（unix は 0600、Windows は本人だけの ACL。privfile）にする。rename しても保護は残る。
func writeTemp(dir, name, content string, private bool) (string, error) {
	pattern := "." + strings.TrimPrefix(name, ".") + ".setup-*"
	var f *os.File
	var err error
	if private {
		f, err = privfile.CreateTemp(dir, pattern)
	} else if f, err = os.CreateTemp(dir, pattern); err == nil {
		err = f.Chmod(0o644)
	}
	if err != nil {
		if f != nil {
			f.Close()
			os.Remove(f.Name())
		}
		return "", err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return tmp, nil
}

// syncDir は rename をディスクに確定させる（対応しない OS では何もしない）。
func syncDir(dir string) {
	if d, err := os.Open(filepath.Clean(dir)); err == nil {
		d.Sync() //nolint:errcheck
		d.Close()
	}
}
