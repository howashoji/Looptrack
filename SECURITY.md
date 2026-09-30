# Security Policy

Looptrack stores the issue history of real projects. It also holds credentials
for the people and the AI agents that work on them. So we take reports
seriously, and we'd much rather hear about a problem early than late.

## Supported versions

| Version | Supported |
| -- | -- |
| 1.x (latest release) | Yes |
| Anything older than the latest release | No — please upgrade first |

Fixes ship in a new patch release of the latest minor version. We don't
backport security fixes to older minor versions.

## Reporting a vulnerability

**Please do not open a public issue, pull request, or discussion for a security
problem.** A public report hands a working exploit to everyone who runs the
software.

Report it privately through GitHub instead:

1. Go to the repository's **Security** tab.
2. Choose **Report a vulnerability** (GitHub's Private vulnerability reporting).
3. Fill in the form and submit it. Only you and the maintainers can see the
   report and the conversation that follows.

Can't use Private vulnerability reporting for some reason? Then open a public
issue that says only *"I would like to report a security issue privately"*,
with no details at all. A maintainer will open a private advisory and invite
you to it.

### What to include

The more of this you can give us, the faster we can confirm and fix it:

- The version (`looptrack version`) and how it's deployed (desktop edition,
  `install.sh` on a server, Docker Compose, built from source).
- The database (MySQL or SQLite), whether the server runs behind a reverse
  proxy, and the URL prefix if you changed it.
- Which surface is affected: REST API, MCP, Web UI, CLI, hooks, the installer,
  or the release artifacts.
- Step-by-step reproduction, and what an attacker gains. A minimal proof of
  concept is very welcome.
- Whether the issue is already public anywhere.

Test only against your own installation, please. Don't access, modify, or
exfiltrate other people's data. And don't run denial-of-service or spam tests
against someone else's server.

## What to expect

| Stage | Target |
| -- | -- |
| Acknowledgement of your report | within 3 business days |
| First assessment (confirmed / not a vulnerability / need more information) | within 10 business days |
| Fix released for a confirmed high-severity issue | within 30 days of confirmation |
| Fix released for other confirmed issues | with the next regular release |

If we miss one of these, we'll say so in the advisory thread. We won't go
quiet. If we decide something isn't a vulnerability, we'll explain why.

## Disclosure

We use coordinated disclosure:

1. We confirm the report privately and agree on a fix.
2. We prepare the fix and a GitHub Security Advisory (with a CVE where it is
   warranted) in a private fork.
3. We publish the release and the advisory at the same time. The advisory
   describes the impact, the affected versions, the fixed version, and any
   mitigation for people who cannot upgrade immediately.
4. We credit you in the advisory under whatever name you choose, unless you ask
   to stay anonymous.

We normally publish within 90 days of confirming a report, even if the fix
isn't complete. That way operators can mitigate. Planning to publish earlier?
Tell us, so we can coordinate.

There is no bug bounty for this project.

## Things that are not vulnerabilities in this project

These come up often. They work as designed, so we handle them as normal issues,
not as security reports:

- **Local mode has no sign-in.** In local mode Looptrack binds to 127.0.0.1
  only and treats everything that reaches it as the single local user. Exposing
  that port to a network is a deployment mistake, not a product bug. But a way
  to reach local mode *from a web page in the user's browser* is a
  vulnerability. Please do report that one.
- **The server doesn't terminate TLS.** It's meant to sit behind a reverse
  proxy that does. Plain HTTP on the loopback port is expected behaviour.
- **Anything a project `editor` can legitimately do:** editing issue bodies,
  adding comments, changing status. The privilege boundaries that matter are
  viewer vs. editor vs. admin, and crossing between projects.
- **Unsigned Windows binaries.** Known, and documented in the README. We plan
  to address it with an OSS code-signing programme.

The security model the server is built on (authentication, per-project
permissions, append-only history, and what the database user is allowed to do)
is described in [docs/server/DESIGN.md](docs/server/DESIGN.md). Found a gap
between what that document promises and what the code does? That's exactly the
kind of report we want.

## Hardening your own installation

- Keep the server bound to 127.0.0.1 and put a reverse proxy with TLS in front.
- Require TOTP for every account on a shared server.
- Give the application's database user only the grants in
  [deploy/grants.sql](deploy/grants.sql). The `SELECT` / `INSERT`-only grant on
  the comment and event tables is what keeps history append-only, even if the
  application is compromised.
- Keep the secret key out of your shell history and your repository. Back up
  the database regularly.
- Verify `SHA256SUMS` and its minisign signature before installing or upgrading.

Deployment details are in [docs/server/DEPLOY.md](docs/server/DEPLOY.md).
