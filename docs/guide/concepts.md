# Concepts: loop engineering

[Guide contents](README.md) · Next: [Where Looptrack fits](where-it-fits.md)

## What loop engineering is

Hand work to a coding agent, and very little of it gets finished in one instruction.
The agent builds and checks. A person decides. Users react, and the work goes around again.
Loop engineering means designing that cycle on purpose, not leaving it to chance.

Looptrack treats the cycle as three nested loops.
Inner loops turn fast. Outer ones turn slowly.

| Loop | Typical period | Who drives it | One turn |
| -- | -- | -- | -- |
| ① Agent loop | minutes | the agent | next (pick up the next issue) → work → comment → verify → Done → next |
| ② Human loop | tens of minutes to hours | a person | A person decides on what the agent put In Review: approve or send back. Feedback from people also enters here |
| ③ Outer loop | hours to weeks | participants and testers | Reactions from real users go back into issues and shape the next work |

## ① The agent loop

The agent takes the highest-priority issue that's ready to start.
It comes with its body, acceptance criteria, and links.
As the agent works, it writes a comment the moment it finds a cause or makes a decision.
It checks each acceptance criterion and records the results in a comment. Only then does it mark the issue Done.
Then on to the next one.

A person doesn't have to be part of every turn.

## ② The human loop

Some things an agent must not decide alone. How to read a spec, how something should look, which approach to take.
So the agent doesn't mark these Done. It moves them to In Review instead.
A person goes through the In Review queue whenever it suits them.
To approve, leave a comment starting with `Decision:` (`判断:`). To ask for rework, start it with `Changes requested:` (`差し戻し:`). That's all.
Before its next turn, the agent picks these up and moves the issue to Done or back to Todo.

## ③ The outer loop

You can't judge what you built until it reaches the people who use it.
Heard a reaction from a participant, tester, or user? Put it on the issue as a comment starting with `Feedback:` (`フィードバック:`).
Reactions nobody has answered yet show up as "unanswered" in the summary.
Answer once (a comment on the plan, a new issue, or a status change) and it drops off the list.

These three prefixes are fixed keywords the server recognises. The English and Japanese forms mean the same thing, so use either.

## Why the issue is the source of truth

An agent's context disappears when the session ends.
Even in a long session, details get lost when the context is summarised.
The agent in the next session doesn't remember what was decided before.

So Looptrack keeps two things in the issue:

- What to do next: status, priority, what it waits for (blocked_by), acceptance criteria
- Why it was done that way: causes, decisions, reasons for sending back, verification results (comments)

Each issue exists exactly once, in the server's database. Every agent and every session reads that same issue.
Comments are append-only. Nobody can edit or delete them, and once an issue is closed, its body can't change either.
That's what lets you trace the history later.

If work moves forward only in chat or private notes, neither of those reaches the next session.
Looptrack's basic rule is simple: start work from an issue.

Put together, the issues are the agent's external memory. Whatever doesn't fit in its context window stays here, and the next session starts from it.
They aren't meant to replace your project's own issue list, though.
Feature requests and bug reports people discuss can stay where you keep them. What goes here are the work items an AI breaks that work into.
Each one is small enough to take one at a time, and carries its plan, its decisions and the tokens its work consumed ([Token reports](token-report.md)).

## The server enforces the rules

The server checks numbering, append-only comments, closed issues staying unchanged, and per-project rules.
CLI or MCP, the checks are the same.
When something is rejected, the error tells you what to do next.
Agents just follow that message and keep going.
