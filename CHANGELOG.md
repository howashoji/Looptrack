# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

## [1.0.0-rc.2] - 2026-09-25

Second release candidate for 1.0.0. The changes below are relative to
`v1.0.0-rc.1`.

### Added

- **MCP `create_project` tool (administrators only).** Creates a project and
  adds the caller as its admin in one transaction, through the same code path
  as the admin page (`POST /admin/projects`); the values follow the rules of
  `looptrack project create`. When `setup` cannot find the project, its error
  now tells an administrator that `create_project` can create it (confirming
  the slug, prefix and width with the user first, since the prefix and width
  cannot be changed later) and tells anyone else to ask an administrator.
- **Rename, archive and restore projects.**
  `/admin/projects` gains "Display name and archiving" for each project:
  change the display name (the slug, the prefix and issued IDs stay as they
  are), or archive the project after typing its slug to confirm. Archiving is a
  soft delete: the project leaves every listing (hub, REST, MCP, CLI) and new
  issues, updates and comments are refused on every path, while its issues,
  comments and history are kept. "Archived projects" at the bottom of the page
  restores it. On the server, `looptrack project rename <slug> <display name>`
  changes the display name, `looptrack project archive|unarchive <slug>` do the
  same as the page, and `looptrack project list` hides archived projects unless
  `--archived` is given. Migration `0004_projects_archived` adds
  `projects.archived_at`; database grants are unchanged.
- **Desktop tray menu: left-click for the menu on every OS, a Settings item,
  and the version.** The tray icon now shows the menu on a left-click on
  Windows too (previously left-click opened the app there, and the menu needed
  a right-click); a new **Settings** item opens the account settings page
  (`/account`) in the browser; and an always-visible "Version {version}" item
  shows the running build's full version string (including any `-rc.N`), so a
  release-candidate build can be told apart from the final one without
  hovering over the icon or opening a terminal.
- **`pre-tool-subagent-model` hook (loop kit).** Stops a Claude Code subagent
  launch (`Task`/`Agent`) with `deny` when no `model` is given, and shows how
  to pick one by difficulty (haiku / sonnet / opus). Skips `subagent_type:
  fork`, a type whose definition file already sets `model:`, and names listed
  in `LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW`.

### Changed

- **The remaining Japanese-only messages are now in English too.** They follow
  the language of the connection (MCP) or of the terminal (CLI,
  `LOOPTRACK_LANG`), like the rest of Looptrack: the MCP prompts (`loop`,
  `review`, `setup`) and their errors; the messages about assignment,
  membership and usage-sending settings, and the `report_verify` guidance;
  errors and warnings about credentials, keys, passwords and the local server
  (server log warnings use the language of whoever started the server); errors
  from importing and from reading Markdown and JSON; the comments in the
  `compose.yaml` and `Dockerfile` that `looptrack setup` writes, and the loop
  section `looptrack issue init` writes into `AGENTS.md`; and the hooks'
  input/output errors, the `verify` command runner, the desktop app's warnings
  and notices, and the Copilot usage hint. Words that are matched as input
  (such as the `## コメント` heading and the answer `はい`) are unchanged.

### Fixed

- **MCP behind nginx: responses are no longer buffered.** The server now sends
  `X-Accel-Buffering: no` on every `/mcp` response, so the SSE stream of
  `subscriptions/listen` reaches the client even when nginx keeps its default
  `proxy_buffering on` (clients such as Copilot CLI gave up after 10 seconds).
  DEPLOY.md now also asks for `proxy_buffering off;` in a hand-written nginx
  configuration.
- **Git guard hook: an unquoted Windows absolute path to `git.exe` is
  recognised.** `pre-tool-git-guard` now reads the command both with POSIX
  escaping and with Windows rules (a backslash is not an escape), so
  `C:\tools\git.exe add -A` is judged like `git add -A` instead of slipping
  through. Git Bash behaves exactly as before. A path containing a space
  still has to be quoted to be recognised.
- **Secrets guard hook: a glob whose name is visible is confirmed.**
  `pre-tool-secrets-guard` now asks before a command that reads or copies
  `.env*` or `id_rsa*` (what remains after dropping the trailing glob names a
  secret). Listing-only commands such as `ls` are not affected, and globs that
  hide the name (`*`, `.e*`) are still not caught.
- **Secrets guard hook: an apostrophe in a trailing comment no longer breaks
  the template exemption.** `cp .env.example .env  # don't …` was taken for an
  unterminated quote and confirmed; a `#` comment outside quotes is now skipped.

### Docs

- **Installing by pasting one prompt (a remote server).** The AI agents guide
  gains ready-to-paste prompts for Claude Code, Codex, GitHub Copilot (VS Code)
  and Copilot CLI, what to prepare, where your hands are still needed (the
  install command in Claude Code even in auto mode, a second restart of Copilot
  CLI after authorisation, the token check in Codex before it restarts), and
  what to check when it goes wrong. The prompts pass `project` to the `setup`
  tool (Codex shares one MCP configuration across projects), and the token is
  never handed to the agent (sign-in uses `looptrack issue login --browser`).
- **New "Updating" guide.** What is automatic and what is manual when updating
  the CLI (`self-update` and the notice about updated distribution scripts),
  the desktop app and the server (`install.sh --upgrade`, or a server set up
  with `looptrack setup` alone), and the looptrack you distribute to users.
- **Desktop guide: "Get started without the CLI".** What to fill in on the
  first-run setup form, and how to connect an AI agent purely by chat from the
  tray's connection settings.

## [1.0.0] - unreleased

> The release date is not fixed yet. Fill in the date when the release is published.

First public release. Everything below is what Looptrack ships in 1.0.0.

### Added

- **Cross-project issue tracking.** One server holds the issues of many projects.
  Each project has its own ID prefix and zero-padded width (`MYP-0001`), its own
  rules and its own guide document. The database is the source of truth; nothing
  is committed into the source repositories being tracked.
- **Loop-shaped workflow.** `next` picks the issue to work on and enforces the
  rules for starting it; `verify` runs the commands written in the issue and
  records the result; `summary` reports three nested loops at the start of every
  session (the current round, decisions waiting for a human, feedback coming from
  outside). Comments and events are append-only, and closed issues are immutable.
- **Per-project rules enforced by the server.** Required comments, forbidden
  status transitions and `verify.require_on_close` apply to every access path
  (REST, MCP, CLI, Web) because they are checked in one place.
- **REST API** at `/looptrack/api/v1` with per-user access tokens (expiry,
  revocation, last-used time).
- **Remote MCP server** at `/looptrack/mcp`, reachable with OAuth 2.1 or a token,
  including a `setup` tool that wires a project up from the MCP connection alone,
  and `guide` / `review` / `loop` prompts.
- **Web UI**: project picker, board / list / trace views with issue details
  (read-only), account settings (tokens, password), and user administration.
  Sign-in is ID + password (argon2id) with TOTP, required or optional per server.
- **CLI (`looptrack issue`)**: create, edit, comment, status, close, list, show,
  next, verify, summary, guide, export, and `init` to wire a project up in one
  command. `looptrack issue login --browser` signs in through the browser and
  refreshes the token from then on.
- **Hooks and the distribution kit.** The `core` layer (session summary, issue
  freshness guard, token accounting) is always installed; the optional `loop`
  layer adds output/working/iteration discipline rules, task-mode and scope
  guards, handoff freshness and a runaway-process guard. Wiring is described by
  a manifest and generated for Claude Code, Codex and GitHub Copilot.
- **Token accounting.** Usage is recorded per session, per agent and per stage of
  the loop, attributed to issues, and rendered as a PDF report
  (`looptrack report pdf`) with an embedded font.
- **Desktop edition.** A double-clickable application for macOS, Windows and
  Linux that starts the server locally, keeps a tray / menu-bar entry, offers to
  put the CLI on `PATH`, and can start at login without administrator rights.
- **Setup and installation.** `looptrack setup` asks a handful of questions and
  writes `.env` (and `compose.yaml` for a team server) plus the first
  administrator. `deploy/install.sh` (POSIX sh) does fetch → setup → start →
  health check on a fresh Linux server, prints nginx and Caddy examples, and also
  handles `--upgrade` and `--uninstall`. A Dockerfile and a Compose file are
  provided as well.
- **Local mode.** A single-user server bound to 127.0.0.1 with SQLite and no
  sign-in, for people who do not want to run a shared server.
- **Signed release artifacts.** `SHA256SUMS` signed with minisign, macOS binaries
  signed with a Developer ID and notarized, and `looptrack self-update` verifying
  the signature before replacing the binary. Every artifact ships a `NOTICE` with
  the full license texts of the third-party components.
- **Markdown export.** `looptrack export` writes issues back out as Markdown, so
  the data is never locked in.
- **Worktree housekeeping.** `looptrack worktree list` shows every git worktree
  with the one thing you need to decide on it — merged or not, uncommitted
  changes (ignoring build leftovers), locked, and **when it last moved** — and
  `looptrack worktree prune` cleans up. It never deletes anything unless you pass
  `--yes`, never touches the main worktree, and keeps anything that moved
  recently, because another session may still be inside it.

[Unreleased]: https://github.com/howashoji/looptrack/compare/v1.0.0-rc.2...HEAD
[1.0.0-rc.2]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.2
[1.0.0]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0
