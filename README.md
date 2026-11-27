<p align="center">
  <img src="docs/images/logo.svg" alt="hopdesk logo" width="112">
</p>
<h1 align="center">hopdesk</h1>
<p align="center">
  Your own remote desktop. Self-hosted, encrypted, and small enough to read in an afternoon.<br><br>
  <a href="#features">Features</a> •
  <a href="#quick-start">Quick start</a> •
  <a href="#build">Build</a> •
  <a href="#connecting-across-countries">Across countries</a> •
  <a href="#file-structure">Structure</a> •
  <a href="#security">Security</a> •
  <a href="#screenshots">Screenshots</a> •
  <a href="#testing">Testing</a>
</p>

<p align="center">
  <img alt="Go 1.22+" src="https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white">
  <img alt="Agent: macOS | Windows" src="https://img.shields.io/badge/agent-macOS%20%7C%20Windows-555">
  <img alt="Viewer: any browser" src="https://img.shields.io/badge/viewer-any%20browser-3b82f6">
  <img alt="WebRTC" src="https://img.shields.io/badge/transport-WebRTC-8b5cf6">
</p>

> [!CAUTION]
> **Only use hopdesk on computers you own or have permission to access.** Anyone with a device's password can see its screen, control it, and read its files.

hopdesk is a personal alternative to RustDesk, TeamViewer and AnyDesk. You run one tiny server, put an agent on the computers you want to reach, and connect from any browser. There are no accounts and no third-party cloud, and the screen goes straight from one computer to the other.

<p align="center">
  <a href="docs/images/session.png"><img src="docs/images/session.png" alt="A hopdesk session in the browser" width="860"></a>
</p>

## Features

- **Smooth H.264 video** through ffmpeg, using the Mac's hardware encoder (VideoToolbox) or x264. Without ffmpeg, it falls back to JPEG frames automatically.
- **Full control**: mouse (click, drag, right-click, scroll) and keyboard with shortcuts. Cmd→Ctrl mapping for Mac-to-Windows.
- **Multi-monitor, quality and frame rate**, switchable live from the viewer.
- **Clipboard sync** (text) in both directions.
- **File transfer**: drop files on the screen to send them, or browse the remote computer and download.
- **Secure by design**: WebRTC (DTLS) encryption, plus each agent has its own identity key that the viewer remembers, like SSH. Your password is never sent anywhere: the viewer proves it knows the password, and that proof is tied to the one connection. A hostile server, a man in the middle, or an impostor agent gets nothing it can use. [Details below](#security).
- **Nothing to install on the viewing side.** It's a web page.

## How it works

<p align="center"><img src="docs/images/architecture.svg" alt="Architecture: viewer and agent find each other through the server, then talk directly over WebRTC" width="820"></p>

| Part | Runs on | Does |
|---|---|---|
| **Server** (`cmd/server`) | anywhere both sides can reach | lets viewers find agents by ID, serves the viewer page |
| **Agent** (`cmd/agent`) | each computer you want to control | streams the screen, replays input, syncs clipboard, moves files |
| **Viewer** (`cmd/server/web`) | your browser | shows the screen, sends input, file browser |

## Quick start

```sh
git clone https://github.com/gadisaka/hopdesk.git && cd hopdesk
go mod tidy

# 1. the server (secrets go in environment variables, not on the command line)
export HOPDESK_AGENT_KEY="$(openssl rand -hex 24)"
go run ./cmd/server

# 2. the agent, on the computer to control (macOS: brew install ffmpeg for H.264)
HOPDESK_KEY="$HOPDESK_AGENT_KEY" go run ./cmd/agent -id mymac
#    first run prints a password (save it) and the computer's identity fingerprint

# 3. the viewer
open http://localhost:8080      # enter "mymac" and the password
```

The first time you connect, the viewer shows the agent's **fingerprint**. Check that it matches the one the agent printed, then confirm. After that it's remembered, and you're warned loudly if it ever changes.

> The viewer only runs on a secure page: `https://`, or `http://localhost`. Browsers only provide the cryptography it needs there, and it stops anyone on the network from tampering with the page. See [Connecting across countries](#connecting-across-countries) for the easy HTTPS setup.

On a Mac, allow **Screen Recording** and **Accessibility** for Terminal (or whatever runs the agent) in **System Settings → Privacy & Security**, then restart the agent.

## Build

You need Go 1.22+. The Mac agent also needs the Xcode command line tools (`xcode-select --install`).

```sh
go build -o hopdesk-server ./cmd/server                                   # server (pure Go)
GOOS=linux GOARCH=amd64 go build -o hopdesk-server-linux ./cmd/server     # server for a Linux VPS
go build -o hopdesk-agent ./cmd/agent                                     # Mac agent (build on the Mac)
GOOS=windows GOARCH=amd64 go build -o hopdesk-agent.exe ./cmd/agent       # Windows agent (from any OS)
```

**ffmpeg** (for H.264 video, on the controlled computer): `brew install ffmpeg` on macOS, or `winget install Gyan.FFmpeg` on Windows. The agent log says which mode it picked, e.g. `video: H.264 with ffmpeg encoder h264_videotoolbox`.

### Server flags

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `:8080` | listen address |
| env `HOPDESK_AGENT_KEY` | random, printed | secret that agents need to register (16+ characters) |
| `-stun` | Google's public STUN | STUN server (`""` to disable) |
| `-turn` + env `HOPDESK_TURN_SECRET` | none | TURN relay, with short-lived credentials (coturn `use-auth-secret`) |
| `-tls-cert`, `-tls-key` | none | serve HTTPS/WSS directly |
| `-behind-proxy` | off | trust `X-Forwarded-*` from a proxy on the same machine (tailscale serve, Caddy) |
| `-allow-origin` | none | extra viewer address(es) to accept, if your proxy rewrites the host |

### Agent flags

| Flag | Default | Meaning |
|---|---|---|
| `-server` | `ws://localhost:8080/ws` | server address. Plain `ws://` is only allowed to private and Tailscale addresses, so use `wss://` |
| env `HOPDESK_KEY` | required | the server's agent key |
| `-id` | hostname | the ID you type in the viewer |
| env `HOPDESK_PASSWORD` | generated once and saved | connection password (10+ characters). Without it, the agent makes one up on first run, shows it once and keeps only a hash |
| `-reset-password` | | generate a new password |
| `-confirm` | off | ask the person at the computer to approve each connection (dialog, denies after 30 s) |
| `-idle` | `30m` | disconnect viewers after this long without input (`0` = never) |
| `-clipboard` | `both` | `both`, `to-remote` (only viewer → this computer), or `off` |
| `-state` | user config folder | where the identity key file lives |
| `-quality` | `medium` | `low` (1280px, 0.8 Mbps), `medium` (1600px, 2 Mbps), `high` (2560px, 5 Mbps) |
| `-fps` | `15` | 5, 10, 15, 20 or 30 |
| `-display` | `0` | display to show first |
| `-codec` | `auto` | `auto`, `h264` or `jpeg` |
| `-ffmpeg`, `-encoder` | auto | ffmpeg path and encoder override |
| `-upload-dir` | `~/Downloads` | where files sent from the viewer are saved |

## Connecting across countries

Both sides must reach the server, and WebRTC must reach **each other**. Behind home routers and carrier-grade NAT, a direct path often fails.

**Option A: Tailscale (easiest, free).** Install Tailscale on every device and sign in with one account. On the computer running the server, turn on MagicDNS and HTTPS certificates in the Tailscale admin console, then:

```sh
./hopdesk-server -addr 127.0.0.1:8080 -behind-proxy
tailscale serve --bg 8080        # HTTPS with a real certificate, only inside your tailnet
```

Open `https://<that-machine>.<your-tailnet>.ts.net` to view, and point agents at `wss://<that-machine>.<your-tailnet>.ts.net/ws`. You don't need open ports or a relay.

**Option B: a small VPS.** Run `hopdesk-server` with TLS plus **coturn** as a TURN relay:

```sh
./hopdesk-server -addr :443 -tls-cert cert.pem -tls-key key.pem \
  -agent-key ... -turn turn:your.vps:3478 -turn-user hop -turn-pass ...
```

Agents use `wss://your.vps/ws`, and you open `https://your.vps`. Open TCP 443 and TCP/UDP 3478 (plus coturn's relay ports).

## File structure

- **[cmd/server](cmd/server)**: signaling server with rate limits and origin checks, plus the viewer ([index.html](cmd/server/web/index.html), [viewer.js](cmd/server/web/viewer.js))
- **[cmd/agent](cmd/agent)**: the agent
  - `main.go`: sessions, authentication steps, control messages
  - `identity.go`: the agent's identity key and stored password key
  - `guard.go`: password lockouts and session caps
  - `stream_h264.go`: ffmpeg → RTP → WebRTC video
  - `capture.go`: screen capture, JPEG fallback stream
  - `input_darwin.go` / `input_windows.go`: mouse and keyboard injection
  - `notify_*.go`: session notifications and the `-confirm` prompt
  - `clipboard.go`, `files.go`: clipboard sync, upload/download/browse
- **[internal/auth](internal/auth)**: identity signatures and password proofs (standard library only, with tests)
- **[internal/proto](internal/proto)**: signaling messages shared by server and agent
- **[test/e2e](test/e2e)**: end-to-end and attack tests, stand-in agent, Go/browser crypto cross-check
- **[docs/images](docs/images)**: logo, diagram, screenshots

## Screenshots

**Connecting**

[![Connect screen](docs/images/login.png)](docs/images/login.png)

**Controlling a remote computer**

[![Remote session](docs/images/session.png)](docs/images/session.png)

**Browsing, downloading and sending files**

[![File transfer](docs/images/files.png)](docs/images/files.png)

**First connection: compare the fingerprint**

[![First connection](docs/images/first-connect.png)](docs/images/first-connect.png)

**An impostor answering as your device is caught before your password is used**

[![Impostor warning](docs/images/impostor-warning.png)](docs/images/impostor-warning.png)

> The screenshots come from the end-to-end test. The "remote computer" in them is a demo desktop drawn by the test's stand-in agent.

## Testing

```sh
go test ./...                                   # auth, lockouts, URL policy, file names, identity file
go build -o hopdesk-server ./cmd/server
pip install playwright && playwright install chromium
python3 test/e2e/run.py                         # 30 end-to-end checks, including attacks
python3 test/e2e/crosscheck.py                  # Go and browser crypto agree exactly
```

`test/e2e/run.py` starts the real server and drives the real viewer in Chrome against a stand-in agent that speaks the full agent protocol. It covers:
- **Features:** video, mouse position, keyboard, clipboard, settings, file browser, and byte-exact uploads and downloads.
- **Attacks:** an impostor agent, a man in the middle, cross-site WebSockets, connection floods, agent-key guessing, wrong passwords, clickjacking headers, and running the viewer over plain HTTP.

Set `SCREENSHOTS=docs/images` to refresh the screenshots.

The tests don't cover the agent's screen capture, input injection or ffmpeg on a real desktop. Check those by hand on a Mac and a Windows PC.

## Limitations

- No audio. The clipboard is text only (up to about 60 KB).
- Text copied on the remote shows up with a **Copy here** button. It never replaces your clipboard on its own, because a compromised remote could otherwise swap in a malicious command for you to paste.
- Downloads are held in browser memory until complete, so multi-GB files may fail. Uploads stream to disk.
- The keyboard uses a US-layout physical key map, with other characters typed directly. The browser keeps a few shortcuts (Cmd+W, Cmd+Q, Cmd+Tab).
- Windows: admin (UAC) windows and the login screen need the agent to run as administrator or as a service. macOS: works while a user is logged in.

## Security

How hopdesk handles each threat:

| Threat | Protection |
|---|---|
| Someone listening on the network | WebRTC encryption (DTLS-SRTP). The viewer needs HTTPS, and agents refuse plain `ws://` to public addresses. |
| A hostile or hacked server, or a man in the middle | The agent signs a statement tied to the exact encrypted connection (its DTLS fingerprints). The viewer's password proof is tied to it too, so a relayed or intercepted connection fails on both sides. |
| An impostor agent answering as your device | Each agent has its own Ed25519 identity, and the viewer remembers it after the first connection. A different identity triggers a red warning **before** anything password-related is sent. |
| Stolen password | The password is never transmitted. The viewer sends an HMAC proof using a PBKDF2 key (600,000 iterations), different for every connection, so it can't be replayed. The agent stores only the derived key, in a file only its user can read. |
| Password guessing | One attempt per connection. 5 wrong passwords block that address for 15 min, doubling each time, and other addresses (yours) are unaffected. There's also a cap of 30 failures per hour from everywhere combined. |
| Floods and resource exhaustion | Per-address limits on connections and attempts at the server, per-device limits, at most 3 unauthenticated sessions per agent, and timeouts. |
| Malicious websites using your browser | WebSocket Origin checks, a strict Content-Security-Policy, no framing, and no inline script. |
| Being watched unnoticed | A desktop notification when a session starts and ends, optional approve/deny prompt (`-confirm`), and an idle disconnect. |
| Clipboard attacks | The remote clipboard is only copied when you click. `-clipboard` can limit or disable sync. |
| Secrets leaking | Secrets come from environment variables, not command-line flags (warned if used), and TURN credentials are short-lived. |
| Malicious file names | Uploads are stripped of folders, reserved Windows names are renamed, and existing files are never overwritten. |

What's still up to you:
- **First connection:** compare the fingerprint the viewer shows with the one the agent printed. That one check is what makes impostors detectable later.
- **The server serves the viewer page**, so run your own server and reach it over HTTPS.
- **Anyone with the password has full control**, including your files. Use the generated password, or a long one, and `-confirm` on shared computers.
- **The 30-per-hour cap** means a determined attacker spread across many addresses can pause new logins for a while, though not break in. If that happens, the agent log shows it.
- **This is a personal project and has not had a professional security audit.**
