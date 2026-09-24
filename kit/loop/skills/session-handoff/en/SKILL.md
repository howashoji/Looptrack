---
name: session-handoff
description: Whenever a unit of work is finished, bring the handoff to a state the next session can resume from by reading it alone. Use it when the handoff freshness guard (a Stop hook) sends you back, right after you close an issue or commit, and when you wrap up a session.
---
<!-- Created by looptrack:init (once you edit it by hand, looptrack issue init leaves it alone) -->

# Updating the session handoff (session-handoff)

**There is exactly one goal**: if this session were cut off at this very moment, the next session could tell what is finished, what is left and what comes next
**by reading alone**, and pick the work up from there.

Writing the handoff only when a session ends **loses it every time the session dies without that ending — context exhausted, a walk-away, a crash**.
What actually happened: right after four pieces of work were finished, the handoff still described the state from two units earlier, and issues that were already done
were still sitting there as "paused, waiting". The next session resumes from that wait. The harm is not that the handoff is old, it is that it **actively misleads**.

## When this fires

- When the Stop hook `stop-handoff-freshness.sh` sends you back ("The handoff has not caught up with the work you finished.")
- When the same Stop hook reports a **lost record** ("they are gone, not merely stale"). That needs a different response from staleness: **write the content back while it is still in this context**
- Right after you close an issue (close / Done / Canceled), and right after you commit your work — `post-work-complete-handoff-mark.sh` prompts you
- When you wrap up a session, before you move to a different task, and when little context is left

## Procedure (the same on every backend)

### 1. Gather the facts from live values (never write them from memory)

```bash
date '+%Y-%m-%d %H:%M'                                  # the timestamp you will write in the handoff — do not guess it
git status --short; git log --oneline -5                # the worktree and the most recent commits
looptrack issue list --status "In Progress"   # issues in progress
cat .claude/handoff-pending.d/* 2>/dev/null             # events finished in this session (the freshness guard's markers)
```

If anything is unmerged or undeployed, measure that too — how many commits sit on which branch, and which environment has not received them.

### 2. Read the existing handoff

Read it from the location given in the "Backends" section below.

### 3. Update the handoff (in place with one session; `append` with parallel sessions)

**With one session, rewrite the one latest entry** (add a new file or a new record and let them coexist, and nobody can tell which one is current).
**With parallel sessions, add your own section with `looptrack handoff append`, as below** (writing the whole file back after reading it wipes out
another session's section; fold the sections down with `compact` once they pile up). Either way, always include:

| Item | How to write it |
| -- | -- |
| The `> 要約:` line, the summary (file backend) | One line. It shows up in the list at the start of a session. **Never omit it** |
| Last updated | `YYYY-MM-DD HH:MM` — the value `date` actually gave you |
| Where things stand | What is done and how far: branch, paths to the deliverables, the real count of what is unmerged or undeployed |
| **Work finished** | Issue IDs and the outcome (Done, merged, and so on) |
| **Work paused** | **How far you got, and what it takes to resume.** If it is blocked, the reason and what it is waiting on |
| Open questions | Who decides what, and by when |
| What to start next | In priority order |

**1,500 characters at most.** Step-by-step implementation detail, changed files and verification results belong in comments on the issue; only the essentials go here.
What is injected at the start of a session goes up to `LOOPTRACK_LOOP_MEMORIES_MAX_CHARS` (6,000 characters by default); past that, in go the **tail sections**
and **the nearest section to the tail whose heading says 現在地 / Where things stand / Current**. **Add the current state at the tail, or update it where it is**
(never leave a stale one sitting at the top).
Once it has grown past the limit, move the old sections into a separate file (`archive-*.md`) or fold them down to the essentials.

**Carry the writer's mark (the file backend, the default)**: put the single line the hook shows you
(of the form `<!-- looptrack:session <session id> -->`) into the section you write, verbatim. **Every session shares one and the same handoff file**,
so a modification time alone cannot say who wrote it, and a write from another session would clear your own marker.
The hook prints the real line when it detects finished work and when it sends you back, so you never have to look your session id up.
**A handoff that carries no mark at all is judged by its modification time, as before** (nothing changes for a project that does not use the mark yet;
a file becomes strict once anyone has written a mark into it once). To pin it to the earlier, time-only judgment, set `LOOPTRACK_LOOP_HANDOFF_WRITER=mtime`.

**While parallel sessions are running, never edit the file directly — append with `looptrack handoff append`**: every session shares
one and the same handoff file, so reading it and writing the whole thing back wipes out any section another session wrote in between
(the side that was wiped gets no error, so it is lost silently). `append` writes once with `O_APPEND`, so concurrent writes neither
interleave nor get lost, and `append` also puts in the mark right after the heading (you never have to look your session id up).
Use `compact` only to fold the whole file down. `compact` makes you pass the SHA-256 as of when you read it, and if another session
has written since, it **writes nothing and refuses** (read it again, then retry).

```bash
looptrack handoff append --title "<heading>" - <<'EOF'
…the body…
EOF

shasum -a 256 <path to the handoff>                   # the SHA-256 as of when you read it
looptrack handoff compact --sha <SHA-256> - <<'EOF'   # fold the whole file down (add --drop-marks to drop the marks)
## …
EOF
```

**Going back to a single session (giving up the marks)**: once you start using `append`, the file carries marks, so the freshness
guard switches to its strict judgment and **an update without your own mark is sent back at Stop** (a hand edit does not count as
"written by this session"). When the parallel sessions are gone, you are back to one session and you want to stop using the marks,
drop the mark lines with `compact --drop-marks` (a file with no mark left in it goes back to being judged by its modification time, as before).

### 4. Keep decisions and lessons somewhere else

Do not bury the reasoning behind a decision, or a lesson learned, in the body of the handoff — the next update overwrites it and it is gone.

- A decision and why it was made → a decision record (`decision-<topic>.md` on the file backend)
- A lesson that prevents a repeat, or a pitfall → a caveat record (`caveat-<topic>.md` on the file backend)
- A working rule to be kept every time → not memory but rules (your project's own rules)

Each of them opens with `# <title>` and `> 要約: <one line>`, and the handoff refers to it by name.

### 5. Clear the markers

Once you update the handoff, the hook clears the markers itself at the next Stop. Clear them by hand only when you have judged that no handoff is needed, and record why.

## Backends (pick one per project — `LOOPTRACK_LOOP_HANDOFF_BACKEND`)

### file (the default) — `.claude/memories/`

- The handoff: `.claude/memories/handoff.md`, which `LOOPTRACK_LOOP_HANDOFF_FILE` can change. Decisions and caveats go in the same directory as `decision-*.md` / `caveat-*.md`.
- **The path is resolved against the main worktree.** Written or read from inside a worktree (one made with `git worktree add`), it is still the one
  `.claude/memories/` of the main worktree. Written into the worktree instead, it is read by nobody and goes away with `worktree prune`
  (when `LOOPTRACK_LOOP_MEMORIES_DIR` / `LOOPTRACK_LOOP_HANDOFF_FILE` names a path, that one still wins, as before).
  When a handoff is found left behind outside the main worktree, Stop says so once (it does not block).
- At the start of a session, `session-start-memories.sh` injects the handoff (up to `LOOPTRACK_LOOP_MEMORIES_MAX_CHARS`, 6,000 characters by default;
  past that, only the newest sections at the tail plus the nearest 現在地 / Where things stand / Current section to the tail)
  plus a list of the `> 要約:` lines from the other files.
- Freshness: the handoff is current when an update carrying **this session's mark** (see "Carry the writer's mark" above) is newer than the markers of the finished events.
  Another `*.md` next to it counts too. A handoff with no mark in it at all, and `LOOPTRACK_LOOP_HANDOFF_WRITER=mtime`, go by the modification time alone (the earlier behavior).
- A commit that changes nothing but the handoff file does not count as a finished event, so committing as the discipline asks does not leave the guard spinning.
- Loss: when a `*.md` that was there last time is gone, Stop reports it as a loss (in different words from staleness). The snapshot of what was seen last time
  is kept in two places, `.claude/.looptrack-freshness/` and `<the git directory>/looptrack/`, so the loss is still caught when all of `.claude/` goes.
  **Keeping the records under version control is the surest thing** (then they can be restored); detection is the last net, for noticing.

### auto-memory — Claude Code's auto-memory

- The handoff: the line naming the current work in `~/.claude/projects/<the project's path>/memory/MEMORY.md`, and the separate file it points to
  (`LOOPTRACK_LOOP_HANDOFF_MEMORY_DIR` can change it). Keep only a one-line pointer in `MEMORY.md` and write the body in that separate file.
- Freshness: the handoff is current when `MEMORY.md` is newer than the markers of the finished events. **Never fix the separate file alone and leave the matching line in
  `MEMORY.md` behind** — the next session starts reading from `MEMORY.md`, so do not leave finished work under "what to do next".

### command — for projects that keep memory in a database or the like

- Where the handoff lives (sqlite, an MCP memory server, whatever it is) and how freshness is judged are for the project to write into `LOOPTRACK_LOOP_HANDOFF_CHECK_CMD`
  (exit code 0 = current / 1 = stale / anything else = cannot tell). The path to the markers arrives in `LOOPTRACK_LOOP_HANDOFF_PENDING`.
- For example: make it a convention that the handoff body always carries `master=<short sha>`, and have the check command compare it against the sha the mainline has reached
  (leave commits that only sync memory out of that tip, and compare against the mainline rather than HEAD so that merely sitting on a working branch is not a mismatch).
- Update through the memory tool's "update", replacing the one latest entry; do not line entries up with "add".

## What not to do

- **Stacking handoffs up as a new file or a new record** (nobody can tell which is current; with one session rewrite the one latest
  entry in place, and with parallel sessions add a section with `looptrack handoff append` and fold them down with `compact`)
- **Leaving out the timestamp or the `> 要約:` line** (freshness cannot be judged / the list says nothing about the contents)
- **Writing paused work down as nothing but "what is left"** (the next session has to rediscover how to resume; write **how far you got and the next single step**)
- **Writing "finished" and forgetting to say that it is unmerged or undeployed** (always state where the change has landed)
- **Burying the reasoning behind a decision, or a lesson, in the handoff** (an update wipes it; split it into a decision or caveat record)
- **Writing step-by-step implementation detail** (that goes in comments on the issue; the handoff carries only the essentials)
