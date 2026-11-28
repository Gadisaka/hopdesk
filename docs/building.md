# Building

## From source

You need [Go](https://go.dev/dl/) (the version in [`go.mod`](../go.mod)). The Mac agent also needs the Xcode command line tools (`xcode-select --install`).

```sh
go build -o hopdesk-server ./cmd/server                                   # server (pure Go)
GOOS=linux GOARCH=amd64 go build -o hopdesk-server-linux ./cmd/server     # server for a Linux VPS
go build -o hopdesk-agent ./cmd/agent                                     # Mac agent (build on the Mac)
GOOS=windows GOARCH=amd64 go build -o hopdesk-agent.exe ./cmd/agent       # Windows agent (from any OS)
```

Run the agent with no arguments to start it as the desktop app, or with flags to use the [command line](command-line.md).

## ffmpeg

H.264 video needs ffmpeg on the **controlled** computer. Without it, HopDesk falls back to JPEG frames.

- **macOS:** `brew install ffmpeg`. HopDesk uses the hardware encoder (`h264_videotoolbox`).
- **Windows:** the installer bundles an [LGPL build of ffmpeg](https://github.com/BtbN/FFmpeg-Builds) (its license is installed next to it) and encodes with Media Foundation (`h264_mf`). You can also run `winget install Gyan.FFmpeg`.

The agent log says which mode it picked, for example `video: H.264 with ffmpeg encoder h264_videotoolbox`.

## Installers

### On GitHub (automatic)

Push a version tag, and the [release workflow](../.github/workflows/release.yml) builds the macOS `.dmg` and the Windows installer, then publishes them in a GitHub Release:

```sh
git tag v1.0.0 && git push --tags
```

You can also run the **Build installers** workflow by hand from the Actions tab, to get the installers as downloadable artifacts.

### On your Mac

```sh
bash scripts/build-macos.sh                               # → dist/HopDesk-<version>-macOS.dmg (universal: Apple silicon + Intel)
brew install makensis && bash scripts/build-windows.sh    # → dist/HopDesk-<version>-Windows-Setup.exe
```

Set `WITH_FFMPEG=0` to build the Windows installer without ffmpeg.

## Keeping permissions across rebuilds (free)

macOS ties Screen Recording and Accessibility to the exact signature of the app. Ad-hoc signed builds get a new signature every time, so macOS forgets both permissions after each rebuild. To fix that on your own Mac, create a free self-signed certificate once:

```sh
bash scripts/make-signing-cert.sh
```

From then on, `build-macos.sh` signs with it automatically. The first time, remove HopDesk from **Screen & System Audio Recording** and **Accessibility** in System Settings and grant them again. After that, they stay on across rebuilds. This only helps on the Mac that builds the app: other Macs still see an unsigned app.

## Code signing (optional, paid)

Without signing, macOS asks you to click **Open Anyway** the first time, and Windows SmartScreen shows a warning. On a Mac, permissions are also tied to the exact build, so after an update you may need to switch Screen Recording and Accessibility off and on again.

With an Apple Developer ID, set these repository secrets: `MACOS_SIGN_IDENTITY`, `APPLE_ID`, `APPLE_TEAM_ID` and `APPLE_APP_PASSWORD`. The workflow then signs and notarizes the app, so there's no "Open Anyway" step and permissions survive updates.
