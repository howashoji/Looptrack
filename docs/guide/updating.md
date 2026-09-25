# Updating

[Guide contents](README.md) · Related: [Getting started](getting-started.md) · [Desktop app](desktop.md) · [FAQ / Troubleshooting](faq.md)

The CLI on your machine, the desktop app, and the server are each updated differently.
**None of them replaces itself with a new version automatically.** A user or an administrator does the replacing.
What does happen automatically is telling you about a new version (the CLI only) and tidying up after you replace something.

## What is automatic and what is manual

| Target | What happens automatically | What you do |
| -- | -- | -- |
| CLI (`looptrack`) | When your version is out of date, "[Update the distributed files]" is added at the start of an agent session and to MCP tool results — only when the server distributes `looptrack` or sets a minimum supported version | Replace it with `looptrack self-update`, then run `looptrack issue init` again in each project |
| Desktop app | The first start of a new version upgrades your data. The start-at-login registration and the CLI location are also brought in line with the current app | Noticing that a new version exists, and replacing the whole app. Nothing tells you about a new version |
| Server | Nothing | An administrator replaces it with `install.sh --upgrade` or similar. The administrator also puts the new `looptrack` for users into the distribution directory |

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
If anything does not match, nothing is replaced.
On Windows the running file cannot be deleted, so the old file is renamed to `looptrack.exe.old` before the new one is put in place. The `.old` file is removed by the next `self-update`.

If your version is the same as or newer than the distributed one, it does nothing.
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

The desktop app does not use `self-update`; you replace the whole app. Nothing tells you about a new version.
Check the [releases page](https://github.com/howashoji/looptrack/releases) for a new version and download the file for your OS.
To see your version, run `looptrack version` with the `looptrack` inside the app ([Checking the update](#checking-the-update) below).

First choose "Quit" in the tray menu, replace the app as below, and start it again.

| OS | How to replace it |
| -- | -- |
| macOS | Open the new dmg, drag `Looptrack` onto `Applications`, and choose "Replace" |
| Windows (installer) | Run the new installer. It installs over the old version and keeps your options. If Looptrack is still running, the installer stops it first |
| Windows (zip) | Extract the new zip over the same folder as before |
| Linux | Put the new AppImage in place of the old one. You can keep the old file name |

After the replacement, the following happens automatically:

- **Your data is kept.** It lives separately from the app and is upgraded to the new format the first time the new version starts.
- The "Start at login" registration is brought in line with the app's current location on the next start.
- The CLI location is fixed too. On macOS and Linux the link keeps pointing at the app, and on Windows the CLI copy is refreshed on the next start.
  A copy you updated yourself (with `self-update`, for example) is left alone.

Where the data lives, and the steps in more detail, are in ["Update" in the desktop app guide](desktop.md#update).

## Server

The server is not updated automatically. An administrator replaces it.
A team server does not migrate its database when it starts, so run the migration together with the replacement.
Every procedure below goes in the same order: stop → back up the database → replace the binary → migrate → start.

### A server installed with install.sh

```bash
sudo sh install.sh --upgrade --from <source>
```

`--from` is where the new version comes from, in the same form as when you installed (`…/releases/download/<version>` on GitHub Releases, or a local directory of release files).
`--upgrade` goes through these steps:

1. Downloads the new version and checks its SHA-256. If the server has `minisign`, it checks the signature too.
2. Stops the service.
3. With SQLite, copies the database to `backup-<date and time>/` while it is stopped. Back up MySQL yourself (`mysqldump` or similar).
4. Replaces the binary. The previous version stays at `/usr/local/bin/looptrack.prev`. With compose it builds a new image and keeps the previous version's image.
5. Runs the migration.
6. Starts the service and waits for `/healthz` to respond.

If the version is the same, it changes nothing.
With MySQL, a version that adds tables cannot be read until the application user's grants (`grants.sql`) are applied again after the migration, so `--upgrade` stops there and tells you the order.
For details, see ["Upgrading (--upgrade)" in DEPLOY.md](../server/DEPLOY.md), written for operators.

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
2. Then put `SHA256SUMS`. For official releases, put `SHA256SUMS.minisig` too: an official build's `self-update` replaces nothing without the signature.
3. Remove the previous version's files.

The server distributes the newest version for each OS and CPU pair. Files not listed in `SHA256SUMS`, or whose hash differs, are not distributed.
Writing `LOOPTRACK_CLIENT_MIN_VERSION=v…` in `.env` adds an "older than the oldest version this server supports" notice for any older `looptrack`.
The details of the layout are in ["Distributing from the server's distribution directory" in RELEASE.md](../server/RELEASE.md).

## Checking the update

```bash
looptrack version                # your version (also shows headless or desktop, and OS/CPU)
looptrack self-update --check    # compare with the newest version the server distributes (replaces nothing)
looptrack doctor                 # besides PATH and wiring, compares your version with the newest distributed one
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
| `self-update` fails on the server and points you to `install.sh --upgrade` | The server was installed with `install.sh`. Update it with `sudo sh install.sh --upgrade --from <source>` |
| "[Update the distributed files]" is still there after updating | It stays until the next session start reports again. If it says the kit is out of date, run `looptrack issue init` too |
| The migration in `install.sh --upgrade` failed | With systemd the previous binary is at `/usr/local/bin/looptrack.prev`; with compose the previous version's image is kept. The error message tells you how to go back |
