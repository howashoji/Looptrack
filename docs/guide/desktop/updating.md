# Updating the desktop app

[Guide contents](../README.md) · [Desktop app](README.md) · Previous: [Using the desktop app](using.md) · Next: [Desktop app troubleshooting](troubleshooting.md)

This page covers how the desktop app tells you about a new version, how the app is replaced, how to go back, and how to remove it.
You update the desktop app by replacing the whole app, and the CLI that comes with it follows along. `looptrack self-update` is not how the app is updated. The one place it works is the CLI copy on Windows ([Updating only the CLI with self-update](#updating-only-the-cli-with-self-update)).

## How you hear about a new version

When a new version is out, "Update to version <version>" ("New version <version> available" where the app cannot be replaced) appears at the top of the tray menu, and a strip appears at the top of the web UI. To stop checking, clear "Check for updates" in the tray menu ([Tray / menu bar](using.md#tray--menu-bar)).
The app checks at startup and every 24 hours. The environment variables that change the check are shared with the server, in [Checking for new versions](../server/updating.md#checking-for-new-versions-desktop-app-and-server).
Whenever the tray can't replace the app, choose the notice or open the [releases page](https://github.com/howashoji/looptrack/releases), and download the file for your OS.
To see your version, run `looptrack version` with the `looptrack` inside the app (`Looptrack.app/Contents/MacOS/looptrack`, the AppImage file, or `cli\looptrack.exe` on Windows).

## Update

Choose "Update to version <version>" at the top of the tray menu to replace the app.
This works for the macOS .app, the Linux AppImage, and Windows, whether you installed it with the installer or extracted the zip.
With "Install updates automatically" checked, the app does the same thing by itself as soon as it finds a new version.

1. It downloads the new version's file and compares it with the SHA-256 in `SHA256SUMS`, whose signature it has verified. If they differ, nothing is replaced.
2. On macOS, it checks the signatures of the dmg and of the `Looptrack.app` inside it with `spctl` and `codesign`. It also checks that the identifier and the signing team match the running app. The Windows files carry no code signature, so on Windows the check stops at the SHA-256 and the shape of the program.
3. It renames the current app with a `.prev` suffix (`Looptrack.app.prev`, `<AppImage file name>.prev`) and puts the new version in place under the original name. An older `.prev` is deleted.
   With the Windows zip, the same happens to each of the four files the zip holds. If you used the Windows installer, the new installer runs with no window instead and keeps your options.
4. It restarts the app. The new version waits for the old one to stop, then starts (without opening the browser).

If a step fails, the current version keeps running and the app tells you why. If the new version can't start, the previous one is put back.
With the Windows installer, the installer starts the app again when it finishes. If it fails and rolls back, it starts the previous version, and the reason is in `setup.log` under `updates` in the data folder.
What if the app's location (`/Applications` and the like) isn't writable? Then nothing is replaced. On macOS the verified dmg is opened so you can drag `Looptrack` onto `Applications`; on Linux and with the Windows zip, the release page is opened.
To go back after a replacement, choose "Quit" in the tray, delete the current app, and rename the `.prev` back to the original name (restore the database as in the steps below).

The Windows zip leaves four `.prev` files next to the app: `Looptrack.exe.prev`, `cli\looptrack.exe.prev`, `NOTICE.prev` and `OFL-BIZUDGothic.txt.prev`.
They are the previous version. Once you have quit the app, you can delete them. If you keep them, the next update replaces them anyway.
To go back by hand, quit the app, delete the four current files, and rename each `.prev` file back to the name without `.prev`.
The installer makes no `.prev` files. To go back, run the previous version's installer.
Whether Smart App Control, Microsoft Defender or SmartScreen stops an update that has no code signature hasn't been tried on a real machine yet. If an update doesn't finish, replace the app by hand as below.

Whenever the tray cannot replace the app, replace it by hand:

1. Choose "Quit" in the tray menu.
2. Replace the app with the new version:
   - macOS: open the new dmg and drag `Looptrack` onto `Applications` (choose "Replace").
   - Windows: run the new installer; it replaces the old version in place and keeps your options. If Looptrack is still running, the installer stops it first. If you use the zip, extract the new zip over the old folder.
   - Linux: put the new AppImage in place of the old one (you can keep the old file name; a replacement from the tray keeps it too).
3. Start the app again.

Your data stays in the data folder. It's upgraded automatically on the first start.
After that, putting the previous version of the app back won't work: it doesn't start, and reports that the DB was migrated by a newer looptrack. (A version from before this check was added doesn't stop and runs on the data anyway. All the more reason not to go back that way.)
Before changing the format, the new version copies `looptrack.db` into the `backups` folder of the data folder ([Where your data lives](using.md#where-your-data-lives)). If it can't make the copy, it leaves the format alone, doesn't start, and tells you why.
Here's how to go back to the previous version:

1. Choose "Quit" in the tray menu.
2. Put the previous version of the app back.
3. In the data folder, replace `looptrack.db` with the newest file in `backups` (the name with the latest time). Delete `looptrack.db-wal` and `looptrack.db-shm` if they are there.
4. Start the app.

Anything you changed after the backup was taken isn't in the restored database.
Leave the key file (`looptrack.db.secret-key`) as it is.
If the update didn't change the format, no backup is taken. The previous version can use the database as it is.

## What happens after the replacement

Your data is kept, and the first start of the new version upgrades it ([Update](#update) above has the details and the way back).
A few other things are brought in line with the new app on their own:

- The "Start at login" registration follows the app's current location on the next start. So do the Linux app list entry (`~/.local/share/applications/looptrack.desktop`) and its icon.
- The CLI location gets fixed too. On macOS and Linux the link keeps pointing at the app, and on Windows the CLI copy is refreshed on the next start.
  A copy you updated yourself with `self-update` is left alone, though ([Updating only the CLI with self-update](#updating-only-the-cli-with-self-update)).

## Updating only the CLI with self-update

What `looptrack self-update` does depends on the OS, because the CLI is a different kind of file on each.

On Windows it works. The CLI (`cli\looptrack.exe`, and the copy the app places in `%LOCALAPPDATA%\Programs\looptrack`) is a plain CLI build with no tray, so `self-update` replaces that file with the `looptrack` of the newest release ([Replacing it with self-update](../server/updating.md#replacing-it-with-self-update)). The app itself (`Looptrack.exe`) is never touched, so the tray and the double-click start stay as they are.

On macOS and Linux it stops with an error and replaces nothing, because the CLI is the app itself. Update the whole app ([Update](#update)). `looptrack self-update --check` still tells you whether a newer release exists.

Once you have replaced the Windows copy this way, three things change:

- The app no longer keeps it up to date. Updating the app leaves the copy at the version you put there, so the CLI and the app can end up on different versions. Run `looptrack self-update` again to catch up.
- Uninstalling the app doesn't remove it. The uninstaller removes only a copy the app placed and that is still unchanged. Delete the folder yourself ([Windows](#windows) under Uninstall).
- To go back to the CLI that comes with the app, delete `%LOCALAPPDATA%\Programs\looptrack\looptrack.exe`, then choose "Make the CLI available" in the tray menu. While that file exists, the menu item leaves it as it is.

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

A CLI copy you replaced with `self-update` stays. Delete its folder yourself:

```powershell
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\looptrack"
```

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
