# Automatic updates

HopDesk checks for a new release once a day. When one is ready, it shows **Update available** at the top of the settings page and in the menu bar or tray. One click on **Install update** downloads it, checks that it's genuine, installs it and restarts HopDesk. Anyone connected is disconnected for a few seconds and can reconnect straight away.

The daily check can be turned off on the settings page (**Updates → Check for updates automatically**). **Check now** works either way.

## How it stays safe

An update can replace the program that controls your computer, so HopDesk only installs one that passes all of these checks:

1. **Signed manifest.** Every release includes `latest.json` (the version, plus each installer's file name, size and SHA-256 checksum) and `latest.json.sig`, an Ed25519 signature over it. The release workflow signs with a private key kept as a GitHub secret. HopDesk contains the matching public key and rejects anything signed with another key.
2. **Newer only.** The signed version must be newer than the running one, so an old release can't be replayed to downgrade you.
3. **Checksum.** The downloaded installer must match the signed size and SHA-256 exactly.
4. **On macOS:** the app inside must be `com.hopdesk.desktop` with an intact code signature. It replaces the old app with two renames in the same folder, so a failure leaves the old version in place.

Development builds (like `v0.4.0-5-ga49f1dd`) still receive the next real release, but releases are never replaced by development builds.

| System | What happens |
|---|---|
| macOS | Mounts the `.dmg`, swaps `HopDesk.app` in place and restarts. If the user can't write to the Applications folder, it opens the `.dmg` so they can drag it in instead. |
| Windows | Runs the installer silently (`/S /RELAUNCH`). It installs into the same folder and starts the new version. |

## Setting it up (once, for the maintainer)

1. **Make the signing key.**
   ```sh
   go run ./cmd/releasetool keygen
   ```
   This writes the private key to `hopdesk-update.key`, which git ignores, and the public key to `cmd/agent/update_key.go`. Commit `update_key.go`.
2. **Give the private key to GitHub** as the `HOPDESK_UPDATE_KEY` secret:
   ```sh
   gh secret set HOPDESK_UPDATE_KEY < hopdesk-update.key
   ```
   Or go to the repository's **Settings → Secrets and variables → Actions → New repository secret**. Keep a backup of the file somewhere safe, such as a password manager. If you lose it, installed copies can't verify future updates. Anyone who has it can publish an update.
3. **Optional, macOS: keep permissions across updates.** Run `bash scripts/make-signing-cert.sh --export`. It adds `MACOS_LOCAL_CERT_P12` and `MACOS_LOCAL_CERT_PASSWORD`, so releases are signed with the same certificate as your own builds. Without it, macOS asks for Screen Recording and Accessibility again after each update.
4. **Release as usual:** `git tag v1.2.3 && git push --tags`. The workflow builds the installers, signs `latest.json` and publishes everything.

Copies built before the public key was added don't update themselves. Install that first signed release by hand.
