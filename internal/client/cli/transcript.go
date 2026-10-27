package cli

// 会話記録の搬送（DESIGN.md §2-5・§9-8）のサブコマンド: `looptrack session transcript push|pull|list|purge`。
// 要約（session digest / pull。§9-7）と取り違えないよう 1 段下にまとめる（push と pull が対のまま残り、
// 引数（イシューの ID と記録の ID）も混ざらない）。
//
//   - push は会話記録を探して（usagesnap）、送る前の変換（transcript.Convert）を通し、登録 → チャンクの追記で送る
//     （transcript_push.go）。
//   - pull はチャンクを順に読んでこの端末の値に戻し、AI が探す場所に新しいセッション ID で置いて、fork の再開の
//     コマンドを表示する。AI は起動しない（transcript_pull.go）。
//   - list は自分の記録の一覧、purge は自分の記録の本体の消去（この file）。
//
// 出力にはファイルの中身・端末の絶対パス・秘密の値を出さない（件数・規則の語・相対の名前だけ）。pull が置いた場所と
// 再開のコマンドだけは、利用者が実行するために絶対パスを出す。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// transcriptCommand は `looptrack session transcript` の木。
func transcriptCommand(lang i18n.Lang) *Command {
	return &Command{Name: "transcript", Help: i18n.T(lang, "cli.cmd.session.transcript"), SubDest: "transcript_action", Subs: []*Command{
		{Name: "push", Help: i18n.T(lang, "cli.cmd.session.transcript.push"), Run: cmdTranscriptPush, Args: []*Arg{
			{Name: "session", Nargs: "?", Metavar: "SESSION_ID", Help: i18n.T(lang, "cli.arg.transcript.push.session")},
			{Name: "--current", Kind: Bool, Help: i18n.T(lang, "cli.arg.transcript.push.current")},
			{Name: "--agent", Choices: []string{trAgentClaude, trAgentCodex}, Help: i18n.T(lang, "cli.arg.transcript.push.agent")},
			{Name: "--dry-run", Kind: Bool, Help: i18n.T(lang, "cli.arg.transcript.push.dry_run")},
		}},
		{Name: "pull", Help: i18n.T(lang, "cli.cmd.session.transcript.pull"), Run: cmdTranscriptPull, Args: []*Arg{
			{Name: "id", Metavar: "TRANSCRIPT_ID", Help: i18n.T(lang, "cli.arg.transcript.id")},
			{Name: "--print-resume", Kind: Bool, Help: i18n.T(lang, "cli.arg.transcript.pull.print_resume")},
			{Name: "--out", Metavar: "DIR", Help: i18n.T(lang, "cli.arg.transcript.pull.out")},
		}},
		{Name: "list", Help: i18n.T(lang, "cli.cmd.session.transcript.list"), Run: cmdTranscriptList, Args: []*Arg{
			{Name: "--all-projects", Kind: Bool, Help: i18n.T(lang, "cli.arg.transcript.list.all_projects")},
			{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.transcript.list.json")},
		}},
		{Name: "purge", Help: i18n.T(lang, "cli.cmd.session.transcript.purge"), Run: cmdTranscriptPurge, Args: []*Arg{
			{Name: "id", Metavar: "TRANSCRIPT_ID", Help: i18n.T(lang, "cli.arg.transcript.id")},
			{Name: "--reason", Metavar: "TEXT", Help: i18n.T(lang, "cli.arg.transcript.purge.reason")},
		}},
	}}
}

// AI の名前（サーバの agent・usagesnap の Client と同じ値）。
const (
	trAgentClaude = "claude-code"
	trAgentCodex  = "codex"
)

// trFile はファイルごとの送った長さ（サーバの応答の files[]）。
type trFile struct {
	FileKey string `json:"file_key"`
	Length  int64  `json:"length"`
}

// trState は会話記録 1 つの今の状態（登録・追記・取得の応答の transcript）。
type trState struct {
	ID                 int64    `json:"id"`
	Project            string   `json:"project"`
	Agent              string   `json:"agent"`
	VendorSessionID    string   `json:"vendor_session_id"`
	MachineLabel       string   `json:"machine_label"`
	OriginOS           string   `json:"origin_os"`
	AgentVersion       string   `json:"agent_version"`
	CreatedAt          string   `json:"created_at"`
	Seq                int64    `json:"seq"`
	Files              []trFile `json:"files"`
	TranscriptsEnabled bool     `json:"transcripts_enabled"`
}

// lengths はファイルごとの送った長さ。
func (s trState) lengths() map[string]int64 {
	out := map[string]int64{}
	for _, f := range s.Files {
		out[f.FileKey] = f.Length
	}
	return out
}

// trChunk はチャンク 1 つのメタデータ（GET /transcripts/{id} の chunks[]）。
type trChunk struct {
	Seq          int64           `json:"seq"`
	FileKey      string          `json:"file_key"`
	FileKind     string          `json:"file_kind"`
	SrcOffset    int64           `json:"src_offset"`
	SrcLength    int64           `json:"src_length"`
	SHA256       string          `json:"sha256"`
	Size         int64           `json:"size"`
	Encoding     string          `json:"encoding"`
	Nonce        string          `json:"nonce"`
	Placeholders json.RawMessage `json:"placeholders"`
	Stats        json.RawMessage `json:"stats"`
	Purged       bool            `json:"purged"`
}

// trSummary は一覧の 1 行。
type trSummary struct {
	ID              int64  `json:"id"`
	Project         string `json:"project"`
	Agent           string `json:"agent"`
	VendorSessionID string `json:"vendor_session_id"`
	MachineLabel    string `json:"machine_label"`
	Chunks          int64  `json:"chunks"`
	Files           int64  `json:"files"`
	Bytes           int64  `json:"bytes"`
	LastChunkAt     string `json:"last_chunk_at"`
	Purged          bool   `json:"purged"`
}

// trDo は会話記録の API を呼び、応答の本文を v に読む（v が nil なら読まない）。古いサーバ（経路が無い 404）は
// 「このサーバは会話記録に対応していない」にする。
func trDo(cl *api.Client, req api.Request, v any) (*api.Response, error) {
	req.Raw = true
	res, err := cl.Do(req)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "unknown_api" {
			return nil, i18n.Errorf("cli.err.transcript.unsupported")
		}
		return nil, err
	}
	if v != nil {
		if err := json.Unmarshal(res.Raw, v); err != nil {
			return nil, i18n.Errorf("cli.err.transcript.bad_response", "reason", err)
		}
	}
	return res, nil
}

// trID は記録の ID（正の整数）。
func trID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, i18n.Errorf("cli.err.transcript.id", "id", raw)
	}
	return id, nil
}

func trPath(id int64, suffix string) string {
	return "/transcripts/" + strconv.FormatInt(id, 10) + suffix
}

// cmdTranscriptList は自分の会話記録の一覧（既定は今のプロジェクトのものだけ）。
func cmdTranscriptList(c *Ctx, v *Values) error {
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	path := "/transcripts"
	if !v.Bool("all_projects") {
		slug, err := c.Project()
		if err != nil {
			return err
		}
		path += "?project=" + url.QueryEscape(slug)
	}
	var body struct {
		Transcripts []trSummary `json:"transcripts"`
	}
	res, err := trDo(cl, api.Request{Method: http.MethodGet, Path: path}, &body)
	if err != nil {
		return err
	}
	if v.Bool("json") {
		val, err := jsonorder.Decode(res.Raw)
		if err != nil {
			return i18n.Errorf("cli.err.transcript.bad_response", "reason", err)
		}
		c.PrintJSON(val)
		return nil
	}
	if len(body.Transcripts) == 0 {
		c.Println(i18n.T(c.Lang, "cli.transcript.list.none"))
		return nil
	}
	c.Println(i18n.T(c.Lang, "cli.transcript.list.header"))
	for _, t := range body.Transcripts {
		last := t.LastChunkAt
		if last == "" {
			last = "-"
		}
		state := ""
		if t.Purged {
			state = "  " + i18n.T(c.Lang, "cli.transcript.list.purged")
		}
		c.Println(strings.Join([]string{strconv.FormatInt(t.ID, 10), t.Agent, t.VendorSessionID, orDash(t.MachineLabel), t.Project,
			strconv.FormatInt(t.Chunks, 10), strconv.FormatInt(t.Files, 10), strconv.FormatInt(t.Bytes, 10), last}, "  ") + state)
	}
	return nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// cmdTranscriptPurge は自分の会話記録の本体を消す（取り消せない。記録の行と消去の記録は残る）。
func cmdTranscriptPurge(c *Ctx, v *Values) error {
	id, err := trID(v.Str("id"))
	if err != nil {
		return err
	}
	cl, err := c.RequireAPI("")
	if err != nil {
		return err
	}
	var body struct {
		Changed  bool    `json:"changed"`
		ChunkIDs []int64 `json:"chunk_ids"`
		Message  string  `json:"message"`
	}
	req := jsonorder.NewObject().Set("reason", v.Str("reason"))
	if _, err := trDo(cl, api.Request{Method: http.MethodPost, Path: trPath(id, "/purge"), Body: req}, &body); err != nil {
		return err
	}
	if !body.Changed {
		c.Println(i18n.T(c.Lang, "cli.transcript.purge.unchanged", "id", id))
		return nil
	}
	c.Println(i18n.TN(c.Lang, "cli.transcript.purge.done", len(body.ChunkIDs), "id", id, "count", len(body.ChunkIDs)))
	return nil
}
