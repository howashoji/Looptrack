package docscheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 「イシューを変える MCP のツール」を表す一覧が、離れた 4 か所にある。
//
//   - サーバ: MCP のツールの登録（internal/server の mcp.AddTool）。変更系は Annotations に
//     DestructiveHint を持ち、読み取り専用は ReadOnlyHint を持つ。そのうち issue_events を
//     書くものが「付与の対象」になる。
//   - 付与の hook: internal/client/hook/core/usage.go の toolRe・copilotToolRe・opOf。
//   - loop の PreToolUse: internal/client/hook/loop/pretool.go の imWriteTools。
//   - 設計: docs/server/DESIGN.md の「MCP の変更系のツール」の一覧と、§9-5 の hook の仕様表。
//
// この 4 つは実際にずれていて（hook は 4 つ・loop は 8 つ・issue_events を書くのは 7 つ）、
// ずれていても何も落ちなかった。文書の中ですら 7 つと 4 つが同時に書かれていた。
// どれか 1 本だけを直すと、残りは黙って古いままになる。
//
// そこで、**一覧をもう 1 本手で書き写すのではなく**、4 か所を実物から読み取って
// 1 つの宣言表（mcpWriteTools）と突き合わせる。宣言表は「どのツールが、どの一覧に、なぜ入るか」を
// 書く唯一の場所で、意図してずらしているもの（verify_issue・add_usage_ledger）も、
// まだ直していないずれ（next・report_verify・assign_issue）も、理由の欄が空なら落ちる。
//
// 実行: go test -count=1 ./internal/docscheck/（DB は要らない）

// mcpWriteTool は 1 つの MCP のツールが、4 つの一覧のどれに入るかの宣言。
//
// この構造体の値（mcpWriteTools）が唯一の期待値で、ほかはすべて実物から読み取る。
type mcpWriteTool struct {
	tool string // MCP のツール名（mcp.AddTool の Name）

	// serverWrite は、サーバの登録で変更系（Annotations が読み取り専用でない）かどうか。
	serverWrite bool
	// event は issue_events に書く kind（空 = 書かない）。付与の対象はここが空でないもの。
	event string
	// hookOp は internal/client/hook/core/usage.go の opOf の値（空 = 付与の hook が拾わない）。
	hookOp string
	// inPretool は internal/client/hook/loop/pretool.go の imWriteTools に入るか。
	inPretool bool

	// why は 4 つの一覧で扱いが違うときの理由。扱いが揃っているときは空でなければならない
	// （揃ったのに理由が残っていると、次に読む人が「まだずれている」と誤読する）。
	why string
}

// mcpWriteTools は宣言表。**一覧を書くのはここ 1 か所だけ**。
//
// 「扱いが揃っている」= serverWrite・event・hookOp・inPretool の 4 つが同じ側（すべて有・すべて無）。
// 揃っていないものは why を必ず書く。
var mcpWriteTools = []mcpWriteTool{
	{tool: "create_issue", serverWrite: true, event: "create", hookOp: "create", inPretool: true},
	{tool: "add_comment", serverWrite: true, event: "comment", hookOp: "comment", inPretool: true},
	{tool: "set_status", serverWrite: true, event: "status", hookOp: "status", inPretool: true},
	{tool: "update_issue", serverWrite: true, event: "update", hookOp: "update", inPretool: true},

	{tool: "assign_issue", serverWrite: true, event: "assign", inPretool: true,
		why: "付与の hook（core/usage.go）が拾わない。担当者だけを変えた操作はトークン情報が送られない。" +
			"未付与の検知の SQL（internal/store/usage.go）は assign を数えるので、取りこぼしは未付与の一覧には出る"},
	{tool: "next", serverWrite: true, event: "status", inPretool: false,
		why: "付与の hook が拾わない（着手で kind status を書くのに、トークン情報が送られない）。" +
			"loop の PreToolUse も見ていない（着手は他プロジェクトへの誤爆の確認が要る操作ではないという判断）"},
	{tool: "report_verify", serverWrite: true, event: "verify", inPretool: true,
		why: "付与の hook が拾わない（kind verify を書くのに、トークン情報が送られない）"},

	{tool: "add_usage_ledger", serverWrite: true, inPretool: true,
		why: "台帳（usage_ledger）だけを書き、issue_events を書かないので付与の対象ではない。" +
			"loop の PreToolUse は「取り消せない書き込み」として確認の対象に入れている"},
	{tool: "verify_issue", inPretool: true,
		why: "読み取り専用（ReadOnlyHint）で何も書かないが、loop の PreToolUse は" +
			"他プロジェクトのイシューを読み違えないように確認の対象に入れている"},
}

// mcpWriteToolsByName は宣言表を名前で引く。
func mcpWriteToolsByName(t *testing.T) map[string]mcpWriteTool {
	t.Helper()
	out := map[string]mcpWriteTool{}
	for _, w := range mcpWriteTools {
		if _, dup := out[w.tool]; dup {
			t.Fatalf("宣言表 mcpWriteTools に %s が 2 回あります", w.tool)
		}
		out[w.tool] = w
	}
	return out
}

// declared は宣言表から、条件に合うツール名の集合を作る。
func declared(pick func(mcpWriteTool) bool) map[string]bool {
	out := map[string]bool{}
	for _, w := range mcpWriteTools {
		if pick(w) {
			out[w.tool] = true
		}
	}
	return out
}

// TestMCPWriteToolSetsAgree は、4 か所の一覧が宣言表と一致することを確かめる。
//
// どれか 1 本だけにツールが増減したら、そのツール名と「どの一覧に無いか」を出して落ちる。
func TestMCPWriteToolSetsAgree(t *testing.T) {
	byName := mcpWriteToolsByName(t)

	// ① サーバの登録（mcp.AddTool）。
	regs := parseMCPRegistrations(t)
	if len(regs) == 0 {
		t.Fatal("internal/server から mcp.AddTool の登録を 1 件も拾えません（解析が空振りしています）")
	}
	for name := range byName {
		if _, ok := regs[name]; !ok {
			t.Errorf("宣言表のツール %s が internal/server の mcp.AddTool に登録されていません"+
				"（ツールを消したなら宣言表の行も消してください）", name)
		}
	}
	serverWrite := map[string]bool{}
	for name, reg := range regs {
		if !reg.readOnly {
			serverWrite[name] = true
		}
	}
	diffSets(t, "サーバの変更系のツール（internal/server の mcp.AddTool・Annotations が読み取り専用でないもの）",
		serverWrite, declared(func(w mcpWriteTool) bool { return w.serverWrite }))

	// ② 付与の hook（core/usage.go）。正規表現 2 本と opOf の 3 つが互いに一致することも確かめる。
	toolAlt, copilotAlt, opOf := parseUsageHookTools(t)
	diffSets(t, "付与の hook の toolRe（internal/client/hook/core/usage.go）",
		toolAlt, declared(func(w mcpWriteTool) bool { return w.hookOp != "" }))
	diffSets(t, "付与の hook の copilotToolRe（internal/client/hook/core/usage.go）",
		copilotAlt, declared(func(w mcpWriteTool) bool { return w.hookOp != "" }))
	opKeys := map[string]bool{}
	for k := range opOf {
		opKeys[k] = true
	}
	diffSets(t, "付与の hook の opOf（internal/client/hook/core/usage.go）",
		opKeys, declared(func(w mcpWriteTool) bool { return w.hookOp != "" }))
	for name, op := range opOf {
		if w, ok := byName[name]; ok && w.hookOp != op {
			t.Errorf("付与の hook の opOf が %s → %q ですが、宣言表は %q です", name, op, w.hookOp)
		}
	}

	// ③ loop の PreToolUse（loop/pretool.go の imWriteTools）。
	pretool := parseLoopPretoolTools(t)
	diffSets(t, "loop の PreToolUse の imWriteTools（internal/client/hook/loop/pretool.go）",
		pretool, declared(func(w mcpWriteTool) bool { return w.inPretool }))

	// ④ issue_events を書くか（サーバのハンドラ → internal/service → store.InsertEvent）。
	writers := parseIssueEventWriters(t, regs)
	diffSets(t, "issue_events を書く MCP のツール（mcp.AddTool のハンドラから store.InsertEvent へ辿れるもの）",
		writers, declared(func(w mcpWriteTool) bool { return w.event != "" }))

	// ⑤ 扱いが揃っていないものには理由が要る（揃っているものに理由が残っていたら消す）。
	for _, w := range mcpWriteTools {
		uniform := w.serverWrite == (w.event != "") &&
			w.serverWrite == (w.hookOp != "") && w.serverWrite == w.inPretool
		switch {
		case !uniform && strings.TrimSpace(w.why) == "":
			t.Errorf("%s は 4 つの一覧で扱いが違います（サーバの変更系=%v・issue_events=%q・hook の op=%q・"+
				"loop の PreToolUse=%v）。宣言表の why に理由を書いてください",
				w.tool, w.serverWrite, w.event, w.hookOp, w.inPretool)
		case uniform && strings.TrimSpace(w.why) != "":
			t.Errorf("%s は 4 つの一覧で扱いが揃っているのに、宣言表に why が残っています"+
				"（揃ったら消してください。残っていると、次に読む人がまだずれていると読みます）: %s", w.tool, w.why)
		}
	}
}

// diffSets は実物から読み取った集合と宣言表を突き合わせ、どちらに無いかを出す。
func diffSets(t *testing.T, what string, actual, want map[string]bool) {
	t.Helper()
	if len(actual) == 0 {
		t.Fatalf("%s を 1 件も拾えません（解析が空振りしています。実物の書き方が変わっていないか確かめてください）", what)
	}
	for _, name := range sortedSet(actual) {
		if !want[name] {
			t.Errorf("%s に %s がありますが、宣言表（mcpWriteTools）にはその扱いがありません"+
				"（ツールを足したなら宣言表の行を直してください）", what, name)
		}
	}
	for _, name := range sortedSet(want) {
		if !actual[name] {
			t.Errorf("宣言表（mcpWriteTools）は %s が %s に入ると書いていますが、実物にはありません"+
				"（実物を直したなら宣言表の行も直してください）", name, what)
		}
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// goFilesUnder は rel（repoRoot からの相対）の直下の *.go を返す（_test.go は外す）。
func goFilesUnder(t *testing.T, rel string) []string {
	t.Helper()
	dir := filepath.Join(repoRoot, rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

func parseGo(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return fset, f
}

// mcpRegistration は mcp.AddTool（と addTool）の 1 件（ツール名・読み取り専用か・ハンドラ）。
type mcpRegistration struct {
	readOnly bool
	handler  ast.Node // Tool の次の引数（ハンドラ）
}

// parseMCPRegistrations は internal/server の mcp.AddTool（と addTool）の登録を読む。
//
// 読み取り専用の判定: Annotations が ReadOnlyHint: true を持つ複合リテラルか、
// ReadOnlyHint: true を代入した変数（このパッケージでは ro）。変更系は DestructiveHint を持つ。
// どちらとも取れない書き方が現れたら、黙って片側に寄せずに落とす。
func parseMCPRegistrations(t *testing.T) map[string]mcpRegistration {
	t.Helper()
	files := goFilesUnder(t, filepath.Join("internal", "server"))
	roVars := map[string]bool{} // ReadOnlyHint: true を持つ変数の名前
	var parsed []*ast.File
	for _, p := range files {
		_, f := parseGo(t, p)
		parsed = append(parsed, f)
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			id, ok := as.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if readOnlyHint(as.Rhs[0]) {
				roVars[id.Name] = true
			}
			return true
		})
	}
	if len(roVars) == 0 {
		t.Fatal("internal/server に ReadOnlyHint: true を持つ変数（ro）が見つかりません（解析が空振りしています）")
	}
	out := map[string]mcpRegistration{}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// 登録は mcp.AddTool(srv, &mcp.Tool{…}, h) か、接続の言語で説明を引いて登録する
			// addTool(srv, lang, &mcp.Tool{…}, h)（internal/server/mcp_tooldef.go）。Tool の位置が 1 つずれる。
			at := -1
			switch {
			case len(call.Args) >= 3 && isSelector(call.Fun, "mcp", "AddTool"):
				at = 1
			case len(call.Args) >= 4 && isIdent(call.Fun, "addTool"):
				at = 2
			}
			if at < 0 {
				return true
			}
			lit := compositeOf(call.Args[at])
			if lit == nil {
				return true
			}
			name, _ := stringField(lit, "Name")
			if name == "" {
				return true
			}
			ann := fieldValue(lit, "Annotations")
			if ann == nil {
				t.Errorf("mcp.AddTool の %s に Annotations がありません（読み取り専用か変更系かが判定できません）", name)
				return true
			}
			ro := readOnlyHint(ann)
			if !ro && !hasField(ann, "DestructiveHint") {
				if id, ok := ann.(*ast.Ident); ok && roVars[id.Name] {
					ro = true
				} else {
					t.Errorf("mcp.AddTool の %s の Annotations が ReadOnlyHint でも DestructiveHint でもありません"+
						"（この検査はこの 2 つで変更系かを見分けています）", name)
					return true
				}
			}
			out[name] = mcpRegistration{readOnly: ro, handler: call.Args[at+1]}
			return true
		})
	}
	return out
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// compositeOf は式（&T{...} でも T{...} でも）から複合リテラルを取る。
func compositeOf(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	lit, _ := e.(*ast.CompositeLit)
	return lit
}

func fieldValue(e ast.Expr, name string) ast.Expr {
	lit := compositeOf(e)
	if lit == nil {
		return nil
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
			return kv.Value
		}
	}
	return nil
}

func hasField(e ast.Expr, name string) bool { return fieldValue(e, name) != nil }

func readOnlyHint(e ast.Expr) bool {
	v := fieldValue(e, "ReadOnlyHint")
	if v == nil {
		return false
	}
	id, ok := v.(*ast.Ident)
	return ok && id.Name == "true"
}

// stringField は複合リテラルの文字列の項目を取る（連結した文字列は先頭だけで足りるので取らない）。
func stringField(lit *ast.CompositeLit, name string) (string, bool) {
	v := fieldValue(lit, name)
	if v == nil {
		return "", false
	}
	s, ok := stringLit(v)
	return s, ok
}

func stringLit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// namedGroup は正規表現の (?P<tool>a|b|c) から a|b|c を取る。
var namedGroup = regexp.MustCompile(`\(\?P<tool>([^)]*)\)`)

// parseUsageHookTools は internal/client/hook/core/usage.go の toolRe・copilotToolRe・opOf を読む。
func parseUsageHookTools(t *testing.T) (toolAlt, copilotAlt map[string]bool, opOf map[string]string) {
	t.Helper()
	path := filepath.Join(repoRoot, "internal", "client", "hook", "core", "usage.go")
	_, f := parseGo(t, path)
	res := map[string]map[string]bool{}
	opOf = map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, nm := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				switch nm.Name {
				case "toolRe", "copilotToolRe":
					call, ok := vs.Values[i].(*ast.CallExpr)
					if !ok || len(call.Args) != 1 {
						continue
					}
					pat, ok := stringLit(call.Args[0])
					if !ok {
						continue
					}
					m := namedGroup.FindStringSubmatch(pat)
					if m == nil {
						t.Fatalf("%s の (?P<tool>…) を読み取れません: %s", nm.Name, pat)
					}
					set := map[string]bool{}
					for _, s := range strings.Split(m[1], "|") {
						set[s] = true
					}
					res[nm.Name] = set
				case "opOf":
					lit := compositeOf(vs.Values[i])
					if lit == nil {
						continue
					}
					for _, el := range lit.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k, ok1 := stringLit(kv.Key)
						v, ok2 := stringLit(kv.Value)
						if ok1 && ok2 {
							opOf[k] = v
						}
					}
				}
			}
		}
	}
	if res["toolRe"] == nil || res["copilotToolRe"] == nil || len(opOf) == 0 {
		t.Fatalf("%s から toolRe・copilotToolRe・opOf を読み取れません（書き方が変わっていないか確かめてください）", path)
	}
	return res["toolRe"], res["copilotToolRe"], opOf
}

// parseLoopPretoolTools は internal/client/hook/loop/pretool.go の imWriteTools の要素を読む。
func parseLoopPretoolTools(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(repoRoot, "internal", "client", "hook", "loop", "pretool.go")
	_, f := parseGo(t, path)
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, nm := range vs.Names {
			if nm.Name != "imWriteTools" || i >= len(vs.Values) {
				continue
			}
			lit := compositeOf(vs.Values[i])
			if lit == nil {
				continue
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				k, ok1 := stringLit(kv.Key)
				id, ok2 := kv.Value.(*ast.Ident)
				if ok1 && ok2 && id.Name == "true" {
					out[k] = true
				}
			}
		}
		return true
	})
	return out
}

// parseIssueEventWriters は、mcp.AddTool のハンドラから store.InsertEvent へ辿れるツールを返す。
//
// 辿り方は 2 段。① ハンドラから internal/server の中を辿り、道中に現れる s.svc.X(...)
// （サーバからドメイン操作を呼ぶ唯一の形）を集める。ハンドラが直接呼ぶとは限らないので 1 段では足りない
// （assign_issue は s.mcpAssign を経由する）。② 集めた X を起点に internal/service の中だけを辿り、
// store.InsertEvent に届くかを見る。
//
// 名前だけで辿るので型は見ていない（同じ名前の関数が複数あれば広めに出る）。
// 2 段に分けて s.svc.X で絞るのは、サーバ側の共通のヘルパ（応答の整形・エラーの訳）から
// internal/service 全体へ広がるのを止めるため（絞らないと読み取り専用のツールまで「書く」と出た）。
func parseIssueEventWriters(t *testing.T, regs map[string]mcpRegistration) map[string]bool {
	t.Helper()
	const target = "InsertEvent"
	serverBodies := funcBodies(t, filepath.Join("internal", "server"))
	serviceBodies := funcBodies(t, filepath.Join("internal", "service"))
	if len(serviceBodies[target]) != 0 {
		t.Fatalf("internal/service に %s という名前の関数があります（この検査は %s を internal/store のものとして見ています）", target, target)
	}
	out := map[string]bool{}
	seeded := 0
	for name, reg := range regs {
		seeds := reachableServiceCalls(serverBodies, reg.handler)
		seeded += len(seeds)
		if reachesCall(serviceBodies, seeds, target) {
			out[name] = true
		}
	}
	if seeded == 0 {
		t.Fatal("mcp.AddTool のハンドラから s.svc.X(...) の呼び出しを 1 件も拾えません（書き方が変わっていないか確かめてください）")
	}
	return out
}

// funcBodies は rel の下の関数・メソッドの本体を名前で引ける形で返す。
func funcBodies(t *testing.T, rel string) map[string][]ast.Node {
	t.Helper()
	out := map[string][]ast.Node{}
	for _, p := range goFilesUnder(t, rel) {
		_, f := parseGo(t, p)
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			out[fd.Name.Name] = append(out[fd.Name.Name], fd.Body)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s の関数を 1 件も拾えません（解析が空振りしています）", rel)
	}
	return out
}

// reachableServiceCalls は、ハンドラ本体と、ハンドラが直接呼ぶ internal/server の関数の中から、
// s.svc.X(...) の X を集める。
//
// 1 段だけ降りるのは、assign_issue が s.mcpAssign へ委譲しているから。深さを増やすと、
// 共通のヘルパ（s.toolError など）から REST 側の書き込みの関数へ名前で繋がり、
// 読み取り専用のツールまで「書く」と出る（実測: 深さを無制限にすると 23 件すべてが「書く」になった）。
// 2 段以上の委譲を足した人は、そのツールが「書かない」側に落ちてこの検査が失敗するので気づく。
func reachableServiceCalls(serverBodies map[string][]ast.Node, start ast.Node) []string {
	out := serviceCalls(start)
	seen := map[string]bool{}
	for _, name := range calledNames(start) {
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, b := range serverBodies[name] {
			out = append(out, serviceCalls(b)...)
		}
	}
	return out
}

// serviceCalls は節の中の s.svc.X(...) の X を返す（サーバからドメイン操作を呼ぶ唯一の形）。
func serviceCalls(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "svc" {
			return true
		}
		if id, ok := inner.X.(*ast.Ident); ok && id.Name == "s" {
			out = append(out, sel.Sel.Name)
		}
		return true
	})
	return out
}

// reachesCall は起点の名前から呼び出しを辿って target に届くかを返す（名前だけの近似）。
func reachesCall(bodies map[string][]ast.Node, start []string, target string) bool {
	seen := map[string]bool{}
	var queue []ast.Node
	for _, name := range start {
		if name == target {
			return true
		}
		if !seen[name] {
			seen[name] = true
			queue = append(queue, bodies[name]...)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, name := range calledNames(n) {
			if name == target {
				return true
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			queue = append(queue, bodies[name]...)
		}
	}
	return false
}

// calledNames は節の中の呼び出しの名前（f(...) の f、x.f(...) の f）を返す。
func calledNames(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			out = append(out, fun.Name)
		case *ast.SelectorExpr:
			out = append(out, fun.Sel.Name)
		}
		return true
	})
	return out
}

// designWriteToolsLine は DESIGN.md の「MCP の変更系のツール（…）」の行を探す目印。
var designWriteToolsLine = regexp.MustCompile("^- MCP の変更系のツール（(.+?)）は、")

// designHookRow は §9-5 の hook の仕様表の Claude Code の PostToolUse の行を探す目印。
// 「| Claude Code | PostToolUse」で始まる行は §9-5 より前の表（AI ごとの hook の有無）にもあるので、
// LOOPTRACK_MCP_SERVER を書いている行だけに絞る（合う行がちょうど 1 本でなければ落とす）。
var designHookRow = regexp.MustCompile("^\\| Claude Code \\| PostToolUse.*`LOOPTRACK_MCP_SERVER`")

// designHookTools は仕様表の行から「ツール名の末尾が `a` / `b` … で」の一覧を取る。
var designHookTools = regexp.MustCompile("ツール名の末尾が (`[a-z_]+`(?: / `[a-z_]+`)*) で")

// designMCPServerDefault は仕様表の行から `LOOPTRACK_MCP_SERVER`（既定 `x`）の x を取る。
var designMCPServerDefault = regexp.MustCompile("`LOOPTRACK_MCP_SERVER`（既定 `([^`]*)`）")

var backticked = regexp.MustCompile("`([a-z_]+)`")

// TestDesignMCPToolListsMatchCode は、DESIGN.md の 2 つの一覧が実物と合っていることを確かめる。
//
// この 2 つは同じ文書の中で 7 つと 4 つに食い違っていた（どちらも「MCP の変更系のツール」に見える書き方）。
// 数が違うこと自体は正しい（hook が拾う範囲は狭い）ので、それぞれが実物と合っているかを見る。
func TestDesignMCPToolListsMatchCode(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "docs", "server", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	var writeLines, hookRows []string
	for _, line := range strings.Split(string(b), "\n") {
		if m := designWriteToolsLine.FindStringSubmatch(line); m != nil {
			writeLines = append(writeLines, m[1])
		}
		if designHookRow.MatchString(line) {
			hookRows = append(hookRows, line)
		}
	}
	// 目印に合う行が増えると、どれを見ているかが分からなくなる（1 本だけのはず）。
	if len(writeLines) != 1 {
		t.Fatalf("DESIGN.md の「- MCP の変更系のツール（…）は、」の行が %d 本です（1 本のはず）", len(writeLines))
	}
	if len(hookRows) != 1 {
		t.Fatalf("DESIGN.md の hook の仕様表の Claude Code の PostToolUse の行（LOOPTRACK_MCP_SERVER を書いている行）が %d 本です（1 本のはず）", len(hookRows))
	}
	writeLine, hookRow := writeLines[0], hookRows[0]

	// ① 「MCP の変更系のツール」の一覧 = issue_events を書くツール。
	got := map[string]bool{}
	for _, m := range backticked.FindAllStringSubmatch(writeLine, -1) {
		got[m[1]] = true
	}
	diffSets(t, "DESIGN.md の「MCP の変更系のツール」の一覧", got,
		declared(func(w mcpWriteTool) bool { return w.event != "" }))

	// ② hook の仕様表の一覧 = 付与の hook が実際に拾うツール。
	m := designHookTools.FindStringSubmatch(hookRow)
	if m == nil {
		t.Fatal("DESIGN.md の hook の仕様表から「ツール名の末尾が `…` で」の一覧を読み取れません")
	}
	hookGot := map[string]bool{}
	for _, mm := range backticked.FindAllStringSubmatch(m[1], -1) {
		hookGot[mm[1]] = true
	}
	diffSets(t, "DESIGN.md の hook の仕様表のツールの一覧", hookGot,
		declared(func(w mcpWriteTool) bool { return w.hookOp != "" }))

	// ③ LOOPTRACK_MCP_SERVER の既定値が実物と一致している。
	d := designMCPServerDefault.FindStringSubmatch(hookRow)
	if d == nil {
		t.Fatal("DESIGN.md の hook の仕様表から `LOOPTRACK_MCP_SERVER`（既定 `…`）を読み取れません")
	}
	if want := mcpServerDefault(t); d[1] != want {
		t.Errorf("DESIGN.md の hook の仕様表は LOOPTRACK_MCP_SERVER の既定を %q と書いていますが、"+
			"実物（internal/client/hook/core/usage.go の pat への代入）は %q です", d[1], want)
	}
}

// mcpServerDefault は internal/client/hook/core/usage.go の pat（MCP_SERVER が空のときの既定）を読む。
func mcpServerDefault(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot, "internal", "client", "hook", "core", "usage.go")
	_, f := parseGo(t, path)
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != "pat" {
			return true
		}
		if s, ok := stringLit(as.Rhs[0]); ok {
			found = append(found, s)
		}
		return true
	})
	if len(found) != 1 {
		t.Fatalf("usage.go の pat への文字列の代入が %d 件です（1 件のはず。書き方が変わっていないか確かめてください）", len(found))
	}
	return found[0]
}
