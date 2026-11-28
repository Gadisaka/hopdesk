# Video quality

The viewer starts at **High quality, 30 fps**. You can change both while connected, and the app's settings page sets the starting values.

| Quality | Resolution | H.264 bitrate at 30 fps |
|---|---|---|
| Low | up to 1280 px wide | 1.5 Mbps |
| Medium | up to 1920 px wide | 4 Mbps |
| High (default) | up to 2560 px wide | 8 Mbps |
| Ultra | native resolution (up to 4K) | 16 Mbps |

At 60 fps the bitrate goes up by half.

- **Profile:** on Chrome, Edge and Safari, HopDesk uses H.264 **High profile**, which is sharper at the same bitrate. Firefox gets Baseline.
- **Scaling:** screens are downscaled with a bilinear filter, which keeps small text readable.
- **Pacing:** frames are only sent as fast as the connection allows, so a slow link lowers the frame rate rather than adding lag.

## For the best picture

- **Install ffmpeg on the controlled computer.** The Mac app uses Homebrew's (`brew install ffmpeg`), and the Windows installer includes one. Without it, HopDesk falls back to JPEG frames.
- **Use a fast connection.** Ultra at 60 fps needs about 25 Mbps of upload on the controlled computer.
- **Prefer direct connections.** Sessions relayed through Tailscale Funnel are subject to Tailscale's bandwidth limits. See [Connecting across countries](connecting.md).
