package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
)

// serveCmd は serve の引数を読む。--env-file <path> は KEY=VALUE のファイル（looptrack setup が書く .env）を読み、
// まだ決まっていない環境変数だけに入れてから serve を起動する（環境変数が先に決まっていればそちらを優先）。
func serveCmd(args []string) int {
	lang := cmdLang()
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	envFile := fs.String("env-file", "", i18n.T(lang, "cmd.arg.serve.env_file"))
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return fail(i18n.Errorf("cmd.err.serve_extra_args", "args", strings.Join(fs.Args(), " ")))
	}
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return fail(err)
		}
	}
	return serve()
}

// loadEnvFile は .env を読み、未設定の環境変数だけを入れる。
// 書式: 空行と # で始まる行は無視、先頭の "export " は許す、値の前後の ' または " は外す（中の展開はしない）。
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("--env-file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return i18n.Errorf("cmd.err.env_file_format", "path", path, "line", n)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("--env-file %s:%d: %w", path, n, err)
		}
	}
	return sc.Err()
}
