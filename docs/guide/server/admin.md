# Administration

[Guide contents](../README.md) · [Server edition](README.md) · Related: [Getting started with the server](getting-started.md) · [Updating the server and the CLI](updating.md)

An administrator is simply a user whose role is `admin`.
`looptrack setup` creates the first one.
The admin pages live under `<server URL>/admin/…`.

Admin commands (`looptrack user …`, `looptrack project …`, …) connect straight to storage. So load `LOOPTRACK_DSN` from `.env` into the environment before you run them (step 4 of [Getting started](getting-started.md)).
On a team server, run them as `docker compose run --rm --no-deps looptrack …`.

## Users

Page: `<server URL>/admin/users`

| Action | What the page lets you do |
| -- | -- |
| Add | Login (letters, digits, `. _ -`), display name, initial password, role (admin / member) |
| Change | Change role, disable / enable, reset password, reset two-factor auth, revoke access tokens |
| Project permissions | Grant or remove viewer / editor / admin per project |

- You can't change your own role or disable yourself. Any change that would leave no active administrator is refused too.
- Disabling a user ends their sessions. Their tokens stop working as well.
- Each user changes their own password, and issues or revokes their own access tokens, at `<server URL>/account`.

Admin commands do the same:

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
- A system administrator can only read projects they haven't joined. Want to write? Add yourself to the project as editor or admin.
- `<server URL>/admin/projects` lists every project's members and roles, and you can change them right there.
- If the person you remove or downgrade to viewer is assigned to open issues, you'll be asked to pick a replacement assignee.

```bash
looptrack member set demo alice --role editor
looptrack member list demo
looptrack member remove demo alice --reassign -    # leave their issues unassigned
```

## Projects

```bash
looptrack project create demo --prefix DEMO --name "Demo" --description "One-line description" --order 100
looptrack project list
looptrack project rename demo "Demo (new name)"   # change the display name only
looptrack project archive demo                    # archive ("delete" on the page)
looptrack project list --archived                 # list only the archived ones
looptrack project unarchive demo                  # restore
```

- A slug is lowercase letters, digits, and hyphens.
- `--prefix` and `--width` can't be changed later: issued IDs would break. Neither can the slug. Only the display name can change (`project rename`).
- `<server URL>/admin/projects` also lets you rename and archive each project, under "Display name and archiving". It's for administrators only.
- **Archiving doesn't erase anything.** The project drops out of the listings of the hub, the API, MCP and the CLI, and new issues, updates and comments are refused. But the issues, comments and history all stay.
  Before archiving, the page asks you to type the slug to confirm. Restore it and it's listed again, with its members and roles as they were.
  To restore, press "Restore" under "Archived projects" at the bottom of the page, or use `project unarchive`. The slug and the prefix are never reused.
- You can also register a per-project operating document. `guide` (CLI and MCP) then returns it to agents together with the common rules.

```bash
looptrack project guide set demo ./demo-rules.md --source demo-rules.md
looptrack project guide show demo
```

## Two-factor auth: required or optional

| Setting | Behaviour |
| -- | -- |
| Required | Everyone must register an authenticator app (TOTP). The first sign-in sends them to the registration page |
| Optional | Only people who registered are asked for a code at sign-in. Each user registers or removes it at `<server URL>/account` |

- You pick the initial setting in step ⑤ of `looptrack setup`.
- To change it later, use `<server URL>/admin/security` or the commands below. Turning "required" off asks for your password again, and for your code too if you registered one.
- Switching from optional to required invalidates any session that didn't pass two-factor auth.
- Registered TOTP secrets survive either switch.
- Access tokens for the CLI and MCP aren't affected.

```bash
looptrack settings two-factor              # current setting and change history
looptrack settings two-factor required
```

**If you lose `LOOPTRACK_SECRET_KEY` in `.env`, nobody's TOTP works any more.** Database backups don't include it. Keep a separate copy.

## Setting project rules

### require_on_close and verify

Rules are written in JSON and registered with `looptrack project rules set`.
**This command replaces the whole rule set.** Any rule missing from the file is gone.
So if you manage rules in a file, put every rule you use into it.

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
| `verify.require_evidence` | On by default. An issue with a verify-commands section can't be marked Done unless the latest `verify` record for the current body carries an attachment. Set it to `false` and you get a note instead of a rejection ([Daily use](../daily-use.md#attachments-evidence)) |

Unknown keys and misspellings are rejected at registration.
Every override (`--override "reason"`) is recorded on the server.

## Attachment limits and purging

On the admin screen's attachment page (`<server URL>/admin/attachments`) you change the attachment limits, see how much each project uses, and purge files.
How people attach files is in [Daily use](../daily-use.md#attachments-evidence).

- There are two limits, per file (20MiB by default) and per project (1GiB by default, counting attachments that aren't purged), shared by every project. A change takes effect from the next attachment without a restart, and the record shows who made it.
- When you raise the per-file limit, raise the body limit of the proxy in front too (`client_max_body_size` in nginx, for example). A request the proxy stops gets a 413 before it ever reaches the server.
- Purging is the way out when someone attaches a secret by mistake. It deletes the file itself and records the reason and who did it.
  The record of the file name and size stays, and reading it says "purged". Other attachments pointing at the same content are purged along with it, and none of it can be undone.
- If the purged file is also in a backup, delete it from there too.

### Where attachments live and backups

The files go on the server's disk and their records go in the database. **Backups come in two parts, the database and the attachment directory**, and you take the database first.

| How it runs | Attachment directory |
| -- | -- |
| install.sh (systemd) | `/var/lib/looptrack/attachments` |
| compose from `looptrack setup` | `<dir>/data/attachments` (the same with MySQL as the store) |

The database backup that install.sh's `--upgrade` takes doesn't include attachments.
`looptrack export` writes the attachment files and a manifest too, and `looptrack verify-files` checks that the files match the manifest. Still, `looptrack import` doesn't carry attachments, so take backups in the two parts.
How to take and restore backups, and `looptrack repair-attachments`, which finds where the database and the directory disagree, are in ["Where attachments live and backups" in DEPLOY.md](../../server/DEPLOY.md), written for operators.

## Token reports

Agent token usage piles up on the server, per issue and per stage.
What gets recorded, how it's added up, how the report is made: it's all in [Token reports](../token-report.md).
You can aggregate it by period, produce a PDF report, and record it in a ledger.

1. On the project board (`<server URL>/p/<slug>/`), use the report-request button to request a report for a period
2. The request shows up at the end of `summary`. In a project with the `token-report` skill, just ask the agent to "create the token report". It goes all the way through aggregation, text, PDF, and ledger entry (`looptrack issue init` installs the skill for Claude Code at `.claude/skills/token-report/SKILL.md`; the PDF is made with `looptrack report pdf`)
3. To aggregate by hand, use these commands

```bash
looptrack issue usage requests                             # report requests made in the web UI
looptrack issue usage report --since-last --json > r.json  # usage since the last report (or --from / --to)
looptrack issue usage report --since-last --xlsx r.xlsx    # the same, as a spreadsheet
looptrack issue usage ledger add "2026-09" --from-report r.json --note report.pdf   # record in the ledger (cannot be undone)
looptrack issue usage ledger list
looptrack issue usage missing --all-users                  # agent operations without token info, and the coverage rate
```

- Build the PDF with `looptrack report pdf --report summary.json --content body.json --out report.pdf`. A Japanese font is built in.
- Prompt texts (task names) aren't sent by default. You can switch this per project at `<server URL>/admin/projects`.
