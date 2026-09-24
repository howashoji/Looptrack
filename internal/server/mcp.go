package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/domain"
	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// リモート MCP（/im/mcp）。ツールは REST と同じ internal/service を呼ぶ
// （プロジェクト別ルール・楽観ロック・イベント記録が同じ）。変更は via=mcp で記録する。

// instructions は MCP の接続に返す案内（AI が読む）。日英の 2 本を持ち、接続ごとに選ぶ
// （mcp.ServerOptions.Instructions は mcp.NewServer のときに決まるので、要求ごとには差し替えられない。
// 言語ごとに *mcp.Server を作り、mcpHandler の getServer が要求の言語で選ぶ）。

const mcpInstructionsJA = `Looptrack のイシュー管理。**最初に setup ツールを 1 回呼ぶ**。引数 workspace に作業ディレクトリの git のルート（git でなければ作業ディレクトリ）の絶対パスを渡す（その作業ディレクトリに導入済みかで判定する。省略すると別のディレクトリの導入を「導入済み」と返すことがある）（この AI の作業環境へのフック・CLI・案内文の導入状態を返す。未導入・古いときは、接続してきた AI に合わせた手順と配布物の SHA-256 を返すので、コマンドの内容を利用者に示し承認を得て実行する。導入済みなら「導入済み」とだけ返る）。
setup の結果がループエンジニアリング一式（loop）を入れるかの問いだけのとき（コマンドは返らない）は、AI が勝手に決めず、その問いの文面を利用者にそのまま示して答え（入れる / 入れない）を得てから、setup を引数 loop（yes / no）と同じ workspace を付けて呼び直す。返った答えに合うコマンド（取得と init が要るときは取得 + init の 1 つ）を、承認を得て実行する。
次に **guide ツールを 1 回呼び**、共通規則・このプロジェクトのルール・運用文書を読んでから作業する。
ツール結果に、導入が未完了（導入済み通知が届いていない）か配布物が古いことを知らせる注記（ツールの結果とは別の行で付き、setup ツールか更新のコマンドを示す。文面は利用者の言語で変わる）が付いている間は、作業の区切りで利用者にその対応（setup の手順）を提案する。
作業はイシューから始める。ループ運用の 1 周は next（着手: 自分が着手中のもの、無ければ着手可能な最上位を In Progress にして本文・受け入れ条件・関連を返す）→ 作業 → 原因・判断が分かった時点で add_comment → 受け入れ条件を検証 → set_status で Done（comment に検証結果）→ 次の next。set_status の結果の末尾に、要件の検証と close を促す行（looptrack issue show <要件ID> と close のコマンドを含む。文面ではなくこのコマンドで判断する）が付いたら、次の next の前にその要件の受け入れ条件を検証して Done にする。定型は prompt「loop」（導入は prompt「setup」）。
In Review（人の判断待ち）や未応答のフィードバック（コメント先頭が「フィードバック:」）があれば、次の next の前に利用者に示し、返答を先頭「判断:」「差し戻し:」のコメントに残して Done か Todo へ動かす（prompt「review」）。利用者から聞いた参加者・テスターの反応は先頭「フィードバック:」でコメントに残す。
コメントは追記のみ（書き換え・削除はできない）。クローズ済み（Done / Canceled）の本文・項目は編集できない（蒸し返しは新規起票して参照）。
状態は set_status で変える（プロジェクト別ルールで拒否されたらメッセージの指示に従う）。本文の編集は get_issue で version と全文を取り、update_issue に version と直した全文を渡す。
担当者: 他の利用者が担当のイシューは In Progress にしない・本文を直さない（サーバが 422 で拒否する。依頼や確認は add_comment）。引き継ぐとき・担当者に代わって本文を直すときは、利用者に確認してから override_reason を付ける（set_status の In Progress と assign_issue は担当が自分に替わる。update_issue は担当を替えずに通り、理由が記録に残る）。next は自分が担当か未設定のイシューだけを取る。
project を省略すると、接続設定のヘッダ X-Looptrack-Project のプロジェクトを使う。
変更の結果に looptrack issue usage attach <ID> が示されたら、そのコマンドをシェルで実行する（next で着手したときも同じ）。目印は文面ではなくこのコマンド（サーバの文面は利用者の言語で変わるので、特定の言い回しで探さない）。
トークン計測の確認・集計: issue_usage（イシューの段階別）、usage_missing（トークン情報が付いていない自分の AI 操作と充足率）、usage_report（期間の集計。request_id で依頼の期間）、list_usage_ledger / add_usage_ledger（レポートの台帳。登録は取り消せない）、list_usage_requests（画面から登録されたレポートの作成依頼）。
project_summary に作成依頼が出たら、skill token-report（.claude/skills/token-report）の手順でレポートを作り台帳に登録する。`

const mcpInstructionsEN = `Looptrack issue management. **Call the setup tool once, first.** Pass the absolute path of the git root of your working directory (or the working directory itself when it is not a git repository) in the workspace argument (the judgment is made for that working directory; leave it out and it may report another directory's installation as "already installed"). (It returns how far the hooks, the CLI and the guide text are installed in this AI's environment. When nothing is installed, or it is out of date, it returns the steps for the AI that connected and the SHA-256 of the files to install, so show the user what the command does, get their approval, and run it. When everything is installed it only says so.)
When the result of setup is nothing but the question of whether to install the loop engineering set (loop) and no command comes back, the AI does not decide by itself: show the user the wording of that question as it is, get their answer (install it / do not), then call setup again with the loop argument (yes / no) and the same workspace. Run the command that matches the answer that comes back (when both fetching and init are needed, that is a single fetch + init) once you have their approval.
Then **call the guide tool once** and read the common rules, this project's rules and its operating document before you work.
While a tool result carries a note that the installation is incomplete (the installed-notification has not arrived) or that the files are out of date (it comes on a line of its own, separate from the tool's result, and names the setup tool or an update command; the wording changes with the user's language), offer the user the steps to deal with it (the setup procedure) at a break in the work.
Work starts from an issue. One round of the loop is: next (start: the issue you already have in progress, or else the highest-ranked issue that is ready to start, moved to In Progress and returned with its body, acceptance criteria and related issues) → work → add_comment as soon as you know the cause or the decision → verify the acceptance criteria → set_status to Done (with the verification result in comment) → the next next. When the result of set_status ends with a line urging you to verify and close a requirement (it holds looptrack issue show <requirement-ID> and the close command; decide from that command, not from the wording), verify that requirement's acceptance criteria and set it to Done before the next next. The prompt "loop" carries the routine (the prompt "setup" carries the installation).
When there are issues In Review (waiting on a human decision) or unanswered feedback (a comment starting with "Feedback:"), put them to the user before the next next, record the answer as a comment starting with "Decision:" or "Changes requested:", and move the issue to Done or Todo (the prompt "review"). Reactions you hear from participants or testers go into a comment starting with "Feedback:".
Comments are append-only (they cannot be rewritten or deleted). The body and the fields of a closed issue (Done / Canceled) cannot be edited (to reopen the subject, file a new issue and refer to it).
Change the status with set_status (when a per-project rule rejects it, do what the message tells you). To edit a body, take the version and the full text with get_issue and send the version and the corrected full text to update_issue.
Assignees: never set an issue assigned to another user to In Progress and never edit its body (the server rejects it with 422; ask and check with add_comment). When you take it over, or edit the body on the assignee's behalf, check with the user first and add override_reason (the override on set_status to In Progress, and assign_issue, move the assignment to you; update_issue goes through without moving it, and the reason stays in the record). next only takes issues assigned to you or to nobody.
Leave project out and the project from the X-Looptrack-Project header of the connection settings is used.
When the result of a change shows looptrack issue usage attach <ID>, run that command in a shell (the same after starting an issue with next). The marker is that command, not the wording (the server's wording changes with the user's language, so do not look for a particular phrase).
Checking and totalling token measurement: issue_usage (per stage of an issue), usage_missing (your AI operations with no token usage attached, and how complete it is), usage_report (the total for a period; request_id for the period of a request), list_usage_ledger / add_usage_ledger (the ledger of reports; a registration cannot be taken back), list_usage_requests (report requests filed from the web UI).
When project_summary shows a request for a report, follow the skill token-report (.claude/skills/token-report) to build the report and register it in the ledger.`

// mcpInstructions は接続の言語に合わせた案内を返す（日本語が正本。英語が無い形は作らない）。
func mcpInstructions(lang i18n.Lang) string {
	if lang == i18n.JA {
		return mcpInstructionsJA
	}
	return mcpInstructionsEN
}

// reviewPromptText は prompt「review」の本文（全文は DESIGN.md §5-8-5 と一致させる。テストで比較する）。
const reviewPromptText = `イシュー管理（looptrack）の「人の判断待ち」と「外からの反応」を利用者に持ちかけてください%s。

1. project_summary を呼び、「人の判断待ち（In Review）」と「外からの反応（未応答のフィードバック）」の一覧を得る。どちらも無ければ「判断待ちはありません」と伝えて止まる
2. In Review を滞留の長い順に 1 件ずつ、get_issue で本文・コメントを読み、利用者に次を示す:
   - 何を作ったか（変更の要点・場所）
   - 検証結果（verify の記録・受け入れ条件ごとの結果と確かめた方法）
   - 判断してほしい点（In Review にしたときのコメント）
3. 利用者の返答を add_comment で残す。了承・指示は先頭「判断: 」、やり直しは先頭「差し戻し: 」とし、利用者の言葉と次の方針を書く
4. 「判断:」なら set_status で Done（comment に検証結果）。「差し戻し:」なら set_status で Todo（comment に方針）。利用者が決めなかったものは In Review のまま次へ
5. 未応答のフィードバック（先頭「フィードバック:」のコメント）を 1 件ずつ利用者に示し、対応を決める: 既存イシューで直す（方針を add_comment）・新しく起票する（create_issue。元のイシューに起票した ID を add_comment）・対応しない（理由を add_comment）。どれも先頭語を付けないコメントで応答を残す（これで未応答から外れる）
6. 扱った件数（Done・Todo へ戻した・残した・フィードバックへの応答）を報告して止まる。この prompt の中では next を呼ばない`

// mcpHandler は認証付きの MCP ハンドラを作る。
func (s *Server) mcpHandler() http.Handler {
	// instructions は mcp.NewServer のときに固まる（SDK は initialize / server/discover の応答に
	// ServerOptions.Instructions をそのまま入れる）。要求ごとには差し替えられないので、**言語ごとに
	// *mcp.Server を作り**、SDK が要求ごとに呼ぶ getServer で選ぶ。切り替えの単位は要求（= 接続の initialize）。
	// ツール定義（ツールと入力項目の説明）も同じで、登録のときに固まるので、言語ごとのサーバにその言語で登録する
	// （mcp_tooldef.go）。サーバは起動時に言語ごとに 1 つだけ作り、要求ごとには作り直さない。
	servers := map[i18n.Lang]*mcp.Server{}
	for _, lang := range []i18n.Lang{i18n.JA, i18n.EN} {
		srv := mcp.NewServer(&mcp.Implementation{Name: "looptrack", Title: i18n.T(lang, "server.mcp.title"), Version: "1"},
			&mcp.ServerOptions{Instructions: mcpInstructions(lang)})
		s.addMCPTools(srv, lang)
		s.addSetupMCP(srv, lang) // setup ツール・prompts・ツール結果への導入の指示
		servers[lang] = srv
		s.mcpServersBuilt.Add(1)
	}
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return servers[mcpConnLang(r)] },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: s.cfg.Logger})
	verify := func(ctx context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
		if s.cfg.LocalMode { // ローカルモードはトークンを見ずに最初の管理者として通す
			p, err := s.localPrincipal(r)
			if err != nil {
				return nil, err
			}
			return &auth.TokenInfo{UserID: strconv.FormatInt(p.User.ID, 10), Extra: map[string]any{"principal": p}}, nil
		}
		p, _, err := s.bearer(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		return &auth.TokenInfo{UserID: strconv.FormatInt(p.User.ID, 10), Extra: map[string]any{"principal": p}}, nil
	}
	// 接続の記録（clientInfo）は認証の後・SDK の前に置く
	protected := auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(s.recordMCPConnections(h))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.LocalMode {
			// SDK の RequireBearerToken は Authorization が無いと verify を呼ばずに 401 を返すため、置き換えて渡す
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer local")
		}
		// 401 には保護リソースのメタデータの場所を添える（クライアントが OAuth の入口を見つけられる）
		protected.ServeHTTP(&challengeWriter{ResponseWriter: w, resourceMetadata: s.resourceMetadataURL(r)}, r)
	})
}

// challengeWriter は 401 に WWW-Authenticate が無ければ補う（RFC 6750。SDK は resource_metadata を設定したときだけ付ける）。
type challengeWriter struct {
	http.ResponseWriter
	resourceMetadata string
}

func (c *challengeWriter) WriteHeader(code int) {
	if code == http.StatusUnauthorized && c.Header().Get("WWW-Authenticate") == "" {
		v := `Bearer realm="im"`
		if c.resourceMetadata != "" {
			v += `, resource_metadata="` + c.resourceMetadata + `"`
		}
		c.Header().Set("WWW-Authenticate", v)
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *challengeWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *challengeWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// mcpCall はツール呼び出しの利用者・経路・既定プロジェクト・言語。
type mcpCall struct {
	p       *principal
	actor   service.Actor
	project string    // X-Looptrack-Project
	lang    i18n.Lang // 要求ごとに決める（グローバルに置かない）
}

// mcpLang は MCP の要求の言語。判定そのものは langFor（lang.go）に置く。
// MCP の接続設定はヘッダを持てないことが多いので、**ヘッダが 1 つも無くても利用者の設定で決まる**
// （利用者は認証が context に置いたものを extra から引く。認証前なら設定の段を飛ばす）。
func mcpLang(req *mcp.CallToolRequest) i18n.Lang {
	extra := req.GetExtra()
	explicit, accept := "", ""
	if extra != nil && extra.Header != nil {
		explicit, accept = extra.Header.Get(langHeader), extra.Header.Get("Accept-Language")
	}
	return langFor("", explicit, userLang(principalOfExtra(extra)), accept)
}

// mcpConnLang は MCP の HTTP 要求（initialize を含む）の言語。instructions を選ぶためだけに使う。
// 判定そのものは langFor（lang.go）に置く。認証は SDK の前に済んでいるので、利用者の設定も引ける。
func mcpConnLang(r *http.Request) i18n.Lang {
	if r == nil {
		return i18n.EN
	}
	return langFor("", r.Header.Get(langHeader), userLang(principalOfContext(r.Context())), r.Header.Get("Accept-Language"))
}

func mcpCallOf(req *mcp.CallToolRequest) (*mcpCall, error) {
	lang := mcpLang(req)
	extra := req.GetExtra()
	if extra == nil || extra.TokenInfo == nil {
		return nil, errors.New(i18n.T(lang, "server.mcp.err.auth_required"))
	}
	p, ok := extra.TokenInfo.Extra["principal"].(*principal)
	if !ok {
		return nil, errors.New(i18n.T(lang, "server.mcp.err.auth_required"))
	}
	c := &mcpCall{p: p, actor: service.Actor{UserID: p.User.ID, TokenID: p.TokenID, Via: "mcp", Lang: lang}, lang: lang}
	if extra.Header != nil {
		if sid := strings.TrimSpace(extra.Header.Get("X-Looptrack-Session")); sid != "" && len(sid) <= 128 {
			c.actor.SessionID = sid
			// X-Looptrack-Session-Kind: セッション ID の種類（REST の actor と同じ判定。知らない値は読み捨てる）。
			// host は器（デスクトップ版の窓）の ID で会話記録と結び付かないので、付与の対象から外す（§9-5）。
			// 下の接続 ID のフォールバックには付けない（サーバが発行した値で、器かどうかとは関係が無い）。
			if strings.EqualFold(strings.TrimSpace(extra.Header.Get("X-Looptrack-Session-Kind")), service.SessionKindHost) {
				c.actor.SessionKind = service.SessionKindHost
			}
		} else if sid := strings.TrimSpace(extra.Header.Get(mcpSessionHeader)); sid != "" && len(sid)+len(service.MCPSessionPrefix) <= 128 {
			// MCP の接続設定はヘッダを持てないことが多い。サーバが initialize で発行した接続 ID
			// （Mcp-Session-Id = mcp_connections.id。setup.go）を、セッションを見分ける値として代わりに使う
			// （同じ利用者の別の接続は別の値になる。接続を張り直すと変わる）。
			// クライアントが名乗るセッション ID とは種類が違うので印を付けて区別する（service.ComparableSessions）。
			c.actor.SessionID = service.MCPSessionPrefix + sid
		}
		c.project = strings.TrimSpace(extra.Header.Get("X-Looptrack-Project"))
	}
	return c, nil
}

// mcpCallAgent は mcpCallOf に、接続してきた AI の種類（clientInfo から判定。setup ツールと同じ）を足す。
// 変更系のツールが使う: 計測を利用者が有効にしたときだけ測れる AI（Copilot）の操作を、有効にしていない利用者なら付与の指示・クローズ時の必須・
// 未付与の検知から外す（判定は service.UsageTarget・store.UsageCoverage）。
func (s *Server) mcpCallAgent(ctx context.Context, req *mcp.CallToolRequest) (*mcpCall, error) {
	c, err := mcpCallOf(req)
	if err != nil {
		return nil, err
	}
	c.actor.Agent = s.mcpClient(ctx, req.GetExtra(), req.ClientInfo(), c.p.User.ID).Agent
	return c, nil
}

// projectSlug は引数・ヘッダ・参加している唯一のプロジェクトの順で対象プロジェクトを決める
// （admin の利用者も参加分で数える。参加していない slug も引数かヘッダで指定すれば閲覧できる。書くには参加が要る）。
func (s *Server) projectSlug(ctx context.Context, c *mcpCall, arg string) (string, error) {
	if arg != "" {
		return arg, nil
	}
	if c.project != "" {
		return c.project, nil
	}
	projects, _, err := store.MemberProjects(ctx, s.db, c.p.User.ID)
	if err != nil {
		return "", err
	}
	if len(projects) == 1 {
		return projects[0].Slug, nil
	}
	var slugs []string
	for _, pr := range projects {
		slugs = append(slugs, pr.Slug)
	}
	if len(slugs) == 0 {
		return "", invalid(i18n.T(c.lang, "server.mcp.err.project_required_none"))
	}
	return "", invalid(i18n.T(c.lang, "server.mcp.err.project_required", "slugs", strings.Join(slugs, ", ")))
}

// toolError は利用者に見せるエラーにする。内部のエラーは記録して一般的な文言に置き換える。
// 対象プロジェクトが決まらないときの案内（projectSlug）も service.Error なので、そのまま返る。
func (s *Server) toolError(lang i18n.Lang, tool string, err error) error {
	var se *service.Error
	if errors.As(err, &se) {
		// MCP は返した error の Error() を isError の本文にする。se.Error() は日本語の
		// se.Message なので、要求の言語の文面に作り直して返す（i18n.Text が Unwrap の先の ID を訳す）。
		return errors.New(i18n.Text(lang, se))
	}
	s.cfg.Logger.Error("mcp tool", "tool", tool, "err", err)
	return errors.New(i18n.T(lang, "server.api.err.internal"))
}

// mcpDataMeta はツール結果の構造化データを載せる _meta のキー（接頭辞 looptrack/ は MCP の _meta のキーの規則に沿う）。
const mcpDataMeta = "looptrack/data"

// result はテキスト（人が読む形）を返し、構造化データは _meta に載せる。
// structuredContent には入れない: Claude Code は structuredContent があるとそれだけを AI に渡し、content の text
// （guide の本文・【導入が未完了】・「トークン情報が未付与です」などの通知）を捨てる。MCP の仕様は、structuredContent を返すなら
// 同じ JSON を text にも入れる（SHOULD）としており、クライアントは text を structuredContent の写しとみなしてよいため。
// ツールは出力スキーマを宣言していない（mcp.AddTool の Out は any）ので、structuredContent を返さなくても仕様に反しない。
func result(text string, data any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, Meta: mcp.Meta{mcpDataMeta: data}}
}

// 一覧の列の既定の幅と、BLOCKED を広げる上限（以前の CLI（1.0.0 より前）と同じ）。
const (
	colID, colBlocked, colAssignee = 9, 8, 12
	blockedMax                     = 26
)

func blockedText(it issueJSON) string {
	if len(it.BlockedBy) == 0 {
		return "-"
	}
	return strings.Join(it.BlockedBy, ",")
}

// rowsText は以前の CLI の list と同じ列の表。担当が 1 件でもあれば TITLE の前に ASSIGNEE 列を足す（以前の CLI と同じ）。
// ID・BLOCKED・ASSIGNEE の幅は中身に合わせて広げ（既定の幅より狭くしない）、BLOCKED は 26 文字を超えたら切って「…」を付ける
// （BLOCKED が長くても ASSIGNEE・TITLE の列がそろう。以前の CLI と同じ規則）。
func rowsText(lang i18n.Lang, items []issueJSON, empty string) string {
	if len(items) == 0 {
		return empty
	}
	withAssignee, inactive := false, false
	wID, wBlocked, wAssignee := colID, colBlocked, colAssignee
	for _, it := range items {
		withAssignee = withAssignee || it.Assignee != ""
		inactive = inactive || it.AssigneeInactive
		wID = max(wID, utf8.RuneCountInString(it.ID))
		wBlocked = max(wBlocked, utf8.RuneCountInString(blockedText(it)))
	}
	wBlocked = min(wBlocked, blockedMax)
	if withAssignee {
		for _, it := range items {
			wAssignee = max(wAssignee, utf8.RuneCountInString(assigneeLabel(it)))
		}
	}
	var b strings.Builder
	head := fmt.Sprintf("%-*s %-11s %-11s %-3s %-*s ", wID, "ID", "TYPE", "STATUS", "PRI", wBlocked, "BLOCKED")
	if withAssignee {
		head += fmt.Sprintf("%-*s ", wAssignee, "ASSIGNEE")
	}
	b.WriteString(head + "TITLE\n")
	for _, it := range items {
		blocked := blockedText(it)
		if r := []rune(blocked); len(r) > wBlocked {
			blocked = string(r[:wBlocked-1]) + "…"
		}
		fmt.Fprintf(&b, "%-*s %-11s %-11s %-3s %-*s ", wID, it.ID, it.Type, it.Status, it.Priority, wBlocked, blocked)
		if withAssignee {
			fmt.Fprintf(&b, "%-*s ", wAssignee, assigneeLabel(it))
		}
		b.WriteString(it.Title + "\n")
	}
	b.WriteString("\n" + i18n.T(lang, "server.mcp.list.count", "count", len(items)))
	if inactive {
		b.WriteString("\n" + i18n.T(lang, "server.mcp.list.inactive_note"))
	}
	return b.String()
}

// assigneeLabel は一覧の担当の表記（未設定は -、印は (!)）。
func assigneeLabel(it issueJSON) string {
	switch {
	case it.Assignee == "":
		return "-"
	case it.AssigneeInactive:
		return it.Assignee + "(!)"
	}
	return it.Assignee
}

// MCP のツールの入力。jsonschema タグは説明の文面ではなく対訳表（internal/i18n の ja.json / en.json）の ID で、
// 登録のときに接続の言語の文面へ置き換える（mcp_tooldef.go）。
type (
	projectArg struct {
		Project string `json:"project,omitempty" jsonschema:"server.mcp.arg.project.project"`
	}
	listIssuesIn struct {
		projectArg
		Status   string `json:"status,omitempty" jsonschema:"server.mcp.arg.list_issues.status"`
		Type     string `json:"type,omitempty" jsonschema:"server.mcp.arg.list_issues.type"`
		Label    string `json:"label,omitempty" jsonschema:"server.mcp.arg.list_issues.label"`
		Ref      string `json:"ref,omitempty" jsonschema:"server.mcp.arg.list_issues.ref"`
		All      bool   `json:"all,omitempty" jsonschema:"server.mcp.arg.list_issues.all"`
		Sort     string `json:"sort,omitempty" jsonschema:"server.mcp.arg.list_issues.sort"`
		Reverse  bool   `json:"reverse,omitempty" jsonschema:"server.mcp.arg.list_issues.reverse"`
		Assignee string `json:"assignee,omitempty" jsonschema:"server.mcp.arg.list_issues.assignee"`
		// 未応答のフィードバックで絞る（§5-8-6）
		HasFeedback bool `json:"has_feedback,omitempty" jsonschema:"server.mcp.arg.list_issues.has_feedback"`
	}
	readyIn struct {
		projectArg
		Sort     string `json:"sort,omitempty" jsonschema:"server.mcp.arg.ready_issues.sort"`
		Reverse  bool   `json:"reverse,omitempty" jsonschema:"server.mcp.arg.ready_issues.reverse"`
		Assignee string `json:"assignee,omitempty" jsonschema:"server.mcp.arg.ready_issues.assignee"`
	}
	issueIDArg struct {
		ID      string `json:"id" jsonschema:"server.mcp.arg.issue_id.id"`
		Project string `json:"project,omitempty" jsonschema:"server.mcp.arg.issue_id.project"`
	}
	createIn struct {
		projectArg
		Title          string   `json:"title" jsonschema:"server.mcp.arg.create_issue.title"`
		Type           string   `json:"type,omitempty" jsonschema:"server.mcp.arg.create_issue.type"`
		Status         string   `json:"status,omitempty" jsonschema:"server.mcp.arg.create_issue.status"`
		Priority       string   `json:"priority,omitempty" jsonschema:"server.mcp.arg.create_issue.priority"`
		Labels         []string `json:"labels,omitempty" jsonschema:"server.mcp.arg.create_issue.labels"`
		Parent         string   `json:"parent,omitempty" jsonschema:"server.mcp.arg.create_issue.parent"`
		BlockedBy      []string `json:"blocked_by,omitempty" jsonschema:"server.mcp.arg.create_issue.blocked_by"`
		Traces         []string `json:"traces,omitempty" jsonschema:"server.mcp.arg.create_issue.traces"`
		Refs           []string `json:"refs,omitempty" jsonschema:"server.mcp.arg.create_issue.refs"`
		Body           string   `json:"body,omitempty" jsonschema:"server.mcp.arg.create_issue.body"`
		OverrideReason string   `json:"override_reason,omitempty" jsonschema:"server.mcp.arg.create_issue.override_reason"`
		Assignee       string   `json:"assignee,omitempty" jsonschema:"server.mcp.arg.create_issue.assignee"`
	}
	commentIn struct {
		issueIDArg
		Text string `json:"text" jsonschema:"server.mcp.arg.add_comment.text"`
	}
	statusIn struct {
		issueIDArg
		Status         string `json:"status" jsonschema:"server.mcp.arg.set_status.status"`
		Comment        string `json:"comment,omitempty" jsonschema:"server.mcp.arg.set_status.comment"`
		OverrideReason string `json:"override_reason,omitempty" jsonschema:"server.mcp.arg.set_status.override_reason"`
		Assignee       string `json:"assignee,omitempty" jsonschema:"server.mcp.arg.set_status.assignee"`
	}
	updateIn struct {
		issueIDArg
		Version   int       `json:"version,omitempty" jsonschema:"server.mcp.arg.update_issue.version"`
		Markdown  *string   `json:"markdown,omitempty" jsonschema:"server.mcp.arg.update_issue.markdown"`
		Title     *string   `json:"title,omitempty" jsonschema:"server.mcp.arg.update_issue.title"`
		Type      *string   `json:"type,omitempty" jsonschema:"server.mcp.arg.update_issue.type"`
		Priority  *string   `json:"priority,omitempty" jsonschema:"server.mcp.arg.update_issue.priority"`
		Parent    *string   `json:"parent,omitempty" jsonschema:"server.mcp.arg.update_issue.parent"`
		Labels    *[]string `json:"labels,omitempty" jsonschema:"server.mcp.arg.update_issue.labels"`
		BlockedBy *[]string `json:"blocked_by,omitempty" jsonschema:"server.mcp.arg.update_issue.blocked_by"`
		Traces    *[]string `json:"traces,omitempty" jsonschema:"server.mcp.arg.update_issue.traces"`
		Refs      *[]string `json:"refs,omitempty" jsonschema:"server.mcp.arg.update_issue.refs"`
		// 担当者。担当だけを変えるときは version を省略できる
		Assignee       *string `json:"assignee,omitempty" jsonschema:"server.mcp.arg.update_issue.assignee"`
		OverrideReason string  `json:"override_reason,omitempty" jsonschema:"server.mcp.arg.update_issue.override_reason"`
	}
	assignIn struct {
		issueIDArg
		Assignee       string `json:"assignee" jsonschema:"server.mcp.arg.assign_issue.assignee"`
		OverrideReason string `json:"override_reason,omitempty" jsonschema:"server.mcp.arg.assign_issue.override_reason"`
	}
	matrixIn struct {
		projectArg
		Format string `json:"format,omitempty" jsonschema:"server.mcp.arg.get_matrix.format"`
	}
	summaryIn struct {
		projectArg
		Limit int `json:"limit,omitempty" jsonschema:"server.mcp.arg.project_summary.limit"`
	}
	activityIn struct {
		IDs   []string `json:"ids" jsonschema:"server.mcp.arg.issue_activity.ids"`
		Since float64  `json:"since,omitempty" jsonschema:"server.mcp.arg.issue_activity.since"`
	}
	usageMissingIn struct {
		projectArg
		Days     int  `json:"days,omitempty" jsonschema:"server.mcp.arg.usage_missing.days"`
		AllUsers bool `json:"all_users,omitempty" jsonschema:"server.mcp.arg.usage_missing.all_users"`
	}
	nextIn struct {
		projectArg
		DryRun         bool     `json:"dry_run,omitempty" jsonschema:"server.mcp.arg.next.dry_run"`
		Comment        string   `json:"comment,omitempty" jsonschema:"server.mcp.arg.next.comment"`
		OverrideReason string   `json:"override_reason,omitempty" jsonschema:"server.mcp.arg.next.override_reason"`
		Types          []string `json:"types,omitempty" jsonschema:"server.mcp.arg.next.types"`
		Assignee       string   `json:"assignee,omitempty" jsonschema:"server.mcp.arg.next.assignee"`
	}
	noArgs struct{}
)

func (s *Server) addMCPTools(srv *mcp.Server, lang i18n.Lang) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	notDestructive := false

	addTool(srv, lang, &mcp.Tool{Name: "list_projects", Description: i18n.T(lang, "server.mcp.tool.list_projects"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			projects, roles, err := store.MemberProjects(ctx, s.db, c.p.User.ID)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "list_projects", err)
			}
			out := []projectSummaryJSON{}
			var b strings.Builder
			for _, pr := range projects {
				set, _, err := s.svc.ProjectIssues(ctx, pr)
				if err != nil {
					return nil, nil, s.toolError(c.lang, "list_projects", err)
				}
				sum := summaryOf(pr, roles[pr.ID])
				sum.Counts = counts(set)
				out = append(out, sum)
				b.WriteString(i18n.T(c.lang, "server.mcp.list_projects.row", "slug", pr.Slug, "name", pr.Name, "prefix", pr.Prefix,
					"role", roles[pr.ID], "open", sum.Counts.Open, "in_progress", sum.Counts.InProgress,
					"ready", sum.Counts.Ready, "bugs", sum.Counts.OpenBugs) + "\n")
			}
			return result(strings.TrimSpace(b.String()), map[string]any{"projects": out}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "list_issues", Description: i18n.T(lang, "server.mcp.tool.list_issues"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in listIssuesIn) (*mcp.CallToolResult, any, error) {
			return s.mcpList(ctx, req, in.Project, in.Sort, in.Reverse, func(set *domain.Set, key string) ([]domain.Issue, error) {
				f := domain.Filter{Status: in.Status, Type: in.Type, Label: in.Label, Ref: in.Ref, All: in.All || in.HasFeedback}
				if f.Status != "" {
					if err := domain.ValidateValue("status", f.Status, domain.Statuses); err != nil {
						return nil, errors.New(i18n.Text(mcpLang(req), err)) // ID を持つ error のまま返すと AI に ID が出る
					}
				}
				return set.List(f, key, in.Reverse), nil
			}, in.Assignee, in.HasFeedback)
		})

	addTool(srv, lang, &mcp.Tool{Name: "ready_issues", Description: i18n.T(lang, "server.mcp.tool.ready_issues"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in readyIn) (*mcp.CallToolResult, any, error) {
			return s.mcpList(ctx, req, in.Project, in.Sort, in.Reverse, func(set *domain.Set, key string) ([]domain.Issue, error) {
				return domain.SortIssues(set.Ready(), key, in.Reverse), nil
			}, in.Assignee, false)
		})

	addTool(srv, lang, &mcp.Tool{Name: "get_issue", Description: i18n.T(lang, "server.mcp.tool.get_issue"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in issueIDArg) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			pr, row, err := s.resolveIssue(ctx, c.lang, c.p.User, in.ID, in.Project, false)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "get_issue", err)
			}
			it, err := s.svc.Detail(ctx, pr, row.ID)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "get_issue", err)
			}
			return result(fmt.Sprintf("version: %d\n\n%s", it.Row.Version, it.Markdown), toDetailJSON(it)), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "create_issue", Description: i18n.T(lang, "server.mcp.tool.create_issue"), Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}},
		func(ctx context.Context, req *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallAgent(ctx, req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "create_issue", err)
			}
			pr, role, err := s.resolveProject(ctx, c.lang, c.p.User, slug)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "create_issue", err)
			}
			if !canWrite(role) {
				return nil, nil, forbidden(c.lang, pr.Slug, i18n.T(c.lang, "server.api.err.what_create"))
			}
			it, err := s.svc.Create(ctx, c.actor, pr, service.CreateInput{Title: in.Title, Type: in.Type, Status: in.Status, Priority: in.Priority,
				Labels: in.Labels, Parent: in.Parent, BlockedBy: in.BlockedBy, Traces: in.Traces, Refs: in.Refs, Body: in.Body, OverrideReason: in.OverrideReason, Assignee: in.Assignee}, c.lang)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "create_issue", err)
			}
			d := toDetailJSON(it)
			d.UsageNotice = s.usageNotice(ctx, c.lang, c.actor, pr, it, false)
			return result(withNotice(i18n.T(c.lang, "server.mcp.issue.created", "id", it.Item.ID, "title", it.Item.Title, "version", it.Row.Version), d.UsageNotice), d), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "add_comment", Description: i18n.T(lang, "server.mcp.tool.add_comment"), Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}},
		func(ctx context.Context, req *mcp.CallToolRequest, in commentIn) (*mcp.CallToolResult, any, error) {
			c, pr, row, err := s.mcpIssue(ctx, req, in.issueIDArg, "add_comment")
			if err != nil {
				return nil, nil, err
			}
			it, err := s.svc.Comment(ctx, c.actor, pr, row.ID, in.Text)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "add_comment", err)
			}
			notice := s.usageNotice(ctx, c.lang, c.actor, pr, it, false)
			return result(withNotice(i18n.T(c.lang, "server.api.issue.comment_added", "id", it.Item.ID), notice),
				map[string]any{"issue": toIssueJSON(it), "seq": len(it.Row.Doc.Comments), "usage_notice": notice}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "set_status", Description: i18n.T(lang, "server.mcp.tool.set_status"), Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}},
		func(ctx context.Context, req *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, any, error) {
			c, pr, row, err := s.mcpIssue(ctx, req, in.issueIDArg, "set_status")
			if err != nil {
				return nil, nil, err
			}
			res, err := s.svc.SetStatusAssign(ctx, c.actor, pr, row.ID, in.Status, in.Comment, in.OverrideReason, in.Assignee)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "set_status", err)
			}
			data := map[string]any{"issue": toIssueJSON(res.Issue), "from": res.From, "to": res.Issue.Item.Status}
			// 下位の最後の 1 件を閉じたら要件の検証と close を促す（REST の messages と同じ行）
			text := strings.Join(withClosable(c.lang, statusMessages(c.lang, res, in.Comment), data, s.closedRequirements(ctx, pr, res)), "\n")
			notice := s.usageNotice(ctx, c.lang, c.actor, pr, res.Issue, res.Issue.Closed() && res.From != res.Issue.Item.Status)
			data["usage_notice"] = notice
			return result(withNotice(text, notice), data), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "update_issue", Description: i18n.T(lang, "server.mcp.tool.update_issue"), Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}},
		func(ctx context.Context, req *mcp.CallToolRequest, in updateIn) (*mcp.CallToolResult, any, error) {
			c, pr, row, err := s.mcpIssue(ctx, req, in.issueIDArg, "update_issue")
			if err != nil {
				return nil, nil, err
			}
			fields := in.Markdown != nil || in.Title != nil || in.Type != nil || in.Priority != nil || in.Parent != nil ||
				in.Labels != nil || in.BlockedBy != nil || in.Traces != nil || in.Refs != nil
			if in.Assignee != nil && !fields { // 担当だけの変更（assign_issue と同じ。version は指定があれば確かめる）
				if in.Version > 0 && in.Version != row.Version {
					cur, err := s.svc.Detail(ctx, pr, row.ID)
					if err != nil {
						return nil, nil, s.toolError(c.lang, "update_issue", err)
					}
					return nil, nil, errors.New(i18n.T(c.lang, "server.mcp.err.version_conflict", "id", cur.Item.ID,
						"given", in.Version, "current", row.Version, "markdown", cur.Markdown))
				}
				return s.mcpAssign(ctx, c, pr, row, *in.Assignee, in.OverrideReason)
			}
			it, err := s.svc.Update(ctx, c.actor, pr, row.ID, in.Version, service.Patch{Title: in.Title, Type: in.Type, Priority: in.Priority,
				Parent: in.Parent, Labels: in.Labels, BlockedBy: in.BlockedBy, Traces: in.Traces, Refs: in.Refs, Markdown: in.Markdown,
				OverrideReason: in.OverrideReason})
			if err != nil {
				var se *service.Error
				if errors.As(err, &se) && se.Current != nil {
					return nil, nil, errors.New(i18n.T(c.lang, "server.mcp.err.current_version", "message", se,
						"current", se.Current.Row.Version, "markdown", se.Current.Markdown))
				}
				return nil, nil, s.toolError(c.lang, "update_issue", err)
			}
			text := i18n.T(c.lang, "server.mcp.issue.updated", "id", it.Item.ID, "version", it.Row.Version)
			if in.Assignee != nil {
				res, err := s.svc.Assign(ctx, c.actor, pr, row.ID, *in.Assignee, in.OverrideReason)
				if err != nil {
					return nil, nil, errors.New(i18n.T(c.lang, "server.mcp.err.update_partial",
						"reason", s.toolError(c.lang, "update_issue", err).Error(), "version", it.Row.Version))
				}
				it = res.Issue
				text += "\n" + res.Message()
			}
			d := toDetailJSON(it)
			d.UsageNotice = s.usageNotice(ctx, c.lang, c.actor, pr, it, false)
			return result(withNotice(text, d.UsageNotice), d), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "assign_issue", Description: i18n.T(lang, "server.mcp.tool.assign_issue"), Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}},
		func(ctx context.Context, req *mcp.CallToolRequest, in assignIn) (*mcp.CallToolResult, any, error) {
			c, pr, row, err := s.mcpIssue(ctx, req, in.issueIDArg, "assign_issue")
			if err != nil {
				return nil, nil, err
			}
			return s.mcpAssign(ctx, c, pr, row, in.Assignee, in.OverrideReason)
		})

	addTool(srv, lang, &mcp.Tool{Name: "get_matrix", Description: i18n.T(lang, "server.mcp.tool.get_matrix"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in matrixIn) (*mcp.CallToolResult, any, error) {
			pr, set, _, err := s.mcpProjectSet(ctx, req, in.Project, "get_matrix")
			if err != nil {
				return nil, nil, err
			}
			m := set.BuildMatrixData()
			type orphanJSON struct {
				Target string   `json:"target"`
				From   []string `json:"from"`
			}
			orphans := []orphanJSON{}
			for _, o := range m.Orphans {
				orphans = append(orphans, orphanJSON{o.Target, o.From})
			}
			data := map[string]any{"project": pr.Slug, "requirements": len(m.Rows), "untested": refs(m.Untested), "orphans": orphans,
				"requirements_ready": closablesJSON(mcpLang(req), set.ClosableRequirements())}
			if in.Format == "json" {
				b, _ := json.MarshalIndent(data, "", "  ")
				return result(string(b), data), nil, nil
			}
			return result(s.matrixMarkdown(set), data), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "project_summary", Description: i18n.T(lang, "server.mcp.tool.project_summary"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in summaryIn) (*mcp.CallToolResult, any, error) {
			pr, set, rows, err := s.mcpProjectSet(ctx, req, in.Project, "project_summary")
			if err != nil {
				return nil, nil, err
			}
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			missing, err := s.usageCoverage(ctx, pr, c.p.User.ID, summaryUsageDays)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "project_summary", err)
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 10
			}
			ready := set.Ready()
			byStatus := func(st string) []issueJSON {
				return itemsJSON(pr, rows, set.List(domain.Filter{Status: st}, "priority", false))
			}
			inProgress, top := byStatus("In Progress"), itemsJSON(pr, rows, ready[:min(limit, len(ready))])
			markOtherSession(ctx, c.lang, s.db, pr.ID, c.actor.SessionID, inProgress) // 別のセッションが着手したものに印
			// 3 層の見出し（§5-8-7。② ③ の行は以前の CLI の summary と同じ文言）
			inReview, fb, cnt, err := s.loopLayers(ctx, c.lang, pr, rows, byStatus("In Review"), counts(set), limit)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "project_summary", err)
			}
			// ① の末尾に下位がすべて完了した要件（対象が無ければ節ごと出さない）
			closable := set.ClosableRequirements()
			closableTop := closablesJSON(c.lang, closable[:min(limit, len(closable))])
			reqs := closableSummaryText(c.lang, closableTop, len(closable))
			if reqs != "" {
				reqs += "\n"
			}
			none := i18n.T(c.lang, "server.api.summary.none")
			ipText := rowsText(c.lang, inProgress, none)
			if n := otherSessionNote(c.lang, inProgress); n != "" {
				ipText += "\n" + n
			}
			if n := crossPathNote(c.lang, inProgress); n != "" {
				ipText += "\n" + n
			}
			text := fmt.Sprintf("%s\n%s\n%s\n\n%s\n%s\n\n%s%s\n%s\n%s",
				layerWorkHeading(c.lang), i18n.T(c.lang, "server.mcp.summary.in_progress_heading"), ipText,
				i18n.T(c.lang, "server.mcp.summary.ready_heading", "shown", len(top), "total", len(ready)), rowsText(c.lang, top, none), reqs,
				reviewLayerText(c.lang, inReview), feedbackLayerText(c.lang, fb, s.svc.Loc),
				i18n.T(c.lang, "server.mcp.summary.open_counts", "open", cnt.Open, "bugs", cnt.OpenBugs))
			if m := usageMissingText(c.lang, missing); m != "" {
				text += "\n" + m
			}
			requests, err := s.requestList(ctx, c.lang, pr, false) // 未完了のレポート作成依頼
			if err != nil {
				return nil, nil, s.toolError(c.lang, "project_summary", err)
			}
			if len(requests) > 0 {
				text += "\n\n" + i18n.T(c.lang, "server.mcp.summary.requests_heading", "count", len(requests)) + "\n" + s.requestText(c.lang, requests) +
					i18n.T(c.lang, "server.mcp.summary.next_command", "command", requestCommand(c.lang, requests[len(requests)-1].ID))
			}
			return result(text, map[string]any{"project": pr.Slug, "counts": cnt, "in_progress": inProgress, "in_review": inReview, "feedback": fb,
				"ready": top, "ready_total": len(ready), "requirements_ready": closableTop, "requirements_ready_total": len(closable),
				"usage_missing":  map[string]any{"count": missing.Missing, "issues": missing.Issues, "days": missing.Days, "command": missing.Command},
				"usage_requests": requests}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "usage_missing", Description: i18n.T(lang, "server.mcp.tool.usage_missing"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in usageMissingIn) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			pr, _, _, err := s.mcpProjectSet(ctx, req, in.Project, "usage_missing")
			if err != nil {
				return nil, nil, err
			}
			days := in.Days
			if days <= 0 {
				days = 30
			}
			if days > 366 {
				return nil, nil, errors.New(i18n.T(c.lang, "server.api.err.days_range"))
			}
			var userID int64
			if !in.AllUsers {
				userID = c.p.User.ID
			}
			cov, err := s.usageCoverage(ctx, pr, userID, days)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "usage_missing", err)
			}
			return result(usageCoverageText(c.lang, cov), cov), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "guide", Description: i18n.T(lang, "server.mcp.tool.guide"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in projectArg) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "guide", err)
			}
			pr, role, err := s.resolveProject(ctx, c.lang, c.p.User, slug)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "guide", err)
			}
			out, err := s.composeGuide(ctx, c.lang, pr, role, c.p.User)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "guide", err)
			}
			return result(out.Markdown, map[string]any{"project": pr.Slug, "rules": nonNilRules(out.Rules), "doc_source": out.DocInfo}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "next", Description: i18n.T(lang, "server.mcp.tool.next"), Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive}},
		func(ctx context.Context, req *mcp.CallToolRequest, in nextIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallAgent(ctx, req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "next", err)
			}
			pr, role, err := s.resolveProject(ctx, c.lang, c.p.User, slug)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "next", err)
			}
			if !canWrite(role) {
				return nil, nil, forbidden(c.lang, pr.Slug, i18n.T(c.lang, "server.api.err.what_next"))
			}
			res, err := s.svc.Next(ctx, c.actor, pr, service.NextOptions{DryRun: in.DryRun, Comment: in.Comment, OverrideReason: in.OverrideReason, Types: in.Types,
				Assignee: in.Assignee})
			if err != nil {
				return nil, nil, s.toolError(c.lang, "next", err)
			}
			v := nextView(c.lang, res)
			if res.Action == "started" {
				v.UsageNotice = s.usageNotice(ctx, c.lang, c.actor, pr, res.Issue, false)
			}
			return result(withNotice(v.Text, v.UsageNotice), v), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "issue_activity", Description: i18n.T(lang, "server.mcp.tool.issue_activity"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in activityIn) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			if len(in.IDs) == 0 || len(in.IDs) > 200 {
				return nil, nil, errors.New(i18n.T(c.lang, "server.mcp.err.ids_required"))
			}
			projects, _, err := store.AccessibleProjects(ctx, s.db, c.p.User)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "issue_activity", err)
			}
			slugs := map[int64]string{}
			for _, pr := range projects {
				slugs[pr.ID] = pr.Slug
			}
			acts, err := store.IssueActivity(ctx, s.db, in.IDs, time.Unix(0, int64(in.Since*1e9)).UTC())
			if err != nil {
				return nil, nil, s.toolError(c.lang, "issue_activity", err)
			}
			out := []activityJSON{}
			var b strings.Builder
			for _, a := range acts {
				slug, ok := slugs[a.ProjectID]
				if !ok {
					continue
				}
				out = append(out, activityJSON{ID: a.DisplayID, Project: slug, Title: a.Title, Status: a.Status,
					Closed: domain.Issue{Status: a.Status}.IsClosed(), LastAt: a.LastAt.UTC().Format(time.RFC3339Nano),
					LastEpoch: float64(a.LastAt.UnixNano()) / 1e9, LastKind: a.LastKind, LastVia: a.LastVia, EventsSince: a.Since})
			}
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			for _, a := range out {
				b.WriteString(i18n.T(c.lang, "server.mcp.activity.row", "id", a.ID, "at", a.LastAt, "kind", a.LastKind,
					"via", a.LastVia, "count", a.EventsSince) + "\n")
			}
			if len(out) == 0 {
				b.WriteString(i18n.T(c.lang, "server.api.summary.none"))
			}
			return result(strings.TrimSpace(b.String()), map[string]any{"items": out}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "issue_usage", Description: i18n.T(lang, "server.mcp.tool.issue_usage"), Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in issueIDArg) (*mcp.CallToolResult, any, error) {
			c, err := mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			pr, row, err := s.resolveIssue(ctx, c.lang, c.p.User, in.ID, in.Project, false)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "issue_usage", err)
			}
			u, err := s.issueUsage(ctx, pr, row)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "issue_usage", err)
			}
			return result(issueUsageText(c.lang, u), u), nil, nil
		})

	s.addUsageMCPTools(srv, lang, ro, notDestructive) // トークンレポートの集計と台帳
	s.addRequestMCPTools(srv, lang, ro)               // レポートの作成依頼
	s.addVerifyMCPTools(srv, lang, ro)                // 検証コマンドの一覧（実行しない）
}

// mcpAssign は担当の変更（assign_issue と update_issue の担当だけの変更）。
func (s *Server) mcpAssign(ctx context.Context, c *mcpCall, pr store.Project, row store.IssueRow, assignee, reason string) (*mcp.CallToolResult, any, error) {
	res, err := s.svc.Assign(ctx, c.actor, pr, row.ID, assignee, reason)
	if err != nil {
		return nil, nil, s.toolError(c.lang, "assign_issue", err)
	}
	return result(res.Message(), map[string]any{"issue": toIssueJSON(res.Issue), "from": res.From, "to": res.To, "changed": res.Changed}), nil, nil
}

// withNotice はツールの結果の文に付与の指示を足す。
func withNotice(text, notice string) string {
	if notice == "" {
		return text
	}
	return text + "\n" + notice
}

// mcpIssue は変更系ツールの共通処理（利用者・対象イシュー・変更権限）。
func (s *Server) mcpIssue(ctx context.Context, req *mcp.CallToolRequest, in issueIDArg, tool string) (*mcpCall, store.Project, store.IssueRow, error) {
	c, err := s.mcpCallAgent(ctx, req)
	if err != nil {
		return nil, store.Project{}, store.IssueRow{}, err
	}
	pr, row, err := s.resolveIssue(ctx, c.lang, c.p.User, in.ID, in.Project, true)
	if err != nil {
		return nil, store.Project{}, store.IssueRow{}, s.toolError(c.lang, tool, err)
	}
	return c, pr, row, nil
}

// mcpProjectSet はプロジェクトを決めて全イシュー（frontmatter）を読む。
func (s *Server) mcpProjectSet(ctx context.Context, req *mcp.CallToolRequest, arg, tool string) (store.Project, *domain.Set, []service.Issue, error) {
	c, err := mcpCallOf(req)
	if err != nil {
		return store.Project{}, nil, nil, err
	}
	slug, err := s.projectSlug(ctx, c, arg)
	if err != nil {
		return store.Project{}, nil, nil, s.toolError(c.lang, tool, err)
	}
	pr, _, err := s.resolveProject(ctx, c.lang, c.p.User, slug)
	if err != nil {
		return store.Project{}, nil, nil, s.toolError(c.lang, tool, err)
	}
	set, rows, err := s.svc.ProjectIssues(ctx, pr)
	if err != nil {
		return store.Project{}, nil, nil, s.toolError(c.lang, tool, err)
	}
	return pr, set, rows, nil
}

func (s *Server) mcpList(ctx context.Context, req *mcp.CallToolRequest, project, sortKey string, reverse bool,
	pick func(set *domain.Set, key string) ([]domain.Issue, error), assignee string, hasFeedback bool) (*mcp.CallToolResult, any, error) {
	if sortKey == "" {
		sortKey = "priority"
	}
	if err := domain.ValidateValue("sort", sortKey, domain.SortKeys); err != nil {
		return nil, nil, errors.New(i18n.Text(mcpLang(req), err))
	}
	pr, set, rows, err := s.mcpProjectSet(ctx, req, project, "list")
	if err != nil {
		return nil, nil, err
	}
	items, err := pick(set, sortKey)
	if err != nil {
		return nil, nil, err
	}
	c, err := mcpCallOf(req)
	if err != nil {
		return nil, nil, err
	}
	out := filterAssignee(itemsJSON(pr, rows, items), assignee, c.p.User.Login)
	// 別のセッションが着手したものに印を付ける（空きを探す経路。project_summary と同じ判定）
	markOtherSession(ctx, c.lang, s.db, pr.ID, c.actor.SessionID, out)
	none := i18n.T(c.lang, "server.api.summary.none")
	text := rowsText(c.lang, out, none)
	if hasFeedback {
		n, _, err := s.pendingByIssue(ctx, pr)
		if err != nil {
			return nil, nil, s.toolError(c.lang, "list", err)
		}
		out = withFeedbackPending(out, n, true)
		text = rowsText(c.lang, out, none)
		if note := feedbackNote(c.lang, out); note != "" {
			text += "\n" + note
		}
	}
	if note := otherSessionNote(c.lang, out); note != "" {
		text += "\n" + note
	}
	if note := crossPathNote(c.lang, out); note != "" {
		text += "\n" + note
	}
	return result(text, map[string]any{"project": pr.Slug, "items": out, "count": len(out)}), nil, nil
}
