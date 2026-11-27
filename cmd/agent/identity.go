package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"hopdesk/internal/auth"
)

// agentState is stored in the user's config folder (macOS: ~/Library/Application
// Support/hopdesk/agent.json, Windows: %AppData%\hopdesk\agent.json), readable
// only by this user. It holds the agent's identity key and, if the agent made
// up the password itself, the stretched password key (never the password).
type agentState struct {
	Seed []byte `json:"seed"`          // Ed25519 private key seed
	Salt []byte `json:"salt"`          // PBKDF2 salt
	Iter int    `json:"iter"`          // PBKDF2 iterations
	Key  []byte `json:"key,omitempty"` // PBKDF2(password), for generated passwords
}

func statePath() string {
	if *stateFile != "" {
		return *stateFile
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "hopdesk", "agent.json")
}

func loadState() (*agentState, error) {
	p := statePath()
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		st := &agentState{Seed: make([]byte, ed25519.SeedSize), Salt: make([]byte, 16), Iter: auth.DefaultIter}
		if _, err := rand.Read(st.Seed); err != nil {
			return nil, err
		}
		if _, err := rand.Read(st.Salt); err != nil {
			return nil, err
		}
		return st, st.save()
	}
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 && filepath.Separator == '/' {
		return nil, errors.New(p + " is readable by other users; run: chmod 600 " + p)
	}
	var st agentState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	if len(st.Seed) != ed25519.SeedSize || len(st.Salt) < 16 || st.Iter < 100_000 {
		return nil, errors.New(p + " is damaged; delete it to create a new identity")
	}
	return &st, nil
}

func (st *agentState) save() error {
	p := statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (st *agentState) private() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(st.Seed) }
func (st *agentState) public() ed25519.PublicKey {
	return st.private().Public().(ed25519.PublicKey)
}
