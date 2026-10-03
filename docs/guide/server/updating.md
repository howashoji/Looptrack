# Updating the server and the CLI

[Guide contents](../README.md) · [Server edition](README.md) · Related: [Getting started with the server](getting-started.md) · [FAQ / Troubleshooting](../faq.md)

This page covers updating the server and the CLI (`looptrack`) on your machine. The desktop app updates in its own way, described in [Updating the desktop app](../desktop/updating.md).
**By default, neither of them replaces itself with a new version automatically.**
The server's administrator can set it to replace itself automatically (off by default). Anything else is replaced by a user or an administrator.
What does happen on its own is this: you're told about a new version, and things get tidied up after you replace something.

## What is automatic and what is manual

| Target | What happens automatically | What you do |
| -- | -- | -- |
| CLI (`looptrack`) | When your version is out of date, "[Update the distributed files]" is added at the start of an agent session and to MCP tool results — only when the server distributes `looptrack` or sets a minimum supported version. The CLI looks at GitHub releases only when you run `self-update` | Have the agent call the setup tool and run the step it returns (one command, with `--url`, that downloads and runs init). To do it by hand, use that same step the setup tool returned |
| Server | When a new version is out, a strip on administrators' screens, `looptrack doctor` and the server's log tell you (it checks at startup and every 24 hours). A systemd server installed with `install.sh` replaces itself once a day only if you turn that on (off by default) | An administrator replaces it with `install.sh --upgrade` or similar. On a server installed with `install.sh`, `install.sh` also brings the `looptrack` for users in the distribution directory up to date. Only on other servers does the administrator put it there by hand ([The looptrack you distribute to users](#the-looptrack-you-distribute-to-users)) |

The CLI's sign-in (its access token) gets extended automatically too. That's a separate mechanism from version updates, though.

## CLI (looptrack)

### How you hear about a new version

When an agent session starts, the hook tells the server "the version of `looptrack` in this working environment".
The server compares it with the newest version it distributes. If yours is older, it adds "[Update the distributed files]" in two places:

- at the end of the summary shown at session start (`looptrack hook summary`)
- to MCP tool results

The notice tells you why, and how to fix it (call the setup tool and run the step it returns).
The reason is either "older than the newest one distributed" or "older than the oldest version this server supports".
You'll see the second only if the administrator has set a minimum version. Then some features won't work correctly until you update.

What if the server neither distributes `looptrack` nor sets a minimum version? There's nothing to compare against, so the notice never appears ([The looptrack you distribute to users](#the-looptrack-you-distribute-to-users)).
In that case, check the releases page, or compare with GitHub releases using `looptrack self-update --from github --check`.

### Replacing it with self-update

```bash
looptrack self-update --check --url <server URL>   # only check whether a newer version exists (replaces nothing)
looptrack self-update --url <server URL>           # replace it
looptrack self-update                               # with no URL, replace it from GitHub releases
```

There are two places to fetch from.
With `--url` or the `LOOPTRACK_API_URL` environment variable it fetches from the server's distribution; with neither, from GitHub releases.
You can also pick one with `--from server` or `--from github`. A failure on one never falls back to the other.

**From the server's distribution**, `self-update` fetches the newest `looptrack` for this OS and CPU from the distribution listing. Inside a project, `--url` can come from the `LOOPTRACK_API_URL` environment variable.
It checks the file against the SHA-256 in the listing. Official builds also verify the minisign signature on `SHA256SUMS`.
They check one more thing: that the version in the listing is the one that was signed. The file name in `SHA256SUMS` (`looptrack_<version>_<os>_<arch>`) and, for a release, the version in the signature's trusted comment must both match it.
If anything doesn't match, nothing gets replaced.
If your version is the same as or newer than the distributed one, it does nothing. To go back to an older version, add `--force`.
A version you built yourself (`dev` and the like) can't be compared with the distributed one, so replacing it takes `--force` too.

**From GitHub releases**, the version is picked the same way as in [Checking for new versions](#checking-for-new-versions-desktop-app-and-server). If you run an rc it follows rcs too, and the `LOOPTRACK_UPDATE_CHANNEL` environment variable changes that.
What it fetches is the server archive (`looptrack_<version>_<os>_<arch>_server.tar.gz`, or `.zip` on Windows).
The archive is checked against the signed `SHA256SUMS`, only the `looptrack` inside is extracted, and that file is checked once more against the `looptrack_<version>_<os>_<arch>` line of the same `SHA256SUMS`.
It replaces the file only when a newer version exists, and `--force` is not accepted.
A build without the public key for checking signatures stops without contacting GitHub. So does any build when `LOOPTRACK_UPDATE_CHECK=off` is set.

Windows can't delete a running file. So the old file is renamed to `looptrack.exe.old` first, and then the new one goes in its place (whichever source you fetch from). The next `self-update` removes the `.old` file.

### What self-update does not replace

In these two cases `self-update` stops with an error and replaces nothing. `--check` still works.

- **The `looptrack` inside the desktop app on macOS and Linux**: replace the whole app ([Updating the desktop app](../desktop/updating.md)).
  The distributed `looptrack` has no tray. Swap it in, and the app won't start on a double-click anymore.
  On Windows the CLI is a separate file from the app (`Looptrack.exe`), so `self-update` can replace it. What that leaves behind is in [Updating only the CLI with self-update](../desktop/updating.md#updating-only-the-cli-with-self-update).
- **The `looptrack` of a server installed with `install.sh`**: update it with `install.sh --upgrade` ([Server](#server)).
  Replacing the file alone neither migrates the database nor restarts the server, so the running server and the file would end up on different versions.

### Bringing each project's kit up to date

The hooks, rule texts and skills (the kit) are embedded in `looptrack`.
So after `self-update`, run `init` once more in each project. That brings its kit and wiring up to the new version.

```bash
looptrack issue init --project <slug> --url <server URL> --agent <agent>
```

The "[Update the distributed files]" notice does not show this command directly. Have the agent call the setup tool, and you get a single download-and-init command with `--url` (the download is skipped when the `looptrack` in place matches the distributed one). A command from the notice, run in a terminal with neither `--url` nor `LOOPTRACK_API_URL`, would point at your local mode instead.
Once you've updated, the next session start reports to the server again and the notice goes away.

## Server

By default, the server isn't updated automatically. An administrator replaces it (you're told about new versions; see "New-version notices and automatic replacement" below).
A team server doesn't migrate its database when it starts. So run the migration along with the replacement.
Every procedure below follows the same order: stop → back up the database → replace the binary → migrate → start.

### A server installed with install.sh

```bash
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade
```

Without `--from`, you get the newest release on GitHub Releases (pick another with `--version <version>`).
To fetch it from somewhere else, add `--from <source>` (a local directory of `deploy/release/dist.sh build` output, or a URL prefix in the same form).
Servers installed with the `install.sh` of 1.0.0-rc.1 or rc.2 upgrade with the same line. The installer reads the `/etc/looptrack/install.conf`, `.env` and unit (or `compose.yaml`) they left, as they are.
Here's what `--upgrade` does:

1. Downloads the new version (`looptrack_<version>_linux_<arch>_server.tar.gz` from GitHub Releases) and checks its SHA-256 before unpacking it. If the server has `minisign`, it checks the signature too.
   It also downloads the `looptrack` you distribute to users (all six platforms) and checks it against the same `SHA256SUMS` (see "The looptrack you distribute to users" below).
2. Stops the service.
3. With SQLite, copies the database to `backup-<date and time>/` while it is stopped. Back up MySQL yourself (`mysqldump` or similar).
   Attachment files aren't in this backup. Back them up separately ([Administration](admin.md#where-attachments-live-and-backups)).
4. Replaces the binary. The previous version stays at `/usr/local/bin/looptrack.prev`. With compose it builds a new image and keeps the previous version's image.
5. Runs the migration.
6. Puts the new `looptrack` for users in the distribution directory, then starts the service and waits for `/healthz` to respond. With systemd, if some other process (an old container left running, say) is already answering `/healthz` on the same port once the service is stopped, it replaces nothing and stops with an error naming that port, instead of taking the other process's answer for its own (an unattended upgrade starts the service again and ends as failed). After starting, it also checks that the one listening on the port is the service itself (this needs `ss`; without it, it only checks that the service is active).

Same version? It doesn't replace the server; it only brings the `looptrack` you distribute to users in line with that version (if it already is, nothing changes).
MySQL with the minimum grants needs one more step. When a version adds tables, the application user can't read them until it's granted access again after the migration. So `--upgrade` asks for administrative MySQL credentials on the terminal at that point (not shown, not stored), grants access again, and then starts the service.
A version with migrations creates its tables in the migration after the service stops, and an application user with the minimum grants can't create them. Pass a connection that can, in the environment variable `LOOPTRACK_SETUP_MIGRATE_DSN`. Without it, `--upgrade` tells you so before stopping anything and asks whether to go on; by default it stops there, changing nothing.
For details, see ["Upgrading (--upgrade)" in DEPLOY.md](../../server/DEPLOY.md), written for operators.

### New-version notices and automatic replacement

The server checks GitHub Releases at startup and every 24 hours. When a new version is out, it tells you in three places:

- A strip at the top of the screen for administrators (users whose role is admin), with the one-line update command. Members do not see it
- A note in `looptrack doctor` run with an administrator's token
- The server's log (`journalctl -u looptrack`, or `docker compose logs` with compose)

To stop checking, set `LOOPTRACK_UPDATE_CHECK=off` in the server's `.env` and restart it (for the other settings, see [Checking for new versions](#checking-for-new-versions-desktop-app-and-server) below).

A systemd server installed with `install.sh` can be set to replace itself automatically. **It is off by default.**

```bash
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --auto-upgrade on    # turn it on (off turns it off)
systemctl list-timers looptrack-upgrade.timer    # when it runs next
journalctl -u looptrack-upgrade                  # the record of automatic replacements (including why one was skipped)
```

Once it's on, the server replaces itself with a new version once a day, using the same steps as `--upgrade` above.
What runs is the copy of `install.sh` placed on the server when you turned it on. It isn't downloaded again each time (the copy is refreshed whenever an administrator runs `install.sh`).
It downloads only the new version's archive. It always checks the signature (the server needs `minisign`), and it never moves to an older version.
If something fails partway, it goes back to the previous version and starts it again, so the service is never left stopped.
Container images (compose) aren't replaced automatically. When you hear about a new version, run `--upgrade`.
With MySQL, a new version that changes the shape of the database (one with migrations to run) isn't replaced automatically either; the server keeps running the current version. Run `--upgrade` on a terminal in that case, passing a connection that can create tables (`LOOPTRACK_SETUP_MIGRATE_DSN`).
For details, see ["New-version notices and automatic replacement" in DEPLOY.md](../../server/DEPLOY.md).

### A server set up with looptrack setup alone

A server set up with `looptrack setup`, without `install.sh`, has no all-in-one procedure like `--upgrade`.
So you go through the same order by hand. With compose (the `setup` default), run this in the directory where you ran `setup`:

```bash
docker compose stop
# with SQLite, back up ./data; with MySQL, back up with mysqldump or similar
# replace the looptrack in this directory with the new linux looptrack
# replace NOTICE with the new looptrack's output as well (./looptrack licenses > NOTICE)
docker compose build
docker compose run --rm --no-deps looptrack migrate
docker compose up -d
```

Under systemd (`--service systemd`), the order is the same.
Stop the service, replace the binary, run `looptrack migrate` with the settings from `.env` loaded, then start it.

### The looptrack you distribute to users

Replacing the server doesn't update the `looptrack` on users' machines.
`self-update` and "[Update the distributed files]" use the `looptrack` placed in the server's **distribution directory**.
You set that directory with `LOOPTRACK_DIST_DIR` in `.env`. Without it, the server doesn't distribute `looptrack`.

**On a server installed with `install.sh`, `install.sh` takes care of it.** Every time it installs or runs `--upgrade` (including the automatic replacement), it fills the directory from the release it downloaded: the binaries for all six platforms, each checked against the signed `SHA256SUMS`, plus that `SHA256SUMS` and its signature.
The directory is `/usr/local/share/looptrack/dist` under systemd and `<dir>/dist` with compose (mounted read-only at `/dist`), and `install.sh` adds `LOOPTRACK_DIST_DIR` to `.env` if it isn't there.
A server installed with an older `install.sh` gets this the first time you run `--upgrade` with the new one (even on the same version; restart the service afterwards as it tells you).
If `LOOPTRACK_DIST_DIR` already points somewhere else, `install.sh` leaves that directory alone. With a `compose.yaml` written by an older setup, add `- ./dist:/dist:ro` under `services.looptrack.volumes` first. Opening the Windows archives (zip) needs `unzip` or `python3` on the server.
When the distributed `looptrack` doesn't match the server's version (no directory, a platform missing, or an older version), `looptrack serve` says so in its startup log and in a banner for administrators.

Otherwise (a server not installed with `install.sh`, or a directory you manage yourself), the administrator replaces what's in the distribution directory to distribute a new version:

1. Put `looptrack_<version>_<OS>_<CPU>` for each OS (with `.exe` at the end for Windows).
   GitHub Releases ships archives, not bare binaries. Take the `looptrack` out of each `looptrack_<version>_<OS>_<CPU>_server.tar.gz` (`.zip` for Windows) and put it under the name above.
   Don't put the archives themselves in the distribution directory (the server distributes only the bare binaries).
2. Then put `SHA256SUMS`. For official releases, put the release's `SHA256SUMS` and `SHA256SUMS.minisig` as they are, because an official build's `self-update` replaces nothing without the signature.
   The official `SHA256SUMS` also lists the binary inside each archive under the name `looptrack_<version>_<OS>_<CPU>[.exe]`. So the binaries you took out get checked against the signed list.
3. Remove the previous version's files.

From an official release, on the server (Linux):

```bash
VER=v1.0.0
BASE="https://github.com/howashoji/looptrack/releases/download/$VER"
cd /path/to/dist            # LOOPTRACK_DIST_DIR
curl -fsSL -O "$BASE/SHA256SUMS" -O "$BASE/SHA256SUMS.minisig"
for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64 windows_arm64; do
  n="looptrack_${VER}_${t}_server"
  case $t in
    windows_*) curl -fsSL -O "$BASE/$n.zip" && grep -E " $n\.zip\$" SHA256SUMS | sha256sum -c - &&
                 unzip -p "$n.zip" "$n/looptrack.exe" >"looptrack_${VER}_${t}.exe" && rm "$n.zip" ;;
    *) curl -fsSL -O "$BASE/$n.tar.gz" && grep -E " $n\.tar\.gz\$" SHA256SUMS | sha256sum -c - &&
         tar -xzOf "$n.tar.gz" "$n/looptrack" >"looptrack_${VER}_${t}" && rm "$n.tar.gz" ;;
  esac || break
done
sha256sum -c --ignore-missing SHA256SUMS   # the binaries you took out (the archive lines have no file here, so they are skipped)
```

The server distributes the newest version for each OS and CPU pair. Files missing from `SHA256SUMS`, or whose hash differs, aren't distributed.
Write `LOOPTRACK_CLIENT_MIN_VERSION=v…` in `.env`, and any older `looptrack` gets an "older than the oldest version this server supports" notice.
You'll find the layout in detail in ["Distributing from the server's distribution directory" in RELEASE.md](../../server/RELEASE.md).

## Checking for new versions (desktop app and server)

The desktop app and the server (`looptrack serve`) check the list of releases on GitHub at startup and every 24 hours.
The CLI (`looptrack`) doesn't check on its own. The only notice the CLI gets is "[Update the distributed files]", which the server adds.
When you run `self-update` against GitHub releases, though, the environment variables below apply to it as they are.

- You hear only about a version whose `SHA256SUMS` signature checks out and whose release has the file for your OS and CPU (the dmg, AppImage or zip for the desktop app; the server archive for the server).
- It follows the same kind of version you run. Run an rc, and you hear about rcs too. Run a stable release, and you hear only about stable releases.
- When a check fails (offline, say), the notice for a version it told you about before stays.
- A version you built yourself (`dev` and the like, which can't be compared with released versions) isn't checked.

You can change this with the environment variables below. For the desktop app, pass them in the environment the app starts in; for the server, write them in `.env` and restart it.
Give it a value it doesn't understand? Then it doesn't check, so that it never connects with a setting it has misread.

| Environment variable | What it does |
| -- | -- |
| `LOOPTRACK_UPDATE_CHECK=off` | Does not check (no connection to GitHub). For the desktop app, clearing "Check for updates" in the tray does the same; if either one is off, it does not check |
| `LOOPTRACK_UPDATE_CHANNEL` | Sets what to follow: `stable` (stable releases only) or `prerelease` (rcs too). Without it, the version you run decides |
| `LOOPTRACK_UPDATE_URL` | Checks somewhere else instead. Only an `https://` URL that returns JSON in the same form as the GitHub API is accepted |

## Checking the update

```bash
looptrack version                # your version (also shows headless or desktop, and OS/CPU)
looptrack self-update --check    # compare with the newest version (the server's distribution or GitHub releases; replaces nothing)
looptrack doctor                 # besides PATH and wiring, compares your version with the newest distributed one (with an admin token, also shows a new version of the server)
```

To check the desktop app's version, see [Updating the desktop app](../desktop/updating.md#how-you-hear-about-a-new-version).
For the server, run `/usr/local/bin/looptrack version` on the server (when it was installed with `install.sh`).

## When something goes wrong

| Symptom | What to do |
| -- | -- |
| `self-update` stops with "The server has no looptrack for …" | The server does not distribute `looptrack` for your OS and CPU. Ask the administrator to put it in the distribution directory, or download the new version again as in step 1 of [Getting started](getting-started.md) |
| `self-update` stops with "No server URL" | You passed `--from server` with no URL. Add `--url <server URL>`, or set the `LOOPTRACK_API_URL` environment variable. To fetch from GitHub releases, use `--from github` |
| `self-update` stops because the build "carries no public key to verify signatures" | This build cannot check signatures. Fetch from a server's distribution with `--url`, or reinstall the `looptrack` from an official release |
| `self-update` stops with "--force is not accepted when fetching from GitHub releases" | From GitHub releases it only installs a newer version. To go back to an older one, download the archive from the releases page and replace the file by hand |
| `self-update` stops with "LOOPTRACK_UPDATE_CHECK=off, so GitHub is not contacted" | Remove `LOOPTRACK_UPDATE_CHECK`, or fetch from a server's distribution with `--url` |
| `self-update` says "The local version … cannot be compared with the distributed version …" | It is a version you built yourself. Add `--force` to replace it |
| `self-update` stops because there is no signature (`.minisig`) | The distribution directory has no `SHA256SUMS.minisig`. Ask the administrator to put it there |
| `self-update` fails in the desktop app on macOS or Linux | Replace the whole desktop app ([Updating the desktop app](../desktop/updating.md)) |
| `self-update` fails on the server and points you to the installer's `--upgrade` | The server was installed with `install.sh`. Update it with `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh \| sudo sh -s -- --upgrade` |
| "[Update the distributed files]" is still there after updating | It stays until the next session start reports again. If it says the kit is out of date, run `looptrack issue init` too |
| The migration in `install.sh --upgrade` failed | With systemd the previous binary is at `/usr/local/bin/looptrack.prev`; with compose the previous version's image is kept. The error message tells you how to go back. If the migration applied even one file (there is an `Applied:` line in the output), also restore the DB from the backup taken before the update |
| `install.sh --auto-upgrade on` stops, saying it changed nothing | Automatic replacement cannot be set up on this server. It is not available for a compose server, a server installed with `--no-start`, or a server where systemd is not running. It also needs `minisign` (`apt-get install -y minisign` on Debian and Ubuntu; from EPEL with `dnf install -y epel-release && dnf install -y minisign` on AlmaLinux and the like) and either `curl` or `wget` to fetch the new release's archive and signature |
| Automatic replacement is on, but the server stays on the old version | `journalctl -u looptrack-upgrade` shows why. With MySQL, a new version that changes the shape of the database is not replaced automatically, so run `--upgrade` on a terminal. When a run fails partway, the server has been put back on the previous version and is running |
| After going back to the previous version, it does not start and says the DB was migrated by a newer looptrack | The newer version has already upgraded the DB. The previous version does not use it, so that it cannot damage it by writing. Go back to the newer version, or, to use the previous version, restore the DB (for the desktop app, the data folder) from a backup taken before the newer version migrated it (the desktop app keeps one in the `backups` folder of the data folder) |
