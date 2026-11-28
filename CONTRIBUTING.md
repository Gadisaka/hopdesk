# Contributing to HopDesk

Thanks for helping. Bug reports, ideas, docs fixes and pull requests are all welcome.

## Reporting bugs

[Open an issue](https://github.com/gadisaka/hopdesk/issues/new) with:
- your OS and version on both sides, and the browser you used
- what you did, what you expected, and what happened
- the relevant lines from the log (**Open log** on the settings page). Remove passwords, keys and addresses first.

For security problems, follow [SECURITY.md](SECURITY.md) instead.

## Development setup

```sh
git clone https://github.com/gadisaka/hopdesk.git && cd hopdesk
go build ./...
go test ./...
```

Before opening a pull request, also run the browser tests (see [docs/testing.md](docs/testing.md)):

```sh
go build -o hopdesk-server ./cmd/server
python3 test/e2e/run.py && python3 test/e2e/crosscheck.py
```

## Guidelines

- **Keep it small.** Avoid new dependencies unless they pull real weight. The auth code uses the standard library only.
- **Format** with `gofmt`, and keep platform code in `_darwin.go` / `_windows.go` files.
- **Shared UI files** (`ui.css`, `theme.js`, `ui.js`, `help.html`, `help.js`) live in `internal/server/web` and are copied into `cmd/agent/appui`. Change both copies together.
- **No inline scripts.** The pages run under a strict Content-Security-Policy.
- **Security-relevant changes** (auth, protocol, server limits, the settings page) need a test, and a note in the pull request explaining the threat being addressed.
- **Write for everyone.** UI text and the guide should make sense to someone who has never used a remote desktop.
