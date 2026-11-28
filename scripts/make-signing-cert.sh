#!/usr/bin/env bash
# Creates a free, self-signed "HopDesk Local" code-signing certificate in your
# login keychain. scripts/build-macos.sh signs every build with it, so macOS
# recognises each new build as the same app and Screen Recording and
# Accessibility stay switched on after updates.
#
#   bash scripts/make-signing-cert.sh            create it (once per Mac)
#   bash scripts/make-signing-cert.sh --export   also give it to GitHub Actions, so the
#                                                releases (and automatic updates) are
#                                                signed with the same certificate
#
# A copy of the certificate is kept in ~/.hopdesk-signing (only you can read it).
# It is not an Apple Developer ID: other Macs still see the app as unsigned.
set -euo pipefail

NAME="HopDesk Local"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"
STORE="$HOME/.hopdesk-signing"
P12="$STORE/hopdesk-local.p12"
PASSFILE="$STORE/hopdesk-local.password"
OPENSSL=/usr/bin/openssl   # macOS's own LibreSSL writes .p12 files that `security` accepts
EXPORT=0
[ "${1:-}" = "--export" ] && EXPORT=1

have_cert() { security find-certificate -c "$NAME" "$KEYCHAIN" >/dev/null 2>&1; }

create() {
  mkdir -p "$STORE" && chmod 700 "$STORE"
  local tmp; tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  cat > "$tmp/cert.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $NAME
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF
  "$OPENSSL" req -x509 -newkey rsa:2048 -nodes -days 3650 -config "$tmp/cert.cnf" \
    -keyout "$tmp/key.pem" -out "$tmp/cert.pem" 2>/dev/null
  "$OPENSSL" rand -hex 16 > "$PASSFILE"
  chmod 600 "$PASSFILE"
  "$OPENSSL" pkcs12 -export -inkey "$tmp/key.pem" -in "$tmp/cert.pem" -name "$NAME" \
    -out "$P12" -passout "file:$PASSFILE"
  chmod 600 "$P12"
  # -T lets codesign use the key without asking for your password on every build.
  security import "$P12" -k "$KEYCHAIN" -P "$(cat "$PASSFILE")" -T /usr/bin/codesign >/dev/null
  echo "Created the \"$NAME\" signing certificate in your login keychain (copy in $STORE)."
}

if have_cert && [ -f "$P12" ]; then
  [ "$EXPORT" = 0 ] && { echo "The \"$NAME\" certificate already exists. Nothing to do."; exit 0; }
elif have_cert; then
  if [ "$EXPORT" = 0 ]; then
    echo "The \"$NAME\" certificate already exists. Nothing to do."
    exit 0
  fi
  # Made by an older version of this script, which didn't keep a copy to export.
  echo "Your \"$NAME\" certificate has no exportable copy, so it will be replaced once."
  echo "Afterwards, grant HopDesk Screen Recording and Accessibility again (one last time)."
  security delete-identity -c "$NAME" "$KEYCHAIN" >/dev/null 2>&1 || \
    security delete-certificate -c "$NAME" "$KEYCHAIN" >/dev/null
  create
else
  create
fi

if [ "$EXPORT" = 0 ]; then
  echo "Now run: bash scripts/build-macos.sh"
  echo "The first time, remove HopDesk from Screen Recording and Accessibility in System Settings"
  echo "and grant them again. After that they stay on across rebuilds."
  exit 0
fi

# Give the certificate to GitHub Actions.
B64="$STORE/hopdesk-local.p12.base64"
base64 < "$P12" | tr -d '\n' > "$B64"
chmod 600 "$B64"
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  gh secret set MACOS_LOCAL_CERT_P12 < "$B64"
  gh secret set MACOS_LOCAL_CERT_PASSWORD < "$PASSFILE"
  echo "Added MACOS_LOCAL_CERT_P12 and MACOS_LOCAL_CERT_PASSWORD to this repository's GitHub secrets."
else
  cat <<EOF
Add two secrets on GitHub (repository → Settings → Secrets and variables → Actions → New repository secret):
  MACOS_LOCAL_CERT_P12       = the contents of $B64
  MACOS_LOCAL_CERT_PASSWORD  = the contents of $PASSFILE
(Tip: pbcopy < "$B64" copies the first one.)
EOF
fi
echo "Rebuild with: bash scripts/build-macos.sh"
