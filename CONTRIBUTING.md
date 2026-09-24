# Contributing to Looptrack

Thank you for taking the time. This document covers how to set up a development
environment, how to run the tests, and the conventions a pull request is
expected to follow.

Looptrack is one Go module that builds one binary, `looptrack`. The same binary
is the server (`serve`, `setup`, `migrate`, the admin subcommands), the client
(`issue`, `hook`, `report`), and the desktop edition (`desktop`). There is no
second runtime and no shell script to install anywhere, and we intend to keep it
that way.

Day-to-day development happens in a private repository and reaches this one as
periodic commits, each carrying one finished piece of work. Issues and pull
requests from outside are welcome here and are handled here; [How this
repository is maintained](#how-this-repository-is-maintained) explains what that
means for you in practice.

## Before you start

- **Bug reports, feature requests and questions all go to GitHub Issues.**
  There is no Discussions tab. Please search the existing issues first, and
  include the version (`looptrack version`), how the server is deployed, and the
  exact commands and output.
- **Security problems do not go to GitHub Issues.** See [SECURITY.md](SECURITY.md).
- **For anything larger than a bug fix, open an issue before you write code.**
  A short description of the problem and the approach you have in mind saves
  everyone a rewrite. Behaviour that every project sees — the CLI, the hooks, the
  kit, the API — is changed carefully and rarely.
- **Writing acceptance criteria is research, not drafting.** Check every path,
  file, string and line number you put in a criterion against the real thing
  first, at a pinned revision (`git show <rev>:<path>`, `git grep <rev>`, the
  routing definitions), and never copy the wording of someone else's report — a
  summary confuses a lookup key with the text that is actually rendered. A
  criterion that cannot be met tempts the implementer into building something to
  fit it; criteria cannot be written ahead of the thing they describe. Find one
  wrong criterion and re-check every criterion written in the same sitting.
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
go build ./cmd/looptrack
```

Start the local MySQL that the tests use. It listens on 127.0.0.1:13306 only,
and its password is for this throwaway container:

```bash
docker compose -f deploy/dev/compose.yaml up -d
```

To try the server itself, run the setup wizard in a scratch directory and pick
the single-user, SQLite answers:

```bash
mkdir /tmp/looptrack-try && cd /tmp/looptrack-try
looptrack setup
looptrack serve --env-file ./.env
```

Do not run `looptrack issue init` against this repository or against a project
you care about — it writes hook wiring and skill files into `.claude/`. Try it in
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

Keep `LOOPTRACK_TEST_DB=mysql` on the command. Without it, and without
`LOOPTRACK_TEST_DSN`, the database-backed tests are not skipped at all: they
fall through to SQLite and pass (`skipWithoutDB` in `internal/testutil` skips
only for `LOOPTRACK_TEST_DB=skip|none`, or for `LOOPTRACK_TEST_DB=mysql` with an
empty DSN). With `LOOPTRACK_TEST_DB=mysql` and no DSN they are skipped rather
than failed, so a green `go test ./...` on its own still does not mean much —
and when you report a green, say that you had that value set. CI fails if any of
them are skipped on the Linux job. `-count=1` keeps Go from reporting a cached
`ok` for a package it did not actually run. `internal/server` creates more than
100 throwaway databases in a single run. **It used to take over ten minutes, but
that was waiting, not weight**: concurrent runs were serialized behind a named
lock shared across the whole MySQL instance (the reason is in the comment on
`migrateLockPrefix` in `internal/store/migrate.go`). The lock is now per schema
and nothing is serialized, so measured in 2026-09 with nothing else running it
takes around two minutes on its own and the whole suite two to three. **In
exchange, runs going at the same time now genuinely compete** for creating the
throwaway databases and for the CPU: what parallelism costs has turned from "you
wait your turn" into "the results wobble", which is why taking the window is not
a nicety but the thing that keeps a result clean. Slower machines stretch it a
long way too, so keep `-timeout 30m` on. Never judge from the elapsed time alone
whether the tests ran: no `(cached)` in the output means the package really did
run, but **run plain, the skip count does not tell you whether a database was
used** — without `LOOPTRACK_TEST_DSN` there is a path that falls through to
SQLite (the branch in `MigratedDB` and `AppDB` in `internal/testutil`), and it
prints no skip. **Run it with `LOOPTRACK_TEST_DB=mysql` and a skip count of 0
becomes evidence that nothing fell through to SQLite** (with that value
`Dialect()` returns mysql, so the SQLite branch cannot be taken and a missing
DSN turns into a `t.Skip`). Run without it, show the dialect
(`testutil.Dialect()`) or the number of throwaway databases created instead.
`--- PASS: TestSQLiteSchemaMatchesMySQL` (`internal/store/sqlite_test.go`, which
skips without `LOOPTRACK_TEST_DSN` and otherwise queries `information_schema` on
MySQL) is evidence of a real MySQL connection on its own. Several sessions
hammering the one development MySQL container at the same time drains the result
of its meaning, so count the runs already going by **looking at the list** from
`ps -eo pid,etime,command | grep -E '[g]o test'` before you start (`grep -c`
also matches the command line of the wrapping shell: measured, it returned 3
where the real count was 0). Whenever you report a green or a red to someone,
say which SHA you checked, in which pinned worktree, whether `-count=1` was on,
how long it took (the real output of `time`), how many tests were skipped
(counted from `-v`), and how many runs were going in parallel at the time; with
any of them missing it is not treated as evidence. A `(cached)` in the output
means that package did not run. A green summary (0 failed, 0 skipped) is no
evidence that the test you cared about ran: an empty `-run` filter, a build tag
or a package left out all look the same from the summary. When you rest a case
on a specific red being cleared, run it with `-count=1 -v` and quote the `===
RUN` and `--- PASS` lines.

`GOOS=windows go vet ./...`, `GOOS=windows go build ./...` and `GOOS=windows go
test -c` only tell you that the code compiles: the test binary they produce
cannot be run on macOS or Linux. How the code behaves at run time on Windows —
path separators and drive letters, a temporary directory on `D:\`, the
resolution of the monotonic clock, line endings — cannot be checked locally at
all, and is only seen by the `go test (windows, no database)` job in CI. That job
does not run on a push or a pull request — CI is triggered by hand
(`workflow_dispatch`) and weekly only — so a change that touches anything
Windows-specific has to wait for a manual run before you can call it green: every
local check can be green while Windows alone fails.

Two more checks run in CI and are worth running locally when you touch documents
or dependencies:

```bash
go test ./internal/docscheck/   # guide/README structure, and the published-tree scan below
bash deploy/public-scan.sh      # no internal words, broken relative links or AI tool config in the published tree
go run ./internal/tools/notice  # regenerate NOTICE after changing dependencies
```

`public-scan.sh` scans the commit plus your working-tree changes (anything
`git add`ed, and untracked files too) and prints what it scanned — including how
many untracked files went in, and their names — on the first and last line.
Files ignored by `.gitignore` are left out: they never reach the published tree
unless they get tracked, so scanning them only produces false positives. Run the
scan in a worktree pinned to the SHA you are checking; in a shared main worktree
another session's untracked files can turn it red. `go test
./internal/docscheck/` runs it too, so it fails locally as well as in CI — on
Windows that test is skipped.

Keep the history in the issue tracker and only the reason in the code: a comment
should say why the code is the way it is, without a tracker ID. IDs in shipped
source are what the scan rejects; test inputs and `testdata/` are exempt.

Test files are published too — `_test.go` and `_test.mjs` are part of what `git
archive` writes out — so keep real user names, tracker IDs and internal proper
nouns out of fixtures and test comments. In a test file the tracker-ID scan reads
the explanatory comments only (everything after `//`); an ID the test uses as
data — an input or an expected value — is not read, and neither is an ID in a
comment that the same file also uses as data (a synthetic fixture). What is
already there is listed per file in `ids_comments_baseline` in
`deploy/public-scan.sh`: the scan fails once a file goes above its number, and
passes for the numbers themselves. A passing scan therefore says only that
nothing was added on top of the baseline, not that the published tree is clean
(emptying the baseline is tracked as its own issue). Known blind spots: inside
`/* … */`, and after a `//` that sits inside a string literal.

`NOTICE` is generated. Never edit it by hand; CI checks that it matches the
dependency graph.

## Things that must not change

Some things are load-bearing for data that already exists. A change to any of
them breaks installations in the field, so they are not accepted as part of an
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

- **Comments and documentation inside the code are written in Japanese.** Write a
  comment when the reason for a decision is not obvious from the code — what the
  code does is usually clear enough, why it does it that way is not. Identifiers,
  error strings for programs, and log keys stay in English.
- **Keep project-specific behaviour out of the shared paths.** The CLI, the hooks
  and the kit reach every project that installs Looptrack. If something only
  makes sense for one project, it belongs in that project's rules
  (`looptrack project rules set`) or in its guide document, not in a branch here.
- **Put rules in one place.** Anything the server should enforce goes into the
  shared service layer (`internal/service`) so that REST, MCP, the CLI and the
  Web UI all get it, rather than into one of the handlers.
- Run `gofmt` — CI rejects unformatted files. Follow the surrounding style;
  there is no separate linter configuration to satisfy beyond `go vet`.
- New dependencies are a real cost: they have to be license-checked and appear in
  `NOTICE` and in every release artifact. Prefer the standard library, and say in
  the pull request why a new module is needed.

### Tests

- Table-driven tests are the default. Give each case a name that reads as a
  sentence about the behaviour, so that a failure message explains itself.
- Test through the same entry point real callers use. Domain rules are tested
  against the service layer, CLI behaviour against the CLI, hook behaviour
  against the hook. That way a rule cannot pass in one access path and fail in
  another.
- Tests that need a database skip themselves when `LOOPTRACK_TEST_DB=mysql` is
  set and `LOOPTRACK_TEST_DSN` is not; keep that pattern rather than failing.
  With neither variable set they fall through to SQLite and run, so a run with
  no skips is not by itself a run against a database.
- Tests must never write to a real server. Do not read `LOOPTRACK_API_URL` or
  `LOOPTRACK_PROJECT` from the ambient environment in a test.
- CLI output is checked against recorded golden files. When output changes on
  purpose, update the golden file in the same commit and say so in the pull
  request; when it changes by accident, that is the test doing its job.
- Changing a request, a header, some wiring or a count makes the golden files
  that record it move, even in packages your branch never touched — run
  `internal/clitest` (`TestCLIGolden`) and `internal/client/kitinit`
  (`TestInitGolden`) after that kind of change.
- The golden test in `internal/client/kitinit` pins the line count of the generated
  `AGENTS.md`, so it fails when you change the length of a `kit/loop/rules` section
  that both carries the `<!-- looptrack:inject session -->` marker and sits in a rules
  file marked `"agents_md": "sections"` in `kit/loop/manifest.json`; the body is masked
  in the golden, so re-record it the way the failure message says.
- A bug fix comes with a test that fails before the fix.

## How this repository is maintained

Looptrack is developed in a separate repository, and each release is published
here. That shows in the history, so it is worth explaining before you read it.

- **Each release arrives as a single commit.** When a version is released, the
  files that changed since the previous release are copied into this repository
  and committed once, with the release tag on that commit. There is no chain of
  smaller commits behind it here, so `git log` is coarser than it would be for a
  project developed in the open, and `git blame` points at the release that
  published a line rather than at the change that wrote it.
- **[CHANGELOG.md](CHANGELOG.md) is where a change is explained.** It is the
  record to read, rather than reconstructing intent from a large diff.
- **Pull requests from outside are ordinary pull requests.** They are reviewed
  and merged here like anywhere else. A merged contribution is also taken into
  the development repository with its author intact, so the next release carries
  it forward instead of overwriting it. Nothing about the arrangement asks you to
  do anything differently.
- **Release commits go straight onto `main`,** on top of the previous release
  and of any pull requests merged since. The history of `main` is never
  rewritten, so a branch you started from `main` stays valid across releases.

## Commits and pull requests

- **One logical change per commit,** carrying only the paths that belong to it.
- The subject line says what changed and, where it is not obvious, why. English
  and Japanese are both fine. Reference the issue in this repository that the
  work belongs to — in the pull request description if not in the commit itself.
- Rebase onto the current `main` rather than merging it into your branch, and
  make sure the whole test suite passes on the result.
- A pull request should say: what problem it solves, what approach it takes, how
  you verified it, and anything reviewers should look at closely. If it changes
  behaviour users can see, update the documents in the same pull request and add
  an entry to the `Unreleased` section of [CHANGELOG.md](CHANGELOG.md).
- If it changes the user-facing guide under `docs/guide/`, change the English and
  the Japanese version together — `go test ./internal/docscheck/` checks that
  their headings, code blocks and tables still line up.
- CI runs formatting, `go vet`, `go mod tidy -diff`, `govulncheck`, the Go tests
  on Linux and Windows and the small checks on every run, and the macOS tests,
  the cross-build and the desktop builds on the weekly run and on a manual run
  with `full`. It is **not** triggered by a push or a pull request (the workflow
  only carries `workflow_dispatch` and `schedule`), so run the commands above
  locally and treat that as the evidence; a maintainer runs CI by hand before a
  deployment, a release tag, or a batch of Windows-related changes.

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
Read the section that covers what you are changing before you change it.
