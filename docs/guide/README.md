# Looptrack User Guide

Looptrack lets coding agents and people run their work around issues.
This guide is for **people who use it**.
Server design and deployment details live in separate documents for developers and operators.

日本語: [ja/README.md](ja/README.md)

## Reading order

| # | Section | What it covers |
| -- | -- | -- |
| 1 | [Concepts](concepts.md) | What loop engineering is, and why the issue is the source of truth |
| 2 | [Where Looptrack fits](where-it-fits.md) | Which feature serves which of the three loops |
| 3 | [Getting started](getting-started.md) | From installing the server to your first full loop |
| 4 | [Daily use](daily-use.md) | Filing issues, acceptance criteria, verify commands, comment types, assignees |
| 5 | [Agent-specific notes](ai-agents.md) | Claude Code, Codex, GitHub Copilot, and other agents |
| 6 | [Administration](admin.md) | Users, permissions, two-factor auth, project rules, token reports |
| 7 | [FAQ / Troubleshooting](faq.md) | Common messages and what to do about them |
| 8 | [Desktop app](desktop.md) | Using it alone on one PC without a terminal: download, first launch, tray, update, uninstall |

If you are new, read 1 → 3 → 4.
To use it by yourself on one PC without a terminal, start with 8.

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

These documents are currently written in Japanese.
