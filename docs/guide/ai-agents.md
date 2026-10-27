# Agent-specific notes

[Guide contents](README.md) · Previous: [Daily use](daily-use.md) · Next: [FAQ / Troubleshooting](faq.md)

## The common model

| Path | When it is used |
| -- | -- |
| CLI (`looptrack issue …`) | The main path for Claude Code. Hooks use the CLI too |
| MCP tools | The main path for Codex and Copilot. Agents without hooks run the whole loop through MCP alone |

The server's rules, permissions, and records don't change from agent to agent.
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

The rule texts ship in both English and Japanese, and the hooks pick one at run time.
Confirm mode reacts to requests in English (check, investigate, review, … / implement, fix, add, …) and in Japanese (「確認して」 / 「実装して」). If a request contains both, the Japanese words win.
Want your own phrasing? Add patterns through `LOOPTRACK_LOOP_TASK_MODE_INVEST_RE` and `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE` in the `env` of your agent settings.
One catch: these are Go regular expressions (RE2), so lookahead and lookbehind such as `(?!…)` aren't supported. The same goes for `LOOPTRACK_LOOP_RUNAWAY_ALLOW` (processes to exempt from the runaway check). A pattern that can't be read is ignored, and only the default words apply. When that happens, the hook shows the variable name and the error as a message.

Whether to install loop is the user's call. Not the agent's.

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --loop          # add loop
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --remove-loop   # remove loop
```

## Claude Code

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp
```

- Hooks: wired in `.claude/settings.json`. When you restart and get asked to approve them, look them over and approve.
- The `/issue` skill: covers filing, starting, commenting, and closing. Type `/issue`, or just ask the agent to file an issue.
- The `/iterate` skill (loop): runs one implementation as "start → implement → gates → file problems → close → next".
- MCP prompts: `/mcp__looptrack__loop` (the standard loop), `/mcp__looptrack__review` (raise items waiting for you and unanswered reactions), `/mcp__looptrack__setup` (installation).
- What tells the agent how to use the CLI is the managed section of `CLAUDE.md`.

Here's an example request:

> Work from the next issue. Put anything that needs my decision In Review and keep going.

## Codex

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent codex
```

- Instructions: a managed section goes into `AGENTS.md`. Codex has no rules files or skills, so the essentials live here.
- Codex works mainly through MCP. Why? Its default sandbox blocks network access for the shell commands the agent runs. So every CLI call would need your approval. Issue operations go through MCP tools instead.
- Connecting MCP: add the following to `~/.codex/config.toml` and authorise with `codex mcp login looptrack`.

```toml
[mcp_servers.looptrack]
url = "http://127.0.0.1:8090/looptrack/mcp"
http_headers = { "X-Looptrack-Project" = "demo" }
```

- Hooks: wired in `.codex/hooks.json`. After trusting the project, **start the terminal `codex` in the project and trust the hooks with `/hooks`**. That trust carries over to the desktop app too. Untrusted hooks are skipped silently.
- CLI environment: init writes `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT` to `[shell_environment_policy]` in `.codex/config.toml`.

## GitHub Copilot (VS Code agent mode, Copilot CLI)

```bash
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent copilot --mcp
```

- Like Codex, Copilot works mainly through MCP.
- Connecting MCP: with `--mcp`, init writes `.vscode/mcp.json` for VS Code and `.github/mcp.json` for Copilot CLI. In Copilot CLI, authorise with `/mcp auth looptrack`.
- Instructions: Copilot reads the managed section of `AGENTS.md`.
- Hooks: wired in `.github/hooks/looptrack.json`. VS Code picks them up in a new chat. Copilot CLI, once you trust the folder.
- Token tracking: Copilot usage is measured only if the user turns on OpenTelemetry file export. If it's off, missing token info isn't held against you.

Parts of the Copilot hook behaviour haven't been confirmed on real installations yet.
If the hooks don't work, run the loop through MCP tools alone.

## Other agents (MCP only)

No hooks? No problem. An agent can still run the loop through MCP tools alone.

1. Add the MCP connection (URL `<server URL>/mcp`, header `X-Looptrack-Project: <slug>`, authorised in the browser)
2. Run `looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent other` and copy the printed guidance into the agent's instruction file
3. Ask the agent: "Read the guide tool first, then work from next."
4. When installation is done, tell the server with `looptrack issue installed --agent other`

Here are the MCP tools:

| Purpose | Tools |
| -- | -- |
| Rules and installation | `guide`, `setup` |
| The loop | `next`, `create_issue`, `add_comment`, `set_status`, `get_issue`, `update_issue`, `assign_issue` |
| Lists | `list_issues`, `ready_issues`, `project_summary`, `get_matrix`, `issue_activity`, `list_projects` |
| Creating a project (server administrators only) | `create_project` |
| Verification | `verify_issue` (returns the list; commands run in your local shell), `report_verify` (sends results you ran locally) |
| Tokens | `issue_usage`, `usage_missing`, `usage_report`, `list_usage_ledger`, `add_usage_ledger`, `list_usage_requests` |

With MCP only, the agent runs the verify commands one by one in its local shell and sends the results with `report_verify`.
Those records carry a "self-reported via MCP" mark. That's how people tell them apart from CLI records.

## Working from several machines

You can work on one project from two machines (at home and at work, say) with your agents.
What no vendor lets you do is carry on **one conversation** on two machines at the same time: you can only take turns, or fork it.
So split the work by issue and branch, not by conversation.

### The rules

1. **One machine, one issue, one branch.** If one issue needs work on both machines, split it into child issues.
2. **Push before you switch machines.** Uncommitted changes and local notes (the handoff file, the agent's memory) stay on the machine where they were written.
3. **The machine taking over declares the start again** with `looptrack issue status <ID> "In Progress"`, and the machine handing over puts the issue back to Todo first. The start moves to the session that declared it last, with no confirmation. From then on, `next` in the other session skips that issue and notes that another session is working on it.
4. **Never run `looptrack issue next` from a shell you type into by hand.** A shell with no AI session id treats every In Progress issue of yours as its own.
5. **Never resume the same conversation in two places at once. To continue somewhere else, fork it.**

```bash
claude --resume <session-id> --fork-session   # Claude Code
codex fork <session-id>                        # Codex
```

A plain `claude --resume <session-id>` appends to the original transcript file even when you run it from another directory, so the two places end up mixed in one transcript.
The official page says the same: "If you resume the same session in two terminals without forking, messages from both interleave into one transcript" (<https://code.claude.com/docs/en/sessions>).
A fork gets a new session id and leaves the original as it was (`--fork-session`: <https://code.claude.com/docs/en/cli-reference>; `codex fork`: <https://developers.openai.com/codex/cli/reference>).
Even after a fork, absolute paths that appear in the conversation are reused as they were. In a measured case, the first edit after continuing in another checkout changed the original checkout.
So give the agent the path of the checkout you continue in, in your first message.

The behaviour in 3 to 5 was measured on 2026-10-25 with Claude Code 2.1.310 and a disposable Looptrack server.

### What the vendors offer

These let you watch and steer one conversation from several devices. In every one of them, the work runs in **one place**.

| Agent | Feature | Where the work runs | Official page |
| -- | -- | -- | -- |
| Claude Code | `claude --cloud "<task>"` starts a cloud session | A cloud VM. It clones your branch from GitHub, so push first | <https://code.claude.com/docs/en/claude-code-on-the-web> |
| Claude Code | `claude --teleport` pulls a cloud session into your terminal | Your terminal, on its own copy. New work there does not appear in the cloud session | <https://code.claude.com/docs/en/claude-code-on-the-web> |
| Claude Code | Remote Control: `claude --remote-control` (or `--rc`), or `/remote-control` in a running session | Your machine. A phone or a browser only steers it | <https://code.claude.com/docs/en/remote-control> |
| Codex | Cloud tasks (`codex cloud`) | A Codex cloud environment. You apply the result to your local repository | <https://developers.openai.com/codex/cli/reference> |
| Codex | Remote connections | The machine that runs the Codex app server | <https://developers.openai.com/codex/remote-connections> |

Neither vendor documents a way to sync local transcripts between machines (checked on 2026-10-25).
Claude Code states that the transcript format "is internal to Claude Code and changes between versions" (<https://code.claude.com/docs/en/sessions>).
Copying transcripts between machines by hand is not a supported way to continue a conversation.

### A shortcut: the same absolute path

When both machines run the same OS under the same user name, put the checkout at the same absolute path on both (for example `/Users/<name>/src/<project>`).
Paths written into handoff notes, issue comments and conversations then stay valid on either machine.
With a different OS or a different user name, the home directory differs and the shortcut does not work.
Put the real directory at that path. A symlink there does not help: Claude Code names its project directory and records the working directory from the real path (measured with Claude Code 2.1.310).
Links (symlinks, Windows junctions) also behave differently from one OS to another, so do not rely on them.

### Session digests

When you switch machines, leave a **session digest**, written by the agent, on the issue. The transcript itself can be carried too ([Carrying the raw transcript](#carrying-the-raw-transcript)), but a digest is usually enough.
The digest is kept on the issue as a comment with a marker at its start, and it shows up in SessionStart and in `next` for a session on the other machine.
The digest stays on the server, and the project's members read it. So never write the value of a credential into it.
**Never read the contents of a credential, never display them, never copy them anywhere else.**

#### What to write

- The gist of the user's instructions
- What was decided, and why
- What was tried and dropped
- Where the work stopped
- The next step

The branch and the HEAD SHA go into the header, so there is no need to repeat them in the body.
The body is limited to 16 KiB (16,384 bytes). Keep to what the next session needs to resume, not the whole history.

#### Sending one

Send it with `looptrack session digest <ID> -`. The body is read from standard input (leaving out the `-` does the same; give a file path and that file is read instead).

```bash
looptrack session digest DEMO-0004 - <<'EOF'
Instructions: add a show-password toggle
Decided: icon at the right of the field (as in search)
Dropped: a checkbox below (breaks the e2e selectors)
Stopped at: icon shown; e2e not run yet
Next: get e2e passing, then close
EOF
```

When the body does not start with the marker, the CLI puts these four header lines and a blank line in front of it.

```text
Session digest: home-mac
branch: feat/login-toggle
head: 3f2c1a9d0b7e4c6a8f1e2d3c4b5a69788796a5b4
ai: claude-code
```

- The marker is in the user's language (`Session digest:` in English, `セッション要約:` in Japanese), followed by the machine label.
- The machine label is the value of `LOOPTRACK_MACHINE_LABEL`. Without it, the part of the host name before the first `.` is used.
- `branch:` is `git rev-parse --abbrev-ref HEAD` in the working directory (`HEAD` when not on a branch), and `head:` is `git rev-parse HEAD`.
  When they cannot be worked out (outside a git working tree, say), nothing is sent. Write them into the header by hand and send it again.
- `ai:` is the kind of agent, worked out from environment variables (`claude-code`, `codex`, `copilot`, or `other` when unknown).
- To write the header yourself, put the marker and the machine label on line 1, and one line each of `branch:`, `head:` and `ai:` by line 8. The CLI adds only the lines that are missing and never rewrites a line you wrote.
- Machine paths inside the body are replaced before it is saved: the root of the git working tree becomes `./`, the home directory becomes `~`, and places only that machine has, such as where the agent keeps its transcripts, become `<machine-local>`. The agent's transcript directories follow `CLAUDE_CONFIG_DIR` and `CODEX_HOME` when they are set. To add more such places, list them in `LOOPTRACK_MACHINE_LOCAL`, separated the way the OS separates `PATH`.
- A digest cannot go into `close --comment` or `status --comment` (it is refused, and the status does not change either). Send it with `session digest` first, then change the status.

#### When it hits a secret

The server refuses a body that matches the shape of a value (`token: …`, `Bearer …`, a private key and the like). No comment is left.
The reason names only the line number and the rule, never the value.

```text
Error: DEMO-0004: line 3 of the session digest holds something that looks like a secret (token: …), so it was not saved. Replace the value with *** and send it again (for example token=***). Prose such as a quotation of the discipline text matches too, so reword it or replace the word with ***
```

- Replace the value on that line with `***` and send it again (`token: ***`).
- Prose such as a quotation of the discipline text matches too. Reword it in that case.

#### Reading it on the other machine

A digest written by another session is shown like this: the first eight non-empty lines after the marker line (up to 400 characters), indented.

```text
Note: the latest session digest on DEMO-0004 was written by another session (home-mac, 2h ago):
  branch: feat/login-toggle
  head: 3f2c1a9d0b7e4c6a8f1e2d3c4b5a69788796a5b4
  ai: claude-code
  Instructions: add a show-password toggle
  Decided: icon at the right of the field (as in search)
  Dropped: a checkbox below (breaks the e2e selectors)
  Stopped at: icon shown; e2e not run yet
  Next: get e2e passing, then close
```

- SessionStart (`looptrack issue summary`) shows the latest digest of each In Progress issue.
- `next` shows the latest digest of the issue it starts. When the machine handing over has put the issue back to Todo, its digest comes into view with `next` on the machine taking over.
- Only digests written by another session are shown. A digest your own session wrote is not.
- Take out the full text with `looptrack session pull <ID>`. It writes the newest three (change it with `--limit N`) to `.claude/.looptrack-work/<ID>-digest.md` and prints them to standard output too. The file is overwritten each time. With no digest, it says so and writes no file.

#### Catching a forgotten digest (loop)

With loop installed, the handoff freshness guard (Stop) can judge by digests. Set the environment variable `LOOPTRACK_LOOP_HANDOFF_BACKEND=issue`.

- After finishing work (a commit, a close and the like), if an issue this session has in progress lacks a digest of its own newer than that work, Stop is sent back once. It names the command to send, `looptrack session digest <ID> -`, for each issue.
- When the server cannot be reached, nothing is stopped. One notice is shown and the turn goes through; the next Stop after the connection is back checks again.
- An issue this session cannot be said to have started (one started from the web or from a shell you type into by hand, for example) is not judged; a notice names it.

To make digests required for a project, turn on the project rule `session.digest_required`.
In such a project the same check is added whatever the handoff backend (`LOOPTRACK_LOOP_HANDOFF_BACKEND`), and Stop goes through only when both pass.
The server does not refuse status changes. `looptrack project rules set` replaces the whole set of rules, so write the other rules into the same file ([Administration](server/admin.md#setting-project-rules)).

```json
{
  "session": { "digest_required": true }
}
```

### Carrying the raw transcript

When you want to read a whole earlier conversation again on another machine, or give the agent its earlier context as it was, you can carry the transcript itself to another machine of yours.
Most of the time the session digest above is enough. A digest is an issue comment the project's members read; a transcript is a raw record only you can see.
On the machine that imports it, you always continue by forking. The same conversation is never continued in two places.

#### What it needs

- The project rule `session.transcripts` must be on (it is off by default; see [Administration](server/admin.md#setting-project-rules)). In a project where it is off, registering a transcript and adding to one are both refused.
- The sending machine and the importing machine must be in the same OS family (Windows and Windows, or macOS and Linux). A transcript from the other family cannot be imported.
- The agents covered are Claude Code and Codex. Importing a Codex transcript (where it is placed, and whether `codex fork` finds it) has not been checked on a real machine yet.
- On the importing machine, log in as the same user who sent it. Only the person who sent a transcript can read it.

#### Sending

```bash
looptrack session transcript push --dry-run   # show the counts only, send nothing
looptrack session transcript push             # send the current session's transcript (from the second time on, only what follows)
```

- Run inside an agent session (from the agent's Bash tool, say), it sends the current session's transcript. From a shell you type into by hand, give the session id as an argument (`push <session-id> --agent claude-code`).
- It goes to the current project. The sending machine remembers how far it has sent, and the next push sends only what follows.
- A `push` you type by hand does not look at `LOOPTRACK_SESSION_TRANSCRIPTS`: typing it is the owner's consent. With the rule on, it sends.
- For Claude Code it carries the main file, the subagent transcripts and the tool-result files; for Codex, the rollouts of the same thread. Other files (Claude Code's `workflows/`, Codex's compressed `.jsonl.zst` and the like) are not carried.

The `--dry-run` output is counts only. It never shows a value or the content of a line.

```text
dry-run (nothing is sent): claude-code session 5f0c2a9e-7b1d-4c3e-9a8f-2d6b1e0c4a7f
Files: to send 31, not carried 7, rewritten at the source 0
Lines: 2104, chunks: 31, unfinished line (sent next time): 0 bytes
Paths rewritten: 4120
Masked values: 0
Absolute paths left unrewritten: structural fields 0, prose 41, keys 0
Not carried: 5f0c2a9e-7b1d-4c3e-9a8f-2d6b1e0c4a7f/workflows (workflows, 7)
```

#### What is rewritten and what is not

- **Paths in structural fields** (`cwd`, `file_path` and `path` in a tool's input, Bash's `command`, the executable path at the start of a hook's command line and the like) are rewritten. This machine's git working tree root, the agent's transcript and settings directories and the home directory are replaced with markers (placeholders) before sending, and the importing machine turns them back into its own paths. The original absolute paths never reach the server.
- **Credential-shaped values** (`token=…`, `Bearer …`, private keys and the like) are masked to `***`, string value by string value.
- **Prose is not rewritten**: the agent's replies, tool output, the contents of files it read, and the text of system reminders. Only structural fields, picked by key name, are rewritten.
  Paths left in prose are the sending machine's, so `pull` shows how many there are as a note. In your first message after the fork, give the agent the path of the checkout you continue in (as in rule 5 above).
- The transcript format is internal to each agent and changes between versions. When `push` notes "absolute paths left unrewritten in structural fields", the format has probably changed, and after a fork they may point at the old root.

#### When it cannot be sent

If a file has any of the following, `push` sends nothing, prints the reason for each file as counts only, and exits with code 1. It never shows a value or the content of a line.

- A line that cannot be read as JSON (including one that became unreadable after rewriting and masking)
- A secret-looking string in a key (a JSON name)
- A single line over the per-chunk limit (32MiB by default)

When a file became shorter or changed after it was sent, its continuation is not sent; the count is reported and the exit code is 1.
Run `purge` and push again to send it from the start.

#### Importing

Run it inside the git working tree you continue in. `list` shows the transcript ids (by default only those of the current project; `--all-projects` for all).

```bash
looptrack session transcript list
looptrack session transcript pull 42
```

```text
Placed transcript 42 (files: 31): /home/u/.claude/projects/-home-u-proj/c81d4e2a-3f6b-4a9c-8e1d-7b2f0a5c9d3e.jsonl
Note: absolute paths of the sending machine remaining in prose (AI replies, tool output and the like, which are never rewritten): 41
Resume (fork): claude --resume /home/u/.claude/projects/-home-u-proj/c81d4e2a-3f6b-4a9c-8e1d-7b2f0a5c9d3e.jsonl --fork-session
Do not open it with a plain claude --resume <id> (Claude Code) or codex resume (Codex): that appends to the imported copy. Always use the fork command
```

- **`pull` places it under a new session id.** The copy never takes the original conversation's id. Open it with the fork command it shows. The CLI does not start the agent.
- **Never resume the original id on two machines.** Opening the copy with a plain `claude --resume` (`codex resume`) appends to the imported copy.
- Running `pull` on the same transcript again finds the same copy in the same place and changes nothing. It shows an "already in place (nothing changed)" line and the resume command.
- For Codex it shows `codex fork <new-id>`. Run it at the root of the working tree.
- `--print-resume` prints only the resume command, one line, to standard output (the placing is the same). `--out <directory>` writes into that directory instead of where the agent looks (it is for reading the contents, and shows no resume command).

It stops without placing anything when:

- this machine already has a transcript with the original session id (it is the machine that sent it, or the conversation is already here)
- the sending machine is in the other OS family (within the same family, Linux and macOS, it places the copy after a note)
- the transcript has been purged
- a copy is already under another working tree, or a copy with different content is already in the same place (it shows only where)
- it was run outside a git working tree

#### Purging

```bash
looptrack session transcript purge 42 --reason "no longer needed"
```

This deletes the content. It cannot be undone. Running `push` on the same conversation after a purge sends it again from the start.

#### Who can see it and who cannot

- Through the API, the CLI and the web, **only the person who sent it** can read it. Project administrators cannot see it, and neither can server administrators (users with the role admin): to them it is as if it did not exist.
- **Transcripts are not encrypted. The server operator can read their contents** (anyone who can read the server's disk, database or backups). The stored content is plain JSONL, after masking and path rewriting.
- A transcript holds the contents of the files the agent read (source code, `CLAUDE.md`, command output) as they were. Sending it puts them on the server.
- Masking catches only what matches fixed rules. A secret no rule matches (a value with no set shape, customer information) stays as it is.
- A purge deletes the content files. The metadata (relative file paths, sizes, sha256, counts, times) stays. Content that went into a backup stays until it is deleted on the backup's side.
- The content is deleted automatically 90 days (by default) after the last push. Each user may store up to 2GiB in total by default, and a push beyond that is refused. An administrator can change both.

The best protection is never letting a credential's value into the conversation.
**Never read the contents of a credential, never display them, never copy them anywhere else.**

#### Sending automatically (hook)

`looptrack issue init` wires a hook that sends transcripts to Stop and SessionEnd for Claude Code and Codex (not for Copilot). To leave it out, use `looptrack issue init --no-transcript`.

- It sends only when both the project rule `session.transcripts` and the per-machine environment variable `LOOPTRACK_SESSION_TRANSCRIPTS=1` (off by default) are on.
- At Stop it sends nothing until 10 minutes have passed since the last attempt (change the interval with `LOOPTRACK_TRANSCRIPT_THROTTLE_MIN`). SessionEnd is not thinned out.
- The sending runs in a detached child process, and the hook returns at once. It never holds up the turn.
- At the start of a session (the SessionStart summary, which init also wires), these lines appear. When the rule is off or not known yet, the first two do not appear.
  - Both on: one line starting "Automatic transcript sending: on"
  - The rule on but no `LOOPTRACK_SESSION_TRANSCRIPTS=1`: one line pointing to that setting
  - Not being sent: one line with the last failure, or one with the unsent count. It appears at every session start until a send succeeds
- A transcript that could not be sent because of unreadable lines or a secret in a key is recorded as a failure once and not retried automatically. A `looptrack session transcript push` typed by hand still works as before.

## Installing through MCP only (the setup tool)

A machine with neither the CLI nor hooks is fine. You can install starting from just an MCP connection.

1. Add the MCP connection to the agent and authorise it in the browser. To have the agent add the connection too, use the prompts in "Installing by pasting one prompt" below
2. Ask the agent to "set up issue management". The agent calls the `setup` tool
3. The `setup` tool first returns only one question: install loop or not? The agent asks you, then calls `setup` again with your answer. What comes back is a single command (download → SHA-256 check → init), and the agent runs it once you approve. If `~/.local/bin/looptrack` (on Windows, `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`) is already there with the same SHA-256 as the distributed one, the download is skipped. Otherwise (or if there is none) it is downloaded, verified and swapped in, and init runs with it. If that place is not on your PATH, the same command adds it: on macOS and Linux one line goes into the startup file of your default shell (`$SHELL`): `~/.zshenv` for zsh (and also under `ZDOTDIR` if it is set in the environment), `~/.bashrc` plus the file a bash login shell reads for bash, `conf.d/looptrack.fish` for fish, `~/.profile` otherwise. On Windows it goes into your user environment variable `Path`. Nothing is written if it is already there. Until you reopen the agent app and the terminal, `looptrack` cannot be called by name (hooks run with the absolute path). When your version is out of date, "[Update the distributed files]" appears, and the same setup steps download it again
4. If there is no token yet, the agent asks for approval and runs the sign-in command from the setup steps: `~/.local/bin/looptrack issue login --browser --url <server URL>` (on Windows, in PowerShell: `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') issue login --browser --url <server URL>`). You sign in and allow access in the browser
5. You restart the agent and approve its hooks
6. At the start of the next session, a hook tells the server the install is complete, and the [Setup incomplete] note disappears from tool results

## Installing by pasting one prompt (a remote server)

These steps connect the agents on your own machine to a Looptrack server running on another machine or in the cloud.
All you do is fill in `<server URL>` and `<project>` in the prompt below and paste it into the agent.
The agent adds the MCP connection, calls `setup`, and runs the install command.
You only step in to allow access and sign in through the browser, approve commands, restart the agent, and trust its hooks.

**Never hand a token to the agent.** Don't put a token, a password, or a verification code into the prompt or the conversation.
Sign in with the `issue login --browser --url <server URL>` command from the setup steps (it runs the `looptrack` that setup placed, by its absolute path). The token travels only between the CLI and the server.

These steps haven't been checked on a real Windows machine.
Nor has Copilot in VS Code, on a real installation.

### Before you start

| Who | What to do |
| -- | -- |
| The server administrator | Creates your user (`<server URL>/admin/users`) and adds you to the project as an editor (on the same page, or at `<server URL>/admin/projects`) |
| You | Get the server URL (for example `https://example.com/looptrack`, without `/mcp` at the end) and the project slug from the administrator |
| You | Check that you can sign in to the server in the browser. If two-factor auth is required, register an authenticator app at your first sign-in |
| You | Start the agent in the top directory of the repository you want to connect. For Codex, mark that directory as trusted first |

If the project doesn't exist yet, the administrator usually creates it first (`<server URL>/admin/projects`).
If you're a server administrator yourself, though, you don't have to. When `setup` answers "Project not found",
an administrator gets a note that the MCP tool `create_project` can create it. The agent follows that note: it creates the project, then calls `setup` again.
The prefix (of the IDs) and the width (digits of the number) can never be changed afterwards. So before it creates the project, the agent shows you the slug, prefix and width and asks you to confirm them.
Anyone who isn't an administrator is told to ask one.

You add nothing to the prompt beyond `<server URL>` and `<project>`.
Paste the same prompt as many times as you like. The agent checks the install state with `setup` and picks up from the steps that are left.

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
7. Sign in with the issue login --browser command from the setup steps (the placed looptrack by its absolute path, with --url). Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
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
7. Sign in with the issue login --browser command from the setup steps (the placed looptrack by its absolute path, with --url). Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
8. Never read or display a token. Never ask me to paste a token.
9. For the [user] step (trusting the hooks with /hooks in the terminal codex), ask me and stop. When I paste this prompt again, confirm that setup reports Installed, and finish.
```

Codex has just one MCP configuration (`~/.codex/config.toml`) for the whole machine.
Here's the catch. If it already holds a looptrack entry for another project, setup is available at step 1, so steps 2 and 3 are skipped.
Without the project argument, setup would then return the steps for the project in the connection's header (the other project). And the agent wouldn't notice.
That's why step 4 passes project (setup falls back to the header's project only when the argument is left out).
Even after the install, the MCP tools use the header's project. If the header names another project, pass the project argument on every MCP tool call.
One more thing. The environment variables that init writes into the repository's `.codex/config.toml` take effect only after Codex restarts. So that it still works
before Codex is restarted, setup returns the steps' commands (including the token check, `looptrack issue config`) with the server's URL and project
already prefixed.

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
7. Sign in with the issue login --browser command from the setup steps (the placed looptrack by its absolute path, with --url). Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
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
7. Sign in with the issue login --browser command from the setup steps (the placed looptrack by its absolute path, with --url). Set the command's timeout longer than 5 minutes. I sign in and allow access in the browser myself.
8. Never read or display a token. Never ask me to paste a token.
9. For the [user] step (restarting copilot and choosing to always trust the folder when asked), ask me and stop. When I paste this prompt again, confirm that setup reports Installed, and finish.
```

Some Copilot models decide whether to install loop without asking the user.
That's why step 5 is worded more strongly in the Copilot prompts. You can also write your answer into the prompt up front (for example "do not install loop").

### Where your hands are needed

| Stage | What you do | Why it is you |
| -- | -- | -- |
| Before you start | The administrator creates your user and adds you to the project | Permissions are per project, and the administrator grants them |
| Adding the MCP connection | Approve the change the agent shows you | It rewrites the settings of the agent on your machine |
| Allowing MCP access | Sign in and allow access in the browser: `/mcp` in Claude Code, `codex mcp login looptrack` in a terminal for Codex, the browser that opens by itself when you restart Copilot CLI (`/mcp auth looptrack` if it does not open), and in VS Code when it asks | The server checks that it is you. Passwords and verification codes are never handed to the agent |
| Restarting and pasting again | Restart the agent (a new chat in VS Code) and paste the same prompt. Restart Copilot CLI once more after allowing access in the browser | A newly added connection is loaded after the restart. Copilot CLI starts the authorisation as soon as it starts, so the connection can pass its 10-second limit while it waits and fail with `Failed to connect … timed out after 10000 ms` |
| The loop question | Answer whether to install it | The user, not the agent, decides whether to install loop |
| The install command | Approve the single command: download → SHA-256 check → init. Claude Code asks for your permission for this command even in auto mode (it flags it as `Contains brace with quote character (expansion obfuscation)`) | It puts the executable in `~/.local/bin` and changes files in the repository |
| Signing in | Approve the sign-in command from the setup steps (`issue login --browser --url <server URL>` on the `looptrack` that setup placed), then sign in (with two-factor auth) and allow access in the browser that opens. If this machine already has a token, this stage is skipped | The token travels only between the CLI and the server and never appears in the conversation |
| Trusting the hooks | Claude Code: restart and approve if asked. Codex: trust them with `/hooks` in the terminal `codex`. Copilot: start a new session (in Copilot CLI, always trust the folder) | Hooks run only after the user accepts them |

In Codex, the sandbox blocks network access for the shell commands the agent runs.
So the install command and the sign-in need your approval to run with raised permissions.
In Copilot CLI, every call to an MCP tool needs your approval.

### When something goes wrong

| Symptom | What to check |
| -- | -- |
| The browser does not open to allow access | Start your default browser first. In Claude Code, close the `/mcp` screen and open it again. The sign-in command (`issue login --browser`) also prints the URL, so you can paste it into a browser that is already running |
| The MCP tools are still unavailable after pasting again | Check that you restarted the agent after the connection was added, and that you allowed access. Also check that the URL ends in `/mcp` |
| [Setup incomplete] does not disappear from tool results | Check that you restarted and trusted the hooks. Codex skips untrusted hooks silently. Without a registered token the hook cannot report (check with `looptrack issue config`) |
| The PATH or the hook wiring looks wrong | Run `looptrack doctor`. It checks the PATH, the hook wiring and the install record (it writes nothing) |
| The `looptrack` that setup installed is not found by name | Check that you reopened the agent app and the terminal (a PATH change only applies after that). If it is still not found, running the setup steps again adds its place to PATH. If setup reports it as installed and returns no steps, `~/.local/bin/looptrack doctor` (on Windows, in PowerShell: `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') doctor`) shows a command that adds it to PATH |
| `setup` answers "Project not found" | Check the spelling of the slug. If you are a server administrator, the agent asks whether it may create the project with `create_project`, showing the slug, prefix and width; check the values before you answer (the prefix and the width cannot be changed later). If you are not an administrator, ask one to create the project or to add you to it |
| loop was installed without asking you | Remove it with `looptrack issue init --remove-loop` |
| You need to work without a browser | Issue a token yourself at `<server URL>/account` and paste it into `~/.local/bin/looptrack issue login --url <server URL>` (on Windows, in PowerShell: `& (Join-Path $env:LOCALAPPDATA 'Programs\looptrack\looptrack.exe') issue login --url <server URL>`; the `/account` page shows both) in your own terminal. Never hand it to the agent |
