package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Credential struct {
	Email        string    `json:"email"`
	Issuer       string    `json:"issuer"`
	Subject      string    `json:"subject"`
	ClientID     string    `json:"client_id"`
	HostID       string    `json:"ext_agent_host_id"`
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Scopes       string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type State struct {
	HostID     string             `json:"host_id"`
	LocalKey   string             `json:"local_key"`
	AdminKey   string             `json:"admin_key"`
	APIPort    int                `json:"api_port"`
	LANEnabled bool               `json:"lan_enabled"`
	APIEnabled bool               `json:"api_enabled"`
	Credential *Credential        `json:"credential,omitempty"`
	Providers  []ExternalProvider `json:"providers,omitempty"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomHostID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return "urn:uuid:" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func defaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "local-siwc-provider", "state.json"), nil
}

func newStore(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = defaultStatePath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	s := &Store{path: path}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("credential store must be a regular, owner-only file (0600)")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &s.state); e != nil {
			return nil, fmt.Errorf("invalid state file: %w", e)
		}
		if s.state.HostID == "" || s.state.LocalKey == "" {
			return nil, errors.New("state missing host ID or local key")
		}
		// Migrate existing installations without changing the local API key or OAuth credentials.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			return nil, err
		}
		changed := false
		if _, ok := fields["api_port"]; !ok {
			s.state.APIPort = defaultAPIPort
			changed = true
		}
		if _, ok := fields["lan_enabled"]; !ok {
			s.state.LANEnabled = true
			changed = true
		}
		if s.state.AdminKey == "" {
			s.state.AdminKey, err = randomString(32)
			if err != nil {
				return nil, err
			}
			changed = true
		}
		if s.state.APIPort == 0 {
			s.state.APIPort = defaultAPIPort
			changed = true
		}
		if s.state.APIPort < 0 || s.state.APIPort > 65535 {
			return nil, errors.New("invalid saved API port")
		}
		if changed {
			if err = s.saveLocked(); err != nil {
				return nil, err
			}
		}
	} else if os.IsNotExist(err) {
		x, e := randomHostID()
		if e != nil {
			return nil, e
		}
		y, e := randomString(32)
		if e != nil {
			return nil, e
		}
		adminKey, e := randomString(32)
		if e != nil {
			return nil, e
		}
		s.state = State{HostID: x, LocalKey: y, AdminKey: adminKey, APIPort: defaultAPIPort, LANEnabled: true}
		if e = s.saveLocked(); e != nil {
			return nil, e
		}
	} else {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

func (s *Store) get() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state
	if v.Credential != nil {
		c := *v.Credential
		v.Credential = &c
	}
	if v.Providers != nil {
		v.Providers = append([]ExternalProvider(nil), v.Providers...)
		for i := range v.Providers {
			v.Providers[i].ManualModels = append([]string(nil), v.Providers[i].ManualModels...)
		}
	}
	return v
}

// upsertProvider saves an independent provider without changing SIWC credentials.
// Empty APIKey during edits preserves the previously saved key.
func (s *Store) upsertProvider(p ExternalProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := append([]ExternalProvider(nil), s.state.Providers...)
	found := false
	for i := range s.state.Providers {
		if s.state.Providers[i].ID == p.ID {
			found = true
			if p.APIKey == "" {
				if p.BaseURL != s.state.Providers[i].BaseURL {
					if p.Kind != "local-openai-compatible" {
						return errors.New("API key must be re-entered when provider URL changes")
					}
					// Never copy a token to a different local network endpoint.
				} else {
					p.APIKey = s.state.Providers[i].APIKey
				}
			}
			s.state.Providers[i] = p
			break
		}
	}
	if !found {
		if len(s.state.Providers) >= 16 {
			return errors.New("maximum 16 external providers")
		}
		if p.APIKey == "" && p.Kind != "local-openai-compatible" {
			return errors.New("API key is required for a new cloud provider")
		}
		s.state.Providers = append(s.state.Providers, p)
	}
	if err := validateExternalProvider(p); err != nil {
		s.state.Providers = before
		return err
	}
	if err := s.saveLocked(); err != nil {
		s.state.Providers = before
		return err
	}
	return nil
}

// reorderProviders changes only the priority of saved providers, preserving
// all secrets and endpoints. Reject stale/partial requests rather than dropping
// providers or treating a reorder request as a configuration replacement.
func (s *Store) reorderProviders(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.state.Providers
	if len(ids) != len(old) {
		return errors.New("provider order must include every configured provider exactly once")
	}
	byID := make(map[string]ExternalProvider, len(old))
	for _, p := range old {
		byID[p.ID] = p
	}
	next := make([]ExternalProvider, 0, len(old))
	seen := make(map[string]bool, len(old))
	for _, id := range ids {
		p, ok := byID[id]
		if !ok || seen[id] {
			return errors.New("invalid provider order: duplicate or unknown provider ID")
		}
		seen[id] = true
		next = append(next, p)
	}
	s.state.Providers = next
	if err := s.saveLocked(); err != nil {
		s.state.Providers = old
		return err
	}
	return nil
}

func (s *Store) deleteProvider(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := append([]ExternalProvider(nil), s.state.Providers...)
	for i, p := range s.state.Providers {
		if p.ID == id {
			s.state.Providers = append(s.state.Providers[:i], s.state.Providers[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.state.Providers = before
				return err
			}
			return nil
		}
	}
	return errors.New("provider not found")
}

func (s *Store) saveCredential(c *Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.state.Credential
	s.state.Credential = c
	if err := s.saveLocked(); err != nil {
		s.state.Credential = old
		return err
	}
	return nil
}

// saveLocalKey changes the Agent API credential only. The dashboard unlock key
// and OpenAI OAuth credentials are intentionally unaffected.
func (s *Store) saveLocalKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state.LocalKey
	s.state.LocalKey = key
	if err := s.saveLocked(); err != nil {
		s.state.LocalKey = prev
		return err
	}
	return nil
}

func (s *Store) saveAPISettings(port int, lan bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldPort, oldLAN := s.state.APIPort, s.state.LANEnabled
	s.state.APIPort, s.state.LANEnabled = port, lan
	if err := s.saveLocked(); err != nil {
		s.state.APIPort, s.state.LANEnabled = oldPort, oldLAN
		return err
	}
	return nil
}

func (s *Store) saveAPIEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state.APIEnabled
	s.state.APIEnabled = enabled
	if err := s.saveLocked(); err != nil {
		s.state.APIEnabled = prev
		return err
	}
	return nil
}
