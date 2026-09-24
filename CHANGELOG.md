# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

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

[Unreleased]: https://github.com/howashoji/looptrack/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0
