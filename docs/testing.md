# Testing

```sh
go test ./...                                   # auth, lockouts, URL policy, file names, identity file, settings-page security
go build -o hopdesk-server ./cmd/server
pip install playwright && playwright install chromium
python3 test/e2e/run.py                         # 37 end-to-end checks, including attacks
python3 test/e2e/crosscheck.py                  # Go and the browser agree on the crypto
```

[`test/e2e/run.py`](../test/e2e/run.py) starts the real server and drives the real viewer in Chrome, against a stand-in agent that speaks the full agent protocol. It covers:

- **Features:** video, mouse position, keyboard, clipboard, quality settings, the file browser, byte-exact uploads and downloads, the first-run tours and the guide page.
- **Attacks:** an impostor agent, a man in the middle, cross-site WebSockets, connection floods, agent-key guessing, wrong passwords, clickjacking headers, and running the viewer over plain HTTP.

Set `SCREENSHOTS=docs/images` to refresh the screenshots.

## Not covered automatically

The tests don't cover the agent's screen capture, input injection, ffmpeg, the macOS permission prompts or Tailscale on a real desktop. Check those by hand on a Mac and a Windows PC before a release.
