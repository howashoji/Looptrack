# Concepts: loop engineering

[Guide contents](README.md) · Next: [Where Looptrack fits](where-it-fits.md)

## What loop engineering is

When you hand work to a coding agent, very little of it is finished in a single instruction.
The agent builds and checks, a person decides, users react, and the work goes around again.
Loop engineering means designing that cycle on purpose instead of leaving it to chance.

Looptrack treats the cycle as **three nested loops**.
The inner loops turn fast; the outer ones turn slowly.

| Loop | Typical period | Who drives it | One turn |
| -- | -- | -- | -- |
| ① Agent loop | minutes | the agent | next (pick up the next issue) → work → comment → verify → Done → next |
| ② Human loop | tens of minutes to hours | a person | A person decides on what the agent put In Review: approve or send back. Feedback from people also enters here |
| ③ Outer loop | hours to weeks | participants and testers | Reactions from real users go back into issues and shape the next work |

## ① The agent loop

The agent takes the highest-priority issue that is ready to start.
That issue comes with its body, acceptance criteria, and links.
The agent does the work and writes a comment as soon as it finds a cause or makes a decision.
It checks each acceptance criterion, records the results in a comment, and only then marks the issue Done.
Then it takes the next issue.

A person does not have to be part of every turn of this loop.

## ② The human loop

Some things an agent must not decide alone: how to read a spec, how something should look, which approach to take.
The agent does not mark these Done; it moves them to In Review instead.
A person reviews the In Review queue whenever it suits them.
To approve, they leave a comment starting with `Decision:` (`判断:`); to ask for rework, one starting with `Changes requested:` (`差し戻し:`).
Before its next turn, the agent picks these up and moves the issue to Done or back to Todo.

## ③ The outer loop

You can only judge what you built once it reaches the people who use it.
When you hear a reaction from a participant, tester, or user, put it on the issue as a comment starting with `Feedback:` (`フィードバック:`).
Reactions nobody has answered yet are listed as "unanswered" in the summary.
One response (a comment on the plan, a new issue, or a status change) removes it from the list.

These three prefixes are fixed keywords that the server recognises. The English and Japanese forms mean the same; use either.

## Why the issue is the source of truth

An agent's context disappears when the session ends.
Even within a long session, details are lost when the context is summarised.
The agent in the next session does not remember what was decided before.

So Looptrack keeps two things in the issue:

- **What to do next**: status, priority, what it waits for (blocked_by), acceptance criteria
- **Why it was done that way**: causes, decisions, reasons for sending back, verification results (comments)

Each issue exists exactly once, in the server's database.
Every agent and every session reads the same issue.
Comments are append-only; they cannot be edited or deleted.
Once an issue is closed, its body cannot change.
That is what lets you trace the history later.

If work moves forward only in chat or private notes, neither of those reaches the next session.
Looptrack's basic rule is simple: **start work from an issue**.

## The server enforces the rules

The server checks numbering, append-only comments, the immutability of closed issues, and per-project rules.
The same checks apply whether you work through the CLI or through MCP.
When something is rejected, the error tells you what to do next.
Agents follow that message to continue.
