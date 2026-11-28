# Security

HopDesk gives full control of a computer to whoever connects, so security is the core of the design, not an add-on. This page explains the threat model and how to report a problem.

## Reporting a vulnerability

Please **don't open a public issue** for security problems. Report them privately through [GitHub security advisories](https://github.com/gadisaka/hopdesk/security/advisories/new) with steps to reproduce, and allow time for a fix before disclosing. Only the latest release gets security fixes.

## Design in one paragraph

Video, input, clipboard and files travel over WebRTC (DTLS-SRTP), directly between the viewer and the agent whenever possible. Each agent has its own Ed25519 identity key, which the viewer remembers after the first connection, like SSH. The password is never transmitted: the viewer proves it knows it with an HMAC over a PBKDF2-derived key, bound to the exact encrypted connection. A hostile server, a man in the middle or an impostor agent gets nothing it can use.

## Threat model

| Threat | Protection |
|---|---|
| Someone listening on the network | WebRTC encryption (DTLS-SRTP). The viewer needs HTTPS, and agents refuse plain `ws://` to public addresses. |
| A hostile or hacked server, or a man in the middle | The agent signs a statement tied to the exact encrypted connection (its DTLS fingerprints). The viewer's password proof is tied to it too, so a relayed or intercepted connection fails on both sides. |
| An impostor agent answering as your device | Each agent has its own Ed25519 identity, remembered after the first connection. A different identity triggers a red warning **before** anything password-related is sent. |
| Stolen password | The password is never transmitted. The viewer sends an HMAC proof using a PBKDF2 key (600,000 iterations), different for every connection, so it can't be replayed. The agent stores only the derived key, in a file only its user can read. |
| Password guessing | One attempt per connection. 5 wrong passwords block that address for 15 minutes, doubling each time, without affecting other addresses. There's also a cap of 30 failures per hour from everywhere combined. |
| Floods and resource exhaustion | Per-address limits on connections and attempts at the server, per-device limits, at most 3 unauthenticated sessions per agent, and timeouts. |
| Malicious websites using your browser | WebSocket Origin checks, a strict Content-Security-Policy, no framing, and no inline script. |
| Being watched unnoticed | A desktop notification when a session starts and ends, an optional approve/deny prompt, and an idle disconnect. |
| Clipboard attacks | The remote clipboard is only copied when you click. Clipboard sync can be limited or turned off. |
| Secrets leaking | Secrets come from environment variables, not command-line flags (with a warning if you use the flags), and TURN credentials are short-lived. |
| Built-in Tailscale Funnel (public address) | Only the viewer page, signaling and the relay are published, all behind the protections above. The relay only accepts short-lived credentials and relays between sessions on this computer. Sign out of Tailscale on the settings page to take it offline. |
| The app's settings page | Listens on this computer only, needs a random token for every request, and checks Host and Origin, so other websites and other users can't change settings. |
| A fake or tampered update | Updates are only installed if `latest.json` carries a valid Ed25519 signature from the key built into HopDesk, the version is newer (no downgrades), and the installer matches the signed SHA-256. On macOS, the new app's bundle ID and code signature are checked too. [Details](docs/updates.md). |
| Probing which devices exist | `/online` answers only for the device names it's asked about, at most 10 at a time and 6 times a minute per address, and only to this server's own page. It reveals no more than a connection attempt ("that device is not online") already does. |
| Windows login-screen control | Off by default. Turning it on needs administrator permission. The service runs its own copy of HopDesk from Program Files, where only administrators can change it, never the per-user install. Settings and the identity move to a folder only the system account, administrators and the user who turned it on can open. Ctrl+Alt+Del goes through an event only the system account can signal. |
| Malicious file names | Uploads are stripped of folders, reserved Windows names are renamed, and existing files are never overwritten. |

## What's still up to you

- **The first connection:** compare the fingerprint the viewer shows with the one in the HopDesk app. That one check is what makes impostors detectable later.
- **The server serves the viewer page,** so run your own server and reach it over HTTPS.
- **Anyone with the password has full control,** including your files. Use the generated password or a long one, and turn on **Approve each connection** for shared computers.
- **Windows login-screen control** runs HopDesk as the system account, so with it on, the password also opens the login screen and admin prompts, and files you upload or download are handled with system rights. Only turn it on for computers you'd trust with an administrator password.
- **The 30-per-hour cap** means a determined attacker spread across many addresses can pause new logins for a while, but can't get in. If that happens, the agent log shows it.

> [!IMPORTANT]
> HopDesk is a personal project and has **not** had a professional security audit.
