package stacks

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A stack's env files aren't in git (.gitignore keeps them out, since they
// can hold secrets), so a restore from history would bring back the
// compose file without them. stack.remove moves them aside instead of
// deleting them, keyed by the commit the stack was at; restoring the stack
// puts the latest back. They're kept on the node, never sent to the hub,
// and pruned after keepRemoved.

const (
	removedDir  = metaDir + "/removed"
	keepRemoved = 30 * 24 * time.Hour
)

// envFile is a name .gitignore keeps out of history.
func envFile(name string) bool {
	return name == ".env" || strings.HasSuffix(name, ".env")
}

func (m *Module) removedPath(stack, head string) string {
	return filepath.Join(m.root, filepath.FromSlash(removedDir), stack, head)
}

// keepEnv moves the env files at the top of dir aside, for head. It
// returns their names; none is no error.
func (m *Module) keepEnv(stack, dir, head string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && envFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 || head == "" {
		return nil, nil
	}
	to := m.removedPath(stack, head)
	if err := os.RemoveAll(to); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return nil, err
	}
	for _, n := range names {
		if err := os.Rename(filepath.Join(dir, n), filepath.Join(to, n)); err != nil {
			return nil, err
		}
	}
	now := m.now()
	_ = os.Chtimes(to, now, now) // the prune reads this, on the node's clock
	return names, nil
}

// restoreEnv puts back the env files the stack's latest removal kept,
// whichever commit it's restored from, without overwriting a file that's
// there already. It returns the names it put back.
func (m *Module) restoreEnv(stack, dir string) ([]string, error) {
	base := filepath.Join(m.root, filepath.FromSlash(removedDir), stack)
	heads, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var from string
	var newest time.Time
	for _, h := range heads {
		if info, err := h.Info(); err == nil && h.IsDir() && (from == "" || info.ModTime().After(newest)) {
			from, newest = filepath.Join(base, h.Name()), info.ModTime()
		}
	}
	if from == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !envFile(e.Name()) {
			continue
		}
		to := filepath.Join(dir, e.Name())
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		if err := os.Rename(filepath.Join(from, e.Name()), to); err != nil {
			return names, err
		}
		names = append(names, e.Name())
	}
	_ = os.RemoveAll(from)
	_ = os.Remove(base)               // only if that was its last
	_ = os.Remove(filepath.Dir(base)) // and the removed folder, if empty
	return names, nil
}

// pruneRemoved deletes what keepEnv kept longer than keepRemoved ago.
func (m *Module) pruneRemoved() {
	base := filepath.Join(m.root, filepath.FromSlash(removedDir))
	stacks, err := os.ReadDir(base)
	if err != nil {
		return
	}
	now := m.now()
	for _, s := range stacks {
		if !s.IsDir() {
			continue
		}
		dir := filepath.Join(base, s.Name())
		heads, _ := os.ReadDir(dir)
		for _, h := range heads {
			if info, err := h.Info(); err == nil && h.IsDir() && now.Sub(info.ModTime()) > keepRemoved {
				if err := os.RemoveAll(filepath.Join(dir, h.Name())); err != nil {
					m.log.Warn("couldn't prune a removed stack's env files", "stack", s.Name(), "err", err)
				}
			}
		}
		_ = os.Remove(dir) // only if empty
	}
	_ = os.Remove(base)
}
