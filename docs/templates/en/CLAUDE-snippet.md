<!-- The section that goes into a project's CLAUDE.md when init runs in English (<slug> is the project name, <URL> the server URL).
     looptrack issue init wraps it in <!-- looptrack:begin … --> / <!-- looptrack:end -->. The source is claudeSnippetEN in internal/client/kitinit/texts.go.
     TestClaudeSnippetDoc in internal/client/kitinit checks that this copy matches (-update rewrites it). Do not paste it by hand. -->

## Issue tracking (issue server)

**Start every piece of work by filing an issue, not in conversation alone. File a bug as soon as you find one.**

This section is for Claude Code. GitHub Copilot and Codex follow the "Issue tracking" section in AGENTS.md instead; if AGENTS.md has none, they start from the `setup` tool of the looptrack MCP server.
The note is here because GitHub Copilot's CLI and VS Code read CLAUDE.md too.

- Operation: `looptrack issue` (API mode, with `LOOPTRACK_API_URL` / `LOOPTRACK_PROJECT=<slug>` in the `env` of `.claude/settings.json`). The skill is `/issue`
- **Read how to use it and the rules with `looptrack issue guide`**. It returns the common rules, this project's rules and the operating documents in one go. Over MCP, use the `guide` tool
- One round of the loop: `looptrack issue next` (start) → work → `looptrack issue comment <ID> "…"` (record what you learn as you learn it) → verify the acceptance criteria → `looptrack issue close <ID> --comment "verification results"` → the next `next`
- Browse: <URL>/p/<slug>/ (login required, read-only)

The authoritative copy of each issue lives on the server (in its database), and there are no local files. Handle them as follows.

- **The first time only**, run `looptrack issue login --browser --url <URL>`. The user logs in and approves in the browser that opens; the token never shows up in the conversation and refreshes itself from then on. Without a browser, issue a token at <URL>/account and paste it into `login --url <URL>`
- Editing a body: `looptrack issue edit <ID>` → Read / Edit `.claude/.looptrack-work/<ID>.md` → `looptrack issue push <ID>`
- Issues need no git pull / commit / push
- The server enforces numbering, the immutability of closed issues and the per-project rules. When it rejects something, follow the instructions in the message
- `--traces` is for issue IDs only, and `--refs` for document IDs (`FR-`/`NFR-`/`UC-`/`ISS-`/`DEC-`) only
- To pass a body or a comment through the shell, wrap it in a quoted heredoc (`<<'EOF'`)
