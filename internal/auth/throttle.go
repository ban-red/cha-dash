package auth

import (
	"sync"
	"time"
)

// maxNotes bounds each throttle's memory.
const maxNotes = 4096

// noteThrottle lets one record per key through each period and counts the
// ones it held back, so the next record can say how many it stands for. A
// full map forgets its oldest key rather than refusing a new one: a flood of
// new keys can't silence a record that's due.
type noteThrottle struct {
	mu    sync.Mutex
	notes map[string]*note
}

type note struct {
	at   time.Time
	held int
}

// allow reports whether key may record now and, when it may, how many
// occurrences the record stands for: this one and those held back since
// the last record. The zero value is ready to use.
func (t *noteThrottle) allow(key string, now time.Time, every time.Duration) (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.notes == nil {
		t.notes = map[string]*note{}
	}
	if n, ok := t.notes[key]; ok {
		if now.Sub(n.at) < every {
			n.held++
			return false, 0
		}
		count := n.held + 1
		n.at, n.held = now, 0
		return true, count
	}
	if len(t.notes) >= maxNotes {
		var oldest string
		var at time.Time
		for k, n := range t.notes {
			if oldest == "" || n.at.Before(at) {
				oldest, at = k, n.at
			}
		}
		delete(t.notes, oldest)
	}
	t.notes[key] = &note{at: now}
	return true, 1
}
