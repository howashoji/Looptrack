# FAQ / Troubleshooting

[Guide contents](README.md) · Previous: [Agent-specific notes](ai-agents.md) · Next: [Token reports](token-report.md)

## "[Setup incomplete]" appears

MCP tool results carry [Setup incomplete] for one of two reasons.
Either the hooks, CLI, and instructions aren't installed in this agent's environment yet, or they are and the server hasn't heard about it.
The session-start hook is what reports a finished installation.

1. Ask the agent to "install using the setup tool's steps". Look over the steps it shows and approve them
2. If you have the CLI, run `looptrack issue init --project <slug> --url <server URL> --agent <agent>` in the project
3. Restart the agent and approve its hooks (for Codex, trust them with `/hooks` in the terminal `codex`)
4. Start a new session. Note still there? Check the local state with:

```bash
looptrack issue installed --agent claude-code   # codex / copilot / other
looptrack doctor
```

## "[Update the distributed files]" appears

You'll see it when your `looptrack` is older than the version the server expects.
Have the agent call the setup tool, and you get one command (with `--url`) that downloads and runs init. To do it by hand:

```bash
looptrack self-update --check --url <server URL>   # only check whether a newer version exists
looptrack self-update --url <server URL>           # replace it (the SHA-256 is verified)
looptrack issue init --project <slug> --url <server URL> --agent <agent>   # refresh rule texts, skills, and wiring
```

`self-update` downloads the `looptrack` the server distributes and swaps it in for the running binary.
Inside a project, `--url` comes from the `LOOPTRACK_API_URL` environment variable.
But a server with no distribution directory configured doesn't distribute `looptrack` at all.
Then replace it from GitHub releases with `looptrack self-update --from github`. It checks the signature and replaces the file only when a newer version exists. Downloading it again as in step 1 of [Getting started](server/getting-started.md) works too.
For how the notice works, and how to update the server and the desktop app, see [Updating the server and the CLI](server/updating.md) and [Updating the desktop app](desktop/updating.md).

## "Token usage has not been attached yet" appears

Sometimes, after a write operation, you'll see this:

```
Token usage has not been attached yet. Run: … usage attach <ID>
```

Just run the command it shows:

```bash
looptrack issue usage attach DEMO-0004
```

- If the end of `summary` says `operations missing token usage: N`, run `usage attach <ID>` for each listed issue (your own agent operations from the last 7 days).
- `looptrack issue usage missing` lists the operations without token info, plus the coverage rate.
- Operations a person types in a terminal don't count.
- With Copilot CLI, `usage attach` works once OpenTelemetry file export is on (it's also attached automatically after each change). **Don't run `usage attach` with Copilot in VS Code.** The shell never gets the conversation ID, so there's nothing to attach it to.
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
They're no longer used.
Re-run `looptrack issue init` and it removes them, then rewrites the wiring, permissions and guidance to `looptrack …`.

## "Setup is not finished" appears

A server with no active administrator shows "Setup is not finished (there is no active administrator)" in the web UI, and the API and MCP return 503 (`setup_required`).
The one exception is `/healthz`, for monitoring.

Create the first administrator and it works again. No restart needed.

```bash
cd ~/looptrack-server
looptrack setup            # when there is no .env
looptrack setup --force    # when .env exists but there are no users (LOOPTRACK_SECRET_KEY is kept)
```

To create just an administrator and keep `.env`, load `LOOPTRACK_DSN` and run this (the first user can't do without `--two-factor`):

```bash
set -a; . ./.env; set +a
looptrack user add admin --name "Admin" --admin --two-factor optional
```

Same thing if you disable administrators down to zero: after the next restart, the server shows "setup incomplete".

## The agent is stopped when it tries to finish

That's the freshness guard.
The agent read an issue and did work, but never updated that issue. So stopping is blocked, and it's sent back.
The point is to keep the next session from reading a stale issue as if it were current.

```bash
looptrack issue comment DEMO-0004 "What was investigated, what was done, what was checked"
looptrack issue close DEMO-0004 --comment "Acceptance-check results"
```

Only when no update is really needed, write the reason in the conversation and release it:

```bash
looptrack issue-freshness ack DEMO-0004   # exclude this ID only
looptrack issue-freshness reset           # exclude everything recorded in this session
looptrack issue-freshness show            # show what is recorded
```

## A rule rejected the change

The message tells you what to do next (add a comment, run `verify`, run `usage attach`, …).
Follow it.
Don't work around it through another path such as MCP or edit.
Use an override (`--override "reason"`) only when the user explicitly asks for one.

## push stopped with a conflict

Someone updated the issue after you took your working copy.
Merge the difference shown into your working copy, then apply it with `looptrack issue push <ID> --rebase`.

## Common Windows pitfalls

| Symptom | What to do |
| -- | -- |
| `looptrack` is not found | PATH changes only apply to terminals and apps started afterwards. Restart PowerShell and the agent |
| Only the agent's hooks cannot find `looptrack` | An agent launched from the Start menu may have a different PATH. If `looptrack` is not on PATH, init wires hooks with an absolute path in `.claude/settings.local.json`. Check with `looptrack doctor` |
| Windows warns about the executable ("Windows protected your PC") | The Windows binaries are currently not signed, so SmartScreen may warn on first start. Confirm the SHA-256 matches `SHA256SUMS`, and that `SHA256SUMS` itself verifies against `SHA256SUMS.minisig`, then click **More info** → **Run anyway** ("Signatures and OS warnings" in [Getting started](server/getting-started.md) has both checks). The macOS binaries are signed and notarized |
| Windows blocks the executable and there is no **Run anyway** | On Windows 11, Smart App Control blocks unsigned apps when it is turned on, and a PC your organization manages may block them by policy. In neither case can you allow Looptrack alone from your side. "Signatures and OS warnings" in [Getting started](server/getting-started.md) has what you can do |
| Here-documents (`<<'EOF'`) do not work | PowerShell has none. Write the body to a file and pass `--body (Get-Content -Raw body.md)`. Git Bash supports them |
| `. ./.env` does not work | In PowerShell, load it with the one-liner in step 4 of [Getting started](server/getting-started.md) |
| make fails when running gates (`looptrack gates`) | make on Windows may fail in directories whose path contains non-ASCII characters such as Japanese. Work in an ASCII-only path |
| Codex hooks do not run | Start the terminal `codex` in the project and trust them with `/hooks`. Typing `/hooks` in the desktop app's chat box is not a command |
| Port 8090 is in use | Change the port with `looptrack setup --force`, then update everything that uses the URL (init, MCP) |

We're still testing on completely fresh Windows machines.
If something doesn't work, we'd be glad to hear about it in the repository's issues.
