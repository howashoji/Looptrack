# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

このプロジェクトの目立った変更は、すべてこのファイルに記録しています。

形式は [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) に基づく。版の付け方は [Semantic Versioning](https://semver.org/spec/v2.0.0.html) に従います。

## [Unreleased]

## [1.0.0-rc.4] - 2026-09-30

Fourth release candidate for 1.0.0. The changes below are relative to
`v1.0.0-rc.3`.

### Fixed

- **MCP works behind a reverse proxy on a server that listens on 127.0.0.1.**
  The MCP library's protection against DNS rebinding refused (403) every
  request received on a loopback address whose `Host` was not a loopback name.
  A server installed with `install.sh` (systemd) listens on 127.0.0.1 behind
  nginx or Caddy, which pass the public host name, so none of its MCP requests
  got through. The check now runs in looptrack itself, before authentication:
  a request received on a loopback address is let through when its `Host`
  (without the port) is a loopback name (127.0.0.0/8, `::1`, `localhost`) or
  the host of `LOOPTRACK_PUBLIC_URL` (case-insensitive), and is refused with
  403 otherwise. `X-Forwarded-Host` is not trusted. Without a public URL (the
  desktop app), only loopback names are accepted, as before; requests received
  on other addresses (inside a container, say) are not checked, as before.
- **`looptrack migrate` works with the minimum MySQL grants when there is
  nothing to apply.** MySQL asks for the `CREATE` privilege for
  `CREATE TABLE IF NOT EXISTS` even when the table already exists, so an
  application user holding only the grants in `deploy/grants.sql` (`SELECT` on
  `schema_migrations`) failed with error 1142 on every migrate. Because of
  this, the automatic update of a MySQL server failed after stopping the
  service and put the previous version back every time, even for a version
  that does not change the database. A manual `install.sh --upgrade` without
  `LOOPTRACK_SETUP_MIGRATE_DSN` failed at the same migrate as well, and left the
  service stopped. migrate now creates `schema_migrations` only
  when it cannot see the table; with a migration still to apply, the missing
  privilege is still reported as an error.
- **The automatic server update always puts the previous version back once it
  has stopped the service.** The previous version was restored only when the
  installer stopped on one of its own error checks. A failing
  `systemctl daemon-reload` or `systemctl start`, any other command that failed
  on its way, or an interruption (`TERM`, say) left the service stopped. Now
  every one of these puts the previous binary (with SQLite, also the database
  copy taken right after stopping) back once, starts it again, leaves a
  one-line reason in `journalctl -u looptrack-upgrade` and exits with a non-zero
  code. A failure to start the new version is now reported with its own
  message. A manual `--upgrade` on a terminal still does not roll back.
- **`install.sh` gives the right reason and the dnf commands.** When
  `--auto-upgrade on` stops because neither `curl` nor `wget` is installed, it
  now says they are needed to fetch the new release's archive, `SHA256SUMS`
  and signature once a day (the timer does not fetch `install.sh` again). The
  hints for installing `minisign` and `curl` now give the dnf commands for
  AlmaLinux and similar systems (`minisign` comes from EPEL:
  `dnf install -y epel-release && dnf install -y minisign`) next to the apt
  ones in the installer's messages; the deployment and updating guides now give
  the dnf command for `minisign` too.

### Docs

- **Plainer wording across the documents.** The README, the user guides, the
  kit README, the AI guide, `CONTRIBUTING.md`, `SECURITY.md` and the other
  guides for adding projects and cloud agents were rewritten in plainer, more
  direct language (in both languages where a document has English and Japanese
  versions), with the facts, the strength of each requirement and the meaning
  of both languages kept as they were.
  `CHANGELOG.md` now carries each version in English followed by Japanese, and
  the server design document describes the MCP `Host` check.

### Moving from 1.0.0-rc.3

- This version adds no migrations.
- The automatic update keeps running the copy of `install.sh` saved on the
  server and does not fetch a new one. To get the fixed rollback, run the
  one-line `--upgrade` once by hand:
  `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`
  (it also refreshes the copy when the server is already on this version).
- If MCP clients were refused with 403 by a server behind a reverse proxy,
  check that `LOOPTRACK_PUBLIC_URL` holds the public URL the clients use; the
  host in it is the one that is accepted.

---

1.0.0 のリリース候補、4 つ目。下の変更は `v1.0.0-rc.3` からの差分です。

### 修正

- **127.0.0.1 で待ち受けるサーバで、リバースプロキシの後ろからも MCP が使える。** MCP のライブラリが持つ DNS rebinding の対策は、ループバックのアドレスで受けた要求のうち、`Host` がループバックの名前でないものをすべて拒否していました（403）。`install.sh` で入れたサーバ（systemd）は 127.0.0.1 で待ち受け、前段の nginx や Caddy が公開のホスト名を渡します。そのため MCP の要求が 1 つも通らなかった。いまは looptrack 自身が認証より前に判定します。ループバックのアドレスで受けた要求は、`Host`（ポートを除く）がループバックの名前（127.0.0.0/8、`::1`、`localhost`）か、`LOOPTRACK_PUBLIC_URL` のホスト（大文字小文字は問わない）なら通し、それ以外は 403 で拒否する。`X-Forwarded-Host` は信じません。公開の URL が無いとき（デスクトップ版）はこれまでどおりループバックの名前だけを通し、ほかのアドレスで受けた要求（コンテナの中など）もこれまでどおり判定しません。
- **MySQL の最小の権限でも、適用するものが無ければ `looptrack migrate` が通る。** MySQL は、表が既にあっても `CREATE TABLE IF NOT EXISTS` に `CREATE` の権限を求めます。そのため `deploy/grants.sql` の権限（`schema_migrations` には `SELECT`）だけを持つアプリケーションのユーザでは、migrate が毎回エラー 1142 で失敗していた。その結果、MySQL のサーバの自動更新は、データベースを変えない版でも、サービスを止めた後で失敗して前の版に戻ることを毎回繰り返していました。`LOOPTRACK_SETUP_MIGRATE_DSN` を渡さずに手で実行する `install.sh --upgrade` も同じ migrate で失敗し、こちらはサービスが止まったまま残った。いまの migrate は、`schema_migrations` が見えないときだけ作ります。適用する移行が残っていれば、権限の不足はこれまでどおりエラーとして知らせる。
- **サーバの自動更新は、サービスを止めた後なら必ず前の版に戻す。** これまで前の版に戻るのは、インストーラ自身の検査で止まったときだけでした。`systemctl daemon-reload` や `systemctl start` の失敗、途中のほかのコマンドの失敗、中断（`TERM` など）では、サービスが止まったまま残っていた。いまはどの場合も、前の実行ファイル（SQLite なら、止めた直後に取った DB の控えも）を 1 回だけ戻して起動し直し、理由を 1 行 `journalctl -u looptrack-upgrade` に残して、0 でない終了コードで終わります。新しい版が起動しないときも、それと分かるメッセージを出すようになりました。端末で手動で実行する `--upgrade` は、これまでどおり戻しません。
- **`install.sh` が正しい理由と dnf のコマンドを示す。** `curl` も `wget` も無いために `--auto-upgrade on` が止まるとき、理由を「1 日 1 回、新しい版の書庫と `SHA256SUMS` と署名を取るのに要る」と示すようになりました（タイマは `install.sh` を取り直さない）。インストーラの案内では、`minisign` と `curl` の入れ方に、apt に並べて AlmaLinux などの dnf のコマンドも示します（`minisign` は EPEL から: `dnf install -y epel-release && dnf install -y minisign`）。配置と更新のガイドにも、`minisign` の dnf のコマンドを足しました。

### 文書

- **文書の言い回しを平易に。** README、利用者ガイド、kit の README、AI 向けのガイド、`CONTRIBUTING.md`、`SECURITY.md`、それにプロジェクトの追加やクラウドのエージェントのガイドを、より平易で率直な言い回しに書き直しました（英語と日本語の両方がある文書は両方）。事実、各要件の強さ、日英の意味はそのままです。`CHANGELOG.md` は各版を英語の後に日本語で載せるようになり、サーバの設計の文書には MCP の `Host` の判定を書き足した。

### 1.0.0-rc.3 から上げるとき

- この版に移行（マイグレーション）はありません。
- 自動更新は、サーバに保存した `install.sh` の写しを動かし続け、新しいものを取ってきません。直った戻し方を使うには、1 行の `--upgrade` を 1 度だけ手で実行してください: `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`（サーバが既にこの版でも、写しは新しくなります）。
- リバースプロキシの後ろのサーバで MCP のクライアントが 403 で拒否されていたなら、`LOOPTRACK_PUBLIC_URL` にクライアントが使う公開の URL が入っていることを確かめてください。通すのはその URL のホストです。

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

---

1.0.0 のリリース候補、3 つ目。下の変更は `v1.0.0-rc.2` からの差分です。

### 追加

- **サーバのインストーラが 1 行に。** Linux のサーバで `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh` を実行すると、`main` にあるインストーラが動きます。`--from` を付けなければ、最新のリリースの `looptrack_<version>_linux_<arch>_server.tar.gz` を GitHub Releases から取ってくる（`--version <version>` で版を選べる）。それを `SHA256SUMS` で照合し、`minisign` が入っていれば `SHA256SUMS` の署名も確かめ（`--require-signature` を付けると署名が必須になる）、そこまで通って初めて展開して `looptrack` を入れます。スクリプトそのものは HTTPS で取ってくるだけで、自分自身を検証できません。気になるなら、先にダウンロードして中身を読んでください。`--from` はこれまでどおり、手元の `deploy/release/dist.sh build` の出力か、サーバの配布ディレクトリ（素のバイナリ）を受け付けます。インストーラは Linux 専用。ほかの環境では何も変えずに止まり、デスクトップ版を案内します。
- **`looptrack grants print|apply`。** MySQL の最小の権限（`deploy/grants.sql`）を `looptrack` に組み込みました。`grants print` は、`LOOPTRACK_DSN` にあるデータベースとユーザに合わせて権限を出力する（`im` と `im_app` に限らない）。`grants apply` は管理者の資格情報を端末で尋ね（表示も保存もしない）、無いデータベースやアプリケーションのユーザを、尋ねてから作ります。テーブルが無ければテーブルも作り、権限を付けて、アプリケーションのユーザが読めることを確かめる。
- **デスクトップ版とサーバに、新しい版のお知らせ。** どちらも起動したときと 24 時間ごとに、GitHub のリリースの一覧を確かめます。知らせるのは、`SHA256SUMS` の署名が正しく、しかもそのリリースに自分の OS と CPU 向けのファイルがある版に限る。どの種類の版を追うかは、動かしている版に合わせます（rc なら rc も知らせ、安定版なら安定版しか知らせない）。確認に失敗したとき（オフラインなど）は、前のお知らせを残す。デスクトップ版では、トレイのメニューの先頭と Web UI の帯に出ます。トレイの「新しい版を確認する」を外すと、確認しなくなる。サーバでは、管理者向けの帯（1 行の更新コマンドつき）、管理者のトークンで実行した `looptrack doctor`、それにログに出ます。`LOOPTRACK_UPDATE_CHECK=off` で確認をやめ（GitHub に接続しない）、`LOOPTRACK_UPDATE_CHANNEL`（`stable` か `prerelease`）で追うものを選び、`LOOPTRACK_UPDATE_URL` で確認先を同じ形の別の `https://` のエンドポイントに向けられます。CLI は GitHub を確かめない。自分でビルドしたもの（`dev`）も確認しません。
- **デスクトップ版をワンクリックで更新（macOS と Linux）。** トレイのメニューの先頭にある「新しい版 <version> に更新する」か、Web UI の帯の「更新する」を押すと、新しい版をダウンロードし、署名つきの `SHA256SUMS` で照合します（macOS では `spctl` と `codesign` でも確かめ、識別子と署名のチームが同じであることを求める）。そのうえで前の版を `.prev` の名前で残してアプリを入れ替え、起動し直す。新しい版が起動しなければ、前の版を戻します。トレイの「新しい版を自動で入れる」にチェックを入れると、新しい版が見つかったときに同じことを自分でやります（既定では無効）。Windows や、アプリの置き場所に書き込めない環境では、リリースのページから新しい版をダウンロードしてください（macOS では検証済みの dmg を開いてくれる）。
- **サーバの自動更新（選んで有効にする）。** `install.sh --auto-upgrade on|off`（`LOOPTRACK_INSTALL_AUTO_UPGRADE`。既定は無効。systemd に限る。`minisign` が要る）は、毎日動く `looptrack-upgrade.timer` を入れます。このタイマは、サーバに残した `install.sh` の写しを `--upgrade --require-signature --yes --only-newer` 付きで実行する。古い版へ移ることはありません。サービスを止めた後で失敗したら、前の版を戻して起動し直します。MySQL で、実行すべき移行を持つ新しい版は、無人では入れない（端末で `--upgrade` を実行する）。compose のサーバと `--no-start` のサーバでは拒否されます。各回で何をしたかは `journalctl -u looptrack-upgrade` で見られる。`--only-newer` は手で渡すこともできます。新しい `looptrack migrate --check` は、未適用の移行を適用せずに並べ、1 つでもあれば終了コード 3 で終わります。
- **デスクトップ版: DB を移行する前に控えを取る。** 新しい版がローカルの SQLite のデータベースにある既存のデータを移行する前に、デスクトップ版（と `LOOPTRACK_LOCAL_MODE` のサーバ）は `looptrack.db` をデータフォルダの `backups` フォルダへ写します（`looptrack.db.<UTC time>`）。控えは新しいほうから 2 つを残す。写せなかったときは、移行も起動もしません。
- **デスクトップ版: Linux のアプリの一覧に載る。** AppImage は起動したときに `~/.local/share/applications/looptrack.desktop` とそのアイコンを置きます。だからランチャーやアクティビティの検索から起動できる。AppImage を別の場所へ動かすと、項目も追いかけます。トレイの「アプリ一覧に登録する」を外すと、項目を消し、その後も置きません。
- **`-` で標準入力から本文を読む。** `looptrack issue comment <ID> -`、`issue new --body -`、それに `issue status`・`issue close`・`issue next` の `--comment -` が、標準入力から本文を読むようになりました。`looptrack handoff append` が前からしていたのと同じです。空の入力は、何も送らずに拒否する。これまでは `-` という文字そのものが本文として保存されていました。
- **雛形の受け入れ条件のまま着手したときの注記。** `acceptance.require_on_close` のあるプロジェクトで、「## 受け入れ条件」が雛形のままのイシューを In Progress にすると（`next` による着手も含む）、このままでは close が拒否される、という注記を返すようになりました。着手そのものは止めない。注記は CLI、REST の応答（`acceptance_notice`）、MCP の `set_status` と `next`、Web UI に出ます。
- **自分の MCP の着手と、ほかの会話の着手を見分ける（Claude Code）。** 新しい core の hook `looptrack hook issue-session-bind`（`mcp__.*` の PreToolUse）が、looptrack の MCP のツールが動く直前に、ツール呼び出しの ID と会話のセッション ID をサーバに渡します。そのため、同じ MCP の接続を通っていても、MCP の `next` と一覧は同じ利用者の別の会話による着手を自分の着手と区別できる。1.5 秒で打ち切り、どんな失敗でも呼び出しは通します。`looptrack issue init --no-session-bind` なら、この hook を入れません。加えて、トークンの集計の hook が MCP の呼び出しのスナップショットを送ると、サーバはその着手を会話に結び付けます（移行 `0006_issue_event_sessions`）。だから CLI でも 2 つを見分けられる。ほかのエージェントには影響しません。
- **`LOOPTRACK_LOOP_HOOK_LOG=1` で hook の判定を記録する。** どの判定も（core の hook を含む）、JSONL の 1 行として `<state dir>/.looptrack-freshness/hook-log.jsonl` に追記します。残すのは時刻・hook・イベント・決定・セッションと、理由の種類を表す語。コマンド、プロンプト、理由の文面、パスは決して残しません。1 MiB でローテーションする。既定は無効。

### 変更

- **インストーラが MySQL の権限を同じ流れで付ける。** MySQL を最小の権限で使うとき、これまでの `install.sh` は `looptrack setup` の後で止まり、`grants.sql` を自分で実行してからもう一度実行するよう求めていました。いまはその時点で管理者の資格情報を端末で尋ね、`looptrack grants apply` を実行し、アプリケーションのユーザが読めることを確かめて、そのままサーバの起動と確認に進む。資格情報が違っていれば、何も起動せずに止まります。もう一度実行すると、その問いから続けます。新しい版がテーブルを足すときは、`--upgrade` も同じことをする。端末なしで動かすとき（systemd に限る）は、`LOOPTRACK_INSTALL_DB_ADMIN_USER` と `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE` を渡してください。
- **`looptrack setup`（だからインストーラも）が、無い MySQL のデータベースを作る。** 接続文字列のデータベースがまだ無いと、setup は移行の前、接続の段階で止まっていました。そのためインストーラは、データベースを作るところまでたどり着けなかった。いまの setup はデータベースが無いことに気づくと、管理者の資格情報を端末で尋ね（1 度だけ。表示も保存もしない）、データベースを作るかを尋ねます（`--yes` を付けていても尋ねる）。そして `looptrack grants apply` と同じやり方で、データベースとアプリケーションのユーザ、テーブル、権限を作ってから先へ進みます。既にあるデータベースはそのまま。no と答えるか、端末なしで実行すると、何も作らずに止まり、自分で実行する `CREATE DATABASE` の文を示します。
- **GitHub Releases のサーバのバイナリを書庫で配る。** 6 つの対象はどれも、`looptrack_<version>_<os>_<arch>_server.tar.gz`（Linux、macOS）か `looptrack_<version>_windows_<arch>_server.zip`（Windows）になりました。書庫を展開すると 1 つのフォルダ `looptrack_<version>_<os>_<arch>_server/` ができ、中身は `looptrack`（`looptrack.exe`）、`NOTICE`、`OFL-BIZUDGothic.txt`、`LICENSE`。素のバイナリ `looptrack_<version>_<os>_<arch>[.exe]` と、単独の `install.sh` と `grants.sql` は、もうリリースの添付物ではありません。書庫にも入っていない。デスクトップ版の添付物は名前を変えず、`NOTICE` と `OFL-BIZUDGothic.txt` も引き続き単独で添付します。`SHA256SUMS` には書庫に加えて、各書庫の中のバイナリも前の名前（`looptrack_<version>_<os>_<arch>[.exe]`）で載せています。だから書庫から取り出したバイナリも、署名つきの一覧で照合できる。ただし、これらの行は添付されていないファイルを指すので、1 行だけを `grep … | sha256sum -c -` で照合するか、`sha256sum -c --ignore-missing SHA256SUMS` を使ってください。
- **サーバの配布ディレクトリと `self-update` は変わらない。** `LOOPTRACK_DIST_DIR` に置くのは、これまでどおり `looptrack_<version>_<os>_<arch>[.exe]` という名前の素のバイナリです。そこに書庫を置いても、一覧にも配布にも載らない。公式のリリースを配るなら、各書庫から `looptrack` を取り出してその名前で置き、リリースの `SHA256SUMS` と `SHA256SUMS.minisig` をそのまま隣に置いてください（更新のガイドの「利用者に配る looptrack（配布ディレクトリ）」）。
- **1.0.0 が出るまでは、リリース候補を Latest にする。** 安定版の `vX.Y.Z` のタグができるまで、リリース候補は GitHub Releases で Latest のリリースとして公開し（プレリリースにはしない）、GHCR では `latest` のタグを付けます。だから 1 行のインストーラ（`releases/latest` を読む）がそれを入れる。1.0.0 の後に出すリリース候補は、プレリリースに戻ります。
- **検証の記録は、記録した利用者の言語で書く。** `verify`（と MCP の `report_verify`）が残すコメントは、記録した人の言語で書かれます。言語の選び方は、その利用者に見せるほかのものと同じ。既に保存された記録は変わりません。英語では、MCP 経由で報告された結果の印が、どこでも「self-reported via MCP」になりました。
- **プロジェクトを作るとき、省いた値をどの経路でも同じように埋める。** `looptrack project create` は `--prefix` と `--name` を必須にしなくなりました（省くと、管理ページや MCP の `create_project` と同じく、slug の大文字と slug になる）。`looptrack setup` が作る最初のプロジェクトも、ほかと同じ既定の並び順（100）になった。明示した `--order 0` は 0 のままです。
- **`pre-tool-subagent-bound` がサブエージェントに識別子の照合も求める（loop kit）。** サブエージェントの指示文に足す注意に、中の識別子（変数名や関数名、コミットの SHA、行番号、ファイルのパス）はどれも使う前に `git show` / `git grep` で実物と照合し、食い違いは 1 行で報告すること、が加わりました。自動では何も足されないエージェントのために、`background-process.md` にも同じ一文を載せています。
- **loop kit の rules に作業規律が増えた。** `working-discipline.md` に次の規律が加わりました。共有の部品の両側を数えること、方法を変えて確かめ直すこと、集計の緑が何を示し何を示さないか、「X は起きない」テストの対照、ガードが取りこぼす形を探ること、変更が記録する golden を回すこと、rebase の直後にビルドと型の検査を回すこと、rules と状態をサブエージェントへ降ろすこと（指示文に写す定型つき）。

### 修正

- **`self-update` が、一覧に載った版を署名と突き合わせる。** これまでは（署名の無い）配布の一覧にある版だけを信じていました。そのため一覧が、古いリリースの正しく署名された `SHA256SUMS` と高い版番号を組み合わせれば、CLI を古い版へ戻せてしまった。いまは署名つきの `SHA256SUMS` にあるファイル名（リリースなら、署名の trusted comment にある版も）が、一覧の版・OS・CPU と一致することを求めます。一致しなければ何も入れ替えない。古い版へ戻すには、これまでどおり `--force` が要ります。
- **新しい looptrack が移行したデータベースでは、古い looptrack は動かない。** `migrate`、`setup`、デスクトップ版、ローカルモード、チームのサーバの `serve` は、自分の知らない移行が適用されたデータベースを拒否するようになりました。これまでは新しい形のデータをそのまま使っていた。戻すには、新しい版が移行する前に取った控えからデータベースを戻してください。`install.sh --upgrade` の移行が何かを適用した後で失敗したときも、インストーラは控えからデータベースを戻すよう案内するようになりました。
- **MCP の `setup` が、既に入っている `looptrack` を入れ替えなくなった。** `~/.local/bin/looptrack`（Windows では `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`）があれば、返すコマンドはそれをダウンロードも入れ替えもせず、それで `init` を実行するだけです。古い版を配るサーバが、手元の新しい版を古い版に戻すことはもうできない（手元のほうが古いときは、次の `setup` が `self-update` を勧める）。
- **MCP の `setup` が、Codex でも環境変数を前に付ける。** Codex は、`init` が `.codex/config.toml` に書いた変数を再起動の後でしか読みません。だから Copilot と同じく、返すコマンド（トークンの確認を含む）に `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` を付けるようにした。
- **イベントを記録する操作は、どれもトークンの集計に入る。** MCP の `next`（イシューに着手したとき）、`report_verify`、`assign_issue`、それに担当が変わったときの CLI の `issue assign` にもトークンを付けるようにしました。これらが帰属なしとして出ることは、もうありません。送れずに後で送り直したスナップショットは、付けられる期間を過ぎていても元の操作に付く（移行 `0005_usage_snapshots_attempted_at`。新しい欄を知らないサーバには、その欄を除いたスナップショットをもう一度送る）。未送信のスナップショットはサーバとプロジェクトごとに保ち、取ったプロジェクトにだけ送り直します（その情報を持たない古いものは送り直さず、7 日で捨てる）。サインアウト中に取ったスナップショットは保っておき、`looptrack issue login` の後に送る。送信の失敗も黙らなくなりました。セッションの開始の要約が、最後の失敗と、送信を待つスナップショットの数を知らせます。
- **エクスポートとインポートが、アーカイブしたプロジェクトを尊重する。** `looptrack export` は、`--archived` を付けない限り、アーカイブしたプロジェクトを含めません。`looptrack import` は、アーカイブしたプロジェクトへの取り込みを拒否する（置き換えると、そこに残したデータが消えるため。先にアーカイブを解いてください）。
- **hook が、包まれたコマンドの中まで見る。** イシューの鮮度ガードと引き継ぎの完了の記録は、`git commit` / `push` / `merge`、`looptrack issue …` の呼び出しと完了（`close`、`status Done`）を、次の形でも見分けるようになりました。`bash -c` / `sh -c` / `eval` で包んだもの、`sudo`・`env`・`nohup`・`xargs` のような前置の語の後ろにあるもの、`function` の中にあるもの、行の継続で分けたもの（入れ子のシェルと前置の語は、git ガードや秘密のガードと同じやり方でほどく）。別の作業ツリーで引き継ぎのファイルだけをコミットした場合も、正しく見分けます。
- **暴走プロセスのガード: 引用符の無い `ps` の行を待ちループとして判定する。** `ps` はコマンド行から引用符を落とします（`/bin/bash -c while true; do sleep 1; done`）。そのせいで、上限の無い待ちループ向けの短い閾値をすり抜けていた。
- **タスクモードの hook: サブエージェントの報告でモードが切り替わらなくなった。** Claude Code は、サブエージェントの最終報告を `<agent-message …>` で包んで UserPromptSubmit に通します。その中の語で調査モードと実行モードが切り替わることはもう無い。その中のイシュー ID も、鮮度ガードにとって利用者が触れたものには数えません。
- **Go（RE2）が読めない正規表現を知らせる。** `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE`、`_INVEST_RE`、`LOOPTRACK_LOOP_RUNAWAY_ALLOW`、`LOOPTRACK_MCP_SERVER` は、先読みなどを使って書くと黙って無視されていました。いまは hook が変数名とエラーをメッセージとして示し、その値を使わずに続けます（既定の語と除外は引き続き効く。`LOOPTRACK_MCP_SERVER` は何にも一致せず、知らせるのは MCP のツールの呼び出しのときに限る）。ガイドにも、これらが RE2 だと書いた。
- **`looptrack issue init` が、余計な `.gitignore` の行を足さなくなった。** 作業ツリーの中の `.gitignore` にある否定でない規則で `.claude/.looptrack-freshness/` が既に無視されていれば、init は最上位の `.gitignore` に手を付けません（`.git/info/exclude` やグローバルの除外ファイルにしか無い規則は数えない。ほかのクローンとは共有されないから）。
- **英語のメッセージが、実在する見出しを指す。** デスクトップ版の `self-update` の拒否は、デスクトップのガイドの「Update」を挙げるようになりました。`install.sh` で入れたサーバへの拒否は、更新のガイドの「Server」の節を指します。

### 文書

- **新しいガイド「トークンレポート」。** 何を記録するか、いつ送り、何を決して送らないか。止め方、帰属と、帰属なしの使用量の取り戻し方、レポートの依頼、PDF を手で作る方法、台帳、設定も説明しています。README からリンクした。
- **「更新」のガイドで、新しい版のお知らせを説明。** デスクトップ版のワンクリックの更新と、サーバの自動更新も扱います。自動更新が見送られるのはどんなときか、そのときどうするかまで書いた。
- **README とガイドの導入** で、Looptrack を、ループエンジニアリングを可能にする AI のコーディングエージェントの外部記憶として説明するようにしました。README の Web UI の説明も、いまの画面に合わせています。

### 1.0.0-rc.1 / rc.2 から上げるとき

- `v1.0.0-rc.1` と `v1.0.0-rc.2` の添付物は、そのまま残します。
- rc.1 や rc.2 の `install.sh` で入れたサーバは、新しい 1 行で上げます: `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`。この 1 行は、それらが残した `/etc/looptrack/install.conf`、`.env`、systemd のユニット（または `compose.yaml`）をそのまま読む。古い `install.sh` を `--from` で新しいリリースに向けても、何も入れ替えずに止まります。リリースに素のバイナリがもう無いからです。
- `…/releases/download/<version>/looptrack_<version>_<os>_<arch>` を直接ダウンロードするスクリプトは、代わりに `_server` の書庫をダウンロードして展開しなければなりません（macOS・Linux・Windows の手順は「始め方」のガイドにある）。
- この版で、移行 `0005`（`usage_snapshots` の列）と `0006`（テーブル `issue_event_sessions`。追記のみで、アプリケーションのユーザが持つのは `SELECT` と `INSERT` だけ）が入ります。MySQL を最小の権限で使っているなら、`install.sh --upgrade` が管理者の資格情報を尋ね、新しいテーブルに権限を付ける。`looptrack setup` だけで用意したサーバでは、起動する前に `looptrack migrate` を実行し、それから `looptrack grants apply`（または `looptrack grants print` が出す文）を実行してください。
- この版がデータベースを移行したら、そこに rc.1 や rc.2 を戻さないでください。どちらも新しい移行を確かめる仕組みより前の版なので、新しい形のデータの上で動いてしまう。代わりに、更新の前に取った控えを戻します。
- `looptrack self-update` の後は、各プロジェクトで `looptrack issue init` をもう一度実行してください。Claude Code のプロジェクトに、新しい `issue-session-bind` の hook が入ります（`--no-session-bind` なら入れない）。

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

---

1.0.0 のリリース候補、2 つ目。下の変更は `v1.0.0-rc.1` からの差分です。

### 追加

- **MCP の `create_project` ツール（管理者に限る）。** プロジェクトを作り、呼び出した人をその管理者に加えるまでを、1 つのトランザクションで行います。通る道は管理ページ（`POST /admin/projects`）と同じで、値は `looptrack project create` の規則に従う。`setup` がプロジェクトを見つけられないときのエラーも変わりました。管理者には `create_project` で作れることを伝え（プレフィックスと桁数は後から変えられないので、先に slug・プレフィックス・桁数を利用者に確かめる）、それ以外の人には管理者に頼むよう伝えます。
- **プロジェクトの名前の変更・アーカイブ・復元。** `/admin/projects` の各プロジェクトに「表示名とアーカイブ」が加わりました。表示名を変えるか（slug・プレフィックス・発番済みの ID はそのまま）、slug を打ち込んで確かめたうえでプロジェクトをアーカイブできる。アーカイブは論理削除です。プロジェクトはどの一覧（ハブ、REST、MCP、CLI）からも消え、新しいイシュー・更新・コメントはどの経路でも拒否されます。ただし、イシュー・コメント・履歴は残る。ページの下の「アーカイブしたプロジェクト」から復元できます。サーバでは、`looptrack project rename <slug> <display name>` で表示名を変え、`looptrack project archive|unarchive <slug>` でページと同じことをします。`looptrack project list` は、`--archived` を付けない限りアーカイブしたプロジェクトを出さない。移行 `0004_projects_archived` が `projects.archived_at` を足します。データベースの権限は変わりません。
- **デスクトップ版のトレイのメニュー: どの OS でも左クリックでメニュー、「設定」の項目、版の表示。** トレイのアイコンは、Windows でも左クリックでメニューを出すようになりました（これまで Windows では左クリックでアプリが開き、メニューには右クリックが要った）。新しい **設定** の項目は、アカウントの設定のページ（`/account`）をブラウザで開く。いつも見えている「バージョン {version}」の項目は、動いているビルドの版の文字列を省かずに示します（`-rc.N` があればそれも）。だから、アイコンにカーソルを合わせたり端末を開いたりしなくても、リリース候補のビルドと正式版を見分けられます。
- **`pre-tool-subagent-model` hook（loop kit）。** `model` を指定せずに Claude Code のサブエージェント（`Task`/`Agent`）を起動すると `deny` で止め、難しさに応じた選び方（haiku / sonnet / opus）を示します。止めないのは、`subagent_type: fork`、定義ファイルが既に `model:` を決めている型、`LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW` に挙げた名前。

### 変更

- **日本語だけだった残りのメッセージにも、英語が付いた。** ほかの Looptrack と同じく、接続（MCP）か端末（CLI、`LOOPTRACK_LANG`）の言語に従います。対象は次のとおり。MCP のプロンプト（`loop`、`review`、`setup`）とそのエラー。担当・メンバー・使用量の送信の設定についてのメッセージと、`report_verify` の案内。資格情報・鍵・パスワード・ローカルのサーバについてのエラーと警告（サーバのログの警告は、サーバを起動した人の言語）。インポートと、Markdown・JSON の読み込みのエラー。`looptrack setup` が書く `compose.yaml` と `Dockerfile` のコメントと、`looptrack issue init` が `AGENTS.md` に書く loop の節。それに hook の入出力のエラー、`verify` のコマンドの実行部、デスクトップ版の警告とお知らせ、Copilot の使用量のヒント。入力として照合される語（`## コメント` の見出しや、答えの `はい` など）は変えていません。

### 修正

- **nginx の後ろの MCP: 応答をバッファしなくなった。** サーバは `/mcp` のすべての応答に `X-Accel-Buffering: no` を付けるようになりました。だから nginx が既定の `proxy_buffering on` のままでも、`subscriptions/listen` の SSE のストリームがクライアントに届く（Copilot CLI のようなクライアントは 10 秒で諦めていた）。DEPLOY.md でも、手で書く nginx の設定に `proxy_buffering off;` を求めるようにしました。
- **git ガードの hook: 引用符の無い Windows の絶対パスの `git.exe` を認識する。** `pre-tool-git-guard` は、コマンドを POSIX のエスケープと Windows の規則（バックスラッシュはエスケープではない）の両方で読むようになりました。そのため `C:\tools\git.exe add -A` は、すり抜けずに `git add -A` と同じように判定されます。Git Bash での動きは前とまったく同じ。空白を含むパスは、これまでどおり引用符で囲まないと認識されません。
- **秘密のガードの hook: 名前が見えているグロブを確認する。** `pre-tool-secrets-guard` は、`.env*` や `id_rsa*` を読む・写すコマンドの前に尋ねるようになりました（末尾のグロブを落とした残りが、秘密を名乗っている）。`ls` のような一覧だけのコマンドには影響しない。名前を伏せるグロブ（`*`、`.e*`）は、これまでどおり捕まえません。
- **秘密のガードの hook: 行末のコメントのアポストロフィで、雛形の例外が壊れなくなった。** `cp .env.example .env  # don't …` は、閉じていない引用符と取り違えられて確認になっていました。いまは引用符の外の `#` のコメントを飛ばします。

### 文書

- **プロンプトを 1 つ貼るだけで入れる（リモートのサーバ）。** 「AI ごとの手引き」のガイドに、Claude Code、Codex、GitHub Copilot（VS Code）、Copilot CLI 向けの貼るだけのプロンプトが加わりました。用意するもの、まだ手を動かす必要がある所（auto モードでも Claude Code ではインストールのコマンド、認可の後の Copilot CLI の 2 回目の再起動、再起動の前の Codex でのトークンの確認）、うまくいかないときに確かめることも書いています。プロンプトは `setup` のツールに `project` を渡す（Codex は 1 つの MCP の設定をプロジェクトの間で共有するため）。トークンはエージェントに決して渡しません（サインインには `looptrack issue login --browser` を使う）。
- **新しいガイド「更新」。** 更新するとき何が自動で何が手作業かを、CLI（`self-update` と、更新された配布スクリプトについてのお知らせ）、デスクトップ版、サーバ（`install.sh --upgrade`、または `looptrack setup` だけで用意したサーバ）、利用者に配る looptrack のそれぞれについて説明します。
- **デスクトップ版のガイド: 「CLI なしで利用を始める」。** 初回の設定のフォームに何を入れるか。それと、トレイの接続の設定から、チャットだけで AI エージェントをつなぐ方法。

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

---

> 公開日はまだ決まっていない。リリースを公開したら、日付を書き入れてください。

最初の公開のリリース。下に挙げるものは、どれも Looptrack が 1.0.0 で出すものです。

### 追加

- **プロジェクト横断のイシュー管理。** 1 つのサーバが、多くのプロジェクトのイシューを持ちます。プロジェクトごとに、ID のプレフィックスとゼロ埋めの桁数（`MYP-0001`）、ルール、ガイドの文書を持つ。正はデータベースで、管理対象のソースのリポジトリには何もコミットしません。
- **ループの形をした進め方。** `next` は取り組むイシューを選び、着手の規則を守らせます。`verify` はイシューに書いたコマンドを実行して、結果を記録する。`summary` は毎セッションの初めに、入れ子になった 3 つのループ（いまの回、人の判断を待つ決定、外から届くフィードバック）を報告します。コメントとイベントは追記のみで、閉じたイシューは変更できません。
- **サーバが強制する、プロジェクト別のルール。** 必須のコメント、禁止した状態の遷移、`verify.require_on_close` は 1 か所で確かめます。だから、どの経路（REST、MCP、CLI、Web）にも効く。
- **REST API**。`/looptrack/api/v1` にあり、利用者ごとのアクセストークン（期限、失効、最後に使った時刻）を使います。
- **リモートの MCP サーバ**。`/looptrack/mcp` にあり、OAuth 2.1 かトークンでつなげる。MCP の接続だけでプロジェクトを配線できる `setup` ツールと、`guide` / `review` / `loop` のプロンプトもあります。
- **Web UI**: プロジェクトの選択、ボード / 一覧 / トレースの表示とイシューの詳細（読み取りのみ）、アカウントの設定（トークン、パスワード）、利用者の管理。サインインは ID とパスワード（argon2id）に TOTP を組み合わせ、TOTP を必須にするか任意にするかはサーバごとに決めます。
- **CLI（`looptrack issue`）**: 作成、編集、コメント、状態、close、一覧、表示、next、verify、summary、guide、export、それにプロジェクトを 1 つのコマンドで配線する `init`。`looptrack issue login --browser` はブラウザでサインインし、以後はトークンを更新し続けます。
- **hook と配布の kit。** `core` の層（セッションの要約、イシューの鮮度ガード、トークンの集計）は必ず入ります。任意の `loop` の層は、出力・作業・イテレーションの規律の rules、タスクモードと範囲のガード、引き継ぎの鮮度、暴走プロセスのガードを足す。配線はマニフェストで記述し、Claude Code・Codex・GitHub Copilot 向けに生成します。
- **トークンの集計。** 使用量をセッション・エージェント・ループの段階ごとに記録し、イシューに帰属させ、フォントを埋め込んだ PDF のレポート（`looptrack report pdf`）にします。
- **デスクトップ版。** macOS・Windows・Linux 向けの、ダブルクリックで起動するアプリケーション。サーバを手元で起動し、トレイ / メニューバーに常駐し、CLI を `PATH` に置くかを尋ねます。管理者の権限なしで、ログイン時に起動することもできる。
- **セットアップとインストール。** `looptrack setup` がいくつかの質問をして、`.env`（チームのサーバなら `compose.yaml` も）と最初の管理者を書き出します。`deploy/install.sh`（POSIX sh）は、まっさらな Linux のサーバで取得 → setup → 起動 → ヘルスチェックまでを行い、nginx と Caddy の例を示す。`--upgrade` と `--uninstall` も扱います。Dockerfile と Compose のファイルも用意しています。
- **ローカルモード。** 127.0.0.1 に結び付けた、SQLite を使うサインインなしの 1 人用のサーバ。共有のサーバを動かしたくない人向けです。
- **署名したリリースの成果物。** `SHA256SUMS` は minisign で署名し、macOS のバイナリは Developer ID で署名して公証を受けています。`looptrack self-update` は、バイナリを入れ替える前に署名を確かめる。どの成果物にも、第三者のコンポーネントのライセンスの全文を収めた `NOTICE` が付きます。
- **Markdown へのエクスポート。** `looptrack export` がイシューを Markdown に書き戻します。だからデータが閉じ込められることはない。
- **作業ツリーの片付け。** `looptrack worktree list` は、すべての git の作業ツリーを、それについて決めるのに要ることと一緒に並べます。マージ済みかどうか、未コミットの変更（ビルドの残りは無視する）、ロック、そして**最後に動いた時刻**。片付けは `looptrack worktree prune` で。`--yes` を渡さない限り何も消さず、本体の作業ツリーには決して触れず、最近動いたものは残します。ほかのセッションがまだ中にいるかもしれないからです。

[Unreleased]: https://github.com/howashoji/looptrack/compare/v1.0.0-rc.4...HEAD
[1.0.0-rc.4]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.4
[1.0.0-rc.3]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3
[1.0.0-rc.2]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.2
[1.0.0]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0
