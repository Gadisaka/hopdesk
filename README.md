# hopdesk

A small, self-hosted remote desktop for your own computers. It's a personal RustDesk alternative.

- **Agent** (`cmd/agent`): runs on the computer you want to control (macOS or Windows). It streams the screen, replays your mouse and keyboard, syncs the clipboard and transfers files.
- **Server** (`cmd/server`): one small signaling server. It lets the viewer find an agent by ID and serves the viewer web page. Screen data and files do **not** go through it.
- **Viewer**: a web page served by the server. Open it in any browser, type the device ID and password, and you're in.

The connection is WebRTC (DTLS-encrypted, peer-to-peer when possible). The password is checked by the agent over that encrypted channel, so it never passes through the server.

```
 Viewer (browser) ──► Server: "connect me to ID mymac" ◄── Agent (registered as mymac)
        ▲                                                       │
        └──── WebRTC: H.264 video, input, clipboard, files ─────┘
                      (direct, or via a TURN relay)
```

## Features

- **H.264 video** through ffmpeg, using the hardware encoder on Macs (VideoToolbox) and x264 elsewhere. If ffmpeg isn't installed, the agent automatically sends JPEG frames instead.
- **Live controls** in the viewer: pick the display (multi-monitor), quality (low/medium/high) and frame rate (5–30 fps).
- **Clipboard sync** (text): copy on the remote and it reaches your computer. Paste with Cmd/Ctrl+V while viewing and your local clipboard is sent first.
- **File transfer**: drop files on the screen (or use "Send files…") to send them to the remote's Downloads folder. Open **Files** to browse the remote computer and download files.
- Mouse (including drag, right-click, scroll), keyboard with shortcuts, and a Cmd→Ctrl option for Mac-to-Windows.

## Build

You need Go 1.22 or newer.

```sh
cd hopdesk
go mod tidy          # downloads pion/webrtc, gorilla/websocket, screenshot, clipboard, x/image
```

**Server** (pure Go, runs anywhere):
```sh
go build -o hopdesk-server ./cmd/server
GOOS=linux GOARCH=amd64 go build -o hopdesk-server-linux ./cmd/server   # for a Linux VPS
```

**Mac agent** (build on the Mac; needs the Xcode command line tools: `xcode-select --install`):
```sh
go build -o hopdesk-agent ./cmd/agent
```

**Windows agent** (pure Go, so you can build it on your Mac):
```sh
GOOS=windows GOARCH=amd64 go build -o hopdesk-agent.exe ./cmd/agent
```

### ffmpeg (for H.264 video, on the controlled computer)
- macOS: `brew install ffmpeg`
- Windows: `winget install Gyan.FFmpeg` (or download a build and pass `-ffmpeg C:\path\to\ffmpeg.exe`)

The agent's log says which mode it uses, e.g. `video: H.264 with ffmpeg encoder h264_videotoolbox`.

## Run

1. **Start the server** somewhere both computers can reach (see "Across countries" below):
   ```sh
   ./hopdesk-server -addr :8080 -agent-key PICK-A-LONG-SECRET
   ```
   If you leave out `-agent-key`, it generates one and prints it. Agents need this key to register, which stops strangers from claiming your device IDs.

2. **Start an agent** on each computer you want to control:
   ```sh
   ./hopdesk-agent -server ws://SERVER:8080/ws -key PICK-A-LONG-SECRET -id mymac -password 'a-strong-password'
   ```

3. **Connect:** open `http://SERVER:8080` in a browser, enter the ID and password, and click Connect. Click the screen to give it keyboard focus.

Agent flags:

| Flag | Default | Meaning |
|---|---|---|
| `-quality` | `medium` | starting quality: `low` (1280px, 0.8 Mbps), `medium` (1600px, 2 Mbps), `high` (2560px, 5 Mbps) |
| `-fps` | `15` | starting frame rate: 5, 10, 15, 20 or 30 |
| `-display` | `0` | display to show first |
| `-codec` | `auto` | `auto`, `h264` or `jpeg` |
| `-ffmpeg` | `ffmpeg` | path to ffmpeg |
| `-encoder` | best available | force an ffmpeg encoder, e.g. `libx264` |
| `-upload-dir` | `~/Downloads` | where files sent from the viewer are saved |

### macOS permissions (on the controlled Mac)
The first time the agent runs, open **System Settings → Privacy & Security** and turn on, for the app that runs the agent (usually **Terminal**):
- **Screen Recording**: without it, frames are black or show only the wallpaper
- **Accessibility**: without it, mouse and keyboard do nothing. The agent warns you at startup.

Restart the agent after changing these.

### Clipboard notes
Browsers only let a page write to your clipboard automatically on `https://` (or `localhost`). Over plain `http://` (e.g. a Tailscale IP), the viewer shows a **Copy here** button when the remote clipboard changes. Pasting into the remote works either way. Text only, up to about 60 KB.

## Across countries

The viewer and agent must both reach the server, and must reach **each other** for WebRTC. Behind home routers and carrier-grade NAT (common with mobile and many ISPs), a direct path often fails, and you need a relay.

**Option A: Tailscale (easiest, free).** Install Tailscale on every device and sign in with one account. Run the server on any of them and use its Tailscale IP (`100.x.y.z`) everywhere:
```sh
./hopdesk-agent -server ws://100.x.y.z:8080/ws ...
```
WebRTC then connects directly over Tailscale. You don't need a TURN server or open ports.

**Option B: a small VPS (no VPN on the devices).** Run `hopdesk-server` and **coturn** (a TURN relay) on a cheap VPS:
```sh
./hopdesk-server -addr :443 -tls-cert cert.pem -tls-key key.pem \
  -agent-key ... -turn turn:your.vps:3478 -turn-user hop -turn-pass ...
```
Use `wss://your.vps/ws` for agents and `https://your.vps` for the viewer. Always use TLS here. Open TCP 443 and TCP/UDP 3478 (plus coturn's relay port range).

## Limitations

- No audio. Clipboard is text only.
- Downloads are held in browser memory until complete, so very large files (several GB) may fail. Uploads stream straight to disk.
- The keyboard uses a US-layout physical key map. Other characters fall back to typing the character itself.
- The browser keeps some shortcuts for itself (Cmd+W, Cmd+Q, Cmd+Tab).
- After packet loss, H.264 video recovers at the next keyframe (within about a second).
- Windows: the agent can't control admin (UAC) windows or the login screen unless it runs as administrator or as a service.
- macOS: the agent only works while a user is logged in.

## Security notes

- Use a long agent key and strong passwords (8+ characters required). After 5 wrong passwords, the agent refuses connections for a minute.
- A connected viewer can browse and download any file the agent's user can read, the same as someone sitting at that computer. Only share the password with people you'd give your computer to.
- Prefer Tailscale or TLS. This is a personal tool and has not been audited.
