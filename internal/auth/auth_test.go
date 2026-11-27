package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const viewerSDP = "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\na=fingerprint:sha-256 aa:bb:cc:01\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"
const agentSDP = "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=fingerprint:sha-256 DD:EE:FF:02\r\n"

func setup(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, []byte, string) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	key, err := DeriveKey("correct-horse-42", []byte("0123456789abcdef"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Binding(viewerSDP, agentSDP)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv, key, b
}

func TestBinding(t *testing.T) {
	_, _, _, b := setup(t)
	if b != "dtls:AA:BB:CC:01,DD:EE:FF:02" {
		t.Fatalf("binding = %q", b)
	}
	if _, err := Binding("v=0\r\n", agentSDP); err == nil {
		t.Fatal("expected error for SDP without fingerprint")
	}
}

func TestHonestRun(t *testing.T) {
	pub, priv, key, b := setup(t)
	sig := SignAgent(priv, b, "nV")
	if !VerifyAgent(pub, b, "nV", sig) {
		t.Fatal("valid signature rejected")
	}
	if !VerifyProof(key, b, "nV", "nA", Proof(key, b, "nV", "nA")) {
		t.Fatal("valid proof rejected")
	}
}

// A man in the middle terminates DTLS separately with each side, so each
// side sees a different fingerprint pair.
func TestManInTheMiddleFails(t *testing.T) {
	pub, priv, key, real := setup(t)
	seenByViewer := strings.Replace(real, "DD:EE:FF:02", "66:66:66:66", 1) // attacker's cert toward the viewer
	if VerifyAgent(pub, seenByViewer, "nV", SignAgent(priv, real, "nV")) {
		t.Fatal("agent signature accepted on a different connection")
	}
	if VerifyProof(key, real, "nV", "nA", Proof(key, seenByViewer, "nV", "nA")) {
		t.Fatal("viewer proof accepted on a different connection")
	}
}

func TestImpostorAndReplayFail(t *testing.T) {
	pub, _, key, b := setup(t)
	_, other, _ := ed25519.GenerateKey(nil)
	if VerifyAgent(pub, b, "nV", SignAgent(other, b, "nV")) {
		t.Fatal("impostor key accepted")
	}
	if VerifyProof(key, b, "nV", "nA-new", Proof(key, b, "nV", "nA-old")) {
		t.Fatal("replayed proof accepted")
	}
	wrong, _ := DeriveKey("wrong-password", []byte("0123456789abcdef"), 1000)
	if VerifyProof(key, b, "nV", "nA", Proof(wrong, b, "nV", "nA")) {
		t.Fatal("wrong password accepted")
	}
}

func TestFingerprintFormat(t *testing.T) {
	pub, _, _, _ := setup(t)
	fp := Fingerprint(pub)
	if len(fp) != 39 || strings.Count(fp, " ") != 7 {
		t.Fatalf("fingerprint %q", fp)
	}
}

// TestWriteVector writes a fixed test vector so the browser implementation
// can be checked against this one: HOPDESK_VECTOR=path go test ./internal/auth
func TestWriteVector(t *testing.T) {
	path := os.Getenv("HOPDESK_VECTOR")
	if path == "" {
		t.Skip("set HOPDESK_VECTOR to write the vector")
	}
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	salt := []byte("hopdesk-test-salt")
	key, _ := DeriveKey("correct-horse-42", salt, 1000)
	b, _ := Binding(viewerSDP, agentSDP)
	enc := base64.StdEncoding.EncodeToString
	v := map[string]any{
		"viewerSDP": viewerSDP, "agentSDP": agentSDP, "binding": b,
		"pub": enc(pub), "fingerprint": Fingerprint(pub),
		"viewerNonce": "nV-123", "agentNonce": "nA-456",
		"sig":      enc(SignAgent(priv, b, "nV-123")),
		"password": "correct-horse-42", "salt": enc(salt), "iter": 1000,
		"proof": enc(Proof(key, b, "nV-123", "nA-456")),
	}
	data, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
