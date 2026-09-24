# Background process discipline (never leave a child process running)

> A rule from kit/loop. Paired with the Stop hook `stop-runaway-background-process.sh`, which detects long-lived child processes this session started and sends the turn back.

## Key points
<!-- looptrack:inject session codex copilot -->

- Give every background loop a bound — a maximum number of attempts or a deadline. When it is reached, stop, give up, and report why. Never write an unbounded `until` / `while`.
- Wait on an identifier that cannot disappear (poll CI by pull request number, never by a commit SHA that a force-push can erase). Kill every background process once you are done with it.
- When you hand background waiting to a subagent, **copy the bound into the instruction verbatim** (this rule is injected into the parent only). Have it confirm that the thing being waited on exists before the wait starts.

## Rules for background polling

When you poll in the background for a completion marker or for CI to finish, **always give the loop a bound**.

- **Never write an unbounded `until` / `while`.** Carry either a maximum attempt count or a deadline, and when it is exceeded, **give up and report the reason** — do not spin on in silence.
- **Assume whatever you are waiting on can disappear.** Poll CI by a durable identifier such as the pull request number.
  If you wait on a commit SHA, a rebase plus force-push deletes that commit and your exit condition becomes unreachable by construction.
- The same applies when a subagent does the polling: if the parent rewrites the branch, what the child is waiting for is gone.
- Kill every background process once you are done with it.

> What actually happened: two unbounded polling loops started by a subagent ran for over 15 hours and hit an external API roughly 6,400 times.
> They were waiting on a SHA the parent had already force-pushed away. **They appeared in neither the subagent list nor the worktree list — only on the user's screen.**

## What to copy into a subagent's instructions

This rule **is injected into the parent session as a rule and nothing more — it does not reach a subagent's instructions on its own**.
Unless the parent copies it across, the child writes an unbounded waiting loop as the natural thing to do. It starts waiting without knowing
that what it waits on can disappear along the way, or that the work may already have finished by the time it starts.

**Measured: three unbounded `until` loops were created in a single day.**
In two of them the work being waited on had already finished (a person found them with `ps` and killed them).
In the third, **the path being waited on did not exist at all** (confirmed with `ls`; the exit condition could never be met by construction,
and the process would have outlived its parent session as an orphan).
**All three were found by a person running `ps`, and none had reached the Stop hook's threshold (30 minutes by default).**
This very rule had carried the "15 hours, roughly 6,400 calls to an external API" example for some time already.
**Three more were created on a single day despite that example being written down**, so writing the rule here is not enough. **Push it down into the instructions.**

**When you hand out work that waits on something in the background, put these three lines into the instruction verbatim** — do not paraphrase them.

```text
Do not write a loop that waits in the background. If you must wait, always carry a maximum number of attempts or a deadline,
and when it is exceeded, give up and report why. Before you start waiting, confirm on the spot that the thing you are waiting on
exists (if it does not, do not wait — report it then and there). Stop every background process you started yourself, and put one
line in your report naming the background processes you started and that they were stopped.
```

**On Claude Code these three lines are appended to the instructions automatically** by `pre-tool-subagent-bound` (PreToolUse):
the fixed marker `[looptrack:background-bound]` goes in front of them, instructions that already carry the marker are left alone, and nothing is stopped.
**On the other agents nothing reaches the child by itself**, so the parent copies the three lines across verbatim (the kit README says where it works).

The parent **verifies, from the report, that the background processes were stopped, and only then integrates the result**. If that one line is missing,
run `ps -eo pid,ppid,etime,command | grep -E 'until|while' | grep -v grep` yourself before integrating, and kill whatever is still there.

## Detection (before it starts, right after a child finishes, at the end of a turn)

- **Before it starts** (`pre-tool-wait-loop-guard` on PreToolUse): a wait loop with no bound (`until` / `while` whose body holds a `sleep`
  and which carries no bound) is stopped with `deny`. A bound is `break`, `timeout`, `seq`, `SECONDS`, `date +%s`, a numeric comparison
  (`-ge` `-gt` `-le` `-lt`), `--max` and the like. Once stopped, rewriting it one of the ways the reason names (a maximum number of attempts,
  a deadline, `timeout`) lets it through. Exempt with `LOOPTRACK_LOOP_WAITLOOP_ALLOW` (space separated). When the loop **does carry a bound but
  the parent directory of what it waits on does not exist**, nothing is stopped and only a notice comes back (waiting for a file that does not
  exist yet is the right way to wait, so it is not stopped).
  A wait loop wrapped by a prefix word or by `eval` (`sudo sh -c '…'`, `nohup bash -c '…' &`, `setsid sh -c '…'`, `time while …`,
  `eval "while true; …"`, `sudo -u deploy sh -c '…'`) is stopped too (the prefix words and the unwrapping are the same code as the git guard's and the secrets guard's).
  **It still catches nothing but slips**: a prefix word not on the list (`caffeinate bash -c '…'`) and argument position
  (`ssh host bash -c '…'`, `docker run img bash -c '…'`) go straight through, leaving the detection by elapsed time below.
- **Right after a child finishes (SubagentStop) and at the end of a turn (Stop)**: `stop-runaway-background-process`.
  It looks at the shells a coding AI spawns for its Bash tool (`bash -c` / `zsh -c` and shell snapshots) that have been alive longer than the threshold (30 minutes by default).
  **A shell with the shape of an unbounded wait loop is judged on a shorter threshold** (10 minutes by default, `LOOPTRACK_LOOP_RUNAWAY_LOOP_THRESHOLD_MIN`); everything else stays at 30 minutes.
- MCP servers, dev servers, docker, editors and the like are legitimately long-running, so they are excluded. Add anything your project runs for a long time with
  `LOOPTRACK_LOOP_RUNAWAY_ALLOW` (a regular expression); set the threshold with `LOOPTRACK_LOOP_RUNAWAY_THRESHOLD_MIN`.
- When you are sent back: kill the process if it is no longer needed, rewrite it with a bound if it is, or record why it must keep running if it legitimately must.
