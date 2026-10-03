# Looptrack User Guide

Looptrack is an issue tracker that works as external memory for AI coding agents. It's how you actually run loop engineering.
It records the work items an AI plans, kept apart from your project's own issues. You can see the plans and decisions behind them, and since it logs the tokens each task consumed, you can report on those too.
Coding agents and people work around the same issues. That's the core idea.

This guide is for people who use it.
Server design and deployment details? Those live in separate documents for developers and operators.

日本語: [ja/README.md](ja/README.md)

## Choosing an edition

Looptrack comes in two editions. Installing it and connecting your AI agent differ between them, and everything after that is shared.

| Edition | Good for | Start here |
| -- | -- | -- |
| Desktop app | Using it alone on one PC, without a terminal. Double-click to start it, finish setup in the browser, and connect your agent by chat | [Desktop app](desktop/README.md) |
| Server | A server several people share, or a server you run for yourself from a terminal. Set it up with `looptrack setup` or `install.sh` | [Server edition](server/README.md) |

On your own, on one PC, with no terminal? Take the desktop app.

## Shared sections

These sections apply to both editions.

| # | Section | What it covers |
| -- | -- | -- |
| 1 | [Concepts](concepts.md) | What loop engineering is, and why the issue is the source of truth |
| 2 | [Where Looptrack fits](where-it-fits.md) | Which feature serves which of the three loops |
| 3 | [Daily use](daily-use.md) | Your first loop, filing issues, acceptance criteria, verify commands, comment types, assignees, the display language |
| 4 | [Agent-specific notes](ai-agents.md) | Claude Code, Codex, GitHub Copilot, and other agents |
| 5 | [FAQ / Troubleshooting](faq.md) | Common messages and what to do about them |
| 6 | [Token reports](token-report.md) | What token usage is recorded and what is not, how it is attributed to issues, and how to make the PDF report and record it in the ledger |

New here? Read 1, then the getting-started page of your edition, then 3.

## Terms

| Term | Meaning |
| -- | -- |
| Server | `looptrack serve`, which stores the issues. It serves a web UI, a REST API, and MCP |
| CLI | `looptrack`, which runs on your machine. `looptrack issue …` works with issues. The server and the CLI are the same binary |
| Project | A container for issues. It has a slug (e.g. `demo`) and an ID prefix (e.g. `DEMO` → `DEMO-0001`) |
| MCP | The connection an agent uses to call the server as tools. Agents work through either the CLI or MCP |
| Hook | A small program the agent runs at fixed points (session start, after a tool call, on stop) |
| Kit | The hooks, rule texts, and skills installed into each project. `core` is always installed; `loop` is optional |

## Documents for developers and operators

- [README](../../README.md): the repository as a whole
- [docs/server/DEPLOY.md](../server/DEPLOY.md): deployment and the details of `looptrack setup`
- [docs/server/DESIGN.md](../server/DESIGN.md): design

For now, they're written in Japanese only.
