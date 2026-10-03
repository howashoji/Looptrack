# Using the desktop app

[Guide contents](../README.md) · [Desktop app](README.md) · Previous: [Getting started with the desktop app](getting-started.md) · Next: [Updating the desktop app](updating.md)

This page covers the tray menu, where the app keeps your data, the CLI that comes with the app, and starting at login.
Setup and connecting your agent are in [Getting started with the desktop app](getting-started.md).

## Tray / menu bar

| Item | What it does |
| -- | -- |
| Update to version <version> | Appears at the top of the menu only while a new version is out. Choosing it downloads and verifies the new version, replaces the app and restarts it (the macOS .app, the Linux AppImage and Windows; see [Update](updating.md#update)). Where the app cannot be replaced (when it runs from somewhere other than the app, say) it reads "New version <version> available" instead, and choosing it opens that version's release page in the browser |
| Open the app | Opens the web UI (`http://127.0.0.1:18090/looptrack/` by default) |
| Settings | Opens the account settings page (`/account`) in the browser |
| Copy AI connection settings | Copies the MCP settings for Claude Code, Codex, or GitHub Copilot to the clipboard, ready to paste. "Open the connection settings page" shows them all in the browser |
| Make the CLI available | Makes `looptrack` callable from terminals and agents (see [Use the CLI](#use-the-cli)) |
| Show in the app list | Shown only for the Linux AppImage. While checked, you can start the app from the launcher and the activities search (it is added on the first start; clearing it removes the entry and keeps it off on later starts) |
| Start at login | When checked, the app starts in the background when you log in (it does not open the browser then) |
| Check for updates | While checked, the app looks for a new version in the GitHub release list at startup and every 24 hours. Clear it to stop checking; the app then contacts nothing (checked by default) |
| Install updates automatically | When checked, the app replaces itself and restarts as soon as it finds a new version (unchecked by default). Shown only for the macOS .app, the Linux AppImage and Windows `Looptrack.exe` |
| Quit | Stops the server and the app |

On every OS, left-click the tray icon to get the menu.

When a new version is out, the top of the menu and a strip at the top of the web UI say so. There's no OS notification.
The app follows the same kind of version you run (run an rc, and it tells you about rcs too). It only tells you about a version whose signature it could verify.
Choosing the top of the menu is all it takes to replace the app, on Windows too ([Update](updating.md#update)).
The "Update now" button in the strip in the web UI does the same replacement (it works without a tray too, as with `--no-tray`). While the replacement runs, and if it fails, the strip says so.
When a check fails (offline, for example), the notice about the version found earlier stays.
Start the app with the environment variable `LOOPTRACK_UPDATE_CHECK=off`, and it won't check, whatever the menu says. Without a tray (`--no-tray` and the like), the strip in the web UI points you to this environment variable instead.

The port stays the same between launches. So the MCP settings you copied keep working.
If another program already has the port, though, the app picks a free one and remembers it. Copy the MCP settings again when that happens.

## Where your data lives

The app and your data live apart. So replacing the app keeps your data.

| OS | Data (database, key, state) | Logs |
| -- | -- | -- |
| macOS | `~/Library/Application Support/Looptrack` | `~/Library/Logs/Looptrack` |
| Windows | `%LOCALAPPDATA%\Looptrack` | `%LOCALAPPDATA%\Looptrack\logs` |
| Linux | `~/.local/share/looptrack` (`$XDG_DATA_HOME`) | `~/.local/state/looptrack` (`$XDG_STATE_HOME`) |

The data folder holds `looptrack.db` (all issues, users, and settings) and `looptrack.db.secret-key` (the key that encrypts two-factor secrets).
Back up both together. Without the key, two-factor registrations can't be used.
Only you can read them.

The files you attach to issues go in the `attachments` folder inside the data folder ([Daily use](../daily-use.md#attachments-evidence)).
The database only holds their names and sizes, so copy the `attachments` folder along with the rest when you back up.

The `backups` folder inside the data folder holds copies of `looptrack.db` (`looptrack.db.20260926T010203Z`; the time is in UTC). The app takes them by itself, before a new version changes the database format.
It takes one only when the new version actually has changes to apply to an existing database. It keeps the two newest and deletes older ones.
Like the database, only you can read them. They're what you use to go back to the previous version ([Update](updating.md#update)).

## Use the CLI

"Make the CLI available" puts the CLI somewhere that needs no administrator rights:

| OS | Location |
| -- | -- |
| macOS / Linux | `~/.local/bin/looptrack` (a link to the `looptrack` inside the app) |
| Windows | `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe` (a copy of the `cli\looptrack.exe` that comes with the app; refreshed when you update the app) |

If that folder isn't on your `PATH`, the app tells you how to add it.
A `looptrack` you already installed another way is left as it is.
For the server URL, use the one `looptrack desktop --status` shows, minus the trailing slash (`http://127.0.0.1:18090/looptrack`).
That server exists only on your own PC. Just like the UI and the agent's MCP connection, it needs no sign-in and no access token.

```bash
LOOPTRACK_API_URL=http://127.0.0.1:18090/looptrack LOOPTRACK_PROJECT=main looptrack issue list
```

You sign in only when you point the same CLI at a server your team shares. Then do it once, with `looptrack issue login --browser --url <that server's URL>`.
The commands work the same as in the server edition, and [Daily use](../daily-use.md) uses them throughout.
The CLI follows the app when you update it ([What happens after the replacement](updating.md#what-happens-after-the-replacement)). The exception is a Windows copy you replaced with `self-update` ([Updating only the CLI with self-update](updating.md#updating-only-the-cli-with-self-update)).

## Start at login

Check "Start at login" in the tray menu. That's it.
It registers the app without administrator rights: a LaunchAgent in `~/Library/LaunchAgents` on macOS, a `looptrack.desktop` file in `~/.config/autostart` on Linux, and the `Looptrack` value under `HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run` on Windows.
Uncheck it, and the registration goes away.
If you move the app, the registration follows it the next time you start the app.
