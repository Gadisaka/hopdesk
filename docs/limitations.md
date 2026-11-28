# Limitations

- **Audio:** none.
- **Clipboard:** text only, up to about 60 KB. Text copied on the remote shows up with a **Copy here** button and never replaces your clipboard on its own, because a compromised remote could otherwise swap in a malicious command for you to paste.
- **Downloads:** held in browser memory until complete, so multi-GB files may fail. Uploads stream to disk.
- **Keyboard:** uses a US-layout physical key map, with other characters typed directly. The browser keeps a few shortcuts for itself (Cmd+W, Cmd+Q, Cmd+Tab).
- **Windows:** admin (UAC) prompts and the login screen need the agent to run as administrator or as a service.
- **macOS:** works while a user is logged in.
- **Relayed video:** video relayed through the built-in Tailscale Funnel is subject to Tailscale's bandwidth limits. Direct connections aren't.
- **Windows encoding:** the bundled ffmpeg encodes with Media Foundation (`h264_mf`). If that fails on a particular PC, HopDesk falls back to JPEG frames. For the best quality, install a full ffmpeg build with x264 next to `HopDesk.exe`.
- **Signing:** releases aren't code-signed unless you add your own certificates (see [Building](building.md#code-signing-optional-paid)).
