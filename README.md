# Looptrack

*日本語版: [README.ja.md](README.ja.md)*

**Looptrack is an issue tracker serving as external memory for AI coding agents,
enabling loop engineering.** It records the work items an AI plans, apart from
your project's own issues, shows the plans and decisions behind them, and logs
the tokens each task consumed so you can report on them.

Four things make that matter:

- **External memory for AI coding agents.** In vibe coding, an AI can remember
  what it is working on only as far as its context window reaches. It cannot
  hold more than that, so across sessions it may repeat work or skip it. That
  calls for a memory kept outside the AI, and Looptrack was built to be one: a
  task memory made for AI. Every session and every agent reads the same issues,
  so what one session decided is still there for the next.
- **Loop engineering, made real.** By loop engineering we mean running
  *start → work → verify → close* from an issue, with the AI and people working
  on the same record. The biggest benefit is that it takes you a step beyond solo
  vibe coding: you can run that loop **with team development in mind**. People
  review the same issues in a browser, so a whole team can share one loop.
- **AI-planned work items, kept apart from project issues.** The feature requests
  and bug reports people discuss stay in your project's own tracker. Looptrack
  holds the units an AI breaks that work into — requirements, designs, tasks, the
  bugs it runs into, tests — tied together by parent and trace links, small
  enough to pick up one at a time and close with evidence.
- **Plans, decisions and tokens in plain view.** The plan is the issue itself:
  its body, its acceptance criteria and its child tasks. Causes found, decisions
  made and verification results are added as comments that can never be edited
  or deleted, anything that needs a human's judgement collects in *In Review*,
  and people read all of it in a browser. The tokens each task consumed are
  recorded as well and come out as an analysis report (a PDF).

Under the hood, it is an issue tracker for the loop between an AI and a human.
Looptrack keeps the issues of many projects in one server (a single Go binary on
MySQL or SQLite) and hands them to coding agents (Claude Code, Codex, GitHub
Copilot) over a CLI, a remote MCP endpoint and hooks. People read and review the
same issues in a browser.

Issues never get committed into the repositories being tracked. The database is
the source of truth, and the work products stay free of tracking numbers.

## Why another tracker

A project's own tracker holds what people ask for. Looptrack is the second place
an agent needs: one where that work is broken into items it can take one at a
time, and where something keeps the rules — because with that, an agent can run
the loop by itself.

Looptrack turns *file an issue → start it (`next`) → work → verify → close → next*
into something an agent performs, with the server enforcing what must not slip:
ID numbering, append-only comments and events, closed issues being immutable, and
the rules each project sets for itself. Every stage's token consumption is
recorded while that happens.

Three nested loops run at different speeds (the framing is Andrew Ng's; the
design is in [DESIGN.md](docs/server/DESIGN.md)):

- **The agent's working loop (minutes).** The agent runs the round by itself.
  `verify` executes the commands written in the issue's own verification section
  and records the result, so a machine decides whether it may be closed.
- **The human's decision loop (tens of minutes to hours).** Anything needing
  judgement collects in *In Review*, `summary` keeps it visible, and the agent
  raises it before picking up the next thing. Answers stay in the issue.
- **The outside feedback loop (hours to weeks).** What users and testers say goes
  back into the issue as a marked comment, and anything unanswered stays on the
  summary until it is.

What you get out of the box:

- **The minimum loop, from `core` alone.** Every session opens with a
  three-layer summary — the current round, what is waiting on a human, what came
  in from outside. `next` starts work, ending a session without updating an issue
  you were reading gets pushed back, and consumption accumulates per stage.
- **Discipline, if you add `loop`.** Optional guards: "just check it" stops
  before editing, malformed tool calls are bounced, a handoff note is required
  after each completed piece of work, context size and runaway background
  processes are watched, and changes reaching another repository ask first. The list is in
  [kit/README.md](kit/README.md). The guards catch slips only: a nested shell, `eval` and a
  prefix such as `sudo` are unwrapped one level, but a command wrapped in a command substitution
  or in a wrapper not on the list can go straight through. The known limits are in that table and in
  [secrets-discipline.md](kit/loop/rules/en/secrets-discipline.md).
- **Claude Code fits first.** Two lines in a project's `.claude/settings.json`
  point it at the server. Codex and Copilot are wired from the same manifest.
- **Token accounting.** Consumption is attributed to sessions, agents, stages and
  issues, and comes out as a PDF report (see [Token reports](#token-reports)).

## Getting started

Pick the entry point that matches how you want to run it. All three end in the
same place: a server, a browser, and a CLI your agents can use.

### 1. On your own machine — the desktop edition

For one person on one machine, with no terminal involved. Download the file for
your OS from the [releases page](https://github.com/howashoji/looptrack/releases),
check it against `SHA256SUMS`, and double-click it.

| OS | File |
| -- | -- |
| macOS 13+ (Apple silicon / Intel) | `Looptrack_<version>_macos_universal.dmg` |
| Windows 10 / 11 | `Looptrack_<version>_windows_amd64_setup.exe` (installer) or the `.zip` to run it in place |
| Linux (x86_64 / aarch64) | `Looptrack_<version>_linux_x86_64.AppImage` |

A local server starts (bound to 127.0.0.1, data in a single SQLite file) and your
browser opens on the first-run setup — administrator, two-factor, first project.
A tray icon (menu bar on macOS) opens the window, copies the MCP connection
settings for your agent, puts the CLI on `PATH`, and starts Looptrack at login.
No administrator rights are needed on any OS.

Details, including how each OS behaves on the first launch:
[Desktop edition](docs/guide/desktop.md).

### 2. On a Linux server — `install.sh`

On an Ubuntu LTS or Debian server, one line runs the installer (a POSIX shell
script). It does **download → verify → unpack → `looptrack setup` → (MySQL
grants) → start → health check**, including creating the systemd unit (or the
Compose file) and printing reverse-proxy examples.

```sh
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh
```

The installer downloads the newest release's
`looptrack_<version>_linux_<arch>_server.tar.gz` from GitHub Releases, checks it
against `SHA256SUMS` — and the minisign signature of `SHA256SUMS` when the
server has `minisign`; `--require-signature` makes the signature mandatory —
and only then unpacks it and puts `looptrack` in place. The script itself is
fetched over HTTPS only and cannot verify itself (it carries the key and the
steps), so if you want to read it before running it:

```sh
curl -fsSL -o install.sh https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh
less install.sh
sudo sh install.sh                      # sudo sh install.sh --version v1.0.0 to pin a version
```

Options go after `sh -s --` in the one-line form (for example
`… | sudo sh -s -- --version v1.0.0`). To install from artifacts you built
yourself (the output of `deploy/release/dist.sh build`), pass that directory to
`--from`:

```sh
sudo sh deploy/install.sh --from /path/to/dist
```

It first asks how to run it (systemd or Docker Compose), then asks the same
questions as `looptrack setup`. When it finishes it prints **nginx and Caddy
examples** (also written to `/etc/looptrack/proxy-examples.txt`), the browser
URL, the CLI sign-in command and the MCP connection settings. Put the proxy in
front and sign in on the public URL.

**If you choose MySQL and restrict the application's database user to the
minimum grants ([deploy/grants.sql](deploy/grants.sql)), the same run takes
care of them.** Per-table `GRANT` statements can only run once the tables exist,
so after `looptrack setup` creates the tables the installer asks on the terminal
for administrative MySQL credentials (not shown, not stored), creates the
application user if it does not exist yet, applies the grants built into
`looptrack` (`looptrack grants print` shows them), checks that the application
user can read, and then starts the server. If the credentials are wrong it stops
before starting anything; run it again and it picks up at the question.
If the database does not exist yet, setup asks for the same administrative
credentials up front and, after asking whether to go ahead, creates the database
too (you are asked only once). This needs the user that creates the tables
(`LOOPTRACK_SETUP_MIGRATE_DSN`) and its `GRANT` on that database to be in place
first; without them it stops at the connection as before. Answer no and it
stops without creating anything, showing the `CREATE DATABASE` statement to run
yourself.

The installer is for Linux only. To run a server on macOS or Windows, use the
desktop edition above.

Upgrade with
`curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`,
remove with `--uninstall` (`--purge` also removes configuration and data).
Servers installed with the `install.sh` of 1.0.0-rc.1 or rc.2 upgrade the same
way. Options and how to verify what you downloaded:
[DEPLOY.md](docs/server/DEPLOY.md).

### 3. Anywhere with Docker — Compose

`looptrack setup` writes a `compose.yaml` for you when you tell it you are
setting up a team server, together with the `Dockerfile` and `NOTICE` that
`compose.yaml` builds from, so this is the same wizard with a container at the end:

```sh
looptrack setup            # answer "team server", pick MySQL or SQLite
docker compose up -d       # builds the image from the Dockerfile written next to it
```

The image is `scratch` plus one statically linked binary (about 28 MB, no shell
and no curl), published to 127.0.0.1 only, read-only, non-root, with capabilities
dropped and a memory limit. Nothing is pulled from a registry — the image is built
from the binary sitting next to the `compose.yaml`. On Linux the wizard copies
itself there; on macOS or Windows, put a `linux/amd64` build of `looptrack` there
first (the wizard says so when it finishes). See [DEPLOY.md](docs/server/DEPLOY.md).

### The wizard itself

Whichever entry point you use, `looptrack setup` is what configures the server:

```bash
looptrack setup
```

It asks, in order: (1) how you will use it — single user or a team server,
(2) where to store data — SQLite or MySQL, (3) what to listen on, (4) the first
administrator, (5) whether two-factor authentication is required or optional,
(6) the first project — slug, ID prefix and display name (`-` creates none; you
can also create one later from the admin screen). It writes `.env` (plus
`compose.yaml`, `Dockerfile` and `NOTICE` for a team server) and the first
administrator, then prints the browser URL, the CLI sign-in command and the MCP
connection settings.

For single-user mode, start it afterwards with
`looptrack serve --env-file ./.env`; it listens on 127.0.0.1 only and skips
sign-in. Non-interactive use (`--yes`), interrupting it, and what a second run
does are covered in [DEPLOY.md](docs/server/DEPLOY.md).

### Then read the guide

The user guide is available in [English](docs/guide/README.md) and
[Japanese](docs/guide/ja/README.md), in this order:
[Concepts](docs/guide/concepts.md) →
[Getting started](docs/guide/getting-started.md) →
[Daily use](docs/guide/daily-use.md).

### Language

Messages you read — the CLI, the wizard, the web pages, errors, hook messages and
the desktop tray — come out in **English or Japanese**. The language is picked in
this order, and falls back to English:

| Order | Command line | Web pages and MCP |
| -- | -- | -- |
| 1 | `LOOPTRACK_LANG` (`ja` or `en`) | `?lang=ja` / `?lang=en` on the URL, for that one request |
| 2 | `LC_ALL`, then `LC_MESSAGES`, then `LANG` | `LOOPTRACK_LANG`, which the CLI sends as an explicit choice |
| 3 | English | The display language you pick on `/account` (it can be left unset) |
| 4 | — | `Accept-Language` |
| 5 | — | English |

Pick a language on `/account` and **even a connection that cannot send headers (MCP) comes back in it**.
`LOOPTRACK_LANG` on your terminal still wins over it.

Text that only an AI agent reads — the MCP `instructions`, the `guide` bodies and
the descriptions of the MCP tools and their inputs — also comes back in the language
picked for each connection, in the same order as above. For the rules and skills
installed into your project, both languages of the rules are installed, and the hooks
pick one at run time in the same order as above. A skill cannot
be picked at run time, so `looptrack issue init` installs only the one in the language
of the installation (same order; run it again after changing the language).

## What the server offers

| URL | What it is |
| -- | -- |
| `https://example.com/looptrack/` | Project picker (sign-in required) |
| `https://example.com/looptrack/p/<slug>/` | Board / list / trace, with issue detail (minimal forms to file, change status, comment and reassign) |
| `https://example.com/looptrack/account` | Account settings: issue and revoke access tokens, change password |
| `https://example.com/looptrack/admin/users` | User administration (administrators) |
| `https://example.com/looptrack/api/v1/` | REST API (bearer token) |
| `https://example.com/looptrack/mcp` | Remote MCP (OAuth 2.1 or a token) |

The public URL and the `/looptrack` prefix are both configurable.

- **Deployment**: one Go binary listening on 127.0.0.1 (container or systemd),
  with a reverse proxy in front forwarding the prefix. MySQL 8.4 or SQLite.
- **Authentication**: the web UI uses ID and password plus TOTP (the
  administrator decides whether TOTP is mandatory). The CLI and MCP use per-user
  access tokens with an expiry, revocation and a last-used time.
- **The database is the source of truth.** There is no periodic Markdown export;
  back up with a database dump. (`looptrack export` writes Markdown on demand, so
  your data is never locked in.)

### On the screen

The project picker lists every project you have access to, one line of
description each. Inside a project, one bar switches between three views of the
same issues — a **board** with a column per status, a **list** you can filter and
sort, and a **trace** view following the links between issues — and selecting an
issue opens its detail beside them: body, comments in order, events, and the
verification commands with their last result. The browser is mainly for
reading. The only writes it offers are changing the assignee and minimal forms
to file an issue, change its status and add a comment — an entry point for
people who do not use a terminal — and they go through the same server-side
operations as the CLI, MCP and the API, so every change passes the same rules.
Issue bodies are not edited in the browser; that, and everything else, goes
through the CLI, MCP or the API.

## The CLI

Each project calls `looptrack issue <subcommand>`; `looptrack issue init` wires a
project up in one go (see [ADD-PROJECT.md](docs/ADD-PROJECT.md)). The server and
project are chosen by `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT`, normally set
in the project's `.claude/settings.json`.

`looptrack issue login --browser` signs in through the browser and refreshes the
token from then on, storing it in `~/.config/looptrack/credentials.json` (mode
600; `%APPDATA%\looptrack\credentials.json` on Windows).

The CLI and the hooks are the same one executable. **Nothing else is installed
into your project** — no second runtime, no shell scripts, on any OS. How agents
use it is described in [AI-GUIDE.md](docs/AI-GUIDE.md).

One thing to know on Windows: `looptrack issue verify` runs an issue's
verification commands under `bash -c` and does not fall back to `cmd.exe` or
PowerShell, so it needs Git Bash (`winget install --id Git.Git -e`).
`looptrack doctor` says whether it found one.

## Issue freshness guard

**It stops a session that read an issue, did real work, and is about to end
without updating it** — which is what makes the next session start from a stale
picture.

| Hook | Command | What it does |
|---|---|---|
| `UserPromptSubmit` | `looptrack hook issue-freshness-mark` | Records issue IDs mentioned by the user |
| `PostToolUse` (`Bash\|Read\|Edit\|Write\|NotebookEdit`) | `looptrack hook issue-freshness-mark` | Records issue IDs that were read, and real work (file changes, `git commit`) |
| `Stop` | `looptrack hook issue-freshness-check` | **Pushes the stop back** if there was real work and an issue is still untouched |

The test is "was it updated even once during this session", answered by the
server's own update events. If the server is unreachable, nothing is blocked. The
escape hatches (`looptrack issue-freshness ack` / `reset`) are documented in
[AI-GUIDE.md](docs/AI-GUIDE.md).

Note that what a hook receives is a command string, and a word inside it is not
necessarily a word that will be executed — heredoc bodies and quoted text are
data. The shared component that extracts the words actually executed is
`internal/client/hook/hookcmd`.

## Token reports

**It measures how many tokens your agents spend on each issue, without anyone
keeping a log by hand.** Right after each change to an issue (from the CLI or
through MCP) and at the end of a turn or a session, `looptrack` reads the
agent's own conversation record on your machine and sends the running token
totals to the server. What the human types is not sent by default, and
`LOOPTRACK_USAGE=0` stops sending altogether.

The server attributes each stretch of the conversation to the issue whose
operation closed it, and adds it up for any period — per issue, label, stage,
agent and conversation. From the project board you can ask an agent for a
report; the `token-report` skill writes the prose, builds the PDF with
`looptrack report pdf` on your machine, and records the report in an
append-only ledger, where the next "since the last report" starts. The PDF is
never stored on the server.

What is recorded, how it is attributed, and how to make and record a report:
[Token reports](docs/guide/token-report.md).

## Repository layout

```
looptrack/
  cmd/looptrack/       the single executable (client: issue, hook, report, desktop …
                       server: serve, setup, migrate, user/member/token/project …)
  internal/
    domain/            issues, statuses, ready, matrix, ordering, per-project rules
    mdformat/          Markdown ⇔ model, round-trip exact
    store/             the database layer
    service/           what REST, MCP and the web UI all go through (including next)
    guide/             assembling the guide for agents: shared rules + project rules + docs
    server/            HTTP: REST, MCP, OAuth, sign-in, views, account, administration
    auth/              passwords (argon2id), TOTP, tokens, encryption
    transfer/          import, export and comparison
    client/            CLI, hooks, usage collection, PDF reports, self-update, desktop
  migrations/          SQL; a file that has shipped is never edited
  deploy/              image (Dockerfile, build.sh), install.sh, grants.sql,
                       rules/ (per-project rule examples), release/, dev/ (local MySQL)
  kit/                 what is distributed to each project: rules, skills and hook wiring
  docs/                guide, AI-GUIDE, ADD-PROJECT, projects/, templates/, server/
  NOTICE               third-party license texts (generated; go run ./internal/tools/notice)
```

## Documentation

| If you want to | Read |
| -- | -- |
| Use Looptrack day to day | [User guide](docs/guide/README.md) ([日本語](docs/guide/ja/README.md)) |
| Measure agents' token usage and make a report | [Token reports](docs/guide/token-report.md) |
| **Work with issues from another project** (agents start here) | [docs/AI-GUIDE.md](docs/AI-GUIDE.md) |
| **Put a new project on the server** | [docs/ADD-PROJECT.md](docs/ADD-PROJECT.md) |
| Write the operating rules for a project | [docs/projects/](docs/projects/) and the templates in [docs/templates/](docs/templates/) |
| Understand how the server works | [docs/server/DESIGN.md](docs/server/DESIGN.md) |
| Deploy, upgrade or administer a server | [docs/server/DEPLOY.md](docs/server/DEPLOY.md) |
| Know what is distributed to each project, and why it is generic | [kit/README.md](kit/README.md) |
| Build release artifacts | [docs/server/RELEASE.md](docs/server/RELEASE.md) |
| Contribute code | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Report a vulnerability | [SECURITY.md](SECURITY.md) |
| See what changed | [CHANGELOG.md](CHANGELOG.md) |

## Release artifacts and signatures

- **The macOS binaries in the official releases are signed with a Developer ID
  and notarized by Apple**, so Gatekeeper does not object
  (`codesign -dvv <file>` shows an Authority of
  `Developer ID Application: HOWA SHOJI K.K.`).
- **The Windows binaries are not signed yet.** SmartScreen may show "Windows
  protected your PC" on first run; choose **More info → Run anyway** — after
  checking the SHA-256 against `SHA256SUMS`.
- `SHA256SUMS` is signed with minisign (`SHA256SUMS.minisig`; the public key is
  [deploy/release/minisign.pub](deploy/release/minisign.pub), key ID
  29D707D7EBFF246B). `looptrack self-update` verifies that signature before
  replacing anything. By hand:
  `minisign -Vm SHA256SUMS -P RWRrJP/r1wfXKalGsxLnzFmmsExUd2azSJh4ccrYDJEBu8yE3N0ZlLJy`.
- Every artifact — the binaries, the `.app`, the dmg, the AppImage, the Windows
  zip, and `/NOTICE` inside the container image — carries the `NOTICE` file with
  the license texts of every dependency, of Go itself, and of the embedded font.
  `looptrack licenses` prints it too.
- Builds you make yourself are unsigned; the signing keys belong to the
  distributor. See [RELEASE.md](docs/server/RELEASE.md).

## Contributing

Bug reports and feature requests belong in GitHub Issues; please do not report
security problems there — [SECURITY.md](SECURITY.md) explains how. Development
setup, how to run the tests and the conventions for commits and pull requests are
in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026 HOWA SHOJI K.K., which is also
the distributor of the official release artifacts. Third-party components keep
their own licenses; their texts are collected in [NOTICE](NOTICE).
