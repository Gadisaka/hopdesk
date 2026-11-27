# hopdesk

A small, self-hosted remote desktop for your own computers. It's a personal RustDesk alternative.

- **Agent** (`cmd/agent`): runs on the computer you want to control (macOS or Windows). It streams the screen and replays your mouse and keyboard.
- **Server** (`cmd/server`): one small signaling server. It lets the viewer find an agent by ID and serves the viewer web page. Screen data does **not** go through it.
- **Viewer**: a web page served by the server. Open it in any browser, type the device ID and password, and you're in.

The connection is WebRTC (DTLS-encrypted, peer-to-peer when possible). The password is checked by the agent over that encrypted channel, so it never passes through the server.

```
 Viewer (browser) ──► Server: "connect me to ID mymac" ◄── Agent (registered as mymac)
        ▲                                                       │
        └──────────── WebRTC: screen frames ◄─► mouse/keys ─────┘
                      (direct, or via a TURN relay)
```

## Build

You need Go 1.22 or newer.

```sh
cd hopdesk
go mod tidy          # downloads pion/webrtc, gorilla/websocket, screenshot, x/image
```

**Server** (pure Go, runs anywhere):
```sh
go build -o hopdesk-server ./cmd/server
# for a Linux VPS:
GOOS=linux GOARCH=amd64 go build -o hopdesk-server-linux ./cmd/server
```

**Mac agent** (build it on the Mac; it needs the Xcode command line tools: `xcode-select --install`):
```sh
go build -o hopdesk-agent ./cmd/agent
```

**Windows agent** (pure Go, so you can build it on your Mac):
```sh
GOOS=windows GOARCH=amd64 go build -o hopdesk-agent.exe ./cmd/agent
```

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
   On Windows: `hopdesk-agent.exe -server ws://SERVER:8080/ws -key ... -id homepc -password ...`

3. **Connect:** open `http://SERVER:8080` in a browser, enter the ID and password, and click Connect. Click the screen to give it keyboard focus.

Useful agent flags: `-fps 10`, `-quality 60` (JPEG), `-maxwidth 1600` (downscale), `-display 0`.

### macOS permissions (on the controlled Mac)
The first time the agent runs, open **System Settings → Privacy & Security** and turn on, for the app that runs the agent (usually **Terminal**):
- **Screen Recording**: without it, frames are black or show only the wallpaper
- **Accessibility**: without it, mouse and keyboard do nothing. The agent warns you at startup.

Restart the agent after changing these.

## Across countries

The viewer and agent must both reach the server, and must reach **each other** for WebRTC. Behind home routers and carrier-grade NAT (common with mobile and many ISPs), a direct path often fails, and you need a relay.

**Option A: Tailscale (easiest, free).** Install Tailscale on every device and sign in with one account. Run the server on any of them and use its Tailscale IP (`100.x.y.z`) everywhere:
```sh
./hopdesk-agent -server ws://100.x.y.z:8080/ws ...
```
WebRTC then connects directly over Tailscale. You don't need a TURN server or open ports, and everything stays off the public internet.

**Option B: a small VPS (no VPN on the devices).** Run `hopdesk-server` and **coturn** (a TURN relay) on a cheap VPS:
```sh
./hopdesk-server -addr :443 -tls-cert cert.pem -tls-key key.pem \
  -agent-key ... -turn turn:your.vps:3478 -turn-user hop -turn-pass ...
```
Use `wss://your.vps/ws` for agents and `https://your.vps` for the viewer. Always use TLS here; plain `ws://` over the internet exposes your traffic to the network. Open TCP 443 and TCP/UDP 3478 (plus coturn's relay port range).

## Limitations (it's a first version)

- Frames are JPEG over a data channel, about 10 fps. That's fine for admin work, not for video or games. The next step up is H.264 video tracks.
- Only one display at a time (`-display N`). No audio, clipboard sync, file transfer or touch input yet.
- The keyboard uses a US-layout physical key map. Other characters fall back to typing the character itself.
- The browser keeps some shortcuts for itself (Cmd+W, Cmd+Q, Cmd+Tab).
- Windows: the agent can't control admin (UAC) windows or the login screen unless it runs as administrator or as a service.
- macOS: the agent only works while a user is logged in.

## Security notes

- Use a long agent key and strong passwords (8+ characters required). After 5 wrong passwords, the agent refuses connections for a minute.
- Prefer Tailscale or TLS. This is a personal tool and has not been audited, so don't expose it to people you don't trust.
