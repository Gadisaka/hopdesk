#!/usr/bin/env bash
# Creates a free, self-signed "HopDesk Local" code-signing certificate in your
# login keychain. Run it once on your Mac. After that, scripts/build-macos.sh
# signs every build with it, so macOS recognises each new build as the same app
# and Screen Recording and Accessibility stay switched on after updates.
#
# This only affects builds made on this Mac. It is not an Apple Developer ID:
# other Macs still see the app as unsigned (see docs/building.md).
set -euo pipefail

NAME="HopDesk Local"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"
OPENSSL=/usr/bin/openssl   # macOS's own LibreSSL writes .p12 files that `security` accepts

if security find-certificate -c "$NAME" "$KEYCHAIN" >/dev/null 2>&1; then
  echo "The \"$NAME\" certificate already exists. Nothing to do."
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cat > "$TMP/cert.cnf" <<EOF
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

"$OPENSSL" req -x509 -newkey rsa:2048 -nodes -days 3650 -config "$TMP/cert.cnf" \
  -keyout "$TMP/key.pem" -out "$TMP/cert.pem" 2>/dev/null
PASS="$("$OPENSSL" rand -hex 16)"
"$OPENSSL" pkcs12 -export -inkey "$TMP/key.pem" -in "$TMP/cert.pem" -name "$NAME" \
  -out "$TMP/cert.p12" -passout "pass:$PASS"

# -T lets codesign use the key without asking for your password on every build.
security import "$TMP/cert.p12" -k "$KEYCHAIN" -P "$PASS" -T /usr/bin/codesign >/dev/null

echo "Created the \"$NAME\" signing certificate in your login keychain."
echo "Now run: bash scripts/build-macos.sh"
echo "The first time, remove HopDesk from Screen Recording and Accessibility in System Settings"
echo "and grant them again. After that they stay on across rebuilds."
