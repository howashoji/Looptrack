---
name: issue
description: Create, start (next), comment on, close and edit issues on the issue management server. Use it when work begins, when a problem turns up, and when a status changes
---
<!-- Created by looptrack:init (once you edit it by hand, looptrack issue init leaves it alone) -->

# Issue skill (issue management server)

The CLI is `looptrack issue` — API mode: `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT` in the `env` of
`.claude/settings.json` decide the server and the project. **There are no issue files on disk.**

**Read `looptrack issue guide` first** — it returns the shared rules, this project's rules and the operations documents in one call (the `guide` tool over MCP).
**Everything the project decides for itself — the types, the labels, how to use `--refs`, the per-project rules — lives in the guide**, not in this skill.

## When to use it

- When you are handed a piece of work (**work starts from an issue**; never carry it in conversation alone)
- When you find another problem, an open question or a defect while investigating (**file it on the spot; do not put it off**)
- When the state of the work changes (started / finished)
- When you decide what to do next

## 0. Check the connection (when an error appears)

```bash
looptrack issue config   # mode, server, project, your own role, where the token comes from, the installed kit
```

When you see `No access token` / `Invalid token` / `The token was revoked or has expired` / `Your login has expired`,
get the user's approval and run `looptrack issue login --browser --url <server URL>`; **the user** logs in and approves
in the browser that opens (it listens for at most 5 minutes; the token never appears in the conversation, and the AI never handles it).
Where there is no browser, ask the user to issue a token on the server (`/account`) and paste it into `login --url <server URL>`.
When `config` ends in an error instead of naming the server (an error saying `LOOPTRACK_API_URL` is not set), the `env` did not reach the process.
The `env` block in `.claude/settings.json` reaches Claude Code sessions only, so when you run it in your own terminal, prefix
`LOOPTRACK_API_URL=… LOOPTRACK_PROJECT=…`.

## 1. One turn of the loop

```bash
looptrack issue next              # start work (your own in-progress issue, or the highest startable one, moved to In Progress). --dry-run only looks
looptrack issue comment <ID> "$(cat <<'EOF'
The cause, the judgement, the plan — as soon as you know them
EOF
)"
looptrack issue close <ID> --comment "How you verified the acceptance criteria"
looptrack issue next              # the next turn
```

- **Comment as soon as you know the cause or the judgement** (do not save the conclusion up for later). Comments are append-only
- **Leave the result of verifying the acceptance criteria in a comment before you close.** Never close what you have not verified
- To change only the status, use `status <ID> "<status>" --comment "…"`

### Waiting for a human decision

- Anything that needs the user's judgement — how to read a spec, how something looks, a choice of direction, something you want them to check — does not go to Done;
  put it in `status <ID> "In Review" --comment "What I need you to decide: …"` instead
- When anything is In Review, show it to the user **before the next `next`** (what you built, how you verified it, what you need decided).
  Record their answer as a comment that starts with `Decision:` (approval or instruction) or `Changes requested:` (redo, with the direction); on a `Decision:` run
  `close <ID> --comment "Decision: …"`, and on a `Changes requested:` run `status <ID> Todo --comment "Changes requested: …"`
- When there is no user, or no answer, leave it In Review and go on to the next `next` (do not chase it). List them with `list --status "In Review"`

### Reactions from outside

- When the user passes on a reaction from a participant, a tester or a user of the product (an impression, a defect report, a request), comment on the issue it belongs to
  starting with `Feedback:` (`Feedback: <from whom, when, in what situation> <what they said>`; file an issue first if none fits)
- Show unanswered feedback to the user before the next `next`, decide what to do about it (a comment on the direction, a new issue, a status change),
  and record the answer in a comment with no lead word

## 2. File an issue

```bash
looptrack issue new "Title" --type task --traces <requirement ID> --body "$(cat <<'EOF'
What you will do and why (this goes into the "## 内容" — Details — section)

## Acceptance criteria

- [ ] Write them so they can be tested
EOF
)"
```

- `--type`: requirement / design / task / bug / test / epic. **design / task / test / bug take `--traces <requirement issue ID>`**
- `--traces` takes issue IDs only. Document IDs and label conventions differ per project (see the guide)
- When something has to happen first, add `--blocked-by <ID>` (writing it in a comment does not feed the "ready to start" check)
- When you pass a body or a comment through the shell, wrap it in a quoted here-document (`<<'EOF'`)

## 3. Edit the body

```bash
looptrack issue edit <ID>   # → edit .claude/.looptrack-work/<ID>.md (a working copy carrying the version) with Read / Edit
looptrack issue push <ID>   # send it back. On a conflict, take in the difference and push --rebase
```

The comments section cannot be edited (append with `comment`). The body of a closed issue cannot be changed (to reopen a question, file a new issue and reference it).

## 4. Search and see the whole

```bash
looptrack issue summary                 # the current turn, what waits on a human decision, reactions from outside (the same as the session-start hook)
looptrack issue ready                   # everything whose blocked_by is resolved
looptrack issue list --status "In Progress"
looptrack issue list --type bug         # "none" = no open bugs
looptrack issue show <ID>
looptrack issue matrix                  # requirements → design / implementation / test, with warnings
```

The web view is the server's `/p/<project>/` (login required, **read only**). Always make status changes and comments through `looptrack issue`.

## When the server refuses

Numbering, append-only comments, the immutability of closed issues and the per-project rules are enforced by the server. When you are refused, **do what the message's "command to run next" says**.
Use `--override "<reason>"` only when you have a sound reason to lift a rule (an explicit instruction from the user, for instance); the reason is recorded on the server.

## What you must not do

- **Start work without an issue** (nothing is left of how it went, and the user cannot follow it afterwards)
- **Write "I will update it" or "I will file it" and then not do it in that same turn** (leaving an issue you referred to un-updated sends the turn back through the freshness guard)
- **Write a dependency in a comment only** (unless it is in `blocked_by`, the "ready to start" check cannot see it)
- **Move an issue to Done without verifying the acceptance criteria**
- **Edit a closed issue** (the server refuses it; to reopen a question, file a new issue and reference it)
- **Route around an operation the server refused** (rewriting the status through MCP or `edit`, for instance)
- **Write an access token into the conversation, a commit or an issue**
- **Write what belongs to the handoff record (memory, handoff) into an issue** (they have different jobs)
