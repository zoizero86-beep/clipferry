# Clipferry

**Copy on your Mac, paste on your PC, and let your AI tools read it too. No account, no server, open source.**

Clipferry is a small app for macOS and Windows that keeps one clipboard across the computers on your desk. Install it on both, pair them with a 9-digit code, and anything you copy on one shows up on the other. The computers find each other on your Wi-Fi and talk directly, end-to-end encrypted. It also:

- **Catches secrets before they travel.** API keys, tokens, private keys, passwords and one-time codes are recognised on your device. By default they sync masked and are deleted everywhere after a few minutes. You can also keep them on the device or sync them normally.
- **Remembers and searches.** Every clip from every device lands in a searchable history, newest first, with the device and time. You can pin the ones you reuse.
- **Talks to AI tools.** Cursor, VS Code, Windsurf and any other [MCP](https://modelcontextprotocol.io) client can ask *"what did I just copy on my other machine?"*
- **Keeps your data yours.** Clips go straight from one computer to the other over your local network. Nothing passes through a cloud, and nothing needs to be set up besides the app. Different networks (home Mac, office PC)? Add an optional relay you run yourself.

## How it works

```
        MacBook                        same Wi-Fi                     Office PC
 ┌──────────────────┐        encrypted, direct (no server)     ┌──────────────────┐
 │ Clipferry        │ ◄──────────────────────────────────────► │ Clipferry        │
 │  clipboard       │                                          │  clipboard       │
 │  history         │     found each other with Bonjour/mDNS   │  history         │
 │  MCP ◄─ AI tools │                                          │  MCP ◄─ AI tools │
 └──────────────────┘                                          └──────────────────┘
```

1. You copy something. Clipferry checks whether it looks like a secret, saves it to the local history and encrypts it with your ferry key (XChaCha20-Poly1305).
2. The encrypted clip goes straight to your other computers on the same network. They find each other with Bonjour/mDNS, the same way AirDrop and printers are found.
3. Each device decrypts the clip, adds it to its own history and puts it on the clipboard, ready to paste.

Pairing uses a password-authenticated key exchange (PAKE) over the 9-digit code, so the code never travels and a wrong guess fails. The ferry key is handed over only after the exchange succeeds. If one computer is asleep, the other catches it up on whatever it missed when it wakes.

## Install

Download the latest build from **[Releases](https://github.com/bkrdmrcioglu/clipferry/releases)**.

### macOS (11 Big Sur or later, Apple silicon and Intel)

1. Open `Clipferry-x.y.z-macos.dmg` and drag **Clipferry** into **Applications**.
2. Open it. The first time, macOS may say the app is from an unidentified developer. Right-click Clipferry in Applications, choose **Open**, then **Open** again.
3. A clipboard icon appears in the menu bar, and the Clipferry window opens. When macOS asks whether Clipferry may **find devices on your local network**, click **Allow**. That is how it finds your PC.

### Windows (10 and 11, x64 and ARM)

1. Run `Clipferry-x.y.z-windows-setup.exe`. It installs for your user only, without administrator rights. If SmartScreen warns you, click **More info → Run anyway**.
2. Clipferry starts and its icon appears in the system tray, next to the clock. Click the **^** arrow if it is hidden. When Windows Firewall asks, allow Clipferry on **private networks**. That is how your Mac reaches it.

Prefer no installer? `Clipferry-amd64.exe` (or `-arm64.exe`) is a portable single file. Clipferry uses Microsoft Edge WebView2 for its window, which is preinstalled on Windows 10 and 11.

Clipferry lives in the menu bar (macOS) or system tray (Windows); closing its window keeps it running. To have it start with your computer, turn on **Start Clipferry when this computer starts** in the welcome card or in Settings. The same option is in the icon's menu as **Start at login**.

## Pair your computers

Both computers must be on the same network (the same Wi-Fi, or Wi-Fi and Ethernet on the same router).

1. On the first computer, open Clipferry and click **Show a code**.
2. On the second computer, click **Enter a code** and type it.
3. Done. Copy on either one and paste on the other.

If the second computer cannot find the first, check that:

- both are on the same network, and not on a guest network with "client isolation";
- Clipferry was allowed through the Windows Firewall and has macOS's local network permission (System Settings → Privacy & Security → Local Network).

To add a third device, use **Add a device** in the menu or the **Devices** panel. A pairing code works once and expires after 5 minutes.

## Secrets

Clipferry looks for the following before anything leaves your device:

| Detected | Examples |
|---|---|
| Cloud and API keys | AWS, GitHub, GitLab, AI providers, Stripe, Slack, Google, npm |
| Tokens | JSON Web Tokens |
| Private keys | `-----BEGIN … PRIVATE KEY-----` |
| Credentials in text | `password=…`, `api_key: …`, `postgres://user:pass@host` |
| Codes and cards | 6–8 digit one-time codes, card numbers (Luhn-checked) |
| Password-like strings | random-looking single tokens with mixed character classes |
| Password managers | anything 1Password, Bitwarden, KeePass and others mark as concealed |

In **Settings → Passwords, keys and codes** you choose what happens to them:

- **Sync briefly** (default): synced and shown masked, then deleted from every device's history after 1–60 minutes. If it is still on your clipboard at that point, the clipboard is cleared too.
- **Keep local:** never leaves this device and is not stored in history.
- **Sync normally:** synced and kept, but still masked in the UI.

AI tools never see secret contents unless you turn on **Let AI tools read secrets**. They are told that a secret was copied, and of what kind.

## Connect an AI tool (MCP)

Each Clipferry app serves an MCP endpoint on this computer at `http://127.0.0.1:47823/mcp`, protected by a per-device token. Click the **plug icon** in the Clipferry window to get a ready-to-paste config with your token filled in.

**Cursor**: `~/.cursor/mcp.json`

```json
{
  "mcpServers": {
    "clipferry": {
      "url": "http://127.0.0.1:47823/mcp",
      "headers": { "Authorization": "Bearer YOUR_TOKEN" }
    }
  }
}
```

**VS Code**: `.vscode/mcp.json`

```json
{
  "servers": {
    "clipferry": {
      "type": "http",
      "url": "http://127.0.0.1:47823/mcp",
      "headers": { "Authorization": "Bearer YOUR_TOKEN" }
    }
  }
}
```

| Tool | What it does |
|---|---|
| `get_latest_clip` | The most recent clip, optionally only from another device (`from_other_device: true`) or a named device |
| `list_recent_clips` | Recent history across devices, optionally for one device |
| `search_clips` | Search the history |
| `list_devices` | The devices sharing this clipboard |

Any other MCP client that supports Streamable HTTP takes the same URL and `Authorization` header.

Then ask your assistant something like *"Use the error I just copied on my Windows machine"* or *"Find the SQL query I copied yesterday"*.

## Different networks? Run a relay (optional)

Same Wi-Fi needs nothing else. If your computers are on different networks (a Mac at home, a PC at the office), they need a meeting point. A Clipferry relay is a tiny server that forwards the already-encrypted clips and cannot read them. Run it on any server you have:

```bash
git clone https://github.com/bkrdmrcioglu/clipferry
cd clipferry
docker compose up -d
```

Put it behind HTTPS (Coolify, Caddy, Traefik, nginx…). Then, on every device, open **Settings → Connection**, choose **Relay server** and enter `wss://your-relay.example.com`. To keep strangers off your relay entirely, set `CLIPFERRY_RELAY_TOKEN` and enter the same token on your devices.

**Coolify:** create a new resource from this Git repository using the *Docker Compose* build pack, set a domain for the `relay` service on port 8080, and deploy.

The relay keeps the last 20 encrypted messages of each ferry for 24 hours, so a laptop that was asleep still catches up. It stores nothing on disk.

## Build from source

You need Go 1.26+. Building the macOS app also needs the Xcode command line tools.

```bash
go test ./...                 # unit tests + end-to-end test
./scripts/build-macos.sh      # dist/Clipferry.app and a .dmg (on macOS)
./scripts/build-windows.sh    # dist/Clipferry.exe, plus the installer if NSIS is installed
go run ./cmd/clipferry-relay  # a relay on :8080
```

### Try it without two computers

```bash
go run ./cmd/clipferry-demo
```

This starts two simulated devices ("MacBook Pro" and "Office PC") on a simulated local network in one process, pairs them, copies some sample clips, and prints a URL for each device's window. Add `CLIPFERRY_UI_DIR=internal/ui/dist` to edit the UI live.

### Project layout

| Path | What |
|---|---|
| `cmd/clipferry` | Desktop app (Wails v3): tray/menu bar icon, window, start at login |
| `cmd/clipferry-relay` | The optional relay server, for devices on different networks |
| `cmd/clipferry-demo` | Two simulated devices for development |
| `internal/core` | Engine: clipboard watching, secret policy, sync, local API, MCP |
| `internal/ferry` | Connections and code pairing (PAKE) |
| `internal/lan` | Finding devices on the local network (mDNS) |
| `internal/relay` | Relay protocol; every device also runs one for its local network |
| `internal/box` | Encryption and key derivation |
| `internal/secrets` | Secret detection |
| `internal/store` | Local SQLite history with full-text search |
| `internal/sysclip` | Native clipboard access for macOS and Windows |
| `internal/ui/dist` | The app window (plain HTML, CSS and JS, no build step) |

## Security notes

- Everything sent between devices is encrypted with a 256-bit key shared only by your paired devices, whether it goes over your network or through a relay.
- For same-network sync, each device listens on TCP port 47824 on your local network. A device that is not paired sees only encrypted bytes.
- The local API and MCP endpoint listen on `127.0.0.1` only. They require this device's token and reject requests whose `Host` is not a loopback name (protection against DNS rebinding).
- The history is stored unencrypted on each device, in `~/Library/Application Support/Clipferry` (macOS) or `%APPDATA%\Clipferry` (Windows), like any clipboard manager's. Secrets in **Sync briefly** mode are removed from it when they expire.
- **Leave this ferry** stops a device from syncing. Removing a lost device from a ferry for good requires a new ferry for now: leave the ferry on every device and pair them again. Key rotation is on the roadmap.
- Release builds are not code-signed yet, hence the first-launch warnings above.

## How Clipferry compares

| | Clipferry | KDE Connect | LocalSend | ClipCascade | Pushbullet |
|---|---|---|---|---|---|
| Automatic Mac ↔ Windows clipboard sync | ✓ | ✓ (weak on macOS) | manual send | ✓ | ✓ |
| Needs a server or account | no | no | no | your own server | account |
| Works across different networks | with your own relay | | | ✓ | ✓ |
| Setup | install + code | install + accept | install | run a server | account |
| End-to-end encrypted | ✓ | ✓ | ✓ | ✓ | partly |
| Secrets detected and expired | ✓ | | | | |
| Searchable history | ✓ | | | | |
| AI tools (MCP) | ✓ | | | | |
| Open source / self-hostable | ✓ / ✓ | ✓ / n.a. | ✓ / n.a. | ✓ / ✓ | ✗ / ✗ |

## Roadmap

- Images and small files
- Signed and notarized builds, automatic updates
- Linux app
- Ferry key rotation when a device is removed

## License

[MIT](LICENSE)
