package clitest

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ブラウザの代わり（login --browser 用）。
//
// CLI はブラウザを開くときに環境変数 BROWSER のコマンドへ URL を渡す（以前の CLI からの決まり）。
// テストでは BROWSER に小さなシェルスクリプトを入れ、テストの実行ファイル自身を CLITEST_BROWSER_HELPER=1 で裏で起動する。
// 起動されたテストの実行ファイルは（TestMain から BrowserHelperMain を呼び）authorize の URL を開き、偽 API の 302 に従って
// CLI の戻り先（127.0.0.1 のコールバック）へ認可コードを届けて終わる。CLI はブラウザのコマンドの終了を待つので、裏で動かす。

const browserEnv = "CLITEST_BROWSER_HELPER"

func browserHelper(dir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "browser.sh")
	script := fmt.Sprintf("#!/bin/sh\n%s=1 %s \"$1\" >/dev/null 2>&1 </dev/null &\nexit 0\n", browserEnv, shellQuote(exe))
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		return "", err
	}
	return p, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// BrowserHelperMain は、テストの実行ファイルがブラウザの代わりとして起動されたときにその仕事をして true を返す
// （呼び出し側はそのまま終了する）。TestMain の最初で呼ぶ。
func BrowserHelperMain() bool {
	if os.Getenv(browserEnv) != "1" || len(os.Args) < 2 {
		return false
	}
	c := &http.Client{Timeout: 30 * time.Second}
	if res, err := c.Get(os.Args[len(os.Args)-1]); err == nil {
		res.Body.Close()
	}
	return true
}
