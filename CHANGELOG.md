# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0-rc.3] - 2026-09-29

Third release candidate for 1.0.0. The changes below are relative to
`v1.0.0-rc.2`.

### Added

- **One-line server installer.** On a Linux server,
  `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh`
  runs the installer from `main`. Without `--from` it downloads the newest
  release's `looptrack_<version>_linux_<arch>_server.tar.gz` from GitHub
  Releases (`--version <version>` picks one), checks it against `SHA256SUMS` and,
  when `minisign` is installed, the signature of `SHA256SUMS`
  (`--require-signature` makes the signature mandatory), and only then unpacks
  it and installs `looptrack`. The script itself is only fetched over HTTPS and
  cannot verify itself; download and read it first if you prefer. `--from`
  still takes a local `deploy/release/dist.sh build` output or a server's
  distribution directory (bare binaries). The installer is for Linux only;
  elsewhere it stops before changing anything and points to the desktop app.
- **`looptrack grants print|apply`.** The minimum MySQL grants
  (`deploy/grants.sql`) are now built into `looptrack`. `grants print` prints
  them for the database and user in `LOOPTRACK_DSN` (not only `im` and
  `im_app`); `grants apply` asks for administrative credentials on the
  terminal (never shown or stored), creates a missing database or application
  user after asking, creates the tables if they are missing, applies the
  grants, and checks that the application user can read.
- **New-version notices in the desktop app and the server.** Both check the
  list of releases on GitHub at startup and every 24 hours, and tell you only
  about a version whose `SHA256SUMS` signature checks out and whose release
  carries the file for your OS and CPU. They follow the kind of version you run
  (an rc hears about rcs too, a stable release only about stable releases), and
  a failed check (offline, say) keeps the previous notice. The desktop app shows
  it at the top of the tray menu and in a strip in the web UI; clearing "Check
  for updates" in the tray stops checking. The server shows it in a strip for
  administrators (with the one-line update command), in `looptrack doctor` run
  with an administrator's token, and in its log. `LOOPTRACK_UPDATE_CHECK=off`
  stops checking (no connection to GitHub), `LOOPTRACK_UPDATE_CHANNEL`
  (`stable` or `prerelease`) chooses what to follow, and `LOOPTRACK_UPDATE_URL`
  points the check at another `https://` endpoint of the same form. The CLI
  does not check GitHub, and a build you made yourself (`dev`) is not checked.
- **Updating the desktop app in one click (macOS and Linux).** "Update to
  version <version>" at the top of the tray menu, or "Update now" in the strip
  in the web UI, downloads the new version, checks it against the signed
  `SHA256SUMS` (on macOS also with `spctl` and `codesign`, requiring the same
  identifier and signing team), replaces the app while keeping the previous one
  under a `.prev` name, and restarts; if the new version does not start, the
  previous one is put back. "Install updates automatically" in the tray does the
  same by itself when a new version is found (off by default). On Windows, or
  where the app's location cannot be written to, download the new version from
  the release page (on macOS the verified dmg is opened for you).
- **Opt-in automatic server updates.** `install.sh --auto-upgrade on|off`
  (`LOOPTRACK_INSTALL_AUTO_UPGRADE`; off by default; systemd only; needs
  `minisign`) installs a daily `looptrack-upgrade.timer` that runs the copy of
  `install.sh` kept on the server with
  `--upgrade --require-signature --yes --only-newer`. It never moves
  to an older version, and if a run fails after the service was stopped, it puts
  the previous version back and starts it again. With MySQL, a new version with
  migrations to run is not installed unattended (run `--upgrade` on a terminal).
  It is refused for compose and `--no-start` servers.
  `journalctl -u looptrack-upgrade` shows what each run did. `--only-newer` can
  also be passed by hand, and the new `looptrack migrate --check` lists pending
  migrations without applying them and exits with code 3 when there are any.
- **Desktop app: a backup before each database upgrade.** Before a new version
  migrates existing data in the local SQLite database, the desktop app (and a
  server in `LOOPTRACK_LOCAL_MODE`) copies `looptrack.db` into the `backups`
  folder of the data folder (`looptrack.db.<UTC time>`) and keeps the two newest
  copies. If the copy cannot be made, it neither migrates nor starts.
- **Desktop app: listed in the Linux app list.** The AppImage puts
  `~/.local/share/applications/looptrack.desktop` and its icon in place when it
  starts, so it can be started from the launcher and the activities search, and
  the entry follows the AppImage when it moves. Clearing "Show in the app list"
  in the tray removes the entry and keeps it off.
- **`-` reads the text from standard input.** `looptrack issue comment <ID> -`,
  `issue new --body -` and `--comment -` of `issue status`, `issue close` and
  `issue next` now read the text from standard input, as
  `looptrack handoff append` already did; empty input is refused without
  sending anything. Before, `-` was saved as the text itself.
- **A note when an issue is started with template acceptance criteria.** In a
  project with `acceptance.require_on_close`, setting an issue to In Progress
  (including through `next`) while its "## Acceptance criteria" are still the
  template now returns a note that closing it will be refused as it stands. The
  start itself is not blocked. The note appears in the CLI, in REST responses
  (`acceptance_notice`), in MCP `set_status` and `next`, and in the web UI.
- **Telling your own MCP start from another conversation's (Claude Code).** A
  new core hook, `looptrack hook issue-session-bind` (PreToolUse on `mcp__.*`),
  hands the server the tool call's ID and the conversation's session ID right
  before a looptrack MCP tool runs, so MCP `next` and the lists can tell a start
  by another conversation of the same user from your own, even over the same
  MCP connection. It is cut off after 1.5 seconds and lets the call through on
  any failure; `looptrack issue init --no-session-bind` leaves it out. In
  addition, when the token-accounting hook sends the snapshot for an MCP call,
  the server ties that start to the conversation (migration
  `0006_issue_event_sessions`), so the CLI can tell the two apart as well.
  Other agents are not affected.
- **`LOOPTRACK_LOOP_HOOK_LOG=1` records hook verdicts.** Every verdict (the core
  hooks included) is appended as one JSONL line to
  `<state dir>/.looptrack-freshness/hook-log.jsonl`, holding only the time, the
  hook, the event, the decision, the session and a word for the kind of reason —
  never the command, the prompt, the wording of the reason or a path. It rotates
  at 1 MiB. Off by default.

### Changed

- **The installer applies the MySQL grants in the same run.** With MySQL and
  the minimum grants, `install.sh` used to stop after `looptrack setup` and ask
  you to run `grants.sql` yourself and then run it again. It now asks for
  administrative credentials on the terminal at that point, runs
  `looptrack grants apply`, checks that the application user can read, and
  goes on to start the server and check it. If the credentials are wrong it
  stops without starting anything and, when run again, continues from the
  question. `--upgrade` does the same when a new version adds tables. Without a
  terminal (systemd only), pass `LOOPTRACK_INSTALL_DB_ADMIN_USER` and
  `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE`.
- **`looptrack setup` (and so the installer) creates a missing MySQL database.**
  When the database in the connection string did not exist yet, setup stopped
  at the connection, before the migration, so the installer never got as far
  as creating it. Setup now notices the missing database, asks for
  administrative credentials on the terminal (once; never shown or stored),
  asks whether to create the database (even with `--yes`), and creates it and
  the application user, the tables and the grants the same way as
  `looptrack grants apply` before carrying on. An existing database is left as
  it is. Answer no, or run without a terminal, and it stops without creating
  anything and shows the `CREATE DATABASE` statement to run yourself.
- **GitHub Releases ship the server binaries as archives.** Each of the six
  targets is now `looptrack_<version>_<os>_<arch>_server.tar.gz` (Linux,
  macOS) or `looptrack_<version>_windows_<arch>_server.zip` (Windows). An
  archive unpacks into a single folder, `looptrack_<version>_<os>_<arch>_server/`,
  holding `looptrack` (`looptrack.exe`), `NOTICE`, `OFL-BIZUDGothic.txt` and
  `LICENSE`. The bare binaries `looptrack_<version>_<os>_<arch>[.exe]` and the
  standalone `install.sh` and `grants.sql` are no longer release assets, and
  the archives do not contain them either. The desktop app's assets keep their
  names, and `NOTICE` and `OFL-BIZUDGothic.txt` are still attached on their
  own. `SHA256SUMS` lists the archives and, in addition, the binary inside each
  archive under its previous name (`looptrack_<version>_<os>_<arch>[.exe]`), so
  a binary taken out of an archive can still be checked against the signed
  list. Because those lines name files that are not attached, check a single
  line with `grep … | sha256sum -c -`, or use
  `sha256sum -c --ignore-missing SHA256SUMS`.
- **The server's distribution directory and `self-update` are unchanged.**
  `LOOPTRACK_DIST_DIR` still holds bare binaries named
  `looptrack_<version>_<os>_<arch>[.exe]`; archives placed there are not
  listed or served. To distribute an official release, take `looptrack` out
  of each archive, put it under that name, and put the release's `SHA256SUMS`
  and `SHA256SUMS.minisig` next to it as they are (see "The looptrack you
  distribute to users" in the updating guide).
- **Release candidates are the Latest release until 1.0.0 is out.** Until a
  stable `vX.Y.Z` tag exists, a release candidate is published as the Latest
  release on GitHub Releases (not as a pre-release) and gets the `latest` tag on
  GHCR, so the one-line installer (which reads `releases/latest`) installs it.
  Release candidates published after 1.0.0 go back to being pre-releases.
- **Verification records follow the language of the user who records them.**
  The comment that `verify` (and MCP `report_verify`) leaves is written in the
  recorder's language, chosen the same way as everything else shown to that
  user; records already stored are unchanged. In English, the mark for a result
  reported over MCP now reads "self-reported via MCP" everywhere.
- **Creating a project fills in omitted values the same way on every path.**
  `looptrack project create` no longer requires `--prefix` and `--name` (they
  default to the slug in upper case and the slug, as on the admin page and in
  MCP `create_project`), and the first project created by `looptrack setup` now
  gets the default sort order (100) like the others. An explicit `--order 0` is
  kept as 0.
- **`pre-tool-subagent-bound` also asks subagents to check identifiers (loop
  kit).** The cautions it appends to a subagent's instructions now include
  cross-checking every identifier in them (a variable or function name, a
  commit SHA, a line number, a file path) against the real thing with
  `git show` / `git grep` before use, and reporting any mismatch in one line.
  `background-process.md` carries the same line for agents where nothing is
  appended automatically.
- **More working discipline in the loop kit's rules.** `working-discipline.md`
  gains rules on counting both sides of a shared component, re-checking with a
  different method, what a green summary does and does not prove, controls for
  "X does not happen" tests, probing guards for the forms they miss, running
  the goldens a change records, running the build and type check right after a
  rebase, and pushing rules and state down to subagents (with a template to copy
  into their instructions).

### Fixed

- **`self-update` checks the listed version against the signature.** It used to
  trust the version in the (unsigned) distribution listing alone, so a listing
  could pair an older release's correctly signed `SHA256SUMS` with a higher
  version number and roll the CLI back. It now requires the file name in the
  signed `SHA256SUMS` (and, for a release, the version in the signature's
  trusted comment) to match the listed version, OS and CPU, and replaces nothing
  otherwise. Going back to an older version still needs `--force`.
- **An older looptrack no longer runs on a database migrated by a newer one.**
  `migrate`, `setup`, the desktop app, local mode and `serve` on a team server
  now refuse a database that has applied migrations they do not know, instead of
  using data in the newer shape. To go back, restore the database from a backup
  taken before the newer version migrated it. When a migration in
  `install.sh --upgrade` fails after applying something, the installer now says
  to restore the database from the backup as well.
- **MCP `setup` no longer replaces a `looptrack` that is already installed.**
  When `~/.local/bin/looptrack` (on Windows,
  `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`) exists, the returned command
  neither downloads nor replaces it and only runs `init` with it, so a server
  distributing an older version can no longer downgrade a newer local one (the
  next `setup` suggests `self-update` when the local one is older).
- **MCP `setup` prefixes the environment variables for Codex too.** Codex picks
  up the variables `init` writes into `.codex/config.toml` only after a restart,
  so, as for Copilot, the returned commands (including the token check) now
  carry `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT`.
- **Token accounting covers every operation that records an event.** Tokens are
  now also attached to MCP `next` (when it starts an issue), `report_verify` and
  `assign_issue`, and to the CLI's `issue assign` when the assignee changes, so
  these no longer show up as unattributed. A snapshot that failed to send and was
  resent later is attached to the original operation even after the attach
  window has passed (migration `0005_usage_snapshots_attempted_at`; a server that
  does not know the new field gets the snapshot again without it). Unsent
  snapshots are kept per server and project and resent only to the project they
  came from (older ones without that information are not resent and are dropped
  after 7 days), and snapshots taken while signed out are kept and sent after
  `looptrack issue login`. Send failures are no longer silent: the session-start
  summary reports the last failure and how many snapshots are waiting.
- **Export and import respect archived projects.** `looptrack export` leaves
  archived projects out unless `--archived` is given, and `looptrack import`
  refuses to import into a project that is archived (replacing it would erase
  the data kept there; unarchive it first).
- **Hooks see through wrapped commands.** The issue freshness guard and the
  handoff completion record now recognise `git commit` / `push` / `merge`,
  `looptrack issue …` calls and completions (`close`, `status Done`) wrapped in
  `bash -c` / `sh -c` / `eval`, behind prefix words such as `sudo`, `env`,
  `nohup` and `xargs`, inside a `function`, or split by a line continuation
  (the nested shells and prefix words are unwrapped the same way as in the git
  and secrets guards). A commit of only the
  handoff file made in another worktree is now told apart correctly.
- **Runaway-process guard: an unquoted `ps` line is judged as a wait loop.**
  `ps` drops the quotes from a command line
  (`/bin/bash -c while true; do sleep 1; done`), which used to slip past the
  shorter threshold for unbounded wait loops.
- **Task-mode hook: a subagent's report no longer switches the mode.** Claude
  Code passes a subagent's final report through UserPromptSubmit wrapped in
  `<agent-message …>`; the words in it no longer switch between investigate and
  execute mode, and issue IDs in it no longer count as mentioned by the user for
  the freshness guard.
- **A regular expression that Go (RE2) cannot read is reported.**
  `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE`, `_INVEST_RE`, `LOOPTRACK_LOOP_RUNAWAY_ALLOW`
  and `LOOPTRACK_MCP_SERVER` were silently ignored when written with lookahead
  and the like. The hook now shows the variable name and the error as a message
  and carries on without it (the default words and exemptions still apply;
  `LOOPTRACK_MCP_SERVER` matches nothing and is reported on MCP tool calls only).
  The guides now say these are RE2.
- **`looptrack issue init` no longer adds a redundant `.gitignore` line.** When
  `.claude/.looptrack-freshness/` is already ignored by a non-negated rule in a
  `.gitignore` inside the working tree, init leaves the top-level `.gitignore`
  alone (rules only in `.git/info/exclude` or a global excludes file do not
  count, since other clones do not share them).
- **English messages point to headings that exist.** The desktop app's
  `self-update` refusal now names "Update" in the desktop guide, and the refusal
  for a server installed with `install.sh` points to the "Server" section of the
  updating guide.

### Docs

- **New "Token reports" guide.** What is recorded, when it is sent and what is
  never sent, how to turn it off, attribution and recovering unattributed usage,
  report requests, building the PDF by hand, the ledger, and the settings. The
  README links to it.
- **The "Updating" guide covers the new-version notices,** the desktop app's
  one-click update and the server's automatic updates, including when an
  automatic update is skipped and what to do then.
- **README and guide introductions** now describe Looptrack as external memory
  for AI coding agents that makes loop engineering possible, and the README's
  description of the web UI matches the current screens.

### Moving from 1.0.0-rc.1 / rc.2

- The assets of `v1.0.0-rc.1` and `v1.0.0-rc.2` stay as they were.
- Servers installed with the `install.sh` of rc.1 or rc.2 upgrade with the new
  line: `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`.
  It reads the `/etc/looptrack/install.conf`, `.env` and systemd unit (or
  `compose.yaml`) they left as they are. An old `install.sh` pointed at the new
  release with `--from` stops without replacing anything, because the release
  no longer carries bare binaries.
- Scripts that download `…/releases/download/<version>/looptrack_<version>_<os>_<arch>`
  directly must download the `_server` archive and unpack it instead (the
  getting-started guide has the steps for macOS, Linux and Windows).
- This version adds migrations `0005` (a column in `usage_snapshots`) and `0006`
  (the table `issue_event_sessions`, append-only: the application user gets
  `SELECT` and `INSERT` only). With MySQL and the minimum grants,
  `install.sh --upgrade` asks for administrative credentials and grants the new
  table; on a server set up with `looptrack setup` alone, run `looptrack migrate`
  and then `looptrack grants apply` (or the statements from
  `looptrack grants print`) before starting it.
- Once this version has migrated a database, do not put rc.1 or rc.2 back on it:
  they predate the check for newer migrations and would run on data in the newer
  shape. Restore the backup taken before the update instead.
- After `looptrack self-update`, run `looptrack issue init` again in each project
  so that Claude Code projects get the new `issue-session-bind` hook
  (`--no-session-bind` leaves it out).

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

[Unreleased]: https://github.com/howashoji/looptrack/compare/v1.0.0-rc.3...HEAD
[1.0.0-rc.3]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3
[1.0.0-rc.2]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.2
[1.0.0]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0
