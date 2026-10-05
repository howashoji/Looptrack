# Contributing to Looptrack

Thanks for taking the time. This document covers setting up a development
environment, running the tests, and the conventions we expect a pull request to
follow.

Looptrack is one Go module that builds one binary, `looptrack`. That same binary
is the server (`serve`, `setup`, `migrate`, the admin subcommands), the client
(`issue`, `hook`, `report`), and the desktop edition (`desktop`). There's no
second runtime and no shell script to install anywhere. We intend to keep it
that way.

Day-to-day development happens in a private repository. It reaches this one as
periodic commits, each carrying one finished piece of work. Issues and pull
requests from outside are welcome here, and they're handled here; [How this
repository is maintained](#how-this-repository-is-maintained) explains what that
means for you in practice.

## Before you start

- **Bug reports, feature requests and questions all go to GitHub Issues.**
  There's no Discussions tab. Search the existing issues first, please. Then
  include the version (`looptrack version`), how the server is deployed, and the
  exact commands and output.
- **Security problems do not go to GitHub Issues.** See [SECURITY.md](SECURITY.md).
- **For anything larger than a bug fix, open an issue before you write code.**
  A short description of the problem and the approach you have in mind saves
  everyone a rewrite. Behaviour that every project sees (the CLI, the hooks, the
  kit, the API) changes carefully, and rarely.
- **Writing acceptance criteria is research, not drafting.** Check every path,
  file, string and line number you put in a criterion against the real thing
  first, at a pinned revision (`git show <rev>:<path>`, `git grep <rev>`, the
  routing definitions). Never copy the wording of someone else's report: a
  summary confuses a lookup key with the text that's actually rendered. And a
  criterion that can't be met tempts the implementer into building something to
  fit it. Criteria can't be written ahead of the thing they describe.
  Found one wrong criterion? Re-check every criterion written in the same sitting.
- By contributing you agree that your contribution is licensed under the MIT
  license (see [LICENSE](LICENSE)).

## Development environment

| Tool | Version | Needed for |
| -- | -- | -- |
| Go | 1.26 or newer (`go.mod` sets the floor; CI builds with the latest 1.27 patch) | everything |
| MySQL | 8.4 | the database-backed tests and the MySQL code path |
| SQLite | bundled — nothing to install | the single-file database path |
| Node.js | 22 or newer | the browser-side tests of the web UI |
| Docker (or Podman) with Compose | any recent version | the local MySQL for tests, and the container image |
| shellcheck | any recent version | the shell scripts under `deploy/` |

```bash
git clone https://github.com/howashoji/looptrack
cd looptrack
go build -o bin/looptrack ./cmd/looptrack
```

Run the build by its path (`./bin/looptrack …`); `/bin/` is git-ignored. Don't
copy it to `~/.local/bin` (where the server edition's setup places the
released binary) and don't use `go install`, which writes to `GOBIN`: a
development build points at a local server by default and would take over the
released binary's place.

Start the local MySQL the tests use. It listens on 127.0.0.1:13306 only, and
its password is for this throwaway container:

```bash
docker compose -f deploy/dev/compose.yaml up -d
```

The container restarts itself if it dies (`restart: unless-stopped`), but any
test run in flight dies with it. If many tests suddenly fail with
`dial tcp 127.0.0.1:13306: connect: connection refused`, first check whether the
container stopped and whether it was killed for lack of memory:

```bash
docker inspect im-dev-mysql --format '{{.State.Status}} {{.RestartCount}} {{.State.StartedAt}} {{.State.OOMKilled}}'
```

Docker resets `OOMKilled` to false when it restarts the container, so after an
automatic restart it no longer tells you anything. A `RestartCount` that went up,
or a `StartedAt` in the middle of your run, means the container died and was
started again; the kernel log (below) says why. That log is a ring buffer kept
since the VM booted, so old records may be gone.

`OOMKilled` being true doesn't mean MySQL itself grew. When the whole Docker
Desktop VM runs short of memory, the kernel picks a victim, and a heavy image
build in another container or another project can make it pick MySQL. The VM's
kernel log tells you which case it was (`global_oom` means the whole VM ran
short):

```bash
docker run --rm --privileged --entrypoint sh mysql:8.4 \
  -c 'cat /dev/kmsg & p=$!; sleep 2; kill $p' | grep -E 'Killed process|global_oom'
```

Don't run a heavy image build and a database-backed full check side by side on
the same VM. Whether to raise Docker Desktop's memory allocation is your call.

A check that was interrupted leaves its throwaway databases (schemas starting
with `im_test_`) behind. The tests don't delete them automatically, because they
could be deleting another run's databases. Drop them by hand only after you've
**looked at the list** from `ps -eo pid,etime,command | grep -E '[g]o test'` and
confirmed that no database-backed check is running. List them first:

```bash
docker exec im-dev-mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE \"im\\_test\\_%\""'
```

Then drop each name the list shows:

```bash
docker exec im-dev-mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -e "DROP DATABASE im_test_xxxx"'
```

Want to try the server itself? Run the setup wizard in a scratch directory and
pick the single-user, SQLite answers:

```bash
mkdir /tmp/looptrack-try && cd /tmp/looptrack-try
looptrack setup
looptrack serve --env-file ./.env
```

Don't run `looptrack issue init` against this repository, or against any project
you care about. It writes hook wiring and skill files into `.claude/`. Try it in
a throwaway directory.

## Running the tests

```bash
# Go. Unset LOOPTRACK_API_URL and LOOPTRACK_PROJECT first: without that, tests
# that build a client can talk to whatever server your shell is pointing at.
# LOOPTRACK_TEST_DB=mysql closes the branch that falls through to SQLite (below)
env -u LOOPTRACK_API_URL -u LOOPTRACK_PROJECT \
  LOOPTRACK_TEST_DB=mysql \
  LOOPTRACK_TEST_DSN='root:imdev@tcp(127.0.0.1:13306)/?parseTime=true' \
  go test -count=1 -timeout 30m ./...

# Browser-side rendering of the web UI
cd internal/server/static && node --test render_test.mjs

# Shell scripts under deploy/
shellcheck -S error deploy/*.sh deploy/dev/*.sh deploy/release/*.sh

# Formatting, vet, module tidiness
gofmt -l .          # must print nothing
go vet ./...
go mod tidy -diff
```

Keep `LOOPTRACK_TEST_DB=mysql` on the command. Here's why. Without it, and
without `LOOPTRACK_TEST_DSN`, the database-backed tests aren't skipped at all:
they fall through to SQLite and pass (`skipWithoutDB` in `internal/testutil`
skips only for `LOOPTRACK_TEST_DB=skip|none`, or for `LOOPTRACK_TEST_DB=mysql`
with an empty DSN). With `LOOPTRACK_TEST_DB=mysql` and no DSN they're skipped
rather than failed. So a green `go test ./...` on its own still doesn't mean
much, and when you report a green, say that you had that value set. CI fails if
any of them are skipped on the Linux job.

`-count=1` keeps Go from reporting a cached `ok` for a package it didn't
actually run.

`internal/server` creates more than 100 throwaway databases in a single run. It
used to take over ten minutes. **But that was waiting, not weight**: concurrent
runs were serialized behind a named lock shared across the whole MySQL instance
(the reason is in the comment on `migrateLockPrefix` in
`internal/store/migrate.go`). The lock is now per schema and nothing is
serialized. Measured in 2026-09 with nothing else running, it takes around two
minutes on its own, and the whole suite two to three.

**The catch: runs going at the same time now genuinely compete**, for creating
the throwaway databases and for the CPU. What parallelism costs has turned from
"you wait your turn" into "the results wobble". That's why taking the window
isn't a nicety. It's the thing that keeps a result clean. Slower machines
stretch the run a long way too, so keep `-timeout 30m` on.

Never judge from the elapsed time alone whether the tests ran. No `(cached)` in
the output means the package really did run. But **run plain, the skip count
doesn't tell you whether a database was used**: without `LOOPTRACK_TEST_DSN`
there's a path that falls through to SQLite (the branch in `MigratedDB` and
`AppDB` in `internal/testutil`), and it prints no skip. **Run it with
`LOOPTRACK_TEST_DB=mysql`, and a skip count of 0 becomes evidence that nothing
fell through to SQLite** (with that value `Dialect()` returns mysql, so the
SQLite branch can't be taken and a missing DSN turns into a `t.Skip`). If you
ran without it, show the dialect (`testutil.Dialect()`) or the number of
throwaway databases created instead. `--- PASS: TestSQLiteSchemaMatchesMySQL`
(`internal/store/sqlite_test.go`, which skips without `LOOPTRACK_TEST_DSN` and
otherwise queries `information_schema` on MySQL) is evidence of a real MySQL
connection on its own.

Several sessions hammering the one development MySQL container at the same time
drain the result of its meaning. So before you start, count the runs already
going by **looking at the list** from
`ps -eo pid,etime,command | grep -E '[g]o test'`. (`grep -c` also matches the
command line of the wrapping shell.
Measured, it returned 3 where the real count was 0.)

Whenever you report a green or a red to someone, say:

- which SHA you checked, and in which pinned worktree
- whether `-count=1` was on
- how long it took (the real output of `time`)
- how many tests were skipped (counted from `-v`)
- how many runs were going in parallel at the time

With any of them missing, it isn't treated as evidence. A `(cached)` in the
output means that package didn't run.

A green summary (0 failed, 0 skipped) is no evidence that the test you cared
about ran. An empty `-run` filter, a build tag, or a package left out all look
the same from the summary. When you rest a case on a specific red being
cleared, run it with `-count=1 -v` and quote the `=== RUN` and `--- PASS` lines.

`deploy/dev/test-record.sh` collects all of that for you. It runs the command
above with `-v` added and prints one line to keep as the record of the run. That
line holds the start time, the SHA, the worktree path, whether `-count=1` was
on, the elapsed time (the whole run, and the `ok` line of `internal/server`),
the PASS / FAIL / SKIP counts with the names of the skipped and failed tests,
the package `ok` / `FAIL` / `(cached)` counts, the `go test` runs already going
when it started, whether `--- PASS: TestSQLiteSchemaMatchesMySQL` appeared, the
exit code, and where the log went.

```bash
SHA=$(git rev-parse --short HEAD); git worktree add --detach ../verify-$SHA $SHA
cd ../verify-$SHA && deploy/dev/test-record.sh   # the log goes to $TMPDIR unless you pass --log <path>
deploy/dev/test-record.sh --parse <log>          # rebuild the line from a saved log, without running anything
```

It refuses to run when the worktree has uncommitted or untracked changes, since
then the record wouldn't be evidence for that SHA (`--allow-dirty` runs anyway
and marks the line `dirty`). The log stays outside the worktree. The script
exits with the status of `go test`, and it prints the line even for a red run.
Paste that line as it is wherever you report the result. The counting itself is
covered by `deploy/dev/test-record_test.sh`, which `go test
./internal/docscheck/` runs.

`GOOS=windows go vet ./...`, `GOOS=windows go build ./...` and `GOOS=windows go
test -c` only tell you that the code compiles. The test binary they produce
can't run on macOS or Linux. How the code behaves at run time on Windows (path
separators and drive letters, a temporary directory on `D:\`, the resolution of
the monotonic clock, line endings) can't be checked locally at all. Only the
`go test (windows, no database)` job in CI sees it. That job runs on every pull
request and on every push to `main` in the public repository. So a change that
touches anything Windows-specific has to wait for that run before you can call
it green. Every local check can be green while Windows alone fails.

Two more checks run in CI. They're worth running locally when you touch
documents or dependencies:

```bash
go test ./internal/docscheck/   # guide/README structure, and the published-tree scan below
bash deploy/public-scan.sh      # no internal words, broken relative links or AI tool config in the published tree
go run ./internal/tools/notice  # regenerate NOTICE after changing dependencies
```

`public-scan.sh` scans the commit plus your working-tree changes (anything
`git add`ed, and untracked files too). On its first and last line it prints what
it scanned, including how many untracked files went in and their names. Files
ignored by `.gitignore` are left out. They never reach the published tree unless
they get tracked, so scanning them only produces false positives.

The list of internal words the scan looks for is kept under `private/`, which
the published tree leaves out. In a clone without `private/`, the scan prints one
line saying it skipped that check and goes on with the others. Tracker IDs are
still checked there, for this repository's own `IM-` prefix.

Run the scan in a worktree pinned to the SHA you're checking. In a shared main
worktree, another session's untracked files can turn it red. `go test
./internal/docscheck/` runs it too, so it fails locally as well as in CI (on
Windows that test is skipped).

Keep the history in the issue tracker and only the reason in the code. A comment
should say why the code is the way it is, without a tracker ID. IDs in shipped
source are what the scan rejects; test inputs and `testdata/` are exempt.

Test files are published too. `_test.go` and `_test.mjs` are part of what `git
archive` writes out, so keep real user names, tracker IDs and internal proper
nouns out of fixtures and test comments. In a test file the tracker-ID scan
reads only the explanatory comments (everything after `//`). It doesn't read an
ID the test uses as data (an input or an expected value), and it doesn't read an
ID in a comment that the same file also uses as data (a synthetic fixture).

What's already there is listed per file in `ids_comments_baseline` in
`deploy/public-scan.sh`. The scan fails once a file goes above its number, and
passes for the numbers themselves. So a passing scan says only that nothing was
added on top of the baseline. It doesn't say the published tree is clean
(emptying the baseline is tracked as its own issue). Known blind spots: inside
`/* … */`, and after a `//` that sits inside a string literal.

`NOTICE` is generated. Never edit it by hand. CI checks that it matches the
dependency graph.

## Things that must not change

Some things are load-bearing for data that already exists. Change any of them
and installations in the field break, so they're not accepted as part of an
ordinary pull request:

| What | Why |
| -- | -- |
| A project's `prefix` and `width` | IDs that have already been issued stop resolving |
| The `id` of an existing issue | Cross-references and traces break |
| The body of a closed issue, or any past comment | The history is the point; the server rejects it too. Reopen the subject as a new issue that references the old one |
| A migration file under `migrations/` that has already shipped | It has been applied to real databases. Add a new, higher-numbered file instead |
| The `SELECT` / `INSERT`-only grant on the comment and event tables in `deploy/grants.sql` | Append-only history is enforced by database privileges, not only by the application |
| The fixtures in `internal/testdata/fixtures` and the recorded results in `internal/domain/testdata/golden.json` | They are a matched pair that pins the conversion behaviour; changing one silently invalidates the other |

## Code conventions

- **Comments and documentation inside the code are written in Japanese.** Write
  a comment when the reason for a decision isn't obvious from the code. What the
  code does is usually clear enough. Why it does it that way isn't.
  Identifiers, error strings for programs, and log keys stay in English.
- **Keep project-specific behaviour out of the shared paths.** The CLI, the hooks
  and the kit reach every project that installs Looptrack. Something that only
  makes sense for one project belongs in that project's rules
  (`looptrack project rules set`) or in its guide document. Not in a branch here.
- **Put rules in one place.** Anything the server should enforce goes into the
  shared service layer (`internal/service`), not into one of the handlers. Then
  REST, MCP, the CLI and the Web UI all get it.
- Run `gofmt`; CI rejects unformatted files. Follow the surrounding style.
  There's no separate linter configuration to satisfy beyond `go vet`.
- New dependencies are a real cost. Each one has to be license-checked, and it
  shows up in `NOTICE` and in every release artifact. Prefer the standard
  library, and say in the pull request why a new module is needed.

### Tests

- Table-driven tests are the default. Name each case with a sentence about the
  behaviour, so a failure message explains itself.
- Test through the same entry point real callers use: domain rules against the
  service layer, CLI behaviour against the CLI, hook behaviour against the hook.
  Then a rule can't pass in one access path and fail in another.
- Tests that need a database skip themselves when `LOOPTRACK_TEST_DB=mysql` is
  set and `LOOPTRACK_TEST_DSN` isn't. Keep that pattern rather than failing.
  With neither variable set they fall through to SQLite and run, so a run with
  no skips isn't by itself a run against a database.
- Tests must never write to a real server. Don't read `LOOPTRACK_API_URL` or
  `LOOPTRACK_PROJECT` from the ambient environment in a test.
- CLI output is checked against recorded golden files. Changed the output on
  purpose? Update the golden file in the same commit and say so in the pull
  request. If it changed by accident, that's the test doing its job.
- Changing a request, a header, some wiring or a count makes the golden files
  that record it move, even in packages your branch never touched. After that
  kind of change, run `internal/clitest` (`TestCLIGolden`) and
  `internal/client/kitinit` (`TestInitGolden`).
- The golden test in `internal/client/kitinit` pins the line count of the
  generated `AGENTS.md`. It fails when you change the length of a
  `kit/loop/rules` section that both carries the
  `<!-- looptrack:inject session -->` marker and sits in a rules file marked
  `"agents_md": "sections"` in `kit/loop/manifest.json`. The body is masked in
  the golden, so re-record it the way the failure message says.
- A bug fix comes with a test that fails before the fix.

## How this repository is maintained

Looptrack is developed in a separate repository, and each release is published
here. That shows in the history, so here's what to expect before you read it.

- **Each release arrives as a single commit.** When a version is released, the
  files that changed since the previous release are copied into this repository
  and committed once, with the release tag on that commit. There's no chain of
  smaller commits behind it here. So `git log` is coarser than it would be for a
  project developed in the open, and `git blame` points at the release that
  published a line, not at the change that wrote it.
- **[CHANGELOG.md](CHANGELOG.md) (the server) and
  [CHANGELOG-desktop.md](CHANGELOG-desktop.md) (the desktop app) are where a
  change is explained.** Read those records instead of reconstructing intent
  from a large diff.
- **Pull requests from outside are ordinary pull requests.** They're reviewed
  and merged here like anywhere else. A merged contribution is also taken into
  the development repository with its author intact, so the next release carries
  it forward instead of overwriting it. You don't have to do anything
  differently because of the arrangement.
- **Release commits go straight onto `main`,** on top of the previous release
  and of any pull requests merged since. The history of `main` is never
  rewritten, so a branch you started from `main` stays valid across releases.

## Commits and pull requests

- **One logical change per commit,** carrying only the paths that belong to it.
- The subject line says what changed and, where it isn't obvious, why. English
  and Japanese are both fine. Reference the issue in this repository that the
  work belongs to, in the pull request description if not in the commit itself.
- Rebase onto the current `main` rather than merging it into your branch. Then
  make sure the whole test suite passes on the result.
- A pull request should say what problem it solves, what approach it takes, how
  you verified it, and anything reviewers should look at closely. Does it change
  behaviour users can see? Then update the documents in the same pull request
  and add an entry to the `Unreleased` section of the changelog for the people
  it affects: [CHANGELOG.md](CHANGELOG.md) for those who run, administer or
  connect to a server, [CHANGELOG-desktop.md](CHANGELOG-desktop.md) for desktop
  app users (the CLI inside the desktop app included), or both. Write it in
  English and in Japanese (the Japanese follows the English after a `---`), and
  open the entry with what changes for that reader.
- If it changes the user-facing guide under `docs/guide/` (including `server/`
  and `desktop/`; the Japanese mirrors the same tree under `docs/guide/ja/`),
  change the English and the Japanese versions together.
  `go test ./internal/docscheck/` checks that their headings, code blocks and
  tables still line up.
- CI runs formatting, `go vet`, `go mod tidy -diff`, `govulncheck`, the Go tests
  on Linux and Windows, and the small checks on every run. The macOS tests, the
  cross-build and the desktop builds run on the weekly run and on a manual run
  with `full`. A pull request and a push to `main` trigger CI in the public
  repository (`github.com/howashoji/looptrack`). In a fork the jobs are skipped,
  so run the commands above locally before you open a pull request. A maintainer
  still runs CI by hand, with `full`, before a deployment or a release tag.

## Where things live

```
cmd/looptrack/   the single binary's entry point
internal/
  domain/        issues, statuses, ordering, per-project rules
  mdformat/      Markdown ⇔ model, round-trip exact
  store/         the database layer
  service/       the operations REST, MCP, CLI and Web all go through
  guide/         assembling the guide handed to AI agents
  server/        HTTP: REST, MCP, OAuth, sign-in, the web UI
  auth/          passwords (argon2id), TOTP, tokens, encryption
  client/        the CLI, the hooks, usage collection, reports, desktop
migrations/      SQL; shipped files are never edited
deploy/          container image, install.sh, grants.sql, release tooling
kit/             what gets distributed to each project: rules, skills, wiring
docs/            guide, design, deployment, adding a project
```

The design document, [docs/server/DESIGN.md](docs/server/DESIGN.md), describes
the data model, the permission model, the API and the rules the server enforces.
Read the section that covers what you're changing before you change it.

The screenshots in the user guide and the README live in `docs/guide/images/ja`
and `docs/guide/images/en`. When a screen changes, take them again with
`deploy/dev/screenshots.sh`. It starts a throwaway SQLite server per language,
fills it with made-up data and captures only the inside of the page in a
headless Chrome, so nothing from your own machine or projects ends up in a
picture. Look at every image before you commit it, and don't add one taken by
hand.
