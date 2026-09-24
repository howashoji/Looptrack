package cli

import "github.com/howashoji/looptrack/internal/i18n"

// issue のサブコマンドと引数（以前の CLI（1.0.0 より前）と同じ並び・同じ名前・同じ既定値）。
// Run が nil のものは未実装（Planned のイシューで実装する）。引数だけは先に揃えておき、互換を 1 か所で確かめる。

var (
	statuses   = []string{"Backlog", "Todo", "In Progress", "In Review", "Done", "Canceled"}
	types      = []string{"requirement", "design", "task", "bug", "test", "epic"}
	priorities = []string{"P0", "P1", "P2", "P3"}
	sortKeys   = []string{"priority", "id", "updated", "created", "status", "type", "title"}
	agents     = []string{"claude-code", "codex", "copilot", "other"}
)

// 既定値（以前の CLI と同じ）。
const (
	verifyTimeout      = 600.0
	verifyTotalTimeout = 1800.0
)

func sortArgs(lang i18n.Lang) []*Arg {
	return []*Arg{
		{Name: "--sort", Default: "priority", Choices: sortKeys, Help: i18n.T(lang, "cli.arg.sort")},
		{Name: "--reverse", Kind: Bool, Help: i18n.T(lang, "cli.arg.reverse")},
	}
}

func jsonArg(lang i18n.Lang) *Arg {
	return &Arg{Name: "--json", Kind: Bool, Help: i18n.T(lang, "cli.arg.json")}
}

func join(lists ...[]*Arg) []*Arg {
	var out []*Arg
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// IssueCommand は `looptrack issue` の引数の木。
func IssueCommand(lang i18n.Lang) *Command {
	return &Command{
		Name:        "issue",
		Description: i18n.T(lang, "cli.cmd.root.desc"),
		Subs: []*Command{
			{Name: "new", Help: i18n.T(lang, "cli.cmd.new"), Run: cmdNew, Args: []*Arg{
				{Name: "title"},
				{Name: "--type", Default: "task", Help: i18n.T(lang, "cli.arg.new.type")},
				{Name: "--status", Default: "Todo", Help: i18n.T(lang, "cli.arg.statuses")},
				{Name: "--priority", Default: "P2", Help: i18n.T(lang, "cli.arg.new.priority")},
				{Name: "--labels", Default: "", Help: i18n.T(lang, "cli.arg.new.labels")},
				{Name: "--parent", Default: ""},
				{Name: "--blocked-by", Dest: "blocked_by", Default: "", Help: i18n.T(lang, "cli.arg.new.blocked_by")},
				{Name: "--traces", Default: "", Help: i18n.T(lang, "cli.arg.new.traces")},
				{Name: "--refs", Default: "", Help: i18n.T(lang, "cli.arg.new.refs")},
				{Name: "--body", Default: "", Help: i18n.T(lang, "cli.arg.new.body")},
				{Name: "--override", Default: "", Help: i18n.T(lang, "cli.arg.override")},
				{Name: "--assignee", Default: "", Help: i18n.T(lang, "cli.arg.new.assignee")},
			}},
			{Name: "list", Help: i18n.T(lang, "cli.cmd.list"), Run: cmdList, Args: join([]*Arg{
				{Name: "--status"},
				{Name: "--type"},
				{Name: "--label"},
				{Name: "--ref", Help: i18n.T(lang, "cli.arg.ref")},
				{Name: "--all", Kind: Bool, Help: i18n.T(lang, "cli.arg.all_closed")},
				{Name: "--assignee", Default: "", Help: i18n.T(lang, "cli.arg.assignee_filter")},
				{Name: "--has-feedback", Kind: Bool, Help: i18n.T(lang, "cli.arg.list.has_feedback")},
			}, sortArgs(lang), []*Arg{jsonArg(lang)})},
			{Name: "export", Help: i18n.T(lang, "cli.cmd.export"), Run: cmdExport, Args: join([]*Arg{
				{Name: "--xlsx", Required: true, Metavar: "PATH", Help: i18n.T(lang, "cli.arg.export.xlsx")},
				{Name: "--status"},
				{Name: "--type"},
				{Name: "--label"},
				{Name: "--ref", Help: i18n.T(lang, "cli.arg.ref")},
				{Name: "--all", Kind: Bool, Help: i18n.T(lang, "cli.arg.all_closed")},
				{Name: "--assignee", Default: "", Help: i18n.T(lang, "cli.arg.assignee_filter")},
			}, sortArgs(lang))},
			{Name: "usage", Help: i18n.T(lang, "cli.cmd.usage"), SubDest: "action", Subs: []*Command{
				{Name: "show", Help: i18n.T(lang, "cli.cmd.usage.show"), Run: cmdUsageShow, Args: []*Arg{{Name: "id", Nargs: "?"}, jsonArg(lang)}},
				{Name: "attach", Help: i18n.T(lang, "cli.cmd.usage.attach"), Run: cmdUsageAttach, Args: []*Arg{{Name: "id", Nargs: "?"}}},
				{Name: "report", Help: i18n.T(lang, "cli.cmd.usage.report"), Run: cmdUsageReport, Args: []*Arg{
					{Name: "--from", Dest: "date_from", Help: i18n.T(lang, "cli.arg.usage.report.from")},
					{Name: "--to", Dest: "date_to", Help: i18n.T(lang, "cli.arg.usage.report.to")},
					{Name: "--since-last", Dest: "since_last", Kind: Bool, Help: i18n.T(lang, "cli.arg.usage.report.since_last")},
					{Name: "--request", Kind: Int, Metavar: "N", Help: i18n.T(lang, "cli.arg.usage.report.request")},
					jsonArg(lang),
					{Name: "--xlsx", Metavar: "PATH", Help: i18n.T(lang, "cli.arg.usage.report.xlsx")},
				}},
				{Name: "ledger", Help: i18n.T(lang, "cli.cmd.usage.ledger"), SubDest: "ledger_action", Subs: []*Command{
					{Name: "list", Help: i18n.T(lang, "cli.cmd.usage.ledger.list"), Run: cmdUsageLedgerList, Args: []*Arg{jsonArg(lang)}},
					{Name: "add", Help: i18n.T(lang, "cli.cmd.usage.ledger.add"), Run: cmdUsageLedgerAdd, Args: []*Arg{
						{Name: "name", Help: i18n.T(lang, "cli.arg.usage.ledger.add.name")},
						{Name: "--from-report", Dest: "from_report", Metavar: "FILE", Help: i18n.T(lang, "cli.arg.usage.ledger.add.from_report")},
						{Name: "--from", Dest: "date_from", Help: i18n.T(lang, "cli.arg.usage.ledger.add.from")},
						{Name: "--to", Dest: "date_to", Help: i18n.T(lang, "cli.arg.usage.ledger.add.to")},
						{Name: "--data-end", Dest: "data_end", Help: i18n.T(lang, "cli.arg.usage.ledger.add.data_end")},
						{Name: "--excluded", Help: i18n.T(lang, "cli.arg.usage.ledger.add.excluded")},
						{Name: "--total-tokens", Dest: "total_tokens", Kind: Int, Help: i18n.T(lang, "cli.arg.usage.ledger.add.total_tokens")},
						{Name: "--note", Default: "", Help: i18n.T(lang, "cli.arg.usage.ledger.add.note")},
						{Name: "--created-at", Dest: "created_at", Help: i18n.T(lang, "cli.arg.usage.ledger.add.created_at")},
						{Name: "--request-id", Dest: "request_id", Kind: Int, Help: i18n.T(lang, "cli.arg.usage.ledger.add.request_id")},
						jsonArg(lang),
					}},
				}},
				{Name: "missing", Help: i18n.T(lang, "cli.cmd.usage.missing"), Run: cmdUsageMissing, Args: []*Arg{
					{Name: "--days", Kind: Int, Default: int64(30), Help: i18n.T(lang, "cli.arg.usage.missing.days")},
					{Name: "--all-users", Dest: "all_users", Kind: Bool, Help: i18n.T(lang, "cli.arg.usage.missing.all_users")},
					jsonArg(lang),
				}},
				{Name: "requests", Help: i18n.T(lang, "cli.cmd.usage.requests"), Run: cmdUsageRequests, Args: []*Arg{
					{Name: "--all", Kind: Bool, Help: i18n.T(lang, "cli.arg.usage.requests.all")},
					jsonArg(lang),
				}},
			}},
			{Name: "show", Help: i18n.T(lang, "cli.cmd.show"), Run: cmdShow, Args: []*Arg{{Name: "id"}, jsonArg(lang)}},
			{Name: "comment", Help: i18n.T(lang, "cli.cmd.comment"), Run: cmdComment, Args: []*Arg{{Name: "id"}, {Name: "text"}}},
			{Name: "status", Help: i18n.T(lang, "cli.cmd.status"), Run: cmdStatus, Args: []*Arg{
				{Name: "id"},
				{Name: "status", Help: i18n.T(lang, "cli.arg.statuses")},
				{Name: "--comment", Default: ""},
				{Name: "--override", Default: "", Help: i18n.T(lang, "cli.arg.status.override")},
				{Name: "--assignee", Default: "", Help: i18n.T(lang, "cli.arg.assignee_set")},
			}},
			{Name: "close", Help: i18n.T(lang, "cli.cmd.close"), Run: cmdClose, Args: []*Arg{
				{Name: "id"},
				{Name: "--comment", Default: ""},
				{Name: "--override", Default: "", Help: i18n.T(lang, "cli.arg.override")},
				{Name: "--assignee", Default: "", Help: i18n.T(lang, "cli.arg.assignee_set")},
			}},
			{Name: "assign", Help: i18n.T(lang, "cli.cmd.assign"), Run: cmdAssign, Args: []*Arg{
				{Name: "id"},
				{Name: "assignee", Help: i18n.T(lang, "cli.arg.assign.assignee")},
				{Name: "--override", Default: "", Help: i18n.T(lang, "cli.arg.assign.override")},
			}},
			{Name: "ready", Help: i18n.T(lang, "cli.cmd.ready"), Run: cmdReady, Args: join([]*Arg{
				{Name: "--assignee", Default: "", Help: i18n.T(lang, "cli.arg.assignee_filter")},
			}, sortArgs(lang), []*Arg{jsonArg(lang)})},
			{Name: "index", Help: i18n.T(lang, "cli.cmd.index_regen"), Run: cmdIndex},
			{Name: "matrix", Help: i18n.T(lang, "cli.cmd.index_regen"), Run: cmdMatrix},
			{Name: "edit", Help: i18n.T(lang, "cli.cmd.edit"), Run: cmdEdit, Args: []*Arg{
				{Name: "id"},
				{Name: "--force", Kind: Bool, Help: i18n.T(lang, "cli.arg.edit.force")},
			}},
			{Name: "push", Help: i18n.T(lang, "cli.cmd.push"), Run: cmdPush, Args: []*Arg{
				{Name: "id"},
				{Name: "--rebase", Kind: Bool, Help: i18n.T(lang, "cli.arg.push.rebase")},
				{Name: "--override", Default: "", Help: i18n.T(lang, "cli.arg.push.override")},
			}},
			{Name: "activity", Help: i18n.T(lang, "cli.cmd.activity"), Run: cmdActivity, Args: []*Arg{
				{Name: "ids", Nargs: "+"},
				{Name: "--since", Kind: Float, Default: 0.0, Help: i18n.T(lang, "cli.arg.activity.since")},
				jsonArg(lang),
			}},
			{Name: "verify", Help: i18n.T(lang, "cli.cmd.verify"), Run: cmdVerify, Args: []*Arg{
				{Name: "id"},
				{Name: "--timeout", Kind: Float, Default: verifyTimeout, Help: i18n.T(lang, "cli.arg.verify.timeout")},
				{Name: "--total-timeout", Dest: "total_timeout", Kind: Float, Default: verifyTotalTimeout, Help: i18n.T(lang, "cli.arg.verify.total_timeout")},
				{Name: "--list", Kind: Bool, Help: i18n.T(lang, "cli.arg.verify.list")},
				{Name: "--last", Kind: Bool, Help: i18n.T(lang, "cli.arg.verify.last")},
				jsonArg(lang),
			}},
			{Name: "summary", Help: i18n.T(lang, "cli.cmd.summary"), Run: cmdSummary, Args: []*Arg{
				{Name: "--limit", Kind: Int, Default: int64(10), Help: i18n.T(lang, "cli.arg.summary.limit")},
				jsonArg(lang),
				{Name: "--agent", Choices: agents, Help: i18n.T(lang, "cli.arg.summary.agent")},
				{Name: "--hook-json", Dest: "hook_json", Kind: Bool, Help: i18n.T(lang, "cli.arg.summary.hook_json")},
			}},
			{Name: "installed", Help: i18n.T(lang, "cli.cmd.installed"), Run: cmdInstalled, Args: []*Arg{
				{Name: "--agent", Required: true, Choices: agents, Help: i18n.T(lang, "cli.arg.installed.agent")},
				jsonArg(lang),
			}},
			{Name: "config", Help: i18n.T(lang, "cli.cmd.config"), Run: cmdConfig, Args: []*Arg{jsonArg(lang)}},
			{Name: "guide", Help: i18n.T(lang, "cli.cmd.guide"), Run: cmdGuide, Args: []*Arg{jsonArg(lang)}},
			{Name: "next", Help: i18n.T(lang, "cli.cmd.next"), Run: cmdNext, Args: []*Arg{
				{Name: "--dry-run", Dest: "dry_run", Kind: Bool, Help: i18n.T(lang, "cli.arg.next.dry_run")},
				{Name: "--comment", Default: "", Help: i18n.T(lang, "cli.arg.next.comment")},
				{Name: "--type", Default: "", Help: i18n.T(lang, "cli.arg.next.type")},
				{Name: "--override", Default: "", Help: i18n.T(lang, "cli.arg.override")},
				{Name: "--assignee", Default: "", Help: i18n.T(lang, "cli.arg.next.assignee")},
				jsonArg(lang),
			}},
			{Name: "init", Help: i18n.T(lang, "cli.cmd.init"), Run: cmdInit, Args: []*Arg{
				{Name: "--project", Help: i18n.T(lang, "cli.arg.init.project")},
				{Name: "--url", Help: i18n.T(lang, "cli.arg.init.url")},
				{Name: "--agent", Default: "claude-code", Help: i18n.T(lang, "cli.arg.init.agent")},
				{Name: "--dir", Help: i18n.T(lang, "cli.arg.init.dir")},
				{Name: "--source", Choices: []string{"link", "copy", "server"}, Help: i18n.T(lang, "cli.arg.init.source")},
				{Name: "--dist", Help: i18n.T(lang, "cli.arg.init.dist")},
				{Name: "--dry-run", Dest: "dry_run", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.dry_run")},
				{Name: "--force", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.force")},
				{Name: "--mcp", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.mcp")},
				{Name: "--no-freshness", Dest: "no_freshness", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.no_freshness")},
				{Name: "--no-usage", Dest: "no_usage", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.no_usage")},
				{Name: "--no-summary", Dest: "no_summary", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.no_summary")},
				{Name: "--no-skill", Dest: "no_skill", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.no_skill")},
				{Name: "--loop", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.loop")},
				{Name: "--no-loop", Dest: "no_loop", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.no_loop")},
				{Name: "--remove-loop", Dest: "remove_loop", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.remove_loop")},
				{Name: "--no-verify", Dest: "no_verify", Kind: Bool, Help: i18n.T(lang, "cli.arg.init.no_verify")},
			}},
			{Name: "login", Help: i18n.T(lang, "cli.cmd.login"), Run: cmdLogin, Args: []*Arg{
				{Name: "--url", Help: i18n.T(lang, "cli.arg.login.url")},
				{Name: "--browser", Kind: Bool, Help: i18n.T(lang, "cli.arg.login.browser")},
			}},
		},
	}
}

// InitRunner は init の本体（internal/client/kitinit が登録する。kitinit は hook を import し、hook は cli を import するので、
// cli から kitinit を import すると循環する。cmd/looptrack が起動時に登録する）。
var InitRunner func(c *Ctx, v *Values) error

func cmdInit(c *Ctx, v *Values) error {
	if InitRunner == nil {
		return i18n.Errorf("cli.err.init_not_built")
	}
	return InitRunner(c, v)
}
