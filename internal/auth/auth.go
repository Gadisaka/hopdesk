// Package auth implements hopdesk's connection authentication. It uses only
// the Go standard library, and the viewer implements the same steps with
// WebCrypto (cmd/server/web/viewer.js).
//
// After the WebRTC connection is up, over its encrypted data channel:
//
//	viewer -> agent: {t:"auth1", nonce: nV}
//	agent  -> viewer: {t:"auth1", pub, sig, nonce: nA, salt, iter}
//	    sig = Ed25519(agentKey, "hopdesk-agent-v1\n" + binding + "\n" + nV)
//	viewer: checks sig, and checks pub against the key it saved for this
//	        device last time (first time: the user compares the fingerprint).
//	        Only then:
//	viewer -> agent: {t:"auth2", proof}
//	    K     = PBKDF2-SHA256(password, salt, iter, 32)
//	    proof = HMAC-SHA256(K, "hopdesk-viewer-v1\n" + binding + "\n" + nV + "\n" + nA)
//	agent  -> viewer: {t:"auth", ok}
//
// binding = "dtls:" + viewer's DTLS fingerprint + "," + agent's DTLS fingerprint,
// read from each side's view of the SDP. A man in the middle (a hostile
// server, or anyone on a plain ws:// path) terminates DTLS twice with its own
// certificates, so the two sides compute different bindings and both the
// signature and the proof fail. An impostor agent can't sign as the real
// agent, and the viewer stops before sending anything derived from the
// password. The password itself is never sent.
package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// DefaultIter is the PBKDF2 iteration count (OWASP 2023 guidance for SHA-256).
const DefaultIter = 600_000

var fpRe = regexp.MustCompile(`(?im)^a=fingerprint:sha-256 ([0-9a-f:]+)\s*$`)

// DTLSFingerprint returns the SHA-256 DTLS certificate fingerprint in an SDP.
func DTLSFingerprint(sdp string) (string, error) {
	m := fpRe.FindStringSubmatch(sdp)
	if m == nil {
		return "", errors.New("no sha-256 fingerprint in SDP")
	}
	return strings.ToUpper(m[1]), nil
}

// Binding ties authentication to this exact WebRTC connection.
func Binding(viewerSDP, agentSDP string) (string, error) {
	v, err := DTLSFingerprint(viewerSDP)
	if err != nil {
		return "", err
	}
	a, err := DTLSFingerprint(agentSDP)
	if err != nil {
		return "", err
	}
	return "dtls:" + v + "," + a, nil
}

func agentMessage(binding, viewerNonce string) []byte {
	return []byte("hopdesk-agent-v1\n" + binding + "\n" + viewerNonce)
}

// SignAgent proves the agent's identity for this connection.
func SignAgent(priv ed25519.PrivateKey, binding, viewerNonce string) []byte {
	return ed25519.Sign(priv, agentMessage(binding, viewerNonce))
}

// VerifyAgent checks an agent signature (the viewer does the same in JS).
func VerifyAgent(pub ed25519.PublicKey, binding, viewerNonce string, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, agentMessage(binding, viewerNonce), sig)
}

// DeriveKey stretches the password so a stolen proof or key file is slow to brute-force.
func DeriveKey(password string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, iter, 32)
}

// Proof is what the viewer sends to show it knows the password.
func Proof(key []byte, binding, viewerNonce, agentNonce string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("hopdesk-viewer-v1\n" + binding + "\n" + viewerNonce + "\n" + agentNonce))
	return m.Sum(nil)
}

// VerifyProof checks the viewer's proof in constant time.
func VerifyProof(key []byte, binding, viewerNonce, agentNonce string, proof []byte) bool {
	return hmac.Equal(Proof(key, binding, viewerNonce, agentNonce), proof)
}

// Fingerprint is the short, human-comparable form of an agent's public key,
// e.g. "3F2A 9C1B 77D0 E4A5 0B6C 1D2E 8F90 A1B2" (128 bits of SHA-256).
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	s := strings.ToUpper(hex.EncodeToString(h[:16]))
	var parts []string
	for i := 0; i < len(s); i += 4 {
		parts = append(parts, s[i:i+4])
	}
	return strings.Join(parts, " ")
}
