# Agent-specific notes

[Guide contents](README.md) · Previous: [Daily use](daily-use.md) · Next: [Administration](admin.md)

## The common model

| Path | When it is used |
| -- | -- |
| CLI (`looptrack issue …`) | The main path for Claude Code. Hooks use the CLI too |
| MCP tools | The main path for Codex and Copilot. Agents without hooks run the whole loop through MCP alone |

The server's rules, permissions, and records are the same for every agent.
Install with `looptrack issue init --agent <agent>`.
An agent that only has an MCP connection can ask the MCP `setup` tool for install steps (see "Installing through MCP only" below).

## Choosing core or core + loop

| Situation | Recommendation |
| -- | -- |
| You want to try it first and are not used to hooks pushing back | core only (`--no-loop`) |
| You want the agent to run on its own for long stretches while you only make decisions | core + loop (`--loop`) |
| Several agents work in one repository at the same time | core + loop (the context size warning and the cross-repository check help) |
| Part of loop does not suit you | Remove it with `--remove-loop`. Fine-tune hooks with the `LOOPTRACK_LOOP_*` environment variables |

What loop adds:

| Item | What it does |
| -- | -- |
| Output and working discipline | Shows rule texts to the agent at every session |
| Confirm mode | When you ask the agent only to check or investigate, it stops at a report and plan and edits are refused. Asking it to implement lifts this |
| Handoff freshness | After a commit or close, blocks stopping until the handoff notes are updated (the `session-handoff` skill) |
| Iteration discipline (the `/iterate` skill) | Runs one implementation as one issue, and files every problem it finds on the spot. Runs the gates (build → lint → test) with `looptrack gates` |
| Context size warning | When the context of one response goes over the threshold, suggests writing the handoff and splitting the session at a good breakpoint (it stops nothing) |
| Runaway background processes | Finds child processes the session left running and reports them |
| Changes to another repository or project | Asks the user before making them |

The rule texts ship in English and Japanese, and the hooks pick one at run time.
Confirm mode reacts to requests in English (check, investigate, review, … / implement, fix, add, …) and in Japanese (「確認して」 / 「実装して」). When a request contains both, the Japanese words decide.
To add your own phrasing, add patterns through `LOOPTRACK_LOOP_TASK_MODE_INVEST_RE` and `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE` in the `env` of your agent settings.

**The user, not the agent, decides** whether to install loop.

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --loop          # add loop
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --remove-loop   # remove loop
```

## Claude Code

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp
```

- **Hooks** are wired in `.claude/settings.json`. When you restart and are asked to approve hooks, review and approve them.
- **The `/issue` skill** covers filing, starting, commenting, and closing. Type `/issue`, or just ask the agent to file an issue.
- **The `/iterate` skill** (loop) runs one implementation as "start → implement → gates → file problems → close → next".
- **MCP prompts**: `/mcp__looptrack__loop` (the standard loop), `/mcp__looptrack__review` (raise items waiting for you and unanswered reactions), `/mcp__looptrack__setup` (installation).
- The managed section of `CLAUDE.md` tells the agent how to use the CLI.

An example request:

> Work from the next issue. Put anything that needs my decision In Review and keep going.

## Codex

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent codex
```

- **Instructions**: a managed section is added to `AGENTS.md` (Codex has no rules files or skills, so the essentials go here).
- **Codex works mainly through MCP.** Codex's default sandbox blocks network access for shell commands the agent runs. Every CLI call would need your approval, so issue operations go through MCP tools.
- **Connecting MCP**: add the following to `~/.codex/config.toml` and authorise with `codex mcp login looptrack`.

```toml
[mcp_servers.looptrack]
url = "http://127.0.0.1:8090/looptrack/mcp"
http_headers = { "X-Looptrack-Project" = "demo" }
```

- **Hooks** are wired in `.codex/hooks.json`. After trusting the project, **start the terminal `codex` in the project and trust the hooks with `/hooks`**. That trust also applies to the desktop app. Untrusted hooks are skipped silently.
- **CLI environment**: init writes `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT` to `[shell_environment_policy]` in `.codex/config.toml`.

## GitHub Copilot (VS Code agent mode, Copilot CLI)

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent copilot --mcp
```

- **Copilot works mainly through MCP** (as with Codex).
- **Connecting MCP**: `--mcp` writes `.vscode/mcp.json` for VS Code and `.github/mcp.json` for Copilot CLI. In Copilot CLI, authorise with `/mcp auth looptrack`.
- **Instructions**: Copilot reads the managed section of `AGENTS.md`.
- **Hooks** are wired in `.github/hooks/looptrack.json`. VS Code picks them up in a new chat; Copilot CLI once you trust the folder.
- **Token tracking**: Copilot usage can only be measured if the user turns on OpenTelemetry file export. If it is off, missing token info is not held against you.

Some of the Copilot hook behaviour has not yet been confirmed on real installations.
If hooks do not work, run the loop through MCP tools alone.

## Other agents (MCP only)

An agent without hooks can still run the loop through MCP tools alone.

1. Add the MCP connection (URL `<server URL>/mcp`, header `X-Looptrack-Project: <slug>`, authorised in the browser)
2. Run `looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent other` and copy the printed guidance into the agent's instruction file
3. Ask the agent: "Read the guide tool first, then work from next."
4. When installation is done, tell the server with `looptrack issue installed --agent other`

The MCP tools:

| Purpose | Tools |
| -- | -- |
| Rules and installation | `guide`, `setup` |
| The loop | `next`, `create_issue`, `add_comment`, `set_status`, `get_issue`, `update_issue`, `assign_issue` |
| Lists | `list_issues`, `ready_issues`, `project_summary`, `get_matrix`, `issue_activity`, `list_projects` |
| Verification | `verify_issue` (returns the list; commands run in your local shell), `report_verify` (sends results you ran locally) |
| Tokens | `issue_usage`, `usage_missing`, `usage_report`, `list_usage_ledger`, `add_usage_ledger`, `list_usage_requests` |

With MCP only, the agent runs the verify commands in its local shell in order and sends the results with `report_verify`.
Those records are marked as "self-reported via MCP", so people can tell them apart from CLI records.

## Installing through MCP only (the setup tool)

You can install on a machine with neither the CLI nor hooks, starting from just an MCP connection.

1. Add the MCP connection to the agent and authorise it in the browser
2. Ask the agent to "set up issue management". The agent calls the `setup` tool
3. The `setup` tool first returns only the question of whether to install loop. The agent asks you, then calls `setup` again with your answer, and runs the single command it gets back (download → SHA-256 check → init) after you approve it
4. If there is no token yet, the agent asks for approval and runs `looptrack issue login --browser`. You sign in and allow access in the browser
5. You restart the agent and approve its hooks
6. At the start of the next session, a hook tells the server the install is complete, and the [Setup incomplete] note disappears from tool results
