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
	HostID     string      `json:"host_id"`
	LocalKey   string      `json:"local_key"`
	Credential *Credential `json:"credential,omitempty"`
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
	} else if os.IsNotExist(err) {
		x, e := randomHostID()
		if e != nil {
			return nil, e
		}
		y, e := randomString(32)
		if e != nil {
			return nil, e
		}
		s.state = State{HostID: x, LocalKey: y}
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
	return v
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
