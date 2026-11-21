package usagesnap

// SessionStart の要約のフックが送る導入済み通知（trigger=hook）の失敗を、この PC に残す。
//
// サーバは trigger=hook の通知を受けたときだけ「フックが動いている」と記録する。通知はフックの中で送るので、
// 失敗してもそのままでは誰にも見えない（フックは SessionStart を止めないよう、失敗しても何も出さずに終わる）。
// サーバには届いていないので、サーバの導入状態は「フックが動いていない」のまま理由を持たない。
// そこで最後の失敗をここに残し、要約と `looptrack issue installed` が読んで見せる。送れたら消す。
//
// 置き場は使用量と同じ looptrack の置き場（StateDir）の下の
// install-report-failure/<ProjectKey の鍵>/<agent>.json。形は usage-failure.json と同じ（Failure）。
// 導入状態はプロジェクトと AI ごとにサーバが持つので、記録も同じ単位で分ける（ほかのプロジェクトや別の AI の
// 通知が届いても、この組の失敗は消さない）。

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InstallFailureDirName は導入済み通知の失敗の置き場（StateDir の下。usage-failure.json・usage-spool と別の名前）。
const InstallFailureDirName = "install-report-failure"

// installFailurePath は (置き場, ファイル名)。置き場・プロジェクト・AI が分からなければ ("", "")。
func installFailurePath(stateDir, apiURL, project, agent string) (string, string) {
	dir := keyedDir(stateDir, InstallFailureDirName, apiURL, project)
	agent = strings.TrimSpace(agent)
	if dir == "" || agent == "" || strings.ContainsAny(agent, `/\.:`) {
		return "", ""
	}
	return dir, agent + ".json"
}

// RecordInstallFailure は導入済み通知の最後の失敗を書く（reason は Fail* のどれか。status は HTTP の状態、無ければ 0）。
// 書けなくてもエラーにしない（フックを止めない）。
func RecordInstallFailure(stateDir, apiURL, project, agent string, now time.Time, reason string, status int) {
	dir, name := installFailurePath(stateDir, apiURL, project, agent)
	if dir == "" {
		return
	}
	recordFailureIn(dir, name, now, reason, status, 0)
}

// ClearInstallFailure は導入済み通知が届いたときに最後の失敗の記録を消す。
func ClearInstallFailure(stateDir, apiURL, project, agent string) {
	dir, name := installFailurePath(stateDir, apiURL, project, agent)
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, name))
}

// ReadInstallFailure は導入済み通知の最後の失敗（無い・読めなければ nil）。
func ReadInstallFailure(stateDir, apiURL, project, agent string) *Failure {
	dir, name := installFailurePath(stateDir, apiURL, project, agent)
	if dir == "" {
		return nil
	}
	return readFailureIn(dir, name)
}
