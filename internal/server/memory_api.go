package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
)

// プロジェクト記憶（docs/server/DESIGN.md §9-9）の REST と MCP。範囲と権限・版の照合・上限・資格情報の検査は
// service（memory.go）にあり、ここでは HTTP と MCP の形だけを決める。
//
//	GET    /api/v1/projects/{slug}/memory?deleted=1                 一覧（project の範囲と自分の personal。本文は含めない）
//	POST   /api/v1/projects/{slug}/memory                           作る（{scope, kind, name, content, machine_label}。あれば 409 memory_exists）
//	GET    /api/v1/projects/{slug}/memory/{kind}/{name}?scope=&version=  1 件と版の履歴（version を付けるとその版の本文）
//	PUT    /api/v1/projects/{slug}/memory/{kind}/{name}?scope=      置き換え（{content, machine_label}。If-Match: "<版>" が必須）
//	POST   /api/v1/projects/{slug}/memory/{kind}/{name}/append?scope=  追記（{content, machine_label}。版を問わない。Idempotency-Key で再送を 1 回に畳む）
//	DELETE /api/v1/projects/{slug}/memory/{kind}/{name}?scope=      消した印の版を足す（If-Match は任意）
//	POST   /api/v1/projects/{slug}/memory/{kind}/{name}/purge?scope=  版の本体を消す（{version, reason} か {all_versions: true, reason}）
//
// どの応答も今の版を ETag に載せる（次の置き換えの If-Match にそのまま使える形）。

// memoryJSON は記憶 1 件（一覧では content を空にする）。
type memoryJSON struct {
	Project   string `json:"project"`
	Scope     string `json:"scope"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	Summary   string `json:"summary"`
	Bytes     int    `json:"bytes"`
	Content   string `json:"content,omitempty"`
	Deleted   bool   `json:"deleted"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

func toMemoryJSON(slug string, m store.Memory, withContent bool) memoryJSON {
	out := memoryJSON{Project: slug, Scope: m.Scope, Kind: m.Kind, Name: m.Name, Version: m.Version, Summary: m.Summary, Bytes: len(m.Content),
		Deleted: m.Deleted, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339), UpdatedBy: m.UpdatedByLogin}
	if withContent {
		out.Content = m.Content
	}
	return out
}

// memoryVersionJSON は版の履歴 1 件（本文は版を指定して取ったときだけ）。
type memoryVersionJSON struct {
	Version      int64  `json:"version"`
	Op           string `json:"op"`
	Summary      string `json:"summary"`
	User         string `json:"user"`
	MachineLabel string `json:"machine_label"`
	SessionID    string `json:"session_id"`
	Via          string `json:"via"`
	CreatedAt    string `json:"created_at"`
	WrittenAt    string `json:"written_at,omitempty"`
	Content      string `json:"content,omitempty"`
	Purged       bool   `json:"purged,omitempty"`
}

func toMemoryVersionJSON(v store.MemoryVersion) memoryVersionJSON {
	out := memoryVersionJSON{Version: v.Version, Op: v.Op, Summary: v.Summary, User: v.UserLogin, MachineLabel: v.MachineLabel, SessionID: v.SessionID,
		Via: v.Via, CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339), Content: v.Content, Purged: v.Purged}
	if !v.WrittenAt.IsZero() {
		out.WrittenAt = v.WrittenAt.UTC().Format(time.RFC3339)
	}
	return out
}

func setMemoryETag(w http.ResponseWriter, version int64) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(version, 10)+`"`)
}

// memoryKeyOf はパスとクエリから記憶のキーを作る。
func memoryKeyOf(r *http.Request) service.MemoryKey {
	return service.MemoryKey{Project: r.PathValue("slug"), Scope: r.URL.Query().Get("scope"), Kind: r.PathValue("kind"), Name: r.PathValue("name")}
}

type memoryWriteRequest struct {
	Scope        string `json:"scope,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Name         string `json:"name,omitempty"`
	Content      string `json:"content,omitempty"`
	MachineLabel string `json:"machine_label,omitempty"`
	// WrittenAt はクライアントが本文を作った時刻（追記・置き換え。RFC 3339）
	WrittenAt string `json:"written_at,omitempty"`
}

// apiListMemories は GET /projects/{slug}/memory。
func (s *Server) apiListMemories(w http.ResponseWriter, r *http.Request) {
	list, p, err := s.svc.ListMemories(r.Context(), actor(r), r.PathValue("slug"), r.URL.Query().Get("deleted") == "1")
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	out := make([]memoryJSON, 0, len(list))
	for _, m := range list {
		out = append(out, toMemoryJSON(p.Slug, m, false))
	}
	login, canWrite := s.svc.ProjectMemoryAccess(r.Context(), actor(r), p.Slug)
	writeJSON(w, http.StatusOK, map[string]any{"memories": out, "count": len(out), "can_write": canWrite, "user": login})
}

// apiGetMemory は GET /projects/{slug}/memory/{kind}/{name}。
func (s *Server) apiGetMemory(w http.ResponseWriter, r *http.Request) {
	key := memoryKeyOf(r)
	if raw := r.URL.Query().Get("version"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "service.err.memory.version_invalid"))
			return
		}
		v, err := s.svc.GetMemoryVersion(r.Context(), actor(r), key, n)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": toMemoryVersionJSON(*v)})
		return
	}
	d, err := s.svc.GetMemory(r.Context(), actor(r), key)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	vs := make([]memoryVersionJSON, 0, len(d.Versions))
	for _, v := range d.Versions {
		vs = append(vs, toMemoryVersionJSON(v))
	}
	setMemoryETag(w, d.Memory.Version)
	writeJSON(w, http.StatusOK, map[string]any{"memory": toMemoryJSON(d.ProjectSlug, d.Memory, true), "versions": vs})
}

// memoryWrite は書き込みの経路に共通の後半（service を呼び、応答を書く）。
func (s *Server) memoryWrite(w http.ResponseWriter, r *http.Request, in service.MemoryWriteInput) {
	res, err := s.svc.WriteMemory(r.Context(), actor(r), in)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	setMemoryETag(w, res.Memory.Version)
	writeJSON(w, status, map[string]any{"memory": toMemoryJSON(res.ProjectSlug, res.Memory, false), "created": res.Created})
}

// memoryKeyInBody は種類・名前・範囲を URL で指す経路の本文にそれらを書いた要求を 400 で拒む（黙って無視すると、踏んだ人が別の記憶に書いたことに気づけないため）。
func memoryKeyInBody(w http.ResponseWriter, r *http.Request, req memoryWriteRequest) bool {
	if req.Scope == "" && req.Kind == "" && req.Name == "" {
		return false
	}
	writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.memory_key_body"))
	return true
}

// apiCreateMemory は POST /projects/{slug}/memory。
func (s *Server) apiCreateMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryWriteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if r.URL.Query().Get("scope") != "" { // 作る経路の範囲は本文で渡す（クエリの値を黙って捨てないため）
		writeError(w, http.StatusBadRequest, "invalid_argument", i18n.T(reqLang(r), "server.api.err.memory_scope_query"))
		return
	}
	s.memoryWrite(w, r, service.MemoryWriteInput{MemoryKey: service.MemoryKey{Project: r.PathValue("slug"), Scope: req.Scope, Kind: req.Kind, Name: req.Name},
		Op: store.MemoryOpCreate, Content: req.Content, MachineLabel: req.MachineLabel})
}

// apiAppendMemory は POST /projects/{slug}/memory/{kind}/{name}/append。
func (s *Server) apiAppendMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryWriteRequest
	if !decodeJSON(w, r, &req) || memoryKeyInBody(w, r, req) {
		return
	}
	s.memoryWrite(w, r, service.MemoryWriteInput{MemoryKey: memoryKeyOf(r), Op: store.MemoryOpAppend, Content: req.Content, MachineLabel: req.MachineLabel,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), WrittenAt: req.WrittenAt})
}

// apiReplaceMemory は PUT /projects/{slug}/memory/{kind}/{name}。
func (s *Server) apiReplaceMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryWriteRequest
	if !decodeJSON(w, r, &req) || memoryKeyInBody(w, r, req) {
		return
	}
	s.memoryWrite(w, r, service.MemoryWriteInput{MemoryKey: memoryKeyOf(r), Op: store.MemoryOpReplace, Content: req.Content,
		IfMatch: r.Header.Get("If-Match"), MachineLabel: req.MachineLabel, WrittenAt: req.WrittenAt})
}

// apiDeleteMemory は DELETE /projects/{slug}/memory/{kind}/{name}（本文は任意。{machine_label}）。
func (s *Server) apiDeleteMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryWriteRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	if memoryKeyInBody(w, r, req) {
		return
	}
	s.memoryWrite(w, r, service.MemoryWriteInput{MemoryKey: memoryKeyOf(r), Op: store.MemoryOpDelete, IfMatch: r.Header.Get("If-Match"),
		MachineLabel: req.MachineLabel})
}

// apiPurgeMemory は POST /projects/{slug}/memory/{kind}/{name}/purge。
func (s *Server) apiPurgeMemory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version     int64  `json:"version"`
		AllVersions bool   `json:"all_versions"`
		Reason      string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.svc.PurgeMemory(r.Context(), actor(r), memoryKeyOf(r), service.MemoryPurgeSelect{Version: req.Version, AllVersions: req.AllVersions}, req.Reason)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": map[string]any{"bodies": res.Bodies, "bytes": res.Bytes, "current": res.Current}})
}

// ---- MCP（list_memories・get_memory・write_memory）

type (
	memoryKeyArg struct {
		projectArg
		Kind  string `json:"kind" jsonschema:"server.mcp.arg.memory.kind"`
		Name  string `json:"name,omitempty" jsonschema:"server.mcp.arg.memory.name"`
		Scope string `json:"scope,omitempty" jsonschema:"server.mcp.arg.memory.scope"`
	}
	listMemoriesIn struct {
		projectArg
		IncludeDeleted bool `json:"include_deleted,omitempty" jsonschema:"server.mcp.arg.list_memories.include_deleted"`
	}
	getMemoryIn struct {
		memoryKeyArg
		Version int64 `json:"version,omitempty" jsonschema:"server.mcp.arg.get_memory.version"`
	}
	writeMemoryIn struct {
		memoryKeyArg
		Op           string `json:"op" jsonschema:"server.mcp.arg.write_memory.op"`
		Content      string `json:"content,omitempty" jsonschema:"server.mcp.arg.write_memory.content"`
		Version      *int64 `json:"version,omitempty" jsonschema:"server.mcp.arg.write_memory.version"`
		MachineLabel string `json:"machine_label,omitempty" jsonschema:"server.mcp.arg.write_memory.machine_label"`
	}
)

func (s *Server) mcpMemoryKey(ctx context.Context, c *mcpCall, in memoryKeyArg) (service.MemoryKey, error) {
	slug, err := s.projectSlug(ctx, c, in.Project)
	if err != nil {
		return service.MemoryKey{}, err
	}
	return service.MemoryKey{Project: slug, Scope: in.Scope, Kind: in.Kind, Name: in.Name}, nil
}

func (s *Server) addMemoryMCPTools(srv *mcp.Server, lang i18n.Lang, ro *mcp.ToolAnnotations) {
	addTool(srv, lang, &mcp.Tool{Name: "list_memories", Annotations: ro, Description: i18n.T(lang, "server.mcp.tool.list_memories")},
		func(ctx context.Context, req *mcp.CallToolRequest, in listMemoriesIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			slug, err := s.projectSlug(ctx, c, in.Project)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "list_memories", err)
			}
			list, p, err := s.svc.ListMemories(ctx, c.actor, slug, in.IncludeDeleted)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "list_memories", err)
			}
			out := make([]memoryJSON, 0, len(list))
			var b strings.Builder
			for _, m := range list {
				out = append(out, toMemoryJSON(p.Slug, m, false))
				fmt.Fprintf(&b, "%s/%s  %s  v%d  %s\n", m.Kind, m.Name, m.Scope, m.Version, m.Summary)
			}
			text := strings.TrimSpace(b.String())
			if text == "" {
				text = i18n.T(c.lang, "server.mcp.memory.none", "project", p.Slug)
			}
			return result(text, map[string]any{"memories": out, "count": len(out)}), nil, nil
		})

	addTool(srv, lang, &mcp.Tool{Name: "get_memory", Annotations: ro, Description: i18n.T(lang, "server.mcp.tool.get_memory")},
		func(ctx context.Context, req *mcp.CallToolRequest, in getMemoryIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			key, err := s.mcpMemoryKey(ctx, c, in.memoryKeyArg)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "get_memory", err)
			}
			if in.Version > 0 {
				v, err := s.svc.GetMemoryVersion(ctx, c.actor, key, in.Version)
				if err != nil {
					return nil, nil, s.toolError(c.lang, "get_memory", err)
				}
				return result(fmt.Sprintf("version: %d\n\n%s", v.Version, v.Content), map[string]any{"version": toMemoryVersionJSON(*v)}), nil, nil
			}
			d, err := s.svc.GetMemory(ctx, c.actor, key)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "get_memory", err)
			}
			vs := make([]memoryVersionJSON, 0, len(d.Versions))
			for _, v := range d.Versions {
				vs = append(vs, toMemoryVersionJSON(v))
			}
			return result(fmt.Sprintf("version: %d\n\n%s", d.Memory.Version, d.Memory.Content),
				map[string]any{"memory": toMemoryJSON(d.ProjectSlug, d.Memory, true), "versions": vs}), nil, nil
		})

	notDestructive := false
	addTool(srv, lang, &mcp.Tool{Name: "write_memory", Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
		Description: i18n.T(lang, "server.mcp.tool.write_memory")},
		func(ctx context.Context, req *mcp.CallToolRequest, in writeMemoryIn) (*mcp.CallToolResult, any, error) {
			c, err := s.mcpCallOf(req)
			if err != nil {
				return nil, nil, err
			}
			key, err := s.mcpMemoryKey(ctx, c, in.memoryKeyArg)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "write_memory", err)
			}
			wi := service.MemoryWriteInput{MemoryKey: key, Op: in.Op, Content: in.Content, MachineLabel: in.MachineLabel}
			if in.Version != nil {
				wi.IfMatch = strconv.FormatInt(*in.Version, 10)
			}
			res, err := s.svc.WriteMemory(ctx, c.actor, wi)
			if err != nil {
				return nil, nil, s.toolError(c.lang, "write_memory", err)
			}
			m := res.Memory
			return result(i18n.T(c.lang, "server.mcp.memory.written", "op", in.Op, "kind", m.Kind, "name", m.Name, "scope", m.Scope, "version", m.Version),
				map[string]any{"memory": toMemoryJSON(res.ProjectSlug, m, false), "created": res.Created}), nil, nil
		})
}
