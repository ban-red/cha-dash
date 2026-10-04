package core

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"
)

// State is a node's place in its lifecycle.
type State string

const (
	Pending  State = "pending"
	Approved State = "approved"
	Revoked  State = "revoked"
)

// ErrBadToken covers unknown, expired and spent join tokens alike, so a
// caller can't tell which.
var ErrBadToken = errors.New("join token invalid, expired or already used")

// Record is the hub's record of one node. The real hub keeps it in SQLite.
type Record struct {
	ID    string
	Key   []byte // pinned public key
	Next  []byte // key the node is rotating to; accepted until first seen
	State State
}

// Registry holds join tokens and nodes. Tokens are stored as SHA-256 hashes,
// like browser sessions, so a database leak doesn't leak a usable token.
type Registry struct {
	Now func() time.Time

	mu      sync.Mutex
	tokens  map[[32]byte]time.Time // hash → expiry
	nodes   map[string]*Record
	changed chan struct{} // closed on every change, then replaced
}

func NewRegistry() *Registry {
	return &Registry{Now: time.Now, tokens: map[[32]byte]time.Time{}, nodes: map[string]*Record{}, changed: make(chan struct{})}
}

// MintToken returns a fresh single-use secret valid for ttl.
func (r *Registry) MintToken(ttl time.Duration) string {
	secret := rand.Text() // 26 base32 characters, 130 bits
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[sha256.Sum256([]byte(secret))] = r.Now().Add(ttl)
	return secret
}

// Enroll spends the token and records key as a pending node. The check and
// the spend happen under one lock, so a token can't be used twice.
func (r *Registry) Enroll(secret string, key []byte) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := sha256.Sum256([]byte(secret))
	exp, ok := r.tokens[h]
	if !ok || !r.Now().Before(exp) {
		return Record{}, ErrBadToken
	}
	if n := r.lookupLocked(key); n != nil {
		return Record{}, errors.New("key already enrolled")
	}
	delete(r.tokens, h)
	n := &Record{ID: "n_" + strings.ToLower(rand.Text()[:10]), Key: key, State: Pending}
	r.nodes[n.ID] = n
	r.notifyLocked()
	return *n, nil
}

// Lookup finds the node a key belongs to. The first sighting of a node's Next
// key completes its rotation: Next becomes Key and the old key stops working.
func (r *Registry) Lookup(key []byte) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.lookupLocked(key)
	if n == nil {
		return Record{}, false
	}
	if n.Next != nil && bytes.Equal(n.Next, key) {
		n.Key, n.Next = n.Next, nil
		r.notifyLocked()
	}
	return *n, true
}

func (r *Registry) lookupLocked(key []byte) *Record {
	for _, n := range r.nodes {
		if bytes.Equal(n.Key, key) || (n.Next != nil && bytes.Equal(n.Next, key)) {
			return n
		}
	}
	return nil
}

// Get returns a node by ID.
func (r *Registry) Get(id string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return Record{}, false
	}
	return *n, true
}

// List returns every node in the given state.
func (r *Registry) List(s State) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Record
	for _, n := range r.nodes {
		if n.State == s {
			out = append(out, *n)
		}
	}
	return out
}

func (r *Registry) set(id string, f func(*Record) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return errors.New("unknown node")
	}
	if err := f(n); err != nil {
		return err
	}
	r.notifyLocked()
	return nil
}

func (r *Registry) approve(id string) error {
	return r.set(id, func(n *Record) error {
		if n.State != Pending {
			return errors.New("node is not pending")
		}
		n.State = Approved
		return nil
	})
}

func (r *Registry) revoke(id string) error {
	return r.set(id, func(n *Record) error { n.State, n.Next = Revoked, nil; return nil })
}

func (r *Registry) setNext(id string, key []byte) error {
	return r.set(id, func(n *Record) error {
		if n.State != Approved {
			return errors.New("node is not approved")
		}
		if o := r.lookupLocked(key); o != nil && o != n {
			return errors.New("key belongs to another node")
		}
		n.Next = key
		return nil
	})
}

// Changed returns a channel that closes on the next change.
func (r *Registry) Changed() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changed
}

func (r *Registry) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}
