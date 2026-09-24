---
name: iterate
description: How to drive an implementation iteration on its own (start → implement → gates → file what you find → close → next). Use it when you start or finish an implementation issue, and when you recover from a failed gate.
---
<!-- Created by looptrack:init (once you edit it by hand, looptrack issue init leaves it alone) -->

# The implementation iteration (/iterate)

> The discipline itself lives in the rule `iteration-discipline.md`. This file is only the procedure.
> If your project's own rules carry implementation discipline of their own — workflow, test tiers, commit conventions — follow those instead.
> For issue operations in general, see the skill `/issue`.

## 0. Check the preconditions (at the top of every iteration)

```bash
looptrack issue list --type bug     # look at the open bugs ("None" = nothing open)
looptrack issue next --dry-run      # the issue you would start next (a look, nothing more)
```

- **Never pile new implementation on top of open bugs.** Fix what you can first (the recovery loop in §3), and put the rest to the user.

## 1. Start

```bash
looptrack issue next                # takes what you already have in progress, or moves the highest ready issue to In Progress
```

- Read the requirement and the design behind the issue, always before you implement. When a call would diverge from the design, do not implement on a guess: file an issue and put it to the user.
- Record what you decided along the way — the calls that stay inside the allowed range — as a comment on the issue.

## 2. Implement, then run the gates

Implement, and write the tests **from the acceptance criteria**: each criterion becomes a test name and an assertion. Then:

```bash
looptrack gates               # stages come from LOOPTRACK_LOOP_GATES_STAGES (build lint test by default); fail-fast
```

## 3. When it fails (the recovery loop)

1. **One problem, one bug, filed on the spot** — never bundle them. Give reproduction / expected, from the relevant part of the requirement or design / actual, quoted from the real log / the stage that caught it:

   ```bash
   looptrack issue new "<the symptom in one line>" --type bug --priority P1 --traces <the requirement issue> --body "$(cat <<'EOF'
   Reproduction: …
   Expected: … (the relevant part of the requirement or design)
   Actual: … (quoted from the real log)
   Caught at stage: …
   EOF
   )"
   ```

2. Move the bug to In Progress and fix it.
3. **Add a test that catches the same problem**, then run `gates.sh` again.
4. Once every stage is green, close the bug, quoting the real output. Repeat 1–4 until everything this round turned up is dealt with.

## 4. Finishing (what close requires)

Close only once all of these hold:

- `gates.sh` is green on every stage — quote the `═══ gates result ═══` block verbatim in the comment
- everything this round turned up has been filed, and whatever you fixed has been closed
- a pass or fail for every acceptance criterion; for anything you could not turn into an automated test, what you checked and how

```bash
looptrack issue close <ID> --comment "<the quoted gates result + pass/fail per acceptance criterion>"
```

- Commit — one commit per issue, staging only the paths you touched — then update the handoff (skill `session-handoff`).

## 5. On to the next (running unattended)

- Go back to §0; you may move on to the next issue without checking in with the user each time.
- **Stop and put it to the user when**: the work runs past the scope of the parent issue / the design has to change / the requirement can be read more than one way /
  the same bug has survived three attempts at a fix — at that point the approach itself needs rethinking.
