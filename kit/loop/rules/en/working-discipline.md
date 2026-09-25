# Working discipline (applies to all work)

> A rule from kit/loop. A stack-independent code of conduct, distilled from the instructions and corrections the user has given over and over.
> The SessionStart hook `session-start-rules.sh` injects the "Discipline for all work" section and the UserPromptSubmit hook `user-prompt-rules.sh`
> injects the "Points for every turn" section — never duplicate the wording inside the hooks.
> The "Optional sections" at the end are not injected. Copy only the ones that fit your project into your project's own rules.

## Points for every turn
<!-- looptrack:inject prompt -->

[Working discipline] Establish facts from live values and the actual files (never take memory or an earlier summary on trust) / map out every path and every related document before you start / prove completion with evidence (grep alone is not completion; a "green" carries the SHA, the worktree, whether the run bypassed the cache, the elapsed time, the skip count and the number of parallel runs) / never defer, never skip (file an issue for whatever you find, on the spot) / keep the parent's context for judgment and hand procedural work (research, implementation, running tests, merges, filing issues, editing documents, recording) to subagents (cap the length of their reports). Full text: working-discipline.md in rules.

## Discipline for all work
<!-- looptrack:inject session -->

The highest-priority code of conduct, distilled from the instructions and corrections the user has given over and over (full text: working-discipline.md in rules).

### Responding and making progress
- **Say it and do it**: a declaration of what you are about to do ("I'll continue", and the like) comes with a tool call in the same turn. Never end a turn still mid-work without a tool call.
- **Put the tool call at the start of the message** (→ output-discipline.md).
- **Never defer, never skip**: do not escape with "in another session" or "better as a separate issue". Either do it now, or file and record it as an issue now.
- **The task-mode contract**: a request to look into something (「確認して」, "check …") stops at a research report plus a proposed plan; a request to carry something out (「実行して」, "implement …") runs through to a finished deliverable (`user-prompt-task-mode.sh` decides the mode and injects it, and `pre-edit-task-mode-guard.sh` blocks edits while investigate mode is on).

### Establishing facts (most important)
- **Establish every fact from a primary source** (the actual file, the actual data, a live value, something the user said explicitly). Never take an earlier summary, your memory, or hearsay on trust.
- **Map out every path, every related document, and every stakeholder before you start.** Do not discover the gaps one at a time and restart.
- **Watch for the blind spots in a grep's scope**: variant spellings (okurigana, kanji vs. kana, mixed English and Japanese, full-width vs. half-width) make a search come back empty. An audit checks the variants too.

### Verification and completion
- **Prove completion with evidence**: the output of the run, the artifact actually existing, a quote of the real text, a pass/fail per criterion. A grep result alone is not "done".
- **Never write an unverified guess as an established fact.** Mark anything you are unsure of as "needs confirmation", and say where the confirmation comes from (from whom, from which material).
- **A deliverable is done when the reader can make a decision from it** — background, assumptions, what was decided and what is still open, all present.
- **Record decisions and open questions separately.** For each open question, add who decides, by when, and what.
- **Green from a check taken in a shared worktree is no evidence about that SHA.** If another session merges while the check is running,
  HEAD moves and the tests start reading different files partway through (what actually happened: a SHA reported as "all tests PASS" from the main worktree
  turned up 3 failing e2e tests when another session pinned that SHA and checked it. The broken push was stopped, but every earlier "all checks PASS"
  message had been produced the same way, with no guarantee it had looked at that SHA). **Run your checks somewhere that pins that SHA.**
  ```sh
  SHA=$(git rev-parse --short HEAD); git worktree add --detach ../verify-$SHA $SHA   # run every check inside this
  git push origin $SHA:main    # even if the main HEAD has moved on, only the SHA you checked goes out
  git worktree remove ../verify-$SHA   # always delete the worktree you checked in
  ```
- **When you tell a person something is "green", say which SHA you checked and where (in which pinned worktree).**
  **Never wave a failing test away as "probably a flake"** — do not blame the environment until you can show, by the path through the code, why that result is deterministic.
- **A report of a "green" always carries, on top of the SHA and the worktree, whether the run bypassed the cache, how long it took, how many tests were skipped, and how many runs were going in parallel.**
  With any of them missing, the side receiving it does not treat it as evidence (even a run that really happened becomes a "green you cannot prove").
  Take the elapsed time from the real output of `time` and count the skips from the verbose, per-test output. A unit whose result came back from the cache did not run.
  **The skip count says only that no test was skipped; it is no evidence that the run used the environment you expected (a database, say).**
  If the test helpers, when a required resource (a database address, say) is missing, **silently fall back** to a substitute (an embedded database, say) instead of skipping,
  the run PASSes with zero skips (the output cannot tell "forgot to pass the resource" from "ran on the substitute").
  If there is a setting that makes the resource required ("this run needs the database", stated explicitly), run with it. With it, a missing resource does not fall back but shows up as a SKIP in the count.
  Show that the run used the expected environment with output that says so directly (a line naming the implementation chosen, the line showing a test that only passes in that environment passed,
  the number of resources actually created), and put it in the report.
  Count the parallel runs by **looking at the list** from `ps -eo pid,etime,command | grep '<your test command>' | grep -v grep`
  (`grep -c` also matches the command line of the wrapping shell: measured, it returned 3 where the real count was 0).
  **Elapsed time alone cannot tell a false green from a real one.** What actually happened: watching the parallel runs with `ps`, a whole-suite run that
  finished inside three minutes was suspected of "not running the heavy package", and re-running it produced a correct green (a whole-suite run works through
  the packages in parallel, so three minutes overall does not contradict one package taking 100 seconds on its own).
  **It only becomes evidence once the cache setting and the skip count are there too.** In the same round, a check that really did run was left unprovable
  because the report carried neither the elapsed time nor the skip count.
- **A test that failed under parallel execution can come from contention even when it does not look like a timeout.** A test that calls an external
  command in a child process can have that call fail under load, and if the implementation is fail-open it **turns into a "content" failure — the expected
  string simply is not there** (what actually happened: it failed when the whole suite ran in parallel, and the same test passed on its own with the cache bypassed).
  **When you see red, first re-run that unit alone with the cache bypassed and separate load-induced failures from real ones.**
- **When a test that checks user-facing text fails, suspect the language pinning before you touch the expected value.** Fix it in this order:
  (1) pin the language in the test's environment (pass a language environment variable to child processes, send `Accept-Language` on HTTP requests) →
  (2) if it still fails, fix the implementation → (3) rewrite the expected value only when the wording itself changed as a matter of specification.
  **Never make a test green by rewriting the expected value into another language.** What actually happened: on the then-current assumption
  that "the client does not tell the server its language", **the expected values in several test files were rewritten into English**. Another session overturned
  that assumption the same day (the client started sending it), and the expected values were put back to Japanese.
  **Had the order been the other way round, what would have remained is "the tests stay green while Japanese users are shown English"** — the kind that quietly gets worse without ever failing.
- **This is a structural failure, not one person's mistake**: when the sending side (the client) and the receiving side (the server's tests) are owned by different sessions,
  **the expected values get rewritten on one side's assumption alone**. Within your own scope it is perfectly coherent, so it does not look wrong at the time.
  **Once you build a handover of language, count both the sending side and the receiving side with grep** (for example `grep -rn "Accept-Language" <your source directories>`;
  **zero hits on the sending side means the receiving side is always falling back to the default language**).
- **When you count fields by a separator, always include a line whose last field is empty in your tests.** `%(upstream:track)` of `git for-each-ref` is
  empty only when the branch is exactly in sync with the remote, and **on the very last line it is stripped as trailing whitespace** — so an implementation that
  rejects by field count silently drops the verdict depending on the order the branches come out in (this bit us for real. The test caught it first).
- **What you confirm before a push is not the scope of your own change but that the base is green.** Running every check is the default; a light check alone is enough only when
  the base SHA carries a record of every check having been green (with no record, run them all). **A narrow blast radius is no evidence that the base is green** (what actually happened: a documentation-only push moved a red base one step further).
- **A green summary (0 failed, 0 skipped) is no evidence that the test you cared about ran** (an empty name filter, a build condition or a unit left out all make "green without having run" look the same).
  When you rest a case on a specific red being cleared or on a specific fix, run it with the cache bypassed and verbose output, and quote the lines showing that test started and passed.
  **When you narrow a run by name, confirm the names of the tests that ran from the verbose output** (a test you meant to select once failed to match
  the pattern and stayed unrun while the run looked green; a PASS count alone cannot show a test that did not run).
  **"Zero matching tests" is not green.** It is a warning that not a single test ran, but the exit code stays 0 and
  a success line still appears, so unless you look at the count you misread it as green.
- **A test that confirms "X does not happen" carries a control, inside the same test, on the side where detection does happen.** Without one it passes even when the path is dead.
  A control counts when it actually exercises the phenomenon you are after (having one there is not enough), and when the premise breaks, word the failure so that it reads as "the premise has broken".

### Parallel sessions (same project)
- **Declare that you have started by changing the state on the server.** Before you tell anyone "I'll take this", put the issue In Progress first (`looptrack issue next` or
  `looptrack issue status <ID> "In Progress"`). **The message is only a notification of that.** A conversation scrolls away; the state stays.
  If you declare it and do not change the server, every other session sees exactly what it sees for an issue nobody has touched.
- **When `summary` says "Note: another session is already working on …", do not take that issue.** If you are taking it over, speak up first and
  start only once the other side has stepped down. For one that has been held a long time, it may have been handed over and then left (check first in that case too).
- **When you hand an issue to another session, the side handing it over puts the state back to Todo and clears the assignee.** Handed over while still In Progress, it keeps
  looking like "somebody is on it" even though the receiving side has not started.
- **Before you start, check which other sessions are running in the same working directory** (with a session-list tool if there is one; otherwise ask the user).
  Never judge "has the other side finished?" from a file's modification time (a lagging indicator).
- **In a shared worktree, stage only the paths you touched** (`git add -A` / `git add .` / `git commit -a` sweep up other sessions' uncommitted changes).
  Use `git add <path>` and `git add -u`. Paired with: the PreToolUse hook `pre-tool-git-guard`, which asks for confirmation (`ask`) on these
  (sweeping in other work, which unstage and amend can undo) and **stops with `deny`** what cannot be taken back (`push --force`, a `+` refspec (`push origin +main`), `reset --hard`,
  `clean -f…`, **`switch --discard-changes` / `switch --force`**, **`checkout` / `restore` with a path**, and **a write through `-C` / `--work-tree` / `--git-dir`
  that points at another worktree**) — **because `ask` was measured to pass straight through in bypass permissions mode**. If one of them has to go through,
  check with the user and exempt it with `LOOPTRACK_LOOP_GIT_GUARD_ALLOW`. **The hook catches nothing but slips**: a nested shell, `eval` and a prefix such as `sudo` are unwrapped first,
  but command substitution, a line continuation, an unquoted `C:\…\git.exe` and a wrapper not on the list go straight through ("What the git guard stops and what it does not" at the end of this file).
- **Never mistake the AI stopping to check on its own for the hook stopping it.** Before a dangerous operation an AI may stop and ask the user
  on its own whether to go ahead. At that point no tool call has been attempted yet, and the hook has not fired. "The AI stopped" is not "the hook stopped it".
  When you check whether a guard works, judge by the real output of the hook's response (the wording of the `deny` reason) and of the operation's result (the target left unchanged).
- **Never put another session's worktree back with `checkout` / `restore` / `reset --hard`. Even a `checkout` with a path cannot restore the other side's uncommitted changes** (they were actually lost).
- **The scratchpad is per session, but the parent and every one of its subagents share the same one** (ask a subagent to report the path of
  its own scratchpad and it gives back the parent's). Give every file you use for measurement a name that cannot collide (a suffix unique to
  your agent), and when a result comes back empty or unexpected, suspect that the measurement broke before anything else (what actually
  happened: subagents running in parallel silently overwrote the parent's measurement script, and the empty result was nearly read as "there are no holes").
- **Every session shares one and the same handoff file. Put the single line the hook shows you
  (`<!-- looptrack:session <session id> -->`) into the section you write, verbatim.** Without that mark, a write from another
  session folds your own marker away as "updated", and your handoff quietly ends up never written.
  The hook prints the real line when it detects finished work and when it sends you back, so you never have to look your session id up.
  A file that carries no mark at all is judged by its modification time, as before (`LOOPTRACK_LOOP_HANDOFF_WRITER=mtime` pins it to the time alone).
- **While parallel sessions are running, never edit the handoff directly — append with `looptrack handoff append`** (read it and write
  the whole thing back, and you wipe out the section another session wrote in between). **To stop using the marks once you are back to one
  session, run `looptrack handoff compact --drop-marks`** (while a file carries marks, an update without your own mark is sent back by the freshness guard).
- **Finishing one task is not the end of the session.** Never treat "idle" and "finished" as the same thing.
- **The moment you detect a conflict, stop and report it to the user.** Never keep writing after you have detected one.

### Subagents (delegation is the default; the parent's context is for judgment)
- **Keep the parent's context for judgment, and hand out work whose procedure is settled.** This is not a preference but a question of how long a session lives.
  The more the parent does by hand, the more is resent per response, and the sooner it hits the `session-scope-guard` warning (250,000 tokens per response by default).
  **Unless you plan around delegation, a single session is too short-lived to finish the job.**
  - **Hand out**: searching, research, implementation, running tests, **merges and conflict resolution**, **filing issues and commenting**,
    **editing documents**, and updating records (handoffs, notes). Even work that feels like it "can only be done step by step" can be handed out if its procedure is settled.
  - **Keep for the parent**: who gets assigned what, verifying the results that come back, **deciding whether to undo something**, and the conversation with the user.
  - What actually happened: five parallel jobs went out to subagents, and then the parent carried 3 merges, 3 conflict resolutions, 5 issues filed, edits to documents in both languages
    and 6 messages to other sessions itself, and hit the warning in about an hour (every one of them could have been handed out).
- **Put "keep your report within N lines" into every instruction you give a subagent.** The report itself eats the parent's context
  (what actually happened: one report ran past 100 lines). What you need is the conclusion, the paths that changed and the pass/fail of the verification — not a retelling of what was read.
  Ask only for what the parent needs to make the next decision, and specify the line count.
- **Never take a result on trust.** Always verify what comes back before you integrate it.
  **The verification can go to a subagent too** — a different agent from the one that did the work, asked to confirm it with quotes from the actual output.
  What the parent does is decide whether to accept it as verified.
- **When you hand a subagent the push itself, put into the instruction that it shows the parent the full set of checks and never pushes before the parent has approved them.**
  Write "push if everything is green" and the report only arrives after the push, so have them send **the numbers from the checks first, in short form** (the full report can follow). **"A green carries its numbers" applies when the one you are telling is the parent, too — and it has to apply before the push.**
- **When you run jobs in parallel, give each one its own worktree.** Never put two into the same worktree
  (what actually happened: two went into the same worktree, and uncommitted changes were really lost). Give each worktree its own branch, and stage only the paths you touched.
- **Match the model to how hard the task is. The axis is whether a mistake would be noticed.**
  **State the model explicitly on every call** (on Claude Code, `pre-tool-subagent-model` stops a launch with no model set).
  - **A lighter model is fine** for work a check can mechanically reject (mechanical merges and conflict resolution, running the checks and transcribing the results,
    filing issues, document formatting, updating records). If it breaks, a test or a check goes red, so nothing slips through and stays.
  - **A medium model is fine** for implementation, research and summaries whose procedure is settled. A mistake there turns a test or a check red.
  - **Use a heavier model** for work that **quietly gets worse without ever failing** (the quality of a translation, consistency with how things are already written, isolating a cause,
    deciding what to drop and what to keep). The checks pass, so the mistakes stay in, unreviewed.
- **When you hand out work that waits on something in the background, copy the bound into the instruction verbatim**
  (→ "What to copy into a subagent's instructions" in background-process.md). **The rule is injected into the parent only, so it thins out the moment you paraphrase it.**
  Three things go into the instruction: always carry a bound (a maximum number of attempts or a deadline), **confirm that the thing being waited on exists before the wait starts**,
  and **report, in one line, every background process started and that it was stopped**. The parent checks for that one line before integrating the result
  (three unbounded `until` loops really were created in a single day; one of them was waiting on a path that did not exist).

---

## Optional sections (not injected; adopt only what fits your project)

### Optional: staying in sync with the remote (when sessions in another environment touch the same repository)
- Another environment (a cloud session and the like) does not show up in the session list, so **read it mechanically from how far ahead the git remote is**
  (fetch every branch before you start, and check for branches behind, diverged, locally ahead but unpushed, or parallel and not yet taken in).
- **Never start a session in another environment while local commits sit unpushed** (that environment branches off an old base, and the work gets done twice).
- **Never start writing while you are behind.** After taking the changes in, confirm that the premises of the work you started (the version of the requirements or the design) have not changed, then resume.
- To automate it, put a hook in your own project that fetches on SessionStart / UserPromptSubmit and prints only when there is divergence (do not put it in the kit).

### Optional: projects whose deliverable is documents
- **Reflect a change of specification in the original document before you close the work** (never leave it in a side note or in the chat).
- Keep terminology in one glossary, and never coin a synonym.

---

## What the git guard stops and what it does not (not injected; for reference)

`pre-tool-git-guard` first turns the command string into **the words that actually run**, then looks at whether the
**first word of a simple command** — split on `;`, `&`, `|`, parentheses and newlines — is `git`. It does that in two
stages, and it calls **the same code** as the secrets guard and the wait-loop guard (copy a rule into two places and only
one of them gets fixed next time). The only difference is an argument: **in which position a nested shell is unwrapped**.
A redirection (`2>&1`, `2>/dev/null`, `>out.log`, `&>/dev/null`) does not end the simple command, and the file descriptor
attached to the operator with no space (the `2` of `2>&1`) and the target (`/dev/null`) are dropped from the words before judging.
So `git checkout main 2>&1 | tail -20` goes through as a branch switch, while `git checkout -- <path> 2>&1` is `deny` as before.

1. **Unwrap one level of a nested shell** (the quotes of `-c` for `bash` / `sh` / `zsh` / `dash`, and of `eval`;
   a run-together short option such as `-lc`, `-ec` or `-o pipefail -c` is read as `-c` too).
   **The shell's name, `.exe` included, is matched case-insensitively** (the macOS and Windows filesystems do not
   distinguish case, and `BASH -c 'git clean -fd'` and `BASH.EXE -c '…'` really do run; measured on a real machine).
   **`eval` alone is case-sensitive** (it is a builtin, so `EVAL` comes back `command not found`; measured on a real machine).
2. **Drop the prefix words** (`sudo`, `doas`, `env`, `xargs`, `nohup`, `time`, `command`, `nice`, `stdbuf`, `timeout`,
   `setsid`, `flock`, `script`). One at the head of what was unwrapped (`bash -c 'sudo git …'`) is dropped too.

**The git guard alone carries one more stage, when it splits into the words of a simple command.** It runs against
**both** the posix rule (`\` cancels the next character — Git Bash included) and a **Windows rule** (`\` is not
treated as an escape at all), and fires when either one reads the first word as `git` / `git.exe`. An unquoted
Windows absolute path (`C:\tools\git.exe`) loses its separators under the posix rule and comes out as
`C:toolsgit.exe`, which no longer matches — but the Windows rule keeps `C:\tools\git.exe` as one word, unbroken.
The posix side does not change by a single bit, so Git Bash stays untouched (the author decided on this design,
"option 1"). **A path with a space that is not quoted** (`C:\Program Files\Git\cmd\git.exe`) is not rescued by
this doubling either way, because the shell itself splits on the space regardless of which rule is used (quote it
and it still stops as before).

When it pairs up quotes, an escaped `'` (`echo don\'t ; bash -c '…'`) and a `\"` inside double quotes do not count as quotes.
When it unwraps in command position, a comment (from a `#` at the start of a word to the end of the line) is skipped too.

**Unwrapping happens in "command position" only** (for the git guard and the wait-loop guard; the secrets guard does it whatever the position).
Command position means the start of the string and right after a separator outside quotes, **plus right after anything
that a command follows**:

- the words of a compound command: `if`, `then`, `else`, `elif`, `do`, `while`, `until`, `!`, `{`
- a variable assignment: `x=1 bash -c '…'`, `FOO=bar BAZ=1 bash -c '…'`
- a prefix word and its options (a path to the executable included): `sudo bash -c '…'`, `timeout 30 bash -c '…'`,
  `/usr/bin/sudo bash -c '…'`. **A prefix word swallows option-shaped words only** (`-x`, `VAR=v`, a number),
  and a short option that takes its value as a separate word swallows that one word
  (`env -u FOO bash -c '…'`, `sudo -u deploy bash -c '…'`, `xargs -a f bash -c '…'`). `flock` and `script`
  take one operand before the command (`flock /tmp/l bash -c '…'`, `script -q /tmp/o bash -c '…'`).
  **Never let it swallow an arbitrary number of words**: that makes the `bash -c` of
  `timeout 30 ssh host bash -c '…'` look like command position and **turns forms that only run remotely into `deny`**
  (24 forms, measured).
- right after `find … -exec` / `-execdir`

**A word that is merely passed as an argument is not included.** `deny` does not pass through even in bypass permissions
mode, so stopping a form that runs nothing would stop the very work of writing about this guard. These therefore
**go through, as measured**: `echo bash -c 'git reset --hard'`, `ls -l bash -c '…'`, `printf '%s' bash -c '…'`,
`rg -n "bash -c 'git reset --hard'" docs/`, `git commit -m "bash -c 'git reset --hard' is stopped"`, and
**the same with a prefix word in front** (`timeout 30 ssh host bash -c '…'`, `nohup ssh host bash -c '…'`,
`env DOCKER_HOST=x docker run img bash -c '…'`, `sudo echo bash -c '…'`, `sudo ls -l bash -c '…'`).

**Forms measured as `deny`**: `git push origin +main`, `sudo git push origin +HEAD:main`, `sudo git reset --hard`, `xargs git reset --hard`, `env git clean -fd`,
`setsid git reset --hard`, `bash -c 'git reset --hard'`, `eval "git clean -fd"`, `BASH -c 'git reset --hard'`,
`BASH.EXE -c '…'`, `/usr/bin/sudo bash -c '…'`, `sudo bash -c '…'`, `x=1 bash -c '…'`,
`if bash -c 'git reset --hard'; then …`, `find . -exec bash -c 'git clean -fd' \;`, `echo don\'t ; bash -c 'git reset --hard'`,
`case a in a) git clean -fd;; esac` (that one stops without any nesting, because `)` is a separator), and
`C:\tools\git.exe add -A` (an unquoted Windows absolute path, spaces excluded — caught by the second, Windows-rule
tokenizer; a program that does not call itself `git`, such as `C:\tools\notgit.exe add -A`, still goes through as before).

### What it does not stop (ruled out, or out of reach — all measured)

- **A compound-command word with `git` directly after it**: `if true; then git reset --hard; fi`, `{ git reset --hard; }`,
  `! git push --force origin main`, `x=1 git reset --hard`, `find . -exec git clean -fd \;`,
  `for f in a; do git clean -fd; done`, `while git reset --hard; do :; done`.
  **"Command position" applies to the unwrapping stage only, not to the body that looks at the first word of a simple
  command** (the first word becomes `then`, `x=1` and so on). **It goes through at the base too**, so this round did not open it.
- **A wrapper not in the list, plus a nested shell**: `caffeinate bash -c 'git reset --hard'`. It is in neither the prefix
  words nor the compound-command words, so it never reaches command position (**it goes through at the base too**).
- **A prefix carrying an option that takes its value as a separate word, with `git` directly after**:
  `env -u FOO git reset --hard`, `sudo -u deploy git clean -fd`, `flock /tmp/l git clean -fd`. It cannot be read through
  as a prefix, and the leftover value word becomes the first one (**it goes through at the base too**).
- **A nested shell that is not in command position** (the "merely passed as an argument" forms above). What was let
  through is two classes — **(i) in argument position, running nothing** and **(ii) running somewhere other than the
  local machine** (`ssh`, `docker run`, `docker exec`, `kubectl exec`) — and that is **for the git guard only**.
  **The secrets guard does not care about the position, so it confirms these** (`ask` is something a person can wave through).
- **Calling git from some interpreter** (from inside `ruby -e '...'`), **building the command name out of a variable**
  (`G=git; $G reset --hard`), and **aliases and functions** (`alias gr='git reset --hard'; gr`). The string after
  expansion never reaches the hook.
- **A nested shell two or more levels deep** (`bash -c "bash -c 'git reset --hard'"`). Unwrapping is one level only, by design.
- **A run-together short option where `c` is not last** (`bash -cx 'git reset --hard'`, `-cl`, `-ce`). A real shell runs it,
  but `-c` is read as the **last** of a run of short options, so it does not match (**it goes through at the base too**).
- **Handing it over with ANSI-C quoting** (`bash -c $'git reset --hard'`) and **piping into a shell**
  (`echo 'git reset --hard' | bash`, `| sh`, `| bash -s`).
- **Command substitution** (`echo "$(git reset --hard)"`, backticks) and **a line continuation**
  (`git reset --hard` plus a backslash and a newline).
- **What follows a quote inside a comment** (`git status # don't` + newline + `bash -c 'git reset --hard'`). The unwrapping
  stage skips the comment, but the stage that splits simple commands counts the `'` in it as an opening quote and takes
  the next line for quoted text.
- **An unquoted Windows absolute path that contains a space** (`C:\Program Files\Git\cmd\git.exe clean -fd`).
  Even with the Windows-rule tokenizer added, a space still splits into a separate word under either rule, so
  `words[0]` ends at `C:\Program` and never reaches `git.exe` (**goes through at the base too**). A space-free
  absolute path (`C:\tools\git.exe clean -fd`) is caught — see "Forms measured as `deny`" above. Quoted, it is stopped.
- **Destruction that does not go through `git`** (`rm -rf .git`, `find … -delete`). **The git guard is a hook that looks
  at git commands**; an arbitrary deletion is not what this hook is for (it is a separate problem).

The hook catches nothing but slips, and is no substitute for the discipline.

---

## Maintaining this file

- As the discipline grows, write anything project-specific into your project's own rules (this file in the kit is common to every project).
- Never let a correction from the user end with the fix in front of you: promote it into the rules or into memory (the backend of the handoff).
