# kit — the hooks / rules / skills distributed to each project (core / loop)

*日本語版: [README.ja.md](README.ja.md)*

The design is in [docs/server/DESIGN.md](../docs/server/DESIGN.md).

Only what works the same way in **every project** belongs here. `looptrack issue init` and the MCP setup tool
distribute what is here to each project. Never add a per-project branch — if it is not generic, it does not go here.
Project-specific practice belongs in the server's per-project rules (`looptrack project rules set`), or in that
project's own operating documents.

| Layer | What goes in | How it is installed |
| -- | -- | -- |
| **core** | Generic, and indispensable for making use of the issue server | init / setup install it unconditionally |
| **loop** | Generic discipline that also stands without the server. Combined with core it becomes the base on which an agent drives "file → start → implement → verify → close → next" by itself | The user chooses, through the agent, whether to take it (`init --loop` / `--no-loop` / `--remove-loop`) |
| Out of scope | Anything that depends on a particular project, stack or external tool | Not distributed |

The "Loops" column says which of the three nested loops (① the agent's work, ② the human's decisions, ③ feedback from
outside — [DESIGN.md](../docs/server/DESIGN.md)) each item drives.
The instructions that make the agent raise ② "waiting on a human" and ③ "feedback from outside", and the three-layer
display of `summary`, cannot exist without this system, so they belong to core.
The server's `review` / `loop` prompts and `guide` (the shared rules) are not distributed, yet they drive ② and ③ too —
they reach the agent from the server instead of being installed.

## core — distributed unconditionally

| What is distributed | Kind | What it does | Why it is generic | Verified by | Loops |
| -- | -- | -- | -- | -- | -- |
| `looptrack issue`, `looptrack hook` | CLI and the hooks themselves (the distributed binary) | Issue operations, the freshness guard, token attribution | It only calls the server's API and knows nothing about the project's contents | `internal/clitest`, `internal/client/hook/core` | ①②③ (`next`, `verify`, `list --status "In Review"`, `list --has-feedback`, `summary`) |
| `summary --agent` on SessionStart | hook | Injects the three-layer summary (① the current round, ② what waits on a human, ③ feedback from outside) and the notices (unattributed tokens, report requests, install state) at the top of the session, and reports that it is installed | It prints what the server returns. The default slug and URL come from `LOOPTRACK_PROJECT` / `LOOPTRACK_API_URL` | `internal/client/hook/core` | ①②③ (it injects the three headings into every session) |
| The freshness guard (`mark` on UserPromptSubmit / PostToolUse, `check` on Stop) | hook | Sends the turn back on Stop when an issue you read has not been updated | The decision looks only at the server's update events, whatever the language or the stack | `internal/client/hook/core` | ① |
| Token accounting (`looptrack hook usage`, on PostToolUse `mcp__.*`, Stop and SessionEnd) | hook | Sends the tokens consumed by MCP calls, by the end of a turn and by the end of a session | Reading the transcript is confined to a per-agent adapter and does not depend on the project | `internal/client/usagesnap` | ① (accounting; consumption in every layer is recorded) |
| `core/skills/issue/SKILL.md` | skill | The steps for file → advance → close → pick the next, and what is forbidden | Every project uses the same CLI and the same rules. Project-specific practice (label conventions, document ID schemes and the like) is left out; it sends the reader to the project's operating documents that `looptrack issue guide` returns | — | ①②③ (it covers the steps for what waits on a human and what comes from outside) |
| `core/skills/token-report/SKILL.md` | skill | The steps to build the token consumption report (PDF) and hand it over | The server holds the aggregation. The skill only says how to ask for it | — | ① (accounting) |
| The guidance section in CLAUDE.md / AGENTS.md | document | The server's URL, how to call the CLI, how to read the guide | What it says follows from the server's settings | `internal/client/kitinit` | — |

## loop — distributed only when the user takes it

| What is distributed | Kind | What it does | Why it is generic | Tuning | Loops |
| -- | -- | -- | -- | -- | -- |
| `rules/output-discipline.md` + `hooks/session-start-rules` + `hooks/user-prompt-rules` + `hooks/stop-tool-markup-guard` | rules, SessionStart, UserPromptSubmit, Stop | Injects the discipline for the format of tool calls (a call stands alone at the top of the message; resend immediately when one is malformed) into every session and every turn, and sends back an answer that still holds raw markup | Format mistakes come from the nature of the harness and have nothing to do with the project's contents | Only the marked sections of the rules are injected, so editing the file changes the wording | ① |
| `rules/working-discipline.md` (injected by the two hooks above) | rules | Injects the shared discipline for answering, establishing facts, verification, parallel sessions and subagents into every session | "Do not take a green run on trust" and "check before you assert" prevent the same failure on any stack | The parts meant for documentation-heavy projects are kept apart as "optional sections", so you choose whether to inject them | ① |
| `hooks/user-prompt-task-mode` + `hooks/pre-edit-task-mode-guard` | UserPromptSubmit, PreToolUse (`Edit\|Write\|NotebookEdit`) | "Just check it" stops at a report and a plan (investigate) and edits are denied. "Implement it" releases it (execute) | Starting to edit when you were asked to check happens in every project | Add words with `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE` / `_INVEST_RE`, change the wording of execute mode with `_EXEC_NOTE`, exempt directories with `_ALLOW_DIRS` | ② (it stops on "just check it" and waits for the human) |
| `hooks/post-work-complete-handoff-mark` + `hooks/stop-handoff-freshness` + `hooks/session-start-memories` + `skills/session-handoff/SKILL.md` | PostToolUse (`Bash\|mcp__.*`), Stop, SessionStart, skill | Collects the work-completed events (`git commit`, `looptrack issue close`, a status change to Done / Canceled) and sends Stop back until the handoff memory is updated. The file-backed memories are injected at the top of the session | A design that writes the handoff only when the session ends always loses it when the session cannot end. That is the same in every project | The backend of the freshness check is `LOOPTRACK_LOOP_HANDOFF_BACKEND`: `file` (the default) / `auto-memory` / `command` (`_CHECK_CMD` answers with 0 / 1 / anything else) | ① |
| `rules/iteration-discipline.md` + `hooks/session-start-iteration` + `skills/iterate/SKILL.md` + `looptrack gates` | rules, SessionStart, skill, command | One iteration = one implementation issue. Every problem found is filed, and no new implementation is piled on top of open bugs. The open bugs are printed at the top of the session, and the gates (build → lint → test) run fail-fast | It is the discipline that self-driving "implement → verify → close → next" needs, and it does not depend on the stack | The stages are `LOOPTRACK_LOOP_GATES_STAGES` (`build lint test` by default) and the directory is `_GATES_DIR` / `-C`. The real commands live in the project's own Makefile | ① |
| `hooks/session-scope-guard` | UserPromptSubmit | Tells you when the context of the last answer crossed the warning threshold (it only suggests writing a handoff at a break and splitting the session — it **blocks nothing**) | Packing several issues into one session inflates the context until cache reads dominate the total consumption, and that is a property of the agent, not of the project. It only suggests, and never binds a session to one issue: binding **gets in the way of parallel work** (`rules/working-discipline.md` assumes parallel sessions and delegation to subagents, and the session that coordinates them spans many issues), so that part was removed | The threshold is `_WARN` (250,000 by default) | ① |
| `hooks/pre-tool-scope-guard` | PreToolUse (`Edit\|Write\|NotebookEdit\|Bash\|mcp__.*`) | Asks the user with `permissionDecision: ask` before a change reaches another git repository, or another project's issues. Reading, `git pull`, a worktree of the same repository and places that are not git pass | Fighting over a worktree, changing a file that affects every project, and attributing the tokens to the wrong place happen in every project | Exempt with `LOOPTRACK_LOOP_SCOPE_ALLOW_REPOS` (absolute paths, space separated). ask rather than deny, because the cross-repository work the user asked for passes on the human's judgement. A change wrapped in a nested shell or `eval` (`bash -c 'cd <other> && touch a'`, `sudo sh -c '…'`, `nohup bash -c '…' &`, `setsid sh -c '…'`, `eval "…"`) is unwrapped one level by the same code as the secrets guard and confirmed just like the bare form (whatever the position, since it is ask). A wrapped form that only reads still passes. Two or more levels of nesting and command substitution go straight through | ① |
| `rules/secrets-discipline.md` + `hooks/pre-tool-secrets-guard` | rules, PreToolUse (`Edit\|Write\|NotebookEdit\|Read\|Bash`) | Confirms with `permissionDecision: ask` every operation that reads a credential, prints one, or puts one under version control (reading the keychain; `cat` / `jq` / `git add` on `.env`, `credentials.json` or a key; a Read / Write aimed at one of those). Templates (`.env.example`) and public keys (`.pub`) pass | When an agent is asked to check something on a real machine, the value ends up in the output while it looks for where the credential lives. Output cannot be taken back. Every product has secrets, and they are named the same way | Exempt with `LOOPTRACK_LOOP_SECRETS_ALLOW` (space separated; a match anywhere in the path passes). ask rather than deny, because there is legitimate work that edits a secret file. It tells secrets apart by name and by the words that run, so **it catches nothing but slips**; the forms it does not catch (what runs inside a command substitution, a command wrapped between escaped quotes, and more) are under "What it does not catch" in `secrets-discipline.md` | ① |
| `hooks/pre-tool-git-guard` (its rules are the section on parallel sessions in `working-discipline.md`) | PreToolUse (`Bash`) | Sweeping in other work (`git add -A` / `git add .` / `git commit -a`) is `ask`. What cannot be taken back (`git push --force`, a refspec starting with `+` (`git push origin +main`, `+HEAD:main`), `git reset --hard`, `git clean -f…`, `git switch --discard-changes`, `git checkout` / `git restore` with a path, and a write through `-C` / `--work-tree` / `--git-dir` pointing outside the current worktree) is `deny`. `git add <path>`, `git add -u`, `--force-with-lease`, switching branches and read-only operations pass | "Stage only the paths you touched" is written in the rules and in every project's documents, and is broken anyway. Sharing a worktree happens in every project (worktrees, several sessions, humans and agents side by side) | Exempt with `LOOPTRACK_LOOP_GIT_GUARD_ALLOW` (space separated; a match anywhere in the command string passes). What can be taken back is ask, what cannot is deny (because `ask` was measured to pass straight through in bypass permissions mode). A nested shell (`bash -c 'git reset --hard'`, `eval "git push --force"`) and a prefix word (`sudo git push --force`, `env git clean -fd`) are unwrapped one level and judged, only when they sit in command position (argument position such as `echo bash -c '…'` or `ssh host bash -c '…'` goes through). A redirection (`2>&1`, `>/dev/null`, `&>/dev/null`) has its file descriptor and target dropped from the words before judging (`git checkout main 2>&1` passes, `git checkout -- <path> 2>&1` is `deny`). **It still catches nothing but slips**, and these go straight through (measured): command substitution (`echo "$(git reset --hard)"`), a line continuation (`git push \` + newline + `--force`), an unquoted Windows path separated by `\` (`C:\tools\git.exe push --force`), a wrapper not on the list (`caffeinate bash -c 'git reset --hard'`) and two levels of nesting. The list is "What the git guard stops and what it does not" in `working-discipline.md`. `C:/tools/git.exe` and `/usr/bin/git` are judged | ① |
| `hooks/session-start-worktrees` + `looptrack worktree` | SessionStart, command | Reports leftover worktrees at the top of the session. It speaks only when there is something to clean up, or something about to lose its contents (uncommitted changes carried over a day), and stays quiet otherwise. It **deletes nothing** (that happens only when you run `looptrack worktree prune --yes`) | When you cut a worktree per issue, the ones already merged pile up. Having the tool is not enough if nothing prompts you to run it. It looks only at properties of git, so it does not depend on the project | Turn it off with `LOOPTRACK_LOOP_WORKTREE_NOTICE=0`. How long counts as "might still be in use" is `LOOPTRACK_LOOP_WORKTREE_MIN_AGE` (30m by default). The main worktree is never reported | ① |
| `hooks/user-prompt-stale-base` | UserPromptSubmit | Tells you every turn when the worktree you are in has been running on a branch point that is long out of date (one line, only while the age of the merge base against `origin/main` is over the threshold; it prints the worktree name, the SHA of the branch point and how many commits have landed since). Inside the threshold it **prints nothing**, and it **blocks nothing** | A long-running session that keeps judging from a stale base is a property of how these agents are run, not of the project. Once on SessionStart only ever fires at the start (where the base is always fresh), and just before `git merge` the judging is already over, so it measures every turn. The verdict is **time alone** because a count of commits behind changes meaning with the pace of the project (half a day is 200 commits in one repository and several months in another) | The threshold is `LOOPTRACK_LOOP_STALE_BASE_MAX_AGE` (`4h` by default), the baseline is `_STALE_BASE_REF` (`origin/main`, then `origin/master`), and `_STALE_BASE_NOTICE=0` turns it off. **It never runs `git fetch`** (so no network wait rides on each of the user's prompts). The price is that while nobody fetches, `origin/main` is itself out of date and the lag is underestimated — **silence does not mean the base is fresh** | ① |
| `hooks/stop-runaway-background-process` + `hooks/subagent-stop-runaway-background-process` + `rules/background-process.md` | Stop, SubagentStop, rules | Detects a child process this session started that never ends (long polling and the like) and sends the turn back. A shell that has **the shape of an unbounded wait loop** (`until` / `while` + `sleep` with no bound) is judged on a shorter threshold (10 minutes by default). It also looks right after a subagent finishes (SubagentStop) | Leaving background processes behind is a failure common to coding agents | The exempt pattern is `LOOPTRACK_LOOP_RUNAWAY_ALLOW` (MCP, docker, editors, dev servers and `--watch` by default). The threshold is `_THRESHOLD_MIN` (30 minutes by default) and the per-shape one is `_LOOP_THRESHOLD_MIN` (10 minutes by default) | ① |
| `hooks/pre-tool-wait-loop-guard` (its rules are `background-process.md`) | PreToolUse (`Bash`) | Stops a wait loop with no bound (`until` / `while` whose body holds a `sleep` and which carries no bound) with `deny`, **before it starts**. It passes when `break`, `timeout`, `seq`, `SECONDS`, `date +%s`, a numeric comparison, `--max` and the like are there. For a bounded wait whose target's **parent directory** does not exist, it stops nothing and returns a notice | Detection on Stop can only look at elapsed time, so anything created before the threshold is invisible by construction (of the three loops measured in a single day, every one was found by a person running `ps` before the threshold). The shape is known before it starts | Exempt with `LOOPTRACK_LOOP_WAITLOOP_ALLOW` (space separated; a match anywhere in the command string passes). `deny` rather than `ask` (because `ask` passes straight through in bypass permissions mode), and in exchange **the message always says how to rewrite it with a bound**. A wait loop wrapped by a prefix word or by `eval` (`sudo sh -c '…'`, `nohup bash -c '…' &`, `setsid sh -c '…'`, `eval "while true; …"`) is stopped too (the prefix words are the same list as the secrets guard's). The nested shell is unwrapped by the same code as the git guard and the secrets guard (command position only; `sudo -u deploy sh -c '…'` and `/bin/bash -c '…'` are stopped too). **It still catches nothing but slips**: a prefix word not on the list (`caffeinate bash -c '…'`) and argument position (`ssh host bash -c '…'`, `docker run img bash -c '…'`) go straight through (what is left is the detection by elapsed time on Stop) | ① |
| `hooks/pre-tool-subagent-bound` (its rules are `background-process.md`) | PreToolUse (`Task`\|`Agent`) | Appends the rule on bounding a background wait **verbatim** to a subagent's instructions (`updatedInput`). It **stops nothing** (it only appends). Nothing is appended when the fixed marker `[looptrack:background-bound]` is already there. That it appended is shown to the user through `systemMessage` | The rules are injected into the parent session only, so unless the parent copies them across every time, they never reach the child. The same breach being created over and over, because nothing reaches the child, happens in every project | **Wired only for an agent where rewriting a tool's input was confirmed on the real thing (Claude Code).** Whether Codex or Copilot has anything like `updatedInput` has not been confirmed, so nothing happens there (see "How Codex is handled" and "How Copilot is handled" below) | ① |
| `hooks/pre-tool-subagent-model` (its rules are the "Subagents" section of `working-discipline.md`) | PreToolUse (`Task`\|`Agent`) | When a subagent launch has no `model`, stops it with `deny` and shows, in the reason, how to pick one by difficulty (three tiers: haiku / sonnet / opus). Calling it again with `model` set lets it through. Out of scope: `subagent_type: fork` (the model follows the parent), and a type whose definition file (`.claude/agents/<name>.md`, in the working directory or `~/`) already carries `model:` in its frontmatter | The rules (`working-discipline.md`) call for switching the model to match how hard the task is, but words alone do not make that happen (what actually happened: a job that was nothing but a summary ran on the same heavy model as the parent, with no model specified) | The exception is `LOOPTRACK_LOOP_SUBAGENT_MODEL_ALLOW` (whitespace-separated `subagent_type` names; `*` exempts all). `deny`, not `ask` (`ask` passes straight through under bypass permissions). It looks only at the tool's input, environment variables and a local definition file — never the server — so it behaves the same in a directory with no project setup or server connection | ① |

## Out of scope (not distributed)

Whether something is generic is decided by **whether the same judgement can be made without knowing the project**.
The following are not distributed.

| Item | Why |
| -- | -- |
| A guard that depends on a particular database, schema or convention for deliverables | The right answer differs per project. Put it in a per-project rule, or in that project's own hook |
| Anything that depends on a particular external service (another issue tracker, browser automation, payments and so on) | Useless, or harmful, in a project that does not use that service |
| The test or deployment steps of a particular framework or language | Only the principles — do not trust a false green, fail rather than skip, keep internal vocabulary inside — are generalized into `iteration-discipline.md` and `working-discipline.md` |
| Anything that assumes a particular branch name, environment name or release procedure | There is room for it in loop if the names can be moved out into settings, but do not bring the assumptions along |

## Layout (this directory)

```
kit/
  README.md                 ← this document in English
  README.ja.md              ← the Japanese of this document (of record for what is distributed)
  embed.go                  ← embeds kit/ into looptrack (the server distributes it at GET /api/v1/dist)
  core/
    skills/issue/SKILL.md          ← skill issue (init writes it to .claude/skills/issue/SKILL.md)
    skills/token-report/SKILL.md   ← skill token-report (likewise .claude/skills/token-report/SKILL.md)
  loop/
    rules/*.md              ← output / working / iteration / secrets-discipline and background-process (5 files)
    rules/en/*.md           ← the English of the above (same file names; only the ones translated)
    skills/iterate/SKILL.md  skills/session-handoff/SKILL.md
    manifest.json           ← the hook wiring (event, matcher, timeout, order) and how Codex and Copilot are handled
```

- `kit/embed.go` (a separate package, because go:embed cannot point above its own package) embeds `kit/` and lists it
  under `GET /api/v1/dist` as `kit/core/…` and `kit/loop/…`, with a SHA-256 for each.
- The hooks themselves are Go (`internal/client/hook/core`, `internal/client/hook/loop`); kit holds no file for them.
  `internal/hookio` adapts the shape of the output per agent.

### The language of these texts (Japanese and English)

**Japanese is of record and stays where it is. The English goes into `en/` in the same directory under the same file
name** (`loop/rules/background-process.md` ↔ `loop/rules/en/background-process.md`).

- The `README.md` / `README.ja.md` at the root, this document (`kit/README.md` / `kit/README.ja.md`) and `docs/guide/`
  put English on top (with `ja/` below), but **none of those are used for wiring**.
  kit's paths are referenced by `manifest.json`, by the hook wiring and by `internal/client/kitinit`, so moving what is
  of record moves all of the wiring with it. It also matches how `internal/i18n` works — Japanese is of record and
  English catches up to it.
- **For rules, installation distributes both languages.** Which one is read is decided at run time (a hook injecting rules follows
  the same order as `i18n.FromEnv`: `LOOPTRACK_LANG` → `LC_ALL` → `LC_MESSAGES` → `LANG`, and a file with no
  translation falls back to the Japanese of record).
  Do not make installation pick one language for rules, or it can no longer follow `LOOPTRACK_LANG` when you switch it.
  The exception is `AGENTS.md` for Codex and Copilot: it is generated, so it is written in the language of the
  installation (run `looptrack issue init --loop` again after changing the language).
- **For skills, only the one in the language of the installation is placed.** The AI's harness reads a skill from the
  fixed path `.claude/skills/<name>/SKILL.md`, and nothing picks a language at run time, so an `en/SKILL.md` placed
  next to it would never be read. So init picks the text in the language of the installation (the same order as above)
  and places that one as `SKILL.md` (its `description` comes out in that language too).
  An `en/SKILL.md` an earlier init placed is cleared away by the next init (one edited by hand is kept).
  **To change the language of the skills, run `looptrack issue init` again** (the same as the `AGENTS.md` exception).
- That the two match in shape — the order of the heading levels, the number of code blocks, the number of tables, the
  injection markers and the relative links — is checked by `TestKitStructure` in `go test ./internal/docscheck/`.
  A file with no translation is not failed (only what has been translated is checked).

### How core is installed

| Thing | Where it goes (Claude Code) |
| -- | -- |
| `skills/issue/SKILL.md`, `skills/issue/en/SKILL.md` | `.claude/skills/issue/SKILL.md`. **Only the one in the language of the installation is placed** (in English, the text of `en/SKILL.md` goes in as `SKILL.md`; no `en/SKILL.md` is placed). init's marker is inside kit's SKILL.md itself; one placed by hand, without the marker, is left alone |
| `skills/token-report/SKILL.md` | `.claude/skills/token-report/SKILL.md` (likewise) |

- core's hooks (summary, the freshness guard, token accounting) are wired as `looptrack hook <name>`.
- **Not one script is placed in the target project.** Wherever these texts name the CLI, it is written as
  `looptrack issue` when it is installed (`cliText` in `internal/client/kitinit`).
- When the server's list holds no `kit/core/…` (an older server), no skill is placed and it reports that the
  distribution has no kit.

### How loop is installed

**`kit/loop/manifest.json` is the wiring of record.** init reads it and writes `settings.json`, `.codex/hooks.json`,
`.github/hooks/looptrack.json` and `AGENTS.md`.

```json
{"version": 1, "layer": "loop",
 "entries": [
   {"name": "hooks/session-start-rules", "kind": "hook", "runner": "looptrack", "event": "SessionStart", "matcher": null,
    "timeout": 5, "order": 10, "codex": {"event": "SessionStart", "matcher": null}, "copilot": {"event": "SessionStart", "matcher": null}},
   {"name": "hooks/stop-handoff-freshness", "kind": "hook", …, "event": "Stop", "codex": {"event": "Stop", "matcher": null, "block": false}, …},
   {"name": "rules/output-discipline.md", "kind": "rules", "codex": null, "copilot": null},
   {"name": "skills/iterate/SKILL.md", "kind": "skill", "codex": {"agents_md": "description"}, "copilot": {"agents_md": "description"}}, …]}
```

- `name` is relative to `kit/loop/`. rules and skills map **one to one onto the files in kit/loop** (except
  manifest.json itself and the `en/` translations). A hook is `hooks/<name>` and has no file (it is the name in
  `looptrack hook <name>`, one to one with the registry in Go).
  `TestManifest` in `internal/client/hook/loop` is what checks this.
- `kind`: `hook` / `rules` / `skill`.
- Only a hook carries `event`, `matcher` (null when there is none), `timeout` (seconds) and `order` (the order within
  the same event; entries are appended after the existing hooks in this order). `runner: "looptrack"` says that the
  wiring becomes `looptrack hook <name> --agent <agent>` (an absolute path on a terminal where it is not on PATH;
  Claude Code uses `.claude/settings.local.json`).
- A hook's `codex`: `{event, matcher, block?}` = wire it into `.codex/hooks.json`, null = do not wire it.
  The nulls are the PreToolUse edit guards (it has not been confirmed that Codex's edit tools are caught on PreToolUse)
  and `stop-tool-markup-guard` (format mistakes are specific to Claude). The two on Stop carry `block: false`:
  `--no-block` is added to the wiring, and the hook only reports with `{"systemMessage": …}` (exit 0) instead of
  sending the turn back.
- The `codex` of rules and skills (what goes into the `<!-- looptrack:loop:begin -->` section of `AGENTS.md`):
  null = **leave it out**; `{"agents_md": "sections"}` on rules = that file's `<!-- looptrack:inject session -->`
  sections, plus one line pointing at where the full text is kept (`.claude/rules/looptrack-loop/<name>`);
  `{"agents_md": "description"}` on a skill = one line from the frontmatter description, plus the path to the steps.
  `output-discipline.md` is specific to Claude's format, so it is null.
- A hook's `copilot`: `{event, matcher, block?}` = wire it into `.github/hooks/looptrack.json`, null = do not wire it
  (the reasons are under "How Copilot is handled" below). The `copilot` of rules and skills works like `codex`.
  **Every entry carries a `codex` and a `copilot` column** (null is fine).
- An injection marker can name an agent: `<!-- looptrack:inject session claude-code -->` goes in only for that agent.

| kind | Where it goes (real files; no symlinks, so Windows works) |
| -- | -- |
| hook | Nowhere (`looptrack hook <name>` is wired instead) |
| rules | `.claude/rules/looptrack-loop/`. session-start-rules and user-prompt-rules read from there (change it with `LOOPTRACK_LOOP_RULES_DIR`) |
| skill | `.claude/skills/iterate/` and `.claude/skills/session-handoff/` (init's marker is inside SKILL.md) |

**What a hook promises**: the project root is decided in the order `CLAUDE_PROJECT_DIR` →
`git rev-parse --show-toplevel` → `pwd`. The state files (`task-mode.d/`, `handoff-pending.d/`, `session-scope/`) live
in `<root>/.claude/` (change it with `LOOPTRACK_LOOP_STATE_DIR`; `.codex/` when there is no `CLAUDE_PROJECT_DIR` but
there is a `CODEX_THREAD_ID`).
When it cannot decide, it lets the call through (fail-open).
**When something other than Claude Code starts it through the wiring in `.claude/settings.json`, it does nothing**
(`hookio.ForeignHost`).
Every per-project adjustment is made with a `LOOPTRACK_LOOP_*` environment variable (`env` in settings.json).

| Environment variable | Hooks that use it | Default |
| -- | -- | -- |
| `LOOPTRACK_LOOP_STATE_DIR` | the two task-mode, the two handoff, scope | `<root>/.claude` |
| `LOOPTRACK_LOOP_RULES_DIR` / `LOOPTRACK_LOOP_AGENT` | session-start-rules / user-prompt-rules | `.claude/rules/looptrack-loop` / the `--agent` of the wiring |
| `LOOPTRACK_LOOP_TASK_MODE_EXEC_RE` / `_INVEST_RE` / `_EXEC_NOTE` / `_ALLOW_DIRS` | the two task-mode | none |
| `LOOPTRACK_LOOP_HANDOFF_BACKEND` / `_FILE` / `_MEMORY_DIR` / `_CHECK_CMD` | the two handoff, memories | `file` / `.claude/memories/handoff.md` |
| `LOOPTRACK_LOOP_HANDOFF_WRITER` | the two handoff | `session` (judged by the writer's mark when the text carries one; a file with no mark, and `mtime`, go by the modification time alone) |
| `LOOPTRACK_LOOP_MEMORIES_DIR` / `_MAX_CHARS` | memories | `.claude/memories` / `6000` (0 or less injects the whole text) |
| `LOOPTRACK_LOOP_GATES_DIR` / `_STAGES` | gates, iteration | `.` / `build lint test` |
| `LOOPTRACK_LOOP_ISSUE_CLI` | iteration | the running `looptrack` (`looptrack issue`) |
| `LOOPTRACK_LOOP_SCOPE_WARN` | scope | 250000 |
| `LOOPTRACK_LOOP_RUNAWAY_ALLOW` / `_THRESHOLD_MIN` / `_LOOP_THRESHOLD_MIN` | runaway | none, 30, 10 |
| `LOOPTRACK_LOOP_WAITLOOP_ALLOW` | wait-loop-guard | none |

**Verified by**: `go test ./internal/client/hook/loop/` (table-driven tests).
The self-check that follows installation (init's verify) confirms that the wiring matches the manifest, that looptrack
starts, that session-start-rules injects the marked sections of the rules it placed, and that the entry point can call
looptrack; when it fails it puts back what it wrote and stops (skip it with `--no-verify`).

## How Codex is handled

| kit item | Codex |
| -- | -- |
| hooks (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SessionEnd) | Wired into `.codex/hooks.json` under the same events. **Blocking on Stop has not been confirmed on the real thing**, so the ones on Stop (handoff freshness, runaway) go in as injection only |
| `pre-tool-subagent-bound` (PreToolUse) | **Not wired**: whether Codex has anything like `updatedInput`, which rewrites a tool's input, has **not been confirmed**. So **on Codex the rule does not reach subagents by itself**: the parent copies the three lines from `background-process.md` into the instructions |
| `pre-tool-subagent-model` (PreToolUse) | **Not wired**: whether Codex has a tool that launches a subagent (the equivalent of Claude Code's `Task`\|`Agent`) has **not been confirmed**. Out of scope (`codex` is `null` in the manifest). The parent follows the "Subagents" section of working-discipline.md (switch the model to match how hard the task is) by hand |
| rules | Codex has no mechanism for rules → appended to `AGENTS.md` as a section between `<!-- looptrack:loop:begin -->` and `<!-- looptrack:loop:end -->` (init owns it, and leaves anything edited by hand alone) |
| skills | None → guidance only (the steps of `/iterate` go into the AGENTS.md section in short form) |
| the state files such as `task-mode.d` | They go under `.codex/` instead of `.claude/` (`CODEX_THREAD_ID` is used as the session ID) |

## How Copilot is handled

GitHub Copilot (agent mode in VS Code, and Copilot CLI).
"Confidence" is: doc = stated in the official documentation, source = confirmed in the product's source,
real = measured on Copilot CLI 1.0.86. VS Code has not been confirmed.

| Assumption | Confidence and source |
| -- | -- |
| Both VS Code and Copilot CLI read the hooks in `.github/hooks/*.json` (the CLI only in a folder you trusted; in VS Code it is a Preview feature). The shape is `{"version": 1, "hooks": {<event>: [{"type": "command", "command", "timeoutSec", "matcher"?}]}}` (there is no grouping per matcher) | doc: [Copilot's hooks configuration](https://docs.github.com/en/copilot/reference/hooks-configuration), [hooks in VS Code](https://code.visualstudio.com/docs/copilot/customization/hooks) |
| Writing the event names in PascalCase (`SessionStart` `UserPromptSubmit` `PreToolUse` `PostToolUse` `Stop`) puts the CLI into its "VS Code compatible format", where the input is snake_case (`session_id`, `hook_event_name`, `tool_name`, `tool_input`, `transcript_path`). VS Code uses the same shape → kit's hooks work without changing how they read the input. The CLI passes the result of PostToolUse in `tool_result` (`text_result_for_llm`) | doc: the same |
| **The shape of the output differs per product.** Copilot CLI reads the top level (`additionalContext` on SessionStart and PostToolUse, `permissionDecision` / `permissionDecisionReason` on PreToolUse, `decision` / `reason` on Stop). VS Code reads inside `hookSpecificOutput` (`additionalContext` on SessionStart, `permissionDecision` on PreToolUse, and **`decision` / `reason` on Stop as well**; only `decision` on PostToolUse is at the top level). The Stop of VS Code does not read a top-level `decision` | CLI: doc ([Copilot's hooks configuration](https://docs.github.com/en/copilot/reference/hooks-configuration)). VS Code: doc ([VS Code's hooks reference](https://code.visualstudio.com/docs/agents/reference/hooks-reference)) and source |
| → When `LOOPTRACK_LOOP_AGENT=copilot` (init puts it in front), kit's hooks **put the same value in both places** (`additionalContext` and `permissionDecision(Reason)` are copied to the top level, and `decision` is copied into `hookSpecificOutput`). `looptrack issue summary --hook-json` puts it in both too. The output for Claude Code and Codex does not change | implementation (the tests check it). Whether both are really read has not been confirmed |
| Exit codes: SessionStart, PostToolUse and Stop on the CLI continue whatever the code is (fail-open). **PreToolUse on the CLI denies on 2, and denies on any other non-zero code as well (fail closed)**. VS Code treats 2 as a blocking error | doc: [Copilot's hooks configuration](https://docs.github.com/en/copilot/reference/hooks-configuration) |
| UserPromptSubmit: **the CLI attaches a top-level `additionalContext` to the user's message as a `<system_reminder>`** (which differs from what the official documentation says). Only the last `additionalContext` among several hooks on the same event is passed on, so init wires a single combined hook (`looptrack hook user-prompt --parts …`) | CLI: real. VS Code: doc only |
| **VS Code ignores the matcher** (it calls on every tool). Its tool names are its own (the shell is `run_in_terminal`, editing is `create_file`, `replace_string_in_file` and so on, and the input is camelCase `filePath`). The CLI matches its matcher against `toolName` with `^(?:…)$`, and **an MCP tool is `<server>-<tool>`** (which does not match `mcp__.*`) | doc: the same. The MCP tool names in VS Code are inferred |
| → A hook does not rely on the matcher; it **narrows by reading the tool name itself**: `post-work-complete-handoff-mark` looks only at the shell (`Bash`, `bash`, `run_in_terminal`) and at MCP's `set_status` (three spellings). The hook for token accounting (`--client copilot`) reads the MCP tool name itself too | implementation (the tests check it) |
| **Windows**: the CLI runs the `powershell` field (PowerShell 7 or newer) and VS Code runs the `windows` field (Windows PowerShell 5.1). init puts the same `looptrack hook …` into `powershell` and `windows` as into `command` (with the environment variables prefixed in PowerShell's form; looptrack runs on Windows too) | CLI: doc. The 5.1 of VS Code: source. The real thing on Windows has not been confirmed |
| VS Code has no SessionEnd. Copilot CLI passes `COPILOT_AGENT_SESSION_ID` to the shell, and the agent terminal of VS Code carries `AI_AGENT=github_copilot_vscode_agent` and `COPILOT_AGENT=1` | doc, source |

The shape of one entry that init writes (`.github/hooks/looptrack.json`):
`{"type": "command", "command": <bash>, "bash": <bash>, "powershell": <for Windows>, "windows": <for Windows>, "timeoutSec": …}`.
`command` and `bash` are the same (VS Code reads `command`; the CLI runs the same thing from either).

| kit item | Copilot |
| -- | -- |
| the four on SessionStart (rules, memories, iteration, worktrees) | Wired (`SessionStart`). The state goes in `<root>/.claude/` (the per-session files are kept apart by `session_id`). **Of several hooks on the same event, the CLI passes only the last `additionalContext` to the agent**, so they are wired as one hook together with core's summary: `looptrack hook session-start --parts summary,session-start-rules,session-start-memories,session-start-iteration,session-start-worktrees` |
| the three on UserPromptSubmit (rules, task-mode, scope) | Wired. For the same reason they are combined into one: `looptrack hook user-prompt --parts user-prompt-rules,user-prompt-task-mode,session-scope-guard`. The reason a Stop hook sent the turn back (which arrives as the user's next message) is recognized by the "[Stop hook sent this back]" at its head, and task-mode and scope do not judge it |
| `pre-edit-task-mode-guard` (PreToolUse) | Wired (no matcher). The guard drops reads, the shell and MCP by the kind of tool, then looks at `path`, `filePath` and the files in an apply_patch body, and stops an edit inside the project while in investigate mode with `permissionDecision: deny` (the denial was confirmed on the real thing). looptrack always exits 0, so it never trips the CLI's fail closed |
| `pre-tool-scope-guard` (PreToolUse) | **null**: VS Code ignores the matcher, and the shape of the MCP tool names differs. It has not been checked |
| `post-work-complete-handoff-mark` (PostToolUse) | Wired (no matcher). The hook reads the tool name itself. On the CLI it reads whether the commit succeeded from `tool_result.text_result_for_llm`. The shape of the `run_in_terminal` result in VS Code has not been confirmed (when it is missing, the event is collected without reading the outcome) |
| `stop-handoff-freshness` (Stop) | Wired, and it **sends the turn back**. The CLI throws the `systemMessage` of Stop away, but `decision: block` works (the `reason` is passed on as the user's next message and it continues; the second time carries `stop_hook_active: true`). The reason is headed with "[Stop hook sent this back]", and the same backlog sends the turn back only once |
| `stop-tool-markup-guard` | **null**: format mistakes are specific to Claude |
| `stop-runaway-background-process` | **null**: it identifies the agent as "a process among the ancestors that calls itself claude or codex". The shape of Copilot's ancestors has not been confirmed, and it does nothing when it finds none |
| `subagent-stop-runaway-background-process` (SubagentStop) | **null**: same reason as above |
| `pre-tool-wait-loop-guard` (PreToolUse) | Wired (no matcher). The decision looks only at the syntax of the command, so it does not depend on the agent. The CLI's PreToolUse is fail closed, but looptrack always exits 0 |
| `pre-tool-subagent-bound` (PreToolUse) | **null**: whether Copilot has anything like `updatedInput`, which rewrites a tool's input, has **not been confirmed**. It is not wired until that is confirmed. So **on Copilot the rule does not reach subagents by itself**: the parent copies the three lines from `background-process.md` into the instructions |
| `pre-tool-subagent-model` (PreToolUse) | **null**: whether Copilot has a tool that launches a subagent (the equivalent of Claude Code's `Task`\|`Agent`) has **not been confirmed**. Out of scope. The parent follows the "Subagents" section of working-discipline.md (switch the model to match how hard the task is) by hand |
| rules | There is no mechanism like `.claude/rules` → as with Codex, the loop section of AGENTS.md (both Copilot CLI and VS Code read AGENTS.md). `.github/instructions/*.instructions.md` is not used (everything goes into AGENTS.md) |
| skills | None → guidance only (one line of the description and the path to the steps, in the loop section of AGENTS.md) |
| token accounting (core) | Wired (`looptrack hook usage --agent copilot --event <event>` on `PostToolUse`, `Stop` and `SessionEnd` (the CLI only), with no matcher, so the hook narrows the MCP tool names itself). OTel writes only under `$COPILOT_HOME/otel/` (Copilot CLI does not pass the destination to a hook). Only a user who enabled OpenTelemetry's file output sends anything (the steps are in [docs/AI-GUIDE.md](../docs/AI-GUIDE.md)) |
| the freshness guard (core) | Not wired (Claude Code only) |

**Guidance text**: Copilot CLI reads AGENTS.md, CLAUDE.md and `.github/copilot-instructions.md` and merges all of them,
and VS Code reads AGENTS.md and CLAUDE.md by default (doc). The administration section of CLAUDE.md is for Claude Code
(guidance for a CLI that assumes the `env` of `.claude/settings.json`), so it opens with "For Claude Code. GitHub
Copilot and Codex follow the section in AGENTS.md".

**`.claude/settings.json` is read as well**: VS Code and Copilot CLI read the hooks in `.claude/settings.json` and
`.claude/settings.local.json` too (both are documented).
Copilot CLI passes `CLAUDE_PROJECT_DIR`, `COPILOT_CLI=1` and `COPILOT_PROJECT_DIR` to such a hook
(there is no `CLAUDECODE`, and the `env` of settings is not passed; real).
A hook therefore reads `COPILOT_PROJECT_DIR` and `COPILOT_CLI` before `CLAUDE_PROJECT_DIR` (see "What a hook promises"
above).
The root that init writes is `${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}`, and a hook
started by anything other than Claude Code exits 0 without printing anything.
init says so when it is installed alongside Claude Code, and when `.claude/settings.json` holds hooks.
VS Code can drop them with the `chat.hookFilesLocations` setting. How VS Code behaves in reality has not been confirmed.
