package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"v0.4.1", "v0.4.0", true},
		{"v0.4.1", "v0.4.0-5-ga49f1dd", true}, // a dev build still gets the next release
		{"v0.4.0", "v0.4.0-5-ga49f1dd", false},
		{"v0.4.0", "v0.4.0", false},
		{"v0.3.9", "v0.4.0", false}, // never downgrade
		{"v1.0.0", "v0.9.12", true},
		{"v0.10.0", "v0.9.0", true}, // numeric, not text, comparison
		{"v0.4.1", "dev", false},
		{"v0.4.1", "4469ea5", false},
		{"v0.4.1-2-gabc1234", "v0.4.0", false}, // never install a dev build
		{"garbage", "v0.4.0", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.cand, c.cur); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}

const goodManifest = `{"version":"v0.5.0","assets":{"darwin":{"name":"HopDesk-v0.5.0-macOS.dmg","sha256":"` +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","size":123}}}`

func TestSignAndVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	sig := Sign(priv, []byte(goodManifest))

	m, err := Verify(pubB64, []byte(goodManifest), sig)
	if err != nil || m.Version != "v0.5.0" || m.Assets[PlatformMac].Size != 123 {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	tampered := strings.Replace(goodManifest, "v0.5.0", "v9.9.9", 1)
	if _, err := Verify(pubB64, []byte(tampered), sig); err == nil {
		t.Fatal("tampered manifest accepted")
	}

	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Verify(base64.StdEncoding.EncodeToString(otherPub), []byte(goodManifest), sig); err == nil {
		t.Fatal("manifest signed by another key accepted")
	}

	// A signature over the bare bytes (without the HopDesk context) must not pass.
	raw := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(goodManifest)))
	if _, err := Verify(pubB64, []byte(goodManifest), raw); err == nil {
		t.Fatal("signature without context accepted")
	}

	if _, err := Verify("", []byte(goodManifest), sig); err == nil {
		t.Fatal("empty public key accepted")
	}
}

func TestManifestRejectsUnsafeNames(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	for _, name := range []string{"../evil.dmg", "a/b.dmg", ".hidden", ""} {
		bad := strings.Replace(goodManifest, "HopDesk-v0.5.0-macOS.dmg", name, 1)
		if _, err := Verify(pub, []byte(bad), Sign(priv, []byte(bad))); err == nil {
			t.Errorf("unsafe asset name %q accepted", name)
		}
	}
	noCheck := strings.Replace(goodManifest, strings.Repeat("a", 64), "abc", 1)
	if _, err := Verify(pub, []byte(noCheck), Sign(priv, []byte(noCheck))); err == nil {
		t.Error("short checksum accepted")
	}
}
