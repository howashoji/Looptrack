# Desktop app

[Guide contents](../README.md)

This is where the desktop app guide starts. It covers downloading the app and connecting your AI agent, then using, updating and removing the app.

The desktop app is the easiest way to use Looptrack on your own, on one machine. You don't need a terminal.
Double-click the icon. It starts a local server (listening on 127.0.0.1 only, with your data in a single SQLite file) and opens the web UI in your default browser.
The first time, the browser shows the first-run setup, where you choose the administrator, two-factor auth and the first project.
From the tray icon (the menu bar on macOS) you can open the UI again, open settings, copy the MCP settings for your agent, make the CLI available, start at login, and quit.
Windows gets an installer. It adds Looptrack to the Start menu, and you can remove it from the list of installed apps.

You don't need administrator rights on any OS.
For a server shared by several people, use the [server edition](../server/README.md) instead.

## Pages for the desktop app

| Page | What it covers |
| -- | -- |
| [Getting started](getting-started.md) | Download, first launch, finishing setup in the browser, connecting your agent by chat |
| [Using the app](using.md) | The tray menu, where your data lives, the CLI that comes with the app, starting at login |
| [Updating](updating.md) | How you hear about a new version, replacing the app, going back, uninstalling |
| [Troubleshooting](troubleshooting.md) | The app seems not to start, the tray icon is missing, the agent lost its connection |

## Once your agent is connected

From there on, Looptrack works the same as the server edition.
Start with [Your first loop](../daily-use.md#your-first-loop), then go on to the [shared sections](../README.md#shared-sections) of the guide.
