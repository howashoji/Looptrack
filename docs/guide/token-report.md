# Token reports

Looptrack records how many tokens your coding agents spend, attributes them to issues, and turns them into a PDF report for a period.
This page is for **administrators who read the reports** and **developers whose agents are measured**.
It covers what is recorded, how it is added up, and how a report is made.

## What is recorded

The client side sends a **snapshot**: the conversation's running total at one moment.
The server knows nothing about tokens by itself; only the agent's own conversation record on your machine has them.
`looptrack` reads that record (Claude Code: `~/.claude/projects/…`, Codex: `~/.codex/sessions/…`) and sends the figures.

A snapshot carries:

- the running token totals of the four kinds (input, cache writes, cache reads, output), for the main conversation and for subagents separately, and per model
- the number of responses, the git branch, and the name of the working directory
- at the end of a session, the list of segments of the conversation: start time, kind, waiting minutes, working minutes and tokens

A running total, not a difference, is sent so that one lost or duplicated snapshot does not become an error: the next one catches up.
Snapshots are append-only on the server (the database user can only add rows).

### When it is sent

| When | Sent by | Notes |
| -- | -- | -- |
| Right after a change made through the CLI: `new`, `push`, `comment`, `status`, `close`, `next` when it starts an issue, `verify`, `assign` | The CLI itself | Works with any agent, even one without hooks. Nothing is sent when you type the command yourself outside an agent |
| Right after a change made through MCP | The `PostToolUse` hook (Claude Code, Codex) | Only the tools that change an issue |
| When a turn ends | The `Stop` hook | Skipped if the previous send was less than 10 minutes ago (`LOOPTRACK_USAGE_THROTTLE_MIN`) |
| When a session ends | The `SessionEnd` hook | Always sent, with the list of segments |
| When you run `looptrack issue usage attach <ID>` | You or the agent | Attaches the conversation's running total to that issue by hand |

Sending never gets in the way of the operation.
The CLI gives up after 2 seconds and the hooks after 5 (`LOOPTRACK_USAGE_TIMEOUT`); the operation's result and exit code stay the same.
What could not be sent is kept in `usage-spool/` next to your credentials (`~/.config/looptrack/`, `%APPDATA%\looptrack\` on Windows),
resent the next time, and dropped after 7 days.
If sending keeps failing, the session-start `summary` says so in one line, because the hooks themselves fail silently.

GitHub Copilot can be measured only when the user turns on OpenTelemetry file export (see [Agent-specific notes](ai-agents.md)).

### What is not sent

**What the human types is not sent by default.** Only the numbers of each segment go to the server.
The first 44 characters of each instruction (a "task name" for the segment) are sent only when **both** of these allow it:

- the project rule `usage.send_prompts` is `true` (an administrator switches it at `<server URL>/admin/projects`, or writes it with `looptrack project rules set`)
- the user has not set `LOOPTRACK_USAGE_SEND_PROMPTS=0`

The CLI learns the project's setting from the server's reply and follows it from the next send; until it knows, it sends nothing.
The server also discards task names that arrive for a project that does not allow them.

### Stopping it

- To stop sending altogether on your machine, set `LOOPTRACK_USAGE=0`.
- To keep one conversation out of the reports, write `本セッションはレポート対象外` ("this session is out of the report") in one of your messages in that conversation.
  The conversation is still recorded, but reports leave it out of the totals and list it separately as excluded.
  The phrase is matched in Japanese only; a quoted phrase (inside `「」` or quotation marks) does not count.

## How it is added up

The server does not store differences. It works them out every time you ask:

1. The snapshots of each conversation are put in order of their running totals.
2. The difference between two neighbouring snapshots is the consumption of that **segment**.
3. A segment belongs to **the issue of the snapshot that closed it**. Research before filing goes to the issue you filed; the work before a comment goes to that issue.
4. A segment closed at the end of a turn or a session goes to the issue the conversation last touched, if that issue is still open. Otherwise it is **unattributed**.

`looptrack issue usage show <ID>` shows one issue's consumption per stage (the operation that closed each segment).
Because of rule 3, parallel work on two issues in one conversation cannot be told apart.

### Operations missing token information

An agent's change counts as covered when a snapshot for the same issue and user arrives within 10 minutes of it.
One attached later with `usage attach` counts too.
Operations you type in a terminal yourself are not counted.

```bash
looptrack issue usage missing                   # your own agent operations in the last 30 days (--days N)
looptrack issue usage missing --all-users       # everyone's, with the coverage rate
looptrack issue usage attach DEMO-0004          # attach this conversation's total to an issue
```

The end of `summary` lists your own operations from the last 7 days that are still missing token information.
To make it mandatory, set the project rule `usage.require_on_close` (see [Administration](admin.md)).
Then an agent cannot mark an issue Done or Canceled without token information from that conversation; the CLI attaches it and retries once.

## Making a report

A report covers a period, adds up the segments closed within it, and is recorded in the project's **ledger**.
The server adds the figures up; the prose and the PDF are made on the machine that runs the agent.
The PDF is never stored on the server.

### Asking an agent from the board

1. On the project board (`<server URL>/p/<slug>/`), open **Token report** and register a request: "since the last report" or a date range, plus a target and a note for the agent. Editor or above is needed.
2. The next time a coding agent starts a session, the request appears at the end of `summary`.
3. Tell the agent `トークンレポートを作成して（依頼 #N）` ("make the token report, request #N"). The `token-report` skill takes it through the figures, the prose, the PDF and the ledger entry.

A request is finished when a ledger row carries its number; it cannot be withdrawn any other way.
Up to 20 requests can be open at once.

The skill is installed by `looptrack issue init` at `.claude/skills/token-report/SKILL.md`.
You can also ask without a request: "make the token report" (since the last report) or with a period.

### By hand

```bash
looptrack issue usage requests                                  # open requests (--all includes finished ones)
looptrack issue usage report --since-last --json > r.json       # since the last report
looptrack issue usage report --from 2026-09-01 --to 2026-09-30  # a period, as a table to read
looptrack issue usage report --request 3 --xlsx r.xlsx          # the period of request #3, as a spreadsheet
looptrack report pdf --report r.json --content body.json --check
looptrack report pdf --report r.json --content body.json --out report.pdf
looptrack issue usage ledger add "2026-09" --from-report r.json --note report.pdf
looptrack issue usage ledger list
```

| Option of `usage report` | Period |
| -- | -- |
| `--since-last` | From the newest data end in the ledger up to now (from the beginning if the ledger is empty) |
| `--from D --to D` | `[from, to)`. A `--to` that is a date alone includes that day. Dates are read in Asia/Tokyo; `--to` defaults to now |
| `--request N` | The period of request #N, resolved when you run it |

A report shows the total and breaks it down per issue, label, issue type, stage, agent, case and conversation.
What is unattributed and the conversations kept out are shown separately, not in the total.
The per-case breakdown groups segments by a case name taken from the issue's labels or the branch name,
using the regular expression in the project rule `usage.case_pattern`.

### The PDF

`looptrack report pdf` builds the PDF from two JSON files:
the figures (the output of `usage report --json`) and the prose (sections written by the agent, as described in the skill).
The tables are built from the figures, so no number is copied by hand.
`--check` only checks the input and shows how many sections and tables it has.
A section whose heading contains "Data limitations" is required.
A Japanese font is built in; `TOKEN_REPORT_FONT` (a path to a TrueType font) replaces it.
The skill saves everything under `~/Documents/トークンレポート/<slug>/` (`TOKEN_REPORT_DIR`), outside the repository.

### The ledger

`usage ledger add` records one report as one row: its name, period, data end, excluded conversations, total and a note.
`--from-report` copies them from the figures JSON, and also the request number when the figures were taken with `--request`.

- **A row cannot be changed or removed.** The name must be unique within the project.
- The next `--since-last` starts from the newest data end in the ledger, so periods neither overlap nor leave a gap.
- A snapshot that arrives late, stamped before the previous data end, appears in no report.
- Writing to the ledger needs editor or above; a viewer can only read the figures.

## Settings

| Setting | Where | Effect |
| -- | -- | -- |
| `LOOPTRACK_USAGE=0` | Your environment | Stops sending token information |
| `LOOPTRACK_USAGE_SEND_PROMPTS=0` | Your environment | Never sends task names, whatever the project rule says |
| `LOOPTRACK_USAGE_TIMEOUT` | Your environment | Seconds to wait when sending (2 for the CLI, 5 for the hooks) |
| `LOOPTRACK_USAGE_THROTTLE_MIN` | Your environment | Minutes between sends at the end of a turn (10; `0` sends every time) |
| `LOOPTRACK_USAGE_DEBUG=1` | Your environment | Prints why a send failed |
| `usage.send_prompts` | Project rule | Allows task names to be sent (off by default) |
| `usage.require_on_close` | Project rule | Requires token information before an agent closes an issue |
| `usage.case_pattern` | Project rule | Regular expression for the per-case breakdown |
| `TOKEN_REPORT_DIR` | Your environment | Where the skill saves reports |
| `TOKEN_REPORT_FONT` | Your environment | The TrueType font for the PDF |

Project rules are set with `looptrack project rules set` (see [Administration](admin.md)).
The design in full is in [DESIGN.md](../server/DESIGN.md) §9-5.
