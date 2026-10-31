# Changelog: Looptrack server

This file records the changes that matter to people who run a Looptrack server,
administer one, or connect to one with the CLI and AI agents. Changes to the
desktop app are in [CHANGELOG-desktop.md](CHANGELOG-desktop.md). Both editions
share one version number and one tag, and a change that affects both is written
in both files.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

このファイルは Looptrack のサーバ版の変更履歴です。サーバを動かす人・管理する人と、CLI や AI エージェントでサーバにつなぐ人に関係する変更をここに記録しています。デスクトップ版の変更の置き場は [CHANGELOG-desktop.md](CHANGELOG-desktop.md) です。2 つの版は版番号とタグを共有するので、両方に効く変更は両方のファイルに書きます。

形式は [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) に基づき、版の付け方は [Semantic Versioning](https://semver.org/spec/v2.0.0.html) に従います。

## [Unreleased]

Nothing yet.

---

まだありません。

## [1.3.0-rc.1] - 2026-10-31

First release candidate for 1.3.0. It is published as a pre-release, so
`v1.2.0` stays the Latest release on GitHub Releases and keeps the `latest`
tag on GHCR. The changes below are relative to `v1.2.0`.

This version is about several sessions working on one project from different
machines or under different users. They can share memory and handoffs through
the server, send each other messages, hold work that runs only once at a time,
and see who else is running. Six migrations come with it, so read "Moving from
1.2.0" before you upgrade.

### Added

- Project memory (handoffs, decisions and caveats) now lives on the server,
  and concurrent writes lose nothing. `looptrack memory append` adds a section
  whatever the version, so two appends both survive.
  `looptrack memory replace --version N` rewrites the whole text only while N
  is the current version, and otherwise answers 409 `memory_version`. A
  `project` memory is read by every member and written by editors and admins.
  A `personal` memory belongs to its owner, and anyone else gets 404, an
  administrator included. Every write keeps a version with the user, the
  machine label and the session. Text that looks like a credential is refused
  without echoing the value. The REST routes are under
  `/api/v1/projects/{slug}/memory`: an append takes an `Idempotency-Key`
  header, a write can carry `written_at`, and the list reports `can_write`.
  The MCP tools are `list_memories`, `get_memory` and `write_memory`, and the
  web view is "Memory" above the board.
- The loop kit can keep the handoff in that memory. With
  `LOOPTRACK_LOOP_HANDOFF_BACKEND=server`, SessionStart injects the handoff and
  a list of summaries of the decisions and caveats, so a session on another
  machine or under another user starts from them.
  `LOOPTRACK_LOOP_MEMORIES_MAX_CHARS` caps the size, trimmed as in the file
  backend. `looptrack handoff append` adds a section, and
  `looptrack handoff compact --version N` writes nothing if the version you
  read is stale. The Stop guard looks for a version this session wrote after
  the work finished. When the server cannot be reached it only shows a notice,
  and `handoff append` and `compact` set the body aside locally. The next
  SessionStart, Stop or write sends it, and a resend never adds a section
  twice. `looptrack handoff spool` lists what is waiting.
- `looptrack memory import` brings `handoff.md`, `decision-*.md` and
  `caveat-*.md` from `.claude/memories/` into the server. A `> 要約:` line
  becomes the summary. An existing name is never overwritten. The result lists
  what was skipped and what was refused, and for a credential it gives only
  the line and the rule.
- The content of memory versions can now be purged for good, and its growth
  has limits. `looptrack memory purge` is for a project admin, a server
  administrator, or the owner of a personal memory. `looptrack purge-memory` is
  for the operator, reaches other users' personal memory, and only reports the
  count and size until you pass `--yes`. Both take `--version N` or
  `--all-versions`, plus a reason. The version row stays, the content answers
  404 `memory_version_purged`, and each purge is recorded in the append-only
  `memory_purges`. Appended text carries into later versions, so if the text of
  version N is still in a later one, the purge answers 409
  `memory_purge_text_remains` and deletes nothing; use `--all-versions` to
  remove a secret. Purging the current version moves the version on, so a
  replace based on an earlier read is refused. A memory keeps the content of at
  most 100 versions, the oldest going first.
  `looptrack settings memory-retention` sets a retention of up to 36500 days.
  The default, 0, means no limit, and the current version is never deleted.
  SQLite now runs with `secure_delete`, so deleted text is not left in the free
  space of the file. Copies in backups have to be removed from the backups.
- Sessions, machines and users can send each other short messages through the
  server. `looptrack message send` and the MCP tool `send_message` address the
  whole project (`--all`), one user (`--to`) or one session (`--to` with
  `--session`), up to 4 KiB. A message to the project reaches every member, and
  any other message only its recipient and its sender. With `--issue`, a
  comment stays on that issue as well. The loop hooks `session-start-messages`
  and `user-prompt-messages` put unread messages into the next turn, at most 10
  at a time and 500 characters each, and pass silently when the server cannot
  be reached. `LOOPTRACK_LOOP_MESSAGES=0` turns them off. Read with
  `looptrack message inbox`, MCP `read_inbox` or "Messages" above the board.
  Editors and admins can send, viewers cannot, and text that looks like a
  credential is refused. A message stops being shown after
  `message_retention_days` (30 by default; `looptrack settings message-retention`
  takes 0 for no limit, or up to 36500), but its row stays.
- Leases let a project hold work that can run only once at a time, such as a
  production deployment, a public pull request or a release tag.
  `looptrack lease acquire deploy.dev --purpose "..." --ttl 30m` takes a lease
  for 1 hour by default and 24 hours at most. If it is already held, the
  command exits with code 1 and names the holder, the purpose and the expiry;
  the REST answer is 409 `lease_held`. `acquire` prints a `lease_token`, and
  `extend` and `release` need that token or the same session. Any editor or
  admin can take a lease once it has expired, and can take a stuck one away
  with `looptrack lease break --reason`. The previous holder gets a message,
  and every action stays in a record. Holders show up in
  `looptrack issue summary`, MCP `project_summary` and "Leases" above the
  board. The REST routes are under `/api/v1/projects/{slug}/leases`, and the
  MCP tools are `list_leases`, `acquire_lease`, `extend_lease`, `release_lease`
  and `break_lease`.
- Members can see the sessions running in a project. `looptrack issue summary`,
  MCP `project_summary` and "Presence" above the board show each session's
  user, machine, kind of AI, branch, HEAD, worktree directory name and issues
  in progress. Hooks send this at SessionStart and Stop, and at most every 5
  minutes during a turn, without ever making the AI wait. A session leaves the
  list 15 minutes after it last sent, or at SessionEnd, and its row is deleted
  after 7 days. The absolute worktree path is never sent. The REST routes are
  `POST` and `DELETE /api/v1/projects/{slug}/presence`.
- The loop kit has a new SessionStart hook, `session-start-remote-sync`. It
  fetches at most once per `LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL` (10 minutes by
  default) and prints one line only when the current branch is behind, has
  diverged, or holds unpushed commits. After a failed or timed-out fetch it
  prints nothing, so silence does not prove you are in sync.

### Changed

- Transcript retention now has the same upper limit as messages and memory,
  36500 days, wherever it is set or read. A stored value above it is ignored
  and the default applies.

### Moving from 1.2.0

- Migrations 0011 to 0016 add the tables for all of the above. A MySQL app user
  with the minimum grants needs these:

  | Table | Grants |
  | -- | -- |
  | `memories`, `leases` | SELECT, INSERT, UPDATE |
  | `memory_versions`, `memory_purges`, `messages`, `message_reads`, `lease_events` | SELECT, INSERT |
  | `memory_bodies` | SELECT, INSERT, DELETE |
  | `session_presence` | SELECT, INSERT, UPDATE, DELETE |

  `looptrack grants check` shows what is missing, and `looptrack grants apply`
  grants it. `install.sh --upgrade` checks this and, if anything is missing,
  asks for the admin credentials on the terminal.
- On MySQL, run the `--upgrade` from a terminal. The automatic upgrade (the
  timer of `--auto-upgrade on`) doesn't replace a version with pending
  migrations, so it leaves the server on the version it has. With the minimum
  grants the app user can't create tables, so pass a connection that can in
  `LOOPTRACK_SETUP_MIGRATE_DSN` ("更新（--upgrade）" in `docs/server/DEPLOY.md`).
- Run `looptrack issue init` again in each project that uses the loop kit. It
  wires `session-start-messages`, `user-prompt-messages` and
  `session-start-remote-sync`. Presence goes over the existing wiring and needs
  no `init`.
- Upgrade the server before relying on the new CLI. Against an older server,
  `looptrack memory`, `message` and `lease` say the server does not support
  them.

---

1.3.0 の最初のリリース候補です。プレリリースとして公開するので、GitHub Releases の Latest と GHCR の `latest` タグは `v1.2.0` のままです。下の変更は `v1.2.0` からの差分です。

この版では別の端末や別の利用者のセッションが 1 つのプロジェクトで一緒に動けます。できるのは記憶と引き継ぎの共有・連絡・同時に 1 本しか動かせない作業の占有・ほかのセッションの確認です。マイグレーションが 6 本あるので、上げる前に「1.2.0 から上げるとき」を読んでください。

### 追加

- プロジェクト記憶（引き継ぎ・決定・注意）をサーバに置けるようになり、同時に書いても消えません。`looptrack memory append` は版を問わず節を足すので 2 つの追記はどちらも残ります。`looptrack memory replace --version N` が全文を置き換えるのは N が今の版のときだけで、違えば 409 `memory_version` を返します。`project` の記憶を読むのは参加者全員で書くのは editor と admin です。`personal` の記憶は持ち主のものなので、管理者を含むほかの利用者には 404 を返します。書くたびに利用者・端末のラベル・セッションを版に残します。資格情報の形の本文は値を出さずに拒みます。REST は `/api/v1/projects/{slug}/memory` の下にあります。追記は `Idempotency-Key` ヘッダを受け取り、書き込みには `written_at` を添えられます。一覧の応答には `can_write` が入ります。MCP のツールは `list_memories`・`get_memory`・`write_memory` で、画面はボードの上の「記憶」です。
- loop の kit が引き継ぎをこの記憶に置けるようになりました。`LOOPTRACK_LOOP_HANDOFF_BACKEND=server` にすると、SessionStart が引き継ぎと決定・注意の要約の一覧を注入します。別の端末や別の利用者のセッションも同じ内容から始められます。上限は `LOOPTRACK_LOOP_MEMORIES_MAX_CHARS` で、切り出し方は file の置き場と同じです。`looptrack handoff append` は節を足し、`looptrack handoff compact --version N` は読んだ版が古ければ何も書きません。Stop のガードは作業の完了より後にこのセッションが書いた版を探します。サーバに届かないときは注意だけを出し、`handoff append` と `compact` の本文は手元に退避します。次の SessionStart・Stop か次の書き込みが送り、送り直しても節は増えません。未送信は `looptrack handoff spool` で確かめられます。
- `looptrack memory import` で `.claude/memories/` の `handoff.md`・`decision-*.md`・`caveat-*.md` をサーバへ取り込めます。`> 要約:` の行は要約になります。既にある名前は上書きしません。結果には飛ばしたファイルと拒んだファイルが出て、資格情報のときは行と規則の語だけを示します。
- 記憶の版の本体を戻せない形で消せるようにし、増え方に上限を設けました。`looptrack memory purge` を使えるのはプロジェクトの admin・サーバの管理者・personal の持ち主です。運用者向けの `looptrack purge-memory` はほかの利用者の personal にも届き、`--yes` を付けるまでは数と大きさを示すだけです。どちらも `--version N` か `--all-versions` と理由を指定します。版の行は残り本体を読むと 404 `memory_version_purged` を返します。消した記録は追記専用の `memory_purges` に残ります。追記した本文は後の版に引き継がれるので、版 N の本文が後の版に残っていれば 409 `memory_purge_text_remains` で何も消しません。秘密を消すときは `--all-versions` を使ってください。今の版を消すと版が進むので、前に読んだ版からの置き換えは拒まれます。本体を持つ版は記憶 1 件につき 100 版までで、古い版から消えます。保持日数は `looptrack settings memory-retention` で 36500 日まで指定できます。既定の 0 は無期限です。どの設定でも今の版は消えません。SQLite は `secure_delete` で動くようになり、消した本文をファイルの空き領域に残しません。バックアップの中の本体はバックアップの側で消してください。
- セッション・端末・利用者の間でサーバ経由の短い連絡を送れます。`looptrack message send` と MCP の `send_message` の宛先はプロジェクト全員（`--all`）・利用者 1 人（`--to`）・セッション 1 本（`--to` と `--session`）で、本文は 4 KiB までです。プロジェクト宛ては参加者全員に届き、それ以外を読めるのは宛先と送り手だけです。`--issue` を付けるとそのイシューにコメントも残ります。loop の hook `session-start-messages` と `user-prompt-messages` が未読を次のターンに入れます。1 回に 10 件・1 件 500 文字までで、サーバに届かないときは何も出さずに通します。止めるには `LOOPTRACK_LOOP_MESSAGES=0` を指定します。読むのは `looptrack message inbox`・MCP の `read_inbox`・ボードの上の「連絡」です。送れるのは editor と admin だけです。資格情報の形の本文も拒みます。`message_retention_days` を過ぎた連絡は出なくなりますが行は残ります。既定は 30 日で、`looptrack settings message-retention` には 0（無期限）か 36500 日までの値を渡せます。
- 本番への反映・公開の PR・リリースのタグのように同時に 1 本しか動かせない作業を、期限つきで占有できます。`looptrack lease acquire deploy.dev --purpose "…" --ttl 30m` で取ります。期限は既定 1 時間・上限 24 時間です。取られていれば終了コード 1 で持ち主・目的・期限を出し、REST は 409 `lease_held` を返します。`acquire` が `lease_token` を出すので、`extend` と `release` にはその鍵か同じセッションが要ります。期限が切れた占有は editor と admin なら誰でも取れます。止まった占有も `looptrack lease break --reason` で外せます。外された者には連絡が届き、操作はすべて記録に残ります。持ち主は `looptrack issue summary`・MCP の `project_summary`・ボードの上の「占有」に出ます。REST は `/api/v1/projects/{slug}/leases` の下にあり、MCP のツールは `list_leases`・`acquire_lease`・`extend_lease`・`release_lease`・`break_lease` です。
- プロジェクトで動いているセッションを参加者が見られます。`looptrack issue summary`・MCP の `project_summary`・ボードの上の「在席」に、利用者・端末・AI の種類・ブランチ・HEAD・作業ツリーのディレクトリの名前・着手中のイシューが出ます。hook が SessionStart と Stop のほかターンの途中でも 5 分に 1 回まで送るので、AI を待たせません。最後に送ってから 15 分か SessionEnd で一覧から消え、行は 7 日で消えます。作業ツリーの絶対パスは送りません。REST は `POST`・`DELETE /api/v1/projects/{slug}/presence` です。
- loop の kit に SessionStart の hook `session-start-remote-sync` を足しました。`LOOPTRACK_LOOP_REMOTE_SYNC_INTERVAL`（既定 10 分）に 1 回まで fetch し、いまのブランチが遅れている・乖離している・push していないコミットを持つときだけ 1 行知らせます。fetch が失敗か時間切れなら何も出さないので、何も出ないことは同期している根拠になりません。

### 変更

- 会話記録の保持日数にも連絡と記憶と同じ 36500 日の上限が付きました。設定と読み出しのどちらでも効きます。保存された値が上限を超えていれば既定の値を使います。

### 1.2.0 から上げるとき

- マイグレーション 0011〜0016 が上の機能の表を足します。MySQL のアプリ用の利用者を最小権限にしているときは、次の権限が要ります。

  | 表 | 権限 |
  | -- | -- |
  | `memories`・`leases` | SELECT・INSERT・UPDATE |
  | `memory_versions`・`memory_purges`・`messages`・`message_reads`・`lease_events` | SELECT・INSERT |
  | `memory_bodies` | SELECT・INSERT・DELETE |
  | `session_presence` | SELECT・INSERT・UPDATE・DELETE |

  足りない権限は `looptrack grants check` で分かります。与えるのは `looptrack grants apply` です。`install.sh --upgrade` は足りないときに管理用の資格情報を端末で尋ねます。
- MySQL では `--upgrade` を端末で実行してください。自動の置き換え（`--auto-upgrade on` の timer）は未適用のマイグレーションがある版を置き換えないので、サーバは今の版のまま動きます。最小権限のアプリ用の利用者は表を作れません。表を作れる接続先を `LOOPTRACK_SETUP_MIGRATE_DSN` で渡します（`docs/server/DEPLOY.md` の「更新（--upgrade）」）。
- loop の kit を使うプロジェクトでは `looptrack issue init` をやり直してください。`session-start-messages`・`user-prompt-messages`・`session-start-remote-sync` が配線されます。在席は既にある配線で送るので `init` は要りません。
- 新しい CLI の機能を使う前にサーバを上げてください。古いサーバには `looptrack memory`・`message`・`lease` が対応していないと出ます。

## [1.2.0] - 2026-10-30

1.2.0 is a stable release. The changes below are relative to `v1.2.0-rc.1`,
and no migrations were added after it. If you upgrade from `v1.1.0`, read the
section for 1.2.0-rc.1 as well. Unlike the release candidate, 1.2.0 becomes the
Latest release on GitHub Releases and takes the `latest` tag on GHCR, so the
one-line installer installs it. A server on 1.1.0 tells you about it, and so
does one on 1.2.0-rc.1; once on 1.2.0, a server follows stable releases only.

### Fixed

- `looptrack session transcript push` no longer ends with exit code 1 on
  every run because of Claude Code subagent files. Claude Code keeps writing
  to `agent-<id>.meta.json` and `agent-<id>.prefix.json` while a subagent
  runs, and adds `"finished": true` when it ends. Such a file that has not
  been sent is now sent only when that `meta.json` has `"finished": true` or
  the matching `agent-<id>.jsonl` has not changed for 30 minutes. A JSON file
  that changes after it was sent is not sent again and is no longer counted
  as rewritten. The output shows it on a separate line, and the hook that
  sends transcripts no longer records a failure for it.

---

1.2.0 は正式版で、下の変更は `v1.2.0-rc.1` からの差分です。rc.1 の後に足したマイグレーションはありません。`v1.1.0` から上げるなら 1.2.0-rc.1 の節も読んでください。候補版と違って 1.2.0 は GitHub Releases の Latest になり、GHCR の `latest` タグも付きます。1 行のインストーラが入れるのもこの版です。1.1.0 と 1.2.0-rc.1 のサーバはこの版を知らせます。1.2.0 に上げた後は正式版だけを追います。

### 修正

- Claude Code のサブエージェントのファイルのせいで、`looptrack session transcript push` が毎回終了コード 1 で終わることはなくなりました。Claude Code はサブエージェントが走っている間も `agent-<id>.meta.json` と `agent-<id>.prefix.json` に書き足し、終わったときに `"finished": true` を足します。まだ送っていないこれらのファイルは、その `meta.json` が `"finished": true` を持つか同じ `agent-<id>.jsonl` が 30 分以上更新されていないときだけ送ります。終わる前に送ると後で中身が変わるからです。送った後に変わった JSON のファイルは送り直さず、書き換えられたファイルにも数えません。出力では別の行に件数を出し、会話記録を送る hook もこれを失敗として記録しません。

## [1.2.0-rc.1] - 2026-10-28

First release candidate for 1.2.0. It is published as a pre-release, so
`v1.1.0` stays the Latest release on GitHub Releases and keeps the `latest`
tag on GHCR.
The changes below are relative to `v1.1.0`. The main additions tell
administrators about features that are off by default, and check whether the
reverse proxy in front lets a conversation transcript through. This version
adds no migrations.

### Added

- **The server tells administrators about a feature that is off by default
  and still off in every project.** Rules such as `session.transcripts`
  (added in 1.1.0) stay off after an upgrade, and until now nothing said so.
  When no project that isn't archived has such a rule on, the server writes a
  warning to its startup log and shows a notice at the top of
  `/admin/projects`, each with how to turn it on.
- **`looptrack features`** is a new server command. For each feature that is
  off by default it prints how many projects have it on, and how to turn it
  on when none do. The exit code is always 0.
- **`looptrack check-proxy [--url <base URL>]`** checks whether the reverse
  proxy in front lets one conversation transcript chunk through. It sends a
  32MiB body without credentials to the transcript route under
  `LOOPTRACK_PUBLIC_URL` plus `LOOPTRACK_BASE_PATH` from `.env`, and reports
  one of three results: enough, stopped by the proxy with HTTP 413, or
  could not tell. Nothing is stored, and the exit code is 0.
- install.sh runs `looptrack check-proxy` once the service is up, both on a
  fresh install and after `--upgrade`. After `--upgrade` it also runs
  `looptrack features`. A failing check prints a warning and the run goes on.
  `LOOPTRACK_LOCAL_MODE=1` skips the proxy check, and `--no-start` skips both.
- The summary response carries `session_transcripts`, the value of the
  project rule of that name.

### Changed

- Transcripts now mask a password given to a MySQL client before they are
  sent: the value joined to `-p` (`-p…`, `-p'…'`) and `--password=…` after
  `mysql`, `mysqldump`, `mysqladmin` and the other MySQL clients, and
  `MYSQL_PWD=…`. A word after `-p` and a space is a database name to the
  MySQL client, so it is left alone, and so are `--password` with no value
  and `-P` (the port). The same rules also mask the password in
  `user:…@tcp(…)` and `https://user:…@`, `Authorization: Basic …`, the value
  of an upper-case `*_KEY=`, and GitHub (`ghp_`, `github_pat_`), Slack
  (`xox?-`) and temporary AWS (`ASIA`) tokens.
- These rules are shared, so verify output, the check before an attachment
  is sent, and the comment check (`comment.reject_secret`) see them too. A
  comment that carries one of these forms is now refused (or saved with a
  notice, if the project turned the rule off). Replace the value with `***`
  and send it again.
- When a project has transcript storage turned off, `issue summary` and the
  summary injected at SessionStart now say so in one line. If this machine
  lacks `LOOPTRACK_SESSION_TRANSCRIPTS=1` as well, the same line says that it
  is needed too.

### Fixed

- The line pointing to `LOOPTRACK_SESSION_TRANSCRIPTS=1` now appears when the
  project rule is on and the variable is missing. The client used to learn the
  rule only from a send, and a machine without the variable never sent, so
  the line never showed. The summary now reads the rule from the server.
- An HTTP 413 without a JSON error comes from the reverse proxy in front, not
  from looptrack. The CLI now says that the proxy stopped the request at its
  body size limit, that it never reached the server, and that only the
  server's administrator can fix it (with nginx, `client_max_body_size 33m`
  or more). It used to print only "HTTP 413". When the automatic transcript
  hook fails this way, the failure line in the session-start summary says the
  same.
- `issue attach` no longer stops a text file when a word such as `secret` or
  `token` ends one line and the next line starts with `=` or `:`. The check
  before an attachment joined the two lines into one match, so a verbose
  `go test -v` log with a test name ending in `secret` could not be attached.
  A key and its value are now looked for within one line. The same rules mask
  verify output and transcripts and check comments, so those no longer join
  lines either.

### Docs

- The server user guide (Getting started) now has a section on reverse proxy
  settings. It shows the Nginx example that install.sh prints and why each
  line is there, including `client_max_body_size 33m` for transcript chunks
  and attachments. It also shows how to check the settings with `nginx -t` and
  a large request, and gives the Caddy example.

---

1.2.0 のリリース候補の 1 つ目です。プレリリースとして公開するので、GitHub Releases の Latest と GHCR の `latest` タグは `v1.1.0` のままです。下の変更は `v1.1.0` からの差分です。中心は既定で切の機能を管理者に知らせることと、前段のリバースプロキシが会話記録を通すかを確かめることです。この版はマイグレーションを足しません。

### 追加

- **既定で切のままどのプロジェクトでも切の機能を、サーバが管理者に知らせます。** 1.1.0 で足した `session.transcripts` のようなルールは版を上げても切のままで、これまでは誰にも知らされませんでした。アーカイブしていないどのプロジェクトでも切なら、起動時のログに警告を残して `/admin/projects` の上にも知らせを出します。どちらにも入れ方を添えます。
- **`looptrack features`** はサーバの管理用の新しいコマンドです。既定で切の機能ごとに入のプロジェクトの数を出し、どこでも切なら入れ方も出します。終了コードはいつも 0 です。
- **`looptrack check-proxy [--url <基点>]`** は前段のリバースプロキシが会話記録の 1 チャンクを通すかを確かめます。`.env` の `LOOPTRACK_PUBLIC_URL` と `LOOPTRACK_BASE_PATH` の下の会話記録の経路へ 32MiB の本文を認証なしで送ります。結果は「足りている」「前段が HTTP 413 で止めた」「確かめられなかった」のどれかです。何も保存せず終了コードは 0 です。
- install.sh はサービスの起動を確かめた後に `looptrack check-proxy` を実行します。新しく入れたときと `--upgrade` の後の両方です。`--upgrade` の後には `looptrack features` も実行します。確かめが失敗しても注意を出して続けます。`LOOPTRACK_LOCAL_MODE=1` では前段の確かめを飛ばし、`--no-start` ではどちらも実行しません。
- summary の応答に、同じ名前のプロジェクト別ルールの値 `session_transcripts` を足しました。

### 変更

- 会話記録は MySQL のクライアントに付けたパスワードを送る前に伏せるようになりました。対象は `mysql`・`mysqldump`・`mysqladmin` などの `-p` につなげた値（`-p…`・`-p'…'`）と `--password=…`、それに `MYSQL_PWD=…` です。
  MySQL のクライアントでは `-p` の後に空白がある語は DB 名なので、伏せません。値の無い `--password` と大文字の `-P`（ポート）も残します。
  同じ規則で `user:…@tcp(…)` と `https://user:…@` のパスワード・`Authorization: Basic …`・大文字の変数名の `*_KEY=` の値も伏せます。GitHub（`ghp_`・`github_pat_`）・Slack（`xox?-`）・AWS の一時キー（`ASIA`）のトークンも対象です。
- 規則は共有なので、verify の出力・添付を送る前の検査・コメントの検査（`comment.reject_secret`）にも効きます。これらの形を含むコメントは拒否されます（ルールを切にしたプロジェクトでは注記を付けて保存します）。値を `***` に替えて送り直してください。
- 会話記録の保存を切にしているプロジェクトでは、`issue summary` と SessionStart で注入する要約がそのことを 1 行で知らせます。この端末に `LOOPTRACK_SESSION_TRANSCRIPTS=1` も無ければそれも要ることを同じ行で添えます。

### 修正

- プロジェクト別ルールが入で環境変数が無いときに、`LOOPTRACK_SESSION_TRANSCRIPTS=1` を案内する行が出るようになりました。クライアントはルールを送ったときの応答からしか覚えません。変数の無い端末は一度も送らないので、この行が出ませんでした。要約はルールをサーバから読みます。
- JSON の error を持たない HTTP 413 は、looptrack ではなく前段のリバースプロキシが返したものです。CLI はこれを「前段のリバースプロキシが本文の大きさの上限で止めた。サーバには届いていない。直せるのはサーバの管理者」と知らせます（nginx なら `client_max_body_size` を 33m 以上にします）。これまでは「HTTP 413」とだけ出ていました。会話記録を自動で送る hook がこれで失敗したときも、セッションの冒頭の要約の失敗の行が同じことを伝えます。
- `secret` や `token` のような語で終わる行の次の行が `=` や `:` で始まっていても、`issue attach` はテキストのファイルを止めなくなりました。添付の前の検査が 2 つの行を 1 つの一致としてつないでいたので、テスト名が `secret` で終わる `go test -v` のログを添付できませんでした。鍵と値はいまは同じ行の中だけで探します。verify の出力と会話記録のマスクとコメントの検査も同じ規則を使うので、これらも行をつながなくなりました。

### 文書

- サーバ版の利用者ガイドの「始め方」に、install.sh が表示する Nginx の例と各行が要る理由を載せた
  リバースプロキシの設定の節を足しました。会話記録のチャンクと添付のための `client_max_body_size 33m`
  もその 1 つです。確かめ方と Caddy の例もあります。

## [1.1.0] - 2026-10-27

1.1.0 is a stable release. The changes below are relative to `v1.1.0-rc.1`,
and no migrations were added after it. If you upgrade from `v1.0.2`, read the
section for 1.1.0-rc.1 as well, starting with "Moving from 1.0.2". Unlike the
release candidate, 1.1.0 becomes the Latest release on GitHub Releases and
takes the `latest` tag on GHCR, so the one-line installer installs it. A
server on 1.0.2 tells you about it, and so does one on 1.1.0-rc.1; once on
1.1.0, a server follows stable releases only.

One change needs your own hands. The nginx example that install.sh prints now
sets `client_max_body_size 33m;`, and the Caddy example in DEPLOY.md sets
`max_size 33MiB`, so a transcript chunk of the default limit (32MiB) gets
through. A proxy you set up from the old `20m` / `20MiB` example keeps its old
limit. If you use transcripts, change it yourself.

### Fixed

- On Windows, when the path of the placed transcript contains `'` (a home
  directory such as `C:\Users\O'Brien`), the resume command that
  `looptrack session transcript pull` prints for Claude Code now passes the
  new session ID instead of the path: `claude --resume <ID> --fork-session`.
  No single quoting of that path works in both PowerShell and Git Bash, and an
  ID needs no quoting. Other paths and other OSes print the same command as
  before.
- A project that turned transcript sending on but has no Stop / SessionEnd
  hook wired for it is now told so. This happens to a project set up with
  `looptrack issue init` from 1.0.2 or earlier when only the CLI was
  replaced. Where `issue summary` and the summary injected at SessionStart
  used to say that sending was on, they now ask you to run
  `looptrack issue init` again.
- The nginx example that install.sh prints sets `client_max_body_size 33m;`
  (it was `20m`), so a transcript chunk of the default limit (32MiB) gets
  through. The Caddy example in DEPLOY.md now sets `max_size 33MiB` (it was
  `20MiB`) for the same reason. A proxy already set up from the old example
  still needs the change described under "Moving from 1.0.2" in 1.1.0-rc.1.

---

1.1.0 は正式版で、下の変更は `v1.1.0-rc.1` からの差分です。rc.1 の後に足したマイグレーションはありません。`v1.0.2` から上げるなら 1.1.0-rc.1 の節も読み、まず「1.0.2 から上げるとき」を確かめてください。候補版と違って 1.1.0 は GitHub Releases の Latest になり、GHCR の `latest` タグも付きます。1 行のインストーラが入れるのもこの版です。1.0.2 と 1.1.0-rc.1 のサーバはこの版を知らせます。1.1.0 に上げた後は正式版だけを追います。

運用者が手で直すものが 1 つあります。install.sh が示す nginx の例を `client_max_body_size 33m;` に、DEPLOY.md の Caddy の例を `max_size 33MiB` にしました。既定の上限（32MiB）の会話記録のチャンクが通る値です。前の `20m`・`20MiB` の例を写したプロキシは古い上限のまま残ります。会話記録を使うなら自分で直してください。

### 修正

- Windows で置いた会話記録のパスに `'` を含むとき（`C:\Users\O'Brien` のようなホーム）は、`looptrack session transcript pull` が出す Claude Code の再開のコマンドで、パスの代わりに新しいセッション ID を渡します（`claude --resume <ID> --fork-session`）。そのパスは 1 つの引用で PowerShell と Git Bash の両方を満たせません。ID なら引用が要りません。ほかのパスとほかの OS では出力は変わりません。
- 会話記録の自動送信を入れたのに Stop・SessionEnd の hook が配線されていないプロジェクトに、そのことを知らせるようにしました。1.0.2 以前の `looptrack issue init` で入れたまま CLI だけを置き換えるとこの状態になります。`issue summary` と SessionStart で注入する要約は、これまで「自動送信: 入」と出していた行で `looptrack issue init` のやり直しを案内します。
- install.sh が示す nginx の例を `client_max_body_size 33m;` にしました（これまでは `20m`）。既定の上限（32MiB）の会話記録のチャンクが通ります。DEPLOY.md の Caddy の例も `max_size 33MiB` にしました（これまでは `20MiB`）。前の例を写した設定は 1.1.0-rc.1 の「1.0.2 から上げるとき」のとおりに直す必要があります。

## [1.1.0-rc.1] - 2026-10-27

First release candidate for 1.1.0. It is published as a pre-release, so
`v1.0.2` stays the Latest release on GitHub Releases and keeps the `latest`
tag on GHCR.
The changes below are relative to `v1.0.2`. The main addition is working on
one project from several machines: an AI session leaves a session digest on
the issue it holds, and a project can let each user carry the raw transcript
to another machine of their own. Comments that look like they carry a
credential are now rejected by default. This version adds migrations 0009
and 0010, and a MySQL app user with the minimum grants needs new ones. If you
upgrade a server from 1.0.2, read "Moving from 1.0.2" first.

### Added

- **Session digests.** A comment whose first line starts with `セッション要約:`
  or `Session digest:` is a session digest: a short record an AI session
  leaves on the issue it holds (what was asked, what was decided, where it
  stopped) so that a session on another machine can pick the work up. The
  server checks the header (a machine label, then `branch:`, `head:` and
  `ai:` within the first 8 lines), rewrites the machine-specific paths the
  client reports in `path_hints` to `./`, `~` and `<machine-local>`, caps the
  body at 16 KiB, and rejects a body that looks like it carries a credential
  (422 `comment_secret`, naming only the line and the rule). The rule lives in
  one place and applies to REST, MCP (`add_comment`) and the CLI alike. A
  digest cannot ride on a status change (422 `digest_on_status`). Comments
  without the lead word are untouched.
- **`looptrack issue attachment get <attachment ID> [--out PATH]`** downloads an
  attachment. It verifies the bytes against the SHA-256 the server records,
  never overwrites an existing file, uses only the last path component of the
  name the server sends, and refuses a purged attachment with exit code 1.

- **`looptrack session digest <ID> [-|<file>]` and `looptrack session pull <ID>`.**
  `digest` posts a session digest as a comment: it fills in the header lines
  the body lacks (the lead word with a machine label from
  `LOOPTRACK_MACHINE_LABEL` or the host name, `branch:`, `head:` and `ai:`),
  sends the machine-specific paths as `path_hints`, and on a rejection prints
  only the line number and the rule. `pull` writes the latest digests of an
  issue to `.claude/.looptrack-work/<ID>-digest.md` and prints them.
- **`summary` and `next` carry the latest digest of each issue in progress**
  (`latest_digest`: excerpt, machine label, age, session, and whether it came
  from another session). The CLI, the SessionStart hook and MCP
  `project_summary` / `next` show the digests written by other sessions, so a
  terminal on another machine starts with the context the last one left.

- **Handoff backend `issue`** (loop kit, `LOOPTRACK_LOOP_HANDOFF_BACKEND=issue`).
  The Stop guard checks that every issue this session holds In Progress has a
  session digest written by this session after the last completion marker; if
  not, it sends the turn back once, naming the command
  `looptrack session digest <ID> -`. When the server cannot be reached it
  prints a notice and lets the turn end (fail-open). The summary response now
  tells each In Progress item whether it was started by the requesting
  session (`own_session`).
- **Project rule `session.digest_required`** (default off). When on, the Stop
  guard requires a fresh digest whatever handoff backend is configured. The
  server does not reject anything for this rule; the hook reads the value from
  the summary response (`session_digest_required`), after the backend's own
  check and only within the time the hook has left.

- **`looptrack issue attach` looks inside archives.** A gzip, zip or tar
  file (including `.tar.gz` / `.tgz`, two layers at most) is expanded in
  memory and every text member gets the same secret check as a plain file;
  one hit stops the whole attach and names the archive, the member, the line
  and the rule, never the value. Expansion is capped (64 MiB of expanded
  bytes, 10,000 members); an archive over the cap, a deeper nesting, or a
  broken or encrypted archive is not sent either. Other binary formats are
  still sent unchecked, and the guide says so.
- **Transcript discovery honours `CLAUDE_CONFIG_DIR` and `CODEX_HOME`**
  (token measurement, the auto-memory handoff backend and the path hints of
  session digests).

- **Groundwork for transferring raw AI transcripts between machines**
  (DESIGN §2-5 / §9-8). Migration 0009 adds the per-user transcript store
  tables (`transcripts`, `transcript_chunks`, `transcript_purges`,
  append-only; `deploy/grants.sql` gains their SELECT/INSERT lines) and the
  settings `transcript_max_chunk_bytes` / `transcript_max_user_bytes` /
  `transcript_retention_days` (defaults 32 MiB, 2 GiB, 90 days). The project
  rule `session.transcripts` (default off) is recognised and listed by
  `guide`. Token measurement counts Codex rollouts that Codex compressed to
  `.jsonl.zst` and says it did not read them instead of ignoring them silently.
  Migration 0010 adds `transcript_locks` (one row per user;
  `deploy/grants.sql` gains its SELECT/INSERT/UPDATE line, so run
  `looptrack grants` again). An append now renames and fsyncs the body before
  its transaction opens, and no longer locks the `users` row. On MySQL,
  appends and purges of one user's transcripts take turns on that user's
  `transcript_locks` row instead, so logins, language saves and token or
  session issuance for that user no longer wait behind an append. SQLite runs
  one write transaction at a time anyway; there the change only shortens the
  append's transaction.
  Removing a body (a purge, `purge-transcripts`, `repair-transcripts
  --apply`, or the cleanup after a rejected append) no longer races an append
  of the same content in another process (a second server, or
  `purge-transcripts` beside the server): an append leaves a marker in the
  store's `.tmp` from placing the body until its record commits, and removal
  moves the body aside, then puts it back if it finds such a marker or a new
  reference. An append must now record its chunk within 10 minutes of placing
  the body; past that it fails without recording.
  `repair-transcripts --apply` also picks up what an interrupted removal or
  append left in `.tmp`: a body moved aside goes back to its path if that
  path is free and its content matches (then the usual orphan rules apply),
  otherwise it is dropped (only a body moved aside more than an hour ago, so
  a removal still running elsewhere is left alone), and markers older than an
  hour are deleted; the counts appear on one line of the output. If such a
  check puts back a body that a purge is removing, the purge reports it as a
  body it could not remove instead of silently keeping it. `purge-transcripts` now finishes
  the body it is removing when interrupted (Ctrl-C, SIGTERM) and then stops.

- **Transcript store directory and `looptrack repair-transcripts`.** The
  server resolves the per-user transcript store's disk root from
  `LOOPTRACK_TRANSCRIPT_DIR`, then `$STATE_DIRECTORY/transcripts`, then the
  directory next to a SQLite database, and keeps it apart from the
  attachments root (a nested configuration disables the transcript API and
  leaves attachments working). `install.sh` writes the variable to `.env`,
  `setup`'s compose file mounts it, and the desktop app places it beside its
  data. `repair-transcripts` reports (or with `--apply` removes) unreferenced
  bodies the way `repair-attachments` does; neither command touches the other
  store's bodies.

- **Transcript store API and admin screen** (the server side of carrying raw
  AI transcripts between machines). Six routes under
  `/api/v1`: register a transcript, append chunks (append-only, `If-Match`
  sequence, `X-Looptrack-Transcript-*` headers, gzip bodies, per-value secret
  check and fail-closed on unreadable lines), list, get, stream a chunk body
  (sandboxed, `Content-Disposition: attachment`) and purge. Only the owner can
  see a transcript; everyone else, project admins included, gets 404.
  Registering and appending need the project rule `session.transcripts` on
  (reading and purging work with it off). `/admin/transcripts` shows the three
  limits (editable) and per-user usage without any transcript content.
  Housekeeping purges the content of transcripts whose last chunk is older
  than `transcript_retention_days`.

- **`looptrack session transcript push | list | purge | pull`.** `push`
  finds the current Claude Code or Codex transcript (the main file, subagent
  files and tool results), rewrites machine-specific paths to reversible
  placeholders, masks credential-shaped values per string, re-reads every
  line as JSON and sends the result to the per-user store in append-only
  chunks; it remembers what it sent and continues from there next time, and
  `--dry-run` prints counts only. `pull` restores a transcript on another
  machine under a NEW session id (so you fork, never append to the original),
  refuses a transcript whose original id already exists locally or whose OS
  family differs, and prints the resume command (`claude --resume <path>
  --fork-session` / `codex fork <id>`) without running it. Codex placement
  still needs checking on a real machine.
- **The transcript hook.** `looptrack issue init` wires a hook on Stop and
  SessionEnd for Claude Code and Codex (not Copilot) that sends the current
  transcript the same way `session transcript push` does. It sends only when
  both the project rule `session.transcripts` and the per-machine
  `LOOPTRACK_SESSION_TRANSCRIPTS=1` are on, at most once per 10 minutes on
  Stop (`LOOPTRACK_TRANSCRIPT_THROTTLE_MIN`), in a detached child so the turn
  is never held. A transcript the server refuses as a secret or that cannot
  be read is recorded once and not retried by the hook; a manual push still
  works. The SessionStart summary says whether sending is on, which setting
  is missing, or what the last failure was. `looptrack issue init
  --no-transcript` leaves the hook out.
- **`looptrack purge-transcripts --user <login> [--transcript <id>] --reason <text> --yes`**
  is the operator's way to delete another user's transcripts (the admin page
  has no purge button). Without `--yes` it only reports how many would be
  purged and their size.

### Docs

- The user guide (AI agents → working from several machines) and the kit's
  working discipline now describe how to work on one project from several
  machines: one terminal is one issue and one branch, push before switching,
  never resume the same conversation in two places (fork instead), and which
  vendor features show one chat on several devices.
- The user guide gains a "Session digests" section (AI agents → working from
  several machines) and the rule's row in the server admin pages; the loop
  kit's `session-handoff` skill carries the digest procedure.
- The user guide gains a "Carrying the raw transcript" section (AI agents →
  working from several machines: what the client rewrites and masks, how to
  push, pull and fork, the hook, and the limits), and the server admin pages
  gain the transcript settings, the per-user usage page and the operator's
  `purge-transcripts`.

### Changed

- **Comments are checked for secrets** (project rule `comment.reject_secret`,
  default on). A comment whose text looks like it carries a credential (the
  same rules as verify output: `token=`, `Bearer …`, private keys and the
  like) is rejected with 422 `comment_secret` on every path (REST, MCP, CLI,
  the web form and the comment of a status change), naming only the line and
  the rule. A project that turns the rule off stores the comment and returns
  a notice instead. Session digests are rejected whatever the rule says. The
  check may fire on prose that merely mentions such a word; replace the value
  with `***` and send again.
- `next` with a comment that is rejected (a secret, or a digest on a status
  change) now returns that 422 instead of silently starting nothing.

### Fixed

- A session digest posted after a `フィードバック:` / `Feedback:` comment no
  longer counts as a response: the feedback stays in "pending feedback" in
  `summary` and `list --has-feedback` until a person or a plain comment
  answers it. The lead words the store matches now come from one list in the
  domain package.
- **`looptrack gates --help` and `-h` print the usage and exit.** An unknown
  argument that starts with `-` is now an error (exit code 2). Before, `--help`
  went to make as a stage name, make read it as one of its own options, and
  gates reported every stage green.
- The hook `pre-tool-subagent-model` also looks for agent definitions under
  `agents/` in `CLAUDE_CONFIG_DIR` (falling back to `~/.claude`), so a type
  whose definition there carries `model:` is no longer stopped.
- **`session transcript push` rewrites more fields of Codex rollouts.** The
  absolute paths in fields found in real rollouts are now rewritten and
  restored on the importing machine: the new form of the writable roots (the
  `entries` of `file_system_sandbox_policy` and `permission_profile`),
  `workspace_roots`, the `input` of `custom_tool_call`, `item.command` and the
  keys of `item.changes`, among others. When the keys of `changes` could
  collide on the importing machine, they are left as they are.
- **Removing an attachment body no longer races another process placing the
  same body.** The orphan cleanup of `repair-attachments --apply` and an
  admin purge now remove bodies the same guarded way as transcripts: an attach
  leaves a marker in the store's `.tmp` from placing its body until its record
  commits, and removal puts the body back if it finds such a marker or a new
  reference. `repair-attachments` also counts what an interrupted removal left
  in `.tmp`; with `--apply` it puts such a body back or drops it, and deletes
  markers older than an hour. An attach now places its body before its
  transaction and must record it within 10 minutes; past that it fails
  without recording.
- **On Windows, the Codex transcript search no longer picks an older rollout
  of the same thread.** Windows records modification times coarsely, so two
  rollouts written one after the other can share one. The search then took
  whichever the listing gave first, which could be the older rollout. It now
  picks the one whose name carries the later time. This affects token
  measurement and `session transcript push`.
- **On Windows, refreshing a login no longer fails when another request is
  reading the saved credentials.** Windows cannot replace a file while it is
  open for reading, so saving the new tokens could fail with "Access is
  denied" while a hook and a command refreshed at the same time. The next
  request then reused the old refresh token, and the server rejected it. The
  save now waits up to 2 seconds for the reader to close the file.
- **Dependencies with known vulnerabilities are updated.** excelize, which
  builds the `.xlsx` files of the issue export and the token usage report,
  moves to v2.11.1-0.20260915055537-22f76f9acb94, the version that fixes
  GO-2026-6688 and GO-2026-6684. It is an untagged version (a Go
  pseudo-version). golang.org/x/net, which excelize uses, moves to v0.60.0.
- **`looptrack --help` lists every subcommand that exists.** The list was
  missing `session transcript`, `grants check`,
  `project rename|archive|unarchive` and `project guide set|show|clear`. Both
  the Japanese and the English text are fixed.

### Moving from 1.0.2

- This version adds migration 0009 (`transcripts`, `transcript_chunks` and
  `transcript_purges`) and migration 0010 (`transcript_locks`). A MySQL app
  user with the minimum grants needs SELECT and INSERT on the first three,
  and SELECT, INSERT and UPDATE on `transcript_locks`. After the migration,
  `--upgrade` checks the grants on every table and, if any are missing, asks
  for the admin credentials on the terminal and grants them again.
  `looptrack grants check` shows what is missing, and `looptrack grants apply`
  grants it.
- On MySQL, run the `--upgrade` from a terminal. The automatic upgrade (the
  timer of `--auto-upgrade on`) doesn't replace a version that adds
  migrations on MySQL, so it leaves the server on 1.0.2. With the minimum
  grants the app user can't create the new tables, so pass a connection that
  can in `LOOPTRACK_SETUP_MIGRATE_DSN` ("更新（--upgrade）" in
  `docs/server/DEPLOY.md` shows how to pass it through `sudo`).
- A server set up with `looptrack setup` alone runs `looptrack migrate` by
  hand, as before. With MySQL and the minimum grants, run
  `looptrack grants check` after it and `looptrack grants apply` if anything
  is missing, then start the server.
- Transcripts need a directory of their own. On a systemd server,
  `install.sh --upgrade` adds `LOOPTRACK_TRANSCRIPT_DIR` to `.env`. A
  `compose.yaml` written by an earlier `setup` has no such line: with SQLite
  the store falls to `/data/transcripts` next to the database, and with MySQL
  you add `LOOPTRACK_TRANSCRIPT_DIR: /data/transcripts` to the `environment`
  of `services.looptrack` (the `./data:/data` volume is needed too; see
  "会話記録の置き場" in `docs/server/DEPLOY.md`) and run `docker compose up -d`.
  Without a directory
  the server runs and only transcripts are unavailable. Nothing is sent until
  a project turns on `session.transcripts`.
- If you turn transcripts on, raise the body limit of the proxy in front
  above the chunk limit (32MiB by default; `client_max_body_size 33m;` in
  nginx). The nginx example that install.sh prints still sets `20m`, the
  attachment limit.
- Back up the transcript directory after the database, as with attachments.
  The backup that `--upgrade` takes doesn't include it.
- Comments are now checked for secrets by default. A project that wants the
  old behavior sets `"comment": {"reject_secret": false}`; such a comment is
  then stored with a notice.
- Update the server before clients send session digests. A server up to
  1.0.2 doesn't know the `path_hints` that `looptrack session digest` sends
  and refuses the comment with 400 (`invalid_json`). `session transcript`
  needs this version on the server too.
- Once this version has migrated the database, 1.0.2 refuses to start on
  it. To go back, restore the backup taken before the upgrade.
- A project set up with the `looptrack issue init` of 1.0.2 or earlier has no
  wiring for the transcript hook (Stop and SessionEnd of Claude Code and
  Codex). Replacing `looptrack` alone doesn't add it, and neither the summary
  nor the server's notice tells you. To send transcripts automatically, run
  `looptrack issue init --project <slug> --url <server URL> --agent <agent>`
  once more in each project with the new `looptrack`. Session digests and a
  manual `session transcript push` work without it.

---

1.1.0 のリリース候補の 1 つ目です。プレリリースとして公開するので、GitHub Releases の Latest と GHCR の `latest` タグは `v1.0.2` のままです。下の変更は `v1.0.2` からの差分です。中心は 1 つのプロジェクトを複数の端末で進める機能です。AI のセッションは持っているイシューにセッション要約を残せます。プロジェクトで有効にすれば、利用者は会話記録そのものを自分の別の端末へ運べます。資格情報らしい文字列を含むコメントは、既定で拒否するようになりました。この版はマイグレーション 0009 と 0010 を足すので、MySQL のアプリ用の利用者を最小権限にしているなら新しい権限が要ります。1.0.2 のサーバを上げるなら、先に「1.0.2 から上げるとき」を読んでください。

### 追加

- **セッション要約。** 先頭が `セッション要約:` か `Session digest:` のコメントはセッション要約として扱います。AI のセッションが、持っているイシューに残す短い記録（頼まれたこと・決めたこと・どこまで進んだか）で、別の端末のセッションが続きから取れるようにするものです。サーバは見出し（端末のラベルと、8 行目までの `branch:`・`head:`・`ai:`）を確かめ、クライアントが `path_hints` で伝えた端末固有のパスを `./`・`~`・`<machine-local>` に書き換え、本文を 16 KiB に制限し、資格情報らしい文字列を含む本文を拒否します（422 `comment_secret`。理由には行番号と規則の語だけを出します）。規則は 1 か所にあり、REST・MCP（`add_comment`）・CLI のどれから送っても同じです。状態変更のコメントには載せられません（422 `digest_on_status`）。印の無いコメントには手を加えません。
- **`looptrack issue attachment get <添付ID> [--out PATH]`** で添付を手元に落とせます。サーバが記録した SHA-256 と照合し、既にあるファイルは上書きせず、サーバが送る名前は最後の要素だけを使い、消去済みの添付は終了コード 1 で断ります。

- **`looptrack session digest <ID> [-|<ファイル>]` と `looptrack session pull <ID>`。** `digest` はセッション要約をコメントとして送ります。本文に無い見出しの行（印と端末のラベル。ラベルは `LOOPTRACK_MACHINE_LABEL` かホスト名。`branch:`・`head:`・`ai:`）を埋め、端末固有のパスを `path_hints` で伝え、拒否されたときは行番号と規則の語だけを出します。`pull` はイシューの最新の要約を `.claude/.looptrack-work/<ID>-digest.md` に書いて表示します。
- **`summary` と `next` が、着手中のイシューごとに最新の要約を載せます**（`latest_digest`: 抜粋・端末のラベル・経過・セッション・別のセッションのものか）。CLI・SessionStart の hook・MCP の `project_summary` / `next` は別のセッションが書いた要約を表示するので、別の端末のターミナルは前の端末が残した文脈から始められます。

- **引き継ぎの backend `issue`**（loop kit・`LOOPTRACK_LOOP_HANDOFF_BACKEND=issue`）。Stop のガードが、このセッションが In Progress にしているイシューのすべてに、最後の完了の印より新しい自分の要約があるかを確かめます。無ければ 1 回だけ差し戻し、`looptrack session digest <ID> -` のコマンドを示します。サーバに届かないときは注記を出して通します（fail-open）。summary の応答は、In Progress の各項目に、要求したセッションが着手したものかを `own_session` で示すようになりました。
- **プロジェクト別ルール `session.digest_required`**（既定 off）。on にすると、引き継ぎの backend に関わらず Stop のガードが新鮮な要約を求めます。サーバはこのルールで何も拒否しません。hook は値を summary の応答（`session_digest_required`）から読みます。読むのは backend 自身の判定の後で、hook に残った時間の範囲だけです。

- **`looptrack issue attach` が書庫の中を見ます。** gzip・zip・tar（`.tar.gz` / `.tgz` を含む。入れ子は 2 段まで）をメモリ内で展開し、テキストの要素にふつうのファイルと同じ秘密の検査を当てます。1 件でも当たれば添付全体を止め、書庫・要素・行・規則だけを示します（値は出しません）。展開には上限（展開後 64 MiB・要素 10,000）があり、超えた書庫・深い入れ子・壊れた書庫や暗号化された書庫も送りません。ほかのバイナリ形式はこれまでどおり検査せずに送ります（ガイドに明記）。
- **会話記録の探索が `CLAUDE_CONFIG_DIR` と `CODEX_HOME` を見ます**（トークンの計測・auto-memory の引き継ぎの backend・セッション要約の path_hints）。

- **会話記録を端末間で運ぶ機能の下準備**（DESIGN §2-5 / §9-8）。マイグレーション 0009 で利用者ごとの会話記録の置き場の表（`transcripts`・`transcript_chunks`・`transcript_purges`。追記専用。`deploy/grants.sql` に SELECT / INSERT の行）と設定 `transcript_max_chunk_bytes` / `transcript_max_user_bytes` / `transcript_retention_days`（既定 32 MiB・2 GiB・90 日）を足しました。プロジェクト別ルール `session.transcripts`（既定 off）を認識し `guide` に載せます。トークンの計測は、Codex が `.jsonl.zst` に圧縮した rollout を黙って無視せず、読まなかった件数を知らせます。
  マイグレーション 0010 で `transcript_locks`（1 利用者 1 行。`deploy/grants.sql` に SELECT / INSERT / UPDATE の行。`looptrack grants` を流し直してください）を足しました。追記は本体の rename と fsync をトランザクションの前に済ませ、`users` の行を握らなくなりました。MySQL では、同じ利用者の会話記録の追記と消去がその利用者の `transcript_locks` の行で順番を待つので、その利用者のログイン・言語の保存・トークンやセッションの発行が追記を待ちません。SQLite はもともと書き込みのトランザクションを 1 つずつ実行するので、変わるのは追記のトランザクションが短くなることだけです。本体を消す処理（消去・`purge-transcripts`・`repair-transcripts --apply`・拒まれた追記の片付け）は、別のプロセス（2 つ目のサーバや、サーバの隣で動かす `purge-transcripts`）で同じ内容を追記している途中の本体を消さなくなりました。追記は本体を置いてから記録を確定するまで置き場の `.tmp` に印を置き、消す側は本体を脇へ外してから、その印か新しい参照があれば本体を戻します。追記は本体を置いてから 10 分以内に記録を確定しなければならず、越えたら記録せずに失敗します。`repair-transcripts --apply` は、途中で止まった削除や追記が `.tmp` に残したものも拾います。外したまま残った本体は、本体のパスが空いていて中身が合えば戻し（その後はふつうの規則で扱う）、そうでなければ捨て（外して 1 時間を過ぎたものだけ。ほかで動いている削除には触れない）、1 時間より古い印を消します。消去の途中の本体をこの検査が戻したときは、消去は黙って残さず、消せなかった本体として知らせます。件数は出力の 1 行に出ます。`purge-transcripts` は割り込み（Ctrl-C・SIGTERM）を受けると、消している途中の本体を片付けてから止まります。

- **会話記録の置き場と `looptrack repair-transcripts`。** サーバは利用者ごとの会話記録の置き場を `LOOPTRACK_TRANSCRIPT_DIR` → `$STATE_DIRECTORY/transcripts` → SQLite の DB の隣、の順で決め、添付の置き場とは分けます（入れ子の設定では会話記録の API だけが使えなくなり、添付は動きます）。`install.sh` は `.env` にこの変数を書き、`setup` の compose は volume を付け、デスクトップ版はデータの隣に置きます。`repair-transcripts` は `repair-attachments` と同じ形で、どこからも指されない本体を報告（`--apply` で削除）します。どちらのコマンドも相手の置き場の本体には触りません。

- **会話記録の置き場の API と管理者の画面**（会話記録を端末間で運ぶ機能のサーバ側）。`/api/v1` の下に 6 経路: 登録・チャンクの追記（追記専用。`If-Match` の連番・`X-Looptrack-Transcript-*` ヘッダ・gzip の本文・値ごとの秘密の検査・読めない行は送らせない）・一覧・取得・チャンクの本体のストリーム（sandbox・`Content-Disposition: attachment`）・消去。会話記録は持ち主だけが読め、プロジェクトの管理者を含む他の利用者には 404。登録と追記はプロジェクト別ルール `session.transcripts` が on のときだけ使えます（読み出しと消去は off でもできます）。`/admin/transcripts` は上限 3 つ（変更可）と利用者ごとの使用量を、会話の中身を出さずに示します。定期処理が、最後の送信から `transcript_retention_days` を過ぎた記録の本体を消します。

- **`looptrack session transcript push | list | purge | pull`。** `push` は現在の Claude Code / Codex の会話記録（本体・サブエージェント・ツールの結果）を見つけ、端末固有のパスを可逆なプレースホルダに書き換え、資格情報らしい値を文字列ごとにマスクし、全行を JSON として読み直してから、利用者ごとの置き場へ追記専用のチャンクで送ります。送った位置を覚えて次回は続きから送り、`--dry-run` は件数だけを出します。`pull` は別の端末で会話記録を**新しいセッション ID**の下に復元し（必ず fork する。元の記録には追記しない）、元の ID が手元にある記録や OS の系統が違う記録は拒み、再開のコマンド（`claude --resume <パス> --fork-session` / `codex fork <id>`）を表示するだけで実行しません。Codex の置き方は実機での確認が残っています。
- **会話記録を自動で送る hook。** `looptrack issue init` が Claude Code と Codex（Copilot は対象外）の Stop と SessionEnd に、現在の会話記録を `session transcript push` と同じやり方で送る hook を配線します。送るのは、プロジェクト別ルール `session.transcripts` と端末ごとの `LOOPTRACK_SESSION_TRANSCRIPTS=1` がどちらも入のときだけです。Stop では 10 分に 1 回まで（`LOOPTRACK_TRANSCRIPT_THROTTLE_MIN`）、送るのは切り離した子プロセスなのでターンを止めません。サーバが秘密として拒んだ記録と読めない記録は失敗として 1 回だけ記録し、hook は送り直しません（手で打つ push はこれまでどおり使えます）。SessionStart の要約に、自動送信が入か・どの設定が足りないか・最後の失敗が出ます。`looptrack issue init --no-transcript` で配線ごと外せます。
- **`looptrack purge-transcripts --user <login> [--transcript <id>] --reason <理由> --yes`。** 運用者がほかの利用者の会話記録を消す唯一の経路です（管理者の画面に消去のボタンはありません）。`--yes` が無いときは、消える件数と大きさを出すだけで何も変えません。

### 文書

- 利用者ガイド（AI エージェント → 複数の端末で使う）と kit の作業規律に、複数の端末で 1 つのプロジェクトを進めるときの決まりを書きました。1 端末 1 イシュー 1 ブランチ、切り替える前に push、同じ会話を 2 か所で再開しない（続けるなら fork）、1 つの会話を複数の端末で見るためのベンダーの仕組み、です。
- 利用者ガイドに「セッション要約」の節（AI エージェント → 複数の端末で使う）と、管理者のページのルールの行を足しました。loop kit の skill `session-handoff` に要約の手順を書きました。
- 利用者ガイドに「会話記録をそのまま運ぶ」の節（AI エージェント → 複数の端末で使う。クライアントが書き換えてマスクするもの・push と pull と fork・hook・限界）を、管理者のページに会話記録の設定と利用者ごとの使用量の画面、運用者の `purge-transcripts` を足しました。

### 変更

- **コメントに秘密の検査がかかります**（プロジェクト別ルール `comment.reject_secret`。既定は入）。資格情報らしい文字列（verify の出力と同じ規則: `token=`・`Bearer …`・秘密鍵など）を含むコメントは、どの経路（REST・MCP・CLI・Web のフォーム・状態変更のコメント）でも 422 `comment_secret` で拒否され、理由には行番号と規則の語だけが出ます。ルールを切ったプロジェクトでは保存して注記を返します。セッション要約はルールに関わらず拒否します。語を含むだけの散文にも当たることがあるので、値を `***` に替えて送り直してください。
- 拒否されるコメント（秘密、または状態変更に載せた要約）を付けた `next` は、黙って何も始めずに終わる代わりに、その 422 を返すようになりました。

### 修正

- `フィードバック:` のコメントの後にセッション要約が付いても、応答とは数えません。人かふつうのコメントが応えるまで、`summary` の「外からの反応」と `list --has-feedback` に残ります。store が照合する先頭語は domain の 1 つの一覧から作るようになりました。
- **`looptrack gates --help` と `-h` は使い方を出して終わります。** `-` で始まる未知の引数はエラーにします（終了コード 2）。以前は `--help` を段の名前として make に渡していました。make はそれを自分の選択肢と読むので、gates は「全段グリーン」と出していました。
- hook `pre-tool-subagent-model` はエージェントの定義を `CLAUDE_CONFIG_DIR` の下の `agents/` でも探します（無ければ `~/.claude`）。そこに `model:` を書いた定義の型は止めません。
- **`session transcript push` が書き換える Codex の rollout の欄を増やしました。** 実際の rollout で見つかった欄の絶対パスも書き換え、取り込む端末で戻します。対象は新しい形の書き込める根（`file_system_sandbox_policy` と `permission_profile` の `entries`）・`workspace_roots`・`custom_tool_call` の `input`・`item.command`・`item.changes` のキーなどです。`changes` のキーが取り込む端末で重なりうるときは、キーを元のまま残します。
- **添付の本体を消す処理が別のプロセスで同じ本体を置いている途中の添付の本体を消さなくなりました。** 対象は `repair-attachments --apply` の孤児の片付けと管理者の消去で、会話記録と同じ手順で消します。添付は本体を置いてから記録を確定するまで置き場の `.tmp` に印を置き、消す側はその印か新しい参照を見つけたら本体を戻します。`repair-attachments` は途中で止まった削除が `.tmp` に残した本体も数えます。`--apply` を付ければそれを戻すか捨てて 1 時間より古い印も消します。添付は本体をトランザクションの前に置くようにしました。置いてから 10 分以内に記録を確定できなければ、記録せずに失敗します。
- **Windows で、Codex の会話記録の探索が同じスレッドの古い rollout を選ばなくなりました。** Windows はファイルの更新時刻の刻みが粗く、続けて書いた 2 つの rollout の更新時刻がそろうことがあります。そのとき探索は一覧で先に出たほうを取り、古い rollout を返すことがありました。いまは名前の時刻が新しいほうを選びます。効くのはトークンの計測と `session transcript push` です。
- **Windows で、ほかの要求が保存した資格情報を読んでいてもログインの更新が失敗しなくなりました。** Windows は読み取りのために開かれているファイルを置き換えられません。そのため hook とコマンドが同時に更新すると、新しいトークンの保存が「Access is denied」で失敗することがありました。次の要求は古い更新トークンを使い直し、サーバに拒否されていました。いまは読み手がファイルを閉じるまで最大 2 秒待って保存します。
- **脆弱性が見つかった依存を上げました。** 課題管理表とトークンの集計の xlsx を作る excelize を、GO-2026-6688 と GO-2026-6684 が直った v2.11.1-0.20260915055537-22f76f9acb94 にしました。この版はタグの無い版（Go の擬似版）です。excelize が使う golang.org/x/net は v0.60.0 に上げています。
- **`looptrack --help` の一覧に実装のサブコマンドがすべて載ります。** `session transcript`・`grants check`・`project rename|archive|unarchive`・`project guide set|show|clear` が抜けていたので足しました。日本語と英語の両方を直しています。

### 1.0.2 から上げるとき

- この版はマイグレーション 0009（`transcripts`・`transcript_chunks`・`transcript_purges`）と 0010（`transcript_locks`）を足します。MySQL のアプリ用の利用者を最小権限にしているなら、前の 3 つの表の SELECT・INSERT と、`transcript_locks` の SELECT・INSERT・UPDATE が要ります。`--upgrade` は migrate の後に全部の表の権限を確かめ、足りなければ管理用の資格情報を端末で尋ねて与え直します。足りない権限は `looptrack grants check` で分かります。与えるのは `looptrack grants apply` です。
- MySQL では `--upgrade` を端末で実行してください。自動の置き換え（`--auto-upgrade on` の timer）は、MySQL でマイグレーションを足す版を置き換えません。サーバは 1.0.2 のまま残ります。最小権限のアプリ用の利用者は新しい表を作れないので、表を作れる接続先を環境変数 `LOOPTRACK_SETUP_MIGRATE_DSN` で渡します。`sudo` を通す渡し方は `docs/server/DEPLOY.md` の「更新（--upgrade）」にあります。
- `looptrack setup` だけで立ち上げたサーバはこれまでどおり `looptrack migrate` を手で実行します。MySQL を最小権限で使うならその後に `looptrack grants check` を実行します。足りなければ `looptrack grants apply` で与えてから起動します。
- 会話記録には専用の置き場が要ります。systemd のサーバなら `install.sh --upgrade` が `.env` に `LOOPTRACK_TRANSCRIPT_DIR` を足します。以前の `setup` が書いた `compose.yaml` にはこの行がありません。SQLite なら DB の隣の `/data/transcripts` に決まります。MySQL なら `services.looptrack` の `environment` に `LOOPTRACK_TRANSCRIPT_DIR: /data/transcripts` を足して `docker compose up -d` を実行します。`./data:/data` の volume も要ります（`docs/server/DEPLOY.md` の「会話記録の置き場」）。置き場が無くてもサーバは動き、会話記録だけが使えません。プロジェクトが `session.transcripts` を入れるまでは何も送られません。
- 会話記録を使うなら、前段のプロキシの本文の上限をチャンクの上限（既定 32MiB）より大きくします。nginx なら `client_max_body_size 33m;` です。install.sh が示す nginx の例は添付の上限の `20m` のままです。
- 会話記録の置き場も添付と同じく DB の後に控えます。`--upgrade` が取る控えには入りません。
- コメントには既定で秘密の検査がかかります。これまでの動きのままにしたいプロジェクトは `"comment": {"reject_secret": false}` を設定します。当たったコメントは注記つきで保存されます。
- クライアントがセッション要約を送る前に、サーバを更新してください。1.0.2 までのサーバは `looptrack session digest` が送る `path_hints` を知らず、そのコメントを 400（`invalid_json`）で拒否します。`session transcript` もサーバにこの版が要ります。
- この版が DB を移行した後は 1.0.2 はその DB では起動しません。戻すときは更新の前に取った控えを戻してください。
- 1.0.2 以前の `looptrack issue init` で入れたプロジェクトには、会話記録を自動で送る hook（Claude Code と Codex の Stop・SessionEnd）の配線がありません。`looptrack` を置き換えるだけでは配線は増えず、summary もサーバの案内もそれを知らせません。自動で送るなら、新しい `looptrack` で `looptrack issue init --project <slug> --url <サーバの URL> --agent <AI>` を各プロジェクトでもう一度実行してください。セッション要約と手で打つ `session transcript push` は配線が無くても使えます。

## [1.0.2] - 2026-10-06

1.0.2 is a stable release. The changes below are relative to `v1.0.1`,
and this version adds no migrations.

### Added

- **The web UI links to the user guide.** "Guide" sits below "Account
  settings" in the user menu and opens the guide website in a new tab. The
  link goes to the pages for the version the server runs, at
  `https://howashoji.github.io/Looptrack/<version>/`. Release candidates get
  their own pages too. When the page language is Japanese, `ja/` is added. A build
  whose version is not a release tag opens `latest/`.

### Docs

- Updated the documentation.

---

1.0.2 は正式版で、下の変更は `v1.0.1` からの差分です。この版にマイグレーションはありません。

### 追加

- **Web 画面から利用者ガイドを開けます。** 利用者メニューの「アカウント設定」の次に「ガイド」が付き、ガイドの Web サイトを新しいタブで開きます。行き先はサーバが動いている版のページで、URL は `https://howashoji.github.io/Looptrack/<版>/` です。リリース候補の版も同じです。画面の言語が日本語なら `ja/` が付きます。版がリリースのタグの形でない手元のビルドは `latest/` を開きます。

### 文書

- ドキュメントを最新に更新しました。

## [1.0.1] - 2026-10-05

1.0.1 is a stable release with the same contents as 1.0.1-rc.1. Nothing
changed after that release candidate, so the changes below are relative to
`v1.0.0`. Unlike the release candidate, 1.0.1 becomes the Latest release on
GitHub Releases and takes the `latest` tag on GHCR. The one-line installer
installs it, and a server on 1.0.0 tells you about it. For a running server,
the one change is a newer SQLite driver. A server that stores its data in
MySQL does not use that driver.

### Changed

- **The SQLite driver goes up to `modernc.org/sqlite` 1.60.1.** It is what a
  server that keeps its data in SQLite (`LOOPTRACK_DSN=sqlite:<file>`) reads
  and writes the database with. It moved up from 1.59.0, and
  `modernc.org/libc` from 1.75.7 to 1.77.1. The new driver ships its own list
  of third-party licenses (`LICENSE-3RD-PARTY.md`), and `NOTICE` carries
  it in full.
- Contributors: `deploy/public-scan.sh` reads its list of internal names
  from outside the published tree. In a clone of the public repository the
  list is not there, so the scan prints one line saying it skipped that check
  and goes on with the others. The check for issue IDs still runs.
  `CONTRIBUTING.md` describes it.

### Moving from 1.0.0 / 1.0.1-rc.1

- This version adds no migrations.
- A server on 1.0.0 shows the new version in a banner at the top of the web
  UI for admins, in `looptrack doctor` and in the startup log. The upgrade is one line:
  `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`.
  With `--auto-upgrade on`, the daily timer replaces the server by itself.
  There are no migrations, so that includes servers on MySQL.
- A server on 1.0.1-rc.1 already runs the same contents. It follows release
  candidates as well, so it tells you about 1.0.1 too, and the same
  `--upgrade` takes it there. Once on 1.0.1, it follows stable releases only;
  set `LOOPTRACK_UPDATE_CHANNEL=prerelease` to keep seeing release candidates.

---

1.0.1 は正式版で、中身は 1.0.1-rc.1 と同じです。その候補版の後に変更は無く、下の変更は `v1.0.0` からの差分になります。候補版と違うのは公開のされ方です。1.0.1 は GitHub Releases の Latest になり、GHCR の `latest` タグも付きます。1 行のインストーラが入れるのはこの版で、1.0.0 のサーバもこの版を知らせます。動いているサーバにとっての変更は、SQLite のドライバが新しくなったことだけです。データを MySQL に置くサーバは、このドライバを使いません。

### 変更

- **SQLite のドライバを `modernc.org/sqlite` 1.60.1 に上げました。** データを SQLite に置くサーバ（`LOOPTRACK_DSN=sqlite:<ファイル>`）は、このドライバで DB を読み書きします。前の版は 1.59.0 です。あわせて `modernc.org/libc` も 1.75.7 から 1.77.1 に上がりました。新しいドライバには第三者のライセンスの一覧（`LICENSE-3RD-PARTY.md`）が付いています。`NOTICE` にもその全文を収めました。
- 開発に加わる人向け: `deploy/public-scan.sh` は内部の名前の一覧を公開物の外から読みます。公開リポジトリの clone にはこの一覧が無いので、検査はその項目を飛ばしたことを 1 行出して残りの項目を続けます。イシューの ID の検査はこれまでどおり。説明は `CONTRIBUTING.md` にあります。

### 1.0.0 / 1.0.1-rc.1 から上げるとき

- この版にマイグレーションはありません。
- 1.0.0 のサーバは管理画面の上部の帯・`looptrack doctor`・起動時のログでこの版を知らせます。上げるときの 1 行は `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade` です。`--auto-upgrade on` にしたサーバでは 1 日 1 回の timer が自分で置き換えます。マイグレーションが無いので MySQL のサーバも同じです。
- 1.0.1-rc.1 のサーバは既に同じ中身で動いています。リリース候補も追う設定なので 1.0.1 も知らせ、同じ `--upgrade` で上がります。1.0.1 に上げた後は正式版だけを追います。リリース候補も見続けたいときは `LOOPTRACK_UPDATE_CHANNEL=prerelease` を設定してください。

## [1.0.1-rc.1] - 2026-10-05

First release candidate for 1.0.1, relative to `v1.0.0`. Because it comes
after 1.0.0, it is published as a pre-release: `v1.0.0` stays the Latest
release on GitHub Releases and keeps the `latest` tag on GHCR, so the one-line
installer still installs 1.0.0. For a running server, the one change is a newer
SQLite driver. A server that stores its data in MySQL does not use that driver.

### Changed

- **The SQLite driver goes up to `modernc.org/sqlite` 1.60.1.** It is what a
  server that keeps its data in SQLite (`LOOPTRACK_DSN=sqlite:<file>`) reads
  and writes the database with. It moved up from 1.59.0, and
  `modernc.org/libc` from 1.75.7 to 1.77.1. The new driver ships its own list
  of third-party licenses (`LICENSE-3RD-PARTY.md`), and `NOTICE` carries
  it in full.
- Contributors: `deploy/public-scan.sh` reads its list of internal names
  from outside the published tree. In a clone of the public repository the
  list is not there, so the scan prints one line saying it skipped that check
  and goes on with the others. The check for issue IDs still runs.
  `CONTRIBUTING.md` describes it.

### Moving from 1.0.0

- This version adds no migrations.
- A server on 1.0.0 follows stable releases and does not tell you about this
  release candidate. To try it, upgrade to it by version:
  `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade --version v1.0.1-rc.1`.
  Set `LOOPTRACK_UPDATE_CHANNEL=prerelease` if you want the new-version notice
  to include release candidates.

---

1.0.1 のリリース候補の 1 つ目で、下の変更は `v1.0.0` からの差分です。1.0.0 の後に出す版はプレリリースとして公開します。GitHub Releases の Latest と GHCR の `latest` タグは `v1.0.0` のままです。1 行のインストーラも 1.0.0 を入れます。動いているサーバにとっての変更は、SQLite のドライバが新しくなったことだけです。データを MySQL に置くサーバは、このドライバを使いません。

### 変更

- **SQLite のドライバを `modernc.org/sqlite` 1.60.1 に上げました。** データを SQLite に置くサーバ（`LOOPTRACK_DSN=sqlite:<ファイル>`）は、このドライバで DB を読み書きします。前の版は 1.59.0 です。あわせて `modernc.org/libc` も 1.75.7 から 1.77.1 に上がりました。新しいドライバには第三者のライセンスの一覧（`LICENSE-3RD-PARTY.md`）が付いています。`NOTICE` にもその全文を収めました。
- 開発に加わる人向け: `deploy/public-scan.sh` は内部の名前の一覧を公開物の外から読みます。公開リポジトリの clone にはこの一覧が無いので、検査はその項目を飛ばしたことを 1 行出して残りの項目を続けます。イシューの ID の検査はこれまでどおり。説明は `CONTRIBUTING.md` にあります。

### 1.0.0 から上げるとき

- この版にマイグレーションはありません。
- 1.0.0 のサーバは正式版だけを追うので、このリリース候補を知らせません。試すなら版を指定して上げてください: `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade --version v1.0.1-rc.1`。新しい版の知らせにリリース候補も含めたいときは、`LOOPTRACK_UPDATE_CHANNEL=prerelease` を設定します。

## [1.0.0] - 2026-10-03

1.0.0 is the first release that is not a release candidate. The changes below
are relative to `v1.0.0-rc.5`; "What 1.0.0 ships" at the end of this section
is an overview of the whole server and CLI, and the sections of the release
candidates below record what changed between them. The biggest change since
rc.5 is evidence: issues can carry attached files, and closing an issue that
has verify commands needs one by default. If you upgrade a server from rc.5,
read "Moving from 1.0.0-rc.5" first. A MySQL app user with the minimum grants
needs new ones.

### Added

- **Attach evidence files to an issue.** Test output, screenshots and
  generated reports can be attached to an issue as files and kept on the
  server, with who attached them and when. From the CLI, use
  `looptrack issue attach <ID> <file>...`, or `--attach <file>` on
  `issue comment` and `issue verify`; `verify --attach-output` puts the full,
  untruncated output on the verify record. `verify --list` and `--last` can't
  be combined with `--attach` or `--attach-output`; the CLI says so and sends
  nothing. The MCP tools take no files, so an
  agent sends them with the CLI and passes the IDs in `attachments` of
  `report_verify` or `add_comment`, and `get_issue` lists them. In the browser
  the comment box takes a file you pick, drop or paste, and the drawer lists
  the attachments and shows images in place. The REST API gains
  `POST /api/v1/issues/{id}/attachments` (the body is the file, the name goes
  in `X-Looptrack-Filename`), `GET /api/v1/issues/{id}/attachments`,
  `GET /api/v1/attachments/{id}` and `POST /api/v1/attachments/{id}/purge`.
  The CLI refuses to send a text file that looks like it holds a secret, since
  the server can't mask it. Attachments can't be deleted; an administrator
  purges the file itself on the new admin page `/admin/attachments`, which
  also sets the limits (20MiB per file and 1GiB per project by default) and
  shows the usage per project.
- Attachments live on disk, so backups come in two parts. The records
  go into the database (migration 0007 adds `attachments` and
  `attachment_purges`, and `deploy/grants.sql` gives the app user SELECT and
  INSERT on both) and the files into a directory: `LOOPTRACK_ATTACH_DIR`, else
  `$STATE_DIRECTORY/attachments`, else `attachments` next to a SQLite
  database. With none of them, the server runs and only attachments are
  unavailable. install.sh adds
  `LOOPTRACK_ATTACH_DIR='/var/lib/looptrack/attachments'` to `.env` on install
  and on `--upgrade`. The compose.yaml from `looptrack setup` now mounts
  `./data:/data` with MySQL too and sets `/data/attachments`; a MySQL
  compose.yaml written by an earlier setup has no writable volume, so
  `--upgrade` leaves it alone and prints what to do: add `- ./data:/data` to
  the volumes and `LOOPTRACK_ATTACH_DIR: /data/attachments` to the
  environment, create `data` owned by 65534:65534, and run
  `docker compose up -d`. Until then only attachments are unavailable. Back up the database first and
  the attachment directory second. The backup `--upgrade` takes doesn't include
  attachments. `looptrack export` writes the attachment files to
  `<slug>/attachments/<sha256>` and a manifest to `<slug>/attachments.json`
  (file name, size, SHA-256, author, time, issue, and the comment or verify
  record it was attached to), and `looptrack verify-files` checks the files
  against the SHA-256 in the manifest. An attachment whose file can't be
  exported is listed as `missing` and `export` exits with 1. `looptrack import`
  doesn't carry attachments and says so when it finds a manifest.
  `looptrack repair-attachments` reports files missing from the directory and
  files nothing points to; `--apply` removes the latter once they are an hour
  old. A least-privilege MySQL app user needs grants on the two new tables.
  After the migration, `install.sh --upgrade` checks every table in
  `deploy/grants.sql` with the new `looptrack grants check`, and when some
  lack grants it asks for the admin credentials on the terminal and grants
  them again. On a systemd server, `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE`
  passes the password instead. You can also run `looptrack grants apply`
  yourself. Raise the body limit of the proxy in front to
  the per-file limit, since nginx's `client_max_body_size` defaults to 1MB.
  See "添付の置き場とバックアップ" in `docs/server/DEPLOY.md`.
- `looptrack grants check` tells whether the MySQL app user has every grant
  it needs. It checks each table in `deploy/grants.sql` for the user in
  `LOOPTRACK_DSN`. With every grant in place it exits with 0; otherwise it
  prints the tables and the missing privileges and exits with 3. With SQLite
  there is nothing to check, and it exits with 0. `install.sh` runs it after
  the migration, and the last check of `looptrack grants apply` looks at
  every table too.
- CLI: `looptrack self-update` updates from GitHub releases when no server
  URL is set. With neither `--url` nor `LOOPTRACK_API_URL`, it no longer
  stops with "No server URL". It picks a version from the GitHub release list
  the same way the desktop app and the server do (semver tags only, never
  `/releases/latest`, rcs too when you run an rc) and downloads the server
  archive for your OS and CPU. The archive is checked against the signed
  `SHA256SUMS`; only the `looptrack` inside is extracted, and that file is
  checked against its own line in the same `SHA256SUMS` before it replaces the
  running one (`.old` on Windows, as before). The signature is mandatory: a
  build without the public key stops without contacting GitHub, and so does any
  build with `LOOPTRACK_UPDATE_CHECK=off`. This path replaces the file only when
  a newer version exists, so it refuses `--force` (exit code 2).
  `--from github|server` picks the source explicitly, and a failure on one never
  falls back to the other. With `--url` or `LOOPTRACK_API_URL`, nothing changes.
- The web UI remembers how you left the board. The view, the search
  words and the filters (type, priority, ready only, hide closed, unanswered
  feedback, label and assignee) are kept per project in the browser's
  localStorage and come back the next time you open that project. The sort
  order was already kept. A label or an assignee the project no longer has is
  dropped when the board loads.
- The browser tab shows the Looptrack icon. Every page, the first-run
  setup page included, links the same picture as the desktop app's icon, and
  the server answers `favicon.ico` under its base path without sign-in.
- The web sign-in page has a "Keep me signed in" checkbox. It is off by
  default, and a session without it works as before: it ends 7 days after
  sign-in, or after 12 hours without use. A session with it is not ended for
  being idle. Its expiry, on the cookie and in the database, moves to 400 days
  after the last access each time the last access is recorded (every 5
  minutes); 400 days is the longest cookie lifetime Chrome accepts. The choice
  carries through the two-factor step, registering and removing two-factor
  auth on the account page, and a password change. Signing out, disabling the
  user, making two-factor auth required and an administrator revoking
  sessions all still end it. Migration 0008 adds the `persistent` column to
  `web_sessions`; the grants in `deploy/grants.sql` do not change.

### Changed

- **Closing an issue as Done needs evidence by default.** On an issue whose body
  holds at least one verify command, the server rejects Done unless the latest
  verify record for the current body carries an attachment, and it rejects a
  missing record too. This is the new per-project rule
  `verify.require_evidence`, on by default even in a project with no rules at
  all. It applies on every path (CLI, MCP, REST, Web) and can be overridden
  with a reason (`--override "reason"`, `override_reason` over MCP), which
  leaves a `rule_override` with `rule: verify_evidence_required`. A project
  that wants the old behavior sets `"verify": {"require_evidence": false}` with
  `looptrack project rules set` and gets a note instead of a rejection. The
  guide, the MCP instructions, the verify plan, the close step of `next` and the
  kit tell the AI to attach the full test output, screenshots and generated
  files.
  An issue created directly as Done with a verify command in its body is
  rejected the same way, since it can't have a record yet. A record whose
  attachment is purged later still counts as having evidence.
  Clients up to 1.0.0-rc.5 have no `--attach`, `--attach-output` or
  `issue attach`, even though the server's wording names them, so they can only
  close such an issue with an override. Update the clients first. A server older
  than this one rejects rules that contain the key, so remove it before going
  back to an older version (see the upgrade section of `docs/server/DEPLOY.md`).
- Windows: the server and client `looptrack.exe` carry version
  information. Both the amd64 and the arm64 `looptrack.exe` have a
  VERSIONINFO resource. Its ProductName is `Looptrack` and its ProductVersion
  is the build version as is, release candidates included. The release build
  reads both values back from every exe it makes and stops if either is missing
  or wrong. The tool that writes the resource changed from rsrc to go-winres
  (0BSD, build-only). `looptrack.exe` still has no icon.
- The server rejects comments with no text and no attachment. A comment
  whose text is empty or only whitespace, with no attachment, gets a 400
  (`comment_empty`) from REST, MCP, the CLI and the web UI alike. Comments
  cannot be deleted, so one empty comment would stay for good. An older client
  or your own script that sent empty comments will get that error from this version on.
  Attachment-only comments still work (over REST, send `"text": ""`). When you
  change an issue's status with a whitespace-only comment, no comment is
  created: the status changes and the response has no "Comment added" line.
- The compose.yaml from `looptrack setup` no longer names a file that isn't
  there. Its header said it was based on `deploy/compose.yaml`, which the
  repository doesn't have. That sentence is gone; the rest of the header is
  unchanged.
- The Japanese text that `init` writes, and the kit's rules and skills,
  use one consistent style. The section `looptrack issue init` puts into
  `CLAUDE.md` and `AGENTS.md`, the five rules of the loop layer and the
  `issue`, `iterate` and `session-handoff` skills were rewritten in the polite
  form throughout, with asides in parentheses turned into sentences.
  Commands, paths and line counts are unchanged, so the next init or kit
  update changes only that wording. The English working-discipline rule had
  the same asides rewritten.

### Fixed

- **The issue drawer in an English browser shows the comments heading in
  English.** Background, Details and the other sections already followed the
  screen's language, but the comments section kept its Japanese heading. The
  stored body still uses that heading as the marker that separates the body
  from the comments, so only the browser swaps it for the screen's language
  when it draws the drawer. Nothing stored changes.
- The project picker in English says "1 project" when there is one. The
  line under the heading read "1 projects". The fix uses the singular for one
  project and the plural otherwise. The Japanese line is unchanged.
- English messages say "1 issue", not "1 issues", when the count is one.
  More than 30 messages in the CLI, the REST API, the MCP tools and the
  reports printed a count of one in the plural, such as "1 issues" or "1 files".
  Each of them uses the singular for one and keeps the plural for zero and
  for two or more. Messages already written with "(s)" and all the Japanese text are
  unchanged.
- AI agent hooks: the guards catch commands hidden behind a heredoc,
  `$'…'` or a comment, and decide on long commands too. The git, secrets,
  wait-loop and scope guards dropped the line after a `<<` that the shell
  does not read as a heredoc: part of `<<<`, inside quotes or a comment, or
  inside `$((…))` and `${…}`. They also dropped the body of `cat <<EOF | sh`
  and `bash <<EOF`, which the shell runs, and lost track of quotes after
  `$'…'` or an apostrophe in a comment. The scope guard didn't look past a
  pipe with no space around it (`echo a|git …`). In each case a real command
  went through unchecked. Each guard reads a command in several more ways and
  stops it when any of them matches, so nothing it stopped before goes through.
  Unwrapping a nested shell took time in proportion to the number of quotes
  times the length, so a command of 20 to 30 KB reached the 4-second limit
  and went through with no decision. After the rewrite, a 300 KB command was
  decided in under a second and a 1 MB one in 2.3 seconds on the machine it
  was measured on; a command that still runs past the limit goes through, as
  before. The body of a heredoc whose terminator is quoted with a backslash
  (`<<\EOF`) is still checked word by word, so if that stops you, write
  `<<'EOF'`.
- AI agent hooks: the handoff hook no longer misses a `close`. The hook
  that notices finished work, so that the handoff check asks for an update,
  skipped a `close` on the line after a comment, after a `<<` inside quotes
  or arithmetic, or in the body of `cat <<EOF | sh`. It reads heredocs and
  comments the same way as the guards do.
- `looptrack handoff -h` and `--help` print the usage. `handoff append -h`
  used to append `-h` to the handoff and succeed. `-h` and `--help`, before or
  after the subcommand, print the usage and write nothing, and a word like
  `-x` is rejected as a mistyped option. Anything after `--` still goes in as
  text.
- In a clone of Looptrack's own repository, setup and `init` no longer end
  in an error. There, `init` used to fail, so the setup tool's
  download-and-init command ended in an error even though `looptrack` had
  already been replaced. For that repository the setup tool returns only the
  download step, and `init` writes nothing, says it isn't needed and
  exits with 0. `doctor` no longer suggests running it.
- `init` writes the guidance section in CLAUDE.md and AGENTS.md in English
  for English users. The section went in Japanese whatever the language. Instead, it
  follows the language of the rest of `init`'s output, read from
  `LOOPTRACK_LANG`, `LC_ALL`, `LC_MESSAGES` and `LANG` in that order. Running
  `init` again in the other language replaces the section rather than adding
  a second one, and anything written outside it stays.
- The `compose.yaml` that setup writes no longer pulls an image of the same
  name from a registry. With Compose v5.5.1, `docker compose up` tried to
  pull `looptrack:latest` before building, although the comment in the file
  said that never happens. The file sets `pull_policy: build`, so the
  image is always built from the Dockerfile next to it. On a server you set
  up earlier, add `pull_policy: build` under `build:` in `compose.yaml`.
- The admin project list in English reads "your role here: admin". It
  said "you are a admin here", because the article was wrong for any role
  that starts with a vowel. The Japanese line is unchanged.

### Docs

- **Windows: what to do when it blocks the unsigned binaries outright.**
  Getting started, the README and the FAQ cover Smart App Control on Windows 11,
  which blocks unsigned apps with no "Run anyway" and no way to allow a single
  app, and PCs whose administrator blocks unsigned apps by policy. The FAQ also
  points to checking `SHA256SUMS` itself against `SHA256SUMS.minisig`.
- Screenshots in the user guide and the README. Getting started for the
  desktop app and the server, the daily-use page and the README show the
  screens they describe, in the language of each page. Every picture is taken
  by `deploy/dev/screenshots.sh` from a throwaway server that holds only
  made-up data.
- The user guide has one entrance per edition. The pages for the server
  are under `docs/guide/server/` and those for the desktop app under
  `docs/guide/desktop/`; the desktop page is split into getting started,
  using, updating and troubleshooting. The old pages (`desktop.md`,
  `getting-started.md`, `admin.md` and `updating.md`) remain as short pages
  that link to the new ones, so links in installers and READMEs up to rc.5
  still work.
- Two changelogs. Changes for the server are in `CHANGELOG.md` and those
  for the desktop app in `CHANGELOG-desktop.md`, and a change that affects
  both is in both. A GitHub release takes the same version from both files
  and shows them under "Server" and "Desktop app".

### Moving from 1.0.0-rc.5

- This version adds migration 0007 (`attachments` and `attachment_purges`)
  and migration 0008 (a `persistent` column on `web_sessions`). A MySQL app
  user with the minimum grants needs SELECT and INSERT on the two new tables;
  0008 needs no new grant, because the app user already has UPDATE on
  `web_sessions`.
  After the migration, `--upgrade` checks the grants on every table and, if
  any are missing, asks for the admin credentials on the terminal and grants
  them again; on a systemd server `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE`
  can pass the password. `looptrack grants check` shows what is missing, and
  `looptrack grants apply` grants it.
- On MySQL, run the `--upgrade` from a terminal. The automatic upgrade (the
  timer of `--auto-upgrade on`) doesn't replace a version that adds
  migrations on MySQL, so it leaves the server on rc.5. With the minimum
  grants the app user can't create the tables of 0007 or run the ALTER TABLE
  of 0008, so pass a connection that can in `LOOPTRACK_SETUP_MIGRATE_DSN`.
  Without it, `--upgrade` lists the pending migrations and asks whether to
  stop the service and go on. The default answer is no, and `--yes` takes the
  default, so it ends without changing anything. `sudo` drops environment variables; "更新（--upgrade）"
  in `docs/server/DEPLOY.md` shows how to pass it.
- Attachments need a directory. On a systemd server, `install.sh --upgrade`
  adds `LOOPTRACK_ATTACH_DIR` to `.env`. With a `compose.yaml` that has no
  place for them (a MySQL one written by an earlier setup), `--upgrade`
  prints the steps: two lines to add, then `mkdir` and `chown` for `data`.
- Raise the body limit of the proxy in front to the per-file limit (20MiB by
  default). nginx's `client_max_body_size` is 1MB unless you set it; the
  nginx example that install.sh prints already sets
  `client_max_body_size 20m;`.
- Update the clients first. Clients up to rc.5 can't attach files, so they
  can close an issue with verify commands only with an override. A project
  that wants the old behavior sets `"verify": {"require_evidence": false}`.
- Back up the database first and the attachment directory second. The backup
  that `--upgrade` takes doesn't include attachments.

### What 1.0.0 ships

- **Cross-project issue tracking.** One server holds the issues of many projects.
  Each project has its own ID prefix and zero-padded width (`MYP-0001`), its own
  rules and its own guide document. The database is the source of truth; nothing
  is committed into the source repositories being tracked.
- Loop-shaped workflow. `next` picks the issue to work on and enforces the
  rules for starting it; `verify` runs the commands written in the issue and
  records the result; `summary` reports three nested loops at the start of every
  session (the current round, decisions waiting for a human, feedback coming from
  outside). Comments and events are append-only, and closed issues are immutable.
- Per-project rules enforced by the server. Required comments, forbidden
  status transitions and `verify.require_on_close` apply to every access path
  (REST, MCP, CLI, Web) because they are checked in one place.
- REST API at `/looptrack/api/v1` with per-user access tokens (expiry,
  revocation, last-used time).
- Remote MCP server at `/looptrack/mcp`, reachable with OAuth 2.1 or a token,
  including a `setup` tool that wires a project up from the MCP connection alone,
  and `guide` / `review` / `loop` prompts.
- Web UI: project picker, board / list / trace views, forms to create
  issues, change their status, comment, assign them and attach files,
  account settings (tokens, password), and user administration.
  Sign-in is ID + password (argon2id) with TOTP, required or optional per server.
  A "Keep me signed in" checkbox on the sign-in page ends the idle timeout.
- CLI (`looptrack issue`): create, edit, comment, status, close, list, show,
  next, verify, summary, guide, export, and `init` to wire a project up in one
  command. `looptrack issue login --browser` signs in through the browser and
  refreshes the token from then on.
- Hooks and the distribution kit. The `core` layer (session summary, issue
  freshness guard, token accounting) is always installed; the optional `loop`
  layer adds output/working/iteration discipline rules, task-mode and scope
  guards, handoff freshness and a runaway-process guard. Wiring is described by
  a manifest and generated for Claude Code, Codex and GitHub Copilot.
- Token accounting. Usage is recorded per session, per agent and per stage of
  the loop, attributed to issues, and rendered as a PDF report
  (`looptrack report pdf`) with an embedded font.
- Setup and installation. `looptrack setup` asks a handful of questions and
  writes `.env` (and `compose.yaml` for a team server) plus the first
  administrator. `deploy/install.sh` (POSIX sh) does fetch → setup → start →
  health check on a fresh Linux server, prints nginx and Caddy examples, and also
  handles `--upgrade` and `--uninstall`. A Dockerfile and a Compose file are
  provided as well.
- Local mode. A single-user server bound to 127.0.0.1 with SQLite and no
  sign-in, for people who do not want to run a shared server. The desktop app
  (see its own changelog) is built on it.
- Signed release artifacts. `SHA256SUMS` signed with minisign, macOS binaries
  signed with a Developer ID and notarized, and `looptrack self-update` verifying
  the signature before replacing the binary. Every artifact ships a `NOTICE` with
  the full license texts of the third-party components.
- Markdown export. `looptrack export` writes issues back out as Markdown, so
  the data is never locked in.
- Worktree housekeeping. `looptrack worktree list` shows every git worktree
  with what you need to decide on it: merged or not, uncommitted changes
  (ignoring build leftovers), locked, and when it last moved.
  `looptrack worktree prune` cleans up. It never deletes anything unless you pass
  `--yes`, never touches the main worktree, and keeps anything that moved
  recently, because another session may still be inside it.

---

1.0.0 はリリース候補ではない最初の版です。下の変更は `v1.0.0-rc.5` からの差分で、この節の最後の「1.0.0 の全体像」にサーバと CLI の全体をまとめました。rc の間に何が変わったかは下の各 rc の節にあります。rc.5 からのいちばん大きな変更はエビデンスの添付です。イシューにファイルを添付できるようになり、検証コマンドのあるイシューを Done にするには既定で添付が要ります。rc.5 のサーバを上げるなら、先に「1.0.0-rc.5 から上げるとき」を読んでください。MySQL のアプリ用の利用者を最小権限にしているなら、新しい権限が要ります。

### 追加

- **イシューにエビデンスのファイルを添付できます。** テストの出力・画面のスクリーンショット・生成したレポートを、ファイルのままイシューに付けてサーバに残せるようになりました。誰がいつ付けたかも残ります。CLI では `looptrack issue attach <ID> <ファイル>...` か `issue comment`・`issue verify` の `--attach <ファイル>` を使います。`verify --attach-output` は切る前の出力の全文を verify の記録に付けます。`verify --list`・`--last` と `--attach`・`--attach-output` は一緒に使えず、CLI はそう伝えて何も送りません。MCP のツールはファイルを受け取らないので、AI は CLI で送って返った ID を `report_verify` か `add_comment` の `attachments` に渡します。添付の一覧は `get_issue` にも載ります。ブラウザではコメントの欄でファイルを選ぶ・ドロップする・貼り付けるのどれでも添付できます。ドロワーに一覧が出て画像はその場で見られます。REST には `POST /api/v1/issues/{id}/attachments`（本文はファイルそのもので、名前は `X-Looptrack-Filename`）・`GET /api/v1/issues/{id}/attachments`・`GET /api/v1/attachments/{id}`・`POST /api/v1/attachments/{id}/purge` が増えました。テキストのファイルに秘密らしいものがあれば、CLI は送らずに止まります。サーバでは中身を隠せないからです。添付は消せません。管理者が新しい画面 `/admin/attachments` で本体を消去します。同じ画面で上限（既定は 1 ファイル 20MiB・1 プロジェクト 1GiB）を変え、プロジェクトごとの使用量も見られます。
- 添付の本体はディスクに置くので、バックアップが 2 系統になります。記録は DB に入ります。migration 0007 が `attachments` と `attachment_purges` を作り、`deploy/grants.sql` はアプリ用の利用者に 2 つの表の SELECT・INSERT だけを与えます。本体の置き場は `LOOPTRACK_ATTACH_DIR` です。無ければ `$STATE_DIRECTORY/attachments` に、それも無ければ SQLite の DB の隣の `attachments` に置きます。どれにも当たらないとき、サーバは動いたまま添付だけが使えません。install.sh は入れるときと `--upgrade` のときに `.env` へ `LOOPTRACK_ATTACH_DIR='/var/lib/looptrack/attachments'` を足します。`looptrack setup` の compose.yaml は MySQL でも `./data:/data` を入れ、置き場を `/data/attachments` に決めるようになりました。以前の setup が書いた MySQL の compose.yaml には書ける volume が無いので、`--upgrade` はそのファイルに触らずに直し方を示します。volumes への `- ./data:/data` と environment への `LOOPTRACK_ATTACH_DIR: /data/attachments` を足し、`data` を作って 65534:65534 の持ち物にしてから `docker compose up -d` を実行する手順です。それまでは添付だけが使えません。控えは DB を先に取ってから添付の置き場を取ります。`--upgrade` が取る控えに添付は入りません。`looptrack export` は添付の本体を `<slug>/attachments/<sha256>` に書き出します。目録の書き出し先は `<slug>/attachments.json` です。目録にはファイル名・大きさ・SHA-256・作者・日時・イシューと、どのコメントや verify の記録に付いたかが入ります。`looptrack verify-files` は、本体が目録の SHA-256 と一致するかを確かめます。書き出せなかった添付は目録に `missing` として載り、`export` は終了コード 1 で終わります。`looptrack import` は添付を運びません。目録があればそう知らせます。`looptrack repair-attachments` は置き場から欠けた本体と、どこからも指されない本体を報告します。`--apply` を付ければ、後者のうち書かれて 1 時間を過ぎたものを消します。MySQL のアプリ用の利用者を最小権限にしているなら、2 つの新しい表の権限が要ります。`install.sh --upgrade` は migrate の後に、新しい `looptrack grants check` で `deploy/grants.sql` の全部の表を確かめます。権限の足りない表があれば、管理用の資格情報を端末で尋ねて与え直します。systemd のサーバでは `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE` でパスワードを渡せば尋ねません。`looptrack grants apply` を自分で実行してもかまいません。前段のプロキシの本文の上限も 1 ファイルの上限まで上げてください。nginx の `client_max_body_size` は既定で 1MB です。手順は `docs/server/DEPLOY.md` の「添付の置き場とバックアップ」にあります。
- `looptrack grants check` で、MySQL のアプリ用の利用者に要る権限がそろっているかを確かめられます。`LOOPTRACK_DSN` の利用者について `deploy/grants.sql` の全部の表の権限を見ます。そろっていれば終了コード 0、足りなければ表と足りない権限を出して 3 で終わります。SQLite では確かめるものが無く 0 で終わります。`install.sh` は migrate の後にこれを使い、`looptrack grants apply` の最後の確かめも全部の表を見ます。
- CLI: サーバの URL が無くても、`looptrack self-update` が GitHub のリリースから更新します。`--url` も `LOOPTRACK_API_URL` も無いときに「サーバの URL がありません」で止まらなくなりました。版の選び方はデスクトップ版やサーバと同じです。semver のタグだけを見て `/releases/latest` は使わず、rc を動かしていれば rc も追います。取るのは自分の OS と CPU に合うサーバ版の書庫で、署名つきの `SHA256SUMS` で照合したうえで中の `looptrack` だけを取り出します。置き換える前には同じ `SHA256SUMS` にある自分の行とも照らし合わせます。Windows ではこれまでどおり `.old` に退けます。署名は必須です。公開鍵を持たないビルドは GitHub に問い合わせずに止まり、`LOOPTRACK_UPDATE_CHECK=off` のビルドも問い合わせません。この経路は新しい版があるときだけ置き換えるので、`--force` を受け付けません（終了コード 2）。取得元は `--from github|server` で明示でき、片方で失敗してももう一方へは切り替えない作りです。`--url` か `LOOPTRACK_API_URL` があるときの動きは変わりません。
- Web の画面がボードを前回の表示のまま開きます。表示形式・検索語・絞り込み（型・優先度・着手可能・クローズ済みを隠す・未応答の反応・ラベル・担当）を、プロジェクトごとにブラウザの localStorage に残します。次にそのプロジェクトを開くと残した条件で表示します。並び順は以前から残っていました。プロジェクトに無くなったラベルや担当は、ボードを読み込んだときに外します。
- ブラウザのタブに Looptrack のアイコンが出ます。初回設定の画面も含めたすべてのページが、デスクトップ版のアイコンと同じ絵を指します。ベースパスの下の `favicon.ico` もサインインなしで返します。
- Web のログイン画面に「ログインしたままにする」のチェックボックスが付きました。既定はチェックなしです。付けないセッションはこれまでどおりログインから 7 日か無操作 12 時間で切れます。付けたセッションは無操作では切れません。期限は Cookie と DB の両方で、最終アクセスを記録するたび（5 分おき）に最終アクセスから 400 日へ延びます。400 日は Chrome が受け付ける Cookie の期限の上限です。チェックは二段階認証の段・アカウント画面の TOTP の登録と解除・パスワードの変更でも引き継ぎます。ログアウト・利用者の無効化・二段階認証の必須化・管理者による破棄では、これまでどおり切れます。migration 0008 が `web_sessions` に `persistent` の列を足します。`deploy/grants.sql` の権限は変わりません。

### 変更

- **イシューを Done にするには既定でエビデンスが要ります。** 本文に検証コマンドが 1 つ以上あるイシューでは、いまの本文に対する最新の verify の記録に添付が無いとサーバが Done を拒否します。記録そのものが無いときも拒否です。検証コマンドを書いた本文を Done のまま起票しても、まだ記録が無いので同じく拒否されます。記録の後で添付を消去してもその記録は添付ありとして数えます。新しいプロジェクト別ルール `verify.require_evidence` の働きで、ルールを何も設定していないプロジェクトでも既定で入っています。CLI・MCP・REST・Web のどの経路にも効き、理由を付ければ上書きできます（`--override "理由"`。MCP は `override_reason`）。上書きすると `rule: verify_evidence_required` の `rule_override` が残ります。これまでの動きのままにしたいプロジェクトは、`looptrack project rules set` で `"verify": {"require_evidence": false}` を設定してください。拒否の代わりに注意だけが返ります。guide・MCP の案内文・verify の計画の文面・`next` の close の段・kit も、テストの出力の全文やスクリーンショット、生成物を添付するよう AI に指示するようになりました。
  1.0.0-rc.5 までのクライアントには `--attach`・`--attach-output`・`issue attach` がありません。サーバの文面はこれらを案内するのに旧版では使えず、この網の掛かるイシューは上書きでしか Done にできない状態です。クライアントを先に更新してください。このキーを知らない旧版のサーバは、キーを含む rules を拒否します。前の版に戻すときは先にキーを外してください（`docs/server/DEPLOY.md` の更新の節）。
- Windows: サーバ版とクライアントの `looptrack.exe` が版の情報を持つように。amd64・arm64 の `looptrack.exe` に VERSIONINFO の資源を入れました。ProductName は `Looptrack` です。ProductVersion はビルドの版そのままで、rc でも入ります。リリースのビルドは作った exe から 2 つの値を読み戻し、無いか違えば止まります。資源を書く道具は rsrc から go-winres（0BSD。ビルドにだけ使う）に替えました。`looptrack.exe` にはこれまでどおりアイコンを入れません。
- サーバが本文の無いコメントを 400 で断るようになりました。本文が空か空白だけで添付も無いコメントは、REST・MCP・CLI・Web のどこから送っても `comment_empty` の 400 になります。コメントは消せないので、空のものが 1 件入ると残り続けるからです。旧版のクライアントや自作のスクリプトが空のコメントを送っていたなら、エラーが返ります。添付だけのコメントはこれまでどおり通ります（REST では `"text": ""` を付けてください）。状態の変更に空白だけのコメントを付けたときは、コメントを作らずに状態だけが変わります。応答に「コメント追記」の行は出ません。
- `looptrack setup` が書き出す compose.yaml の見出しが、無いファイルを名乗らなくなりました。見出しには「deploy/compose.yaml を元にした雛形」とありましたが、リポジトリにそのファイルはありません。外したのはこの 1 文だけで、見出しのほかの行は変えていません。
- `init` が書く日本語と、kit の rules・skill の日本語の文体をそろえました。対象は `looptrack issue init` が `CLAUDE.md`・`AGENTS.md` に入れる案内節と、loop の層の rules 5 本と skill の `issue`・`iterate`・`session-handoff` です。地の文は です・ます にし、括弧の補足は本文の文に直しました。コマンド・パス・行数は変えていません。次に init や kit を更新したときに変わるのは文面だけ。英語の working-discipline の rules も、同じ箇所の括弧の補足を文に直しています。

### 修正

- **英語の画面ではイシューの詳細のコメント節も英語の見出しで描きます。** 背景・内容などの節は画面の言語に合わせて出ていましたが、コメント節の見出しだけが日本語のままでした。保存した本文ではこの見出しは本文とコメントを分ける目印なので訳しません。ブラウザが画面の言語の見出しに替えるのは描くときだけです。保存した内容は変わりません。
- 英語のプロジェクト選択の画面: 1 件なら「1 project」と単数に。見出しの下の行が「1 projects」になっていました。1 件のときは単数、それ以外は複数で出します。日本語の行は変わりません。
- 英語の文言で件数が 1 のときに単数で出るようになりました。CLI・REST・MCP・帳票の英語の文言のうち 30 件あまりが、「1 issues」「1 files」のように 1 件でも複数形で出ていました。1 件のときは単数にし、0 件と 2 件以上は複数のままです。日本語の文面と、「(s)」で書いていた文言には手を入れていません。
- AI エージェントの hook: ヒアドキュメント・`$'…'`・コメントの陰に置いたコマンドをガードが見落とさず、長いコマンドも判定します。git・秘密・待ちループ・scope の 4 つのガードは、シェルがヒアドキュメントと読まない `<<` の次の行を落としていました。`<<<` の一部・引用符やコメントの中・`$((…))` や `${…}` の中の `<<` がこれに当たります。シェルが実行する `cat <<EOF | sh`・`bash <<EOF` の本文も落とし、`$'…'` やコメントの中のアポストロフィの後ろでは引用符の組を見失っていました。scope のガードは空白を挟まないパイプ（`echo a|git …`）の後ろを見ていません。どれも本物のコマンドが確かめられずに通る形です。ガードには読み方を足し、どれか 1 つで当たれば止める形にしました。以前に止めていた形が通るようにはなっていません。入れ子のシェルをほどく段は引用符の数と長さの積に比例して遅くなっていました。20〜30 KB のコマンドで 4 秒の打ち切りに届き、判定しないまま通していたわけです。書き直した後の実測では 300 KB のコマンドを 1 秒未満で、1 MB を 2.3 秒で判定しました。打ち切りを越えたコマンドはこれまでどおり通します。終端をバックスラッシュで引用したヒアドキュメント（`<<\EOF`）の本文は、いまも語として判定します。それで止まったら `<<'EOF'` に書き換えてください。
- AI エージェントの hook: 引き継ぎの hook が `close` を取りこぼさなくなりました。終わった作業に気づき、引き継ぎの更新を求めさせる hook のことです。コメントの次の行・引用符や算術の中の `<<` の後ろ・`cat <<EOF | sh` の本文にある `close` を数えていませんでした。ヒアドキュメントとコメントの読み方は、ガードと同じものにそろえました。
- `looptrack handoff -h`・`--help` が使い方を出します。これまでの `handoff append -h` は `-h` を本文として引き継ぎに足して、成功で終わっていました。`-h`・`--help` はサブコマンドの前でも後でも使い方を出すだけで、何も書きません。`-x` のような語は選択肢の打ち間違いとして止めます。`--` の後ろの語はこれまでどおり本文です。
- Looptrack 自身のリポジトリの clone で、setup と `init` がエラーで終わらなくなりました。そこでは `init` が失敗していたので、setup ツールの取得と init のコマンドは `looptrack` を置き換えた後でエラーになっていました。そのリポジトリに対して、setup ツールは取得の段だけを返します。`init` は何も書かず、要らない旨を出して終了コード 0 で終わります。`doctor` も init を勧めません。
- `init` が CLAUDE.md・AGENTS.md に書く案内の節が、英語の利用者には英語で入ります。言語に関わらず日本語だった節を `init` のほかの出力と同じ言語で選ぶようにしました。言語を見る順は `LOOPTRACK_LANG`・`LC_ALL`・`LC_MESSAGES`・`LANG` です。別の言語で打ち直しても節は 2 つにならず、丸ごと差し替わります。節の外に書いた本文は残ります。
- setup が書く `compose.yaml` は同じ名前のイメージをレジストリから引かなくなりました。Compose v5.5.1 では、`docker compose up` がビルドの前に `looptrack:latest` を引きにいきました。ファイルの注釈は引くことはないと書いていました。ファイルに `pull_policy: build` を入れたので、いつも隣の Dockerfile からイメージを作ります。以前に setup したサーバでは `compose.yaml` の `build:` の下に `pull_policy: build` を足してください。
- 英語の管理画面のプロジェクト一覧が、「your role here: admin」と出ます。以前は「you are a admin here」で、母音で始まる役割では冠詞が誤っていました。日本語の行は変わりません。

### 文書

- **Windows が署名の無い版をそのまま止めるときの案内。** 始め方・README・FAQ に、Windows 11 のスマート アプリ コントロールと、管理者が方針で署名の無いアプリを止めている PC のことを書きました。スマート アプリ コントロールには「実行」の道も、アプリごとに許可する手段もありません。FAQ からは `SHA256SUMS` 自体を `SHA256SUMS.minisig` で確かめる手順にも案内します。
- 利用者ガイドと README にスクリーンショットを入れました。デスクトップ版とサーバ版の始め方・日々の使い方・README に、説明している画面の画像をページの言語に合わせて載せています。画像はどれも架空のデータだけを入れた使い捨てのサーバから `deploy/dev/screenshots.sh` で撮ったものです。
- 利用者ガイドの入口を版ごとに分けました。サーバ版のページは `docs/guide/server/`、デスクトップ版のページは `docs/guide/desktop/` にあります。デスクトップ版のページは始め方・使い方・更新・困ったときの 4 本に分けました。元のページ（`desktop.md`・`getting-started.md`・`admin.md`・`updating.md`）は移った先へのリンクだけを持つ短いページとして残しています。rc.5 までのインストーラや README のリンクも切れません。
- 変更履歴は 2 本です。サーバ版の変更は `CHANGELOG.md` に、デスクトップ版の変更は `CHANGELOG-desktop.md` に書きます。両方に効く変更は両方に載せます。GitHub のリリースの本文は 2 本の同じ版の節を「Server」「Desktop app」の 2 節に並べたもの。

### 1.0.0-rc.5 から上げるとき

- この版はマイグレーション 0007（`attachments`・`attachment_purges`）と 0008（`web_sessions` の `persistent` の列）を足します。MySQL のアプリ用の利用者を最小権限にしているなら、新しい 2 つの表の SELECT・INSERT が要ります。0008 に新しい権限は要りません。アプリ用の利用者は `web_sessions` の UPDATE を既に持っているからです。`--upgrade` は migrate の後に全部の表の権限を確かめ、足りなければ管理用の資格情報を端末で尋ねて与え直します。systemd のサーバならパスワードは `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE` でも渡せます。足りない権限は `looptrack grants check` で分かります。与えるのは `looptrack grants apply` です。
- MySQL では `--upgrade` を端末で実行してください。自動の置き換え（`--auto-upgrade on` の timer）は、MySQL でマイグレーションを足す版を置き換えません。サーバは rc.5 のまま残ります。最小権限のアプリ用の利用者は 0007 の表を作れず、0008 の ALTER TABLE も実行できないので、表を作れる接続先を環境変数 `LOOPTRACK_SETUP_MIGRATE_DSN` で渡します。渡さずに上げると `--upgrade` は未適用のマイグレーションを示し、サービスを止めて続けるかを尋ねます。既定の答えは「続けない」です。`--yes` でもこの答えになり何も変えずに止まります。`sudo` は環境変数を落とすので、渡し方は `docs/server/DEPLOY.md` の「更新（--upgrade）」を見てください。
- 添付には置き場が要ります。systemd のサーバなら `install.sh --upgrade` が `.env` に `LOOPTRACK_ATTACH_DIR` を足します。置き場の無い `compose.yaml`（以前の setup が書いた MySQL のもの）では、`--upgrade` が直し方を示します。足す 2 行と `data` の `mkdir`・`chown` の手順です。
- 前段のプロキシの本文の上限を 1 ファイルの上限（既定は 20MiB）まで上げます。nginx の `client_max_body_size` は設定しなければ 1MB です。install.sh が示す nginx の例には `client_max_body_size 20m;` が入っています。
- クライアントを先に更新してください。rc.5 までのクライアントはファイルを添付できず、検証コマンドのあるイシューを上書きでしか Done にできません。これまでの動きのままにしたいプロジェクトは、`"verify": {"require_evidence": false}` を設定します。
- 控えは DB を先に取ってから添付の置き場を取ります。`--upgrade` が取る控えに添付は入りません。

### 1.0.0 の全体像

- **プロジェクト横断のイシュー管理。** 1 つのサーバが多くのプロジェクトのイシューを持ちます。プロジェクトごとに ID のプレフィックスとゼロ埋めの桁数（`MYP-0001`）・ルール・ガイドの文書を持ちます。正はデータベースで、管理対象のソースのリポジトリには何もコミットしません。
- ループの形をした進め方。`next` は取り組むイシューを選んで着手の規則を守らせます。`verify` はイシューに書いたコマンドを実行して結果を記録します。`summary` は毎セッションの初めに、入れ子になった 3 つのループを報告します。いまの回・人の判断を待つ決定・外から届くフィードバックの 3 つです。コメントとイベントは追記のみで、閉じたイシューは変更できません。
- サーバが強制するプロジェクト別のルール。必須のコメント・禁止した状態の遷移・`verify.require_on_close` は 1 か所で確かめるので、REST・MCP・CLI・Web のどの経路にも効きます。
- REST API。`/looptrack/api/v1` にあり、利用者ごとのアクセストークン（期限・失効・最後に使った時刻）を使います。
- リモートの MCP サーバ。`/looptrack/mcp` にあり OAuth 2.1 かトークンでつなげます。MCP の接続だけでプロジェクトを配線できる `setup` ツールと、`guide` / `review` / `loop` のプロンプトもあります。
- Web UI: プロジェクトの選択とボード / 一覧 / トレースの表示。イシューの起票・状態の変更・コメント・担当の変更・ファイルの添付。アカウントの設定（トークン・パスワード）と利用者の管理。サインインは ID とパスワード（argon2id）に TOTP を組み合わせ、TOTP を必須にするか任意にするかはサーバごとに決めます。ログイン画面の「ログインしたままにする」を付けると、無操作では切れません。
- CLI（`looptrack issue`）: 作成・編集・コメント・状態・close・一覧・表示・next・verify・summary・guide・export と、プロジェクトを 1 つのコマンドで配線する `init`。`looptrack issue login --browser` はブラウザでサインインし、以後はトークンを更新し続けます。
- hook と配布の kit。`core` の層（セッションの要約・イシューの鮮度ガード・トークンの集計）は必ず入ります。任意の `loop` の層は出力・作業・イテレーションの規律の rules を足します。タスクモードと範囲のガード・引き継ぎの鮮度・暴走プロセスのガードもこの層が足します。配線はマニフェストで記述し、Claude Code・Codex・GitHub Copilot 向けに生成します。
- トークンの集計。使用量をセッション・エージェント・ループの段階ごとに記録し、イシューに帰属させます。結果はフォントを埋め込んだ PDF のレポート（`looptrack report pdf`）になります。
- セットアップとインストール。`looptrack setup` がいくつかの質問をして、`.env`（チームのサーバなら `compose.yaml` も）と最初の管理者を書き出します。`deploy/install.sh`（POSIX sh）はまっさらな Linux のサーバで取得 → setup → 起動 → ヘルスチェックまでを行い、nginx と Caddy の例を示します。`--upgrade` と `--uninstall` も扱います。Dockerfile と Compose のファイルも用意しています。
- ローカルモード。127.0.0.1 に結び付けた、SQLite を使うサインインなしの 1 人用のサーバです。共有のサーバを動かしたくない人に向けたもので、デスクトップ版もこの上に作ってあります。
- 署名したリリースの成果物。`SHA256SUMS` は minisign で署名し、macOS のバイナリは Developer ID で署名して公証を受けています。`looptrack self-update` はバイナリを入れ替える前に署名を確かめます。どの成果物にも第三者のコンポーネントのライセンスの全文を収めた `NOTICE` が付きます。
- Markdown へのエクスポート。`looptrack export` がイシューを Markdown に書き戻すので、データが閉じ込められることはありません。
- 作業ツリーの片付け。`looptrack worktree list` はすべての git の作業ツリーを並べ、それぞれについて決めるのに要ることも一緒に出します。マージ済みかどうか・未コミットの変更（ビルドの残りは無視する）・ロック・最後に動いた時刻です。片付けは `looptrack worktree prune` で行います。`--yes` を渡さない限り何も消さず、本体の作業ツリーには決して触れません。ほかのセッションがまだ中にいるかもしれないので、最近動いたものは残します。

## [1.0.0-rc.5] - 2026-10-01

Fifth release candidate for 1.0.0, relative to `v1.0.0-rc.4`. Most of it is
about how the working environment of a server user gets and keeps an up-to-date
`looptrack`, and about how `install.sh` upgrades a server. If you upgrade a
server from rc.4, run the one-line `--upgrade` once by hand. "Moving from
1.0.0-rc.4" at the end of this section has the line and the other steps.

### Fixed

- **AI agent setup: one command fixes an out-of-date installation, and it
  always points at your server.** When the `looptrack` or the kit in a working
  environment was out of date, the setup tool used to return
  `looptrack self-update && looptrack issue init --project <slug> --agent <agent>`
  with no `--url`. If the loop choice had been answered, a second init with
  `--url` followed, so init ran twice. The "[Update the distributed files]"
  notice (at session start and in MCP tool results) suggested the same init
  without `--url`. Run in a terminal without `LOOPTRACK_API_URL`, that init
  pointed at the CLI's default: the local mode. The setup tool returns a single
  command instead. It downloads `looptrack`, verifies its SHA-256 and runs init
  with `--url`, `--source server` and `--dist` (plus `--loop` / `--no-loop`
  when the loop choice was answered). The notice tells you to call the setup
  tool and shows no command. The optional check after it passes the server URL
  and the project to the `looptrack` it placed, rather than relying on the
  agent's environment. No command in the setup result or the notices uses a
  bare `looptrack` from the PATH any more: the loop step, the "declined" hint,
  the token-paste hint and the `doctor` hint all use the absolute path of the
  `looptrack` it placed.
- AI agent setup replaces a `looptrack` that differs from the one your
  server distributes. It used to keep any `looptrack` already in
  `~/.local/bin` (on Windows, `%LOCALAPPDATA%\Programs\looptrack`) and only run
  init with it. The download is skipped only when the file there has the same
  SHA-256 as the distributed one; anything else is downloaded, verified and
  swapped in. A symbolic link there is replaced, and the file it points to is
  left alone. If the desktop app also lives on the machine, its symbolic link
  there (on Windows, its marked copy) is one of the files that differ, so the
  server setup replaces it with the distributed one.
- Setup commands for Codex and Copilot no longer leave environment variables
  in your shell. Some setup commands pass `LOOPTRACK_API_URL` and
  `LOOPTRACK_PROJECT`: every command for Codex and Copilot, and the PowerShell
  command that reports an `other` agent as installed. They set them with
  `export` in sh or `$env:` in PowerShell. Pasting one into a terminal left them
  set, and a later `looptrack` in another project silently pointed at this one.
  Three changes fix that: a chained command exports them inside a subshell, a
  single command takes them as a plain prefix
  (`LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=… …`), and PowerShell restores the
  previous values when the command ends (saved under names that the wrapped
  command does not use).
- The setup download command leaves nothing in your shell and cleans up
  after a failed download. It was not wrapped for any agent. Pasted into a
  terminal, it left the variables `U`, `S` and `D` and a `sum` function hiding
  `/usr/bin/sum` in sh, and `$ErrorActionPreference`, `$ProgressPreference`,
  `$U` and the other variables in PowerShell. It is wrapped for every agent: in
  a subshell in sh, in `& { }` in PowerShell. If the download, the SHA-256
  check or the swap fails in sh, the `.part` file is removed, as PowerShell
  already did.
- The guide, `init`, `doctor`, `self-update` and the account page no longer
  send a server user to an init without `--url` or to a `looptrack` on the
  PATH. The guide used to say to add loop with `looptrack issue init --loop`;
  it says to call the MCP setup tool instead. After a server install (`--url`
  or `--source server`) that declined loop, `init` shows the command to add it
  with the absolute path of the running `looptrack`, `--project`, `--url`,
  `--agent` and `--source server`. `doctor` (for an install whose kit came from
  the server) and `self-update` point to the setup tool instead of re-running
  init or `self-update`, and the sign-in hint in `doctor` uses the running
  `looptrack`. The account page shows the sign-in commands with the absolute
  path where setup places `looptrack`, for macOS and Linux and for Windows.
  Init errors about a broken JSON file say to run the same init command again.
- AI agent setup adds the place of `looptrack` to your PATH. Skills, rules,
  hooks and MCP messages call `looptrack` by name, yet a server user had no
  `looptrack` on the PATH: setup placed it in `~/.local/bin` (on Windows,
  `%LOCALAPPDATA%\Programs\looptrack`) without adding that place to the PATH.
  The download-and-init command adds it when it is not on the PATH yet. On
  macOS and Linux that means one line in the startup file of your default shell
  (`~/.zshenv` for zsh, also under `ZDOTDIR` when it is set, `~/.bashrc` plus
  the file a bash login shell reads for bash, `conf.d/looptrack.fish` for fish,
  `~/.profile` otherwise). On Windows the place goes into your user environment
  variable `Path`, without `setx` and keeping the kind of the existing value.
  Nothing is written when it is already there. Reopen the agent app and the
  terminal afterwards. If `looptrack` still cannot be found on the PATH for an
  install whose kit came from the server, `doctor` and the session-start
  summary say so and show how to fix it (run the setup steps again, or the
  command that `doctor` shows to add the place to the PATH).
- Administrators: upgrading a server with `install.sh` also updates the
  `looptrack` it distributes to users. `install.sh --upgrade` (and the
  automatic replacement's timer) used to replace only the server's own binary.
  The distribution directory (`LOOPTRACK_DIST_DIR`) kept the old `looptrack`,
  and sessions running an old `looptrack` were never told to update. When it
  installs and on every `--upgrade`, `install.sh` fills the distribution
  directory from the release it downloaded: the binaries for all six platforms
  (taken out of the archives and checked against the signed `SHA256SUMS`), plus
  that `SHA256SUMS` and its signature. The directory is
  `/usr/local/share/looptrack/dist` under systemd and `<dir>/dist` with compose
  (setup's `compose.yaml` mounts `./dist` read-only at `/dist`). `install.sh`
  adds `LOOPTRACK_DIST_DIR` to `.env` when it is not there and leaves one that
  points elsewhere alone. To bring an existing server in line, run `--upgrade`
  once with the new `install.sh`, even on the same version. Opening the Windows
  archives needs `unzip` or `python3` on the server.
- Administrators: the server tells you when the distributed `looptrack` is
  out of date. If the distribution directory is missing, lacks a platform, or
  holds an older version than the server, `looptrack serve` says so in its
  startup log and in a banner for administrators, with the `--upgrade` line
  that fixes it. After `LOOPTRACK_DIST_DIR` is added to `.env`, the server
  distributes nothing (and the notice stays) until it is restarted. An empty
  value is different. A server whose `.env` sets `LOOPTRACK_DIST_DIR` to an
  empty value is treated as one that has decided not to distribute
  `looptrack`: it logs one line at startup, shows no warning or banner, and
  `install.sh` leaves it alone.
- `install.sh` (systemd) no longer takes another process's answer for the
  service's. The start check looked only for a `200` from `/healthz`. If
  another process (an old container left running, say) held the same port, the
  installer reported a finished install while the service had failed to bind.
  Once the service is stopped, `install.sh` stops with an error naming the port
  if something still answers there (an unattended upgrade starts the service
  again and ends as failed). After starting, it also checks that the service is
  active and that the process listening on the port is the service's main
  process. That second check needs `ss`; without it, only that the service is
  active.
- `install.sh` checks `.env` before reading it, without printing any value.
  It reads `.env` with the shell's `.`, so an unquoted value containing
  brackets, spaces or other special characters made the shell print the line (a
  password included) or run part of the value as a separate command. Every line
  has to be blank, a comment, `KEY='…'`, `KEY="…"` (with no `$`, backquote, `\`
  or `"` inside) or an unquoted value with no spaces or special characters that
  does not start with `~`. Anything else stops `install.sh` before the file is
  read, naming only the key (or the line number). The `KEY='…'` form that setup
  writes always passes.
- MySQL: a terminal `--upgrade` checks for pending migrations before it
  stops anything. Without `LOOPTRACK_SETUP_MIGRATE_DSN`, it used to fall back
  to the application user, stop the service, fail to create the new tables and
  leave the service stopped. With systemd and with compose alike, it first runs
  the new version's `migrate --check` (with compose, from the new image). When
  migrations are pending and `LOOPTRACK_SETUP_MIGRATE_DSN` is not set, it lists
  them and asks whether to go on. The default is to stop without changing
  anything, and that is also what happens with `--yes` or without a terminal.
  The unattended upgrade still leaves such a version alone; its message says
  that `LOOPTRACK_SETUP_MIGRATE_DSN` is needed.
- AI agent hooks: the secrets guard and the work record read quotes and line
  continuations the way a shell does. The secrets guard and the freshness
  guard's record of work read quotes both the POSIX way (outside quotes and
  inside double quotes, `\"` neither opens nor closes a pair) and the Windows
  way (`\` is an ordinary character, as in PowerShell), and act when either
  reading matches. A command that hid a `cat` of a secret file behind `\"` is
  confirmed, and nothing that was confirmed or recorded before goes through. A
  line ending in an even number of backslashes no longer joins the next line
  onto it; as in the shell, only an odd number counts as a continuation. The
  git guard is unchanged.

### Docs

- **The guides cover the distributed `looptrack`, the checks before an
  upgrade, and signing in with the placed `looptrack`.** The deployment guide
  and the updating guides describe the distribution directory, the start check
  and the migration check before stopping. The AI agents guide shows the
  sign-in commands with the absolute path of the `looptrack` that setup placed,
  and adds what to do when that `looptrack` is not found by name. The FAQ tells
  you to fix an out-of-date `looptrack` through the setup tool.
- Contributors: development builds stay in the working tree.
  `CONTRIBUTING.md` builds into `./bin/looptrack` and says not to copy it to
  `~/.local/bin` (where the server edition's setup places the released binary)
  or to use `go install`.

### Moving from 1.0.0-rc.4

- This version adds no migrations.
- **Run the one-line `--upgrade` once by hand.** The automatic update keeps
  running the copy of `install.sh` saved on the server, so this is how the new
  one gets there:
  `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`
  It also fills the distribution directory and adds `LOOPTRACK_DIST_DIR` to
  `.env`, even when the server is already on this version. Restart the service
  if it tells you to.
- With a `compose.yaml` written by an older setup, add `- ./dist:/dist:ro`
  under `services.looptrack.volumes` before that `--upgrade` (create `volumes:`
  there if it is missing, as it is with MySQL).
- If the new `install.sh` stops on a line of `.env`, put that value in single
  quotes (`KEY='…'`).
- Once the server distributes the new `looptrack`, sessions running an older
  one get "[Update the distributed files]". Have the agent call the setup tool
  and run the step it returns; it replaces `looptrack` and adds its place to
  the PATH. Reopen the agent app and the terminal afterwards.

---

1.0.0 のリリース候補の 5 つ目で、下の変更は `v1.0.0-rc.4` からの差分です。中心は 2 つです。サーバ版の利用者の作業環境が新しい `looptrack` を取って保つ仕組みと、`install.sh` によるサーバの更新を直しました。rc.4 のサーバを上げるなら、1 行の `--upgrade` を 1 度だけ手で実行してください。コマンドとほかの手順はこの節の最後の「1.0.0-rc.4 から上げるとき」にあります。

### 修正

- **AI エージェントの導入: 古い導入は 1 つのコマンドで直り、そのコマンドは必ずサーバを指します。** 作業環境の `looptrack` や kit が古いとき、以前の setup ツールは `looptrack self-update && looptrack issue init --project <slug> --agent <AI>` を `--url` 無しで返していました。loop の答えがあれば続けて `--url` 付きの init も返します。init が 2 回続く形でした。【配布スクリプトの更新】の案内も、セッションの開始時と MCP のツールの結果で同じ `--url` の無い init を示していました。`LOOPTRACK_API_URL` の無い端末で実行すると、その init は CLI の既定であるローカルモードに向きます。いまの setup ツールが返すのは 1 つのコマンドです。`looptrack` を取得して SHA-256 を確かめ、`--url`・`--source server`・`--dist` を付けて init するところまでを受け持ちます（loop の答えがあれば `--loop` / `--no-loop` も）。案内はコマンドを示さず、setup ツールを呼ぶよう伝えるだけ。その後の任意の確認も置いた `looptrack` にサーバの URL とプロジェクトを渡すので、AI の作業環境の環境変数には頼りません。setup の結果と案内に出るコマンドは、loop の手順・辞退済みの案内・トークンを貼る案内・`doctor` の案内のどれも、置いた `looptrack` の絶対パスで書いてあります。PATH 頼みの `looptrack` はもう使いません。
- AI エージェントの導入: サーバが配るものと違う `looptrack` は置き換えます。これまでは `~/.local/bin`（Windows は `%LOCALAPPDATA%\Programs\looptrack`）に `looptrack` があれば、どんなものでもそのまま使って init だけを行っていました。いまはそこにあるファイルの SHA-256 が配布物と同じときだけ取得を省きます。違えば取得して確かめてから置き換えます。symlink ならリンクを置き換え、リンクの先には触りません。同じ PC にデスクトップ版があると、その symlink（Windows は印つきのコピー）も配布物と違うファイルに当たり、サーバ版の setup が配布物に置き換えることになります。
- Codex と Copilot 向けの setup のコマンドが、シェルに環境変数を残さなくなりました。`LOOPTRACK_API_URL`・`LOOPTRACK_PROJECT` を渡す setup のコマンドがあります。Codex・Copilot 向けのすべてのコマンドと、other の導入済みを知らせる PowerShell のコマンドです。これらは値を sh では `export`・PowerShell では `$env:` で置いていたので、端末に貼れば値が残りました。そのまま同じ端末で別のプロジェクトに移ると、そちらの `looptrack` が黙ってこのプロジェクトに向いていたわけです。いまは `&&` でつないだコマンドならサブシェルの中で export します。単純コマンドは export の無い前置（`LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=… …`）で渡し、PowerShell は終わったら前の値に戻します。退避の変数名には、包む中身が使わない名前を選びました。
- setup の取得のコマンドもシェルに何も残しません。失敗したら途中のファイルも消します。取得 + init のコマンドはどの AI 向けでも包まずに出ていました。端末に貼ると残るのは sh なら変数 `U`・`S`・`D` と `/usr/bin/sum` を覆い隠す関数 `sum` で、PowerShell なら `$ErrorActionPreference`・`$ProgressPreference` と `$U` などの変数です。いまはどの AI 向けでも sh はサブシェルで、PowerShell は `& { }` で包みます。sh で取得・SHA-256 の確認・置き換えのどれかに失敗したら `.part` を消す動きは、PowerShell に合わせたものです。
- guide・`init`・`doctor`・`self-update`・アカウント設定の画面は、サーバ版の利用者に `--url` の無い init や PATH の `looptrack` を案内しません。guide は loop を足すのに `looptrack issue init --loop` を案内していましたが、いまは MCP の setup ツールを呼ぶよう案内します。サーバ版（`--url` か `--source server`）で loop を辞退した `init` は、足すためのコマンドを示します。中身は実行中の `looptrack` の絶対パスに `--project`・`--url`・`--agent`・`--source server` を付けたものです。`doctor`（kit をサーバから取った導入）と `self-update` が示すのは、init の再実行や `self-update` ではなく setup ツールの手順。`doctor` のログインの案内は実行中の `looptrack` で書きます。アカウント設定の画面はログインのコマンドを setup が置く `looptrack` の絶対パスで示し、macOS・Linux 向けと Windows 向けの両方を載せます。JSON が壊れているときの init のエラーは「同じ init のコマンドを再実行」と案内します。
- AI エージェントの導入: setup の取得が、`looptrack` の置き場を PATH に足します。スキル・規律・hook・MCP の案内は `looptrack` を名前で呼ぶのに、サーバ版の利用者の PATH には `looptrack` がありませんでした。setup が `~/.local/bin`（Windows は `%LOCALAPPDATA%\Programs\looptrack`）に置くだけで、その置き場を PATH に足していなかったからです。いまは取得 + init のコマンドが、置き場が PATH に無ければ足します。macOS・Linux なら、既定のシェルの起動ファイルに 1 行を書きます。zsh は `~/.zshenv` に書きます。`ZDOTDIR` があればその下にも書きます。bash は `~/.bashrc` とログインのシェルが読むファイルに書きます。fish は `conf.d/looptrack.fish` で、それ以外は `~/.profile` です。Windows なら利用者の環境変数 `Path` に入れ、`setx` を使わずに元の値の種類を保ちます。既にあれば何も書きません。足した後は AI のアプリと端末を開き直してください。kit をサーバから取った導入で PATH から `looptrack` が見つからないときは、`doctor` とセッション開始時の要約がそれを知らせて直し方を示します。直し方は setup の手順の再実行か、`doctor` が示す PATH を足すコマンドです。
- 管理者向け: `install.sh` でサーバを上げると、利用者に配る `looptrack` も新しくなります。これまでの `install.sh --upgrade` と自動の置き換えの timer は、サーバ自身の実行ファイルだけを置き換えていました。配布ディレクトリ（`LOOPTRACK_DIST_DIR`）には古い `looptrack` が残り、古い `looptrack` を使うセッションに更新の知らせも出ません。いまの `install.sh` は入れるときと `--upgrade` のたびに、取得したリリースから配布ディレクトリをそろえます。置くのは 6 つの OS・CPU の組の実行ファイルと、その `SHA256SUMS` と署名。実行ファイルは書庫から取り出し、署名つきの `SHA256SUMS` で照合したものです。置き場は systemd が `/usr/local/share/looptrack/dist` で compose が `<dir>/dist` です。setup の `compose.yaml` は `./dist` をコンテナの `/dist` に読み取り専用で入れるようになりました。`.env` に `LOOPTRACK_DIST_DIR` が無ければ足します。別の置き場を指していれば触りません。いまのサーバは新しい `install.sh` で 1 度 `--upgrade` を実行すればそろいます。同じ版でもそろえます。Windows 向けの書庫を開くには、サーバに `unzip` か `python3` が要ります。
- 管理者向け: 配る `looptrack` が古いとサーバが知らせます。配布ディレクトリが無い・OS・CPU の組が足りない・サーバより古い版のどれかなら、`looptrack serve` が起動時のログと管理者の画面の帯で知らせます。直す `--upgrade` の 1 行も示します。`.env` に `LOOPTRACK_DIST_DIR` を足しても再起動するまでは何も配らず、知らせも消えません。`.env` で `LOOPTRACK_DIST_DIR` を空の値にしたサーバは別扱いです。`looptrack` を配らないと決めたものとみなし、起動時のログに 1 行残すだけです。警告も帯も出さず `install.sh` も触りません。
- `install.sh`（systemd）がほかのプロセスの応答をサービスの応答と取り違えなくなりました。起動の確認が見ていたのは `/healthz` の `200` だけでした。止め忘れた古いコンテナなどが同じポートを握っていると、サービスは bind に失敗しているのにインストールが終わったと報告していました。いまの `install.sh` はサービスを止めた後もそのポートに応答があれば、ポートを示して止まります。無人の更新はサービスを起動し直して失敗として終わります。起動した後はサービスが active であることに加えて、ポートで待ち受けているのがサービスのメインのプロセスであることも確かめます。この確かめには `ss` が要り、無ければ active かどうかだけを見ます。
- `install.sh` は `.env` を読む前に確かめ、値は一切出しません。`install.sh` は `.env` をシェルの `.` で読みます。だから引用符で囲まない値に括弧・空白などの特殊な文字があると、シェルがパスワードを含むその行を出力したり、値の一部を別のコマンドとして実行したりしていました。いまは次のどれにも当たらない行があると、ファイルを読む前に止まります。空行・コメント・`KEY='…'`・`KEY="…"`（中に `$`・バッククォート・`\`・`"` を含まない）・空白も特殊な文字も含まず `~` で始まらない引用なしの値、の 5 つです。止まるときに示すのはキーの名前だけで、読めなければ行番号を示します。setup が書く `KEY='…'` の形は必ず通ります。
- MySQL: 端末の `--upgrade` が何かを止める前に未適用のマイグレーションを確かめます。`LOOPTRACK_SETUP_MIGRATE_DSN` が無いと、アプリ用の利用者で接続してサービスを止めていました。新しい表を作れずに失敗し、サービスは止まったまま残っていました。いまは systemd でも compose でも、先に新しい版の `migrate --check` を実行します（compose は新しい版のイメージで）。未適用があって `LOOPTRACK_SETUP_MIGRATE_DSN` が無ければ、その一覧を示して続けるかを尋ねます。既定は続けないこと。`--yes` や端末の無いときも同じで何も変えずに止まります。無人の更新はこれまでどおりそういう版を置き換えず、理由の文面に `LOOPTRACK_SETUP_MIGRATE_DSN` が要ることを足しました。
- AI エージェントの hook: 秘密のガードと作業の記録が、引用符と行末の継続をシェルと同じように読みます。秘密のガードと鮮度ガードの作業の記録は、引用符を 2 通りに読みます。posix の読み方では引用符の外と二重引用符の中の `\"` が組を開きも閉じもしません。Windows の読み方では PowerShell と同じく `\` はただの文字です。どちらかで当たれば確認・記録します。`\"` の陰に秘密のファイルの `cat` を隠したコマンドも確認になり、これまで確認・記録していたものが素通りになることはありません。偶数本のバックスラッシュで終わる行は、もう次の行とつなぎません。継続とみなすのはシェルと同じく奇数本のときだけです。git ガードは変えていません。

### 文書

- **ガイドに、配る `looptrack`・更新の前の確かめ・置いた `looptrack` でのサインインを書きました。** 配置の文書と更新のガイドには配布ディレクトリ・起動の確認・止める前のマイグレーションの確認を書き足しています。AI エージェントのガイドはサインインのコマンドを setup が置いた `looptrack` の絶対パスで示し、その `looptrack` が名前で見つからないときの対処も足しました。FAQ は古い `looptrack` を setup ツールで直すよう案内します。
- 開発に加わる人向け: 開発版は作業ツリーに置きます。`CONTRIBUTING.md` のビルドの手順は `./bin/looptrack` に作るようになりました。`~/.local/bin`（サーバ版の setup が配布物を置く場所）に写さないことと、`go install` を使わないことも書いてあります。

### 1.0.0-rc.4 から上げるとき

- この版にマイグレーションはありません。
- **1 行の `--upgrade` を 1 度だけ手で実行してください。** 自動更新はサーバに保存した `install.sh` の写しを動かし続けるので、新しいものはこれで入れます: `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`。サーバが既にこの版でも配布ディレクトリをそろえ、`.env` に `LOOPTRACK_DIST_DIR` を足します。起動し直すよう出たらそのとおりにしてください。
- 古い setup が書いた `compose.yaml` なら、その `--upgrade` の前に `services.looptrack.volumes` へ `- ./dist:/dist:ro` を足します。MySQL の構成では `volumes:` が無いので、作ってから足してください。
- `install.sh` が `.env` のある行で止まったら、その値を単引用符で囲みます（`KEY='…'`）。
- サーバが新しい `looptrack` を配り始めると、古いものを使うセッションに【配布スクリプトの更新】が出ます。AI に setup ツールを呼ばせ、返った手順を実行してください。`looptrack` が置き換わって置き場が PATH に入ります。最後に AI のアプリと端末を開き直すこと。

## [1.0.0-rc.4] - 2026-09-30

Fourth release candidate for 1.0.0, relative to `v1.0.0-rc.3`. It fixes MCP
behind a reverse proxy, `looptrack migrate` with the minimum MySQL grants, and
the rollback of the automatic server update. To get the fixed rollback, run the
one-line `--upgrade` once by hand ("Moving from 1.0.0-rc.3" below).

### Fixed

- **Behind a reverse proxy: MCP works on a server that listens on 127.0.0.1.**
  The MCP library's protection against DNS rebinding refused (403) every
  request received on a loopback address whose `Host` was not a loopback name.
  A server installed with `install.sh` (systemd) listens on 127.0.0.1 behind
  nginx or Caddy, which pass the public host name, so none of its MCP requests
  got through. The check runs in looptrack itself, before authentication: a
  request received on a loopback address is let through when its `Host`
  (without the port) is a loopback name (127.0.0.0/8, `::1`, `localhost`) or
  the host of `LOOPTRACK_PUBLIC_URL` (case-insensitive), and is refused with
  403 otherwise. `X-Forwarded-Host` is not trusted. Without a public URL (the
  desktop app), only loopback names are accepted, as before; requests received
  on other addresses (inside a container, say) are not checked, as before.
- MySQL: `looptrack migrate` works with the minimum grants when there is
  nothing to apply. MySQL asks for the `CREATE` privilege for
  `CREATE TABLE IF NOT EXISTS` even when the table already exists, so an
  application user holding only the grants in `deploy/grants.sql` (`SELECT` on
  `schema_migrations`) failed with error 1142 on every migrate. Because of
  this, the automatic update of a MySQL server failed after stopping the
  service and put the previous version back every time, even for a version
  that does not change the database. A manual `install.sh --upgrade` without
  `LOOPTRACK_SETUP_MIGRATE_DSN` failed at the same migrate as well, and left the
  service stopped. migrate creates `schema_migrations` only when it cannot see
  the table; with a migration still to apply, the missing privilege is still
  reported as an error.
- The automatic server update always puts the previous version back once it
  has stopped the service. The previous version was restored only when the
  installer stopped on one of its own error checks. A failing
  `systemctl daemon-reload` or `systemctl start`, any other command that failed
  on its way, or an interruption (`TERM`, say) left the service stopped. Every
  one of these puts the previous binary (with SQLite, also the database copy
  taken right after stopping) back once, starts it again, leaves a one-line
  reason in `journalctl -u looptrack-upgrade` and exits with a non-zero code. A
  failure to start the new version gets its own message. A manual `--upgrade`
  on a terminal still does not roll back.
- `install.sh` gives the right reason and the dnf commands. When
  `--auto-upgrade on` stops because neither `curl` nor `wget` is installed, it
  says they are needed to fetch the new release's archive, `SHA256SUMS` and
  signature once a day (the timer does not fetch `install.sh` again). The hints
  for installing `minisign` and `curl` give the dnf commands for AlmaLinux and
  similar systems (`minisign` comes from EPEL:
  `dnf install -y epel-release && dnf install -y minisign`) next to the apt
  ones in the installer's messages, and the deployment and updating guides give
  the dnf command for `minisign` too.

### Docs

- **Plainer wording across the documents.** The README, the user guides, the
  kit README, the AI guide, `CONTRIBUTING.md`, `SECURITY.md` and the other
  guides for adding projects and cloud agents were rewritten in plainer, more
  direct language (in both languages where a document has English and Japanese
  versions), with the facts, the strength of each requirement and the meaning
  of both languages kept as they were. `CHANGELOG.md` carries each version in
  English followed by Japanese, and the server design document describes the
  MCP `Host` check.

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

1.0.0 のリリース候補の 4 つ目で、下の変更は `v1.0.0-rc.3` からの差分です。リバースプロキシの後ろの MCP を直しました。MySQL の最小の権限での `looptrack migrate` と、サーバの自動更新の戻し方も直しています。直った戻し方を使うには 1 行の `--upgrade` を 1 度だけ手で実行します（下の「1.0.0-rc.3 から上げるとき」）。

### 修正

- **リバースプロキシの後ろ: 127.0.0.1 で待ち受けるサーバでも MCP が使えます。** MCP のライブラリが持つ DNS rebinding の対策は、ループバックのアドレスで受けた要求のうち、`Host` がループバックの名前でないものをすべて拒否していました（403）。`install.sh` で入れたサーバ（systemd）は 127.0.0.1 で待ち受け、前段の nginx や Caddy が公開のホスト名を渡します。MCP の要求は 1 つも通りませんでした。いまは looptrack 自身が認証より前に判定します。ループバックのアドレスで受けた要求は、`Host` がループバックの名前（127.0.0.0/8・`::1`・`localhost`）か `LOOPTRACK_PUBLIC_URL` のホストなら通し、それ以外は 403 で拒否します。比べるときはポートを除き、大文字小文字は問いません。`X-Forwarded-Host` は信じない作りです。公開の URL が無いときはこれまでどおりループバックの名前だけを通し、デスクトップ版はこれに当たります。コンテナの中のように、ほかのアドレスで受けた要求もこれまでどおり判定しません。
- MySQL: 最小の権限でも適用するものが無ければ `looptrack migrate` が通ります。MySQL は、表が既にあっても `CREATE TABLE IF NOT EXISTS` に `CREATE` の権限を求めます。`deploy/grants.sql` の権限（`schema_migrations` には `SELECT`）だけを持つアプリケーションのユーザでは、migrate が毎回エラー 1142 で失敗していました。その結果、MySQL のサーバの自動更新は、データベースを変えない版でもサービスを止めた後で失敗し、前の版に戻ることを毎回繰り返していました。`LOOPTRACK_SETUP_MIGRATE_DSN` を渡さずに手で実行する `install.sh --upgrade` も同じ migrate で失敗し、こちらはサービスが止まったまま残ります。いまの migrate は、`schema_migrations` が見えないときだけ作ります。適用する移行が残っていれば、権限の不足はこれまでどおりエラーとして知らせます。
- サーバの自動更新はサービスを止めた後なら必ず前の版に戻します。これまで前の版に戻るのはインストーラ自身の検査で止まったときだけでした。`systemctl daemon-reload` や `systemctl start` の失敗・途中のほかのコマンドの失敗・`TERM` などの中断では、サービスが止まったまま残っていました。いまはどの場合も前の実行ファイルを 1 回だけ戻して起動し直します。SQLite なら止めた直後に取った DB の控えも戻します。理由は 1 行 `journalctl -u looptrack-upgrade` に残ります。終了コードは 0 以外です。新しい版が起動しないときもそれと分かるメッセージを出すようになりました。端末で手動で実行する `--upgrade` はこれまでどおり戻しません。
- `install.sh` が正しい理由と dnf のコマンドを示します。`curl` も `wget` も無いために `--auto-upgrade on` が止まるとき、理由を「1 日 1 回、新しい版の書庫と `SHA256SUMS` と署名を取るのに要る」と示すようになりました。タイマは `install.sh` を取り直しません。インストーラの案内では `minisign` と `curl` の入れ方に、apt に並べて AlmaLinux などの dnf のコマンドも示します。`minisign` は EPEL から入れるもので、コマンドは `dnf install -y epel-release && dnf install -y minisign` です。配置と更新のガイドにも `minisign` の dnf のコマンドを足しました。

### 文書

- **文書の言い回しを平易に。** README・利用者ガイド・kit の README・AI 向けのガイド・`CONTRIBUTING.md`・`SECURITY.md` と、プロジェクトの追加やクラウドのエージェントのガイドを、より平易で率直な言い回しに書き直しました。英語と日本語の両方がある文書は両方です。事実・各要件の強さ・日英の意味はそのままにしてあります。`CHANGELOG.md` は各版を英語の後に日本語で載せるようになり、サーバの設計の文書には MCP の `Host` の判定を書き足しました。

### 1.0.0-rc.3 から上げるとき

- この版に移行（マイグレーション）はありません。
- 自動更新はサーバに保存した `install.sh` の写しを動かし続け、新しいものを取ってきません。直った戻し方を使うには 1 行の `--upgrade` を 1 度だけ手で実行してください: `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`。サーバが既にこの版でも写しは新しくなります。
- リバースプロキシの後ろのサーバで MCP のクライアントが 403 で拒否されていたなら、`LOOPTRACK_PUBLIC_URL` にクライアントが使う公開の URL が入っていることを確かめます。通すのはその URL のホストです。

## [1.0.0-rc.3] - 2026-09-29

Third release candidate for 1.0.0, relative to `v1.0.0-rc.2`. A Linux server
can be installed and upgraded with one line, can update itself every day if you
turn that on, and tells administrators about new versions. GitHub Releases ship
the server binaries as archives, so read "Moving from 1.0.0-rc.1 / rc.2" if a
script downloads them.

### Added

- **Linux servers: a one-line installer.** On a Linux server,
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
- Administrators: opt-in automatic server updates.
  `install.sh --auto-upgrade on|off` (`LOOPTRACK_INSTALL_AUTO_UPGRADE`; off by
  default; systemd only; needs `minisign`) installs a daily
  `looptrack-upgrade.timer` that runs the copy of `install.sh` kept on the
  server with `--upgrade --require-signature --yes --only-newer`. It never moves
  to an older version, and if a run fails after the service was stopped, it puts
  the previous version back and starts it again. With MySQL, a new version with
  migrations to run is not installed unattended (run `--upgrade` on a terminal).
  It is refused for compose and `--no-start` servers.
  `journalctl -u looptrack-upgrade` shows what each run did. `--only-newer` can
  also be passed by hand, and the new `looptrack migrate --check` lists pending
  migrations without applying them and exits with code 3 when there are any.
- Administrators: the server tells you about new versions. It checks the
  list of releases on GitHub at startup and every 24 hours, and tells you only
  about a version whose `SHA256SUMS` signature checks out and whose release
  carries the file for your OS and CPU. It follows the kind of version you run
  (an rc hears about rcs too, a stable release only about stable releases), and
  a failed check (offline, say) keeps the previous notice. The notice appears
  in a strip for administrators (with the one-line update command), in
  `looptrack doctor` run with an administrator's token, and in the server log.
  `LOOPTRACK_UPDATE_CHECK=off` stops checking (no connection to GitHub),
  `LOOPTRACK_UPDATE_CHANNEL` (`stable` or `prerelease`) chooses what to follow,
  and `LOOPTRACK_UPDATE_URL` points the check at another `https://` endpoint of
  the same form. The CLI does not check GitHub, and a build you made yourself
  (`dev`) is not checked. The desktop app gets the same notices in its tray.
- MySQL: `looptrack grants print|apply`. The minimum MySQL grants
  (`deploy/grants.sql`) are built into `looptrack`. `grants print` prints them
  for the database and user in `LOOPTRACK_DSN` (not only `im` and `im_app`);
  `grants apply` asks for administrative credentials on the terminal (never
  shown or stored), creates a missing database or application user after
  asking, creates the tables if they are missing, applies the grants, and
  checks that the application user can read.
- Local mode: a backup before each database upgrade. Before a new version
  migrates existing data in the local SQLite database, a server in
  `LOOPTRACK_LOCAL_MODE` (and the desktop app) copies `looptrack.db` into the
  `backups` folder of the data folder (`looptrack.db.<UTC time>`) and keeps the
  two newest copies. If the copy cannot be made, it neither migrates nor
  starts.
- CLI: `-` reads the text from standard input.
  `looptrack issue comment <ID> -`, `issue new --body -` and `--comment -` of
  `issue status`, `issue close` and `issue next` read the text from standard
  input, as `looptrack handoff append` already did; empty input is refused
  without sending anything. Before, `-` was saved as the text itself.
- Starting an issue whose acceptance criteria are still the template returns
  a note. In a project with `acceptance.require_on_close`, setting an issue to
  In Progress (including through `next`) while its "## Acceptance criteria" are
  still the template returns a note that closing it will be refused as it
  stands. The start itself is not blocked. The note appears in the CLI, in REST
  responses (`acceptance_notice`), in MCP `set_status` and `next`, and in the
  web UI.
- Claude Code: your own MCP start is told apart from another
  conversation's. A new core hook, `looptrack hook issue-session-bind`
  (PreToolUse on `mcp__.*`), hands the server the tool call's ID and the
  conversation's session ID right before a looptrack MCP tool runs, so MCP
  `next` and the lists can tell a start by another conversation of the same
  user from your own, even over the same MCP connection. It is cut off after
  1.5 seconds and lets the call through on any failure;
  `looptrack issue init --no-session-bind` leaves it out. When the
  token-accounting hook sends the snapshot for an MCP call, the server also
  ties that start to the conversation (migration `0006_issue_event_sessions`),
  so the CLI can tell the two apart as well. Other agents are not affected.
- AI agent hooks: `LOOPTRACK_LOOP_HOOK_LOG=1` records hook verdicts. Every
  verdict (the core hooks included) is appended as one JSONL line to
  `<state dir>/.looptrack-freshness/hook-log.jsonl`, holding only the time, the
  hook, the event, the decision, the session and a word for the kind of reason.
  It never records the command, the prompt, the wording of the reason or a path. It rotates
  at 1 MiB. Off by default.

### Changed

- **GitHub Releases ship the server binaries as archives.** Each of the six
  targets is `looptrack_<version>_<os>_<arch>_server.tar.gz` (Linux, macOS) or
  `looptrack_<version>_windows_<arch>_server.zip` (Windows). An archive unpacks
  into a single folder, `looptrack_<version>_<os>_<arch>_server/`, holding
  `looptrack` (`looptrack.exe`), `NOTICE`, `OFL-BIZUDGothic.txt` and `LICENSE`.
  The bare binaries `looptrack_<version>_<os>_<arch>[.exe]` and the standalone
  `install.sh` and `grants.sql` are no longer release assets, and the archives
  do not contain them either. The desktop app's assets keep their names, and
  `NOTICE` and `OFL-BIZUDGothic.txt` are still attached on their own.
  `SHA256SUMS` lists the archives and, in addition, the binary inside each
  archive under its previous name (`looptrack_<version>_<os>_<arch>[.exe]`), so
  a binary taken out of an archive can still be checked against the signed
  list. Because those lines name files that are not attached, check a single
  line with `grep … | sha256sum -c -`, or use
  `sha256sum -c --ignore-missing SHA256SUMS`.
- The server's distribution directory and `self-update` are unchanged.
  `LOOPTRACK_DIST_DIR` still holds bare binaries named
  `looptrack_<version>_<os>_<arch>[.exe]`; archives placed there are not
  listed or served. To distribute an official release, take `looptrack` out
  of each archive, put it under that name, and put the release's `SHA256SUMS`
  and `SHA256SUMS.minisig` next to it as they are (see "The looptrack you
  distribute to users" in the updating guide).
- MySQL: the installer applies the grants in the same run. With MySQL and
  the minimum grants, `install.sh` used to stop after `looptrack setup` and ask
  you to run `grants.sql` yourself and then run it again. It asks for
  administrative credentials on the terminal at that point, runs
  `looptrack grants apply`, checks that the application user can read, and
  goes on to start the server and check it. If the credentials are wrong it
  stops without starting anything and, when run again, continues from the
  question. `--upgrade` does the same when a new version adds tables. Without a
  terminal (systemd only), pass `LOOPTRACK_INSTALL_DB_ADMIN_USER` and
  `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE`.
- MySQL: `looptrack setup` (and so the installer) creates a missing
  database. When the database in the connection string did not exist yet,
  setup stopped at the connection, before the migration, so the installer never
  got as far as creating it. Setup notices the missing database, asks for
  administrative credentials on the terminal (once; never shown or stored),
  asks whether to create the database (even with `--yes`), and creates it and
  the application user, the tables and the grants the same way as
  `looptrack grants apply` before carrying on. An existing database is left as
  it is. Answer no, or run without a terminal, and it stops without creating
  anything and shows the `CREATE DATABASE` statement to run yourself.
- Release candidates are the Latest release until 1.0.0 is out. Until a
  stable `vX.Y.Z` tag exists, a release candidate is published as the Latest
  release on GitHub Releases (not as a pre-release) and gets the `latest` tag on
  GHCR, so the one-line installer (which reads `releases/latest`) installs it.
  Release candidates published after 1.0.0 go back to being pre-releases.
- Verification records follow the language of the user who records them.
  The comment that `verify` (and MCP `report_verify`) leaves is written in the
  recorder's language, chosen the same way as everything else shown to that
  user; records already stored are unchanged. In English, the mark for a result
  reported over MCP reads "self-reported via MCP" everywhere.
- Administrators: creating a project fills in omitted values the same way on
  every path. `looptrack project create` no longer requires `--prefix` and
  `--name` (they default to the slug in upper case and the slug, as on the admin
  page and in MCP `create_project`), and the first project created by
  `looptrack setup` gets the default sort order (100) like the others. An
  explicit `--order 0` is kept as 0.
- Loop kit: `pre-tool-subagent-bound` also asks subagents to check
  identifiers. The cautions it appends to a subagent's instructions include
  cross-checking every identifier in them (a variable or function name, a
  commit SHA, a line number, a file path) against the real thing with
  `git show` / `git grep` before use, and reporting any mismatch in one line.
  `background-process.md` carries the same line for agents where nothing is
  appended automatically.
- Loop kit: more working discipline in the rules. `working-discipline.md`
  gains rules on counting both sides of a shared component, re-checking with a
  different method, what a green summary does and does not prove, controls for
  "X does not happen" tests, probing guards for the forms they miss, running
  the goldens a change records, running the build and type check right after a
  rebase, and pushing rules and state down to subagents (with a template to copy
  into their instructions).

### Fixed

- **CLI: `self-update` checks the listed version against the signature.** It
  used to trust the version in the (unsigned) distribution listing alone, so a
  listing could pair an older release's correctly signed `SHA256SUMS` with a
  higher version number and roll the CLI back. It requires the file name in the
  signed `SHA256SUMS` (and, for a release, the version in the signature's
  trusted comment) to match the listed version, OS and CPU, and replaces nothing
  otherwise. Going back to an older version still needs `--force`.
- An older looptrack no longer runs on a database migrated by a newer one.
  `migrate`, `setup`, local mode and `serve` on a team server (and the desktop
  app) refuse a database that has applied migrations they do not know, instead
  of using data in the newer shape. To go back, restore the database from a
  backup taken before the newer version migrated it. When a migration in
  `install.sh --upgrade` fails after applying something, the installer says to
  restore the database from the backup as well.
- AI agent setup: MCP `setup` no longer replaces a `looptrack` that is
  already installed. When `~/.local/bin/looptrack` (on Windows,
  `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`) exists, the returned
  command neither downloads nor replaces it and only runs `init` with it, so a
  server distributing an older version can no longer downgrade a newer local
  one (the next `setup` suggests `self-update` when the local one is older).
- Codex: MCP `setup` prefixes the environment variables. Codex picks up
  the variables `init` writes into `.codex/config.toml` only after a restart,
  so, as for Copilot, the returned commands (including the token check) carry
  `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT`.
- Token accounting covers every operation that records an event. Tokens are
  also attached to MCP `next` (when it starts an issue), `report_verify` and
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
- Administrators: export and import respect archived projects.
  `looptrack export` leaves archived projects out unless `--archived` is given,
  and `looptrack import` refuses to import into a project that is archived
  (replacing it would erase the data kept there; unarchive it first).
- AI agent hooks see through wrapped commands. The issue freshness guard
  and the handoff completion record recognise `git commit` / `push` / `merge`,
  `looptrack issue …` calls and completions (`close`, `status Done`) wrapped in
  `bash -c` / `sh -c` / `eval`, behind prefix words such as `sudo`, `env`,
  `nohup` and `xargs`, inside a `function`, or split by a line continuation
  (the nested shells and prefix words are unwrapped the same way as in the git
  and secrets guards). A commit of only the handoff file made in another
  worktree is told apart correctly.
- Loop kit, runaway-process guard: an unquoted `ps` line is judged as a wait
  loop. `ps` drops the quotes from a command line
  (`/bin/bash -c while true; do sleep 1; done`), which used to slip past the
  shorter threshold for unbounded wait loops.
- Loop kit, task-mode hook: a subagent's report no longer switches the
  mode. Claude Code passes a subagent's final report through UserPromptSubmit
  wrapped in `<agent-message …>`; the words in it no longer switch between
  investigate and execute mode, and issue IDs in it no longer count as
  mentioned by the user for the freshness guard.
- AI agent hooks: a regular expression that Go (RE2) cannot read is
  reported. `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE`, `_INVEST_RE`,
  `LOOPTRACK_LOOP_RUNAWAY_ALLOW` and `LOOPTRACK_MCP_SERVER` were silently
  ignored when written with lookahead and the like. The hook shows the variable
  name and the error as a message and carries on without it (the default words
  and exemptions still apply; `LOOPTRACK_MCP_SERVER` matches nothing and is
  reported on MCP tool calls only). The guides say these are RE2.
- CLI: `looptrack issue init` no longer adds a redundant `.gitignore`
  line. When `.claude/.looptrack-freshness/` is already ignored by a
  non-negated rule in a `.gitignore` inside the working tree, init leaves the
  top-level `.gitignore` alone (rules only in `.git/info/exclude` or a global
  excludes file do not count, since other clones do not share them).
- The English refusal of `self-update` on a server installed with
  `install.sh` points to a heading that exists: the "Server" section of the
  updating guide.

### Docs

- **New "Token reports" guide.** What is recorded, when it is sent and what is
  never sent, how to turn it off, attribution and recovering unattributed usage,
  report requests, building the PDF by hand, the ledger, and the settings. The
  README links to it.
- The "Updating" guide covers the new-version notices and the server's
  automatic updates, including when an automatic update is skipped and what
  to do then.
- README and guide introductions describe Looptrack as external memory for
  AI coding agents that makes loop engineering possible, and the README's
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

1.0.0 のリリース候補の 3 つ目で、下の変更は `v1.0.0-rc.2` からの差分です。Linux のサーバを 1 行で入れて上げられるようになりました。選べば毎日自分で更新して新しい版を管理者に知らせます。GitHub Releases のサーバのバイナリは書庫になりました。ダウンロードするスクリプトがあるなら「1.0.0-rc.1 / rc.2 から上げるとき」を読んでください。

### 追加

- **Linux のサーバ: インストーラが 1 行に。** Linux のサーバで `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh` を実行すると、`main` にあるインストーラが動きます。`--from` を付けなければ、最新のリリースの `looptrack_<version>_linux_<arch>_server.tar.gz` を GitHub Releases から取ってきます（`--version <version>` で版を選べる）。それを `SHA256SUMS` で照合します。`minisign` が入っていれば `SHA256SUMS` の署名も確かめます。`--require-signature` を付けると署名が必須です。そこまで通って初めて展開して `looptrack` を入れます。スクリプトそのものは HTTPS で取ってくるだけで、自分自身を検証できません。気になるなら先にダウンロードして中身を読んでください。`--from` はこれまでどおり手元の `deploy/release/dist.sh build` の出力か、サーバの配布ディレクトリ（素のバイナリ）を受け付けます。インストーラは Linux 専用です。ほかの環境では何も変えずに止まり、デスクトップ版を案内します。
- 管理者向け: サーバの自動更新を選んで有効にできます。`install.sh --auto-upgrade on|off`（`LOOPTRACK_INSTALL_AUTO_UPGRADE`）は毎日動く `looptrack-upgrade.timer` を入れます。既定は無効です。使えるのは systemd に限られ `minisign` も要ります。このタイマはサーバに残した `install.sh` の写しを `--upgrade --require-signature --yes --only-newer` 付きで実行するもの。古い版へ移ることはありません。サービスを止めた後で失敗したら、前の版を戻して起動し直します。MySQL で実行すべき移行を持つ新しい版は、無人では入れません（端末で `--upgrade` を実行する）。compose のサーバと `--no-start` のサーバでは拒否されます。各回で何をしたかは `journalctl -u looptrack-upgrade` で見られます。`--only-newer` は手で渡すこともできます。新しい `looptrack migrate --check` は未適用の移行を適用せずに並べ、1 つでもあれば終了コード 3 で終わります。
- 管理者向け: サーバが新しい版を知らせます。起動したときと 24 時間ごとに、GitHub のリリースの一覧を確かめます。知らせるのは `SHA256SUMS` の署名が正しく、しかもそのリリースに自分の OS と CPU 向けのファイルがある版に限ります。どの種類の版を追うかは動かしている版に合わせます。rc なら rc も知らせます。安定版なら安定版しか知らせません。オフラインなどで確認に失敗したときは、前のお知らせを残します。お知らせが出るのは管理者向けの帯（1 行の更新コマンドつき）・管理者のトークンで実行した `looptrack doctor`・サーバのログです。`LOOPTRACK_UPDATE_CHECK=off` で確認をやめ（GitHub に接続しない）、`LOOPTRACK_UPDATE_CHANNEL`（`stable` か `prerelease`）で追うものを選べます。`LOOPTRACK_UPDATE_URL` を使えば、確認先を同じ形の別の `https://` のエンドポイントに向けられます。CLI は GitHub を確かめず、自分でビルドしたもの（`dev`）も確認しません。デスクトップ版のトレイにも同じお知らせが出ます。
- MySQL: `looptrack grants print|apply`。MySQL の最小の権限（`deploy/grants.sql`）を `looptrack` に組み込みました。`grants print` は `LOOPTRACK_DSN` にあるデータベースとユーザに合わせて権限を出力します（`im` と `im_app` に限らない）。`grants apply` は管理者の資格情報を端末で尋ねます。表示も保存もしません。無いデータベースやアプリケーションのユーザは、尋ねてから作ります。テーブルが無ければテーブルも作ります。権限を付けて、アプリケーションのユーザが読めることを確かめます。
- ローカルモード: DB を移行する前に控えを取ります。新しい版がローカルの SQLite のデータベースにある既存のデータを移行する前に、`LOOPTRACK_LOCAL_MODE` のサーバとデスクトップ版は `looptrack.db` をデータフォルダの `backups` フォルダへ写します（`looptrack.db.<UTC time>`）。控えは新しいほうから 2 つを残します。写せなかったときは移行も起動もしません。
- CLI: `-` で標準入力から本文を読みます。標準入力から本文を読むようになったのは `looptrack issue comment <ID> -` と `issue new --body -`、それに `issue status`・`issue close`・`issue next` の `--comment -` です。`looptrack handoff append` が前からしていたのと同じです。空の入力は何も送らずに拒否します。これまでは `-` という文字そのものが本文として保存されていました。
- 雛形の受け入れ条件のまま着手すると注記が返ります。`acceptance.require_on_close` のあるプロジェクトで、「## 受け入れ条件」が雛形のままのイシューを In Progress にしたときの話です（`next` による着手も含む）。このままでは close が拒否される、という注記を返すようになりました。着手そのものは止めません。注記は CLI・REST の応答（`acceptance_notice`）・MCP の `set_status` と `next`・Web UI に出ます。
- Claude Code: 自分の MCP の着手と、ほかの会話の着手を見分けます。新しい core の hook `looptrack hook issue-session-bind`（`mcp__.*` の PreToolUse）が、looptrack の MCP のツールが動く直前に、ツール呼び出しの ID と会話のセッション ID をサーバに渡します。同じ MCP の接続を通っていても MCP の `next` と一覧は同じ利用者の別の会話による着手を自分の着手と区別できるようになりました。1.5 秒で打ち切ります。どんな失敗でも呼び出しは通します。`looptrack issue init --no-session-bind` ならこの hook を入れません。トークンの集計の hook が MCP の呼び出しのスナップショットを送ると、サーバはその着手を会話に結び付けます（移行 `0006_issue_event_sessions`）。これで CLI でも 2 つを見分けられます。ほかのエージェントには影響しません。
- AI エージェントの hook: `LOOPTRACK_LOOP_HOOK_LOG=1` で判定を記録します。core の hook を含むどの判定も、JSONL の 1 行として `<state dir>/.looptrack-freshness/hook-log.jsonl` に追記します。残すのは時刻・hook・イベント・決定・セッションと、理由の種類を表す語。コマンド・プロンプト・理由の文面・パスは決して残しません。1 MiB でローテーションします。既定は無効です。

### 変更

- **GitHub Releases のサーバのバイナリを書庫で配ります。** 6 つの対象はどれも `looptrack_<version>_<os>_<arch>_server.tar.gz`（Linux・macOS）か `looptrack_<version>_windows_<arch>_server.zip`（Windows）になりました。書庫を展開すると 1 つのフォルダ `looptrack_<version>_<os>_<arch>_server/` ができ、中身は `looptrack`（`looptrack.exe`）・`NOTICE`・`OFL-BIZUDGothic.txt`・`LICENSE` です。素のバイナリ `looptrack_<version>_<os>_<arch>[.exe]` と単独の `install.sh` と `grants.sql` はもうリリースの添付物ではありません。書庫にも入っていません。デスクトップ版の添付物は名前を変えず、`NOTICE` と `OFL-BIZUDGothic.txt` も引き続き単独で添付します。`SHA256SUMS` には書庫に加えて、各書庫の中のバイナリも前の名前（`looptrack_<version>_<os>_<arch>[.exe]`）で載せています。書庫から取り出したバイナリも署名つきの一覧で照合できます。これらの行は添付されていないファイルを指します。照合には 1 行だけを `grep … | sha256sum -c -` に渡すか `sha256sum -c --ignore-missing SHA256SUMS` を使ってください。
- サーバの配布ディレクトリと `self-update` は変わりません。`LOOPTRACK_DIST_DIR` に置くのはこれまでどおり `looptrack_<version>_<os>_<arch>[.exe]` という名前の素のバイナリです。そこに書庫を置いても一覧にも配布にも載りません。公式のリリースを配るなら、各書庫から `looptrack` を取り出してその名前で置き、リリースの `SHA256SUMS` と `SHA256SUMS.minisig` をそのまま隣に置いてください（更新のガイドの「利用者に配る looptrack（配布ディレクトリ）」）。
- MySQL: インストーラが権限を同じ流れで付けます。MySQL を最小の権限で使うとき、これまでの `install.sh` は `looptrack setup` の後で止まって `grants.sql` を自分で実行してからもう一度実行するよう求めていました。いまはその時点で管理者の資格情報を端末で尋ね、`looptrack grants apply` を実行します。アプリケーションのユーザが読めることを確かめたら、そのままサーバの起動と確認に進みます。資格情報が違っていれば何も起動せずに止まります。もう一度実行するとその問いから続きます。新しい版がテーブルを足すときは `--upgrade` も同じことをします。端末なしで動かすとき（systemd に限る）は、`LOOPTRACK_INSTALL_DB_ADMIN_USER` と `LOOPTRACK_INSTALL_DB_ADMIN_PASSWORD_FILE` を渡してください。
- MySQL: `looptrack setup` とインストーラが無いデータベースを作ります。接続文字列のデータベースがまだ無いと、setup は移行の前の接続の段階で止まっていました。インストーラはデータベースを作るところまでたどり着けませんでした。いまの setup はデータベースが無いことに気づくと、管理者の資格情報を端末で 1 度だけ尋ねます。表示も保存もしません。続けてデータベースを作るかを尋ね、これは `--yes` を付けていても尋ねます。作るときは `looptrack grants apply` と同じやり方で、データベース・アプリケーションのユーザ・テーブル・権限を作ってから先へ進みます。既にあるデータベースはそのまま。no と答えるか端末なしで実行すると、何も作らずに止まって自分で実行する `CREATE DATABASE` の文を示します。
- 1.0.0 が出るまではリリース候補を Latest にします。安定版の `vX.Y.Z` のタグができるまで、リリース候補は GitHub Releases で Latest のリリースとして公開します。プレリリースにはしません。GHCR では `latest` のタグを付けます。`releases/latest` を読む 1 行のインストーラが入れるのは、そのリリース候補です。1.0.0 の後に出すリリース候補は、プレリリースに戻ります。
- 検証の記録は記録した利用者の言語で書きます。`verify`（と MCP の `report_verify`）が残すコメントは、記録した人の言語で書かれます。言語の選び方はその利用者に見せるほかのものと同じです。既に保存された記録は変わりません。英語では MCP 経由で報告された結果の印が、どこでも「self-reported via MCP」になりました。
- 管理者向け: プロジェクトを作るとき、省いた値をどの経路でも同じように埋めます。`looptrack project create` は `--prefix` と `--name` を必須にしなくなりました。省くと管理ページや MCP の `create_project` と同じく、slug の大文字と slug になります。`looptrack setup` が作る最初のプロジェクトもほかと同じ既定の並び順（100）です。明示した `--order 0` は 0 のまま残ります。
- loop kit: `pre-tool-subagent-bound` がサブエージェントに識別子の照合も求めます。サブエージェントの指示文に足す注意に 1 つ加わりました。中の識別子（変数名や関数名・コミットの SHA・行番号・ファイルのパス）はどれも使う前に `git show` / `git grep` で実物と照合し、食い違いは 1 行で報告すること、です。自動では何も足されないエージェントのために、`background-process.md` にも同じ一文を載せています。
- loop kit: rules に作業規律が増えました。`working-discipline.md` に加わった規律は次のとおりです。共有の部品の両側を数えること・方法を変えて確かめ直すこと・集計の緑が何を示し何を示さないか・「X は起きない」テストの対照・ガードが取りこぼす形を探ること。変更が記録する golden を回すこと、rebase の直後にビルドと型の検査を回すこと、rules と状態をサブエージェントへ降ろすこと（指示文に写す定型つき）も入っています。

### 修正

- **CLI: `self-update` が一覧に載った版を署名と突き合わせます。** これまでは署名の無い配布の一覧にある版だけを信じていました。そのため一覧が古いリリースの正しく署名された `SHA256SUMS` と高い版番号を組み合わせれば、CLI を古い版へ戻せてしまいました。いまは署名つきの `SHA256SUMS` にあるファイル名が、一覧の版・OS・CPU と一致することを求めます。リリースなら、署名の trusted comment にある版も比べます。一致しなければ何も入れ替えません。古い版へ戻すにはこれまでどおり `--force` が要ります。
- 新しい looptrack が移行したデータベースでは、古い looptrack は動きません。`migrate`・`setup`・ローカルモード・チームのサーバの `serve` は、自分の知らない移行が適用されたデータベースを拒否するようになりました。デスクトップ版も同じです。これまでは新しい形のデータをそのまま使っていました。戻すには、新しい版が移行する前に取った控えからデータベースを戻してください。`install.sh --upgrade` の移行が何かを適用した後で失敗したときも、インストーラは控えからデータベースを戻すよう案内するようになりました。
- AI エージェントの導入: MCP の `setup` が、既に入っている `looptrack` を入れ替えなくなりました。`~/.local/bin/looptrack`（Windows では `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`）があれば、返すコマンドはそれをダウンロードも入れ替えもしません。それで `init` を実行するだけです。古い版を配るサーバが手元の新しい版を古い版に戻すことはもうできません。手元のほうが古いときは、次の `setup` が `self-update` を勧めます。
- Codex: MCP の `setup` が環境変数を前に付けます。Codex は、`init` が `.codex/config.toml` に書いた変数を再起動の後でしか読みません。そこで Copilot と同じく、返すコマンド（トークンの確認を含む）に `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` を付けるようにしました。
- トークンの集計: イベントを記録する操作は、どれも集計に入ります。MCP の `next`（イシューに着手したとき）・`report_verify`・`assign_issue` と、担当が変わったときの CLI の `issue assign` にもトークンを付けるようにしました。これらが帰属なしとして出ることはもうありません。送れずに後で送り直したスナップショットは、付けられる期間を過ぎていても元の操作に付きます（移行 `0005_usage_snapshots_attempted_at`）。新しい欄を知らないサーバにはその欄を除いたスナップショットをもう一度送ります。未送信のスナップショットはサーバとプロジェクトごとに保ち、取ったプロジェクトにだけ送り直す仕組みです。その情報を持たない古いものは送り直さずに 7 日で捨てます。サインアウト中に取ったスナップショットは保っておき、`looptrack issue login` の後に送ります。送信の失敗も黙らなくなりました。セッションの開始の要約が最後の失敗と、送信を待つスナップショットの数を知らせます。
- 管理者向け: エクスポートとインポートが、アーカイブしたプロジェクトを尊重します。`looptrack export` は `--archived` を付けない限り、アーカイブしたプロジェクトを含めません。`looptrack import` はアーカイブしたプロジェクトへの取り込みを拒否します。置き換えるとそこに残したデータが消えるからです。先にアーカイブを解いてください。
- AI エージェントの hook が包まれたコマンドの中まで見ます。イシューの鮮度ガードと引き継ぎの完了の記録は、`git commit` / `push` / `merge` と `looptrack issue …` の呼び出しと完了（`close`・`status Done`）を見分けます。これらを次の形でも見分けるようになりました。`bash -c` / `sh -c` / `eval` で包んだもの・`sudo`・`env`・`nohup`・`xargs` のような前置の語の後ろにあるもの・`function` の中にあるもの・行の継続で分けたものです。入れ子のシェルと前置の語は git ガードや秘密のガードと同じやり方でほどきます。別の作業ツリーで引き継ぎのファイルだけをコミットした場合も、正しく見分けます。
- loop kit の暴走プロセスのガード: 引用符の無い `ps` の行を待ちループとして判定します。`ps` はコマンド行から引用符を落とします（`/bin/bash -c while true; do sleep 1; done`）。そのせいで、上限の無い待ちループ向けの短い閾値をすり抜けていました。
- loop kit のタスクモードの hook: サブエージェントの報告でモードが切り替わらなくなりました。Claude Code はサブエージェントの最終報告を `<agent-message …>` で包んで UserPromptSubmit に通します。その中の語で調査モードと実行モードが切り替わることはもうありません。その中のイシュー ID も鮮度ガードにとって利用者が触れたものには数えません。
- AI エージェントの hook: Go（RE2）が読めない正規表現を知らせます。`LOOPTRACK_LOOP_TASK_MODE_EXEC_RE`・`_INVEST_RE`・`LOOPTRACK_LOOP_RUNAWAY_ALLOW`・`LOOPTRACK_MCP_SERVER` は先読みなどを使って書くと黙って無視されていました。いまは hook が変数名とエラーをメッセージとして示し、その値を使わずに続けます。既定の語と除外は引き続き効きます。`LOOPTRACK_MCP_SERVER` は何にも一致せず、知らせるのは MCP のツールの呼び出しのときだけです。ガイドにも、これらが RE2 だと書きました。
- CLI: `looptrack issue init` が余計な `.gitignore` の行を足さなくなりました。作業ツリーの中の `.gitignore` にある否定でない規則で `.claude/.looptrack-freshness/` が既に無視されていれば、init は最上位の `.gitignore` に手を付けません。`.git/info/exclude` やグローバルの除外ファイルにしか無い規則は数えません。ほかのクローンとは共有されないからです。
- `install.sh` で入れたサーバでの `self-update` の英語の拒否が、実在する見出しを指します。指す先は更新のガイドの「Server」の節です。

### 文書

- **新しいガイド「トークンレポート」。** 何を記録するか・いつ送るか・何を決して送らないかを書きました。止め方・帰属と帰属なしの使用量の取り戻し方・レポートの依頼・PDF を手で作る方法・台帳・設定も説明しています。README からリンクしました。
- 「更新」のガイドで、新しい版のお知らせとサーバの自動更新を説明します。自動更新が見送られるのはどんなときか、そのときどうするかまで書きました。
- README とガイドの導入で、Looptrack をループエンジニアリングを可能にする AI のコーディングエージェントの外部記憶として説明するようにしました。README の Web UI の説明も、いまの画面に合わせています。

### 1.0.0-rc.1 / rc.2 から上げるとき

- `v1.0.0-rc.1` と `v1.0.0-rc.2` の添付物はそのまま残します。
- rc.1 や rc.2 の `install.sh` で入れたサーバは、新しい 1 行で上げます: `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade`。この 1 行は、それらが残した `/etc/looptrack/install.conf`・`.env`・systemd のユニット（または `compose.yaml`）をそのまま読みます。古い `install.sh` を `--from` で新しいリリースに向けても、何も入れ替えずに止まります。リリースに素のバイナリがもう無いからです。
- `…/releases/download/<version>/looptrack_<version>_<os>_<arch>` を直接ダウンロードするスクリプトは、代わりに `_server` の書庫をダウンロードして展開しなければなりません。macOS・Linux・Windows の手順は「始め方」のガイドにあります。
- この版で移行 `0005`（`usage_snapshots` の列）と `0006`（テーブル `issue_event_sessions`）が入ります。`issue_event_sessions` は追記のみで、アプリケーションのユーザが持つのは `SELECT` と `INSERT` だけです。MySQL を最小の権限で使っているなら、`install.sh --upgrade` が管理者の資格情報を尋ねて新しいテーブルに権限を付けます。`looptrack setup` だけで用意したサーバでは起動する前に `looptrack migrate` を実行し、それから `looptrack grants apply`（または `looptrack grants print` が出す文）を実行してください。
- この版がデータベースを移行したら、そこに rc.1 や rc.2 を戻さないこと。どちらも新しい移行を確かめる仕組みより前の版なので、新しい形のデータの上で動いてしまいます。代わりに更新の前に取った控えを戻します。
- `looptrack self-update` の後は各プロジェクトで `looptrack issue init` をもう一度実行してください。Claude Code のプロジェクトに、新しい `issue-session-bind` の hook が入ります（`--no-session-bind` なら入れない）。

## [1.0.0-rc.2] - 2026-09-25

Second release candidate for 1.0.0, relative to `v1.0.0-rc.1`. Administrators
can rename, archive and restore projects, and an AI agent can create a project
over MCP. MCP behind nginx no longer stalls.

### Added

- **Administrators: MCP `create_project` tool.** Creates a project and adds the
  caller as its admin in one transaction, through the same code path as the
  admin page (`POST /admin/projects`); the values follow the rules of
  `looptrack project create`. When `setup` cannot find the project, its error
  tells an administrator that `create_project` can create it (confirming the
  slug, prefix and width with the user first, since the prefix and width cannot
  be changed later) and tells anyone else to ask an administrator.
- Administrators: rename, archive and restore projects.
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
- Loop kit: `pre-tool-subagent-model` hook. Stops a Claude Code subagent
  launch (`Task`/`Agent`) with `deny` when no `model` is given, and shows how
  to pick one by difficulty (haiku / sonnet / opus). Skips `subagent_type:
  fork`, a type whose definition file already sets `model:`, and names listed
  in `LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW`.

### Changed

- **The remaining Japanese-only messages are in English too.** They follow
  the language of the connection (MCP) or of the terminal (CLI,
  `LOOPTRACK_LANG`), like the rest of Looptrack: the MCP prompts (`loop`,
  `review`, `setup`) and their errors; the messages about assignment,
  membership and usage-sending settings, and the `report_verify` guidance;
  errors and warnings about credentials, keys, passwords and the local server
  (server log warnings use the language of whoever started the server); errors
  from importing and from reading Markdown and JSON; the comments in the
  `compose.yaml` and `Dockerfile` that `looptrack setup` writes, and the loop
  section `looptrack issue init` writes into `AGENTS.md`; and the hooks'
  input/output errors, the `verify` command runner and the Copilot usage hint.
  Words that are matched as input (such as the `## コメント` heading and the
  answer `はい`) are unchanged.

### Fixed

- **Behind nginx: MCP responses are no longer buffered.** The server sends
  `X-Accel-Buffering: no` on every `/mcp` response, so the SSE stream of
  `subscriptions/listen` reaches the client even when nginx keeps its default
  `proxy_buffering on` (clients such as Copilot CLI gave up after 10 seconds).
  DEPLOY.md also asks for `proxy_buffering off;` in a hand-written nginx
  configuration.
- Loop kit, git guard: an unquoted Windows absolute path to `git.exe` is
  recognised. `pre-tool-git-guard` reads the command both with POSIX
  escaping and with Windows rules (a backslash is not an escape), so
  `C:\tools\git.exe add -A` is judged like `git add -A` instead of slipping
  through. Git Bash behaves exactly as before. A path containing a space
  still has to be quoted to be recognised.
- Loop kit, secrets guard: a glob whose name is visible is confirmed.
  `pre-tool-secrets-guard` asks before a command that reads or copies
  `.env*` or `id_rsa*` (what remains after dropping the trailing glob names a
  secret). Listing-only commands such as `ls` are not affected, and globs that
  hide the name (`*`, `.e*`) are still not caught.
- Loop kit, secrets guard: an apostrophe in a trailing comment no longer
  breaks the template exemption. `cp .env.example .env  # don't …` was taken
  for an unterminated quote and confirmed; a `#` comment outside quotes is
  skipped.

### Docs

- **Installing on a remote server by pasting one prompt.** The AI agents guide
  gains ready-to-paste prompts for Claude Code, Codex, GitHub Copilot (VS Code)
  and Copilot CLI, what to prepare, where your hands are still needed (the
  install command in Claude Code even in auto mode, a second restart of Copilot
  CLI after authorisation, the token check in Codex before it restarts), and
  what to check when it goes wrong. The prompts pass `project` to the `setup`
  tool (Codex shares one MCP configuration across projects), and the token is
  never handed to the agent (sign-in uses `looptrack issue login --browser`).
- New "Updating" guide. What is automatic and what is manual when updating
  the CLI (`self-update` and the notice about updated distribution scripts),
  the server (`install.sh --upgrade`, or a server set up with `looptrack setup`
  alone), and the looptrack you distribute to users.

---

1.0.0 のリリース候補の 2 つ目で、下の変更は `v1.0.0-rc.1` からの差分です。管理者はプロジェクトの名前の変更・アーカイブ・復元ができるようになり、AI エージェントは MCP でプロジェクトを作れます。nginx の後ろで MCP が止まる問題も直りました。

### 追加

- **管理者向け: MCP の `create_project` ツール。** プロジェクトを作って呼び出した人をその管理者に加えるまでを、1 つのトランザクションで行います。通る道は管理ページ（`POST /admin/projects`）と同じで、値は `looptrack project create` の規則に従います。`setup` がプロジェクトを見つけられないときのエラーも変わりました。管理者には `create_project` で作れることを伝え、それ以外の人には管理者に頼むよう伝えます。プレフィックスと桁数は後から変えられないので、作る前に slug・プレフィックス・桁数を利用者に確かめるよう添えています。
- 管理者向け: プロジェクトの名前の変更・アーカイブ・復元。`/admin/projects` の各プロジェクトに「表示名とアーカイブ」が加わりました。表示名を変えるか（slug・プレフィックス・発番済みの ID はそのまま）、slug を打ち込んで確かめたうえでプロジェクトをアーカイブできます。アーカイブは論理削除です。プロジェクトはハブ・REST・MCP・CLI のどの一覧からも消え、新しいイシュー・更新・コメントはどの経路でも拒否されます。イシュー・コメント・履歴は残ります。ページの下の「アーカイブしたプロジェクト」から復元できます。サーバでは `looptrack project rename <slug> <display name>` で表示名を変えます。`looptrack project archive|unarchive <slug>` でページと同じことをします。`looptrack project list` は `--archived` を付けない限りアーカイブしたプロジェクトを出しません。移行 `0004_projects_archived` が `projects.archived_at` を足します。データベースの権限は変わりません。
- loop kit: `pre-tool-subagent-model` hook。`model` を指定せずに Claude Code のサブエージェント（`Task`/`Agent`）を起動すると `deny` で止め、難しさに応じた選び方（haiku / sonnet / opus）を示します。止めないのは `subagent_type: fork`・定義ファイルが既に `model:` を決めている型・`LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW` に挙げた名前の 3 つです。

### 変更

- **日本語だけだった残りのメッセージにも、英語が付きました。** ほかの Looptrack と同じく、接続（MCP）か端末（CLI・`LOOPTRACK_LANG`）の言語に従います。対象は次のとおりです。MCP のプロンプト（`loop`・`review`・`setup`）とそのエラー。担当・メンバー・使用量の送信の設定についてのメッセージと、`report_verify` の案内。資格情報・鍵・パスワード・ローカルのサーバについてのエラーと警告で、サーバのログの警告はサーバを起動した人の言語になります。インポートと、Markdown・JSON の読み込みのエラー。`looptrack setup` が書く `compose.yaml` と `Dockerfile` のコメントと `looptrack issue init` が `AGENTS.md` に書く loop の節。それに hook の入出力のエラー・`verify` のコマンドの実行部・Copilot の使用量のヒント。入力として照合される語（`## コメント` の見出しや、答えの `はい` など）は変えていません。

### 修正

- **nginx の後ろ: MCP の応答をバッファしなくなりました。** サーバは `/mcp` のすべての応答に `X-Accel-Buffering: no` を付けます。だから nginx が既定の `proxy_buffering on` のままでも、`subscriptions/listen` の SSE のストリームがクライアントに届きます。Copilot CLI のようなクライアントは、これまで 10 秒で諦めていました。DEPLOY.md でも手で書く nginx の設定に `proxy_buffering off;` を求めるようにしました。
- loop kit の git ガード: 引用符の無い Windows の絶対パスの `git.exe` を認識します。`pre-tool-git-guard` はコマンドを POSIX のエスケープと Windows の規則（バックスラッシュはエスケープではない）の両方で読むようになりました。そのため `C:\tools\git.exe add -A` はすり抜けずに `git add -A` と同じように判定されます。Git Bash での動きは前とまったく同じ。空白を含むパスはこれまでどおり引用符で囲まないと認識されません。
- loop kit の秘密のガード: 名前が見えているグロブを確認します。`pre-tool-secrets-guard` は `.env*` や `id_rsa*` を読む・写すコマンドの前に尋ねるようになりました。末尾のグロブを落とした残りが秘密を名乗っているからです。`ls` のような一覧だけのコマンドには影響しません。名前を伏せるグロブ（`*`・`.e*`）はこれまでどおり捕まえません。
- loop kit の秘密のガード: 行末のコメントのアポストロフィで、雛形の例外が壊れなくなりました。`cp .env.example .env  # don't …` は閉じていない引用符と取り違えられて確認になっていました。いまは引用符の外の `#` のコメントを飛ばします。

### 文書

- **プロンプトを 1 つ貼るだけでリモートのサーバにつなげます。** 「AI ごとの手引き」のガイドに、Claude Code・Codex・GitHub Copilot（VS Code）・Copilot CLI 向けの貼るだけのプロンプトが加わりました。用意するもの・まだ手を動かす必要がある所・うまくいかないときに確かめることも書いています。手を動かす所は auto モードでも Claude Code で要るインストールのコマンド・認可の後の Copilot CLI の 2 回目の再起動・再起動の前の Codex でのトークンの確認です。プロンプトは `setup` のツールに `project` を渡します。Codex は 1 つの MCP の設定をプロジェクトの間で共有するためです。トークンはエージェントに決して渡しません（サインインには `looptrack issue login --browser` を使う）。
- 新しいガイド「更新」。更新するとき何が自動で何が手作業かは、CLI（`self-update` と、更新された配布スクリプトについてのお知らせ）・サーバ（`install.sh --upgrade`、または `looptrack setup` だけで用意したサーバ）・利用者に配る looptrack のそれぞれについて説明します。

[Unreleased]: https://github.com/howashoji/looptrack/compare/v1.3.0-rc.1...HEAD
[1.3.0-rc.1]: https://github.com/howashoji/looptrack/releases/tag/v1.3.0-rc.1
[1.2.0]: https://github.com/howashoji/looptrack/releases/tag/v1.2.0
[1.2.0-rc.1]: https://github.com/howashoji/looptrack/releases/tag/v1.2.0-rc.1
[1.1.0]: https://github.com/howashoji/looptrack/releases/tag/v1.1.0
[1.1.0-rc.1]: https://github.com/howashoji/looptrack/releases/tag/v1.1.0-rc.1
[1.0.2]: https://github.com/howashoji/looptrack/releases/tag/v1.0.2
[1.0.1]: https://github.com/howashoji/looptrack/releases/tag/v1.0.1
[1.0.1-rc.1]: https://github.com/howashoji/looptrack/releases/tag/v1.0.1-rc.1
[1.0.0]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0
[1.0.0-rc.5]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.5
[1.0.0-rc.4]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.4
[1.0.0-rc.3]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3
[1.0.0-rc.2]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.2
