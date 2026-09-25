package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/relver"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/kit"
)

// MCP の接続設定だけで導入が完了するための仕組み（設計は DESIGN.md §5-6）。
//   - 接続の記録: initialize の clientInfo を接続（Mcp-Session-Id）ごとに DB へ記録する（/im/mcp は stateless のため）
//   - setup ツール: 接続してきた AI の種類に合わせた導入手順と、配布物の SHA-256・取得 URL（期限つきの券）を返す
//   - 導入済み通知: フック（SessionStart の looptrack hook）が POST /projects/{slug}/install で知らせる。
//     未導入・フック未承認・配布物が古い間は、MCP のツール結果に導入・更新の指示を付け続ける
//   - prompts: ループの定型（loop）と導入（setup）

const (
	agentClaudeCode = "claude-code"
	agentCodex      = "codex"
	agentCopilot    = "copilot"
	agentOther      = "other"

	mcpSessionHeader = "Mcp-Session-Id"
	maxMCPPeek       = 1 << 20 // initialize を探すために読む本文の上限
	setupTicketTTL   = time.Hour
	setupTicketKind  = "dist"
	setupNoticeMeta  = "looptrack/notice" // ツール結果に付けた導入の指示の印（_meta のキー）
)

var agentKinds = []string{agentClaudeCode, agentCodex, agentCopilot, agentOther}

// agentOf は clientInfo.name（または User-Agent）から AI の種類を判定する。
// Claude Code は "claude-code"、Codex は "codex-mcp-client"（rmcp）を名乗る。
// GitHub Copilot: VS Code は productService.nameLong（"Visual Studio Code" / "Visual Studio Code - Insiders" / "Code - OSS"。
// microsoft/vscode の src/vs/workbench/contrib/mcp/common/mcpServerRequestHandler.ts）、Copilot CLI は "github-copilot-developer"
// （github/copilot-cli の issue #432 の保守者のコメント。公式文書には無い）。Copilot CLI 1.0.86 の実物は "copilot-cli" を名乗った
// （2026-09-19 の実測。名前に copilot を含むので copilot と判定する）。VS Code は実物では未確認。
// VS Code の MCP の接続は Copilot のエージェントモード（Chat）が使うので copilot とみなす（Codex・Claude Code の VS Code 拡張は自分の名前を名乗る）。
func agentOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "claude-code") || strings.Contains(n, "claude code") || strings.Contains(n, "claude_code"):
		return agentClaudeCode
	case strings.Contains(n, "codex"):
		return agentCodex
	// GitHub Copilot: CLI は "github-copilot-developer"（copilot-cli#432）、VS Code は productService.nameLong
	// （"Visual Studio Code"・"Visual Studio Code - Insiders"・ソースからのビルドは "Code - OSS"。mcpServer.ts）。
	case strings.Contains(n, "copilot") || strings.HasPrefix(n, "visual studio code") || strings.HasPrefix(n, "code - oss") || n == "vscode":
		return agentCopilot
	}
	return agentOther
}

// agentLabel は AI の呼び名。製品の名前は訳さず、総称だけを利用者の言語で出す。
func agentLabel(lang i18n.Lang, agent string) string {
	switch agent {
	case agentClaudeCode:
		return "Claude Code"
	case agentCodex:
		return "Codex"
	case agentCopilot:
		return "GitHub Copilot"
	case agentOther:
		return i18n.T(lang, "server.mcp.setup.agent.other")
	}
	return i18n.T(lang, "server.mcp.setup.agent.unknown")
}

// setupMCPLang は setup / prompt の経路の言語。MCP のツールは http.Request を持たないので、
// SDK が渡すヘッダと利用者（分かっていれば）を langFor（lang.go）へ渡す。判定は書き分けない。
// 接続設定がヘッダを持てなくても、利用者の設定があればその言語になる。
func setupMCPLang(extra *mcp.RequestExtra, p *principal) i18n.Lang {
	explicit, accept := "", ""
	if extra != nil && extra.Header != nil {
		explicit, accept = extra.Header.Get(langHeader), extra.Header.Get("Accept-Language")
	}
	return langFor("", explicit, userLang(p), accept)
}

// needsHook は導入済みの判定にフックからの通知を求める AI か（フックの仕組みがある AI）。
// Copilot は .github/hooks/*.json の SessionStart（VS Code・Copilot CLI とも文書で確認。Copilot CLI は実物でも確認済み）で通知する。
func needsHook(agent string) bool {
	return agent == agentClaudeCode || agent == agentCodex || agent == agentCopilot
}

// ---------------------------------------------------------------- 接続の記録

// principalOfContext は MCP の認証（RequireBearerToken）が context に置いた利用者。
func principalOfContext(ctx context.Context) *principal {
	ti := auth.TokenInfoFromContext(ctx)
	if ti == nil {
		return nil
	}
	p, _ := ti.Extra["principal"].(*principal)
	return p
}

func principalOfExtra(extra *mcp.RequestExtra) *principal {
	if extra == nil || extra.TokenInfo == nil {
		return nil
	}
	p, _ := extra.TokenInfo.Extra["principal"].(*principal)
	return p
}

// initializeOf は JSON-RPC の本文（単体・バッチ）から接続の始まりを探す。
// 2026-07-28 より前のプロトコルは initialize の params から、それ以降（SEP-2575）は接続を張らずに
// server/discover を送ってくるので、その params の _meta から同じものを読む
// （新しいプロトコルのクライアントは initialize を送らないため、これを見ないと接続の記録も Mcp-Session-Id も作られない）。
func initializeOf(body []byte) (clientName, clientVersion, protocol string, ok bool) {
	type initParams struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
		Meta struct {
			ProtocolVersion string `json:"io.modelcontextprotocol/protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"io.modelcontextprotocol/clientInfo"`
		} `json:"_meta"`
	}
	type msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	var msgs []msg
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if json.Unmarshal(trimmed, &msgs) != nil {
			return
		}
	} else {
		var m msg
		if json.Unmarshal(trimmed, &m) != nil {
			return
		}
		msgs = []msg{m}
	}
	for _, m := range msgs {
		if m.Method != "initialize" && m.Method != "server/discover" {
			continue
		}
		var p initParams
		_ = json.Unmarshal(m.Params, &p)
		if m.Method == "server/discover" { // SEP-2575: clientInfo とプロトコルの版は params の _meta に載る
			return p.Meta.ClientInfo.Name, p.Meta.ClientInfo.Version, p.Meta.ProtocolVersion, true
		}
		return p.ClientInfo.Name, p.ClientInfo.Version, p.ProtocolVersion, true
	}
	return
}

// recordMCPConnections は SDK のハンドラの前で接続を記録する（認証の後に置く）。
//   - POST の initialize: 接続 ID を発行して clientInfo を記録し、応答ヘッダ Mcp-Session-Id で渡す
//     （stateless の SDK は ID を発行しない。クライアントは以後の要求にこのヘッダを付ける。SDK はヘッダを無視する）
//   - それ以外の POST: 接続の最終利用時刻を進める
//   - DELETE: 接続の終了を記録して 204（stateless の SDK は 405 を返すため、ここで受ける）
func (s *Server) recordMCPConnections(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(mcpBaseHeader, s.publicBase(r)+s.cfg.BasePath)
		p := principalOfContext(r.Context())
		if p == nil {
			next.ServeHTTP(w, r)
			return
		}
		sid := strings.TrimSpace(r.Header.Get(mcpSessionHeader))
		switch r.Method {
		case http.MethodDelete:
			if sid != "" {
				if err := store.CloseMCPConnection(r.Context(), s.db, sid, p.User.ID, s.cfg.Now()); err != nil {
					s.cfg.Logger.Warn("mcp connection close", "err", err)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		case http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPPeek+1))
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			if name, version, protocol, ok := initializeOf(body); ok && len(body) <= maxMCPPeek {
				id := make([]byte, 16)
				if _, err := rand.Read(id); err == nil {
					conn := store.MCPConnection{ID: hex.EncodeToString(id), UserID: p.User.ID, TokenID: p.TokenID, ClientName: name,
						ClientVersion: version, Agent: agentOf(name), ProtocolVersion: protocol,
						Project: strings.TrimSpace(r.Header.Get("X-Looptrack-Project")), UserAgent: r.UserAgent()}
					if err := store.InsertMCPConnection(r.Context(), s.db, conn); err != nil {
						s.cfg.Logger.Warn("mcp connection", "err", err)
					} else {
						w.Header().Set(mcpSessionHeader, conn.ID)
					}
				}
			} else if sid != "" {
				if err := store.TouchMCPConnection(r.Context(), s.db, sid, p.User.ID, s.cfg.Now()); err != nil {
					s.cfg.Logger.Warn("mcp connection touch", "err", err)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientJSON は接続してきた AI。Source は判定の根拠（request: 要求の _meta・connection: 接続時の initialize・user-agent・空は不明）。
type clientJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Agent   string `json:"agent"`
	Source  string `json:"source"`
}

// mcpClient は要求の clientInfo（新しいプロトコル）→ 接続の記録（Mcp-Session-Id）→ User-Agent の順に AI を判定する。
func (s *Server) mcpClient(ctx context.Context, extra *mcp.RequestExtra, info *mcp.Implementation, userID int64) clientJSON {
	if info != nil && info.Name != "" {
		return clientJSON{Name: info.Name, Version: info.Version, Agent: agentOf(info.Name), Source: "request"}
	}
	if extra == nil || extra.Header == nil {
		return clientJSON{}
	}
	if sid := strings.TrimSpace(extra.Header.Get(mcpSessionHeader)); sid != "" {
		c, err := store.MCPConnectionByID(ctx, s.db, sid, userID)
		// 名乗り（clientInfo）の無い接続は判定に使わない。SEP-2575 の clientInfo は任意なので、
		// 名前の無い行で User-Agent の判定を潰さないようにする（潰すと Copilot が other に倒れ、
		// 計測を有効にしていない利用者の操作まで付与の対象になる。service.UsageTarget）
		if err == nil && c.ClientName != "" {
			return clientJSON{Name: c.ClientName, Version: c.ClientVersion, Agent: c.Agent, Source: "connection"}
		}
		if !errors.Is(err, store.ErrNotFound) {
			s.cfg.Logger.Warn("mcp connection lookup", "err", err)
		}
	}
	if ua := extra.Header.Get("User-Agent"); ua != "" && agentOf(ua) != agentOther {
		return clientJSON{Name: ua, Agent: agentOf(ua), Source: "user-agent"}
	}
	return clientJSON{}
}

// ---------------------------------------------------------------- 配布物と導入状態

// distLatest は導入状態の比較に使う現在の配布物。kit は core と loop を別々に一式のハッシュ
// （bundleSHA256。.claude/.looptrack-kit.json の core / loop の bundle_sha256 と同じ計算）で比べる。CLI（looptrack）の古さは
// 通知の client.version で比べる。以前の配布スクリプトのハッシュの比較は撤去した。
type distLatest struct {
	Core, Loop string // kit/core・kit/loop 一式の bundleSHA256（その層が無ければ空）
	HasLoop    bool   // kit/loop/manifest.json がある（init --loop で入れられる）
	LoopHooks  int    // 問いの文面の本数（kit.LoopCounts。looptrack issue init の問いと同じ数え方）
	LoopRules  int
	LoopSkills int
}

func latestDist() (distLatest, error) {
	files, err := distFiles()
	if err != nil {
		return distLatest{}, err
	}
	var out distLatest
	core, loop := map[string]string{}, map[string]string{}
	bodies := map[string]string{} // kit/loop の本体（kit.LoopCounts が manifest の hook の項目を数える）
	for _, f := range files {
		switch {
		case strings.HasPrefix(f.Name, "kit/core/"):
			core[f.Name] = f.SHA256
		case strings.HasPrefix(f.Name, "kit/loop/"):
			loop[f.Name] = f.SHA256
			body := ""
			if f.Name == "kit/loop/manifest.json" {
				out.HasLoop = true
				b, err := distRead(f.Name)
				if err != nil {
					return distLatest{}, err
				}
				body = string(b)
			}
			bodies[f.Name] = body
		}
	}
	out.LoopHooks, out.LoopRules, out.LoopSkills = kit.LoopCounts(bodies)
	if len(core) > 0 {
		out.Core = bundleSHA256(core)
	}
	if len(loop) > 0 {
		out.Loop = bundleSHA256(loop)
	}
	return out, nil
}

// bundleSHA256 は「名前 ハッシュ」の行を名前順に並べたものの SHA-256。
func bundleSHA256(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s %s\n", n, files[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// installStateJSON は 1 つの AI の導入状態。State: missing（通知が無い）/ no_hook（手動の通知だけでフックから届いていない）/
// stale（配布物より古いファイルがある）/ current（最新）。
type installStateJSON struct {
	Agent      string   `json:"agent"`
	State      string   `json:"state"`
	Source     string   `json:"source,omitempty"`
	StaleFiles []string `json:"stale_files"`
	Bundle     string   `json:"bundle_sha256,omitempty"`
	ReportedAt string   `json:"reported_at,omitempty"`
	HookAt     string   `json:"hook_at,omitempty"`
	Host       string   `json:"host,omitempty"`
	Workspace  string   `json:"workspace,omitempty"`
	// SelfRepo は looptrack 自身のリポジトリ（kit の正本）からの通知。kit の新旧は比べない
	SelfRepo      bool   `json:"self_repo,omitempty"`
	Message       string `json:"message"`
	UpdateCommand string `json:"update_command,omitempty"`
	// 導入セット（DESIGN.md §5-7）。Loop: installed / declined（辞退）/ none（未選択・通知が無い）。
	// StaleKit は一式のハッシュが現在の配布物と違う層（core / loop）。loop は installed のときだけ比べる
	Loop             string   `json:"loop"`
	LoopVersion      string   `json:"loop_version,omitempty"`
	LoopBundle       string   `json:"loop_bundle_sha256,omitempty"`
	LatestLoopBundle string   `json:"latest_loop_bundle_sha256,omitempty"`
	CoreBundle       string   `json:"core_bundle_sha256,omitempty"`
	LatestCoreBundle string   `json:"latest_core_bundle_sha256,omitempty"`
	StaleKit         []string `json:"stale_kit"`
	// 実行ファイル（looptrack）の版。LatestClient は配布ディレクトリの (os, arch) の最新、
	// MinClient は LOOPTRACK_CLIENT_MIN_VERSION。古さは版で決める（stale_files に "looptrack"）。
	// 版を送らない通知は 1.0.0 より前の CLI（撤去済み）の導入で、同じく古いものとして looptrack への置き換えを求める
	ClientVersion string `json:"client_version,omitempty"`
	ClientOS      string `json:"client_os,omitempty"`
	ClientArch    string `json:"client_arch,omitempty"`
	LatestClient  string `json:"latest_client_version,omitempty"`
	MinClient     string `json:"min_client_version,omitempty"`
	// updateShell は UpdateCommand の案内に対応する、そのまま実行できるコマンド。UpdateCommand は利用者の言語の
	// 文（「… の後に … を再実行する」）なので、そこから機械で取り出すと言語ごとに壊れる。手順に出すのはこちら
	updateShell string
}

// goClientName は looptrack の古さを stale_files に出すときの名前。
const goClientName = "looptrack"

// loopStateOf は通知の loop の状態（通知が無い・loop を送らない古い CLI は none）。
func loopStateOf(inst *store.AgentInstall) string {
	if inst != nil && (inst.LoopState == "installed" || inst.LoopState == "declined") {
		return inst.LoopState
	}
	return "none"
}

// loopPhrase は導入済みの文に添える loop の状態。
func loopPhrase(lang i18n.Lang, st installStateJSON) string {
	switch st.Loop {
	case "installed":
		v := st.LoopVersion
		if v == "" && len(st.LoopBundle) >= 12 {
			v = st.LoopBundle[:12]
		}
		return i18n.T(lang, "server.mcp.setup.loop.installed", "version", orDash(v))
	case "declined":
		return i18n.T(lang, "server.mcp.setup.loop.declined")
	}
	return i18n.T(lang, "server.mcp.setup.loop.none")
}

func (s *Server) fmtTime(t time.Time) string { return t.In(s.svc.Loc).Format("2006-01-02 15:04") }

// evalInstall は通知（無ければ nil）と現在の配布物を比べる。
func (s *Server) evalInstall(lang i18n.Lang, pr store.Project, agent string, inst *store.AgentInstall, latest distLatest) installStateJSON {
	st := installStateJSON{Agent: agent, StaleFiles: []string{}, StaleKit: []string{},
		Loop: loopStateOf(inst), LatestCoreBundle: latest.Core, LatestLoopBundle: latest.Loop}
	label := agentLabel(lang, agent)
	if inst == nil {
		st.State = "missing"
		st.Message = i18n.T(lang, "server.mcp.setup.state.missing", "agent", label, "project", pr.Slug)
		return st
	}
	st.Source, st.Bundle, st.Host, st.Workspace, st.SelfRepo = inst.Source, inst.BundleSHA256, inst.Host, inst.Workspace, inst.SelfRepo
	st.ReportedAt = s.fmtTime(inst.ReportedAt)
	if inst.HookAt != nil {
		st.HookAt = s.fmtTime(*inst.HookAt)
	}
	if inst.ClientOS == "" {
		// 実行ファイルの版を送らない通知は、撤去した 1.0.0 より前の CLI の導入。その CLI は新しいサーバにつながらないので、
		// フックの有無・kit の控えに依らず、looptrack の取得 + init（hook の配線を looptrack に置き換える）を求める
		st.State = "stale"
		st.StaleFiles = append(st.StaleFiles, goClientName)
		st.UpdateCommand = i18n.T(lang, "server.mcp.setup.update.legacy_cli",
			"command", fmt.Sprintf("looptrack issue init --project %s --agent %s", pr.Slug, agent))
		st.Message = i18n.T(lang, "server.mcp.setup.state.legacy_cli", "agent", label, "at", st.ReportedAt,
			"source", orDash(inst.Source), "command", st.UpdateCommand)
		return st
	}
	// 実行ファイルの版で比べる。比べられない版（dev など）は判定しない
	var why []string // 古さの理由（文面）
	st.ClientVersion, st.ClientOS, st.ClientArch = inst.ClientVersion, inst.ClientOS, inst.ClientArch
	st.LatestClient, st.MinClient = s.latestClient(inst.ClientOS, inst.ClientArch), s.cfg.ClientMinVersion
	if st.MinClient != "" && relver.Older(inst.ClientVersion, st.MinClient) {
		why = append(why, i18n.T(lang, "server.mcp.setup.stale.below_min", "version", st.MinClient))
	}
	if st.LatestClient != "" && relver.Older(inst.ClientVersion, st.LatestClient) {
		why = append(why, i18n.T(lang, "server.mcp.setup.stale.below_latest", "version", st.LatestClient))
	}
	if len(why) > 0 {
		st.StaleFiles = append(st.StaleFiles, goClientName)
	}
	// kit は層ごとに比べる。控えの無い通知（古い CLI）・配布物に無い層は比べない。loop は入っているときだけ。
	// looptrack 自身のリポジトリ（kit の正本）からの通知は比べない: 手元のほうが新しいのは当たり前で、
	// 促す init の再実行はクライアントが拒否し、--dir で押し通すと古い配布物が正本を上書きする
	st.CoreBundle, st.LoopBundle, st.LoopVersion = inst.CoreBundle, inst.LoopBundle, inst.LoopVersion
	if !st.SelfRepo {
		if inst.CoreBundle != "" && latest.Core != "" && inst.CoreBundle != latest.Core {
			st.StaleKit = append(st.StaleKit, "core")
		}
		if st.Loop == "installed" && inst.LoopBundle != "" && latest.Loop != "" && inst.LoopBundle != latest.Loop {
			st.StaleKit = append(st.StaleKit, "loop")
		}
	}
	switch {
	case needsHook(agent) && inst.HookAt == nil:
		st.State = "no_hook"
		st.Message = i18n.T(lang, "server.mcp.setup.state.no_hook", "agent", label, "project", pr.Slug,
			"approve", approveStep(lang, agent))
	case len(st.StaleFiles) > 0 || len(st.StaleKit) > 0:
		st.State = "stale"
		st.UpdateCommand, st.updateShell, st.Message = s.goStaleMessage(lang, pr, agent, label, st, why)
	default:
		st.State = "current"
		dist := i18n.T(lang, "server.mcp.setup.state.dist_current", "version", orDash(st.ClientVersion))
		st.Message = i18n.T(lang, "server.mcp.setup.state.current", "agent", label, "dist", dist,
			"at", orDash(st.HookAt), "loop", loopPhrase(lang, st))
		if !needsHook(agent) {
			st.Message = i18n.T(lang, "server.mcp.setup.state.current_no_hook", "agent", label, "dist", dist, "at", st.ReportedAt)
		}
	}
	if st.SelfRepo {
		// 比べなかったことを黙らせない（本当に kit が古い別のプロジェクトと見分けが付かなくなる）
		st.Message = i18n.T(lang, "server.mcp.setup.state.self_repo", "message", st.Message)
	}
	return st
}

// goStaleMessage は Go 版（looptrack）の導入が古いときの更新コマンドと文面。実行ファイルは self-update で、
// kit（core / loop）の控えは init の再実行で直す（kit は実行ファイルに埋め込まれているので、self-update の後に init を回す）。
// 返すのは (案内の文, そのまま実行できるコマンド, 状態の文)。案内の文は言語で変わるので、手順に載せる
// コマンドは別に作る（文から取り出すと言語ごとに壊れる）。
func (s *Server) goStaleMessage(lang i18n.Lang, pr store.Project, agent, label string, st installStateJSON, why []string) (string, string, string) {
	initCmd := fmt.Sprintf("looptrack issue init --project %s --agent %s", pr.Slug, agent)
	const selfUpdate = "looptrack self-update"
	var cmd, shell string
	switch {
	case len(st.StaleFiles) > 0 && len(st.StaleKit) > 0:
		cmd = i18n.T(lang, "server.mcp.setup.update.self_and_init", "self_update", selfUpdate, "command", initCmd)
		shell = selfUpdate + " && " + initCmd
	case len(st.StaleFiles) > 0:
		cmd, shell = selfUpdate, selfUpdate
	default:
		cmd = i18n.T(lang, "server.mcp.setup.update.init_only", "command", initCmd)
		shell = initCmd
	}
	for _, l := range st.StaleKit {
		why = append(why, i18n.T(lang, "server.mcp.setup.stale.kit", "layer", l))
	}
	msg := i18n.T(lang, "server.mcp.setup.state.stale", "agent", label, "version", orDash(st.ClientVersion),
		"os", st.ClientOS, "arch", orDash(st.ClientArch), "why", strings.Join(why, i18n.T(lang, "server.mcp.setup.sep.reason")),
		"at", st.ReportedAt, "command", cmd)
	return cmd, shell, msg
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func approveStep(lang i18n.Lang, agent string) string {
	switch agent {
	case agentClaudeCode:
		return i18n.T(lang, "server.mcp.setup.approve.claude_code")
	case agentCodex:
		return i18n.T(lang, "server.mcp.setup.approve.codex")
	case agentCopilot:
		// VS Code は .github/hooks/*.json を既定で読む（Preview。組織の設定で無効のことがある）。Copilot CLI は信頼したフォルダだけ
		return i18n.T(lang, "server.mcp.setup.approve.copilot")
	}
	return ""
}

// installState は利用者のそのプロジェクトへの導入状態。agent が空（AI を判定できない）なら、
// 最新の導入が 1 つでもあればそれを、無ければ最も進んだものを返す。
func (s *Server) installState(ctx context.Context, lang i18n.Lang, userID int64, pr store.Project, agent string) (installStateJSON, error) {
	latest, err := latestDist()
	if err != nil {
		return installStateJSON{}, err
	}
	installs, err := store.AgentInstalls(ctx, s.db, userID, pr.ID)
	if err != nil {
		return installStateJSON{}, err
	}
	if agent != "" {
		for i := range installs {
			if installs[i].Agent == agent {
				return s.evalInstall(lang, pr, agent, &installs[i], latest), nil
			}
		}
		return s.evalInstall(lang, pr, agent, nil, latest), nil
	}
	rank := map[string]int{"current": 0, "stale": 1, "no_hook": 2, "missing": 3}
	best := s.evalInstall(lang, pr, "", nil, latest)
	for i := range installs {
		if st := s.evalInstall(lang, pr, installs[i].Agent, &installs[i], latest); rank[st.State] < rank[best.State] {
			best = st
		}
	}
	return best, nil
}

// ---------------------------------------------------------------- 導入済み通知（REST）

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

type installRequest struct {
	Agent         string            `json:"agent"`
	Trigger       string            `json:"trigger"` // hook（フックから）/ manual（looptrack issue installed）
	Source        string            `json:"source"`
	Files         map[string]string `json:"files"`
	ClientVersion string            `json:"client_version"`
	Host          string            `json:"host"`
	Workspace     string            `json:"workspace"`
	// SelfRepo は looptrack 自身のリポジトリ（kit の正本）からの通知。クライアントが判定して送る
	// （internal/client/cli の IsSelfRepo）。立っていれば kit は配布物と比べない
	SelfRepo bool `json:"self_repo"`
	// 導入セット。.claude/.looptrack-kit.json の控え。古い CLI は送らない（loop は none として扱う）
	Core *kitCoreReport `json:"core,omitempty"`
	Loop *kitLoopReport `json:"loop,omitempty"`
	// 実行ファイル。Go 版の CLI（looptrack）が送る。古さを版で判定し、files は 0 件でもよい
	// （入口を置かない導入には配布スクリプトが無い）。送らない通知は撤去した以前の CLI（files が 1 件以上）からのもので、
	// 導入状態は stale（looptrack への置き換えを求める）
	Client *clientReport `json:"client,omitempty"`
}

type clientReport struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

var clientPlatformRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

type kitCoreReport struct {
	Bundle string `json:"bundle_sha256"`
}

type kitLoopReport struct {
	Installed bool   `json:"installed"`
	Declined  bool   `json:"declined"` // declined_at がある（入っていないときだけ意味を持つ）
	Bundle    string `json:"bundle_sha256"`
	Version   string `json:"version"`
}

// apiPostInstall は POST /projects/{slug}/install。閲覧のみの利用者も送れる（閲覧だけでもフックと CLI は使う）。
func (s *Server) apiPostInstall(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var req installRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	lang := reqLang(r)
	bad := func(msg string) { writeError(w, http.StatusBadRequest, "invalid_argument", msg) }
	req.Agent = strings.ToLower(strings.TrimSpace(req.Agent))
	switch {
	case !contains(agentKinds, req.Agent):
		bad(i18n.T(lang, "server.mcp.setup.err.agent", "kinds", strings.Join(agentKinds, " / ")))
		return
	case req.Trigger != "hook" && req.Trigger != "manual":
		bad(i18n.T(lang, "server.mcp.setup.err.trigger"))
		return
	case req.Source != "" && !contains([]string{"link", "copy", "server"}, req.Source):
		bad(i18n.T(lang, "server.mcp.setup.err.source"))
		return
	case len(req.Files) > 32 || (len(req.Files) == 0 && req.Client == nil):
		bad(i18n.T(lang, "server.mcp.setup.err.files"))
		return
	}
	if c := req.Client; c != nil {
		c.Version = strings.TrimSpace(c.Version)
		switch {
		case c.Version == "" || len(c.Version) > 64:
			bad(i18n.T(lang, "server.mcp.setup.err.client_version"))
			return
		case !clientPlatformRe.MatchString(c.OS) || !clientPlatformRe.MatchString(c.Arch):
			bad(i18n.T(lang, "server.mcp.setup.err.client_platform"))
			return
		}
	}
	for n, h := range req.Files {
		if len(n) > 64 || strings.ContainsAny(n, "/\\") || !sha256Re.MatchString(h) {
			bad(i18n.T(lang, "server.mcp.setup.err.file_format", "name", n))
			return
		}
	}
	p := principalFrom(r.Context())
	inst := store.AgentInstall{UserID: p.User.ID, ProjectID: pr.ID, Agent: req.Agent, Source: req.Source, Files: req.Files,
		BundleSHA256: bundleSHA256(req.Files), ClientVersion: req.ClientVersion, Host: req.Host, Workspace: req.Workspace,
		SelfRepo: req.SelfRepo}
	if req.Files == nil {
		inst.Files = map[string]string{}
	}
	if c := req.Client; c != nil {
		inst.ClientVersion, inst.ClientOS, inst.ClientArch = c.Version, c.OS, c.Arch
	}
	if req.Core != nil {
		if req.Core.Bundle != "" && !sha256Re.MatchString(req.Core.Bundle) {
			bad(i18n.T(lang, "server.mcp.setup.err.core_bundle"))
			return
		}
		inst.CoreBundle = req.Core.Bundle
	}
	if l := req.Loop; l != nil {
		switch {
		case l.Bundle != "" && !sha256Re.MatchString(l.Bundle):
			bad(i18n.T(lang, "server.mcp.setup.err.loop_bundle"))
			return
		case len(l.Version) > 64:
			bad(i18n.T(lang, "server.mcp.setup.err.loop_version"))
			return
		case l.Installed:
			inst.LoopState, inst.LoopBundle, inst.LoopVersion = "installed", l.Bundle, l.Version
		case l.Declined:
			inst.LoopState = "declined"
		default:
			inst.LoopState = "none"
		}
	}
	if err := store.UpsertAgentInstall(r.Context(), s.db, inst, req.Trigger == "hook", s.cfg.Now()); err != nil {
		s.internalError(w, r, err)
		return
	}
	st, err := s.installState(r.Context(), lang, p.User.ID, pr, req.Agent)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// apiGetInstall は GET /projects/{slug}/install（自分の導入状態。AI の種類ごと）。
func (s *Server) apiGetInstall(w http.ResponseWriter, r *http.Request) {
	pr, _, ok := s.projectFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	p, lang := principalFrom(r.Context()), reqLang(r)
	out := []installStateJSON{}
	for _, a := range agentKinds {
		st, err := s.installState(r.Context(), lang, p.User.ID, pr, a)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, st)
	}
	latest, _ := latestDist()
	writeJSON(w, http.StatusOK, map[string]any{"project": pr.Slug, "installs": out,
		"latest_core_bundle_sha256": latest.Core, "latest_loop_bundle_sha256": latest.Loop})
}

// ---------------------------------------------------------------- 取得券（トークンの無い端末から配布物を取る）

// newSetupTicket は配布物の取得だけに使える期限つきの券を作る（LOOPTRACK_SECRET_KEY で暗号化。DB に置かない）。
// MCP の接続（OAuth）はあるが CLI のトークンがまだ無い端末が、setup の手順で配布物を取るために使う。
func (s *Server) newSetupTicket(userID int64) (string, time.Time, error) {
	if s.cfg.Box == nil {
		return "", time.Time{}, i18n.Errorf("server.setup.err.no_secret_key")
	}
	exp := s.cfg.Now().Add(setupTicketTTL)
	sealed, err := s.cfg.Box.Seal([]byte(fmt.Sprintf("%s|%d|%d", setupTicketKind, userID, exp.Unix())))
	if err != nil {
		return "", time.Time{}, err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), exp, nil
}

func (s *Server) checkSetupTicket(r *http.Request, ticket string) bool {
	if s.cfg.Box == nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil {
		return false
	}
	plain, err := s.cfg.Box.Open(raw)
	if err != nil {
		return false
	}
	parts := strings.Split(string(plain), "|")
	if len(parts) != 3 || parts[0] != setupTicketKind {
		return false
	}
	uid, err1 := strconv.ParseInt(parts[1], 10, 64)
	exp, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || s.cfg.Now().Unix() > exp {
		return false
	}
	u, err := store.UserByID(r.Context(), s.db, uid)
	return err == nil && !u.Disabled
}

func (s *Server) setupTicketError(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusForbidden, "invalid_ticket", i18n.T(reqLang(r), "server.mcp.setup.err.ticket"))
}

// setupDistList は GET /setup/{ticket}/（配布物の一覧。GET /api/v1/dist と同じ形）。
func (s *Server) setupDistList(w http.ResponseWriter, r *http.Request) {
	if !s.checkSetupTicket(r, r.PathValue("ticket")) {
		s.setupTicketError(w, r)
		return
	}
	s.apiDist(w, r)
}

// setupDistFile は GET /setup/{ticket}/{name}（本体。X-Looptrack-SHA256 にハッシュ）。
func (s *Server) setupDistFile(w http.ResponseWriter, r *http.Request) {
	if !s.checkSetupTicket(r, r.PathValue("ticket")) {
		s.setupTicketError(w, r)
		return
	}
	s.apiDistFile(w, r)
}

// ---------------------------------------------------------------- MCP: setup ツール・prompts・ツール結果への指示

type setupIn struct {
	projectArg
	Agent string `json:"agent,omitempty" jsonschema:"server.mcp.arg.setup.agent"`
	CLI   string `json:"cli,omitempty" jsonschema:"server.mcp.arg.setup.cli"`
	OS    string `json:"os,omitempty" jsonschema:"server.mcp.arg.setup.os"`
	// 導入済みかを作業ディレクトリの単位で判定する。省略時は従来どおり利用者・プロジェクト・AI の単位
	Workspace string `json:"workspace,omitempty" jsonschema:"server.mcp.arg.setup.workspace"`
	// loop の選択が要るとき、1 回目（loop なし）は問いだけを返し、利用者の答えを付けた 2 回目に答えに合うコマンドを 1 つ返す
	Loop string `json:"loop,omitempty" jsonschema:"server.mcp.arg.setup.loop"`
}

// loopAnswerOf は setup の引数 loop（利用者の答え）を yes / no / 空にそろえる。
func loopAnswerOf(lang i18n.Lang, v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return "", nil
	case "yes", "y":
		return "yes", nil
	case "no", "n":
		return "no", nil
	}
	return "", &serviceErrorText{i18n.T(lang, "server.mcp.setup.err.loop_answer")}
}

// workspaceName は setup の引数 workspace（絶対パスか名前）の最後の要素。導入済み通知の workspace
// （CLI が送る「導入先ディレクトリの名前」。looptrack は filepath.Base(EvalSymlinks(Root))）と同じ形にそろえる。「/」「\」のどちらの区切りも受ける。空・ルートだけなら空。
func workspaceName(p string) string {
	p = strings.TrimRight(strings.TrimSpace(p), `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if p == "." || p == ".." || strings.HasSuffix(p, ":") { // 「C:」のようなドライブだけ
		return ""
	}
	return p
}

type setupStepJSON struct {
	Who     string `json:"who"` // ai（AI が実行。コマンドは利用者の承認を得る）/ human（利用者が行う）
	Title   string `json:"title"`
	Command string `json:"command,omitempty"`
	// 利用者に問う手順（loop を入れるか）の問いの文面。問いだけの結果（ask: loop）の手順にだけ付く。
	// 答えごとのコマンドは返さない（答えを付けて呼び直したときに、答えに合うコマンドを 1 つだけ Command で返す）
	Text string `json:"text,omitempty"`
	// OS が分からないとき、Command は macOS・Linux（sh）、CommandWindows は Windows（PowerShell）
	CommandWindows string `json:"command_windows,omitempty"`
}

// loopQuestion は loop を入れるかの問いの文面（looptrack issue init の問いと同じ。本数は配布物の kit/loop から数える）。
func loopQuestion(lang i18n.Lang, l distLatest) string {
	return i18n.T(lang, "server.mcp.setup.loop.question", "hooks", l.LoopHooks, "rules", l.LoopRules, "skills", l.LoopSkills)
}

// needLoopAsk は loop を入れるかを利用者に問う状態か。導入状態の loop が none（未選択）で、配布物に kit/loop があり、
// loop を入れられる AI（Claude Code / Codex / Copilot）のとき（辞退・導入済み・other には問わない）。
func needLoopAsk(agent string, st installStateJSON, l distLatest) bool {
	return st.Loop == "none" && l.HasLoop && agent != agentOther
}

// loopAskStep は問いだけの結果の手順（who: human）。コマンドは持たない。
func loopAskStep(lang i18n.Lang, l distLatest) setupStepJSON {
	return setupStepJSON{Who: "human", Title: i18n.T(lang, "server.mcp.setup.step.loop_ask"), Text: loopQuestion(lang, l)}
}

// loopFlag は利用者の答えに合う init の旗。
func loopFlag(answer string) string {
	if answer == "yes" {
		return " --loop"
	}
	return " --no-loop"
}

// loopAnswerPhrase は手順の見出しに添える利用者の答え。
func loopAnswerPhrase(lang i18n.Lang, answer string) string {
	if answer == "yes" {
		return i18n.T(lang, "server.mcp.setup.loop.answer_yes")
	}
	return i18n.T(lang, "server.mcp.setup.loop.answer_no")
}

type setupFileJSON struct {
	distFileJSON
	URL string `json:"url"`
}

type setupJSON struct {
	Project   string `json:"project"`
	Agent     string `json:"agent"`
	CLI       string `json:"cli"` // 手順の CLI（looptrack）
	OS        string `json:"os,omitempty"`
	Workspace string `json:"workspace,omitempty"` // 引数 workspace の名前（判定に使ったもの）
	// Ask が "loop" なら問いだけの結果（steps は問いの 1 つ・コマンド・files・取得 URL は無い）。
	// LoopAnswer は手順に反映した利用者の答え（yes / no。問う状態でない呼び出しでは引数 loop を使わないので空）
	Ask        string           `json:"ask,omitempty"`
	LoopAnswer string           `json:"loop_answer,omitempty"`
	Client     clientJSON       `json:"client"`
	Install    installStateJSON `json:"install"`
	Steps      []setupStepJSON  `json:"steps"`
	Files      []setupFileJSON  `json:"files"`
	Binaries   []distBinaryJSON `json:"binaries"` // looptrack の実行ファイル（券の URL。配布ディレクトリが無ければ空）
	DistURL    string           `json:"dist_url"`
	ExpiresAt  string           `json:"dist_url_expires_at"`
	Text       string           `json:"text"`
}

// setupStepsFor は AI の種類と導入状態に合わせた手順。answer は loop の利用者の答え
// （yes / no。問う状態なら composeSetupFor が答えのある呼び出しでだけここへ来る）で、答えに合う loop の手順を挟む。
// 取得と init がある手順（missing・以前の CLI の導入の置き換え）では、取得 + init のコマンドに答えの旗（--loop / --no-loop）を付けた
// 1 つにし、承認 1 回で導入を終える。
// Copilot 向けは、手順のコマンドにサーバの URL とプロジェクトを環境変数で前置する（copilotEnv）。
func setupStepsFor(lang i18n.Lang, agent, slug, base, dist string, st installStateJSON, l distLatest, g *goSetup, answer string) []setupStepJSON {
	steps := setupStepsOf(lang, agent, slug, base, dist, st, l, g, answer)
	if agent != agentCopilot {
		return steps
	}
	for i := range steps {
		s := &steps[i]
		s.Command, s.CommandWindows = copilotEnv(s.Command, base, slug, g.isWindows()), copilotEnv(s.CommandWindows, base, slug, true)
	}
	return steps
}

// copilotEnv は Copilot 向けの手順のコマンドの前に、サーバの URL とプロジェクトの環境変数を置く。
// Claude Code は init が書いた .claude/settings.json の env を CLI に渡すが、Copilot（CLI・VS Code）は渡さないため、
// 前置しないと「サーバの URL がありません」になる。looptrack は LOOPTRACK_* を読む。
// && でつないだ後ろのコマンドにも効くよう、sh は export、PowerShell は $env: で置く。空のコマンドはそのまま。
func copilotEnv(cmd, base, slug string, win bool) string {
	if cmd == "" {
		return cmd
	}
	const urlVar, projVar = "LOOPTRACK_API_URL", "LOOPTRACK_PROJECT"
	if win {
		return fmt.Sprintf("$env:%s='%s'; $env:%s='%s'; %s", urlVar, base, projVar, slug, cmd)
	}
	return fmt.Sprintf("export %s=%s %s=%s && %s", urlVar, base, projVar, slug, cmd)
}

func setupStepsOf(lang i18n.Lang, agent, slug, base, dist string, st installStateJSON, l distLatest, g *goSetup, answer string) []setupStepJSON {
	steps := g.baseSteps(lang, agent, slug, base, dist, st)
	ls := g.loopStep(lang, agent, slug, base, dist, st, l, answer)
	if ls == nil {
		return steps
	}
	at := 0 // current: 最初に問う
	switch {
	case st.State == "no_hook":
		at = 1 // login の後・承認の前
	case st.State == "stale" && st.ClientOS != "":
		at = 1 // self-update の後
	case st.State != "current":
		// 取得と init の手順（steps[0]）を、答えの旗を付けた取得 + init の 1 つに置き換える
		steps[0] = g.fetchStep(lang, agent, slug, base, dist, loopFlag(answer), loopAnswerPhrase(lang, answer))
		return steps
	}
	return append(steps[:at], append([]setupStepJSON{*ls}, steps[at:]...)...)
}

func (s *Server) composeSetup(ctx context.Context, lang i18n.Lang, base string, p *principal, pr store.Project, client clientJSON, agentArg string) (setupJSON, error) {
	return s.composeSetupFor(ctx, lang, base, p, pr, client, setupIn{Agent: agentArg}, "")
}

// composeSetupFor は setup ツールの本体。in.OS で取得コマンドの形を選ぶ。ua は MCP の要求の User-Agent（OS の推測）。
func (s *Server) composeSetupFor(ctx context.Context, lang i18n.Lang, base string, p *principal, pr store.Project, client clientJSON, in setupIn, ua string) (setupJSON, error) {
	agentArg := in.Agent
	agent := strings.ToLower(strings.TrimSpace(agentArg))
	if agent == "" {
		agent = client.Agent
	}
	if agent == "" {
		agent = agentOther
	}
	if !contains(agentKinds, agent) {
		return setupJSON{}, &serviceErrorText{i18n.T(lang, "server.mcp.setup.err.agent", "kinds", strings.Join(agentKinds, " / "))}
	}
	st, err := s.installState(ctx, lang, p.User.ID, pr, agent)
	if err != nil {
		return setupJSON{}, err
	}
	latest, err := latestDist()
	if err != nil {
		return setupJSON{}, err
	}
	answer, err := loopAnswerOf(lang, in.Loop)
	if err != nil {
		return setupJSON{}, err
	}
	ws := workspaceName(in.Workspace)
	if ws != "" && st.State != "missing" && st.Workspace != "" && !strings.EqualFold(st.Workspace, ws) {
		// 導入済み通知（利用者 × プロジェクト × AI で 1 行・最後の通知）が別の作業ディレクトリからのもの。
		// この作業ディレクトリは未導入として手順を返す（loop の選択もこのディレクトリで問う）。通知に workspace が無い
		// （送らない古い CLI）ときは比べられないので従来どおり
		prev := st
		st = s.evalInstall(lang, pr, agent, nil, latest)
		st.Message = i18n.T(lang, "server.mcp.setup.state.other_workspace", "workspace", ws, "agent", agentLabel(lang, agent),
			"project", pr.Slug, "prev_workspace", prev.Workspace, "host", orDash(prev.Host), "at", prev.ReportedAt)
	}
	ask := needLoopAsk(agent, st, latest)
	if ask && answer == "" {
		// loop の選択が要るのに利用者の答えが無い。問いだけを返し、導入のコマンドは返さない（問いを飛ばせない形にする）
		if _, err := s.goSetupOf(lang, in, ua, nil); err != nil { // cli・os の値の検査だけ（手順は答えの後に作る）
			return setupJSON{}, err
		}
		return s.loopAskSetup(lang, pr, agent, client, st, latest, ws), nil
	}
	if !ask {
		answer = "" // loop の選択が済んでいる（導入済み・辞退済み）・問わない AI では、引数 loop を使わない
	}
	ticket, exp, err := s.newSetupTicket(p.User.ID)
	if err != nil {
		return setupJSON{}, err
	}
	dist := base + "/setup/" + ticket
	files, err := distFiles()
	if err != nil {
		return setupJSON{}, err
	}
	out := setupJSON{Project: pr.Slug, Agent: agent, Client: client, Install: st, DistURL: dist, ExpiresAt: s.fmtTime(exp),
		CLI: goClientName, Binaries: s.distBinaries(dist + "/"), Workspace: ws, LoopAnswer: answer}
	g, err := s.goSetupOf(lang, in, ua, out.Binaries)
	if err != nil {
		return setupJSON{}, err
	}
	out.OS = g.os
	for _, f := range files {
		out.Files = append(out.Files, setupFileJSON{distFileJSON: f, URL: dist + "/" + f.Name})
	}
	out.Steps = setupStepsFor(lang, agent, pr.Slug, base, dist, st, latest, g, answer)

	// 本文の先頭に AI への指示を置く（Copilot CLI は大きな結果をファイルに逃がし、先頭のプレビューだけを渡す。
	// MCP の instructions も既定では読まれない）。配布物の一覧は本文に出さない（構造化データの files と一覧の URL にある。
	// 取得コマンドが looptrack の SHA-256 を、init が一覧の SHA-256 で kit を確かめる）
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", i18n.T(lang, "server.mcp.setup.text.heading", "project", pr.Slug,
		"agent", agentLabel(lang, agent), "state", stateWord(lang, st.State)))
	switch {
	case answer == "yes":
		b.WriteString(i18n.T(lang, "server.mcp.setup.text.instruction_yes") + "\n")
	case answer == "no":
		b.WriteString(i18n.T(lang, "server.mcp.setup.text.instruction_no") + "\n")
	case st.State != "current":
		b.WriteString(i18n.T(lang, "server.mcp.setup.text.instruction") + "\n")
	}
	b.WriteString("\n")
	writeSetupClient(&b, lang, client, agent)
	fmt.Fprintf(&b, "%s\n", i18n.T(lang, "server.mcp.setup.text.state", "message", st.Message))
	if g.missing != "" {
		fmt.Fprintf(&b, "%s\n", i18n.T(lang, "server.mcp.setup.text.cli_missing", "reason", g.missing))
	} else {
		fmt.Fprintf(&b, "%s\n", i18n.T(lang, "server.mcp.setup.text.cli", "place", g.placeText()))
	}
	if agent != agentOther && st.State != "current" && st.State != "missing" {
		fmt.Fprintf(&b, "%s\n", loopPhrase(lang, st))
	}
	if in.Loop != "" && answer == "" {
		fmt.Fprintf(&b, "%s\n", i18n.T(lang, "server.mcp.setup.text.loop_arg_unused", "value", strings.TrimSpace(in.Loop)))
	}
	fmt.Fprintf(&b, "\n## %s\n\n", i18n.T(lang, "server.mcp.setup.text.steps_heading"))
	for i, step := range out.Steps {
		who := "AI"
		if step.Who == "human" {
			who = i18n.T(lang, "server.mcp.setup.text.who_human")
		}
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, who, step.Title)
		if step.Text != "" {
			fmt.Fprintf(&b, "   > %s\n", step.Text)
		}
		if step.Command != "" && step.CommandWindows != "" {
			fmt.Fprintf(&b, "   - macOS / Linux:\n     ```sh\n     %s\n     ```\n   - Windows（PowerShell）:\n     ```powershell\n     %s\n     ```\n", step.Command, step.CommandWindows)
		} else if step.Command != "" {
			fmt.Fprintf(&b, "   ```%s\n   %s\n   ```\n", g.fence(), step.Command)
		}
		// 取得の手順を出せない（looptrack を配っていない）ときは、init のコマンドが利用者の手順の見出しにある
		if answer == "yes" && strings.Contains(step.Command+" "+step.Title, " --loop") {
			fmt.Fprintf(&b, "   %s\n", i18n.T(lang, "server.mcp.setup.text.after_loop_install"))
		}
	}
	if st.Loop == "declined" && agent != agentOther {
		cmd := goInitCommand("looptrack", agent, pr.Slug, base, dist)
		if agent == agentCopilot {
			cmd = copilotEnv(cmd, base, pr.Slug, g.isWindows()) // Copilot には環境変数を付けて渡す
		}
		fmt.Fprintf(&b, "\n%s\n", i18n.T(lang, "server.mcp.setup.text.loop_declined", "command", cmd))
	}
	fmt.Fprintf(&b, "\n## %s\n\n", i18n.T(lang, "server.mcp.setup.text.dist_heading", "expires", out.ExpiresAt))
	fmt.Fprintf(&b, "- %s\n", i18n.T(lang, "server.mcp.setup.text.dist_note",
		"files", len(out.Files), "binaries", len(out.Binaries), "url", dist))
	out.Text = strings.TrimRight(b.String(), "\n")
	return out, nil
}

// writeSetupClient は setup の本文の「接続してきた AI」の行。
func writeSetupClient(b *strings.Builder, lang i18n.Lang, client clientJSON, agent string) {
	if client.Source == "" {
		fmt.Fprintf(b, "%s\n", i18n.T(lang, "server.mcp.setup.client.unknown", "agent", agent))
		return
	}
	src := "User-Agent"
	switch client.Source {
	case "request":
		src = i18n.T(lang, "server.mcp.setup.client.src_request")
	case "connection":
		src = i18n.T(lang, "server.mcp.setup.client.src_connection")
	}
	fmt.Fprintf(b, "%s\n", i18n.T(lang, "server.mcp.setup.client.detected",
		"name", client.Name, "version", client.Version, "source", src, "agent", client.Agent))
}

// loopAskSetup は loop の問いだけの setup の結果。導入のコマンド・配布物・取得 URL（券）を返さない。
// AI は問いを利用者にそのまま示し、答えを得てから setup を loop=yes / no で呼び直す（そのときに答えに合うコマンドが 1 つ返る）。
// 本文の先頭（Copilot CLI のプレビューの 500 文字）に、指示と問いの全文を置く。
func (s *Server) loopAskSetup(lang i18n.Lang, pr store.Project, agent string, client clientJSON, st installStateJSON, l distLatest, ws string) setupJSON {
	step := loopAskStep(lang, l)
	out := setupJSON{Project: pr.Slug, Agent: agent, CLI: goClientName, Workspace: ws, Ask: "loop", Client: client, Install: st,
		Steps: []setupStepJSON{step}, Files: []setupFileJSON{}, Binaries: []distBinaryJSON{}}
	again := i18n.T(lang, "server.mcp.setup.ask.again")
	if ws != "" {
		again = i18n.T(lang, "server.mcp.setup.ask.again_workspace")
	}
	// 先頭 500 文字（Copilot CLI のプレビュー）に収まるよう、指示の前半 → 問いの全文 → 呼び直し方、の順に置く
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", i18n.T(lang, "server.mcp.setup.ask.heading", "project", pr.Slug,
		"agent", agentLabel(lang, agent), "state", stateWord(lang, st.State)))
	fmt.Fprintf(&b, "%s\n\n", i18n.T(lang, "server.mcp.setup.ask.instruction", "question", step.Text, "again", again))
	writeSetupClient(&b, lang, client, agent)
	if st.State == "missing" {
		fmt.Fprintf(&b, "%s\n", i18n.T(lang, "server.mcp.setup.text.state", "message", st.Message))
	} else {
		// 更新が必要・フックの承認待ちの文は更新のコマンドを含むので、問いの段階では出さない（答えの後の結果に出る）
		fmt.Fprintf(&b, "%s\n", i18n.T(lang, "server.mcp.setup.ask.state_detail", "state", stateWord(lang, st.State)))
	}
	b.WriteString("\n" + i18n.T(lang, "server.mcp.setup.ask.no_command"))
	out.Text = b.String()
	return out
}

// stateWord は setup の本文の見出しに付ける導入状態の短い語。
func stateWord(lang i18n.Lang, state string) string {
	switch state {
	case "missing":
		return i18n.T(lang, "server.mcp.setup.word.missing")
	case "no_hook":
		return i18n.T(lang, "server.mcp.setup.word.no_hook")
	case "stale":
		return i18n.T(lang, "server.mcp.setup.word.stale")
	case "current":
		return i18n.T(lang, "server.mcp.setup.word.current")
	}
	return state
}

// serviceErrorText はツールの利用者向けエラー（内部エラーとして伏せない）。
type serviceErrorText struct{ msg string }

func (e *serviceErrorText) Error() string { return e.msg }

// mcpBaseHeader は recordMCPConnections が要求に付ける、外から見たサーバの URL（例 https://example.com/looptrack）。
// ツールの処理は http.Request を持たないため、ヘッダで渡す（クライアントが送った同名のヘッダは上書きする）。
const mcpBaseHeader = "X-Looptrack-Internal-Base"

func baseOf(extra *mcp.RequestExtra) string {
	if extra == nil || extra.Header == nil {
		return ""
	}
	return extra.Header.Get(mcpBaseHeader)
}

const loopPromptText = `イシュー管理（looptrack）のループを 1 周回してください%s。

0. このセッションでまだなら guide ツールを 1 回呼び、共通規則・このプロジェクトのルール・運用文書を読む（ツール結果に、導入が未完了であることを知らせる注記（setup ツールを呼ぶよう促す別の行。文面は利用者の言語で変わる）が付いていれば、先に setup ツールの手順を利用者に提案する）
0'. project_summary の「人の判断待ち」「外からの反応」に項目があり、利用者がこの会話にいるなら、先に prompt「review」の手順を行う（いなければ飛ばす）
1. next ツールで着手する（自分が着手中のものがあればそれが返る）。「着手可能なイシューはありません」なら止まって利用者に相談する
2. 返った本文・受け入れ条件・関連に沿って作業する。原因・判断・方針が分かった時点で add_comment に記録する（会話だけで進めない。不具合は見つけた時点で create_issue。利用者から参加者・テスターの反応を聞いたら、該当するイシューに先頭「フィードバック: 」で add_comment）
3. 受け入れ条件を 1 つずつ検証する（テスト・実行結果で確かめる）。満たせない条件があれば、閉じずに止まって相談する
4. set_status で Done にし、comment に検証結果（条件ごとの結果と確かめた方法）を書く。プロジェクト別ルールで拒否されたらメッセージの指示に従う。利用者の判断が要るもの（仕様の解釈・見た目・方針）は Done にせず In Review にし、comment に判断してほしい点を書く（人の判断待ち）。結果の末尾に、要件の検証と close を促す行（looptrack issue show <要件ID> と close のコマンドを含む。文面ではなくこのコマンドで判断する）が付いたら、次へ進む前にその要件の受け入れ条件を get_issue で確かめ、満たしていれば set_status で Done にする（comment に検証結果）
5. 利用者が続けるよう指示していれば次の next へ。そうでなければ、この周の結果（イシュー ID・変更・検証）を報告して止まる`

// loopIteratePromptText は loop（ループエンジニアリング一式）が入っている作業環境の prompt「loop」（DESIGN.md §5-7）。
// skill /iterate（kit/loop）の手順: next → 実装 → ゲート → close → next。0・0' は最小ループと同じ。
const loopIteratePromptText = `イシュー管理（looptrack）のループを 1 周回してください%s。この作業環境にはループエンジニアリング一式（loop）が入っているので、skill /iterate の手順で回す。

0. このセッションでまだなら guide ツールを 1 回呼び、共通規則・このプロジェクトのルール・運用文書を読む（ツール結果に、導入が未完了であることを知らせる注記（setup ツールを呼ぶよう促す別の行。文面は利用者の言語で変わる）が付いていれば、先に setup ツールの手順を利用者に提案する）
0'. project_summary の「人の判断待ち」「外からの反応」に項目があり、利用者がこの会話にいるなら、先に prompt「review」の手順を行う（いなければ飛ばす）
1. 未解決の bug の確認: list_issues（type: bug）で未クローズの bug を見る。bug を抱えたまま新しい実装を積み上げず、直せるものから直す（next も bug を先に選ぶ）
2. next ツールで着手する（自分が着手中のものがあればそれが返る）。「着手可能なイシューはありません」なら止まって利用者に相談する。対象の要件・設計を読んでから実装する
3. 実装し、受け入れ条件からテストを書く。手元のシェルでゲート（looptrack gates）を回す。失敗は問題 1 件ごとに create_issue（type: bug）で起票し、直して同じ問題を検出するテストを足してから回し直す。原因・判断・方針が分かった時点で add_comment に記録する（利用者から参加者・テスターの反応を聞いたら、該当するイシューに先頭「フィードバック: 」で add_comment）
4. 受け入れ条件を 1 つずつ検証する（ゲートが全段グリーン。この周で見つけた問題が起票済みで、直したものは close 済み）。満たせない条件があれば、閉じずに止まって相談する
5. set_status で Done にし、comment にゲートの結果と受け入れ条件ごとの結果を書く。プロジェクト別ルールで拒否されたらメッセージの指示に従う。利用者の判断が要るもの（仕様の解釈・見た目・方針）は Done にせず In Review にし、comment に判断してほしい点を書く（人の判断待ち）。結果の末尾に、要件の検証と close を促す行（looptrack issue show <要件ID> と close のコマンドを含む。文面ではなくこのコマンドで判断する）が付いたら、次へ進む前にその要件の受け入れ条件を get_issue で確かめ、満たしていれば set_status で Done にする（comment に検証結果）
6. 1 に戻って次のイシューへ進んでよい。止めて利用者に諮るのは、親イシューの範囲を超える・設計の変更が要る・要件の解釈が割れる・同じ bug の修正が 3 回失敗したとき。止めるときは、この周の結果（イシュー ID・変更・検証）を報告する`

const setupPromptText = `イシュー管理（looptrack）をこの作業環境に導入してください%s。

1. setup ツールを呼ぶ。引数 workspace に作業ディレクトリの git のルート（git でなければ作業ディレクトリ）の絶対パスを渡す（接続してきた AI とこの作業ディレクトリに合わせた手順・配布物の SHA-256・取得 URL が返る）
2. 結果が loop（ループエンジニアリング一式）を入れるかの問いだけなら（コマンドは返らない）、その問いを利用者にそのまま示して答え（入れる / 入れない）を得る。
   AI が答えを決めない。答えを得たら setup ツールを引数 loop（yes / no）と同じ workspace を付けて呼び直す（答えに合うコマンドが 1 つだけ返る）
3. 返った手順のうち [AI] のものは、コマンドの内容を利用者に示して承認を得てから、プロジェクトのルートで実行する。SHA-256 が一致しなければ止まる
4. [利用者] の手順（トークンの登録・再起動とフックの承認）は利用者に依頼する。トークンは AI が扱わない
5. 利用者が再起動したら setup ツールをもう一度呼び、「導入済み」になったことを確かめる。続けて guide → next（prompt「loop」）`

// loopPromptTextEN・loopIteratePromptTextEN・setupPromptTextEN は prompt の英語版（日本語の定数が正本。
// 項目の並びと数をそろえる。instructions と同じく、言語ごとの *mcp.Server にその言語で登録する）。
const loopPromptTextEN = `Run one round of the issue management (looptrack) loop%s.

0. If you have not done so in this session, call the guide tool once and read the common rules, this project's rules and its operating document (if the tool result carries a note that the installation is incomplete (a separate line urging you to call the setup tool; the wording changes with the user's language), first offer the user the steps of the setup tool)
0'. If project_summary lists items under "waiting for a human decision" or "feedback from outside" and the user is in this conversation, follow the prompt "review" first (skip it when they are not)
1. Start with the next tool (if you already have an issue in progress, that one comes back). If it says there is no issue ready to start, stop and consult the user
2. Work along the body, the acceptance criteria and the related issues that came back. Record the cause, a decision or the approach with add_comment as soon as you know it (do not move on in the conversation alone. File a defect with create_issue the moment you find it. When the user tells you how participants or testers reacted, add_comment on the issue in question starting with "Feedback: ")
3. Verify the acceptance criteria one by one (confirm them with tests and real output). If a criterion cannot be met, do not close; stop and consult the user
4. Set it to Done with set_status and write the verification result in comment (the result per criterion and how you confirmed it). When a per-project rule rejects it, do what the message tells you. When the user has to decide something (how to read the specification, how it looks, the direction), do not set Done: set In Review and write in comment what they need to decide (waiting on a human decision). When the result ends with a line urging you to verify and close a requirement (it holds looptrack issue show <requirement-ID> and the close command; decide from that command, not from the wording), check that requirement's acceptance criteria with get_issue before moving on, and set it to Done with set_status when they are met (with the verification result in comment)
5. If the user has told you to keep going, go on to the next next. Otherwise report the result of this round (issue IDs, changes, verification) and stop`

const loopIteratePromptTextEN = `Run one round of the issue management (looptrack) loop%s. This working environment has the loop engineering set (loop) installed, so run it with the steps of the skill /iterate.

0. If you have not done so in this session, call the guide tool once and read the common rules, this project's rules and its operating document (if the tool result carries a note that the installation is incomplete (a separate line urging you to call the setup tool; the wording changes with the user's language), first offer the user the steps of the setup tool)
0'. If project_summary lists items under "waiting for a human decision" or "feedback from outside" and the user is in this conversation, follow the prompt "review" first (skip it when they are not)
1. Check the open bugs: look at the open bugs with list_issues (type: bug). Do not pile new implementation on top of open bugs; fix what you can first (next also picks bugs first)
2. Start with the next tool (if you already have an issue in progress, that one comes back). If it says there is no issue ready to start, stop and consult the user. Read the requirement and the design in question before you implement
3. Implement, and write tests from the acceptance criteria. Run the gates (looptrack gates) in your local shell. File every failure with create_issue (type: bug), one issue per problem; fix it, add a test that catches the same problem, and run the gates again. Record the cause, a decision or the approach with add_comment as soon as you know it (when the user tells you how participants or testers reacted, add_comment on the issue in question starting with "Feedback: ")
4. Verify the acceptance criteria one by one (every gate green; the problems found in this round filed, and the ones you fixed closed). If a criterion cannot be met, do not close; stop and consult the user
5. Set it to Done with set_status and write the gate results and the result per acceptance criterion in comment. When a per-project rule rejects it, do what the message tells you. When the user has to decide something (how to read the specification, how it looks, the direction), do not set Done: set In Review and write in comment what they need to decide (waiting on a human decision). When the result ends with a line urging you to verify and close a requirement (it holds looptrack issue show <requirement-ID> and the close command; decide from that command, not from the wording), check that requirement's acceptance criteria with get_issue before moving on, and set it to Done with set_status when they are met (with the verification result in comment)
6. You may go back to 1 and move on to the next issue. Stop and put it to the user only when the work goes beyond the parent issue, the design has to change, the requirement can be read more than one way, or the fix for the same bug has failed three times. When you stop, report the result of this round (issue IDs, changes, verification)`

const setupPromptTextEN = `Install issue management (looptrack) in this working environment%s.

1. Call the setup tool. Pass the absolute path of the git root of your working directory (or the working directory itself when it is not a git repository) in the workspace argument (it returns the steps for the AI that connected and for this working directory, the SHA-256 of the files and the URL to fetch them from)
2. If the result is nothing but the question of whether to install the loop engineering set (loop) (no command comes back), show the user that question as it is and get their answer (install it / do not).
   The AI does not decide the answer. Once you have it, call the setup tool again with the loop argument (yes / no) and the same workspace (exactly one command that matches the answer comes back)
3. For the steps marked [AI], show the user what the command does, get their approval, and run it at the root of the project. Stop if the SHA-256 does not match
4. Ask the user to do the steps marked [user] (registering the token, restarting and approving the hooks). The AI never handles the token
5. Once the user has restarted, call the setup tool again and confirm that it reports the installation as complete. Then guide → next (the prompt "loop")`

// promptText は prompt の本文を lang で選ぶ（日本語のときだけ日本語の正本、それ以外は英語）。
func promptText(lang i18n.Lang, ja, en string) string {
	if lang == i18n.JA {
		return ja
	}
	return en
}

// projectSuffix は prompt の本文の先頭の文に足す、対象のプロジェクトの指定（slug が無ければ空）。
func projectSuffix(lang i18n.Lang, slug string) string {
	if slug == "" {
		return ""
	}
	return i18n.T(lang, "server.mcp.prompt.project_suffix", "slug", slug)
}

// setupProjectError は setup の対象のプロジェクトが引けないときのエラー。「見つかりません」には次の手を足す:
// 管理者なら create_project で作れること（prefix と width は後から変えられないので、値を利用者に確かめてから）、
// 管理者でなければ管理者に作ってもらう（か参加させてもらう）こと。管理者は全プロジェクトを引けるので、
// 管理者に NotFound が返るのはプロジェクトが無いときだけ。
func (s *Server) setupProjectError(c *mcpCall, slug string, err error) error {
	var se *service.Error
	if !errors.As(err, &se) || se.Kind != service.NotFound {
		return s.toolError(c.lang, "setup", err)
	}
	hint := i18n.T(c.lang, "server.mcp.setup.project_missing_member", "slug", slug)
	if c.p.User.Role == "admin" {
		d := service.NewProjectDefaults(store.Project{Slug: slug}) // 省略したときに使われる値（create_project と同じ既定）
		hint = i18n.T(c.lang, "server.mcp.setup.project_missing_admin", "slug", d.Slug, "prefix", d.Prefix, "width", d.Width)
	}
	return errors.New(i18n.Text(c.lang, se) + "\n" + hint)
}

func (s *Server) addSetupMCP(srv *mcp.Server, lang i18n.Lang) {
	addTool(srv, lang, &mcp.Tool{Name: "setup",
		Description: i18n.T(lang, "server.mcp.tool.setup"),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, req *mcp.CallToolRequest, in setupIn) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "setup", err)
			}
			pr, _, err := s.resolveProject(ctx, c.lang, c.p.User, slug)
			if err != nil {
				return nil, nil, s.setupProjectError(c, slug, err)
			}
			client := s.mcpClient(ctx, req.GetExtra(), req.ClientInfo(), c.p.User.ID)
			ua := ""
			if ex := req.GetExtra(); ex != nil && ex.Header != nil {
				ua = ex.Header.Get("User-Agent")
			}
			out, err := s.composeSetupFor(ctx, setupMCPLang(req.GetExtra(), c.p), baseOf(req.GetExtra()), c.p, pr, client, in, ua)
			if err != nil {
				var te *serviceErrorText
				if errors.As(err, &te) {
					return nil, nil, te
				}
				return nil, nil, s.toolError(c.lang, "setup", err)
			}
			return result(out.Text, out), nil, nil
		})

	projectArgDef := []*mcp.PromptArgument{{Name: "project", Title: i18n.T(lang, "server.mcp.prompt.arg.project.title"),
		Description: i18n.T(lang, "server.mcp.prompt.arg.project.description")}}
	prompt := func(text string) mcp.PromptHandler {
		return func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			if principalOfExtra(req.GetExtra()) == nil {
				return nil, errors.New(i18n.T(lang, "server.mcp.err.auth_required"))
			}
			slug := ""
			if req.Params != nil {
				slug = strings.TrimSpace(req.Params.Arguments["project"])
			}
			if slug == "" && req.GetExtra() != nil && req.GetExtra().Header != nil {
				slug = strings.TrimSpace(req.GetExtra().Header.Get("X-Looptrack-Project"))
			}
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: fmt.Sprintf(text, projectSuffix(lang, slug))}}}}, nil
		}
	}
	srv.AddPrompt(&mcp.Prompt{Name: "loop", Title: i18n.T(lang, "server.mcp.prompt.loop.title"),
		Description: i18n.T(lang, "server.mcp.prompt.loop.description"),
		Arguments:   projectArgDef}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		text := promptText(lang, loopPromptText, loopPromptTextEN)
		if s.promptLoopState(ctx, req) == "installed" {
			text = promptText(lang, loopIteratePromptText, loopIteratePromptTextEN)
		}
		return prompt(text)(ctx, req)
	})
	srv.AddPrompt(&mcp.Prompt{Name: "review", Title: i18n.T(lang, "server.mcp.prompt.review.title"),
		Description: i18n.T(lang, "server.mcp.prompt.review.description"), Arguments: projectArgDef},
		prompt(promptText(lang, reviewPromptText, reviewPromptTextEN)))
	srv.AddPrompt(&mcp.Prompt{Name: "setup", Title: i18n.T(lang, "server.mcp.prompt.setup.title"),
		Description: i18n.T(lang, "server.mcp.prompt.setup.description"), Arguments: projectArgDef},
		prompt(promptText(lang, setupPromptText, setupPromptTextEN)))

	srv.AddReceivingMiddleware(s.setupNoticeMiddleware)
}

// promptLoopState は prompt を求めた利用者・プロジェクト・AI の loop の状態（installed / declined / none）。
// プロジェクトは引数 → X-Looptrack-Project → 唯一のプロジェクト。決まらない・引けないときは none（最小ループ）。
func (s *Server) promptLoopState(ctx context.Context, req *mcp.GetPromptRequest) string {
	extra := req.GetExtra()
	p := principalOfExtra(extra)
	if p == nil {
		return "none"
	}
	c := &mcpCall{p: p, lang: setupMCPLang(extra, p)}
	if extra.Header != nil {
		c.project = strings.TrimSpace(extra.Header.Get("X-Looptrack-Project"))
	}
	arg := ""
	if req.Params != nil {
		arg = strings.TrimSpace(req.Params.Arguments["project"])
	}
	slug, err := s.projectSlug(ctx, c, arg)
	if err != nil {
		return "none"
	}
	pr, _, err := s.resolveProject(ctx, c.lang, p.User, slug)
	if err != nil {
		return "none"
	}
	client := s.mcpClient(ctx, extra, req.ClientInfo(), p.User.ID)
	st, err := s.loopState(ctx, p.User.ID, pr, client.Agent)
	if err != nil {
		s.cfg.Logger.Warn("prompt loop state", "err", err)
		return "none"
	}
	return st
}

// loopState は利用者のそのプロジェクトへの導入の loop の状態。agent が空（AI を判定できない）なら、
// どれかの AI の導入に loop が入っていれば installed（guide は CLI・REST・MCP で同じ Markdown を返すため agent を使わない）。
func (s *Server) loopState(ctx context.Context, userID int64, pr store.Project, agent string) (string, error) {
	installs, err := store.AgentInstalls(ctx, s.db, userID, pr.ID)
	if err != nil {
		return "", err
	}
	best := "none"
	for i := range installs {
		st := loopStateOf(&installs[i])
		if agent != "" && installs[i].Agent == agent {
			return st, nil
		}
		if agent == "" && (st == "installed" || (st == "declined" && best == "none")) {
			best = st
		}
	}
	return best, nil
}

// setupNoticeMiddleware は tools/call の結果に、未導入・フック未承認・配布物が古いときの指示を付ける（setup ツール自身には付けない）。
func (s *Server) setupNoticeMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || method != "tools/call" {
			return res, err
		}
		call, ok1 := req.(*mcp.CallToolRequest)
		out, ok2 := res.(*mcp.CallToolResult)
		if !ok1 || !ok2 || out == nil || call.Params == nil || call.Params.Name == "setup" {
			return res, err
		}
		if notice := s.setupNotice(ctx, call); notice != "" {
			out.Content = append(out.Content, &mcp.TextContent{Text: notice, Meta: mcp.Meta{setupNoticeMeta: "setup"}})
		}
		return res, err
	}
}

// setupNotice は、その呼び出しの利用者・プロジェクト・AI の導入状態が最新でなければ指示の文を返す。
// プロジェクトを決められない呼び出し（複数プロジェクトで project 未指定）には付けない。
func (s *Server) setupNotice(ctx context.Context, call *mcp.CallToolRequest) string {
	c, err := mcpCallOf(call)
	if err != nil {
		return ""
	}
	var args struct {
		Project string `json:"project"`
	}
	if len(call.Params.Arguments) > 0 {
		_ = json.Unmarshal(call.Params.Arguments, &args)
	}
	slug, err := s.projectSlug(ctx, c, args.Project)
	if err != nil {
		return ""
	}
	pr, _, err := s.resolveProject(ctx, c.lang, c.p.User, slug)
	if err != nil {
		return ""
	}
	client := s.mcpClient(ctx, call.GetExtra(), call.ClientInfo(), c.p.User.ID)
	st, err := s.installState(ctx, setupMCPLang(call.GetExtra(), c.p), c.p.User.ID, pr, client.Agent)
	if err != nil {
		s.cfg.Logger.Warn("setup notice", "err", err)
		return ""
	}
	if st.State == "current" {
		return ""
	}
	return st.Message
}
