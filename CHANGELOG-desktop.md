# Changelog: Looptrack desktop app

This file records the changes that matter to people who use the Looptrack
desktop app, including the CLI that comes with it and the AI agents connected
to it. Changes for people who run a server, administer one, or connect to one
are in [CHANGELOG.md](CHANGELOG.md). Both editions share one version number and
one tag, and a change that affects both is written in both files.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

Looptrack のデスクトップ版の変更履歴。同梱の CLI と、アプリにつないだ AI エージェントに効く変更も、ここに入ります。サーバを動かす人・管理する人と、CLI や AI エージェントでチームのサーバにつなぐ人に向けた変更は、別のファイル [CHANGELOG.md](CHANGELOG.md) に分けて記録しています。2 つの版は版番号とタグを共有するので、両方に効く変更は両方のファイルに書きます。

形式は [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) に基づき、版の付け方は [Semantic Versioning](https://semver.org/spec/v2.0.0.html) に従います。

## [Unreleased]

Nothing yet.

---

まだありません。

## [1.0.1-rc.1] - 2026-10-05

First release candidate for 1.0.1, relative to `v1.0.0`. Because it comes
after 1.0.0, it is published as a pre-release, and `v1.0.0` stays the Latest
release on GitHub Releases. An app on 1.0.0 follows stable releases, so it
does not offer this version. To try it, download it from the release page.

### Changed

- **The SQLite driver is now `modernc.org/sqlite` 1.60.1.** The app keeps
  your data in a single SQLite file and reads and writes it with this driver.
  It moved up from 1.59.0, and `modernc.org/libc` from 1.75.7 to 1.77.1. The
  new driver ships its own list of third-party licenses
  (`LICENSE-3RD-PARTY.md`), and `NOTICE` now carries it in full.

---

1.0.1 のリリース候補の 1 つ目で、下の変更は `v1.0.0` からの差分です。1.0.0 の後に出す版はプレリリースとして公開します。GitHub Releases の Latest は `v1.0.0` のまま。1.0.0 のアプリは正式版だけを追うため、この版を知らせません。試すときは、リリースのページから取得してください。

### 変更

- **SQLite のドライバを `modernc.org/sqlite` 1.60.1 に上げました。** アプリはデータを 1 つの SQLite のファイルに置き、このドライバで読み書きします。前の版は 1.59.0 です。あわせて `modernc.org/libc` も 1.75.7 から 1.77.1 に上がりました。新しいドライバには第三者のライセンスの一覧（`LICENSE-3RD-PARTY.md`）が付いています。`NOTICE` にもその全文を収めました。

## [1.0.0] - 2026-10-03

1.0.0 is the first release that is not a release candidate. The changes below
are relative to `v1.0.0-rc.5`; "What 1.0.0 ships" at the end of this section
is an overview of the whole desktop app, and the sections of the release
candidates below record what changed between them. The biggest change since
rc.5 is evidence: issues can carry attached files, and closing an issue that
has verify commands needs one by default. On Windows, install this one update
by hand from the release page ("Moving from 1.0.0-rc.5" below).

### Added

- **Attach evidence files to an issue in the app.** Test output, screenshots
  and generated reports can now be attached to an issue as files, with who
  attached them and when. From the app's CLI, use
  `looptrack issue attach <ID> <file>...`, or `--attach <file>` on
  `issue comment` and `issue verify`; `verify --attach-output` puts the full,
  untruncated output on the verify record. `verify --list` and `--last` can't
  be combined with `--attach` or `--attach-output`; the CLI says so and sends
  nothing. The MCP tools take no files, so an
  agent sends them with the CLI and passes the IDs in `attachments` of
  `report_verify` or `add_comment`. In the app's window the comment box takes a
  file you pick, drop or paste, and the drawer lists the attachments and shows
  images in place. The CLI refuses to send a text file that looks like it holds
  a secret. The files go in the `attachments` folder inside the data folder,
  next to `looptrack.db`, which only holds their names and sizes, so copy that
  folder too when you back up. `looptrack export` with
  `LOOPTRACK_DSN=sqlite:<path to looptrack.db>` writes the attachment files and
  a manifest (`<slug>/attachments.json`) next to the Markdown, taking the files
  from the `attachments` folder beside the database, and `looptrack
  verify-files` checks them; `looptrack import` doesn't carry attachments.
  Attachments can't be deleted; the admin page `/admin/attachments` purges a
  file and sets the limits (20MiB per file and 1GiB per project by default).
- **Windows: "Update to version <version>" replaces the app, too.** On
  Windows the tray menu and the strip in the web UI used to open the release
  page; now they update the app in place, as on macOS and Linux, and "Install
  updates automatically" appears in the tray. If you used the installer, the
  app downloads the new `Looptrack_<version>_windows_<arch>_setup.exe`, checks
  it against the signed `SHA256SUMS`, and runs it with no window. Your options
  ("Start at login", "Make the CLI available") carry over. The installer starts
  the app again when it finishes, and if it fails and rolls back, it starts the
  previous version; the reason is in `updates\setup.log` in the data folder. If
  you extracted the zip, the app downloads the new zip, checks it the same way,
  and replaces `Looptrack.exe`, `cli\looptrack.exe`, `NOTICE` and
  `OFL-BIZUDGothic.txt` one by one, keeping each old file as `.prev` beside it.
  If any of them can't be replaced, the ones already done are put back. When the
  folder isn't writable, the release page opens as before. The Windows files
  carry no code signature, so the check is the signed SHA-256. Whether Smart
  App Control, Microsoft Defender or SmartScreen stops such an update hasn't
  been tried on a real PC yet; if an update doesn't finish, update by hand.
- **CLI: `looptrack self-update` checks GitHub releases instead of stopping
  with "No server URL".** With neither `--url` nor `LOOPTRACK_API_URL`, the
  CLI picks a version from the GitHub release list the same way the desktop app
  and the server do (semver tags only, never `/releases/latest`, rcs too when
  you run an rc). What that means for the app's CLI depends on the OS. On macOS
  and Linux the CLI is the app itself, which still refuses to replace itself:
  update the whole app, and use `looptrack self-update --check` to see whether
  a newer release exists. On Windows the CLI in the zip (`cli\looptrack.exe`,
  and the copy the app places in `%LOCALAPPDATA%\Programs\looptrack`) is a
  plain CLI build, so `self-update` can replace that copy with the newer
  release's `looptrack`; the app itself is still updated as a whole. The
  replacement takes the server archive for your OS and CPU, checks it against
  the signed `SHA256SUMS`, extracts only the `looptrack` inside, and checks
  that file against its own line in the same `SHA256SUMS` before it replaces
  the running one (`.old` on Windows, as before). The signature is mandatory: a
  build without the public key stops without contacting GitHub, and so does any
  build with `LOOPTRACK_UPDATE_CHECK=off`. This path replaces the file only when
  a newer version exists, so it refuses `--force` (exit code 2).
  `--from github|server` picks the source explicitly, and a failure on one never
  falls back to the other. With `--url` or `LOOPTRACK_API_URL`, nothing changes.
- **The board in the app remembers how you left it.** The view, the search
  words and the filters (type, priority, ready only, hide closed, unanswered
  feedback, label and assignee) are kept per project in localStorage and come
  back the next time you open that project. The sort order was already kept.
  A label or an assignee the project no longer has is dropped when the board
  loads.
- **The browser tab shows the app's icon.** Every page of the app's web UI
  links the same picture as the app's icon.

### Changed

- **Closing an issue as Done needs evidence by default, in the app too.** On an
  issue whose body holds at least one verify command, the app's local server
  rejects Done unless the latest verify record for the current body carries an
  attachment, and it rejects a missing record too. This is the new per-project
  rule `verify.require_evidence`, on by default even in a project with no rules
  at all. It applies on every path (CLI, MCP, REST, Web) and can be overridden
  with a reason (`--override "reason"`, `override_reason` over MCP), which
  leaves a `rule_override` with `rule: verify_evidence_required`. A project
  that wants the old behavior sets `"verify": {"require_evidence": false}` with
  `looptrack project rules set` and gets a note instead of a rejection. The
  guide, the MCP instructions, the verify plan, the close step of `next` and the
  kit tell the AI to attach the full test output, screenshots and generated
  files.
  An issue created directly as Done with a verify command in its body is
  rejected the same way, since it can't have a record yet. A record whose
  attachment is purged later still counts as having evidence. Versions up to 1.0.0-rc.5 have no `--attach`, `--attach-output` or
  `issue attach`, and a version older than this one rejects rules that contain
  the key, so remove it before going back to an older version.
- **Windows: `Looptrack.exe` and `cli\looptrack.exe` carry version
  information.** Both exes in the desktop zip have a VERSIONINFO resource. Its
  ProductName is `Looptrack` and its ProductVersion is the build version as
  is, release candidates included. The release build reads both values back
  from every exe it makes and stops if either is missing or wrong. The tool
  that writes the resource changed from rsrc to go-winres (0BSD, build-only).
  The desktop exes keep their icon.
- **The app rejects comments with no text and no attachment, too.** The
  server inside the app now answers a comment whose text is empty or only
  whitespace, with no attachment, with a 400 (`comment_empty`), whether it
  comes from the app, the CLI or an AI agent. Comments cannot be deleted, so
  one empty comment would stay for good. A script of your own that sent empty
  comments will now get that error. Attachment-only comments still work (over
  REST, send `"text": ""`). When you change an issue's status with a
  whitespace-only comment, no comment is created: the status changes and the
  response has no "Comment added" line.
- **The Japanese text that `init` writes, and the kit's rules and skills,
  use one consistent style.** The section `looptrack issue init` puts into
  `CLAUDE.md` and `AGENTS.md`, the five rules of the loop layer and the
  `issue`, `iterate` and `session-handoff` skills were rewritten in the polite
  form throughout, with asides in parentheses turned into sentences.
  Commands, paths and line counts are unchanged, so the next init or kit
  update changes only that wording. The English working-discipline rule had
  the same asides rewritten.

### Fixed

- **macOS: the app's small icon no longer shows as noise in Finder.** In the
  small views (16 and 32 px, such as list view), `Looptrack.app` showed a
  garbled picture instead of the icon. The two smallest sizes in the icon file
  were stored in a form macOS reads as raw pixels, not as images. They are now
  stored in the form macOS reads correctly, and the larger sizes are unchanged.
  Update the app as a whole, as usual.
- **The issue drawer in an English browser shows the comments heading in
  English.** Background, Details and the other sections already followed the
  screen's language, but the comments section kept its Japanese heading. The
  stored body still uses that heading as the marker that separates the body
  from the comments, so only the browser swaps it for the screen's language
  when it draws the drawer. Nothing stored changes.
- **The project picker in English says "1 project" when there is one.** The
  line under the heading read "1 projects". It now uses the singular for one
  project and the plural otherwise. The Japanese line is unchanged.
- **English messages say "1 issue", not "1 issues", when the count is one.**
  More than 30 messages in the CLI, the REST API, the MCP tools and the
  reports printed a count of one in the plural, such as "1 issues" or "1 files".
  They now use the singular for one, and keep the plural for zero and for two
  or more. Messages already written with "(s)" and all the Japanese text are
  unchanged.
- **AI agent hooks: the guards catch commands hidden behind a heredoc,
  `$'…'` or a comment, and decide on long commands too.** The git, secrets,
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
- **AI agent hooks: the handoff hook no longer misses a `close`.** The hook
  that notices finished work, so that the handoff check asks for an update,
  skipped a `close` on the line after a comment, after a `<<` inside quotes
  or arithmetic, or in the body of `cat <<EOF | sh`. It reads heredocs and
  comments the same way as the guards do.
- **`looptrack handoff -h` and `--help` print the usage.** `handoff append -h`
  used to append `-h` to the handoff and succeed. `-h` and `--help`, before or
  after the subcommand, now print the usage and write nothing, and a word like
  `-x` is rejected as a mistyped option. Anything after `--` still goes in as
  text.
- **In a clone of Looptrack's own repository, setup and `init` no longer end
  in an error.** There, `init` used to fail, so the setup tool's
  download-and-init command ended in an error even though `looptrack` had
  already been replaced. For that repository the setup tool returns only the
  download step, and `init` writes nothing, says it isn't needed and
  exits with 0. `doctor` no longer suggests running it.
- **`init` writes the guidance section in CLAUDE.md and AGENTS.md in English
  for English users.** The section went in Japanese whatever the language. It
  now follows the language of the rest of `init`'s output, read from
  `LOOPTRACK_LANG`, `LC_ALL`, `LC_MESSAGES` and `LANG` in that order. Running
  `init` again in the other language replaces the section rather than adding
  a second one, and anything written outside it stays.
- **The admin project list in English reads "your role here: admin".** It
  said "you are a admin here", because the article was wrong for any role
  that starts with a vowel. The Japanese line is unchanged.

### Docs

- **Windows: what to do when it blocks the unsigned app outright.** Getting
  started, the README, the FAQ and the desktop guide cover Smart App Control on
  Windows 11, which blocks unsigned apps with no "Run anyway" and no way to
  allow a single app, and PCs whose administrator blocks unsigned apps by
  policy. The FAQ also points to checking `SHA256SUMS` itself against
  `SHA256SUMS.minisig`.
- **Windows: the guide now says the CLI can be updated with `self-update`, and
  what that leaves behind.** On Windows, `looptrack self-update` replaces the
  CLI (`cli\looptrack.exe` and the copy in `%LOCALAPPDATA%\Programs\looptrack`)
  with the newest release's `looptrack`, and never touches `Looptrack.exe`. On
  macOS and Linux it refuses, because the CLI is the app itself. A replaced
  Windows copy is no longer refreshed when you update the app, so the CLI and
  the app can end up on different versions. Uninstalling doesn't remove it
  either. The desktop update page now covers this, including how to delete the
  copy or go back to the one that comes with the app, and the server update
  page and the design document no longer say the desktop app never uses
  `self-update`. Nothing in the app's behavior changes.
- **Screenshots in the user guide and the README.** Getting started for the
  desktop app and the server, the daily-use page and the README now show the
  screens they describe, in the language of each page. Every picture is taken
  by `deploy/dev/screenshots.sh` from a throwaway server that holds only
  made-up data.
- **The desktop app has its own pages in the user guide.** They are under
  `docs/guide/desktop/`: getting started, using, updating and
  troubleshooting, split out of the one desktop page. The old pages remain as
  short pages that link to the new ones, so links in the installers and
  READMEs up to rc.5 still work.
- **Two changelogs.** Changes for the server are in `CHANGELOG.md` and those
  for the desktop app in `CHANGELOG-desktop.md`, and a change that affects
  both is in both. A GitHub release takes the same version from both files
  and shows them under "Server" and "Desktop app".

### Moving from 1.0.0-rc.5

- Update the app as a whole, as usual. On Windows the rc.5 app still opens
  the release page for this update, so install 1.0.0 from there by hand.
  Updating in place works from the next update on.
- When you back up, copy the `attachments` folder in the data folder along
  with `looptrack.db`.

### What 1.0.0 ships

- **Desktop edition.** A double-clickable application for macOS, Windows and
  Linux that starts the server locally, keeps a tray / menu-bar entry, offers to
  put the CLI on `PATH`, and can start at login without administrator rights.
  The server it starts is a single-user server bound to 127.0.0.1 with SQLite
  and no sign-in.
- **Cross-project issue tracking.** The app holds the issues of many projects.
  Each project has its own ID prefix and zero-padded width (`MYP-0001`), its own
  rules and its own guide document. The database is the source of truth; nothing
  is committed into the source repositories being tracked.
- **Loop-shaped workflow.** `next` picks the issue to work on and enforces the
  rules for starting it; `verify` runs the commands written in the issue and
  records the result; `summary` reports three nested loops at the start of every
  session (the current round, decisions waiting for a human, feedback coming from
  outside). Comments and events are append-only, and closed issues are immutable.
- **Per-project rules.** Required comments, forbidden status transitions and
  `verify.require_on_close` apply to every access path (REST, MCP, CLI, Web)
  because they are checked in one place.
- **MCP for AI agents**, including a `setup` tool that wires a project up from
  the MCP connection alone, and `guide` / `review` / `loop` prompts.
- **Web UI**: project picker, board / list / trace views, forms to create
  issues, change their status, comment and attach files, and account
  settings.
- **CLI (`looptrack issue`)**: create, edit, comment, status, close, list, show,
  next, verify, summary, guide, export, and `init` to wire a project up in one
  command.
- **Hooks and the distribution kit.** The `core` layer (session summary, issue
  freshness guard, token accounting) is always installed; the optional `loop`
  layer adds output/working/iteration discipline rules, task-mode and scope
  guards, handoff freshness and a runaway-process guard. Wiring is described by
  a manifest and generated for Claude Code, Codex and GitHub Copilot.
- **Token accounting.** Usage is recorded per session, per agent and per stage of
  the loop, attributed to issues, and rendered as a PDF report
  (`looptrack report pdf`) with an embedded font.
- **Signed release artifacts.** `SHA256SUMS` signed with minisign, and macOS
  binaries signed with a Developer ID and notarized. Every artifact ships a
  `NOTICE` with the full license texts of the third-party components.
- **Worktree housekeeping.** `looptrack worktree list` shows every git worktree
  with the one thing you need to decide on it — merged or not, uncommitted
  changes (ignoring build leftovers), locked, and **when it last moved** — and
  `looptrack worktree prune` cleans up. It never deletes anything unless you pass
  `--yes`, never touches the main worktree, and keeps anything that moved
  recently, because another session may still be inside it.
- **Markdown export.** `looptrack export --out <dir>` writes issues back out as
  Markdown, so the data is never locked in. Give it the app's database with
  `LOOPTRACK_DSN=sqlite:<path to looptrack.db in the data folder>` and it writes
  out what the app holds.

---

1.0.0 はリリース候補ではない最初の版です。下の変更は `v1.0.0-rc.5` からの差分で、この節の最後の「1.0.0 の全体像」にデスクトップ版の全体をまとめました。rc の間に何が変わったかは、下の各 rc の節にあります。rc.5 からのいちばん大きな変更は、エビデンスの添付。イシューにファイルを添付できるようになり、検証コマンドのあるイシューを Done にするには既定で添付が要ります。Windows では、この版への更新だけはリリースのページから手で入れてください（下の「1.0.0-rc.5 から上げるとき」）。

### 追加

- **アプリでも、イシューにエビデンスのファイルを添付できます。** テストの出力・画面のスクリーンショット・生成したレポートを、ファイルのままイシューに付けられるようになりました。誰がいつ付けたかも残ります。アプリの CLI では `looptrack issue attach <ID> <ファイル>...` か、`issue comment`・`issue verify` の `--attach <ファイル>` を使います。`verify --attach-output` は、切る前の出力の全文を verify の記録に付けます。`verify --list`・`--last` と `--attach`・`--attach-output` は一緒に使えず、CLI はそう伝えて何も送りません。MCP のツールはファイルを受け取らないので、AI は CLI で送って、返った ID を `report_verify` か `add_comment` の `attachments` に渡します。アプリの画面では、コメントの欄でファイルを選ぶ・ドロップする・貼り付けるのどれでも添付でき、ドロワーに一覧が出て、画像はその場で見られます。テキストのファイルに秘密らしいものがあれば、CLI は送らずに止まります。本体はデータのフォルダの中の `attachments` フォルダに入り、`looptrack.db` には名前や大きさの記録しかありません。バックアップではこのフォルダも一緒に写してください。`LOOPTRACK_DSN=sqlite:<looptrack.db のパス>` で `looptrack export` を実行すると、DB の隣の `attachments` フォルダから添付の本体と目録（`<slug>/attachments.json`）も書き出し、`looptrack verify-files` で確かめられます。`looptrack import` は添付を運びません。添付は消せません。管理者の画面 `/admin/attachments` で本体を消去でき、上限（既定は 1 ファイル 20MiB・1 プロジェクト 1GiB）もそこで変えます。
- **Windows でも「新しい版 <版> に更新する」でアプリが置き換わります。** これまで Windows のトレイのメニューと画面の帯はリリースのページを開くだけでした。この版からは macOS・Linux と同じくその場で置き換わり、トレイに「新しい版を自動で入れる」も出ます。インストーラで入れた人は、新しい `Looptrack_<版>_windows_<arch>_setup.exe` を取得して署名つきの `SHA256SUMS` で照らし、画面を出さずに実行します。「ログイン時に起動する」「CLI を使えるようにする」の選択肢はそのまま。インストーラは終わったところでアプリを起動し直し、失敗して元に戻したときは前の版を起動します。理由はデータのフォルダの `updates\setup.log` に残ります。zip を展開して使っている人は、新しい zip を同じように確かめてから `Looptrack.exe`・`cli\looptrack.exe`・`NOTICE`・`OFL-BIZUDGothic.txt` を 1 つずつ置き換えます。前のファイルは隣に `.prev` を付けて残します。どれかを置き換えられなければ、済んだ分を元に戻します。フォルダに書き込めないときは、これまでどおりリリースのページを開きます。Windows のファイルにはコード署名が無いので、確かめるのは署名された SHA-256 です。スマート アプリ コントロール・Microsoft Defender・SmartScreen がこの更新を止めるかどうかは、まだ実機で確かめていません。更新が終わらないときは手で置き換えてください。
- **CLI: `looptrack self-update` が、「サーバの URL がありません」で止まらずに GitHub のリリースを確かめます。** `--url` も `LOOPTRACK_API_URL` も無いとき、CLI はデスクトップ版やサーバと同じ選び方で GitHub のリリースの一覧から版を決めます。semver のタグだけを見て `/releases/latest` は使わず、rc を動かしていれば rc も追います。アプリの CLI にとっての意味は OS で違います。macOS と Linux では CLI はアプリそのもので、これまでどおり自分を置き換えません。アプリごと更新し、新しいリリースがあるかは `looptrack self-update --check` で確かめられます。Windows の zip の CLI（`cli\looptrack.exe` と、アプリが `%LOCALAPPDATA%\Programs\looptrack` に置くコピー）は普通の CLI のビルドです。そのため `self-update` でそのコピーを新しいリリースの `looptrack` に置き換えられますが、アプリ本体はこれまでどおりアプリごと更新します。置き換えるときは、自分の OS と CPU に合うサーバ版の書庫を取り、署名つきの `SHA256SUMS` で照合したうえで中の `looptrack` だけを取り出します。置き換える前には、同じ `SHA256SUMS` にある自分の行とも照らし合わせます。Windows ではこれまでどおり `.old` に退けます。署名は必須です。公開鍵を持たないビルドは GitHub に問い合わせずに止まり、`LOOPTRACK_UPDATE_CHECK=off` のビルドも問い合わせません。この経路は新しい版があるときだけ置き換えるので、`--force` を受け付けません（終了コード 2）。取得元は `--from github|server` で明示でき、片方で失敗してももう一方へは切り替えない作りです。`--url` か `LOOPTRACK_API_URL` があるときの動きは変わりません。
- **アプリのボードが、前回の表示のまま開きます。** 表示形式・検索語・絞り込み（型・優先度・着手可能・クローズ済みを隠す・未応答の反応・ラベル・担当）を、プロジェクトごとに localStorage に残します。次にそのプロジェクトを開くと、残した条件で表示します。並び順は以前から残っていました。プロジェクトに無くなったラベルや担当は、ボードを読み込んだときに外します。
- **ブラウザのタブにアプリのアイコンが出ます。** アプリの Web の画面のどのページも、アプリのアイコンと同じ絵を指します。

### 変更

- **アプリでも、イシューを Done にするには既定でエビデンスが要ります。** 本文に検証コマンドが 1 つ以上あるイシューでは、いまの本文に対する最新の verify の記録に添付が無いと、アプリの中のサーバが Done を拒否します。記録そのものが無いときも拒否です。検証コマンドを書いた本文を Done のまま起票しても、まだ記録が無いので同じく拒否されます。記録の後で添付を消去しても、その記録は添付ありとして数えます。新しいプロジェクト別ルール `verify.require_evidence` の働きで、ルールを何も設定していないプロジェクトでも既定で入っています。CLI・MCP・REST・Web のどの経路にも効き、理由を付ければ上書きできます（`--override "理由"`。MCP は `override_reason`）。上書きすると `rule: verify_evidence_required` の `rule_override` が残ります。これまでの動きのままにしたいプロジェクトは、`looptrack project rules set` で `"verify": {"require_evidence": false}` を設定してください。拒否の代わりに注意だけが返ります。guide・MCP の案内文・verify の計画の文面・`next` の close の段・kit も、テストの出力の全文やスクリーンショット、生成物を添付するよう AI に指示するようになりました。1.0.0-rc.5 までの版には `--attach`・`--attach-output`・`issue attach` がありません。このキーを知らない旧版は、キーを含む rules を拒否します。前の版に戻すときは、先にキーを外してください。
- **Windows: `Looptrack.exe` と `cli\looptrack.exe` が版の情報を持つように。** デスクトップ版の zip の 2 つの exe に VERSIONINFO の資源を入れました。ProductName は `Looptrack`、ProductVersion はビルドの版そのままで、rc でも入ります。リリースのビルドは作った exe から 2 つの値を読み戻し、無いか違えば止まります。資源を書く道具は rsrc から go-winres（0BSD。ビルドにだけ使う）に替えました。デスクトップ版の exe のアイコンはそのままです。
- **アプリでも、本文の無いコメントは 400 で断られます。** アプリの中で動くサーバが、本文が空か空白だけで添付も無いコメントを `comment_empty` の 400 で拒否します。アプリの画面・CLI・AI エージェントのどこから送っても同じです。コメントは消せないので、空のものが 1 件入ると残り続けるからです。空のコメントを送っていた自作のスクリプトは、エラーが返ります。添付だけのコメントはこれまでどおり通ります（REST では `"text": ""` を付けてください）。状態の変更に空白だけのコメントを付けたときは、コメントを作らずに状態だけが変わり、応答に「コメント追記」の行は出ません。
- **`init` が書く日本語と、kit の rules・skill の日本語の文体をそろえました。** 対象は、`looptrack issue init` が `CLAUDE.md`・`AGENTS.md` に入れる案内節と、loop の層の rules 5 本、skill の `issue`・`iterate`・`session-handoff` です。地の文を です・ます にし、括弧の補足は本文の文に直しました。コマンド・パス・行数は変えていません。次に init や kit を更新したときに変わるのは文面だけ。英語の working-discipline の rules も、同じ箇所の括弧の補足を文に直しています。

### 修正

- **macOS: アプリの小さいアイコンが、Finder でノイズの絵になる問題を直しました。** 一覧表示などの小さい表示（16・32px）で、`Looptrack.app` がアイコンの代わりに崩れた絵を出していました。アイコンのファイルのいちばん小さい 2 つの大きさが、macOS が画像ではなく生の画素として読む形で入っていたためです。2 つとも、macOS が正しく読める形に入れ直しました。ほかの大きさは変わりません。更新はいつもどおりアプリごと行ってください。
- **英語の画面では、イシューの詳細のコメント節も英語の見出しで描きます。** 背景・内容などの節は画面の言語に合わせて出ていましたが、コメント節の見出しだけが日本語のままでした。保存した本文では、この見出しは本文とコメントを分ける目印なので訳しません。描くときにだけ、ブラウザが画面の言語の見出しに替えます。保存した内容は変わりません。
- **英語のプロジェクト選択の画面: 1 件なら「1 project」と単数に。** 見出しの下の行が「1 projects」になっていました。1 件のときは単数、それ以外は複数で出します。日本語の行は変わりません。
- **英語の文言で、件数が 1 のときに単数で出るようになりました。** CLI・REST・MCP・帳票の英語の文言のうち 30 件あまりが、「1 issues」「1 files」のように、1 件でも複数形で出ていました。1 件のときは単数にし、0 件と 2 件以上は複数のままです。日本語の文面と、「(s)」で書いていた文言には手を入れていません。
- **AI エージェントの hook: ヒアドキュメント・`$'…'`・コメントの陰に置いたコマンドをガードが見落とさず、長いコマンドも判定します。** git・秘密・待ちループ・scope の 4 つのガードは、シェルがヒアドキュメントと読まない `<<` の次の行を落としていました。`<<<` の一部、引用符やコメントの中、`$((…))`・`${…}` の中の `<<` がこれに当たります。シェルが実行する `cat <<EOF | sh`・`bash <<EOF` の本文も落とし、`$'…'` やコメントの中のアポストロフィの後ろでは引用符の組を見失っていました。scope のガードは、空白を挟まないパイプ（`echo a|git …`）の後ろを見ていません。どれも、本物のコマンドが確かめられずに通る形です。ガードには読み方を足し、どれか 1 つで当たれば止める形にしました。以前に止めていた形が通るようにはなっていません。入れ子のシェルをほどく段は、引用符の数と長さの積に比例して遅くなっていました。20〜30 KB のコマンドで 4 秒の打ち切りに届き、判定しないまま通していたわけです。書き直した後の実測では、300 KB のコマンドを 1 秒未満、1 MB を 2.3 秒で判定しました。打ち切りを越えたコマンドは、これまでどおり通します。終端をバックスラッシュで引用したヒアドキュメント（`<<\EOF`）の本文は、いまも語として判定します。それで止まったら `<<'EOF'` に書き換えてください。
- **AI エージェントの hook: 引き継ぎの hook が `close` を取りこぼさなくなりました。** 終わった作業に気づき、引き継ぎの更新を求めさせる hook のことです。コメントの次の行、引用符や算術の中の `<<` の後ろ、`cat <<EOF | sh` の本文にある `close` を数えていませんでした。ヒアドキュメントとコメントの読み方は、ガードと同じものにそろえました。
- **`looptrack handoff -h`・`--help` が使い方を出します。** これまでの `handoff append -h` は、`-h` を本文として引き継ぎに足し、成功で終わっていました。`-h`・`--help` は、サブコマンドの前でも後でも使い方を出すだけで、何も書きません。`-x` のような語は、選択肢の打ち間違いとして止めます。`--` の後ろの語は、これまでどおり本文です。
- **Looptrack 自身のリポジトリの clone で、setup と `init` がエラーで終わらなくなりました。** そこでは `init` が失敗していたので、setup ツールの取得と init のコマンドは、`looptrack` を置き換えた後でエラーになっていました。そのリポジトリに対して、setup ツールは取得の段だけを返します。`init` は何も書かず、要らない旨を出して終了コード 0 で終わります。`doctor` も init を勧めません。
- **`init` が CLAUDE.md・AGENTS.md に書く案内の節が、英語の利用者には英語で入ります。** 言語に関わらず日本語だった節を、`init` のほかの出力と同じ言語で選ぶようにしました。言語を見る順は `LOOPTRACK_LANG`・`LC_ALL`・`LC_MESSAGES`・`LANG` です。別の言語で打ち直しても節は 2 つにならず、丸ごと差し替わります。節の外に書いた本文は残ります。
- **英語の管理画面のプロジェクト一覧が、「your role here: admin」と出ます。** 以前は「you are a admin here」で、母音で始まる役割では冠詞が誤っていました。日本語の行は変わりません。

### 文書

- **Windows が署名の無いアプリをそのまま止めるときの案内。** 始め方・README・FAQ・デスクトップ版のガイドに、Windows 11 のスマート アプリ コントロールと、管理者が方針で署名の無いアプリを止めている PC のことを書きました。スマート アプリ コントロールには「実行」の道も、アプリごとに許可する手段もありません。FAQ からは、`SHA256SUMS` 自体を `SHA256SUMS.minisig` で確かめる手順にも案内します。
- **Windows の CLI を `self-update` で新しくできることと、その後に残るもの。** Windows では、`looptrack self-update` が CLI（`cli\looptrack.exe` と `%LOCALAPPDATA%\Programs\looptrack` の写し）を最新のリリースの `looptrack` に置き換えます。`Looptrack.exe` には触りません。macOS と Linux では CLI がアプリそのものなので、断られます。置き換えた Windows の写しは、アプリを更新しても新しくならず、CLI とアプリの版がずれることがあります。アンインストールでも消えません。デスクトップ版の更新のページに、この動きと、写しの消し方・アプリ同梱のものに戻す方法を足しました。サーバ版の更新のページと設計文書も、「デスクトップ版は `self-update` を使わない」とだけ書くのをやめています。アプリの動きは変わりません。
- **利用者ガイドと README にスクリーンショットを入れました。** デスクトップ版とサーバ版の始め方・日々の使い方・README に、説明している画面の画像を、ページの言語に合わせて載せています。画像はどれも、架空のデータだけを入れた使い捨てのサーバから `deploy/dev/screenshots.sh` で撮ったものです。
- **利用者ガイドに、デスクトップ版のページができました。** 1 本だったデスクトップ版のページを、`docs/guide/desktop/` の始め方・使い方・更新・困ったときの 4 本に分けています。元のページは、移った先へのリンクだけを持つ短いページとして残しました。rc.5 までのインストーラや README のリンクも切れません。
- **変更履歴は 2 本です。** サーバ版の変更は `CHANGELOG.md`、デスクトップ版の変更は `CHANGELOG-desktop.md` に書き、両方に効く変更は両方に載せます。GitHub のリリースの本文は、2 本の同じ版の節を「Server」「Desktop app」の 2 節に並べたもの。

### 1.0.0-rc.5 から上げるとき

- いつもどおりアプリごと更新します。Windows では rc.5 のアプリがこの更新でもリリースのページを開くので、1.0.0 はそこから手で入れてください。その場での置き換えが効くのは、次の更新からです。
- バックアップでは、データのフォルダの `attachments` フォルダも `looptrack.db` と一緒に写してください。

### 1.0.0 の全体像

- **デスクトップ版。** macOS・Windows・Linux 向けの、ダブルクリックで起動するアプリケーションです。サーバを手元で起動してトレイ / メニューバーに常駐し、CLI を `PATH` に置くかを尋ねます。管理者の権限なしで、ログイン時に起動することもできます。起動するのは 127.0.0.1 に結び付けた、SQLite を使うサインインなしの 1 人用のサーバ。
- **プロジェクト横断のイシュー管理。** アプリが、多くのプロジェクトのイシューを持ちます。プロジェクトごとに、ID のプレフィックスとゼロ埋めの桁数（`MYP-0001`）、ルール、ガイドの文書を持ちます。正はデータベースで、管理対象のソースのリポジトリには何もコミットしません。
- **ループの形をした進め方。** `next` は取り組むイシューを選び、着手の規則を守らせます。`verify` はイシューに書いたコマンドを実行して、結果を記録します。`summary` は毎セッションの初めに、入れ子になった 3 つのループを報告します。いまの回、人の判断を待つ決定、外から届くフィードバックの 3 つです。コメントとイベントは追記のみで、閉じたイシューは変更できません。
- **プロジェクト別のルール。** 必須のコメント、禁止した状態の遷移、`verify.require_on_close` は 1 か所で確かめるので、REST・MCP・CLI・Web のどの経路にも効きます。
- **AI エージェント向けの MCP**。MCP の接続だけでプロジェクトを配線できる `setup` ツールと、`guide` / `review` / `loop` のプロンプトがあります。
- **Web UI**: プロジェクトの選択、ボード / 一覧 / トレースの表示、イシューの起票・状態の変更・コメント・ファイルの添付、アカウントの設定。
- **CLI（`looptrack issue`）**: 作成・編集・コメント・状態・close・一覧・表示・next・verify・summary・guide・export と、プロジェクトを 1 つのコマンドで配線する `init`。
- **hook と配布の kit。** `core` の層（セッションの要約・イシューの鮮度ガード・トークンの集計）は必ず入ります。任意の `loop` の層は、出力・作業・イテレーションの規律の rules、タスクモードと範囲のガード、引き継ぎの鮮度、暴走プロセスのガードを足します。配線はマニフェストで記述し、Claude Code・Codex・GitHub Copilot 向けに生成します。
- **トークンの集計。** 使用量をセッション・エージェント・ループの段階ごとに記録し、イシューに帰属させます。結果はフォントを埋め込んだ PDF のレポート（`looptrack report pdf`）になります。
- **署名したリリースの成果物。** `SHA256SUMS` は minisign で署名し、macOS のバイナリは Developer ID で署名して公証を受けています。どの成果物にも、第三者のコンポーネントのライセンスの全文を収めた `NOTICE` が付きます。
- **作業ツリーの片付け。** `looptrack worktree list` は、すべての git の作業ツリーを、それについて決めるのに要ることと一緒に並べます。マージ済みかどうか、未コミットの変更（ビルドの残りは無視する）、ロック、そして**最後に動いた時刻**。片付けは `looptrack worktree prune` で行います。`--yes` を渡さない限り何も消さず、本体の作業ツリーには決して触れません。最近動いたものは、ほかのセッションがまだ中にいるかもしれないので残します。
- **Markdown へのエクスポート。** `looptrack export --out <dir>` がイシューを Markdown に書き戻します。だからデータが閉じ込められることはありません。アプリのデータフォルダにある `looptrack.db` を `LOOPTRACK_DSN=sqlite:<looptrack.db のパス>` で渡せば、アプリが持つデータをそのまま書き出せます。

## [1.0.0-rc.5] - 2026-10-01

Fifth release candidate for 1.0.0, relative to `v1.0.0-rc.4`. Most of this
version is for server users; for the desktop app it changes the PATH hint, a
few commands the AI agent setup returns, and the secrets guard of the AI agent
hooks. Update the app as a whole, as usual.

### Fixed

- **The hint for a CLI place that is not on the PATH shows the command that
  adds it.** The desktop app's hint no longer tells you to edit `~/.zshrc` or
  `~/.bashrc`. It shows the same command that `looptrack doctor` shows to add
  the place to the PATH.
- **If you also connect to a team server: its setup replaces the app's CLI
  link.** The AI agent setup of a team server downloads its own `looptrack`
  whenever the file in `~/.local/bin` (on Windows,
  `%LOCALAPPDATA%\Programs\looptrack`) differs from the one it distributes. The
  desktop app's symbolic link there (on Windows, its marked copy) is such a
  file, so the server setup replaces it with the distributed one. A symbolic
  link is replaced, and the file it points to is left alone.
- **Setup commands for Codex and Copilot no longer leave environment variables
  in your shell.** Some setup commands pass `LOOPTRACK_API_URL` and
  `LOOPTRACK_PROJECT`: every command for Codex and Copilot, and the PowerShell
  command that reports an `other` agent as installed. They set them with
  `export` in sh or `$env:` in PowerShell. Pasting one into a terminal left them
  set, and a later `looptrack` in another project silently pointed at this one.
  A chained command exports them inside a subshell, a single command takes them
  as a plain prefix (`LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=… …`), and
  PowerShell restores the previous values when the command ends (saved under
  names that the wrapped command does not use).
- **The "[Update the distributed files]" notice tells you to call the setup
  tool.** It used to show an init command without `--url`; the notice at session
  start and in MCP tool results shows no command any more.
- **AI agent hooks: the secrets guard and the work record read quotes and line
  continuations the way a shell does.** The secrets guard and the freshness
  guard's record of work read quotes both the POSIX way (outside quotes and
  inside double quotes, `\"` neither opens nor closes a pair) and the Windows
  way (`\` is an ordinary character, as in PowerShell), and act when either
  reading matches. A command that hid a `cat` of a secret file behind `\"` is
  confirmed, and nothing that was confirmed or recorded before goes through. A
  line ending in an even number of backslashes no longer joins the next line
  onto it; as in the shell, only an odd number counts as a continuation. The
  git guard is unchanged.

---

1.0.0 のリリース候補の 5 つ目で、下の変更は `v1.0.0-rc.4` からの差分です。この版の大半はサーバ版の利用者向けです。デスクトップ版で変わるのは、PATH の案内、AI エージェントの導入が返すいくつかのコマンド、AI エージェントの hook の秘密のガード。更新はいつもどおりアプリごと行ってください。

### 修正

- **CLI の置き場が PATH に無いときの案内が、足すためのコマンドを示します。** デスクトップ版の案内は、`~/.zshrc`・`~/.bashrc` に足すよう伝えるのをやめました。代わりに、`looptrack doctor` が示すのと同じ、置き場を PATH に足すコマンドを示します。
- **チームのサーバにもつなぐ人へ: そのサーバの setup が、アプリの CLI のリンクを置き換えます。** チームのサーバの AI エージェントの導入は、`~/.local/bin`（Windows は `%LOCALAPPDATA%\Programs\looptrack`）のファイルが自分の配るものと違えば、配布物を取得して置き換えます。デスクトップ版がそこに置く symlink（Windows は印つきのコピー）もこれに当たり、サーバ版の setup が配布物に置き換えることになります。symlink ならリンクを置き換え、リンクの先には触りません。
- **Codex と Copilot 向けの setup のコマンドが、シェルに環境変数を残さなくなりました。** `LOOPTRACK_API_URL`・`LOOPTRACK_PROJECT` を渡す setup のコマンドがあります。Codex・Copilot 向けのすべてのコマンドと、other の導入済みを知らせる PowerShell のコマンドです。これらは値を sh では `export`、PowerShell では `$env:` で置いていたので、端末に貼れば値が残りました。そのまま同じ端末で別のプロジェクトに移ると、そちらの `looptrack` が黙ってこのプロジェクトに向いていたわけです。いまは `&&` でつないだコマンドならサブシェルの中で export します。単純コマンドは export の無い前置（`LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=… …`）で渡し、PowerShell は終わったら前の値に戻します。退避の変数名には、包む中身が使わない名前を選びました。
- **【配布スクリプトの更新】の案内は、setup ツールを呼ぶよう伝えます。** これまでは `--url` の無い init のコマンドを示していました。セッションの開始時と MCP のツールの結果に出る案内は、もうコマンドを示しません。
- **AI エージェントの hook: 秘密のガードと作業の記録が、引用符と行末の継続をシェルと同じように読みます。** 秘密のガードと鮮度ガードの作業の記録は、引用符を 2 通りに読みます。posix の読み方では、引用符の外と二重引用符の中の `\"` が組を開きも閉じもしません。Windows の読み方では、PowerShell と同じく `\` はただの文字です。どちらかで当たれば確認・記録します。`\"` の陰に秘密のファイルの `cat` を隠したコマンドも確認になり、これまで確認・記録していたものが素通りになることはありません。偶数本のバックスラッシュで終わる行は、もう次の行とつなぎません。シェルと同じく、継続とみなすのは奇数本のときだけです。git ガードは変えていません。

## [1.0.0-rc.4] - 2026-09-30

Fourth release candidate for 1.0.0, relative to `v1.0.0-rc.3`. The desktop app
works as it did. The MCP fix in this version is for servers behind a reverse
proxy; without a public URL, as on the desktop app, MCP still accepts only
loopback host names.

### Docs

- **Plainer wording across the documents.** The README, the user guides
  (the desktop guide among them), the kit README, the AI guide and the other
  guides were rewritten in plainer, more direct language, in both languages
  where a document has English and Japanese versions. The facts, the strength
  of each requirement and the meaning of both languages were kept as they were.

---

1.0.0 のリリース候補の 4 つ目で、下の変更は `v1.0.0-rc.3` からの差分です。デスクトップ版の動きは変わりません。この版の MCP の修正はリバースプロキシの後ろのサーバ向けで、デスクトップ版のように公開の URL が無いときは、これまでどおりループバックのホスト名だけを通します。

### 文書

- **文書の言い回しを平易に。** README・利用者ガイド（デスクトップ版のガイドを含む）・kit の README・AI 向けのガイドなどを、より平易で率直な言い回しに書き直しました。英語と日本語の両方がある文書は両方です。事実・各要件の強さ・日英の意味はそのままにしてあります。

## [1.0.0-rc.3] - 2026-09-29

Third release candidate for 1.0.0, relative to `v1.0.0-rc.2`. The desktop app
tells you about new versions, updates itself in one click on macOS and Linux,
backs up its database before upgrading it, and appears in the Linux app list.
Read "Moving from 1.0.0-rc.1 / rc.2" at the end before you go back to an
older version.

### Added

- **New-version notices in the app.** The app checks the list of releases on
  GitHub at startup and every 24 hours, and tells you only about a version
  whose `SHA256SUMS` signature checks out and whose release carries the file
  for your OS and CPU. It follows the kind of version you run (an rc hears
  about rcs too, a stable release only about stable releases), and a failed
  check (offline, say) keeps the previous notice. The notice appears at the top
  of the tray menu and in a strip in the web UI. Clearing "Check for updates" in
  the tray stops checking, and so does `LOOPTRACK_UPDATE_CHECK=off` (no
  connection to GitHub). `LOOPTRACK_UPDATE_CHANNEL` (`stable` or `prerelease`)
  chooses what to follow, and `LOOPTRACK_UPDATE_URL` points the check at
  another `https://` endpoint of the same form. A build you made yourself
  (`dev`) is not checked.
- **macOS and Linux: update the app in one click.** "Update to version
  <version>" at the top of the tray menu, or "Update now" in the strip in the
  web UI, downloads the new version, checks it against the signed `SHA256SUMS`
  (on macOS also with `spctl` and `codesign`, requiring the same identifier and
  signing team), replaces the app while keeping the previous one under a
  `.prev` name, and restarts; if the new version does not start, the previous
  one is put back. "Install updates automatically" in the tray does the same by
  itself when a new version is found (off by default). On Windows, or where the
  app's location cannot be written to, download the new version from the
  release page (on macOS the verified dmg is opened for you).
- **A backup before each database upgrade.** Before a new version migrates
  existing data in the local SQLite database, the app copies `looptrack.db`
  into the `backups` folder of the data folder (`looptrack.db.<UTC time>`) and
  keeps the two newest copies. If the copy cannot be made, it neither migrates
  nor starts.
- **Linux: the app is listed in the app list.** The AppImage puts
  `~/.local/share/applications/looptrack.desktop` and its icon in place when it
  starts, so it can be started from the launcher and the activities search, and
  the entry follows the AppImage when it moves. Clearing "Show in the app list"
  in the tray removes the entry and keeps it off.
- **CLI: `-` reads the text from standard input.**
  `looptrack issue comment <ID> -`, `issue new --body -` and `--comment -` of
  `issue status`, `issue close` and `issue next` read the text from standard
  input, as `looptrack handoff append` already did; empty input is refused
  without sending anything. Before, `-` was saved as the text itself.
- **Starting an issue whose acceptance criteria are still the template returns
  a note.** In a project with `acceptance.require_on_close`, setting an issue to
  In Progress (including through `next`) while its "## Acceptance criteria" are
  still the template returns a note that closing it will be refused as it
  stands. The start itself is not blocked. The note appears in the CLI, in REST
  responses (`acceptance_notice`), in MCP `set_status` and `next`, and in the
  web UI.
- **Claude Code: your own MCP start is told apart from another
  conversation's.** A new core hook, `looptrack hook issue-session-bind`
  (PreToolUse on `mcp__.*`), hands the app the tool call's ID and the
  conversation's session ID right before a looptrack MCP tool runs, so MCP
  `next` and the lists can tell a start by another conversation from your own,
  even over the same MCP connection. It is cut off after 1.5 seconds and lets
  the call through on any failure; `looptrack issue init --no-session-bind`
  leaves it out. When the token-accounting hook sends the snapshot for an MCP
  call, the app also ties that start to the conversation (migration
  `0006_issue_event_sessions`), so the CLI can tell the two apart as well.
  Other agents are not affected.
- **AI agent hooks: `LOOPTRACK_LOOP_HOOK_LOG=1` records hook verdicts.** Every
  verdict (the core hooks included) is appended as one JSONL line to
  `<state dir>/.looptrack-freshness/hook-log.jsonl`, holding only the time, the
  hook, the event, the decision, the session and a word for the kind of reason —
  never the command, the prompt, the wording of the reason or a path. It rotates
  at 1 MiB. Off by default.

### Changed

- **The desktop app's release assets keep their names.** From this version
  GitHub Releases ship the server binaries as archives, but the desktop app's
  files are named as before, and `SHA256SUMS` still lists them.
- **Verification records follow your language.** The comment that `verify`
  (and MCP `report_verify`) leaves is written in the recorder's language,
  chosen the same way as everything else shown to that user; records already
  stored are unchanged. In English, the mark for a result reported over MCP
  reads "self-reported via MCP" everywhere.
- **Loop kit: `pre-tool-subagent-bound` also asks subagents to check
  identifiers.** The cautions it appends to a subagent's instructions include
  cross-checking every identifier in them (a variable or function name, a
  commit SHA, a line number, a file path) against the real thing with
  `git show` / `git grep` before use, and reporting any mismatch in one line.
  `background-process.md` carries the same line for agents where nothing is
  appended automatically.
- **Loop kit: more working discipline in the rules.** `working-discipline.md`
  gains rules on counting both sides of a shared component, re-checking with a
  different method, what a green summary does and does not prove, controls for
  "X does not happen" tests, probing guards for the forms they miss, running
  the goldens a change records, running the build and type check right after a
  rebase, and pushing rules and state down to subagents (with a template to copy
  into their instructions).

### Fixed

- **An older app no longer runs on a database migrated by a newer one.** The
  desktop app refuses a database that has applied migrations it does not know,
  instead of using data in the newer shape. To go back, restore the database
  from a backup taken before the newer version migrated it.
- **AI agent setup no longer replaces the `looptrack` that is already
  installed, so the app's CLI link stays.** When `~/.local/bin/looptrack` (on
  Windows, `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`) exists, the
  command that MCP `setup` returns neither downloads nor replaces it and only
  runs `init` with it. The link the app places there (on Windows, its copy of
  the CLI) is left alone, and a server that distributes an older version can no
  longer downgrade it (the next `setup` suggests `self-update` when the local
  one is older). 1.0.0-rc.5 changes this for a file that differs from the one
  the server distributes.
- **Codex: MCP `setup` prefixes the environment variables.** Codex picks up
  the variables `init` writes into `.codex/config.toml` only after a restart,
  so, as for Copilot, the returned commands (including the token check) carry
  `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT`.
- **Token accounting covers every operation that records an event.** Tokens are
  also attached to MCP `next` (when it starts an issue), `report_verify` and
  `assign_issue`, and to the CLI's `issue assign` when the assignee changes, so
  these no longer show up as unattributed. A snapshot that failed to send and was
  resent later is attached to the original operation even after the attach
  window has passed (migration `0005_usage_snapshots_attempted_at`). Unsent
  snapshots are kept per server and project and resent only to the project they
  came from (older ones without that information are not resent and are dropped
  after 7 days). Send failures are no longer silent: the session-start summary
  reports the last failure and how many snapshots are waiting.
- **AI agent hooks see through wrapped commands.** The issue freshness guard
  and the handoff completion record recognise `git commit` / `push` / `merge`,
  `looptrack issue …` calls and completions (`close`, `status Done`) wrapped in
  `bash -c` / `sh -c` / `eval`, behind prefix words such as `sudo`, `env`,
  `nohup` and `xargs`, inside a `function`, or split by a line continuation
  (the nested shells and prefix words are unwrapped the same way as in the git
  and secrets guards). A commit of only the handoff file made in another
  worktree is told apart correctly.
- **Loop kit, runaway-process guard: an unquoted `ps` line is judged as a wait
  loop.** `ps` drops the quotes from a command line
  (`/bin/bash -c while true; do sleep 1; done`), which used to slip past the
  shorter threshold for unbounded wait loops.
- **Loop kit, task-mode hook: a subagent's report no longer switches the
  mode.** Claude Code passes a subagent's final report through UserPromptSubmit
  wrapped in `<agent-message …>`; the words in it no longer switch between
  investigate and execute mode, and issue IDs in it no longer count as
  mentioned by the user for the freshness guard.
- **AI agent hooks: a regular expression that Go (RE2) cannot read is
  reported.** `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE`, `_INVEST_RE`,
  `LOOPTRACK_LOOP_RUNAWAY_ALLOW` and `LOOPTRACK_MCP_SERVER` were silently
  ignored when written with lookahead and the like. The hook shows the variable
  name and the error as a message and carries on without it (the default words
  and exemptions still apply; `LOOPTRACK_MCP_SERVER` matches nothing and is
  reported on MCP tool calls only). The guides say these are RE2.
- **CLI: `looptrack issue init` no longer adds a redundant `.gitignore`
  line.** When `.claude/.looptrack-freshness/` is already ignored by a
  non-negated rule in a `.gitignore` inside the working tree, init leaves the
  top-level `.gitignore` alone (rules only in `.git/info/exclude` or a global
  excludes file do not count, since other clones do not share them).
- **The English refusal of `self-update` in the app's CLI names a heading that
  exists:** "Update" in the desktop guide.

### Docs

- **New "Token reports" guide.** What is recorded, when it is sent and what is
  never sent, how to turn it off, attribution and recovering unattributed usage,
  report requests, building the PDF by hand, the ledger, and the settings. The
  README links to it.
- **The "Updating" guide covers the new-version notices and the one-click
  update of the desktop app.**
- **README and guide introductions** describe Looptrack as external memory for
  AI coding agents that makes loop engineering possible, and the README's
  description of the web UI matches the current screens.

### Moving from 1.0.0-rc.1 / rc.2

- This version adds migrations `0005` and `0006`, which the app applies to its
  database. Once it has done so, do not put rc.1 or rc.2 back on that
  database: they predate the check for newer migrations and would run on data
  in the newer shape. Restore the backup taken before the update instead.
- After updating, run `looptrack issue init` again in each project so that
  Claude Code projects get the new `issue-session-bind` hook
  (`--no-session-bind` leaves it out).

---

1.0.0 のリリース候補の 3 つ目で、下の変更は `v1.0.0-rc.2` からの差分です。デスクトップ版は新しい版を知らせ、macOS と Linux ではワンクリックで更新できるようになりました。DB を移行する前に控えを取り、Linux ではアプリの一覧にも載ります。古い版に戻す前に、最後の「1.0.0-rc.1 / rc.2 から上げるとき」を読んでください。

### 追加

- **アプリが新しい版を知らせます。** 起動したときと 24 時間ごとに、GitHub のリリースの一覧を確かめます。知らせるのは、`SHA256SUMS` の署名が正しく、しかもそのリリースに自分の OS と CPU 向けのファイルがある版に限ります。どの種類の版を追うかは、動かしている版に合わせます。rc なら rc も知らせ、安定版なら安定版しか知らせません。オフラインなどで確認に失敗したときは、前のお知らせを残します。お知らせはトレイのメニューの先頭と Web UI の帯に出ます。トレイの「新しい版を確認する」を外すと確認しなくなり、`LOOPTRACK_UPDATE_CHECK=off` でも同じです（GitHub に接続しない）。`LOOPTRACK_UPDATE_CHANNEL`（`stable` か `prerelease`）で追うものを選べます。`LOOPTRACK_UPDATE_URL` を使えば、確認先を同じ形の別の `https://` のエンドポイントに向けられます。自分でビルドしたもの（`dev`）は確認しません。
- **macOS と Linux: アプリをワンクリックで更新できます。** トレイのメニューの先頭にある「新しい版 <version> に更新する」か、Web UI の帯の「更新する」を押すと、新しい版をダウンロードし、署名つきの `SHA256SUMS` で照合します。macOS では `spctl` と `codesign` でも確かめ、識別子と署名のチームが同じであることを求めます。そのうえで前の版を `.prev` の名前で残してアプリを入れ替え、起動し直す流れです。新しい版が起動しなければ、前の版を戻します。トレイの「新しい版を自動で入れる」にチェックを入れると、新しい版が見つかったときに同じことを自分でやります（既定では無効）。Windows や、アプリの置き場所に書き込めない環境では、リリースのページから新しい版をダウンロードしてください。macOS では検証済みの dmg を開いてくれます。
- **DB を移行する前に控えを取ります。** 新しい版がローカルの SQLite のデータベースにある既存のデータを移行する前に、アプリは `looptrack.db` をデータフォルダの `backups` フォルダへ写します（`looptrack.db.<UTC time>`）。控えは新しいほうから 2 つを残します。写せなかったときは、移行も起動もしません。
- **Linux: アプリの一覧に載ります。** AppImage は起動したときに `~/.local/share/applications/looptrack.desktop` とそのアイコンを置きます。ランチャーやアクティビティの検索から起動できるのは、このためです。AppImage を別の場所へ動かすと、項目も追いかけます。トレイの「アプリ一覧に登録する」を外すと、項目を消し、その後も置きません。
- **CLI: `-` で標準入力から本文を読みます。** `looptrack issue comment <ID> -`、`issue new --body -`、それに `issue status`・`issue close`・`issue next` の `--comment -` が、標準入力から本文を読むようになりました。`looptrack handoff append` が前からしていたのと同じです。空の入力は、何も送らずに拒否します。これまでは `-` という文字そのものが本文として保存されていました。
- **雛形の受け入れ条件のまま着手すると、注記が返ります。** `acceptance.require_on_close` のあるプロジェクトで、「## 受け入れ条件」が雛形のままのイシューを In Progress にしたときの話です（`next` による着手も含む）。このままでは close が拒否される、という注記を返すようになりました。着手そのものは止めません。注記は CLI、REST の応答（`acceptance_notice`）、MCP の `set_status` と `next`、Web UI に出ます。
- **Claude Code: 自分の MCP の着手と、ほかの会話の着手を見分けます。** 新しい core の hook `looptrack hook issue-session-bind`（`mcp__.*` の PreToolUse）が、looptrack の MCP のツールが動く直前に、ツール呼び出しの ID と会話のセッション ID をアプリに渡します。同じ MCP の接続を通っていても、MCP の `next` と一覧はほかの会話による着手を自分の着手と区別できるようになりました。1.5 秒で打ち切り、どんな失敗でも呼び出しは通します。`looptrack issue init --no-session-bind` なら、この hook を入れません。トークンの集計の hook が MCP の呼び出しのスナップショットを送ると、アプリはその着手を会話に結び付けます（移行 `0006_issue_event_sessions`）。これで CLI でも 2 つを見分けられます。ほかのエージェントには影響しません。
- **AI エージェントの hook: `LOOPTRACK_LOOP_HOOK_LOG=1` で判定を記録します。** core の hook を含むどの判定も、JSONL の 1 行として `<state dir>/.looptrack-freshness/hook-log.jsonl` に追記します。残すのは時刻・hook・イベント・決定・セッションと、理由の種類を表す語。コマンド・プロンプト・理由の文面・パスは決して残しません。1 MiB でローテーションし、既定は無効です。

### 変更

- **デスクトップ版のリリースの添付物は、名前が変わりません。** この版から GitHub Releases のサーバのバイナリは書庫になりましたが、デスクトップ版のファイルの名前はこれまでどおり。`SHA256SUMS` にも引き続き載っています。
- **検証の記録は、記録した利用者の言語で書きます。** `verify`（と MCP の `report_verify`）が残すコメントは、記録した人の言語で書かれます。言語の選び方は、その利用者に見せるほかのものと同じです。既に保存された記録は変わりません。英語では、MCP 経由で報告された結果の印が、どこでも「self-reported via MCP」になりました。
- **loop kit: `pre-tool-subagent-bound` がサブエージェントに識別子の照合も求めます。** サブエージェントの指示文に足す注意に、1 つ加わりました。中の識別子（変数名や関数名・コミットの SHA・行番号・ファイルのパス）はどれも使う前に `git show` / `git grep` で実物と照合し、食い違いは 1 行で報告すること、です。自動では何も足されないエージェントのために、`background-process.md` にも同じ一文を載せています。
- **loop kit: rules に作業規律が増えました。** `working-discipline.md` に加わった規律は次のとおりです。共有の部品の両側を数えること、方法を変えて確かめ直すこと、集計の緑が何を示し何を示さないか、「X は起きない」テストの対照、ガードが取りこぼす形を探ること。変更が記録する golden を回すこと、rebase の直後にビルドと型の検査を回すこと、rules と状態をサブエージェントへ降ろすこと（指示文に写す定型つき）も入っています。

### 修正

- **新しい版が移行したデータベースでは、古いアプリは動きません。** デスクトップ版は、自分の知らない移行が適用されたデータベースを拒否するようになりました。これまでは新しい形のデータをそのまま使っていました。戻すには、新しい版が移行する前に取った控えからデータベースを戻してください。
- **AI エージェントの導入が、既に入っている `looptrack` を入れ替えなくなりました。アプリの CLI のリンクも残ります。** `~/.local/bin/looptrack`（Windows では `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`）があれば、MCP の `setup` が返すコマンドはそれをダウンロードも入れ替えもせず、それで `init` を実行するだけです。アプリがそこに置くリンク（Windows ではアプリの CLI のコピー）には触りません。古い版を配るサーバが、それを古い版に戻すこともできません。手元のほうが古いときは、次の `setup` が `self-update` を勧めます。1.0.0-rc.5 では、サーバが配るものと違うファイルについて、この扱いが変わりました。
- **Codex: MCP の `setup` が環境変数を前に付けます。** Codex は、`init` が `.codex/config.toml` に書いた変数を再起動の後でしか読みません。そこで Copilot と同じく、返すコマンド（トークンの確認を含む）に `LOOPTRACK_API_URL` と `LOOPTRACK_PROJECT` を付けるようにしました。
- **トークンの集計: イベントを記録する操作は、どれも集計に入ります。** MCP の `next`（イシューに着手したとき）・`report_verify`・`assign_issue` と、担当が変わったときの CLI の `issue assign` にもトークンを付けるようにしました。これらが帰属なしとして出ることは、もうありません。送れずに後で送り直したスナップショットは、付けられる期間を過ぎていても元の操作に付きます（移行 `0005_usage_snapshots_attempted_at`）。未送信のスナップショットはサーバとプロジェクトごとに保ち、取ったプロジェクトにだけ送り直す仕組みです。その情報を持たない古いものは送り直さず、7 日で捨てます。送信の失敗も黙らなくなりました。セッションの開始の要約が、最後の失敗と、送信を待つスナップショットの数を知らせます。
- **AI エージェントの hook が、包まれたコマンドの中まで見ます。** イシューの鮮度ガードと引き継ぎの完了の記録が、`git commit` / `push` / `merge`、`looptrack issue …` の呼び出しと完了（`close`・`status Done`）を、次の形でも見分けるようになりました。`bash -c` / `sh -c` / `eval` で包んだもの、`sudo`・`env`・`nohup`・`xargs` のような前置の語の後ろにあるもの、`function` の中にあるもの、行の継続で分けたものです。入れ子のシェルと前置の語は、git ガードや秘密のガードと同じやり方でほどきます。別の作業ツリーで引き継ぎのファイルだけをコミットした場合も、正しく見分けます。
- **loop kit の暴走プロセスのガード: 引用符の無い `ps` の行を待ちループとして判定します。** `ps` はコマンド行から引用符を落とします（`/bin/bash -c while true; do sleep 1; done`）。そのせいで、上限の無い待ちループ向けの短い閾値をすり抜けていました。
- **loop kit のタスクモードの hook: サブエージェントの報告でモードが切り替わらなくなりました。** Claude Code は、サブエージェントの最終報告を `<agent-message …>` で包んで UserPromptSubmit に通します。その中の語で調査モードと実行モードが切り替わることはもうありません。その中のイシュー ID も、鮮度ガードにとって利用者が触れたものには数えません。
- **AI エージェントの hook: Go（RE2）が読めない正規表現を知らせます。** `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE`・`_INVEST_RE`・`LOOPTRACK_LOOP_RUNAWAY_ALLOW`・`LOOPTRACK_MCP_SERVER` は、先読みなどを使って書くと黙って無視されていました。いまは hook が変数名とエラーをメッセージとして示し、その値を使わずに続けます。既定の語と除外は引き続き効きます。`LOOPTRACK_MCP_SERVER` は何にも一致せず、知らせるのは MCP のツールの呼び出しのときだけです。ガイドにも、これらが RE2 だと書きました。
- **CLI: `looptrack issue init` が、余計な `.gitignore` の行を足さなくなりました。** 作業ツリーの中の `.gitignore` にある否定でない規則で `.claude/.looptrack-freshness/` が既に無視されていれば、init は最上位の `.gitignore` に手を付けません。`.git/info/exclude` やグローバルの除外ファイルにしか無い規則は数えません。ほかのクローンとは共有されないからです。
- **アプリの CLI での `self-update` の英語の拒否が、実在する見出しを指します。** 指す先はデスクトップのガイドの「Update」です。

### 文書

- **新しいガイド「トークンレポート」。** 何を記録するか、いつ送り、何を決して送らないか。止め方、帰属と、帰属なしの使用量の取り戻し方、レポートの依頼、PDF を手で作る方法、台帳、設定も説明しています。README からリンクしました。
- **「更新」のガイドで、新しい版のお知らせとデスクトップ版のワンクリックの更新を説明します。**
- **README とガイドの導入** で、Looptrack を、ループエンジニアリングを可能にする AI のコーディングエージェントの外部記憶として説明するようにしました。README の Web UI の説明も、いまの画面に合わせています。

### 1.0.0-rc.1 / rc.2 から上げるとき

- この版で移行 `0005` と `0006` が入り、アプリが自分のデータベースに適用します。適用した後のデータベースに rc.1 や rc.2 を戻さないでください。どちらも新しい移行を確かめる仕組みより前の版なので、新しい形のデータの上で動いてしまいます。代わりに、更新の前に取った控えを戻します。
- 更新した後は、各プロジェクトで `looptrack issue init` をもう一度実行してください。Claude Code のプロジェクトに、新しい `issue-session-bind` の hook が入ります（`--no-session-bind` なら入れない）。

## [1.0.0-rc.2] - 2026-09-25

Second release candidate for 1.0.0, relative to `v1.0.0-rc.1`. The tray menu
opens with a left-click on every OS and shows the version, projects can be
renamed, archived and restored, and the desktop guide explains how to start
without the CLI.

### Added

- **Tray menu: left-click for the menu on every OS, a Settings item, and the
  version.** The tray icon shows the menu on a left-click on Windows too
  (previously left-click opened the app there, and the menu needed a
  right-click); a new **Settings** item opens the account settings page
  (`/account`) in the browser; and an always-visible "Version {version}" item
  shows the running build's full version string (including any `-rc.N`), so a
  release-candidate build can be told apart from the final one without
  hovering over the icon or opening a terminal.
- **Rename, archive and restore projects.** `/admin/projects` gains "Display
  name and archiving" for each project: change the display name (the slug, the
  prefix and issued IDs stay as they are), or archive the project after typing
  its slug to confirm. Archiving is a soft delete: the project leaves every
  listing (hub, REST, MCP, CLI) and new issues, updates and comments are
  refused on every path, while its issues, comments and history are kept.
  "Archived projects" at the bottom of the page restores it. Migration
  `0004_projects_archived` adds `projects.archived_at`.
- **AI agents can create a project over MCP (`create_project`).** The tool
  creates a project and adds the caller as its admin in one transaction,
  through the same code path as the admin page (`POST /admin/projects`). When
  `setup` cannot find the project, its error tells an administrator that
  `create_project` can create it, after confirming the slug, prefix and width
  with the user first, since the prefix and width cannot be changed later.
- **Loop kit: `pre-tool-subagent-model` hook.** Stops a Claude Code subagent
  launch (`Task`/`Agent`) with `deny` when no `model` is given, and shows how
  to pick one by difficulty (haiku / sonnet / opus). Skips `subagent_type:
  fork`, a type whose definition file already sets `model:`, and names listed
  in `LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW`.

### Changed

- **The remaining Japanese-only messages are in English too.** They follow
  the language of the connection (MCP) or of the terminal (CLI,
  `LOOPTRACK_LANG`), like the rest of Looptrack: the desktop app's warnings and
  notices; the MCP prompts (`loop`, `review`, `setup`) and their errors; the
  messages about assignment, membership and usage-sending settings, and the
  `report_verify` guidance; errors and warnings about credentials, keys,
  passwords and the local server; errors from importing and from reading
  Markdown and JSON; the loop section `looptrack issue init` writes into
  `AGENTS.md`; and the hooks' input/output errors, the `verify` command runner
  and the Copilot usage hint. Words that are matched as input (such as the
  `## コメント` heading and the answer `はい`) are unchanged.

### Fixed

- **Loop kit, git guard: an unquoted Windows absolute path to `git.exe` is
  recognised.** `pre-tool-git-guard` reads the command both with POSIX
  escaping and with Windows rules (a backslash is not an escape), so
  `C:\tools\git.exe add -A` is judged like `git add -A` instead of slipping
  through. Git Bash behaves exactly as before. A path containing a space
  still has to be quoted to be recognised.
- **Loop kit, secrets guard: a glob whose name is visible is confirmed.**
  `pre-tool-secrets-guard` asks before a command that reads or copies
  `.env*` or `id_rsa*` (what remains after dropping the trailing glob names a
  secret). Listing-only commands such as `ls` are not affected, and globs that
  hide the name (`*`, `.e*`) are still not caught.
- **Loop kit, secrets guard: an apostrophe in a trailing comment no longer
  breaks the template exemption.** `cp .env.example .env  # don't …` was taken
  for an unterminated quote and confirmed; a `#` comment outside quotes is
  skipped.

### Docs

- **Desktop guide: "Get started without the CLI".** What to fill in on the
  first-run setup form, and how to connect an AI agent purely by chat from the
  tray's connection settings.
- **New "Updating" guide.** It covers what is automatic and what is manual when
  updating the desktop app.

---

1.0.0 のリリース候補の 2 つ目で、下の変更は `v1.0.0-rc.1` からの差分です。トレイのメニューはどの OS でも左クリックで開き、版も見えるようになりました。プロジェクトの名前の変更・アーカイブ・復元ができます。デスクトップ版のガイドには、CLI なしで始める方法を足しました。

### 追加

- **トレイのメニュー: どの OS でも左クリックでメニュー、「設定」の項目、版の表示。** トレイのアイコンは、Windows でも左クリックでメニューを出すようになりました。これまで Windows では左クリックでアプリが開き、メニューには右クリックが要りました。新しい **設定** の項目は、アカウントの設定のページ（`/account`）をブラウザで開きます。いつも見えている「バージョン {version}」の項目は、動いているビルドの版の文字列を省かずに示します（`-rc.N` があればそれも）。アイコンにカーソルを合わせたり端末を開いたりしなくても、リリース候補のビルドと正式版を見分けられます。
- **プロジェクトの名前の変更・アーカイブ・復元。** `/admin/projects` の各プロジェクトに「表示名とアーカイブ」が加わりました。表示名を変えるか（slug・プレフィックス・発番済みの ID はそのまま）、slug を打ち込んで確かめたうえでプロジェクトをアーカイブできます。アーカイブは論理削除です。プロジェクトはハブ・REST・MCP・CLI のどの一覧からも消え、新しいイシュー・更新・コメントはどの経路でも拒否されます。イシュー・コメント・履歴は残ります。ページの下の「アーカイブしたプロジェクト」から復元できます。移行 `0004_projects_archived` が `projects.archived_at` を足します。
- **AI エージェントが MCP でプロジェクトを作れます（`create_project`）。** このツールはプロジェクトを作り、呼び出した人をその管理者に加えるまでを、1 つのトランザクションで行います。通る道は管理ページ（`POST /admin/projects`）と同じです。`setup` がプロジェクトを見つけられないときは、エラーが管理者に `create_project` で作れることを伝えます。プレフィックスと桁数は後から変えられないので、作る前に slug・プレフィックス・桁数を利用者に確かめるよう添えています。
- **loop kit: `pre-tool-subagent-model` hook。** `model` を指定せずに Claude Code のサブエージェント（`Task`/`Agent`）を起動すると `deny` で止め、難しさに応じた選び方（haiku / sonnet / opus）を示します。止めないのは、`subagent_type: fork`、定義ファイルが既に `model:` を決めている型、`LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW` に挙げた名前の 3 つです。

### 変更

- **日本語だけだった残りのメッセージにも、英語が付きました。** ほかの Looptrack と同じく、接続（MCP）か端末（CLI・`LOOPTRACK_LANG`）の言語に従います。対象は次のとおりです。デスクトップ版の警告とお知らせ。MCP のプロンプト（`loop`・`review`・`setup`）とそのエラー。担当・メンバー・使用量の送信の設定についてのメッセージと、`report_verify` の案内。資格情報・鍵・パスワード・ローカルのサーバについてのエラーと警告。インポートと、Markdown・JSON の読み込みのエラー。`looptrack issue init` が `AGENTS.md` に書く loop の節。それに hook の入出力のエラー、`verify` のコマンドの実行部、Copilot の使用量のヒント。入力として照合される語（`## コメント` の見出しや、答えの `はい` など）は変えていません。

### 修正

- **loop kit の git ガード: 引用符の無い Windows の絶対パスの `git.exe` を認識します。** `pre-tool-git-guard` は、コマンドを POSIX のエスケープと Windows の規則（バックスラッシュはエスケープではない）の両方で読むようになりました。そのため `C:\tools\git.exe add -A` は、すり抜けずに `git add -A` と同じように判定されます。Git Bash での動きは前とまったく同じ。空白を含むパスは、これまでどおり引用符で囲まないと認識されません。
- **loop kit の秘密のガード: 名前が見えているグロブを確認します。** `pre-tool-secrets-guard` は、`.env*` や `id_rsa*` を読む・写すコマンドの前に尋ねるようになりました。末尾のグロブを落とした残りが、秘密を名乗っているからです。`ls` のような一覧だけのコマンドには影響しません。名前を伏せるグロブ（`*`・`.e*`）は、これまでどおり捕まえません。
- **loop kit の秘密のガード: 行末のコメントのアポストロフィで、雛形の例外が壊れなくなりました。** `cp .env.example .env  # don't …` は、閉じていない引用符と取り違えられて確認になっていました。いまは引用符の外の `#` のコメントを飛ばします。

### 文書

- **デスクトップ版のガイド: 「CLI なしで利用を始める」。** 初回の設定のフォームに何を入れるか。それと、トレイの接続の設定から、チャットだけで AI エージェントをつなぐ方法。
- **新しいガイド「更新」。** デスクトップ版を更新するとき、何が自動で何が手作業かを説明します。

[Unreleased]: https://github.com/howashoji/looptrack/compare/v1.0.1-rc.1...HEAD
[1.0.1-rc.1]: https://github.com/howashoji/looptrack/releases/tag/v1.0.1-rc.1
[1.0.0]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0
[1.0.0-rc.5]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.5
[1.0.0-rc.4]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.4
[1.0.0-rc.3]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.3
[1.0.0-rc.2]: https://github.com/howashoji/looptrack/releases/tag/v1.0.0-rc.2
