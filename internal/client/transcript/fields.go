package transcript

import "regexp"

// 書き換える欄（DESIGN.md §9-8「パスの書き換え」の表）。キーの名前で選び、深さは問わない
// （形式の版が上がって入れ子が変わっても当たるように）。値（種類を表す "type" の値など）では選ばない。
//
// ruleHead は値の頭のパスだけを置き換える欄、ruleAnywhere は値の中のどこにでもパスが出る欄、
// ruleCommandHead はコマンド行の欄（先頭の語だけを置き換え、残りは書き換えずに数える）。
// どれでもない欄（散文・tool_result・base64・差分・暗号化された欄）は書き換えず、送る端末のパスが残っていれば数える。
type fieldRule int

const (
	ruleNone fieldRule = iota
	ruleHead
	ruleAnywhere
	ruleCommandHead
)

// fieldKey は文字列の欄の名前（配列の要素なら、その配列のキー）。
func fieldKey(path []seg) (string, int) {
	for i := len(path) - 1; i >= 0; i-- {
		if !path[i].index {
			return path[i].key, i
		}
	}
	return "", -1
}

// hasAncestor は path[:upto] に name のキーがあるか。
func hasAncestor(path []seg, upto int, name string) bool {
	for i := 0; i < upto && i < len(path); i++ {
		if !path[i].index && path[i].key == name {
			return true
		}
	}
	return false
}

// claudeRule は Claude Code の記録の欄の扱い。
//   - 値の頭: cwd・live_cwd・serverClassifierContext の中の root・attachment の中の workingDirectory と
//     scratchpadDirectory・attachment.files[].path・toolUseResult の outputFile・transcriptDir と、名前が Path で終わる欄・
//     toolUseResult の中の path（wireIngestContext.*.cwd は cwd として当たる）。
//   - 中のどこでも: tool_use の input（content[].input の下の文字列すべて）と、同じ入力を写した wireToolInputs の下。
//   - コマンド行の先頭の語: hookInfos と attachment の中の command（hook の実行ファイルの絶対パスで始まる。
//     値に空白があるので値の頭の規則には当たらず、引数は散文と同じく書き換えない）。
func claudeRule(path []seg) fieldRule {
	for i := range path {
		if path[i].index {
			continue
		}
		switch path[i].key {
		case "wireToolInputs":
			if i < len(path)-1 {
				return ruleAnywhere
			}
		case "input":
			if i >= 2 && path[i-1].index && !path[i-2].index && path[i-2].key == "content" {
				return ruleAnywhere
			}
		}
	}
	k, at := fieldKey(path)
	switch k {
	case "cwd", "live_cwd":
		return ruleHead
	case "root":
		if hasAncestor(path, at, "serverClassifierContext") {
			return ruleHead
		}
	case "workingDirectory", "scratchpadDirectory":
		if hasAncestor(path, at, "attachment") {
			return ruleHead
		}
	case "path":
		if at >= 2 && path[at-1].index && path[at-2].key == "files" && hasAncestor(path, at, "attachment") {
			return ruleHead
		}
	case "command":
		if hasAncestor(path, at, "hookInfos") || hasAncestor(path, at, "attachment") {
			return ruleCommandHead
		}
	}
	if hasAncestor(path, at, "toolUseResult") {
		if k == "outputFile" || k == "transcriptDir" || k == "path" || pathSuffixRe.MatchString(k) {
			return ruleHead
		}
	}
	return ruleNone
}

var pathSuffixRe = regexp.MustCompile(`^[a-z][A-Za-z]*Path$`)

// codexRule は Codex の rollout の欄の扱い。
// 欄の名前は openai/codex のソースを読んで選び、利用者の端末の実測（codex-cli 0.156.1 の rollout 84 件の
// キーと件数。値は見ていない）で足した。実物に無い名前（local_shell_call）も、古い版の rollout のために残す。
//   - 値の頭: cwd（session_meta.payload・turn_context.payload・thread_settings_applied・world_state の環境）・
//     runtime_workspace_roots[]・workspace_roots[]・サンドボックスの writable_roots[]（古い形）と、新しい形の
//     書き込める根 entries[].path.path（file_system_sandbox_policy と permission_profile の下だけ）・
//     working_directory・workdir・agents_md の中の directory・parsed_cmd[].path・changes の中の move_path。
//   - 中のどこでも: arguments の名前の欄（function_call の、JSON を文字列にしたもの）と、executed_tool_calls[] の
//     arguments の下・local_shell_call の action の下・行の payload の直下の input（custom_tool_call の exec の入力と
//     apply_patch の差分の文字列）の下・item の command[] と parsed_cmd[].cmd（function_call の arguments と同じコマンド）。
//     input は種類（payload.type の値）では選ばず、位置（payload の直下）で絞る。ほかの欄の中の input は書き換えない。
func codexRule(path []seg) fieldRule {
	k, at := fieldKey(path)
	if k == "arguments" || hasAncestor(path, at+1, "action") ||
		(hasAncestor(path, at, "arguments") && hasAncestor(path, at, "executed_tool_calls")) ||
		(len(path) >= 2 && !path[0].index && path[0].key == "payload" && !path[1].index && path[1].key == "input") {
		return ruleAnywhere
	}
	switch k {
	case "cwd", "runtime_workspace_roots", "workspace_roots", "writable_roots", "working_directory", "workdir":
		return ruleHead
	case "command":
		if hasAncestor(path, at, "item") {
			return ruleAnywhere
		}
	case "cmd":
		if hasAncestor(path, at, "parsed_cmd") {
			return ruleAnywhere
		}
	case "path":
		if hasAncestor(path, at, "parsed_cmd") ||
			(hasAncestor(path, at, "entries") && (hasAncestor(path, at, "file_system_sandbox_policy") || hasAncestor(path, at, "permission_profile"))) {
			return ruleHead
		}
	case "directory":
		if hasAncestor(path, at, "agents_md") {
			return ruleHead
		}
	case "move_path":
		if hasAncestor(path, at, "changes") {
			return ruleHead
		}
	}
	return ruleNone
}

// codexKeyRule は Codex の rollout で、キーそのものの頭を書き換えるオブジェクトか（path はキーの持ち主のオブジェクトの位置）。
// item の changes（パッチで変えたファイルの一覧）は、変えたファイルの絶対パスをキーにした dict で書かれる。
// キーも値の頭と同じプレースホルダにするので、戻すときは値と同じく字面の上で戻る（可逆）。
// ただし書き換えるのは、そのオブジェクトのキーがすべて同じ 1 つの種類に書き換わるときだけ（keyRewritable）。
func codexKeyRule(path []seg) bool {
	if len(path) == 0 || path[len(path)-1].index || path[len(path)-1].key != "changes" {
		return false
	}
	return hasAncestor(path, len(path)-1, "item")
}

// identRe は件数の表に名前を出してよいキー（識別子の形）。それ以外（パス・値から作られたキー）は * にする。
var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$-]{0,47}$`)

// fieldName は件数の表に出す欄の名前（キーの経路。要素は []、識別子の形でないキーは *）。値は出さない。
func fieldName(path []seg) string {
	b := make([]byte, 0, 64)
	for i, s := range path {
		if s.index {
			b = append(b, "[]"...)
			continue
		}
		if i > 0 {
			b = append(b, '.')
		}
		if identRe.MatchString(s.key) {
			b = append(b, s.key...)
		} else {
			b = append(b, '*')
		}
	}
	return string(b)
}
