package usagesnap

// 会話記録の自動送信（DESIGN.md §9-8「hook」）が、この PC に残すもの。置き場は使用量と同じ looptrack の置き場（StateDir）。
//
//   - transcript-failure.json … 最後の失敗（使用量の usage-failure.json と同じ形）。送れたら消す。
//     `looptrack issue summary`（SessionStart の要約）が読んで「会話記録を送れていない」を 1 行出す。
//   - transcript-spool/<ProjectKey の鍵>/<agent>-<セッション ID>.json … 送れなかった記録の「どれを送るか」だけ
//     （agent・セッション ID・記録のパス・作業ディレクトリ）。**中身は入れない**（会話記録は大きく秘密も混ざるので、写しを増やさない）。
//     次の hook がこれを読み、元のファイルから続きを送る。7 日で捨てる。
//   - transcript-rejected/<ProjectKey の鍵>/<agent>-<セッション ID>.json … 変換が止めた（読めない行・キーの秘密・長すぎる行）記録の印。
//     同じ記録は何度試しても同じ結果になるので、hook は自動では試し直さない（手で打つ push は印を見ない）。7 日で捨てる。

import (
	"os"
	"path/filepath"
	"time"
)

const (
	// TranscriptSpoolDirName は送れなかった会話記録の「どれを送るか」の置き場（StateDir の下）。
	TranscriptSpoolDirName = "transcript-spool"
	// TranscriptRejectedDirName は変換が止めた記録の印の置き場（StateDir の下）。
	TranscriptRejectedDirName = "transcript-rejected"
	transcriptFailureFile     = "transcript-failure.json"
)

// 会話記録の自動送信だけの失敗の理由のキー（ほかは Fail* と同じ。表示のときに訳す）。
const (
	FailTranscriptRejected  = "transcript_rejected"  // 変換が止めた（読めない行・キーの秘密・長すぎる行。退避しない）
	FailTranscriptRewritten = "transcript_rewritten" // 送った後に記録が書き換えられた（purge するまで続きを送れない）
	// FailProxyLimit は前段のリバースプロキシが本文の大きさの上限で止めた（JSON でない 413。サーバには届いていない）。
	// 4xx なので FailRejected と同じく送り直さないが、直すのはサーバではなく前段の設定なので理由を分けて案内する。
	FailProxyLimit = "proxy_limit"
)

// RecordTranscriptFailure は会話記録の最後の失敗を書く（files は変換が止めたファイルの数。ほかの理由では 0）。
func RecordTranscriptFailure(dir string, now time.Time, reason string, status, files int) {
	recordFailureIn(dir, transcriptFailureFile, now, reason, status, files)
}

// ClearTranscriptFailure は会話記録を送れたときに最後の失敗の記録を消す。
func ClearTranscriptFailure(dir string) {
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, transcriptFailureFile))
}

// ReadTranscriptFailure は会話記録の最後の失敗（無い・読めなければ nil）。
func ReadTranscriptFailure(dir string) *Failure { return readFailureIn(dir, transcriptFailureFile) }

// TranscriptSpoolDir はプロジェクト（API の URL + slug）の、送れなかった会話記録の「どれを送るか」の置き場。
// 置き場かプロジェクトが分からなければ ""。数えるのは SpoolFiles。
func TranscriptSpoolDir(stateDir, apiURL, project string) string {
	return keyedDir(stateDir, TranscriptSpoolDirName, apiURL, project)
}

// TranscriptRejectedDir はプロジェクトの、変換が止めた記録の印の置き場（分からなければ ""）。
func TranscriptRejectedDir(stateDir, apiURL, project string) string {
	return keyedDir(stateDir, TranscriptRejectedDirName, apiURL, project)
}

// HandoffSpoolDirName はサーバに届かなかった引き継ぎの退避の置き場（StateDir の下）。
const HandoffSpoolDirName = "handoff-spool"

// HandoffSpoolDir はプロジェクト（API の URL + slug）の、引き継ぎの退避の置き場。数えるのは SpoolFiles。
func HandoffSpoolDir(stateDir, apiURL, project string) string {
	return keyedDir(stateDir, HandoffSpoolDirName, apiURL, project)
}

func keyedDir(stateDir, name, apiURL, project string) string {
	key := ProjectKey(apiURL, project)
	if stateDir == "" || key == "" {
		return ""
	}
	return filepath.Join(stateDir, name, key)
}
