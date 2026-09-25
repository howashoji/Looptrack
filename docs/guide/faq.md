# FAQ / Troubleshooting

[Guide contents](README.md) · Previous: [Administration](admin.md)

## "[Setup incomplete]" appears

MCP tool results carry [Setup incomplete] when the hooks, CLI, and instructions are not yet installed in this agent's environment, or the server has not yet heard that they are.
The session-start hook is what reports a completed installation.

1. Ask the agent to "install using the setup tool's steps". Review the steps it shows and approve them
2. If you have the CLI, run `looptrack issue init --project <slug> --url <server URL> --agent <agent>` in the project
3. Restart the agent and approve its hooks (for Codex, trust them with `/hooks` in the terminal `codex`)
4. Start a new session. If the note is still there, check the local state with:

```bash
looptrack issue installed --agent claude-code   # codex / copilot / other
looptrack doctor
```

## "[Update the distributed files]" appears

It appears when your `looptrack` is older than the version the server expects.

```bash
looptrack self-update --check --url <server URL>   # only check whether a newer version exists
looptrack self-update --url <server URL>           # replace it (the SHA-256 is verified)
looptrack issue init --project <slug> --url <server URL> --agent <agent>   # refresh rule texts, skills, and wiring
```

`self-update` downloads the `looptrack` the server distributes and replaces the running binary.
Inside a project, `--url` can be taken from the `LOOPTRACK_API_URL` environment variable.
If the server does not distribute `looptrack` (no distribution directory configured), download the new version again as in step 1 of [Getting started](getting-started.md).
How the notice works, and how to update the desktop app and the server, is summed up in [Updating](updating.md).

## "Token usage has not been attached yet" appears

After a write operation you may see this:

```
Token usage has not been attached yet. Run: … usage attach <ID>
```

Run the command it shows:

```bash
looptrack issue usage attach DEMO-0004
```

- If the end of `summary` says `operations missing token usage: N`, run `usage attach <ID>` for each listed issue (your own agent operations from the last 7 days).
- `looptrack issue usage missing` lists operations without token info and the coverage rate.
- Operations a person types in a terminal are not counted.
- With Copilot CLI, `usage attach` works once OpenTelemetry file export is enabled (it is also attached automatically after each change). **Do not run `usage attach` with Copilot in VS Code.** The shell does not receive the conversation ID, so nothing can be attached.
- To stop sending usage, set the environment variable `LOOPTRACK_USAGE=0`.

## You cannot sign in

| Symptom | What to do |
| -- | -- |
| `No access token` / `Invalid token` / `Your login has expired` | Run `looptrack issue login --browser --url <server URL>` again |
| No browser opens | Paste the printed URL into a browser that is already running. It waits up to 5 minutes |
| No browser available (e.g. over ssh) | Create a token at `<server URL>/account` and paste it into `looptrack issue login --url <server URL>`. The token is shown only once |
| Forgot the web password | Ask an administrator to reset it at `<server URL>/admin/users` |
| Lost the authenticator app | Ask an administrator to reset two-factor auth (in the web page or with `looptrack user totp-reset <login>`) |
| `Project not found` | The slug is misspelled or you lack permission. Ask an administrator for access |
| 403 / "read only" when writing | You are a viewer on the project, or not a member. Ask to be made editor or above |
| `Cannot reach the server` | Check that the server is running (`<server URL>/healthz` returns 200). Locally, check that the `looptrack serve` terminal is still open |

Older projects may still contain the entry-point scripts an earlier setup placed under `.claude/`.
They are no longer used: re-run `looptrack issue init` and it removes them and rewrites the wiring, permissions and guidance to `looptrack …`.

## "Setup is not finished" appears

A server with no active administrator shows "Setup is not finished (there is no active administrator)" in the web UI and returns 503 (`setup_required`) from the API and MCP.
Only `/healthz`, used for monitoring, still answers.

Once you create the first administrator, it works again without restarting the server.

```bash
cd ~/looptrack-server
looptrack setup            # when there is no .env
looptrack setup --force    # when .env exists but there are no users (LOOPTRACK_SECRET_KEY is kept)
```

To create just an administrator and keep `.env`, load `LOOPTRACK_DSN` and run the following (the first user needs `--two-factor`):

```bash
set -a; . ./.env; set +a
looptrack user add admin --name "Admin" --admin --two-factor optional
```

If you disabled administrators down to zero, the server shows "setup incomplete" after its next restart.

## The agent is stopped when it tries to finish

That is the freshness guard.
If the agent read an issue and did work but never updated that issue, stopping is blocked.
This keeps the next session from reading a stale issue as if it were current.

```bash
looptrack issue comment DEMO-0004 "What was investigated, what was done, what was checked"
looptrack issue close DEMO-0004 --comment "Acceptance-check results"
```

Only when no update is really needed, record the reason in the conversation and release it:

```bash
looptrack issue-freshness ack DEMO-0004   # exclude this ID only
looptrack issue-freshness reset           # exclude everything recorded in this session
looptrack issue-freshness show            # show what is recorded
```

## A rule rejected the change

The message says what to do next (add a comment, run `verify`, run `usage attach`, …).
Follow it.
Do not work around it through another path (MCP or edit).
Use an override (`--override "reason"`) only when the user explicitly asks for it.

## push stopped with a conflict

Someone updated the issue after you took the working copy.
Merge the difference shown into your working copy and apply it with `looptrack issue push <ID> --rebase`.

## Common Windows pitfalls

| Symptom | What to do |
| -- | -- |
| `looptrack` is not found | PATH changes only apply to terminals and apps started afterwards. Restart PowerShell and the agent |
| Only the agent's hooks cannot find `looptrack` | An agent launched from the Start menu may have a different PATH. If `looptrack` is not on PATH, init wires hooks with an absolute path in `.claude/settings.local.json`. Check with `looptrack doctor` |
| Windows warns about the executable ("Windows protected your PC") | The Windows binaries are currently not signed, so SmartScreen may warn on first start. Confirm the SHA-256 matches `SHA256SUMS`, then click **More info** → **Run anyway** (see "Signatures and OS warnings" in [Getting started](getting-started.md)). The macOS binaries are signed and notarized |
| Here-documents (`<<'EOF'`) do not work | PowerShell has none. Write the body to a file and pass `--body (Get-Content -Raw body.md)`. Git Bash supports them |
| `. ./.env` does not work | In PowerShell, load it with the one-liner in step 4 of [Getting started](getting-started.md) |
| make fails when running gates (`looptrack gates`) | make on Windows may fail in directories whose path contains non-ASCII characters such as Japanese. Work in an ASCII-only path |
| Codex hooks do not run | Start the terminal `codex` in the project and trust them with `/hooks`. Typing `/hooks` in the desktop app's chat box is not a command |
| Port 8090 is in use | Change the port with `looptrack setup --force`, then update everything that uses the URL (init, MCP) |

Testing on a completely fresh Windows machine is still in progress.
If something does not work, please let us know in the repository's issues.
