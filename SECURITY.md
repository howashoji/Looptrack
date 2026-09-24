# Security Policy

Looptrack stores the issue history of real projects and holds credentials for
the people and the AI agents that work on them. We take reports seriously and
would rather hear about a problem early than late.

## Supported versions

| Version | Supported |
| -- | -- |
| 1.x (latest release) | Yes |
| Anything older than the latest release | No — please upgrade first |

Fixes are shipped in a new patch release of the latest minor version. We do not
backport security fixes to older minor versions.

## Reporting a vulnerability

**Please do not open a public issue, pull request, or discussion for a security
problem.** A public report is a working exploit handed to everyone who runs the
software.

Report it privately through GitHub:

1. Go to the repository's **Security** tab.
2. Choose **Report a vulnerability** (GitHub's Private vulnerability reporting).
3. Fill in the form and submit. The report and the conversation that follows are
   visible only to you and the maintainers.

If Private vulnerability reporting is unavailable to you for any reason, open a
public issue that says only *"I would like to report a security issue privately"*
— with no details at all — and a maintainer will open a private advisory and
invite you to it.

### What to include

The more of this you can give us, the faster we can confirm and fix it:

- The version (`looptrack version`) and how it is deployed (desktop edition,
  `install.sh` on a server, Docker Compose, built from source).
- The database (MySQL or SQLite) and whether the server runs behind a reverse
  proxy, plus the URL prefix if you changed it.
- Which surface is affected: REST API, MCP, Web UI, CLI, hooks, the installer,
  or the release artifacts.
- Step-by-step reproduction, and what an attacker gains. A minimal proof of
  concept is very welcome.
- Whether the issue is already public anywhere.

Please test only against your own installation. Do not access, modify, or
exfiltrate other people's data, and do not run denial-of-service or spam tests
against someone else's server.

## What to expect

| Stage | Target |
| -- | -- |
| Acknowledgement of your report | within 3 business days |
| First assessment (confirmed / not a vulnerability / need more information) | within 10 business days |
| Fix released for a confirmed high-severity issue | within 30 days of confirmation |
| Fix released for other confirmed issues | with the next regular release |

If we miss one of these, we will say so in the advisory thread rather than go
quiet. If we decide something is not a vulnerability, we will explain why.

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

We normally publish within 90 days of confirming a report, even if the fix is
not complete, so that operators can mitigate. If you plan to publish earlier,
please tell us so we can coordinate.

There is no bug bounty for this project.

## Things that are not vulnerabilities in this project

These come up often and are working as designed, so they are handled as normal
issues rather than security reports:

- **Local mode has no sign-in.** When Looptrack runs in local mode it binds to
  127.0.0.1 only and treats everything reaching it as the single local user.
  Exposing that port to a network is a deployment mistake, not a product bug —
  but a way to reach local mode *from a web page in the user's browser* is a
  vulnerability, so please do report that.
- **The server does not terminate TLS.** It is meant to sit behind a reverse
  proxy that does. Reports that plain HTTP is available on the loopback port are
  expected behaviour.
- **Anything a project `editor` can legitimately do** — editing issue bodies,
  adding comments, changing status. Privilege boundaries that matter are:
  viewer vs. editor vs. admin, and crossing between projects.
- **Unsigned Windows binaries.** Known and documented in the README; we plan to
  address it with an OSS code-signing programme.

The security model the server is built on — authentication, per-project
permissions, append-only history, and what the database user is allowed to do —
is described in [docs/server/DESIGN.md](docs/server/DESIGN.md). If you find a
gap between what that document promises and what the code does, that is exactly
the kind of report we want.

## Hardening your own installation

- Keep the server bound to 127.0.0.1 and put a reverse proxy with TLS in front.
- Require TOTP for every account on a shared server.
- Give the application's database user only the grants in
  [deploy/grants.sql](deploy/grants.sql); the `SELECT` / `INSERT`-only grant on
  the comment and event tables is what keeps history append-only even if the
  application is compromised.
- Keep the secret key out of your shell history and your repository, and back up
  the database regularly.
- Verify `SHA256SUMS` and its minisign signature before installing or upgrading.

Deployment details are in [docs/server/DEPLOY.md](docs/server/DEPLOY.md).
