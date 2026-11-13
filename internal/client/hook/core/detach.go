package core

// usage の送信を切り離す（hook は操作を待たせずにすぐ 0 で終わり、送信は子プロセスが続ける）。
// 以前の実装は二重 fork + setsid だった。Go は fork できないので自分（looptrack）を起動し直し、仕事は標準入力の JSON で渡す。
// 起動の仕方（setsid・DETACHED_PROCESS）は internal/client/detach の 1 か所にある（loop の hook も同じ部品で離す）。

import (
	"os"

	"github.com/howashoji/looptrack/internal/client/detach"
)

// ChildArgs は切り離した子プロセスの引数（実行ファイルの後ろ）。cmd/looptrack の `hook usage` に届く形。
var ChildArgs = []string{"hook", "usage", usageJobFlag}

// spawnSelf は自分の実行ファイルを ChildArgs で起動し、job を標準入力に書いて待たずに離す（Env.Spawn の既定）。
// 環境変数は親と同じ（API の URL・トークンの場所を引き継ぐ）。標準出力・標準エラーは捨てる。
func spawnSelf(job []byte, dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnDetached(exe, ChildArgs, job, dir, nil)
}

// spawnTranscriptSelf は自分の実行ファイルを TranscriptChildArgs で起動する（Env.SpawnTranscript の既定。会話記録の送信）。
func spawnTranscriptSelf(job []byte, dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnDetached(exe, TranscriptChildArgs, job, dir, nil)
}

// spawnDetached は exe を切り離して起動する（detach.Start。core の各所の呼び出しの名前を保つ）。
func spawnDetached(exe string, args []string, job []byte, dir string, environ []string) error {
	return detach.Start(exe, args, job, dir, environ)
}
