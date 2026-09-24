# Desktop app

[Guide contents](README.md) · Related: [Getting started](getting-started.md) · [FAQ / Troubleshooting](faq.md)

The desktop app is the easiest way to use Looptrack by yourself on one machine, without opening a terminal.
Double-click the icon and it starts a local server (listening on 127.0.0.1 only, storing data in a single SQLite file) and opens the web UI in your default browser.
The first time, the browser shows the first-run setup (administrator, two-factor auth, first project).
A tray icon (menu bar on macOS) lets you open the UI again, copy the MCP settings for your agent, make the CLI available, start at login, and quit.
On Windows there is an installer: it adds Looptrack to the Start menu and you can remove it from the list of installed apps.

No administrator rights are needed on any OS.
For a server shared by several people, use `looptrack setup` instead ([Getting started](getting-started.md)).

## Download

From the releases page (https://github.com/howashoji/looptrack/releases ), download the file for your OS and `SHA256SUMS`.

| OS | File | What is inside |
| -- | -- | -- |
| macOS 13 or later (Apple silicon and Intel) | `Looptrack_<version>_macos_universal.dmg` | `Looptrack.app` |
| Windows 10 / 11 | `Looptrack_<version>_windows_amd64_setup.exe` (`arm64` for Arm PCs) | The installer (recommended) |
| Windows 10 / 11, without installing | `Looptrack_<version>_windows_amd64.zip` (`arm64` for Arm PCs) | `Looptrack\Looptrack.exe` (the app) and `Looptrack\cli\looptrack.exe` (the CLI) |
| Linux (x86_64 / aarch64) | `Looptrack_<version>_linux_x86_64.AppImage` (`aarch64` for Arm) | The app as a single file |

Check the download against `SHA256SUMS` (`shasum -a 256 -c SHA256SUMS --ignore-missing` on macOS, `sha256sum -c SHA256SUMS --ignore-missing` on Linux, `Get-FileHash` on Windows).

## First launch

### macOS

Open the dmg and drag `Looptrack` onto `Applications`, then double-click `Looptrack` in `Applications`.
The app is signed and notarized, so macOS does not show a warning.
It has no Dock icon; look for the loop icon in the menu bar.

### Windows

Double-click `Looptrack_<version>_windows_amd64_setup.exe`.
The installer is not code-signed yet, so SmartScreen may say "Windows protected your PC"; choose "More info" → "Run anyway".
No administrator rights are needed: it installs into `%LOCALAPPDATA%\Programs\Looptrack Desktop` for you only, and adds Looptrack to the Start menu.
Two options are offered; both can be changed later from the tray menu:

- **Start Looptrack when you log in**
- **Make the looptrack CLI available** (see [Use the CLI](#use-the-cli))

When the installer finishes, leave "Launch Looptrack" checked, or start Looptrack from the Start menu.
The icon appears in the notification area. Windows 11 hides new icons under the "^" (hidden icons) button: click "^", then drag the Looptrack icon onto the taskbar to keep it visible (or turn it on in Settings → Personalization → Taskbar → Other system tray icons).
Left-click the icon to open the UI; right-click it for the menu.

If you would rather not install anything, use the zip instead: extract it to a folder in your user profile (for example `%USERPROFILE%\Apps`, which gives `%USERPROFILE%\Apps\Looptrack\Looptrack.exe`) and double-click `Looptrack.exe`.
Do not extract it into `%LOCALAPPDATA%\Programs\looptrack`; that folder is where the CLI goes.

### Linux

Put the AppImage somewhere stable (for example `~/Applications`), make it executable, and double-click it (or run it from a terminal).
The AppImage does not need FUSE 2, but it does need `fusermount3` (on Ubuntu: `sudo apt install fuse3`; a normal desktop install already has it).
If you cannot install it, start the app with `APPIMAGE_EXTRACT_AND_RUN=1 ./Looptrack_<version>_linux_x86_64.AppImage`, or unpack it with `./Looptrack_<version>_linux_x86_64.AppImage --appimage-extract` and run `squashfs-root/usr/bin/looptrack`.
The tray icon needs a desktop that shows StatusNotifierItem icons (KDE, Xfce, Cinnamon, …; on GNOME install the "AppIndicator and KStatusNotifierItem Support" extension).
Without a tray the app still runs; stop it with `looptrack desktop --quit` (see [Troubleshooting](#troubleshooting)).

Double-clicking again while the app is running does not start a second copy; it just opens the UI in the browser.

## Tray / menu bar

| Item | What it does |
| -- | -- |
| Open the app | Opens the web UI (`http://127.0.0.1:18090/looptrack/` by default) |
| Copy AI connection settings | Copies the MCP settings for Claude Code, Codex, or GitHub Copilot to the clipboard, ready to paste. "Open the connection settings page" shows them all in the browser |
| Make the CLI available | Makes `looptrack` callable from terminals and agents (see [Use the CLI](#use-the-cli)) |
| Start at login | When checked, the app starts in the background when you log in (it does not open the browser then) |
| Quit | Stops the server and the app |

On Windows, left-clicking the tray icon opens the UI and right-clicking shows the menu.
On macOS and Linux, either button shows the menu.

The port stays the same between launches, so the MCP settings you copied keep working.
If another program is already using the port, the app picks a free one and remembers it; copy the MCP settings again in that case.

## Where your data lives

The app and your data are kept apart, so replacing the app keeps your data.

| OS | Data (database, key, state) | Logs |
| -- | -- | -- |
| macOS | `~/Library/Application Support/Looptrack` | `~/Library/Logs/Looptrack` |
| Windows | `%LOCALAPPDATA%\Looptrack` | `%LOCALAPPDATA%\Looptrack\logs` |
| Linux | `~/.local/share/looptrack` (`$XDG_DATA_HOME`) | `~/.local/state/looptrack` (`$XDG_STATE_HOME`) |

The data folder holds `looptrack.db` (all issues, users, and settings) and `looptrack.db.secret-key` (the key that encrypts two-factor secrets).
Back up both together; without the key, two-factor registrations cannot be used.
Only you can read them.

## Use the CLI

"Make the CLI available" puts the CLI in a place that needs no administrator rights:

| OS | Location |
| -- | -- |
| macOS / Linux | `~/.local/bin/looptrack` (a link to the `looptrack` inside the app) |
| Windows | `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe` (a copy of the `cli\looptrack.exe` that comes with the app; refreshed when you update the app) |

If that folder is not on your `PATH`, the app tells you how to add it.
An existing `looptrack` that you installed another way is left as it is.
For the server URL, use the one shown by `looptrack desktop --status`, without the trailing slash (`http://127.0.0.1:18090/looptrack`).
That server only exists on your own PC, so — just like the UI and the agent's MCP connection — **no sign-in and no access token are needed**.

```bash
LOOPTRACK_API_URL=http://127.0.0.1:18090/looptrack LOOPTRACK_PROJECT=main looptrack issue list
```

Only when you point the same CLI at a server your team shares do you sign in once, with `looptrack issue login --browser --url <that server's URL>`.

## Start at login

Check "Start at login" in the tray menu.
It registers the app without administrator rights: a LaunchAgent in `~/Library/LaunchAgents` on macOS, a `looptrack.desktop` file in `~/.config/autostart` on Linux, and the `Looptrack` value under `HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run` on Windows.
Uncheck it to remove the registration.
If you move the app, the registration follows it the next time you start the app.

## Update

1. Choose "Quit" in the tray menu.
2. Replace the app with the new version:
   - macOS: open the new dmg and drag `Looptrack` onto `Applications` (choose "Replace").
   - Windows: run the new installer; it replaces the old version in place and keeps your options. If Looptrack is still running, the installer stops it first. If you use the zip, extract the new zip over the old folder.
   - Linux: put the new AppImage in place of the old one (you can keep the old file name).
3. Start the app again.

Your data stays in the data folder above and is upgraded automatically on the first start.
The desktop app does not use `looptrack self-update`; update the whole app instead.
The CLI link (macOS / Linux) keeps pointing at the app, and the Windows CLI copy is refreshed on the next start.

## Uninstall

Choose "Quit" in the tray menu first, and uncheck "Start at login" if it is checked.
Then remove the app, the CLI, the CLI's credentials, and (only if you no longer need them) your data.

### macOS

```bash
rm -rf /Applications/Looptrack.app
rm -f ~/Library/LaunchAgents/*looptrack*.plist        # only if "Start at login" was left on
[ -L ~/.local/bin/looptrack ] && rm ~/.local/bin/looptrack
# The CLI's credentials (access tokens for the servers you signed in to; keep it if you still use the CLI elsewhere):
rm -rf ~/.config/looptrack
# Your data and logs — this deletes all issues:
rm -rf ~/Library/Application\ Support/Looptrack ~/Library/Logs/Looptrack
```

### Windows

If you used the installer, open Settings → Apps → Installed apps, find **Looptrack**, and choose Uninstall.
That removes the app, the Start menu entry, the "start at login" registration, and the CLI copy it made.
**Your data is kept on purpose**, so that reinstalling brings all your issues back.

If you used the zip:

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\Apps\Looptrack"          # the extracted app (use your folder)
Remove-ItemProperty -Path HKCU:\Software\Microsoft\Windows\CurrentVersion\Run -Name Looptrack -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\looptrack"      # the CLI copy (only if "Make the CLI available" made it)
```

The CLI's credentials and your data are kept either way. To delete them:

```powershell
Remove-Item -Recurse -Force "$env:APPDATA\looptrack"        # the CLI's credentials (keep it if you still use the CLI elsewhere)
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Looptrack"   # your data — this deletes all issues
```

### Linux

```bash
rm -f ~/Applications/Looptrack_*.AppImage                   # use your location
rm -f ~/.config/autostart/looptrack.desktop
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

From a terminal (the AppImage file, `Looptrack.app/Contents/MacOS/looptrack`, or `cli\looptrack.exe` on Windows works as `looptrack` here):

```bash
looptrack desktop --status   # prints the URL if the app is running
looptrack desktop --quit     # stops the running app
```

`--quit` first asks the app to stop, so it finishes writing its data before exiting (SIGTERM on macOS and
Linux, a quit signal object on Windows). This works the same way whether or not the tray is shown. Only when
the app does not stop after being asked does Windows force it, and it prints why (macOS and Linux have no
force stop).
