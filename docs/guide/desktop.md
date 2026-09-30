# Desktop app

[Guide contents](README.md) · Related: [Getting started](getting-started.md) · [FAQ / Troubleshooting](faq.md)

The desktop app is the easiest way to use Looptrack on your own, on one machine. You don't need a terminal.
Double-click the icon. It starts a local server (listening on 127.0.0.1 only, with your data in a single SQLite file) and opens the web UI in your default browser.
The first time, the browser shows the first-run setup, where you choose the administrator, two-factor auth and the first project.
From the tray icon (the menu bar on macOS) you can open the UI again, open settings, copy the MCP settings for your agent, make the CLI available, start at login, and quit.
Windows gets an installer. It adds Looptrack to the Start menu, and you can remove it from the list of installed apps.

You don't need administrator rights on any OS.
For a server shared by several people, use `looptrack setup` instead ([Getting started](getting-started.md)).

## Download

From the releases page (https://github.com/howashoji/looptrack/releases ), download the file for your OS and `SHA256SUMS`.

| OS | File | What is inside |
| -- | -- | -- |
| macOS 13 or later (Apple silicon and Intel) | `Looptrack_<version>_macos_universal.dmg` | `Looptrack.app` |
| Windows 10 / 11 | `Looptrack_<version>_windows_amd64_setup.exe` (`arm64` for Arm PCs) | The installer (recommended) |
| Windows 10 / 11, without installing | `Looptrack_<version>_windows_amd64.zip` (`arm64` for Arm PCs) | `Looptrack\Looptrack.exe` (the app) and `Looptrack\cli\looptrack.exe` (the CLI) |
| Linux (x86_64 / aarch64) | `Looptrack_<version>_linux_x86_64.AppImage` (`aarch64` for Arm) | The app as a single file |

Then check the download against `SHA256SUMS` (`shasum -a 256 -c SHA256SUMS --ignore-missing` on macOS, `sha256sum -c SHA256SUMS --ignore-missing` on Linux, `Get-FileHash` on Windows).

## First launch

### macOS

Open the dmg and drag `Looptrack` onto `Applications`, then double-click `Looptrack` in `Applications`.
It's signed and notarized, so macOS won't warn you.
There's no Dock icon. Look for the loop icon in the menu bar.

### Windows

Double-click `Looptrack_<version>_windows_amd64_setup.exe`.
The installer isn't code-signed yet, so SmartScreen may say "Windows protected your PC". If it does, choose "More info" → "Run anyway".
No administrator rights needed. It installs into `%LOCALAPPDATA%\Programs\Looptrack Desktop` for you only, and adds Looptrack to the Start menu.
You'll be offered two options. Both can be changed later from the tray menu:

- Start Looptrack when you log in
- Make the looptrack CLI available (see [Use the CLI](#use-the-cli))

When the installer finishes, leave "Launch Looptrack" checked, or start Looptrack from the Start menu.
The icon shows up in the notification area. But Windows 11 tucks new icons under the "^" (hidden icons) button.
Click "^" and drag the Looptrack icon onto the taskbar to keep it visible. You can also turn it on in Settings → Personalization → Taskbar → Other system tray icons.
Left-click the icon for the menu.

Rather not install anything? Use the zip. Extract it to a folder in your user profile (for example `%USERPROFILE%\Apps`, which gives `%USERPROFILE%\Apps\Looptrack\Looptrack.exe`) and double-click `Looptrack.exe`.
Just don't extract it into `%LOCALAPPDATA%\Programs\looptrack`. That folder is where the CLI goes.

### Linux

Put the AppImage somewhere it'll stay (for example `~/Applications`), make it executable, and double-click it. Running it from a terminal works too.
It doesn't need FUSE 2. It does need `fusermount3`, which a normal desktop install already has (on Ubuntu: `sudo apt install fuse3`).
If you can't install it, start the app with `APPIMAGE_EXTRACT_AND_RUN=1 ./Looptrack_<version>_linux_x86_64.AppImage`. Or unpack it with `./Looptrack_<version>_linux_x86_64.AppImage --appimage-extract` and run `squashfs-root/usr/bin/looptrack`.
The tray icon needs a desktop that shows StatusNotifierItem icons (KDE, Xfce, Cinnamon, …). On GNOME, install the "AppIndicator and KStatusNotifierItem Support" extension.
The app still runs without a tray. Stop it with `looptrack desktop --quit` (see [Troubleshooting](#troubleshooting)).
On the first start, the app adds Looptrack to the app list (the launcher and the activities search). It puts `~/.local/share/applications/looptrack.desktop` and an icon (`~/.local/share/icons/hicolor/256x256/apps/looptrack.png`) in place, so you can start it from there afterwards.
Moved the AppImage? The entry follows the new location the next time you start that AppImage. If you don't want the entry, clear "Show in the app list" in the tray (it then stays off on later starts too).

Double-clicking again while the app is running won't start a second copy. It just opens the UI in the browser.

## Get started without the CLI

Finishing setup and connecting an AI agent both happen through the app's own screens and a chat with your agent.
You never need to open a terminal or type a `looptrack` command yourself.

### Finish setup in the browser

The first time, the browser shows a one-time setup form instead of the normal screen. It's the same whether you got there by double-clicking the app, choosing "Open the app" in the tray, or opening the URL `looptrack desktop --status` prints.

| Section | What to enter |
| -- | -- |
| Administrator | Login (letters, digits, `.` `_` `-`), display name (optional), a password (12+ characters, entered twice) |
| Two-factor auth | Required or optional. Local mode skips sign-in either way, so this only matters if you later point other people at this same server |
| First project | Slug, ID prefix, and name — all optional. Leave them blank and create one later, from `/admin/projects` or by asking your agent |

Submit the form. The next page shows a link to your project (if you created one) and the MCP connection settings for Claude Code, Codex and GitHub Copilot. Those are the same settings "Copy AI connection settings" puts on your clipboard.
The form appears only while no administrator exists yet. Once you've finished it, the app goes straight to the normal screen from then on.
You can still open the connection settings any time, from the tray's "Open the connection settings page".

### Connect your AI agent by chat

1. Open your agent (Claude Code, Codex, GitHub Copilot, …) in the repository you want to track issues for.
2. From the tray menu, choose "Copy AI connection settings". You can also choose "Open the connection settings page" and copy the same thing from the browser.
3. Paste it into a prompt along with what you want. For example, write "Add this MCP connection, then set up issue management for this repository" and paste the copied block below that sentence.
4. Approve what the agent proposes as it goes: adding the MCP connection, then the install command it gets back from the `setup` tool. [Agent-specific notes](ai-agents.md) walks through exactly what happens at each step, under "Installing through MCP only". You won't type any of it yourself. (Signing in only comes up if you point the connection at a server other people share, not at this local app.)
5. Restart the agent when it asks you to, and approve its hooks.

From here on, it's all prompts.
Start by asking the agent to file an issue, or to take the next one and run a full loop.

## Tray / menu bar

| Item | What it does |
| -- | -- |
| Update to version <version> | Appears at the top of the menu only while a new version is out. Choosing it downloads and verifies the new version, replaces the app and restarts it (the macOS .app and the Linux AppImage; see [Update](#update)). Where the app cannot be replaced (Windows and the like) it reads "New version <version> available" instead, and choosing it opens that version's release page in the browser |
| Open the app | Opens the web UI (`http://127.0.0.1:18090/looptrack/` by default) |
| Settings | Opens the account settings page (`/account`) in the browser |
| Copy AI connection settings | Copies the MCP settings for Claude Code, Codex, or GitHub Copilot to the clipboard, ready to paste. "Open the connection settings page" shows them all in the browser |
| Make the CLI available | Makes `looptrack` callable from terminals and agents (see [Use the CLI](#use-the-cli)) |
| Show in the app list | Shown only for the Linux AppImage. While checked, you can start the app from the launcher and the activities search (it is added on the first start; clearing it removes the entry and keeps it off on later starts) |
| Start at login | When checked, the app starts in the background when you log in (it does not open the browser then) |
| Check for updates | While checked, the app looks for a new version in the GitHub release list at startup and every 24 hours. Clear it to stop checking; the app then contacts nothing (checked by default) |
| Install updates automatically | When checked, the app replaces itself and restarts as soon as it finds a new version (unchecked by default). Shown only for the macOS .app and the Linux AppImage |
| Quit | Stops the server and the app |

On every OS, left-click the tray icon to get the menu.

When a new version is out, the top of the menu and a strip at the top of the web UI say so. There's no OS notification.
The app follows the same kind of version you run (run an rc, and it tells you about rcs too). It only tells you about a version whose signature it could verify.
On macOS and Linux, choosing the top of the menu is all it takes to replace the app ([Update](#update)). On Windows, download the new version from the release page and replace the app.
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

The `backups` folder inside the data folder holds copies of `looptrack.db` (`looptrack.db.20260926T010203Z`; the time is in UTC). The app takes them by itself, before a new version changes the database format.
It takes one only when the new version actually has changes to apply to an existing database. It keeps the two newest and deletes older ones.
Like the database, only you can read them. They're what you use to go back to the previous version ([Update](#update)).

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

## Start at login

Check "Start at login" in the tray menu. That's it.
It registers the app without administrator rights: a LaunchAgent in `~/Library/LaunchAgents` on macOS, a `looptrack.desktop` file in `~/.config/autostart` on Linux, and the `Looptrack` value under `HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run` on Windows.
Uncheck it, and the registration goes away.
If you move the app, the registration follows it the next time you start the app.

## Update

The macOS .app and the Linux AppImage are replaced from the top of the tray menu: "Update to version <version>".
With "Install updates automatically" checked, the app does the same thing by itself as soon as it finds a new version.

1. It downloads the new version's file and compares it with the SHA-256 in `SHA256SUMS`, whose signature it has verified. If they differ, nothing is replaced.
2. On macOS, it checks the signatures of the dmg and of the `Looptrack.app` inside it with `spctl` and `codesign`. It also checks that the identifier and the signing team match the running app.
3. It renames the current app with a `.prev` suffix (`Looptrack.app.prev`, `<AppImage file name>.prev`) and puts the new version in place under the original name. An older `.prev` is deleted.
4. It restarts the app. The new version waits for the old one to stop, then starts (without opening the browser).

If a step fails, the current version keeps running and the app tells you why. If the new version can't start, the previous one is put back.
What if the app's location (`/Applications` and the like) isn't writable? Then nothing is replaced. On macOS the verified dmg is opened so you can drag `Looptrack` onto `Applications`; on Linux the release page is opened.
To go back after a replacement, choose "Quit" in the tray, delete the current app, and rename the `.prev` back to the original name (restore the database as in the steps below).

On Windows, and whenever the tray cannot replace the app, replace it by hand:

1. Choose "Quit" in the tray menu.
2. Replace the app with the new version:
   - macOS: open the new dmg and drag `Looptrack` onto `Applications` (choose "Replace").
   - Windows: run the new installer; it replaces the old version in place and keeps your options. If Looptrack is still running, the installer stops it first. If you use the zip, extract the new zip over the old folder.
   - Linux: put the new AppImage in place of the old one (you can keep the old file name; a replacement from the tray keeps it too).
3. Start the app again.

Your data stays in the data folder above. It's upgraded automatically on the first start.
After that, putting the previous version of the app back won't work: it doesn't start, and reports that the DB was migrated by a newer looptrack. (A version from before this check was added doesn't stop and runs on the data anyway. All the more reason not to go back that way.)
Before changing the format, the new version copies `looptrack.db` into the `backups` folder of the data folder ([Where your data lives](#where-your-data-lives)). If it can't make the copy, it leaves the format alone, doesn't start, and tells you why.
Here's how to go back to the previous version:

1. Choose "Quit" in the tray menu.
2. Put the previous version of the app back.
3. In the data folder, replace `looptrack.db` with the newest file in `backups` (the name with the latest time). Delete `looptrack.db-wal` and `looptrack.db-shm` if they are there.
4. Start the app.

Anything you changed after the backup was taken isn't in the restored database.
Leave the key file (`looptrack.db.secret-key`) as it is.
If the update didn't change the format, no backup is taken. The previous version can use the database as it is.
The desktop app doesn't use `looptrack self-update`. Update the whole app instead.
The CLI link (macOS / Linux) keeps pointing at the app, and the Windows CLI copy is refreshed on the next start.
You hear about a new version in the tray menu and a strip in the web UI ([Tray / menu bar](#tray--menu-bar)). [Updating](updating.md) sums up what's automatic and what's manual.

## Uninstall

First choose "Quit" in the tray menu, and uncheck "Start at login" if it's checked.
Then remove the app, the CLI and the CLI's credentials. Remove your data too, but only if you no longer need it.

### macOS

```bash
rm -rf /Applications/Looptrack.app /Applications/Looptrack.app.prev
rm -f ~/Library/LaunchAgents/*looptrack*.plist        # only if "Start at login" was left on
[ -L ~/.local/bin/looptrack ] && rm ~/.local/bin/looptrack
# The CLI's credentials (access tokens for the servers you signed in to; keep it if you still use the CLI elsewhere):
rm -rf ~/.config/looptrack
# Your data and logs — this deletes all issues:
rm -rf ~/Library/Application\ Support/Looptrack ~/Library/Logs/Looptrack
```

### Windows

If you used the installer, open Settings → Apps → Installed apps, find Looptrack, and choose Uninstall.
That removes the app, the Start menu entry, the "start at login" registration, and the CLI copy the installer made.
Your data is kept on purpose. Reinstall, and all your issues come back.

If you used the zip:

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\Apps\Looptrack"          # the extracted app (use your folder)
Remove-ItemProperty -Path HKCU:\Software\Microsoft\Windows\CurrentVersion\Run -Name Looptrack -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\looptrack"      # the CLI copy (only if "Make the CLI available" made it)
```

The CLI's credentials and your data stay either way. To delete them:

```powershell
Remove-Item -Recurse -Force "$env:APPDATA\looptrack"        # the CLI's credentials (keep it if you still use the CLI elsewhere)
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Looptrack"   # your data — this deletes all issues
```

### Linux

```bash
rm -f ~/Applications/Looptrack_*.AppImage ~/Applications/Looptrack_*.AppImage.prev   # use your location
rm -f ~/.config/autostart/looptrack.desktop
rm -f ~/.local/share/applications/looptrack.desktop ~/.local/share/icons/hicolor/256x256/apps/looptrack.png   # the app list entry
[ -L ~/.local/bin/looptrack ] && rm ~/.local/bin/looptrack
# The CLI's credentials (access tokens for the servers you signed in to; keep it if you still use the CLI elsewhere):
rm -rf ~/.config/looptrack
# Your data and logs — this deletes all issues:
rm -rf ~/.local/share/looptrack ~/.local/state/looptrack
```

## Troubleshooting

| Symptom | What to do |
| -- | -- |
| Nothing seems to happen on double-click | The app may already be running without a visible tray icon. Open `http://127.0.0.1:18090/looptrack/`, or check the log in the log folder above |
| No tray icon on Windows 11 | It is under the "^" (hidden icons) button next to the clock. Click "^" and drag the Looptrack icon onto the taskbar, or turn it on in Settings → Personalization → Taskbar → Other system tray icons |
| SmartScreen blocks the installer | The installer is not code-signed yet. Choose "More info" → "Run anyway", after checking the download against `SHA256SUMS` |
| The AppImage fails to start with `No suitable fusermount binary found on the $PATH` | FUSE 3 is missing. Install it (`sudo apt install fuse3` on Ubuntu; the equivalent package elsewhere). If you cannot, start it with `APPIMAGE_EXTRACT_AND_RUN=1`, or unpack it with `--appimage-extract` and run `squashfs-root/usr/bin/looptrack` |
| No tray icon on Linux | Install a StatusNotifierItem host (GNOME: the AppIndicator extension). Stop the app from a terminal with the command below |
| The agent's MCP connection stopped working after a restart | Another program took the port, so the app switched to a free one (the log says so). Copy the MCP settings again |
| The browser does not open | Open the URL from `looptrack desktop --status` yourself |

From a terminal, use these. Here `looptrack` can also be the AppImage file, `Looptrack.app/Contents/MacOS/looptrack`, or `cli\looptrack.exe` on Windows:

```bash
looptrack desktop --status   # prints the URL if the app is running
looptrack desktop --quit     # stops the running app
```

`--quit` first asks the app to stop, so it finishes writing its data before it exits.
It asks with SIGTERM on macOS and Linux, and with a quit signal object on Windows. This works the same way whether or not the tray is shown.
What if the app doesn't stop when asked? Only Windows goes on to force it, and it prints why. macOS and Linux have no force stop.
