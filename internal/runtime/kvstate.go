package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
)

// checkState refuses a value integration.State doesn't accept.
func checkState(key string, value []byte) error {
	switch {
	case key == "":
		return errors.New("runtime: a state value needs a key")
	case len(value) > integration.MaxStateBytes:
		return fmt.Errorf("runtime: state %q is %d bytes, more than the %d allowed", key, len(value), integration.MaxStateBytes)
	case !json.Valid(value):
		return fmt.Errorf("runtime: state %q isn't JSON", key)
	}
	return nil
}

// dbState is a saved instance's integration.State: rows of the
// integration_state table, deleted with the instance row.
type dbState struct {
	st  *store.Store
	id  string
	now func() time.Time
}

func (s dbState) Load(ctx context.Context, key string) ([]byte, error) {
	var v string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM integration_state WHERE instance_id = ? AND key = ?`, s.id, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(v), nil
}

func (s dbState) Save(ctx context.Context, key string, value []byte) error {
	if err := checkState(key, value); err != nil {
		return err
	}
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO integration_state (instance_id, key, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (instance_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		s.id, key, string(value), s.now().UnixMilli())
	return err
}

// memState is an integration.State in memory.
type memState struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewMemoryState returns an integration.State that lives in memory: for an
// unsaved config under test, and for contract tests (integrationtest).
func NewMemoryState() integration.State { return &memState{m: map[string][]byte{}} }

func (s *memState) Load(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[key]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, nil
}

func (s *memState) Save(_ context.Context, key string, value []byte) error {
	if err := checkState(key, value); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = append([]byte(nil), value...)
	return nil
}
