package clitest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
)

// looptrack session transcript push / list / purge（会話記録の搬送。DESIGN.md §9-8）。
// 偽 API はサーバの応答の形（internal/server/transcripts_api.go）だけを返す。変換・続きの判断・状態のファイルは CLI の側で、
// 実サーバに対する往復は internal/server の TestTranscriptCLI* が確かめる。
// 会話記録は合成の行。golden の push のケースは一時ディレクトリに依らないパスで書く（trSeedsAt）。
// pull のケースは CLI が置いた中身に一時ディレクトリのパスが入る（JSON の中で逃がされる）ので、Windows では省略する（PosixPaths）。

const (
	trSID = "00000000-0000-4000-8000-0000000000f1"
	// trSecretValue は合成の秘密の値（domain の規則 password=… に当たる偽物）。出力に出ないことを確かめる
	trSecretValue = "Synthetic0Value9"
	pTrRegister   = "/api/v1/projects/demo/transcripts"
	pTr7          = "/api/v1/transcripts/7"
)

// trLines は合成の Claude Code の記録の本体（cwd と tool_use のパスは root の下）。
func trLines(root string) string {
	return `{"type":"user","sessionId":"` + trSID + `","cwd":"` + root + `","version":"2.1.0","message":{"role":"user","content":"x.go を直して"}}` + "\n" +
		`{"type":"assistant","sessionId":"` + trSID + `","cwd":"` + root + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Edit","input":{"file_path":"` + root + `/x.go"}}]}}` + "\n" +
		`{"type":"user","sessionId":"` + trSID + `","cwd":"` + root + `","message":{"role":"user","content":"設定は password=` + trSecretValue + ` にある"}}` + "\n"
}

func trSubLine(root string) string {
	return `{"type":"user","sessionId":"` + trSID + `","isSidechain":true,"cwd":"` + root + `","message":{"role":"user","content":"調べて"}}` + "\n"
}

// trSeedsAt は作業ツリー（ws/.git）と、合成の Claude Code の記録（本体とサブエージェント 1 つ。パスは root の下）。
// golden のケースは一時ディレクトリに依らないパス（/srv/x）で書く（書き換える手がかりに当たらないが、長さと sha256 が実行ごとに変わらない）。
// 書き換えは TestTranscriptPushNoValues（{{ROOT}}/ws。golden に記録しない）と internal/server の TestTranscriptCLI* が確かめる。
func trSeedsAt(root string) []Seed {
	return append(append([]Seed(nil), gitSeeds...),
		Seed{Path: "home/.claude/projects/-proj/" + trSID + ".jsonl", Content: trLines(root)},
		Seed{Path: "home/.claude/projects/-proj/" + trSID + "/subagents/agent-a1.jsonl", Content: trSubLine(root)})
}

var (
	trSeeds = trSeedsAt("/srv/x")
	trEnv   = map[string]string{"LOOPTRACK_MACHINE_LABEL": "desk-a"}
)

// trStateJSON はサーバの transcript の形（seq と files だけを変える）。
func trStateJSON(seq int, files string) string {
	return `{"id":7,"project":"demo","agent":"claude-code","vendor_session_id":"` + trSID + `","machine_label":"desk-a","origin_os":"linux",` +
		`"agent_version":"2.1.0","created_at":"2026-10-25T00:00:00Z","via":"cli","seq":` + strconv.Itoa(seq) + `,"files":` + files + `,"transcripts_enabled":true}`
}

func trRegistered(seq int, files string) Response {
	return created(`{"transcript":` + trStateJSON(seq, files) + `,"created":true}`)
}

func trAppended(seq int) Response {
	return created(`{"chunk":{"seq":` + strconv.Itoa(seq) + `},"transcript":` + trStateJSON(seq, `[]`) + `}`)
}

// transcriptCases は golden に記録するケース。
func transcriptCases() []Case {
	mainLen := strconv.Itoa(len(trLines("/srv/x")))
	return []Case{
		{Name: "session-transcript/help", Group: "session", Args: []string{"transcript", "-h"}},
		{Name: "session-transcript/push-help", Group: "session", Args: []string{"transcript", "push", "-h"}},
		// --dry-run は件数だけで、要求を 1 本も送らない
		{Name: "session-transcript/push-dry-run", Group: "session", Args: []string{"transcript", "push", trSID, "--dry-run"}, Env: trEnv,
			Seeds: trSeeds},
		// 登録 → 本体 → サブエージェントの順に追記する（If-Match は今の seq・本文は gzip・状態のファイルを書く）
		{Name: "session-transcript/push", Group: "session", Args: []string{"transcript", "push", trSID}, Env: trEnv, Seeds: trSeeds,
			Routes: []Route{r("POST", pTrRegister, trRegistered(0, `[]`)), r("POST", pTr7+"/chunks", trAppended(1), trAppended(2))}},
		// --current（セッション ID は環境から）
		{Name: "session-transcript/push-current", Group: "session", Args: []string{"transcript", "push", "--current"},
			Env: map[string]string{"LOOPTRACK_MACHINE_LABEL": "desk-a", "CLAUDE_CODE_SESSION_ID": trSID}, Seeds: trSeeds,
			Routes: []Route{r("POST", pTrRegister, trRegistered(0, `[]`)), r("POST", pTr7+"/chunks", trAppended(1), trAppended(2))}},
		// 409（ほかの送信が先に本体を送った）: 取り直して、本体は送らずサブエージェントだけを If-Match "1" で送る
		{Name: "session-transcript/push-resync", Group: "session", Args: []string{"transcript", "push", trSID}, Env: trEnv, Seeds: trSeeds,
			Routes: []Route{r("POST", pTrRegister, trRegistered(0, `[]`)),
				r("POST", pTr7+"/chunks", ErrorResponse(http.StatusConflict, "transcript_seq",
					i18n.T(i18n.JA, "service.err.transcript.seq", "id", 7, "current", 1, "given", 0)), trAppended(2)),
				r("GET", pTr7, Response{Body: `{"transcript":` + trStateJSON(1, `[{"file_key":"`+trSID+`.jsonl","length":`+mainLen+`}]`) + `,"chunks":[]}`})}},
		// 変換が止めた（キーに秘密）: 要求を送らず、件数と理由だけで終了コード 1
		{Name: "session-transcript/push-reject", Group: "session", Args: []string{"transcript", "push", trSID}, Env: trEnv,
			Seeds: append(append([]Seed(nil), gitSeeds...), Seed{Path: "home/.claude/projects/-proj/" + trSID + ".jsonl",
				Content: trLines("/srv/x") + `{"type":"user","password=` + trSecretValue + `":1}` + "\n"})},
		// ルールが切: サーバの理由を出して終了コード 1（切を覚える）
		{Name: "session-transcript/push-rule-off", Group: "session", Args: []string{"transcript", "push", trSID}, Env: trEnv, Seeds: trSeeds,
			Routes: []Route{r("POST", pTrRegister, ErrorResponse(422, "transcript_rule_off", i18n.T(i18n.JA, "service.err.transcript.rule_off", "slug", "demo")))}},
		// 置き場が無いサーバ: サーバの理由を出して終了コード 1
		{Name: "session-transcript/push-unavailable", Group: "session", Args: []string{"transcript", "push", trSID}, Env: trEnv, Seeds: trSeeds,
			Routes: []Route{r("POST", pTrRegister, ErrorResponse(422, "transcripts_unavailable", i18n.T(i18n.JA, "service.err.transcript.unavailable")))}},
		{Name: "session-transcript/push-not-found", Group: "session", Args: []string{"transcript", "push", trSID}, Env: trEnv, Seeds: gitSeeds},
		{Name: "session-transcript/list", Group: "session", Args: []string{"transcript", "list"},
			Routes: []Route{rq("GET", "/api/v1/transcripts", map[string]string{"project": "demo"}, ok(`{"transcripts":[`+
				`{"id":7,"project":"demo","agent":"claude-code","vendor_session_id":"`+trSID+`","machine_label":"desk-a","chunks":2,"files":2,"bytes":512,"last_chunk_at":"2026-10-25T01:00:00Z","purges":0,"purged":false},`+
				`{"id":5,"project":"demo","agent":"codex","vendor_session_id":"019a0000-0000-7000-8000-0000000000d1","machine_label":"","chunks":0,"files":0,"bytes":0,"purges":1,"purged":true}],"count":2}`))}},
		{Name: "session-transcript/list-empty", Group: "session", Args: []string{"transcript", "list", "--all-projects"},
			Routes: []Route{r("GET", "/api/v1/transcripts", ok(`{"transcripts":[],"count":0}`))}},
		{Name: "session-transcript/purge", Group: "session", Args: []string{"transcript", "purge", "7", "--reason", "別の端末へ移した"},
			Routes: []Route{r("POST", pTr7+"/purge", ok(`{"transcript_id":7,"changed":true,"chunk_ids":[1,2],"removed":2,"message":"x"}`))}},
		{Name: "session-transcript/purge-not-found", Group: "session", Args: []string{"transcript", "purge", "8"},
			Routes: []Route{r("POST", "/api/v1/transcripts/8/purge", ErrorResponse(404, "not_found", i18n.T(i18n.JA, "service.err.transcript.not_found", "id", "8")))}},
	}
}

// trPullBody は取り込むチャンクの本体（送る前の変換の後の形。プレースホルダの印は固定）。
const trPullBody = `{"type":"user","sessionId":"` + trSID + `","cwd":"{{LT:0123456789ab:ROOT0}}","message":{"role":"user","content":"x.go を直して"}}` + "\n" +
	`{"type":"assistant","sessionId":"` + trSID + `","cwd":"{{LT:0123456789ab:ROOT0}}","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Edit","input":{"file_path":"{{LT:0123456789ab:ROOT0}}/x.go"}}]}}` + "\n"

// trPullDetail は GET /transcripts/7 の応答（originOS は送った端末の OS、purged はチャンクを消去済みにする）。
func trPullDetail(originOS string, purged bool) Response {
	sum := sha256.Sum256([]byte(trPullBody))
	st := strings.Replace(trStateJSON(1, `[{"file_key":"`+trSID+`.jsonl","length":`+strconv.Itoa(len(trPullBody))+`}]`), `"origin_os":"linux"`, `"origin_os":"`+originOS+`"`, 1)
	chunk := `{"seq":1,"file_key":"` + trSID + `.jsonl","file_kind":"jsonl","src_offset":0,"src_length":` + strconv.Itoa(len(trPullBody)) +
		`,"line_count":2,"sha256":"` + hex.EncodeToString(sum[:]) + `","size":` + strconv.Itoa(len(trPullBody)) +
		`,"encoding":"identity","nonce":"0123456789ab","placeholders":["ROOT0"],"stats":{"lines":2,"unrewritten_prose":1},"created_at":"2026-10-25T00:00:00Z","via":"cli","purged":` +
		strconv.FormatBool(purged) + `,"url":"/im/api/v1/transcripts/7/chunks/1"}`
	return ok(`{"transcript":` + st + `,"chunks":[` + chunk + `]}`)
}

// otherFamilyOS は、この端末と OS の系統（Windows とそれ以外）が違う OS。
func otherFamilyOS() string {
	if runtime.GOOS == "windows" {
		return "linux"
	}
	return "windows"
}

// transcriptPullCases は pull の golden（置いた中身に一時ディレクトリのパスが入るので PosixPaths）。
func transcriptPullCases() []Case {
	body := Response{Body: trPullBody, ContentType: "application/octet-stream", Header: map[string]string{"X-Looptrack-Transcript-Encoding": "identity"}}
	routes := func(detail Response) []Route {
		return []Route{r("GET", pTr7, detail), r("GET", pTr7+"/chunks/1", body)}
	}
	return []Case{
		// 新しいセッション ID で作業ツリーの slug の下に置き、fork の再開のコマンドを表示する（AI は起動しない）
		{Name: "session-transcript/pull", Group: "session", Args: []string{"transcript", "pull", "7"}, Seeds: gitSeeds, PosixPaths: true,
			Routes: routes(trPullDetail(runtime.GOOS, false))},
		// --print-resume: 標準出力は再開のコマンドの 1 行だけ
		{Name: "session-transcript/pull-print-resume", Group: "session", Args: []string{"transcript", "pull", "7", "--print-resume"}, Seeds: gitSeeds, PosixPaths: true,
			Routes: routes(trPullDetail(runtime.GOOS, false))},
		// 同じセッション ID の記録が別の slug にあれば置かない
		{Name: "session-transcript/pull-duplicate", Group: "session", Args: []string{"transcript", "pull", "7"}, PosixPaths: true,
			Seeds:  append(append([]Seed(nil), gitSeeds...), Seed{Path: "home/.claude/projects/-elsewhere/" + trSID + ".jsonl", Content: "{}\n"}),
			Routes: routes(trPullDetail(runtime.GOOS, false))},
		// OS の系統が違う端末で作った記録は置かない（本体も取らない）
		{Name: "session-transcript/pull-cross-os", Group: "session", Args: []string{"transcript", "pull", "7"}, Seeds: gitSeeds, PosixPaths: true,
			Routes: routes(trPullDetail(otherFamilyOS(), false))},
		// 消去済みは置かない
		{Name: "session-transcript/pull-purged", Group: "session", Args: []string{"transcript", "pull", "7"}, Seeds: gitSeeds, PosixPaths: true,
			Routes: routes(trPullDetail(runtime.GOOS, true))},
		// git の作業ツリーの外ではプレースホルダを戻せないので置かない
		{Name: "session-transcript/pull-no-root", Group: "session", Args: []string{"transcript", "pull", "7"}, PosixPaths: true,
			Routes: routes(trPullDetail(runtime.GOOS, false))},
		{Name: "session-transcript/pull-help", Group: "session", Args: []string{"transcript", "pull", "-h"}},
	}
}

// TestTranscriptPushNoValues は、push の出力・エラー（dry-run・送信・止め）に秘密の値と端末の一時パスが出ないことを確かめる。
// 対照: 秘密の値は置いた記録の中にあり（送る前の変換の対象）、送った本文（gzip を伸ばしたもの）にはプレースホルダがある。
func TestTranscriptPushNoValues(t *testing.T) {
	impl := GoImpl(goBin(t))
	for _, name := range []string{"session-transcript/push-dry-run", "session-transcript/push", "session-transcript/push-reject"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("中身に POSIX の形のパス（{{ROOT}}/ws）を書くので Windows では省略")
			}
			c := findTranscriptCase(t, name)
			// 書き換えの手がかり（作業ツリーのルート）に当たるパスで置き直す（止めのケースは止める行を残す）
			seeds := trSeedsAt(RootPlaceholder + "/ws")
			if name == "session-transcript/push-reject" {
				seeds[len(gitSeeds)].Content += `{"type":"user","password=` + trSecretValue + `":1}` + "\n"
			}
			c.Seeds = seeds
			res := Run(t, impl, c)
			out := res.Stdout + res.Stderr
			if strings.Contains(out, trSecretValue) {
				t.Fatalf("出力に秘密の値が出ています:\n%s", out)
			}
			for _, p := range res.Norm.Paths {
				if p != "" && strings.Contains(out, p) {
					t.Fatalf("出力に端末のパス（%s）が出ています:\n%s", p, out)
				}
			}
			seeded := false
			for _, f := range res.Files {
				if strings.HasSuffix(f.Path, trSID+".jsonl") && bytes.Contains(f.Content, []byte(trSecretValue)) {
					seeded = true
				}
			}
			if !seeded {
				t.Fatalf("前提が崩れています: 置いた記録に秘密の値がありません")
			}
			for _, q := range res.Requests {
				if q.Header.Get("Content-Encoding") != "gzip" {
					continue
				}
				zr, err := gzip.NewReader(bytes.NewReader(q.Body))
				if err != nil {
					t.Fatal(err)
				}
				plain, _ := io.ReadAll(zr)
				if bytes.Contains(plain, []byte(trSecretValue)) || bytes.Contains(plain, []byte(res.Norm.Paths[0])) {
					t.Fatalf("送った本文に値が残っています:\n%s", plain)
				}
				if !bytes.Contains(plain, []byte("{{LT:")) {
					t.Fatalf("前提が崩れています: 送った本文にプレースホルダがありません:\n%s", plain)
				}
			}
			switch name {
			case "session-transcript/push-dry-run", "session-transcript/push-reject":
				if len(res.Requests) != 0 {
					t.Fatalf("要求を %d 本送っています（0 本のはず）", len(res.Requests))
				}
			case "session-transcript/push":
				if len(res.Requests) != 3 {
					t.Fatalf("要求 %d 本（登録と追記 2 本のはず）", len(res.Requests))
				}
			}
		})
	}
}

func findTranscriptCase(t *testing.T, name string) Case {
	t.Helper()
	for _, c := range transcriptCases() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("ケースがありません: %s", name)
	return Case{}
}
