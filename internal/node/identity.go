package node

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"dash.cha.sh/internal/nodeproto"
)

// Identity files, under <dir>/identity (ADR-0014):
//
//	node.key       the private key: PKCS#8 PEM, 0600, written atomically
//	node.next.key  the next key, only while a rotation is in flight
//	state.json     the node ID, the hub URL and the pinned hub keys
const (
	keyFile   = "node.key"
	nextFile  = "node.next.key"
	stateFile = "state.json"
	// maxHubPins bounds the hub keys kept: the newest few, so a rollover
	// window works without the list growing forever.
	maxHubPins = 4
)

// State is what the node remembers between runs.
type State struct {
	Node    string   `json:"node,omitempty"`
	HubURL  string   `json:"hub_url,omitempty"`
	HubPins []string `json:"hub_pins,omitempty"`
}

// IdentityStore holds the node's keys and state on disk.
type IdentityStore struct {
	dir string

	mu    sync.Mutex
	cur   nodeproto.Identity
	next  *nodeproto.Identity
	state State
}

// OpenIdentity loads the identity in dir (/etc/chadash-node/identity),
// creating the directory (0700) and a key on first run.
func OpenIdentity(dir string) (*IdentityStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("node: identity dir: %w", err)
	}
	s := &IdentityStore{dir: dir}
	key, err := nodeproto.ReadKeyFile(s.path(keyFile))
	if errors.Is(err, os.ErrNotExist) {
		if key, err = nodeproto.GenerateKey(); err == nil {
			err = nodeproto.WriteKeyFile(s.path(keyFile), key)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("node: identity key: %w", err)
	}
	if s.cur, err = nodeproto.NewIdentity(key); err != nil {
		return nil, err
	}
	if next, err := nodeproto.ReadKeyFile(s.path(nextFile)); err == nil {
		id, err := nodeproto.NewIdentity(next)
		if err != nil {
			return nil, err
		}
		s.next = &id
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("node: next key: %w", err)
	}
	b, err := os.ReadFile(s.path(stateFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("node: state: %w", err)
	default:
		if err := json.Unmarshal(b, &s.state); err != nil {
			return nil, fmt.Errorf("node: state.json: %w", err)
		}
	}
	return s, nil
}

func (s *IdentityStore) path(name string) string { return filepath.Join(s.dir, name) }

// Current is the identity the hub knows.
func (s *IdentityStore) Current() nodeproto.Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Next is the identity a rotation is moving to, if one is in flight.
func (s *IdentityStore) Next() (nodeproto.Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next == nil {
		return nodeproto.Identity{}, false
	}
	return *s.next, true
}

// State returns a copy of the state.
func (s *IdentityStore) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.HubPins = slices.Clone(st.HubPins)
	return st
}

// Update changes the state and saves it.
func (s *IdentityStore) Update(f func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.HubPins = slices.Clone(st.HubPins)
	f(&st)
	if len(st.HubPins) > maxHubPins {
		st.HubPins = st.HubPins[len(st.HubPins)-maxHubPins:]
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := nodeproto.WriteFileAtomic(s.path(stateFile), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("node: saving state: %w", err)
	}
	s.state = st
	return nil
}

// AddHubPin appends pin to the trusted hub keys (newest last).
func (s *IdentityStore) AddHubPin(pin string) error {
	if !nodeproto.ValidPin(pin) {
		return errors.New("node: malformed hub pin")
	}
	return s.Update(func(st *State) {
		st.HubPins = slices.DeleteFunc(st.HubPins, func(p string) bool { return p == pin })
		st.HubPins = append(st.HubPins, pin)
	})
}

// BeginRotate makes the next key and stores it as node.next.key. It
// returns its pin, which the node then announces to the hub.
func (s *IdentityStore) BeginRotate() (string, error) {
	key, err := nodeproto.GenerateKey()
	if err != nil {
		return "", err
	}
	id, err := nodeproto.NewIdentity(key)
	if err != nil {
		return "", err
	}
	if err := nodeproto.WriteKeyFile(s.path(nextFile), key); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.next = &id
	s.mu.Unlock()
	return id.Pin, nil
}

// CommitRotate makes the next key current: the hub has seen it.
func (s *IdentityStore) CommitRotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next == nil {
		return errors.New("node: no rotation in flight")
	}
	if err := os.Rename(s.path(nextFile), s.path(keyFile)); err != nil {
		return err
	}
	s.cur, s.next = *s.next, nil
	return nil
}

// AbortRotate drops the next key: the hub never learned it.
func (s *IdentityStore) AbortRotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = nil
	if err := os.Remove(s.path(nextFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PublicKey is the current key's public half.
func (s *IdentityStore) PublicKey() ed25519.PublicKey {
	return s.Current().Key.Public().(ed25519.PublicKey)
}
