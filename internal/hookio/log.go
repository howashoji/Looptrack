package hookio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// HookLogEnv は hook の判定の記録を有効にする環境変数（値が "1" のときだけ記録する）。
//
// 既定では何も書かない（ファイルも作らない）。hook の運用の記録（どの hook が・どれだけ・何を理由に止めたか）を
// 集めるときだけ有効にする。誤検知かどうかは、記録のセッション ID を手がかりに会話の記録と突き合わせて判断する。
const HookLogEnv = "LOOPTRACK_LOOP_HOOK_LOG"

// HookLogMax は記録のファイルの上限（バイト）。超えたら 1 世代だけ <名前>.1 へ回す（前の .1 は消える）。
const HookLogMax = 1 << 20

// 判定の語（記録の decision）。Render が実際に出す形（Normalize の後）から決める。
const (
	DecisionDeny    = "deny"
	DecisionBlock   = "block"
	DecisionAsk     = "ask"
	DecisionUpdate  = "update"
	DecisionContext = "context"
	DecisionSystem  = "system"
	DecisionPass    = "pass"
	DecisionError   = "error"
	DecisionTimeout = "timeout"
	DecisionPanic   = "panic"
)

// logRecord は記録の 1 行。
//
// **コマンドの文字列・プロンプトの本文・理由の文面・パスは入れない**（秘密が混ざらないようにするため）。
// kind は hook がコード中の定数から選んだ語（Result.Kind）だけで、入力から切り出した文字列は入らない。
// session・subagent・event は入力から来るので、記号を落として長さを切る（logID）。
type logRecord struct {
	TS       string `json:"ts"`
	Hook     string `json:"hook"`
	Event    string `json:"event"`
	Decision string `json:"decision"`
	Session  string `json:"session,omitempty"`
	Subagent string `json:"subagent,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

var logIDDrop = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// logID は入力から来た識別子を記録に書ける形にする（英数字と _ . - だけ・64 文字まで）。
// セッション ID は UUID の形なので、ふつうはそのまま残る。
func logID(s string) string {
	s = logIDDrop.ReplaceAllString(s, "")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// decisionOf は、Render が実際に出す形（Normalize の後）の判定の語。
func decisionOf(n Result) string {
	switch {
	case n.Deny != "":
		return DecisionDeny
	case n.Block != "":
		return DecisionBlock
	case n.Ask != "":
		return DecisionAsk
	case n.UpdatedInput != "":
		return DecisionUpdate
	case n.Context != "":
		return DecisionContext
	case n.SystemMessage != "":
		return DecisionSystem
	}
	return DecisionPass
}

// hookLog は Run の 1 回分の記録（Run の最後に 1 行だけ書く）。
type hookLog struct {
	opts     RunOptions
	ev       Event
	parsed   bool
	decision string
	kind     string
}

// write は記録を 1 行追記する。有効でない・判定が決まっていない・置き場が分からないときは何もしない。
// 書き込みの失敗（ディレクトリを作れない・開けない・回せない）は捨てる（fail-open。hook の出力と終了コードは変えない）。
func (l *hookLog) write() {
	defer func() { _ = recover() }() // 記録の失敗で hook の終了コードを変えない
	o := l.opts
	if !o.HookLog || l.decision == "" || o.LogPath == nil {
		return
	}
	ev := l.ev
	if !l.parsed {
		ev = FromMap(map[string]any{}, o.Parse) // 入力が読めなかったとき。置き場の解決（作業ディレクトリ）にだけ使う
	}
	path := o.LogPath(ev)
	if path == "" {
		return
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	event := string(ev.Name)
	if ev.Name == Other || ev.Name == "" {
		event = logID(ev.RawName)
	}
	if !l.parsed {
		event = logID(o.Parse.Event)
	}
	rec := logRecord{
		TS:       now().Format(time.RFC3339),
		Hook:     logID(o.HookName),
		Event:    event,
		Decision: l.decision,
		Kind:     l.kind,
	}
	if l.parsed {
		rec.Session = logID(ev.SessionID)
		rec.Subagent = logID(ev.SubagentID)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	appendLog(path, append(b, '\n'))
}

// appendLog は path に line を追記する。追記すると HookLogMax を超えるなら、先に path を path.1 へ回す（1 世代だけ）。
func appendLog(path string, line []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size()+int64(len(line)) > HookLogMax {
		_ = os.Remove(path + ".1") // Windows の Rename は上書きしない
		if err := os.Rename(path, path+".1"); err != nil {
			return
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(line)
	_ = f.Close()
}
