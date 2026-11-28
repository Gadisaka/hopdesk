# Command line

The desktop app is the easiest way to use HopDesk. You can also run the server and the agent directly, for example to put the server on a Linux VPS or to script the agent.

## Quick start

```sh
git clone https://github.com/gadisaka/hopdesk.git && cd hopdesk

# 1. the server (secrets go in environment variables, not on the command line)
export HOPDESK_AGENT_KEY="$(openssl rand -hex 24)"
go run ./cmd/server

# 2. the agent, on the computer to control (macOS: brew install ffmpeg for H.264)
HOPDESK_KEY="$HOPDESK_AGENT_KEY" go run ./cmd/agent -id mymac
#    the first run prints a password (save it) and this computer's fingerprint

# 3. the viewer
open http://localhost:8080      # enter "mymac" and the password
```

The first time you connect, the viewer shows the agent's **fingerprint**. Check that it matches the one the agent printed, then confirm. After that it's remembered, and you're warned if it ever changes.

On a Mac, allow **Screen Recording** and **Accessibility** for Terminal (or whatever runs the agent) in **System Settings → Privacy & Security**, then restart the agent.

> [!NOTE]
> The viewer only works on `https://` or `http://localhost`. To reach it from other devices, see [Connecting across countries](connecting.md).

## Server flags

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `:8080` | Listen address |
| env `HOPDESK_AGENT_KEY` | random, printed | Secret that agents need to register (16+ characters) |
| `-stun` | Google's public STUN | STUN server (`""` to disable) |
| `-turn` + env `HOPDESK_TURN_SECRET` | none | TURN relay, with short-lived credentials (coturn `use-auth-secret`) |
| `-turn-user`, `-turn-pass` | none | Static TURN credentials (prefer the secret) |
| `-tls-cert`, `-tls-key` | none | Serve HTTPS/WSS directly |
| `-behind-proxy` | off | Trust `X-Forwarded-*` from a proxy on the same machine (`tailscale serve`, Caddy) |
| `-allow-origin` | none | Extra viewer address(es) to accept, if your proxy rewrites the host |

## Agent flags

| Flag | Default | Meaning |
|---|---|---|
| `-server` | `ws://localhost:8080/ws` | Server address. Plain `ws://` is only allowed to private and Tailscale addresses, so use `wss://` |
| env `HOPDESK_KEY` | required | The server's agent key |
| `-id` | hostname | The device name you type in the viewer |
| env `HOPDESK_PASSWORD` | generated once and saved | Connection password (10+ characters). Without it, the agent makes one up on first run, shows it once and keeps only a hash |
| `-reset-password` | | Generate a new password |
| `-confirm` | off | Ask the person at the computer to approve each connection (a dialog that denies after 30 s) |
| `-idle` | `30m` | Disconnect viewers after this long without input (`0` = never) |
| `-clipboard` | `both` | `both`, `to-remote` (only viewer → this computer), or `off` |
| `-state` | user config folder | Where the identity key file lives |
| `-quality` | `high` | `low`, `medium`, `high` or `ultra` (see [Video quality](video-quality.md)) |
| `-fps` | `30` | 5, 10, 15, 20, 30 or 60 |
| `-display` | `0` | Display to show first |
| `-codec` | `auto` | `auto`, `h264` or `jpeg` |
| `-ffmpeg`, `-encoder` | auto | ffmpeg path and encoder override |
| `-upload-dir` | `~/Downloads` | Where files sent from the viewer are saved |

## Where the app keeps its files

- **Settings and identity:** `~/Library/Application Support/hopdesk` on macOS, or `%APPDATA%\hopdesk` on Windows. Reinstalling keeps the same fingerprint.
- **Log:** in the same folder. Click **Open log** on the settings page.
