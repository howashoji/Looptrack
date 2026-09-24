### What this system puts in place (the base of loop engineering)

An AI runs "file → start (next) → work → verify → close → next" around issues, the server enforces the rules (ID numbering, append-only, closed issues frozen, per-project rules), and the token usage of each stage is recorded. In terms of the three nested loops:

- **(1) The AI's working loop (minutes)**: the AI runs the round above by itself. Write a "## Verify commands" section into an issue and `verify` runs it on your machine, leaves the result on the issue, and a machine decides before it is closed (on some projects it cannot be closed unless they all succeeded).
- **(2) The human decision loop (tens of minutes to hours)**: whatever needs a human decision collects in In Review, and `summary` shows how long each has been sitting (marked past 48 hours). The AI puts it to the user before the next `next` (prompt `review`), records the answer with `Decision:` / `Changes requested:` and moves the issue on. People only decide.
- **(3) The outside feedback loop (hours to weeks)**: reactions heard from users and testers are brought back onto the issue by the AI as a comment led by `Feedback:`, and the unanswered ones line up under "outside feedback" in `summary`. They disappear once you answer (a comment with the way forward, a new issue, a status change).

How it is installed changes how much of this you get:

- **core only (the minimal loop)**: a three-layer summary (the current round, what is waiting on a human, outside feedback) is injected at the start of the session, `next` starts an issue, ending your work without updating an issue you looked at sends you back, and usage piles up per stage.
- **with loop added**: a request to only look into something stops edits, a broken tool-call format is sent back, the handoff memory has to be updated whenever a unit of work finishes, the size of the context and abandoned background processes are watched, and a change to another repository or project is checked with the user. Detecting deviations is left to the hooks, so people can concentrate on deciding. Whether to add loop is the user's call (an AI never adds it on its own).

### What you can assume

The server (its database) holds the issues, and it is the source of truth. There are no files on your machine and no git operations are needed. You work through the CLI (`looptrack issue <subcommand>`) or the MCP tools; both are validated by the same rules. The CLI runs from the single `looptrack` binary (no separate runtime is needed).

### How to work (one round of the loop)

1. **Start**: `looptrack issue next` (`next` over MCP). If you have an issue in progress it returns that one; otherwise it moves the highest-ranked issue that is ready to start to In Progress and returns its body, acceptance criteria and related issues. A container such as an epic that still has open children is not counted as in progress even when it is In Progress; pick from its children instead. `--type` (`types` over MCP) also applies to which in-progress issue is picked. To check without changing any status, use `--dry-run` (`dry_run` over MCP).
2. **Work**: comment the moment you know the cause, the decision or the way forward: `looptrack issue comment <ID> "…"` (`add_comment` over MCP). Do not write only the conclusion afterwards.
3. **Verify**: check the acceptance criteria one by one.
4. **Finish**: `looptrack issue close <ID> --comment "the result of verifying the acceptance criteria"` (over MCP, `set_status` to Done with `comment`). Never set Done without leaving the verification result.
   - **Verifying and closing the requirement**: when closing this issue also closes every child of a requirement it points at through traces (the issues that trace to that requirement), the close response ends with one line per requirement urging you to verify and close it (that line holds `looptrack issue show <requirement-ID>` and the close command). **Decide from that command, not from the wording** (the wording changes with the user's language). Do not go straight on to the next `next`: check the requirement's acceptance criteria one by one (`looptrack issue show <requirement-ID>`; `get_issue` over MCP) and, if they are met, close it with `looptrack issue close <requirement-ID> --comment "the result of verifying the acceptance criteria"`. If they are not met, file what is missing (`--traces <requirement-ID>`) and say so in a comment. If it needs the user's decision, set it to In Review.
   - What slips through lines up under "requirements still open although every child is finished" in `looptrack issue matrix` and under (1) in `summary`. A requirement whose children are all Canceled is not counted (they were only decided against, so settle with the user whether to withdraw the requirement (Canceled) or file its children again).

**Waiting on a human**: anything in your own work that needs the user to decide (how to read a specification, how something looks, a choice of direction, or something that meets the acceptance criteria but that you want checked) does not go to Done but to `looptrack issue status <ID> "In Review" --comment "What I need decided: …"`.
If there are issues In Review, put them to the user **before the next `next`**: what you built, the verification result (the verify record and the result per acceptance criterion), and what you need decided.
Record the user's answer as a comment led by **`Decision:`** (approval or instructions) or **`Changes requested:`** (do it again; write the direction too). For `Decision:` close it (`close --comment "Decision: …"`); for `Changes requested:` put it back to Todo (`status <ID> Todo --comment "Changes requested: …"`).
If there is no user, or no answer, leave it In Review and go on to the next `next` (do not chase it; the wait shows up under "waiting on a human" in `summary`).

**Outside feedback**: when you hear a reaction from a participant, a tester or the user in conversation (how it felt to use, a bug report, a request), comment it on the issue it belongs to, led by **`Feedback:`** (`Feedback: <from whom, when, in what situation> <what they said>`). If there is no matching issue, file one first and then comment.
If "outside feedback" in `summary` has unanswered items, put them to the user before the next `next`, settle what to do (a comment with the way forward, a new issue, a status change) and leave the answer as a comment with no lead word.

5. On to the next round (back to 1). If nothing is ready to start, look at the whole picture with `looptrack issue ready` / `summary` and talk it over with the user.

### Filing and editing

- **Work starts from an issue. Never carry it in the conversation alone.** File a bug, another problem or an open question the moment you find it (`looptrack issue new "title" --type bug …`; `create_issue` over MCP).
- Types: requirement / design / task / bug / test / epic. Statuses: Backlog / Todo / In Progress / In Review / Done / Canceled. Priorities: P0 to P3.
- Put what you are waiting on in `--blocked-by <ID>` (writing it in a comment does not make the "ready to start" check see it).
- `--traces` takes **existing issue IDs only** (design / test / task / bug point at the requirement issue). Document IDs (FR- / NFR- / UC- / ISS- / DEC-) go in `--refs`.
  blocked_by / traces / refs hold one ID per element (`"A B"` separated by a space inside an MCP or API array is rejected; the CLI accepts either commas or spaces).
- Write acceptance criteria **so that they can be tested** (no vague words such as "fast" or "easy to use").
- Editing the body: `looptrack issue edit <ID>` → edit `.claude/.looptrack-work/<ID>.md` → `looptrack issue push <ID>` (over MCP, take the version and the full text from `get_issue` and send them to `update_issue`). On a conflict, take the changes in as the message tells you and try again.
- When you pass a body or a comment through a shell, wrap it in a quoted heredoc (`"$(cat <<'EOF' … EOF)"`) (backquotes and `$( )` are executed otherwise and wreck the text).

### Assignees (so the same work is not done twice)

**Assignee**: never set an issue assigned to another user to In Progress, and never edit its body or its fields (the server rejects it with 422 `assigned_to_other`). Ask for things and check things in a comment. When you take it over, or edit it on the assignee's behalf, check with the user first and add `--override "reason"` (`override_reason` over MCP). The override on In Progress and `assign <ID> me --override "reason"` (`assign_issue` over MCP) move the assignment to you (a handover). The override on a body edit (`push` / `update_issue`) goes through without moving the assignment, and the record keeps who edited it with what reason. `next` only picks issues that are assigned to you or to nobody. Status changes other than to In Progress, and comments, work whoever the assignee is.

### What cannot be changed, and what not to do

- Comments are append-only. The body and the fields of a closed issue (Done / Canceled) cannot be edited (to reopen the subject, file a new issue and refer to it in the body).
- When a per-project rule rejects something, **do what the message tells you to do next**. Do not get around it through another path (MCP, edit and so on). Override (`--override "reason"`) only on the user's explicit instruction.
- Never write an access token into the conversation, a commit, an issue or a log. Log in with `looptrack issue login --browser` (the AI runs it with the user's approval, and the user themselves logs in and approves in the browser that opens; the token never appears in the conversation, and it is refreshed automatically from then on). On a machine with no browser, the user issues the token and pastes it into `login --url` themselves.
- Never paste production data into an issue body (mask it, or write only the ID).

### When you get stuck

- `looptrack issue config` tells you the mode, the project, your permissions and where the token comes from. If it stops with an error saying there is no server URL, the settings environment variables (`LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT`) are not in that shell.
- If you end your work without updating an issue you looked at, the freshness guard (a Stop hook) stops you. Leave what you found out and what you did in a comment.
- Token usage is sent automatically by the CLI's write operations and by the hooks. **The marker is the command, not the wording**: when `looptrack issue usage attach <ID>` is shown in the result of a write operation (filing, commenting, a status change, reporting a verify), on standard error, or at the end of `summary`, run that command as it is for each ID shown (the server's wording changes with the user's language, so do not look for a particular phrase). On some projects, setting an issue to Done / Canceled is rejected unless this conversation's token usage is on it (`usage.require_on_close`; the CLI attaches it and retries automatically).
- Commands for token measurement: `looptrack issue usage show <ID>` (per stage of an issue; `issue_usage` over MCP), `usage attach <ID>` (attach it by hand), `usage missing` (your AI operations with nothing attached, and how complete it is; `usage_missing` over MCP), `usage report --since-last` / `--from D --to D` (the total for a period; `usage_report` over MCP), `usage ledger list` / `usage ledger add <name> --from-report r.json` (the ledger of reports; a registration cannot be taken back; `list_usage_ledger` / `add_usage_ledger` over MCP), `usage requests` (report requests filed from the web UI; `list_usage_requests` over MCP).
- When `summary` (`project_summary` over MCP) shows a "request for a token report", or you are asked to make a token report, follow the skill `token-report` (`.claude/skills/token-report`) all the way through: aggregate → body → PDF → register it in the ledger (the period of the request is `usage report --request N`, or `request_id` on `usage_report` over MCP).
- When an MCP tool result carries a note that the installation is incomplete (the hook / CLI installed-notification has not arrived) or that the distributed files are out of date (it comes on a line of its own, separate from the tool's result, and names the `setup` tool or an update command), call the MCP `setup` tool and run the steps it returns with the user's approval (installing the hooks, the CLI and the guide text; registering the token, restarting the AI and approving the hooks are done by the user). You can check the state on your machine with `looptrack issue installed --agent <claude-code|codex|copilot|other>`.
