# Getting started

[Guide contents](README.md) · Previous: [Where Looptrack fits](where-it-fits.md) · Next: [Daily use](daily-use.md)

This section takes you from a machine with nothing installed to your first full loop, with every command spelled out.
Run the steps in order.
The examples use the values below; substitute your own.

| Item | Example value |
| -- | -- |
| Version | `v1.0.0` |
| Server directory | `~/looptrack-server` (Windows: `%USERPROFILE%\looptrack-server`) |
| Server URL (local) | `http://127.0.0.1:8090/looptrack` |
| Administrator login | `admin` |
| Project slug / ID prefix | `demo` / `DEMO` (the first issue is `DEMO-0001`) |
| Repository you work in | `~/work/demo-app` |

## 0. Choose how you will run it

| Mode | Good for | Storage | Sign-in |
| -- | -- | -- | -- |
| Local, single user | Using it on your own machine only | SQLite (a single file) | Skipped (listens on 127.0.0.1 only) |
| Team server | Several people | MySQL or SQLite | Login and password (+ two-factor auth) |

This section describes the **local, single-user** setup.
The differences for a team server are collected under "For a team server" after step 3.
If the team server is a bare Linux machine, you can skip steps 1 to 3 entirely and let the installer do them; see "On a bare Linux server" after step 3.

Before you begin, install the agent you plan to use (Claude Code, Codex, GitHub Copilot, …).
You will also need git.
Nothing else: Looptrack is a single executable, and the server, the CLI and the hooks all run from it.
There is no runtime to install (no separate runtime), and `looptrack issue init` writes no scripts into your project.
This is the same on Windows.

## 1. Download the binaries

From the releases page (https://github.com/howashoji/looptrack/releases ), download these two files:

- `looptrack_<version>_<os>_<arch>` (one binary for the server, the CLI, and the hooks)
- `SHA256SUMS` (the checksums)

`<os>` is `darwin` (macOS), `linux`, or `windows`; `<arch>` is `amd64` or `arm64`.
Windows files end in `.exe`.
Put the binaries somewhere that does not need administrator rights.

### macOS / Linux

```bash
VER=v1.0.0
OS=darwin      # linux on Linux
ARCH=arm64     # amd64 on Intel / AMD CPUs
BASE="https://github.com/howashoji/looptrack/releases/download/$VER"
mkdir -p ~/.local/bin
TMP="$(mktemp -d)" && cd "$TMP"
curl -fsSL -O "$BASE/looptrack_${VER}_${OS}_${ARCH}" -O "$BASE/SHA256SUMS"
grep -E " looptrack_${VER}_${OS}_${ARCH}\$" SHA256SUMS | shasum -a 256 -c -   # on Linux: sha256sum -c -
install -m 755 "looptrack_${VER}_${OS}_${ARCH}" ~/.local/bin/looptrack
```

Make sure the checksum output says `OK`.
If `~/.local/bin` is not on your PATH, add this line to your shell profile (`~/.zshrc`, `~/.bashrc`, …) and open a new terminal.

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Windows (PowerShell)

No administrator rights are needed.
The binaries go in `%LOCALAPPDATA%\Programs\looptrack`.

```powershell
$Ver = 'v1.0.0'
$Arch = 'amd64'   # arm64 on ARM PCs
$D = Join-Path $env:LOCALAPPDATA 'Programs\looptrack'
New-Item -ItemType Directory -Force -Path $D | Out-Null
$Base = "https://github.com/howashoji/looptrack/releases/download/$Ver"
Invoke-WebRequest -UseBasicParsing -Uri "$Base/looptrack_${Ver}_windows_$Arch.exe" -OutFile (Join-Path $D 'looptrack.exe')
Invoke-WebRequest -UseBasicParsing -Uri "$Base/SHA256SUMS" -OutFile (Join-Path $D 'SHA256SUMS')
Get-Content (Join-Path $D 'SHA256SUMS') | ForEach-Object {
  $h, $n = $_ -split '\s+', 2
  if ($n -match "^looptrack_${Ver}_windows_$Arch\.exe$") {
    $f = Join-Path $D 'looptrack.exe'
    if ((Get-FileHash -Algorithm SHA256 -Path $f).Hash -ne $h) { throw "SHA-256 mismatch: $n" } else { "OK $n" }
  }
}
$UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($UserPath -split ';') -notcontains $D) { [Environment]::SetEnvironmentVariable('Path', "$UserPath;$D", 'User') }
```

Make sure you see an `OK` line.
The PATH change only applies to **terminals and apps started afterwards**.
Open a new PowerShell window before you continue.

> **`looptrack issue verify` needs Git Bash on Windows.** Verification commands run under `bash -c`, and
> Looptrack does not fall back to `cmd.exe` or PowerShell (a POSIX command line means something else there).
> Install Git for Windows — `winget install --id Git.Git -e` — if you want `verify`. Without it you cannot
> close an issue that has a `## Verify commands` section in a project with `verify.require_on_close`.
> `looptrack doctor` says whether it found a shell.

### Windows (Git Bash)

Git Bash works too.
Follow the macOS / Linux steps with `OS=windows`, and add `.exe` to the file names.

```bash
VER=v1.0.0
OS=windows
ARCH=amd64     # arm64 on ARM PCs
BASE="https://github.com/howashoji/looptrack/releases/download/$VER"
mkdir -p ~/.local/bin
TMP="$(mktemp -d)" && cd "$TMP"
curl -fsSL -O "$BASE/looptrack_${VER}_${OS}_${ARCH}.exe" -O "$BASE/SHA256SUMS"
grep -E " looptrack_${VER}_${OS}_${ARCH}\.exe\$" SHA256SUMS | sha256sum -c -
cp "looptrack_${VER}_${OS}_${ARCH}.exe" ~/.local/bin/looptrack.exe
```

PowerShell and your agent may not see Git Bash's `~/.local/bin`.
If an agent will call `looptrack`, installing into `%LOCALAPPDATA%\Programs\looptrack` with the PowerShell steps is more reliable.

### Check

```bash
looptrack version
```

### Signatures and OS warnings

- **macOS**: the official macOS binaries are signed with the publisher's Apple Developer ID and notarized by Apple.
  Gatekeeper does not block them, even when you download them with a browser.
  Binaries you build yourself (for example, from a fork) are not signed.
- **Windows**: the Windows binaries are **currently not signed**.
  When you start one downloaded with a browser, Microsoft Defender SmartScreen may show "Windows protected your PC".
  First check that the SHA-256 matches `SHA256SUMS` (the steps above), then click **More info** → **Run anyway**.
  Files downloaded with `Invoke-WebRequest` and started from PowerShell usually do not show this screen.
- **`SHA256SUMS` is signed with minisign** (`SHA256SUMS.minisig` on the releases page).
  `looptrack self-update` checks this signature before replacing itself.
  To check it by hand, install [minisign](https://jedisct1.github.io/minisign/), download `SHA256SUMS.minisig` next to `SHA256SUMS`, and run:

```bash
minisign -Vm SHA256SUMS -P RWRrJP/r1wfXKalGsxLnzFmmsExUd2azSJh4ccrYDJEBu8yE3N0ZlLJy
```

It should print `Signature and comment signature verified`.

## 2. Set up the server (`looptrack setup`)

Create a directory for the server and run the interactive wizard.

```bash
mkdir -p ~/looptrack-server
cd ~/looptrack-server
looptrack setup
```

In PowerShell:

```powershell
New-Item -ItemType Directory -Force -Path "$HOME\looptrack-server" | Out-Null
Set-Location "$HOME\looptrack-server"
looptrack setup
```

Answer the questions as follows (values in `[ ]` are defaults; press Enter to accept them).

| Question | Answer for local, single-user use |
| -- | -- |
| ① Mode | Local, single user (default) |
| ② Storage | SQLite (default; the file is `<server directory>/im.db`) |
| ③ Listening | Port `8090`, URL prefix `/looptrack` (defaults) |
| ④ First administrator | Login `admin`, display name, password (12+ characters, entered twice) |
| ⑤ Two-factor auth | Optional (the local default; it has no effect because local mode skips sign-in) |
| ⑥ First project | `-` (create none here; step 4 creates `demo`. Entering a slug creates it now and you can skip step 4) |

Review the summary and enter `y`. The wizard creates:

- `<server directory>/.env` (settings; it contains secrets, so do not share it or commit it)
- `<server directory>/im.db` (the SQLite data)
- the first administrator

When it finishes, it prints the start command, the browser URL, the CLI login command, and the MCP connection settings.
**If you lose `LOOPTRACK_SECRET_KEY` in `.env`, nobody's two-factor auth will work any more.** Keep a copy of `.env` somewhere safe.

If you stop with Ctrl-C part-way, no files and no users are left behind.
Running `looptrack setup` a second time changes nothing and shows the current settings.
To start over, run `looptrack setup --force`.

## 3. Start the server

```bash
cd ~/looptrack-server
looptrack serve --env-file ./.env
```

In PowerShell: `looptrack serve --env-file .\.env`.
Leave this terminal open; closing it stops the server.
Open http://127.0.0.1:8090/looptrack/ in a browser and check that the page loads.
In local, single-user mode you will not see a login page.

Local mode listens on `127.0.0.1` only.
**Do not use it on a machine shared by several people.** Other users on the same machine could use it without signing in.

### For a team server

- In ①, choose "team server". The default storage is MySQL; pass the MySQL connection string in the environment variable `LOOPTRACK_SETUP_DSN`.
- In ③, enter the public URL (e.g. `https://im.example.com`). Two-factor auth defaults to required.
- The wizard writes a `compose.yaml` next to `.env`, plus the `Dockerfile` and `NOTICE` it builds from.
  Start it with `docker compose up -d`; the image is built locally, not pulled.
  On Linux the wizard also copies itself there as `looptrack`; elsewhere, put a `linux/amd64` build there first.
- Put a reverse proxy (Nginx, …) in front: receive `/looptrack/` and pass it to `127.0.0.1:8090`. Do not strip the `/looptrack` prefix.
- For the admin commands in step 4, use `docker compose run --rm --no-deps looptrack …` instead of `looptrack …` (e.g. `docker compose run --rm --no-deps looptrack user list`).
- Non-interactive runs (`--yes`) and how to split MySQL privileges are described under 「新しく立ち上げる」 in [docs/server/DEPLOY.md](../server/DEPLOY.md).

### On a bare Linux server (`install.sh`)

On a fresh Ubuntu LTS or Debian server you do not have to do steps 1 to 3 by hand.
One script downloads the binary, runs `looptrack setup`, writes a systemd unit (or a `compose.yaml`), starts the service and checks that it answers.

```bash
VER=v1.0.0
curl -fsSL -O "https://github.com/howashoji/looptrack/releases/download/$VER/install.sh"
less install.sh   # read it before running it
sudo sh install.sh --from "https://github.com/howashoji/looptrack/releases/download/$VER"
```

`--from` is where the executable is fetched from; the installer verifies `SHA256SUMS` and its signature before it installs anything.
If you built the distribution yourself, pass that directory instead: `sudo sh deploy/install.sh --from /path/to/dist`.

The installer first asks how to run the server (systemd or Docker compose), then the same questions as `looptrack setup`.
When it finishes it prints Nginx and Caddy configuration examples (also written to `/etc/looptrack/proxy-examples.txt`), the browser URL, the CLI login command and the MCP connection settings.
Put the reverse proxy in front, sign in at the public URL, and continue from step 4.

If you choose MySQL and give the application's database user only the privileges it needs (`deploy/grants.sql`), the order matters: per-table `GRANT`s can only be run once the tables exist.
So it is: run the installer (setup creates the tables) → load the grants file with an administrative account → run the installer again, which only starts the service and checks it.
Before starting, the installer tries to read the storage as the service user; if it cannot, it stops and prints this order instead of waiting for a server that will never come up.

To update, run `sudo sh install.sh --upgrade --from <source>`; to remove it, `--uninstall` (`--purge` also deletes the settings and the data).

## 4. Create a project and join it

**If you already created a project at question ⑥ of the wizard, skip this step.**
Re-creating the same slug fails.
You can also create one from the browser later (`/admin/projects`, "Create project").

Projects are created with an admin command.
Admin commands connect to storage using `LOOPTRACK_DSN` from `.env`.
Run them **in a different terminal from the one running the server**.

macOS / Linux / Git Bash:

```bash
cd ~/looptrack-server
set -a; . ./.env; set +a
looptrack project create demo --prefix DEMO --name "Demo" --description "A project for trying Looptrack"
looptrack member set demo admin --role admin
looptrack project list
```

PowerShell:

```powershell
Set-Location "$HOME\looptrack-server"
Get-Content .\.env | ForEach-Object { if ($_ -match "^([A-Z_]+)='(.*)'$") { Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2] } }
looptrack project create demo --prefix DEMO --name "Demo" --description "A project for trying Looptrack"
looptrack member set demo admin --role admin
looptrack project list
```

- `--prefix` (the ID prefix) and `--width` (digits in the number; default 4) **cannot be changed later**.
- Even an administrator can only read a project they have not joined. To write, join it as editor or admin with `member set`.
- If `demo` appears at http://127.0.0.1:8090/looptrack/, it worked.

## 5. Users and permissions (team server)

Skip this step for local, single-user use.
On a team server, an administrator adds users in the browser.

1. Open `<server URL>/admin/users` and add a user with a login, display name, initial password, and role (admin / member)
2. On the same page, under project permissions, give the user `editor` (create, comment, change status) or `viewer` (read only) on the project
3. Send the user the URL, login, and initial password. They sign in and, if two-factor auth is required, register an authenticator app. They can change their password at `<server URL>/account`

See [Administration](admin.md) for more.

## 6. Sign in the CLI (`looptrack issue login`)

Skip this step for local, single-user use: like the UI and the agent's MCP connection, the CLI works without a token there.
You sign in when you point it at a team server.

```bash
looptrack issue login --browser --url http://127.0.0.1:8090/looptrack
```

A browser opens; sign in and click Allow (in local, single-user mode there is no login page).
If no browser opens, open the URL that is printed.
The token is stored in your user configuration directory, readable only by you (`~/.config/looptrack/credentials.json` on macOS / Linux, `%APPDATA%\looptrack\credentials.json` on Windows).
After that, it is refreshed automatically before it expires.

Where there is no browser (for example over ssh), create an access token at `<server URL>/account` and paste it into:

```bash
looptrack issue login --url http://127.0.0.1:8090/looptrack
```

## 7. Install into a project (`looptrack issue init`)

Run this in the repository you work in.
Start with `--dry-run` to see what would be written.

```bash
mkdir -p ~/work/demo-app
cd ~/work/demo-app
git init
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp --dry-run
looptrack issue init --project demo --url http://127.0.0.1:8090/looptrack --agent claude-code --mcp --no-loop
```

- `--agent` is the agent you use: `claude-code`, `codex`, `copilot`, or `other`. Separate several with commas (e.g. `claude-code,codex`).
- `--mcp` also writes the MCP connection settings (`.mcp.json` for Claude Code).
- `--no-loop` installs core only; use `--loop` to add loop as well. With neither, you will be asked. See [Agent-specific notes](ai-agents.md) for how to choose.
- Existing settings are merged, not replaced. Changed files are backed up to `.claude/.looptrack-init-backup/<timestamp>/`.
- A self-check runs after installing; if it fails, everything written is rolled back.
- Running it again is safe. The second run reports that nothing changed.

For Claude Code, the main things installed are:

| What | Contents |
| -- | -- |
| `env` in `.claude/settings.json` | `LOOPTRACK_API_URL` and `LOOPTRACK_PROJECT` (no token is written) |
| hooks in `.claude/settings.json` | Session-start summary, freshness guard, token tracking (`looptrack hook …`) |
| `.claude/skills/issue/` | The `/issue` skill |
| `CLAUDE.md` | A managed section between `<!-- looptrack:begin -->` and `<!-- looptrack:end -->` |
| `.mcp.json` | MCP connection settings (with `--mcp`) |

If `looptrack` is not found on PATH, init wires the hooks with an absolute path and writes that wiring to the machine-local `.claude/settings.local.json`.
Check the wiring and PATH with:

```bash
looptrack doctor
```

## 8. Connect MCP

If you used `init --mcp`, Claude Code is already configured.
To add it by hand:

```bash
claude mcp add --transport http looptrack http://127.0.0.1:8090/looptrack/mcp --header "X-Looptrack-Project: demo"
```

Or in `.mcp.json`:

```json
{ "mcpServers": { "looptrack": { "type": "http", "url": "http://127.0.0.1:8090/looptrack/mcp",
  "headers": { "X-Looptrack-Project": "demo" } } } }
```

**Restart** Claude Code.
If it asks you to approve hooks, review them and approve.
On a team server, pick `looptrack` in Claude Code's `/mcp` and allow access in the browser.
For Codex and Copilot, see [Agent-specific notes](ai-agents.md).

## 9. Your first loop

First, check the connection from the CLI.

```bash
cd ~/work/demo-app
export LOOPTRACK_API_URL=http://127.0.0.1:8090/looptrack LOOPTRACK_PROJECT=demo
looptrack issue config
looptrack issue guide
```

The two variables are needed because `init` writes them into `.claude/settings.json`,
which only reaches the agent's own session — a plain terminal does not see them.
If `config` shows the project `demo` and your role, you are connected.

Next, file your first issue and walk through one turn by hand.

```bash
looptrack issue new "Write an overview in README" --type task --body "$(cat <<'EOF'
Write a three-line overview of this repository in README.md.

## 受け入れ条件

- [ ] README.md exists
- [ ] It has three lines of overview

## 検証コマンド

- `test -f README.md`
- `test "$(grep -c . README.md)" -ge 3`
EOF
)"
looptrack issue next
```

`next` moves `DEMO-0001` to In Progress and shows its body and acceptance criteria.
`## 受け入れ条件` (acceptance criteria) and `## 検証コマンド` (verify commands) are the section headings the server looks for. You can write them in English instead: `## Acceptance criteria` and `## Verify commands` (case does not matter).
From here, let the agent take over.
Open Claude Code and ask:

> Take the next issue and do one full loop. Verify the acceptance criteria before you close it.

The agent does the work, runs the verify commands with `verify`, records the results, and closes the issue.
Finally, check the result yourself:

```bash
looptrack issue show DEMO-0001
looptrack issue verify DEMO-0001 --last
looptrack issue summary
```

If `show` lists the comments and the status is Done, your first loop is complete.
You can see the same issue in the browser at http://127.0.0.1:8090/looptrack/p/demo/.

PowerShell has no here-documents; write the body to a file and pass it with `--body (Get-Content -Raw body.md)`.

## 10. Language (English / Japanese)

Everything you read comes out in English or Japanese. Set it explicitly with
`LOOPTRACK_LANG`, or leave it to your terminal and browser:

```bash
LOOPTRACK_LANG=en looptrack issue list   # this command only
export LOOPTRACK_LANG=ja                 # this shell
```

| Order | Command line | Web pages and MCP |
| -- | -- | -- |
| 1 | `LOOPTRACK_LANG` | `?lang=ja` / `?lang=en`, for that one request |
| 2 | `LC_ALL`, then `LC_MESSAGES`, then `LANG` | `LOOPTRACK_LANG`, which the CLI sends as an explicit choice |
| 3 | English | The display language you pick on `/account` (it can be left unset) |
| 4 | — | `Accept-Language` |
| 5 | — | English |

Pick a display language on `/account` and **even a connection that cannot send headers (MCP) comes back in it**.
Set it back to unset and it follows your browser and terminal again, exactly as before.

What an AI agent reads (the MCP `instructions`, the `guide` bodies, the tool
descriptions) comes back in the same language, picked for each connection. The rules installed into
your project are shipped in both languages, and the hooks pick one at run time by the
same order. For skills, `looptrack issue init` installs only the one in the language
of the installation (run it again after changing the language).
