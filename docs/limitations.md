# Limitations

- **Sound:** one way, from the remote computer to you (no microphone). It needs ffmpeg with an Opus encoder (Homebrew's ffmpeg and the bundled Windows build have one), and macOS 13 or later. On Windows it plays whatever the default output device plays.
- **Clipboard:** text only, up to about 60 KB. Text copied on the remote shows up with a **Copy here** button and never replaces your clipboard on its own, because a compromised remote could otherwise swap in a malicious command for you to paste.
- **Downloads:** held in browser memory until complete, so multi-GB files may fail. Uploads stream to disk.
- **Keyboard:** uses a US-layout physical key map, with other characters typed directly. The browser keeps a few shortcuts for itself (Cmd+W, Cmd+Q, Cmd+Tab).
- **Windows login screen and admin prompts:** need **Control the login screen and admin prompts** turned on in the settings page, which installs the HopDesk service (administrator permission needed). It follows the computer's own screen (the console), not Remote Desktop sessions. With it on, this computer can't also be the HopDesk server, and **Approve each connection** can't be answered at the login screen because nobody is signed in. After a HopDesk update, the settings page asks you to update the service's copy too.
- **macOS:** works while a user is logged in.
- **Relayed video:** video relayed through the built-in Tailscale Funnel is subject to Tailscale's bandwidth limits. Direct connections aren't.
- **Windows encoding:** HopDesk uses the graphics chip when it can: NVIDIA (`h264_nvenc`), Intel (`h264_qsv`), AMD (`h264_amf`) or Windows' hardware encoder (`h264_mf`). It tests each one when it starts and uses the first that works; the settings page shows which. Without one, it falls back to Windows' software encoder, which struggles at Ultra quality and 60 fps: choose High and 30 fps there, or put a full ffmpeg build with x264 next to `HopDesk.exe`.
- **Signing:** releases aren't code-signed unless you add your own certificates (see [Building](building.md#code-signing-optional-paid)).
