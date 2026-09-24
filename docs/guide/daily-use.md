# Daily use

[Guide contents](README.md) · Previous: [Getting started](getting-started.md) · Next: [Agent-specific notes](ai-agents.md)

This page uses CLI commands.
The MCP tools do the same things (`create_issue`, `next`, `add_comment`, `set_status`, and so on).
The server applies the same checks on either path.

## A typical day

1. When an agent session starts, a hook shows the agent the `summary` (all three loops)
2. If anything is waiting for you (②) or an outside reaction is unanswered (③), the agent raises it first. It records your answer as a comment
3. The agent picks up work with `next` and repeats work → verify → close
4. Anything that needs a decision collects In Review. Answer them in a batch when it suits you
5. When you hear how people are using the product, tell the agent so it records the reaction on the issue

To check things yourself, these commands help:

```bash
looptrack issue summary                    # the three-part summary
looptrack issue list --status "In Review"  # waiting for a person
looptrack issue list --has-feedback        # issues with unanswered reactions
looptrack issue ready                      # ready to start
looptrack issue list --assignee me         # assigned to you
```

## How big an issue should be

- **One issue = something one piece of work can close.** It is the unit you can finish by checking every acceptance criterion.
- As a rule of thumb, one session handles one issue.
- Make a large goal an `epic` or `requirement` and list `task`s under it. By default `next` never picks an epic, nor a parent whose children are still open.
- File bugs, separate problems, and open questions **as soon as you find them**. Do not let them live only in chat.
- Types are `requirement` / `design` / `task` / `bug` / `test` / `epic`; priorities run from `P0` (right now) to `P3`.

```bash
looptrack issue new "Add a show/hide toggle to the password field" --type task --priority P2 \
  --labels "ui,login" --traces DEMO-0003 --body "$(cat <<'EOF'
Users often mistype their password, so let them see what they are typing.

## 受け入れ条件
- [ ] Clicking the eye icon shows the password in plain text
- [ ] Clicking it again masks it
- [ ] All existing end-to-end tests pass
EOF
)"
```

The `--body` text goes into the issue's description section. If it contains a `## 受け入れ条件` section, that becomes the acceptance criteria as written.
When you pass a body through the shell, wrap it in a **quoted here-document** (`<<'EOF'`).
Inside double quotes, the shell runs backticks and `$( )`, which corrupts the text.

## Writing acceptance criteria

Acceptance criteria are what the agent checks, one by one, before marking an issue Done.
Write them so they **can be tested**.

| Weak | Better |
| -- | -- |
| Make it faster | The list renders 1,000 rows within 500 ms |
| Make it easier to use | You can save in three clicks or fewer |
| Make it work properly | `make test` passes and there are at least two new tests |

- Put them as a checkbox list under the `## 受け入れ条件` heading in the body.
- Avoid vague words such as "fast" or "easy".
- Anything that needs human eyes is decided by a person through In Review.

## The verify-commands section

If the body has a `## 検証コマンド` section, `verify` runs those commands on your machine and records the result on the issue.

```markdown
## 検証コマンド

- `make lint`
- `make test`
```

A fenced code block works too.

- One command per line. Line continuations (a trailing `\`) and here-documents are not supported; put multi-line logic in a script and call that.
- Inside a code block, lines starting with `#` are skipped. Outside a code block, only list items that are entirely inline code are read.
- Limits: 20 commands, 1,000 characters per command.
- Commands run in order from the git root. A failure does not stop the rest.
- Each command may run for up to 600 seconds and the whole run for 1,800 seconds (`--timeout`, `--total-timeout`).
- **On Windows this needs Git Bash.** Commands run under `bash -c`, and there is no fallback to `cmd.exe` or
  PowerShell, because a POSIX command line would mean something else there. Install Git for Windows
  (`winget install --id Git.Git -e`); `looptrack doctor` says whether it found a shell.

```bash
looptrack issue verify DEMO-0004 --list   # show the commands and the latest record without running
looptrack issue verify DEMO-0004          # run and record (0 all passed, 1 some failed, 2 no section)
looptrack issue verify DEMO-0004 --last   # show the latest record with output
```

**Editing the body invalidates earlier records.**
In a project with `verify.require_on_close`, you must run `verify` again after editing before the issue can be marked Done.

The server finds the sections by their headings. Write them as `## Verify commands` and `## Acceptance criteria` (case does not matter), or in Japanese as `## 検証コマンド` and `## 受け入れ条件`.

## Comment types

Comments are append-only.
They cannot be edited or deleted.
A leading keyword gives a comment its meaning.

| Starts with | Written by | When | Example |
| -- | -- | -- | -- |
| (nothing) | agent or person | as soon as a cause, decision, or plan is known | `Cause: the date conversion ignored the time zone. Fix: store in UTC` |
| `Decision:` (`判断:`) | the agent, recording a person's answer | the answer to an In Review item is an approval or instruction | `Decision: This wording is fine. Mark it Done` |
| `Changes requested:` (`差し戻し:`) | the agent, recording a person's answer | the answer asks for rework | `Changes requested: Put the button top right and match the existing colour` |
| `Feedback:` (`フィードバック:`) | the agent, recording what it was told | a participant or tester reacted | `Feedback: Tester A (18 Sep, while saving) could not find the save button` |

- Put the keyword at the very start, with no leading spaces or line breaks. Case does not matter for the English keywords. The Japanese keywords also accept a full-width colon (`：`).
- On `判断:` move the issue to Done (`close --comment "判断: …"`); on `差し戻し:` move it to Todo (`status <ID> Todo --comment "差し戻し: …"`).
- A `フィードバック:` comment counts as answered once a comment without a keyword, or a status change, follows it.
- **Do not write only the conclusion at the end.** Writing the cause when you find it keeps the history even if the work stops half-way.

```bash
looptrack issue comment DEMO-0004 "Cause: the date conversion ignored the time zone. Fix: store in UTC"
looptrack issue status DEMO-0004 "In Review" --comment "Please decide: two wordings prepared, A or B?"
looptrack issue close DEMO-0004 --comment "判断: A approved. All three acceptance criteria checked"
looptrack issue status DEMO-0004 Todo --comment "差し戻し: Use B, and match the existing button colour"
```

## Assignees

Each issue has one assignee.
This keeps several people, or several agent sessions, from working on the same issue at once.

- Moving an issue to In Progress makes you the assignee if nobody is assigned.
- **An issue assigned to someone else** cannot be moved to In Progress or have its body edited (the server refuses). Ask them in a comment instead.
- To take it over, check with the assignee first and add `--override "reason"`. The reason is recorded.
- `next` only picks issues assigned to you or to nobody.

```bash
looptrack issue assign DEMO-0004 me                          # assign to yourself (- to unassign)
looptrack issue assign DEMO-0004 alice --override "Taking over while Bob is on leave"
looptrack issue new "…" --assignee me                          # assign when filing
```

## Waiting on other issues (blocked_by)

When an issue has to wait for another to finish, put that issue in `blocked_by`.
Until the blocker is Done or Canceled, the issue does not appear in `ready` or `next`.
**Writing "waiting" in a comment is not enough.**

```bash
looptrack issue new "Build the checkout page" --type task --blocked-by DEMO-0010,DEMO-0011
```

## Requirements and traces

`traces` points to the **requirement issue** that this issue fulfils.
Give `design` / `task` / `test` / `bug` issues `--traces <requirement ID>`.

- Only IDs of issues that exist go in `traces`.
- IDs from documents such as specs (e.g. `FR-001`) go in `--refs`. `list --ref FR-001` looks them up in reverse.
- `matrix` prints the requirement → design → implementation → test table, plus warnings.

```bash
looptrack issue new "End-to-end test for the login page" --type test --traces DEMO-0003
looptrack issue matrix
```

When every issue tracing a requirement is closed, the close response says so ("Every child of requirement <ID> is finished").
At that point, check the requirement's acceptance criteria and close the requirement too if they are met.

## Editing an issue body

```bash
looptrack issue edit DEMO-0004    # writes a working copy to .claude/.looptrack-work/DEMO-0004.md
# edit the working copy
looptrack issue push DEMO-0004    # apply it (the working copy is removed)
```

If someone updated the issue in the meantime, `push` stops and shows the difference.
Merge the difference into your working copy and apply it with `push --rebase`.
The body of a closed issue (Done / Canceled) cannot be edited.
To reopen a topic, file a new issue and reference the old one in its body.

## Cleaning up worktrees

If you work on one issue per git worktree, merged worktrees and their branches pile up until
nobody can tell which ones are still alive. `looptrack worktree` collects the facts you need.

```bash
looptrack worktree list          # every worktree: merged?, uncommitted changes?, locked?, when it last moved
looptrack worktree prune         # show what would go. Nothing is deleted
looptrack worktree prune --yes   # actually delete
looptrack worktree mark          # record that this session is using this worktree
looptrack worktree mark --yes    # overwrite it even when another session's marker is there
```

A worktree is removed only if **all** of these hold: it is not the main worktree, it is merged into
the default branch, it has no uncommitted changes, it is not locked, its branch is not published to a
remote, and it has not moved for at least `--min-age` (30 minutes by default — another session may
still be inside it). Anything else is kept, with the reason printed next to it. A worktree whose
branch tracks a remote branch that still exists is treated as a permanent one (the kind you deploy or
cut over from) and is never cleaned up; once the upstream is gone, it is an ordinary temporary branch
again. Build leftovers (`node_modules`, `.DS_Store`, interpreter caches and friends) do not count as
uncommitted changes. A worktree with uncommitted changes
older than a day is reported as **about to be lost**, so that you move the work somewhere safe
instead of forgetting it. Branches left behind by a removed worktree are listed too.

`mark` never overwrites another session's marker. If one is already there it **fails with exit
code 1** and prints `Another session's marker exists (<session>, <time>). Not overwriting (pass --yes
to overwrite).` Take that as "somebody else is inside this worktree": ask them first, and only then
take it over with `looptrack worktree mark --yes`. A marker that cannot be read or parsed is treated
the same way (`Could not read the existing marker ...`), because there is no telling whose it is.
Marking again with your own session is not a failure, and neither is the main worktree: it prints
`This is the main worktree, so no marker is written (several sessions use it at the same time).` and
exits 0.

With the loop layer installed, a session-start hook tells you when there is something to clean up or
something about to be lost, and says nothing otherwise. It never deletes anything. Turn it off with
`LOOPTRACK_LOOP_WORKTREE_NOTICE=0`.

## Things not to do

- Starting work without an issue
- Marking an issue Done without checking the acceptance criteria
- Getting around a rule rejection by another path (MCP or edit)
- Writing access tokens into chat, commits, issues, or logs
- Pasting production data into issues (mask it or write only IDs)
