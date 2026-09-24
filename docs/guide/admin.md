# Administration

[Guide contents](README.md) · Previous: [Agent-specific notes](ai-agents.md) · Next: [FAQ / Troubleshooting](faq.md)

An administrator is a user whose role is `admin`.
`looptrack setup` creates the first one.
The admin pages live under `<server URL>/admin/…`.

Admin commands (`looptrack user …`, `looptrack project …`, …) connect straight to storage.
Load `LOOPTRACK_DSN` from `.env` into the environment before running them (step 4 of [Getting started](getting-started.md)).
On a team server, run them as `docker compose run --rm --no-deps looptrack …`.

## Users

Page: `<server URL>/admin/users`

| Action | What the page lets you do |
| -- | -- |
| Add | Login (letters, digits, `. _ -`), display name, initial password, role (admin / member) |
| Change | Change role, disable / enable, reset password, reset two-factor auth, revoke access tokens |
| Project permissions | Grant or remove viewer / editor / admin per project |

- You cannot change your own role or disable yourself. Changes that would leave no active administrator are refused.
- Disabling a user ends their sessions and their tokens stop working.
- Each user changes their own password and issues or revokes their own access tokens at `<server URL>/account`.

The same can be done with admin commands:

```bash
looptrack user list
looptrack user add alice --name "Alice"          # prompts for the password twice
looptrack user disable alice
looptrack user totp-reset alice
```

## Permissions

| Role | Can |
| -- | -- |
| viewer | Read only (cannot comment or leave feedback) |
| editor | Create, comment, change status, change assignee |
| admin (project) | Write, the same as editor |

- Permissions are per project.
- **A system administrator can only read projects they have not joined.** To write, join the project as editor or admin.
- `<server URL>/admin/projects` lists every project's members and roles and lets you change them.
- If the person you remove or downgrade to viewer is assigned to open issues, you are asked to choose a replacement assignee.

```bash
looptrack member set demo alice --role editor
looptrack member list demo
looptrack member remove demo alice --reassign -    # leave their issues unassigned
```

## Projects

```bash
looptrack project create demo --prefix DEMO --name "Demo" --description "One-line description" --order 100
looptrack project list
```

- A slug is lowercase letters, digits, and hyphens.
- `--prefix` and `--width` cannot be changed later, because issued IDs would break.
- If you register a per-project operating document, `guide` (CLI and MCP) returns it to agents together with the common rules.

```bash
looptrack project guide set demo ./demo-rules.md --source demo-rules.md
looptrack project guide show demo
```

## Two-factor auth: required or optional

| Setting | Behaviour |
| -- | -- |
| Required | Everyone must register an authenticator app (TOTP). The first sign-in sends them to the registration page |
| Optional | Only people who registered are asked for a code at sign-in. Each user registers or removes it at `<server URL>/account` |

- The initial setting is chosen in step ⑤ of `looptrack setup`.
- To change it later, use `<server URL>/admin/security` or the commands below. Turning "required" off asks for your password again (and your code, if you registered one).
- Switching from optional to required invalidates sessions that did not pass two-factor auth.
- Registered TOTP secrets are kept either way.
- Access tokens for the CLI and MCP are not affected.

```bash
looptrack settings two-factor              # current setting and change history
looptrack settings two-factor required
```

**If you lose `LOOPTRACK_SECRET_KEY` in `.env`, nobody's TOTP will work any more.** It is not in database backups, so keep a separate copy.

## Setting project rules

### require_on_close and verify

Rules are written in JSON and registered with `looptrack project rules set`.
**This command replaces the whole rule set.** Any rule missing from the file is removed,
so if you manage rules in a file, write every rule you use into it.

```json
{
  "verify": { "require_on_close": true },
  "usage": { "require_on_close": true }
}
```

```bash
looptrack project rules set demo ./demo-rules.json
looptrack project rules show demo
looptrack project rules clear demo
```

| Key | Effect |
| -- | -- |
| `verify.require_on_close` | An issue with a verify-commands section cannot be marked Done unless the latest `verify` against the current body passed completely |
| `usage.require_on_close` | When an agent marks an issue Done or Canceled, refuse if there is no token info from that conversation. The CLI attaches it automatically and retries once |

Unknown keys and misspellings are rejected when you register the rules.
Every override (`--override "reason"`) is recorded on the server.

## Token reports

Agent token usage accumulates on the server per issue and per stage.
You can aggregate it by period, produce a PDF report, and record it in a ledger.

1. On the project board (`<server URL>/p/<slug>/`), use the report-request button to request a report for a period
2. The request appears at the end of `summary`. In a project that has the `token-report` skill, asking the agent to "create the token report" takes it through aggregation, text, PDF, and ledger entry (`looptrack issue init` installs the skill for Claude Code at `.claude/skills/token-report/SKILL.md`; the PDF is made with `looptrack report pdf`)
3. To aggregate by hand, use these commands

```bash
looptrack issue usage requests                             # report requests made in the web UI
looptrack issue usage report --since-last --json > r.json  # usage since the last report (or --from / --to)
looptrack issue usage report --since-last --xlsx r.xlsx    # the same, as a spreadsheet
looptrack issue usage ledger add "2026-09" --from-report r.json --note report.pdf   # record in the ledger (cannot be undone)
looptrack issue usage ledger list
looptrack issue usage missing --all-users                  # agent operations without token info, and the coverage rate
```

- Build the PDF with `looptrack report pdf --report summary.json --content body.json --out report.pdf` (a Japanese font is built in).
- Prompt texts (task names) are not sent by default. Switch this per project at `<server URL>/admin/projects`.
