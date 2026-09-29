package usagesnap

// トークン情報の送信の失敗を、この PC に残す（付与の hook（internal/client/hook/core）が書き、
// `looptrack issue summary`（SessionStart の要約）が読んで利用者と AI に見せる）。
//
// 送信の失敗は hook の中で起きるので、そのままでは LOOPTRACK_USAGE_DEBUG を付けない限り誰にも見えない
// （hook は操作を妨げないよう、失敗しても何も出さずに 0 で終わる）。サーバには届いていないので、サーバの
// 未付与の一覧（usage missing）は「何が付いていないか」は数えられても「なぜか（hook が送れていない）」は分からない。
// そこで、最後の失敗と、手元に退避して再送を待っている件数をここに残す。
//
// 置き場は looptrack の置き場（資格情報と同じ。internal/client/cred）:
//   - usage-failure.json … 最後の失敗（理由のキー・HTTP の状態・時刻・続けて失敗した回数）。送信に成功したら消す。
//     文面ではなくキーで書く（書いたときと読むときで言語が違いうるため。表示のときに訳す）。
//   - usage-spool/<プロジェクトの鍵>/*.json … 送れなかった payload（再送待ち）。名前は <退避した時刻（Unix 秒）>-<16 進 8 桁>.json。
//     鍵は ProjectKey（API の URL + slug）。payload はプロジェクトを持たないので、置き場で送り先を分ける
//     （分けないと、別のプロジェクトのセッションが再送したときにそのプロジェクトへ送ってしまう）。
//     usage-spool/*.json（鍵の無い、以前の版の置き場）は送り先が分からないので再送しない（7 日で捨てる）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/cred"
	"github.com/howashoji/looptrack/internal/client/env"
)

const (
	// SpoolDirName は送れなかった payload の置き場（StateDir の下）。
	SpoolDirName = "usage-spool"
	failureFile  = "usage-failure.json"
)

// 失敗の理由のキー（usage-failure.json の reason。表示のときに訳す）。
const (
	FailUnreachable = "unreachable"  // サーバに届かない・5xx・時間切れ（退避して再送する）
	FailRejected    = "rejected"     // サーバが 4xx で拒否した（再送しても直らないので捨てた）
	FailNoToken     = "no_token"     // ログインしていない（退避して、ログインの後に再送する）
	FailSpool       = "spool_failed" // 退避の書き込みに失敗した（その payload は失った）
	FailClient      = "client_error" // 資格情報を読めないなど、送る前に失敗した（退避して再送する）
)

// Failure は最後の送信の失敗。
type Failure struct {
	At     time.Time `json:"at"`               // 最後に失敗した時刻（この PC の時計）
	Reason string    `json:"reason"`           // Fail* のどれか（知らないキーはそのまま表示する）
	Status int       `json:"status,omitempty"` // rejected のときの HTTP の状態
	Count  int       `json:"count"`            // 送信に成功するまでに続けて失敗した回数
}

// StateDir は usage-spool・usage-last・usage-failure.json の置き場（資格情報と同じ looptrack の置き場。
// Windows は %APPDATA%\looptrack、他は $XDG_CONFIG_HOME（無ければ ~/.config）/looptrack）。ホームが分からなければ ""。
func StateDir(e env.Env) string {
	p, err := cred.DefaultPaths(e)
	if err != nil || p.Primary == "" {
		return ""
	}
	return filepath.Dir(p.Primary)
}

// RecordFailure は最後の失敗を書く（続けて失敗した回数を 1 つ増やす）。書けなくてもエラーにしない（hook を止めない）。
func RecordFailure(dir string, now time.Time, reason string, status int) {
	if dir == "" {
		return
	}
	f := ReadFailure(dir)
	n := 1
	if f != nil {
		n = f.Count + 1
	}
	b, err := json.Marshal(Failure{At: now.UTC(), Reason: reason, Status: status, Count: n})
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, failureFile), b, 0o644)
}

// ClearFailure は送信に成功したときに最後の失敗の記録を消す。
func ClearFailure(dir string) {
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, failureFile))
}

// ReadFailure は最後の失敗（無い・読めなければ nil）。
func ReadFailure(dir string) *Failure {
	if dir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, failureFile))
	if err != nil {
		return nil
	}
	var f Failure
	if json.Unmarshal(b, &f) != nil || f.Reason == "" {
		return nil
	}
	return &f
}

// SpoolDir はプロジェクト（API の URL + slug）の、送れなかった payload の置き場（stateDir は StateDir）。
// 置き場かプロジェクトが分からなければ ""。
func SpoolDir(stateDir, apiURL, project string) string {
	key := ProjectKey(apiURL, project)
	if stateDir == "" || key == "" {
		return ""
	}
	return filepath.Join(stateDir, SpoolDirName, key)
}

// SpoolFiles は再送を待っている payload のファイル（spoolDir は SpoolDir。古い順）。
func SpoolFiles(spoolDir string) []string {
	if spoolDir == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(spoolDir, "*.json"))
	out := paths[:0]
	for _, p := range paths {
		if !strings.HasPrefix(filepath.Base(p), ".") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// SpooledAt は退避したファイルの退避した時刻（名前の先頭の Unix 秒。読めなければ更新時刻）。
func SpooledAt(path string) (time.Time, bool) {
	base := filepath.Base(path)
	if i := strings.IndexByte(base, '-'); i > 0 {
		if sec, err := strconv.ParseInt(base[:i], 10, 64); err == nil && sec > 0 {
			return time.Unix(sec, 0), true
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}
