# Desktop app troubleshooting

[Guide contents](../README.md) · [Desktop app](README.md) · Previous: [Updating the desktop app](updating.md) · Related: [FAQ / Troubleshooting](../faq.md)

This page covers what to do when the desktop app doesn't start, its tray icon is missing, or your agent loses its connection.
Messages that come up in either edition, such as "[Setup incomplete]", are in the shared [FAQ / Troubleshooting](../faq.md).

## Symptoms and what to do

| Symptom | What to do |
| -- | -- |
| Nothing seems to happen on double-click | The app may already be running without a visible tray icon. Open `http://127.0.0.1:18090/looptrack/`, or check the log in the log folder ([Where your data lives](using.md#where-your-data-lives)) |
| No tray icon on Windows 11 | It is under the "^" (hidden icons) button next to the clock. Click "^" and drag the Looptrack icon onto the taskbar, or turn it on in Settings → Personalization → Taskbar → Other system tray icons |
| SmartScreen blocks the installer | The installer is not code-signed yet. Choose "More info" → "Run anyway", after checking the download against `SHA256SUMS` |
| The installer or the app is blocked and there is no "Run anyway" | On Windows 11, Smart App Control blocks unsigned apps when it is turned on, and a PC your organization manages may block them by policy. "Signatures and OS warnings" in [Getting started with the server](../server/getting-started.md#signatures-and-os-warnings) has what you can do (it applies to the desktop app too) |
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

Pitfalls of using the CLI on Windows (PATH, hooks, here-documents) are under [Common Windows pitfalls](../faq.md#common-windows-pitfalls) in the FAQ.
