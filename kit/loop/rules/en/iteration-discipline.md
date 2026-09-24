# Implementation iteration discipline

> A rule from kit/loop. Given that implementation and testing run unattended, it lays down "every problem you find becomes an issue" and "never pile new implementation on top of problems you are still carrying".
> Paired with: SessionStart `session-start-iteration` (injects the open bugs) /
> `looptrack gates` (runs the gates) / the skill `/iterate` (the procedure).
> If your project's own rules carry a project-specific implementation discipline (workflow, the definition of the test tiers, commit conventions), that one wins wherever the two conflict.

## Key points
<!-- looptrack:inject session codex copilot -->

- One iteration = one implementation issue: implement → gates (`looptrack gates`; if even one stage is missing, do not call it green) → confirm zero problems → close (with a comment showing the evidence) → next.
- File every problem you find on the spot with `--type bug` (reproduction / expected / actual / the stage that caught it). Never pile new implementation on top of open bugs — fix what you can first.
- Resolving a bug = the fix + a test that catches the same problem + all gates green + the real output quoted in the closing comment.

## 1. What one iteration is

One iteration = one implementation issue. Go in this order, and **never skip a stage**:

```
implement → gates (build → lint → test …) → confirm zero problems → close (with a comment showing the evidence) → next implementation
```

- Run the gates with `gates.sh` (a wrapper around `make -C <directory> <stage>`; fail-fast, saves the log).
  The project decides the stages and the directory (as arguments or through the environment variables `LOOPTRACK_LOOP_GATES_STAGES` / `LOOPTRACK_LOOP_GATES_DIR`).
  Even when you ran the tests by hand, always put every stage through `gates.sh` before you close.
- **If even one stage is missing, never report it as green.** Treat a stage that is not in the Makefile, or that has no recipe, as a failure (this is what keeps a "false green" that PASSes without running anything from happening).

## 2. Turning problems into issues (finding one means filing one)

A "problem" is a failing gate, a failing test, a discrepancy against the design, unexpected behavior, or a defect in existing code that you found while implementing.

- File it with `--type bug` **the moment you find it**. Add `--traces` to the issue for the requirement in question, and write
  **the steps to reproduce / expected (the relevant part of the requirement or the design) / actual (quoted from the real log) / the stage that caught it** in the body.
- Never move on carrying a problem in the conversation, in a TODO comment, or in your head. A problem that is not filed ends up treated as one
  that does not exist, and once the conversation is over nobody finds it again (it lasts only as long as someone remembers it).
- When it is a discrepancy against the design, it is not a bug but a design question to send back (put it to the user with a record of the decision). If you are unsure, file it as a bug and write "design needs confirming" in the body.
- **Never pile new implementation on top of open `type: bug` issues (`looptrack issue list --type bug`).** SessionStart prints the count and the list,
  so look at it before you start and fix what you can (if one stays open, write on the issue why it is not being fixed now).
- Resolving a bug = the fix + a test at the stage that caught it (so the same problem is never found by hand twice) + every gate green + the real output quoted in the closing comment.

## 3. Principles for running unattended

- You may keep going through "implement → gates → file the bug → fix → close → next implementation" without asking the user at each step
  (within the scope of the parent issue (the epic). Stop and put it to the user only when the scope or the design has to change).
- Turn the acceptance criteria into automated tests. For verification that cannot be automated (a visual check and the like), leave "what you checked and how" in a comment on the issue.
- Commit per issue. When an iteration ends, update the handoff (the skill `session-handoff`).

## 4. Maintaining this file

- The project decides the real gate commands (the stages, the directory) and puts them in the Makefile and in `LOOPTRACK_LOOP_GATES_*` (never duplicated in this document).
- Promote what you learn from running it into your project's own rules or into memory (the backend of the handoff).
