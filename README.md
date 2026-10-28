# FolderChain

Keeps one folder identical on every computer on the local network — built for sharing G-code between shop machines. Single `FolderChain.exe`, no installer, Windows 10/11.

**Download:** grab `FolderChain.exe` from the [latest release](https://github.com/MylanMachine/FolderChain/releases/latest).

## Using it

1. Copy `FolderChain.exe` to each computer (anywhere — e.g. `C:\Tools`).
2. On the first computer: open it → **Create a new share** → pick the folder.
3. Click **Add a device** and copy the share key.
4. On each other computer: open it → **Join an existing share** → paste the key → pick that computer's folder.
5. When Windows asks *"Allow FolderChain on public and private networks?"* click **Allow**. (Or later: Settings → Windows Firewall → Allow.)

FolderChain then lives in the system tray and starts with Windows. Closing the window keeps it syncing; use the tray menu → **Quit** to stop it.

## What it does for reliability

| Situation | What happens |
|---|---|
| Laptop leaves the network / sleeps | Heartbeat every 5 s; a dead link is dropped within 20 s. Nothing in the folder changes. |
| Laptop rejoins (same or new IP) | Network adapters are watched every 2 s; on a change it re-announces and reconnects immediately. |
| Network blocks discovery | Add the other computer's IP under Settings → Extra device addresses. |
| Transfer interrupted | Partial download is kept and resumes from the same byte. |
| Crash / power loss | Files are written to a temp file, hash-verified, then swapped in atomically. On restart the folder is re-checked against the saved index. |
| Same file edited on two computers while apart | Both are kept: the newer one keeps the name, the other becomes `name.sync-conflict-<date>-<computer>.gcode`. |
| File overwritten or deleted by a sync | The old copy is moved to `<folder>\.folderchain\archive\<date>\` and kept 30 days. |
| File open in another program | Retried automatically until it's free. |
| Folder's drive missing | Syncing pauses with a warning instead of treating everything as deleted. |

## Security

- All traffic is TLS 1.3 (forward secret) directly between your computers — no cloud, no relay.
- Devices authenticate by proving knowledge of the 160-bit share key, bound to each TLS session, so a man-in-the-middle can't relay. The key itself is never sent.
- Discovery broadcasts carry only a share ID and are MAC'd with a key derived from the share key.
- The local UI listens on `127.0.0.1` only and requires a random per-run token.
- Anyone with the share key can join — treat it like a password. To revoke a computer, make a new share and re-join the others.

## Files

- Settings, index and log: `%APPDATA%\FolderChain\` (`folderchain.log` is useful when something looks wrong).
- Inside the synced folder: hidden `.folderchain\` (temp downloads, archive, folder marker). Don't sync it with anything else.
- Ignored: `desktop.ini`, `Thumbs.db`, `~$*`, `*.tmp`, `*.part`, `*.crdownload`.

Ports: TCP 47124 (sync), UDP 47123 (discovery).

## Building

```powershell
.\build.ps1
```

Code layout: `internal/engine` is the sync engine (scanner, version vectors, protocol, discovery, transfers) with integration tests that run real devices against each other, including silent network drops, interrupted transfers, crashes and conflicts. `main.go` / `winshell.go` / `server.go` are the tray, window, and local UI API; `web/` is the UI.

## License

[MIT](LICENSE) — free to use, modify and share.
