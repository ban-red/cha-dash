package nodes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// hubKeys are the hub's identities, newest last. The private keys are
// sealed in the vault (node/hub-key/<id>); node_hub_keys lists them.
type hubKeys struct {
	st    *store.Store
	vault *vault.Vault
	now   func() time.Time

	mu   sync.Mutex
	keys []hubKey
}

type hubKey struct {
	id string
	nodeproto.Identity
}

func vaultName(id string) string { return "node/hub-key/" + id }

// load reads the live keys, making the first on a new hub.
func (k *hubKeys) load(ctx context.Context) error {
	rows, err := k.st.Reader().QueryContext(ctx, `SELECT id FROM node_hub_keys WHERE retired_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var keys []hubKey
	for _, id := range ids {
		b, err := k.vault.Get(ctx, vaultName(id))
		if err != nil {
			return fmt.Errorf("nodes: the hub's node key %s: %w", id, err)
		}
		key, err := nodeproto.DecodeKey(b)
		if err != nil {
			return fmt.Errorf("nodes: the hub's node key %s: %w", id, err)
		}
		ident, err := nodeproto.NewIdentity(key)
		if err != nil {
			return err
		}
		keys = append(keys, hubKey{id: id, Identity: ident})
	}
	k.mu.Lock()
	k.keys = keys
	k.mu.Unlock()
	if len(keys) == 0 {
		_, err := k.add(ctx)
		return err
	}
	return nil
}

// add makes a new newest key.
func (k *hubKeys) add(ctx context.Context) (string, error) {
	key, err := nodeproto.GenerateKey()
	if err != nil {
		return "", err
	}
	ident, err := nodeproto.NewIdentity(key)
	if err != nil {
		return "", err
	}
	pem, err := nodeproto.EncodeKey(key)
	if err != nil {
		return "", err
	}
	id := "hk_" + strings.ToLower(rand.Text())
	err = k.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := k.vault.PutTx(ctx, tx, vaultName(id), pem); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO node_hub_keys (id, pin, created_at) VALUES (?, ?, ?)`, id, ident.Pin, k.now().UnixMilli())
		return err
	})
	if err != nil {
		return "", fmt.Errorf("nodes: storing a hub key: %w", err)
	}
	k.mu.Lock()
	k.keys = append(k.keys, hubKey{id: id, Identity: ident})
	k.mu.Unlock()
	return ident.Pin, nil
}

// newest is the pin of the newest key.
func (k *hubKeys) newest() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys[len(k.keys)-1].Pin
}

// has reports whether pin is one of the hub's live keys.
func (k *hubKeys) has(pin string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.ContainsFunc(k.keys, func(key hubKey) bool { return key.Pin == pin })
}

// pick is the identity to present to a node that trusts accepted: the
// newest key among them, else the newest key (the node will refuse it, and
// that's the right answer for a node that pins none of ours).
func (k *hubKeys) pick(accepted []string) nodeproto.Identity {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, key := range slices.Backward(k.keys) {
		if slices.Contains(accepted, key.Pin) {
			return key.Identity
		}
	}
	return k.keys[len(k.keys)-1].Identity
}

// retireOld retires every key but the newest and drops their private
// halves from the vault.
func (k *hubKeys) retireOld(ctx context.Context) (int, error) {
	k.mu.Lock()
	old := slices.Clone(k.keys[:len(k.keys)-1])
	k.mu.Unlock()
	now := k.now().UnixMilli()
	for _, key := range old {
		if _, err := k.st.Writer().ExecContext(ctx, `UPDATE node_hub_keys SET retired_at = ? WHERE id = ?`, now, key.id); err != nil {
			return 0, fmt.Errorf("nodes: retiring a hub key: %w", err)
		}
		if err := k.vault.Delete(ctx, vaultName(key.id)); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return 0, fmt.Errorf("nodes: retiring a hub key: %w", err)
		}
	}
	k.mu.Lock()
	k.keys = k.keys[len(old):]
	k.mu.Unlock()
	return len(old), nil
}
