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
| Creating a project (server administrators only) | `create_project` |
| Verification | `verify_issue` (returns the list; commands run in your local shell), `report_verify` (sends results you ran locally) |
| Tokens | `issue_usage`, `usage_missing`, `usage_report`, `list_usage_ledger`, `add_usage_ledger`, `list_usage_requests` |

With MCP only, the agent runs the verify commands in its local shell in order and sends the results with `report_verify`.
Those records are marked as "self-reported via MCP", so people can tell them apart from CLI records.

## Installing through MCP only (the setup tool)

You can install on a machine with neither the CLI nor hooks, starting from just an MCP connection.

1. Add the MCP connection to the agent and authorise it in the browser. To have the agent add the connection too, use the prompts in "Installing by pasting one prompt" below
2. Ask the agent to "set up issue management". The agent calls the `setup` tool
3. The `setup` tool first returns only the question of whether to install loop. The agent asks you, then calls `setup` again with your answer, and runs the single command it gets back (download → SHA-256 check → init) after you approve it
4. If there is no token yet, the agent asks for approval and runs `looptrack issue login --browser`. You sign in and allow access in the browser
5. You restart the agent and approve its hooks
6. At the start of the next session, a hook tells the server the install is complete, and the [Setup incomplete] note disappears from tool results

## Installing by pasting one prompt (a remote server)

These steps connect the agents on your own machine to a Looptrack server that runs on another machine or in the cloud.
All you do is fill in `<server URL>` and `<project>` in the prompt below and paste it into the agent.
The agent adds the MCP connection, calls `setup`, and runs the install command.
Your hands are needed only to allow access and sign in through the browser, approve commands, restart the agent, and trust its hooks.

**Never hand a token to the agent.** Do not put a token, a password, or a verification code into the prompt or the conversation.
Sign in with `looptrack issue login --browser`; the token travels only between the CLI and the server.

These steps have not been checked on a real Windows machine.
Copilot in VS Code has not been checked on a real installation either.

### Before you start

| Who | What to do |
| -- | -- |
| The server administrator | Creates your user (`<server URL>/admin/users`) and adds you to the project as an editor (on the same page, or at `<server URL>/admin/projects`) |
| You | Get the server URL (for example `https://example.com/looptrack`, without `/mcp` at the end) and the project slug from the administrator |
| You | Check that you can sign in to the server in the browser. If two-factor auth is required, register an authenticator app at your first sign-in |
| You | Start the agent in the top directory of the repository you want to connect. For Codex, mark that directory as trusted first |

If the project does not exist yet, the administrator usually creates it first (`<server URL>/admin/projects`).
If you are a server administrator yourself, you do not have to. When `setup` answers "Project not found",
an administrator gets a note that the MCP tool `create_project` can create it, and the agent follows it: it creates the project, then calls `setup` again.
The prefix (of the IDs) and the width (digits of the number) can never be changed afterwards, so the agent shows you the slug, prefix and width and asks you to confirm them before it creates the project.
Anyone who is not an administrator is told to ask an administrator.

Apart from `<server URL>` and `<project>`, there is nothing to add to the prompt.
You can paste the same prompt as many times as you like. The agent checks the install state with `setup` and carries on from the steps that are left.

### The prompt for Claude Code

```text
Connect this repository to the project <project> on the Looptrack issue tracking server (<server URL>).
Before every command you run and every setting you change, show me what it is and get my approval.

1. If the looptrack MCP tools (setup) are available in this session, skip 2 and 3 and go to 4.
2. Add the MCP connection with this command:
   claude mcp add --transport http looptrack <server URL>/mcp --header "X-Looptrack-Project: <project>"
3. Stop here and ask me: "Restart Claude Code, pick looptrack in /mcp and allow access in the browser, then paste this prompt again."
4. Call the setup tool with the arguments workspace (the absolute path of this repository's git root) and project: "<project>".
5. If you are asked whether to install loop (the loop engineering set), ask me before deciding. Call setup again with my answer and the same arguments as in 4.
6. Carry out the steps setup returns, from the top. Run the commands of the [AI] steps at the repository root only after I approve them. If the SHA-256 does not match, stop there.
7. Sign in with looptrack issue login --browser, as the setup steps say. Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
8. Never read or display a token. Never ask me to paste a token.
9. For the [user] steps (restarting and approving the hooks), ask me and stop. When I paste this prompt again, confirm that setup reports Installed, and finish.
```

### The prompt for Codex

```text
Connect this repository to the project <project> on the Looptrack issue tracking server (<server URL>).
Before every command you run and every setting you change, show me what it is and get my approval.

1. If the looptrack MCP tools (setup) are available in this session, skip 2 and 3 and go to 4.
2. Add these 3 lines to the end of ~/.codex/config.toml. If [mcp_servers.looptrack] is already there, do not rewrite it; tell me instead (even if it is for another project, there is no need to rewrite it, because 4 passes project).
   [mcp_servers.looptrack]
   url = "<server URL>/mcp"
   http_headers = { "X-Looptrack-Project" = "<project>" }
3. Stop here and ask me: "Run codex mcp login looptrack in a terminal and allow access in the browser, restart Codex, then paste this prompt again."
4. Call the setup tool with the arguments workspace (the absolute path of this repository's git root) and project: "<project>".
5. If you are asked whether to install loop (the loop engineering set), ask me before deciding. Call setup again with my answer and the same arguments as in 4.
6. Carry out the steps setup returns, from the top. Run the commands of the [AI] steps at the repository root only after I approve them. If the sandbox blocks network access, ask me whether you may run it with raised permissions. If the SHA-256 does not match, stop there.
7. Sign in with looptrack issue login --browser, as the setup steps say. Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
8. Never read or display a token. Never ask me to paste a token.
9. For the [user] step (trusting the hooks with /hooks in the terminal codex), ask me and stop. When I paste this prompt again, confirm that setup reports Installed, and finish.
```

Codex has one MCP configuration (`~/.codex/config.toml`) for the whole machine.
If it already holds a looptrack entry for another project, setup is available at step 1, so steps 2 and 3 are skipped.
Without the project argument, setup would then return the steps for the project in the connection's header (the other project), and the agent would not notice.
That is why step 4 passes project (setup falls back to the header's project only when the argument is left out).
After the install, too, the MCP tools use the header's project. If the header names another project, pass the project argument on every MCP tool call.
Before Codex is restarted, the token check that setup shows (`looptrack issue config`) can fail with `Error: the issue server URL …`.
The environment variables that init writes into the repository's `.codex/config.toml` take effect only after Codex restarts.
In that case, run it again with `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT` set (in the hands-on check, the agent noticed this and added them itself).

### The prompt for GitHub Copilot (VS Code)

Paste it into a chat in agent mode.

```text
Connect this repository to the project <project> on the Looptrack issue tracking server (<server URL>).
Before every command you run and every file you change, show me what it is and get my approval.

1. If the looptrack MCP tools (setup) are available in this chat, skip 2 and 3 and go to 4.
2. Add the following looptrack entry to servers in .vscode/mcp.json. Create the file if it does not exist. If looptrack is already there, do not rewrite it; tell me instead.
   "looptrack": { "type": "http", "url": "<server URL>/mcp", "headers": { "X-Looptrack-Project": "<project>" } }
3. Stop here and ask me: "When VS Code asks to allow looptrack, allow it in the browser, then paste this prompt again in a new chat."
4. Call the setup tool with the arguments workspace (the absolute path of this repository's git root) and project: "<project>".
5. If you are asked whether to install loop (the loop engineering set), always ask me before deciding. Never decide the answer yourself. Call setup again with my answer and the same arguments as in 4.
6. Carry out the steps setup returns, from the top. Run the commands of the [AI] steps at the repository root only after I approve them. If the SHA-256 does not match, stop there.
7. Sign in with looptrack issue login --browser, as the setup steps say. Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
8. Never read or display a token. Never ask me to paste a token.
9. For the [user] step (starting a new chat), ask me and stop. When I paste this prompt again, confirm that setup reports Installed, and finish.
```

### The prompt for GitHub Copilot CLI

```text
Connect this repository to the project <project> on the Looptrack issue tracking server (<server URL>).
Before every command you run and every setting you change, show me what it is and get my approval.

1. If the looptrack MCP tools (setup) are available in this session, skip 2 and 3 and go to 4.
2. Add the MCP connection with this command:
   copilot mcp add --transport http --header "X-Looptrack-Project: <project>" looptrack <server URL>/mcp
3. Stop here and ask me: "Restart copilot, allow access in the browser that opens (if it does not open, use /mcp auth looptrack), restart copilot once more, then paste this prompt again."
4. Call the setup tool with the arguments workspace (the absolute path of this repository's git root) and project: "<project>".
5. If you are asked whether to install loop (the loop engineering set), always ask me before deciding. Never decide the answer yourself. Call setup again with my answer and the same arguments as in 4.
6. Carry out the steps setup returns, from the top. Run the commands of the [AI] steps at the repository root only after I approve them. If the SHA-256 does not match, stop there.
7. Sign in with looptrack issue login --browser, as the setup steps say. Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
8. Never read or display a token. Never ask me to paste a token.
9. For the [user] step (restarting copilot and choosing to always trust the folder when asked), ask me and stop. When I paste this prompt again, confirm that setup reports Installed, and finish.
```

Some Copilot models decide whether to install loop without asking the user.
That is why step 5 is worded more strongly in the Copilot prompts. You can also write your answer into the prompt from the start (for example "do not install loop").

### Where your hands are needed

| Stage | What you do | Why it is you |
| -- | -- | -- |
| Before you start | The administrator creates your user and adds you to the project | Permissions are per project, and the administrator grants them |
| Adding the MCP connection | Approve the change the agent shows you | It rewrites the settings of the agent on your machine |
| Allowing MCP access | Sign in and allow access in the browser: `/mcp` in Claude Code, `codex mcp login looptrack` in a terminal for Codex, the browser that opens by itself when you restart Copilot CLI (`/mcp auth looptrack` if it does not open), and in VS Code when it asks | The server checks that it is you. Passwords and verification codes are never handed to the agent |
| Restarting and pasting again | Restart the agent (a new chat in VS Code) and paste the same prompt. Restart Copilot CLI once more after allowing access in the browser | A newly added connection is loaded after the restart. Copilot CLI starts the authorisation as soon as it starts, so the connection can pass its 10-second limit while it waits and fail with `Failed to connect … timed out after 10000 ms` |
| The loop question | Answer whether to install it | The user, not the agent, decides whether to install loop |
| The install command | Approve the single command: download → SHA-256 check → init. Claude Code asks for your permission for this command even in auto mode (it flags it as `Contains brace with quote character (expansion obfuscation)`) | It puts the executable in `~/.local/bin` and changes files in the repository |
| Signing in | Approve `looptrack issue login --browser`, then sign in (with two-factor auth) and allow access in the browser that opens. If this machine already has a token, this stage is skipped | The token travels only between the CLI and the server and never appears in the conversation |
| Trusting the hooks | Claude Code: restart and approve if asked. Codex: trust them with `/hooks` in the terminal `codex`. Copilot: start a new session (in Copilot CLI, always trust the folder) | Hooks run only after the user accepts them |

In Codex, the sandbox blocks network access for the shell commands the agent runs.
The install command and the sign-in need your approval to run with raised permissions.
In Copilot CLI, every call to an MCP tool needs your approval.

### When something goes wrong

| Symptom | What to check |
| -- | -- |
| The browser does not open to allow access | Start your default browser first. In Claude Code, close the `/mcp` screen and open it again. `looptrack issue login --browser` also prints the URL, so you can paste it into a browser that is already running |
| The MCP tools are still unavailable after pasting again | Check that you restarted the agent after the connection was added, and that you allowed access. Also check that the URL ends in `/mcp` |
| [Setup incomplete] does not disappear from tool results | Check that you restarted and trusted the hooks. Codex skips untrusted hooks silently. Without a registered token the hook cannot report (check with `looptrack issue config`) |
| The PATH or the hook wiring looks wrong | Run `looptrack doctor`. It checks the PATH, the hook wiring and the install record (it writes nothing) |
| `setup` answers "Project not found" | Check the spelling of the slug. If you are a server administrator, the agent asks whether it may create the project with `create_project`, showing the slug, prefix and width; check the values before you answer (the prefix and the width cannot be changed later). If you are not an administrator, ask one to create the project or to add you to it |
| loop was installed without asking you | Remove it with `looptrack issue init --remove-loop` |
| You need to work without a browser | Issue a token yourself at `<server URL>/account` and paste it into `looptrack issue login --url <server URL>` in your own terminal. Never hand it to the agent |
