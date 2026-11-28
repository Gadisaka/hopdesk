// Package update describes HopDesk releases for automatic updates.
//
// Every release publishes latest.json (the manifest: version and installer
// checksums) and latest.json.sig (an Ed25519 signature over it). The release
// workflow signs with a private key kept in a GitHub secret. HopDesk embeds
// the matching public key and installs an update only if the signature is
// valid, the version is newer than its own, and the downloaded installer
// matches the checksum. Standard library only.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// sigContext is prepended to the manifest before signing, so a signature made
// for something else with the same key can never pass as a HopDesk manifest.
const sigContext = "hopdesk-update-manifest-v1\n"

// Platform keys used in Manifest.Assets.
const (
	PlatformMac     = "darwin"
	PlatformWindows = "windows"
)

// Asset is one installer in a release.
type Asset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is the content of latest.json.
type Manifest struct {
	Version   string           `json:"version"`
	Published string           `json:"published,omitempty"`
	NotesURL  string           `json:"notesUrl,omitempty"`
	Assets    map[string]Asset `json:"assets"`
}

var assetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Validate checks the manifest's shape: a parsable version and safe asset names.
func (m *Manifest) Validate() error {
	if _, ok := ParseVersion(m.Version); !ok {
		return fmt.Errorf("bad version %q", m.Version)
	}
	for p, a := range m.Assets {
		if !assetName.MatchString(a.Name) {
			return fmt.Errorf("bad file name for %s", p)
		}
		if b, err := hex.DecodeString(a.SHA256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("bad checksum for %s", p)
		}
		if a.Size <= 0 {
			return fmt.Errorf("bad size for %s", p)
		}
	}
	return nil
}

// Sign returns the base64 signature for the exact manifest bytes.
func Sign(priv ed25519.PrivateKey, manifest []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, append([]byte(sigContext), manifest...)))
}

// Verify checks the signature, then parses and validates the manifest.
func Verify(pubB64 string, manifest []byte, sigB64 string) (*Manifest, error) {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("this build has no valid update key")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("the update signature is malformed")
	}
	if !ed25519.Verify(pub, append([]byte(sigContext), manifest...), sig) {
		return nil, errors.New("the update signature is not valid")
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("the update description is unreadable: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("the update description is invalid: %w", err)
	}
	return &m, nil
}

// Version is a parsed release version. Ahead counts commits after the tag in
// development builds ("v0.4.0-5-ga49f1dd" is v0.4.0 plus 5 commits).
type Version struct {
	Major, Minor, Patch, Ahead int
}

var versionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:\.(\d+))?(?:-(\d+)-g[0-9a-f]+)?(?:-dirty)?$`)

// ParseVersion understands tags (v1.2.3, 1.2) and git-describe builds
// (v1.2.3-4-gabcdef0). Anything else ("dev", a bare commit hash) is not a version.
func ParseVersion(s string) (Version, bool) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, false
	}
	n := func(x string) int { v, _ := strconv.Atoi(x); return v }
	return Version{n(m[1]), n(m[2]), n(m[3]), n(m[4])}, true
}

// Compare returns -1, 0 or 1. A development build is newer than its tag but
// older than the next release.
func (a Version) Compare(b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}, {a.Ahead, b.Ahead}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v Version) String() string { return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch) }

// IsNewer reports whether candidate is a newer release than current. It is
// false if either cannot be parsed, so development builds never update.
func IsNewer(candidate, current string) bool {
	c, ok1 := ParseVersion(candidate)
	cur, ok2 := ParseVersion(current)
	return ok1 && ok2 && c.Ahead == 0 && c.Compare(cur) > 0
}

// SHA256Reader returns the hex SHA-256 of everything read from r.
func SHA256Reader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
