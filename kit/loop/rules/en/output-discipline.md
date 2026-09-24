# Output discipline (tool-call format, saying it and doing it)

> A rule from kit/loop. Follow it every session. Three layers of hooks keep it present and catch breaches:
> SessionStart `session-start-rules.sh` (injects "Points for every session" below), UserPromptSubmit `user-prompt-rules.sh`
> (injects "Points for every turn"), and Stop `stop-tool-markup-guard.sh` (sends back a reply that still holds raw markup).
> The Stop hook that detected say-do mismatches by vocabulary has been retired (it misfired too often, and a wrong send-back skipped the user's decision in at least one case).
> Keep saying-and-doing as a rule below; missed issue updates are caught by core's freshness guard, from the state itself.

## Points for every session
<!-- looptrack:inject session claude-code -->

Tool-call format (follow it strictly, every session):
1. Put the tool call **at the very start of the message**, on its own. Never write prose before a tool call (the prose → tool-call transition is what triggers the loss of the antml: namespace).
2. Push explanations and preambles **after** the tool call, or into a separate turn. Put several independent calls into one message, at the start.
3. When you get a malformed / unparsed warning, resend **the tool call alone, with no preamble**, immediately (writing an explanation or an apology first is what triggers a repeat).
4. A tool call opens with `antml:invoke` / `antml:parameter`. Bare invoke / parameter, or a stray count / court at the front, gets it rejected as a no-op (it never runs).
5. If nothing is blocking you, never close a turn with a declaration of what you will do. Take the next action itself (the tool call).

## Points for every turn
<!-- looptrack:inject prompt claude-code -->

[Output discipline] Put the tool call at the start of the message. Never write prose before a tool call (the prose → tool-call transition is what triggers the broken format). When you get a malformed / unparsed warning, resend the tool call alone, with no preamble, immediately. If nothing is blocking you, do not close with a declaration — take the next action.

## 1. Tool-call format (most important)

**Root cause (demonstrated)**: a broken tool-call format (the leading `antml:` namespace is lost → the harness rejects the call as malformed → the work stops on a no-op) happens,
without exception, at the transition "**prose written, then a tool call**". A message begun directly with a tool call succeeds every time.
The decoder cannot be changed, so **eliminate the triggering condition (prose-before-tool) by discipline**.

### Rules to follow without exception
1. **Put the tool call at the very start of the message, on its own.** Never write prose before a tool call.
2. Push explanations and preambles **after** the tool call, or into a separate turn. Put several independent calls into one message, at the start.
3. **When you get a malformed / unparsed warning, resend the tool call alone, with no preamble, immediately** (writing an explanation or an apology first is what triggers a repeat).
4. A tool call opens with `antml:invoke` / `antml:parameter`. A bare `invoke` / `parameter` tag, or a stray `count` / `court` at the front,
   gets it rejected as a no-op (it never runs. Nothing is corrupted, but the work stops).

### Three layers of enforcement
- **SessionStart** `session-start-rules.sh`: injects "Points for every session" from this file at the start of the session (present from before the first tool call).
- **UserPromptSubmit** `user-prompt-rules.sh`: injects "Points for every turn" on every turn.
- **Stop** `stop-tool-markup-guard.sh`: if unparsed markup is left in the text of the reply, it sends the Stop back and prompts an immediate resend in the correct format.

## 2. Saying it and doing it

- **A declaration of what you are about to do** — "I'll continue", "I'll go ahead", "I'll get started" — **comes with a tool call in the same turn**.
  Unless it is a question you are asking or a blocker you are reporting, line the declaration up with the tool call.
- Never close a turn still mid-work (in_progress) without a tool call.
- **If nothing is blocking you, never close a turn with a declaration. Take the next action itself.**
  Close with "Next I'll merge X and move on to verification." and, even where the blocker (waiting on CI, say) has already cleared,
  you stay stopped for hours until the user speaks to you again (what actually happened: CI was already green, and it sat there for about 14 hours).
  A turn that is only a declaration looks to the user like motion, so **the harm is not the slowness itself but looking busy while they wait**.
- **Waiting on a dependency does not count as blocking**: a sentence that really is waiting for something external to finish — "once …", "as soon as …", "after it completes" — is not sent back.
  In that case, write what you are waiting on and who you are waiting for, and close the turn (if you are waiting on the user, write it as a question).
  False positives lead to the guard being bypassed as a matter of course, which hollows it out.
- When you are waiting on a decision from the user (a fork), present it with a question tool (Claude Code's `AskUserQuestion`, for example) — then the declaration and the action line up.
