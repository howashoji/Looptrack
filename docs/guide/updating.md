# Updating

[Guide contents](README.md) · Related: [Getting started](getting-started.md) · [Desktop app](desktop.md) · [FAQ / Troubleshooting](faq.md)

The CLI on your machine, the desktop app, and the server are each updated differently.
**By default, none of them replaces itself with a new version automatically.** The desktop app (macOS and Linux) replaces itself when you choose it in the tray menu, and automatically only if you turn that on. The server can be set by its administrator to replace itself automatically (off by default). Everything else is replaced by a user or an administrator.
What does happen automatically is telling you about a new version (the CLI, the desktop app and the server) and tidying up after you replace something.

## What is automatic and what is manual

| Target | What happens automatically | What you do |
| -- | -- | -- |
| CLI (`looptrack`) | When your version is out of date, "[Update the distributed files]" is added at the start of an agent session and to MCP tool results — only when the server distributes `looptrack` or sets a minimum supported version. The CLI never checks GitHub releases by itself | Replace it with `looptrack self-update`, then run `looptrack issue init` again in each project |
| Desktop app | When a new version is out, the top of the tray menu and a strip in the web UI tell you (it checks at startup and every 24 hours). The first start of a new version upgrades your data. The start-at-login registration and the CLI location are also brought in line with the current app. Only with "Install updates automatically" checked (it is not by default) are the replacement and the restart automatic too (macOS and Linux) | On macOS and Linux, choose "Update to version <version>" in the tray. On Windows, replace the whole app |
| Server | When a new version is out, a strip on administrators' screens, `looptrack doctor` and the server's log tell you (it checks at startup and every 24 hours). A systemd server installed with `install.sh` replaces itself once a day only if you turn that on (off by default) | An administrator replaces it with `install.sh --upgrade` or similar. The administrator also puts the new `looptrack` for users into the distribution directory |

The CLI's sign-in (its access token) is also extended automatically, but that is a separate mechanism from version updates.

## CLI (looptrack)

### How you hear about a new version

When an agent session starts, the hook reports "the version of `looptrack` in this working environment" to the server.
The server compares it with the newest version it distributes and, if yours is older, adds "[Update the distributed files]" in two places:

- at the end of the summary shown at session start (`looptrack hook summary`)
- to MCP tool results

The notice gives the reason and the command to run.
The reason is either "older than the newest one distributed" or "older than the oldest version this server supports".
The second appears only when the administrator has set a minimum version. In that case some features will not work correctly until you update.

If the server neither distributes `looptrack` nor sets a minimum version, there is nothing to compare against, so the notice never appears ([The looptrack you distribute to users](#the-looptrack-you-distribute-to-users)).
In that case, check the releases page for a new version.

### Replacing it with self-update

```bash
looptrack self-update --check --url <server URL>   # only check whether a newer version exists (replaces nothing)
looptrack self-update --url <server URL>           # replace it
```

Inside a project, `--url` can be taken from the `LOOPTRACK_API_URL` environment variable.
`self-update` takes the newest `looptrack` for this OS and CPU from the server's distribution listing.
It checks the downloaded file against the SHA-256 in the listing. Official builds also verify the minisign signature on `SHA256SUMS`.
They also check that the version in the listing is the signed one: the file name in `SHA256SUMS` (`looptrack_<version>_<os>_<arch>`) and, for a release, the version in the signature's trusted comment must both match it.
If anything does not match, nothing is replaced.
On Windows the running file cannot be deleted, so the old file is renamed to `looptrack.exe.old` before the new one is put in place. The `.old` file is removed by the next `self-update`.

If your version is the same as or newer than the distributed one, it does nothing. To go back to an older version, add `--force`.
A version you built yourself (`dev` and the like) cannot be compared with the distributed one, so add `--force` to replace it.

### What self-update does not replace

In these two cases `self-update` stops with an error instead of replacing anything. `--check` still works.

- **The `looptrack` inside the desktop app**: replace the whole app ([Desktop app](#desktop-app)).
  The distributed `looptrack` has no tray, so replacing it would stop the app from starting on a double-click.
- **The `looptrack` of a server installed with `install.sh`**: update it with `install.sh --upgrade` ([Server](#server)).
  Replacing the file alone neither migrates the database nor restarts the server, so the running server and the file would end up on different versions.

### Bringing each project's kit up to date

The hooks, rule texts, and skills (the kit) are embedded in `looptrack`.
After `self-update`, run `init` once more in each project to bring its kit and wiring up to the new version.

```bash
looptrack issue init --project <slug> --url <server URL> --agent <agent>
```

When the kit is out of date, the "[Update the distributed files]" notice shows this command as well.
After the update, the next session start reports to the server again and the notice goes away.

## Desktop app

The desktop app does not use `self-update`; you replace the whole app.
When a new version is out, "Update to version <version>" ("New version <version> available" where the app cannot be replaced) appears at the top of the tray menu, and a strip appears at the top of the web UI. To stop checking, clear "Check for updates" in the tray menu ([Tray / menu bar in the desktop app guide](desktop.md#tray--menu-bar)).
For the macOS .app and the Linux AppImage, choosing the notice downloads, verifies and replaces the app and restarts it. One previous version is kept under a name with a `.prev` suffix (["Update" in the desktop app guide](desktop.md#update)).
The "Update now" button in the strip in the web UI does the same replacement (it works without a tray too).
Check "Install updates automatically" in the tray and the app goes through the same steps by itself when it finds a new version (it is not checked by default, and it appears only for the macOS .app and the Linux AppImage).
On Windows, and whenever the tray cannot replace the app, choose the notice or open the [releases page](https://github.com/howashoji/looptrack/releases), and download the file for your OS.
To see your version, run `looptrack version` with the `looptrack` inside the app ([Checking the update](#checking-the-update) below).

To replace it by hand, first choose "Quit" in the tray menu, replace the app as below, and start it again.

| OS | How to replace it |
| -- | -- |
| macOS | Open the new dmg, drag `Looptrack` onto `Applications`, and choose "Replace" |
| Windows (installer) | Run the new installer. It installs over the old version and keeps your options. If Looptrack is still running, the installer stops it first |
| Windows (zip) | Extract the new zip over the same folder as before |
| Linux | Put the new AppImage in place of the old one. You can keep the old file name |

After the replacement, the following happens automatically:

- **Your data is kept.** It lives separately from the app and is upgraded to the new format the first time the new version starts.
  Once it has been upgraded, putting the previous version of the app back does not start: it reports that the DB was migrated by a newer looptrack (a version from before this check was added does not stop and runs on the data anyway, which is all the more reason not to go back that way).
  Before it changes the format, the new version copies the database into the `backups` folder of the data folder by itself (it keeps the two newest; if it cannot make the copy, it does not change the format and does not start).
  To go back to the previous version, put the previous app back and replace `looptrack.db` with the newest copy in `backups` ([Update in Desktop app](desktop.md#update)).
- The "Start at login" registration is brought in line with the app's current location on the next start. So are the Linux app list entry (`~/.local/share/applications/looptrack.desktop`) and its icon.
- The CLI location is fixed too. On macOS and Linux the link keeps pointing at the app, and on Windows the CLI copy is refreshed on the next start.
  A copy you updated yourself (with `self-update`, for example) is left alone.

Where the data lives, and the steps in more detail, are in ["Update" in the desktop app guide](desktop.md#update).

## Server

By default the server is not updated automatically. An administrator replaces it (you are told about new versions; see "New-version notices and automatic replacement" below).
A team server does not migrate its database when it starts, so run the migration together with the replacement.
Every procedure below goes in the same order: stop → back up the database → replace the binary → migrate → start.

### A server installed with install.sh

```bash
curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh | sudo sh -s -- --upgrade
```

Without `--from`, the new version is the newest release on GitHub Releases (add `--version <version>` to pick one).
To take it from somewhere else, add `--from <source>` (a local directory of `deploy/release/dist.sh build` output, or a URL prefix in the same form).
Servers installed with the `install.sh` of 1.0.0-rc.1 or rc.2 upgrade with the same line: the installer reads the `/etc/looptrack/install.conf`, `.env` and unit (or `compose.yaml`) they left as they are.
`--upgrade` goes through these steps:

1. Downloads the new version (`looptrack_<version>_linux_<arch>_server.tar.gz` from GitHub Releases) and checks its SHA-256 before unpacking it. If the server has `minisign`, it checks the signature too.
2. Stops the service.
3. With SQLite, copies the database to `backup-<date and time>/` while it is stopped. Back up MySQL yourself (`mysqldump` or similar).
4. Replaces the binary. The previous version stays at `/usr/local/bin/looptrack.prev`. With compose it builds a new image and keeps the previous version's image.
5. Runs the migration.
6. Starts the service and waits for `/healthz` to respond.

If the version is the same, it changes nothing.
With MySQL and the minimum grants, a version that adds tables cannot be read until the application user is granted access to them after the migration, so `--upgrade` asks for administrative MySQL credentials on the terminal at that point (not shown, not stored), grants them again, and then starts the service.
For details, see ["Upgrading (--upgrade)" in DEPLOY.md](../server/DEPLOY.md), written for operators.

### New-version notices and automatic replacement

The server checks GitHub Releases at startup and every 24 hours, and when a new version is out it tells you in three places:

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

When it is on, once a day it replaces the server with a new version, using the same steps as `--upgrade` above. What runs is the copy of `install.sh` placed on the server when you turned it on; it is never downloaded again on its own (the copy is refreshed when an administrator runs `install.sh`).
It downloads only the new version's archive, always checks the signature (the server needs `minisign`), and never moves to an older version.
If something fails partway, it goes back to the previous version and starts it again, so the service is never left stopped.
Container images (compose) are not replaced automatically: when you are told about a new version, run `--upgrade`.
With MySQL, a new version that changes the shape of the database (one with migrations to run) is not replaced automatically; the server keeps running the current version. Run `--upgrade` on a terminal in that case.
For details, see ["New-version notices and automatic replacement" in DEPLOY.md](../server/DEPLOY.md).

### A server set up with looptrack setup alone

A server set up with `looptrack setup`, without `install.sh`, has no all-in-one procedure like `--upgrade`.
Go through the same order by hand. With compose (the `setup` default), run this in the directory where you ran `setup`:

```bash
docker compose stop
# with SQLite, back up ./data; with MySQL, back up with mysqldump or similar
# replace the looptrack in this directory with the new linux looptrack
# replace NOTICE with the new looptrack's output as well (./looptrack licenses > NOTICE)
docker compose build
docker compose run --rm --no-deps looptrack migrate
docker compose up -d
```

The order is the same when it runs under systemd (`--service systemd`).
Stop the service, replace the binary, run `looptrack migrate` with the settings from `.env` loaded, and then start it.

### The looptrack you distribute to users

Replacing the server does not update the `looptrack` on users' machines.
`self-update` and "[Update the distributed files]" use the `looptrack` placed in the server's **distribution directory**.
The distribution directory is set with `LOOPTRACK_DIST_DIR` in `.env`. Without it, the server does not distribute `looptrack`.

To distribute a new version, the administrator replaces the contents of the distribution directory:

1. Put `looptrack_<version>_<OS>_<CPU>` for each OS (with `.exe` at the end for Windows).
   GitHub Releases ships archives, not bare binaries: take the `looptrack` out of each `looptrack_<version>_<OS>_<CPU>_server.tar.gz` (`.zip` for Windows) and put it under the name above.
   Do not put the archives themselves in the distribution directory (the server distributes only the bare binaries).
2. Then put `SHA256SUMS`. For official releases, put the release's `SHA256SUMS` and `SHA256SUMS.minisig` as they are: an official build's `self-update` replaces nothing without the signature.
   The official `SHA256SUMS` also lists the binary inside each archive under the name `looptrack_<version>_<OS>_<CPU>[.exe]`, so the binaries you took out are checked against the signed list.
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

The server distributes the newest version for each OS and CPU pair. Files not listed in `SHA256SUMS`, or whose hash differs, are not distributed.
Writing `LOOPTRACK_CLIENT_MIN_VERSION=v…` in `.env` adds an "older than the oldest version this server supports" notice for any older `looptrack`.
The details of the layout are in ["Distributing from the server's distribution directory" in RELEASE.md](../server/RELEASE.md).

## Checking for new versions (desktop app and server)

The desktop app and the server (`looptrack serve`) check the list of releases on GitHub at startup and every 24 hours.
The CLI (`looptrack`) does not. The only notice the CLI gets is "[Update the distributed files]", added by the server.

- You are told only about a version whose `SHA256SUMS` signature checks out and whose release has the file for your OS and CPU (the dmg, AppImage or zip for the desktop app; the server archive for the server).
- It follows the same kind of version as the one you run: if you run an rc, you are told about rcs too; if you run a stable release, only about stable releases.
- When a check fails (offline, say), the notice for a version it told you about before stays.
- A version you built yourself (`dev` and the like, which cannot be compared with released versions) is not checked.

You can change this with the environment variables below. For the desktop app, pass them in the environment the app starts in; for the server, write them in `.env` and restart it.
With a value it does not understand, it does not check (so that it never connects with a setting it has misread).

| Environment variable | What it does |
| -- | -- |
| `LOOPTRACK_UPDATE_CHECK=off` | Does not check (no connection to GitHub). For the desktop app, clearing "Check for updates" in the tray does the same; if either one is off, it does not check |
| `LOOPTRACK_UPDATE_CHANNEL` | Sets what to follow: `stable` (stable releases only) or `prerelease` (rcs too). Without it, the version you run decides |
| `LOOPTRACK_UPDATE_URL` | Checks somewhere else instead. Only an `https://` URL that returns JSON in the same form as the GitHub API is accepted |

## Checking the update

```bash
looptrack version                # your version (also shows headless or desktop, and OS/CPU)
looptrack self-update --check    # compare with the newest version the server distributes (replaces nothing)
looptrack doctor                 # besides PATH and wiring, compares your version with the newest distributed one (with an admin token, also shows a new version of the server)
```

For the desktop app, run `version` with the `looptrack` inside the app (`Looptrack.app/Contents/MacOS/looptrack`, the AppImage file, or `cli\looptrack.exe` on Windows).
For the server, run `/usr/local/bin/looptrack version` on the server (when it was installed with `install.sh`).

## When something goes wrong

| Symptom | What to do |
| -- | -- |
| `self-update` stops with "The server has no looptrack for …" | The server does not distribute `looptrack` for your OS and CPU. Ask the administrator to put it in the distribution directory, or download the new version again as in step 1 of [Getting started](getting-started.md) |
| `self-update` stops with "No server URL" | Add `--url <server URL>`, or set the `LOOPTRACK_API_URL` environment variable |
| `self-update` says "The local version … cannot be compared with the distributed version …" | It is a version you built yourself. Add `--force` to replace it |
| `self-update` stops because there is no signature (`.minisig`) | The distribution directory has no `SHA256SUMS.minisig`. Ask the administrator to put it there |
| `self-update` fails in the desktop app | Replace the whole desktop app ([Desktop app](#desktop-app)) |
| `self-update` fails on the server and points you to the installer's `--upgrade` | The server was installed with `install.sh`. Update it with `curl -fsSL https://raw.githubusercontent.com/howashoji/looptrack/main/deploy/install.sh \| sudo sh -s -- --upgrade` |
| "[Update the distributed files]" is still there after updating | It stays until the next session start reports again. If it says the kit is out of date, run `looptrack issue init` too |
| The migration in `install.sh --upgrade` failed | With systemd the previous binary is at `/usr/local/bin/looptrack.prev`; with compose the previous version's image is kept. The error message tells you how to go back. If the migration applied even one file (there is an `Applied:` line in the output), also restore the DB from the backup taken before the update |
| `install.sh --auto-upgrade on` stops, saying it changed nothing | Automatic replacement cannot be set up on this server. It is not available for a compose server, a server installed with `--no-start`, or a server where systemd is not running. It also needs `minisign` (`apt-get install -y minisign`) and either `curl` or `wget` |
| Automatic replacement is on, but the server stays on the old version | `journalctl -u looptrack-upgrade` shows why. With MySQL, a new version that changes the shape of the database is not replaced automatically, so run `--upgrade` on a terminal. When a run fails partway, the server has been put back on the previous version and is running |
| After going back to the previous version, it does not start and says the DB was migrated by a newer looptrack | The newer version has already upgraded the DB. The previous version does not use it, so that it cannot damage it by writing. Go back to the newer version, or, to use the previous version, restore the DB (for the desktop app, the data folder) from a backup taken before the newer version migrated it (the desktop app keeps one in the `backups` folder of the data folder) |
