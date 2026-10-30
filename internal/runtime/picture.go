package runtime

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
)

// Picture bounds.
const (
	// pictureTimeout bounds one Picturer.Picture call.
	pictureTimeout = 10 * time.Second
	// pictureTTL is how long a picture is served again, so several tabs on
	// one camera don't each ask the camera server.
	pictureTTL = 2 * time.Second
	// pictureCacheSize bounds the cache; the oldest entry goes first.
	pictureCacheSize = 64
	// MaxPictureHeight is the largest height a tile may ask for.
	MaxPictureHeight = 1080
)

// pictureTypes are the content types a picture may have.
var pictureTypes = map[string]bool{"image/jpeg": true, "image/webp": true, "image/png": true}

// PictureError is a picture the source couldn't give: it failed, or its
// answer wasn't an image the hub serves. Message is safe to show.
type PictureError struct {
	Message string
	Err     error
}

func (e *PictureError) Error() string { return e.Message }
func (e *PictureError) Unwrap() error { return e.Err }

type pictureKey struct {
	inst, rid string
	height    int
}

// pictureEntry is one fetch: in flight until done closes, then its result
// is served until at+pictureTTL.
type pictureEntry struct {
	done chan struct{}
	pic  integration.Picture
	err  error
	at   time.Time
}

// pictureCache shares fetches across callers for pictureTTL.
type pictureCache struct {
	mu      sync.Mutex
	entries map[pictureKey]*pictureEntry
	order   []pictureKey // oldest first
}

// Picture returns a still picture of resource rid from the instance id that
// owns it (a camera's latest frame), at most height pixels tall when the
// source can scale (0: as it has it; clamped to 0..MaxPictureHeight).
// Callers within pictureTTL share one fetch. Errors: ErrNotFound,
// ErrNotRunning (disabled, or not connected yet), integration.ErrNoPicture
// (no such picture, or the integration has none) and *PictureError.
func (m *Manager) Picture(ctx context.Context, id, rid string, height int) (integration.Picture, error) {
	mi := m.get(id)
	if mi == nil {
		return integration.Picture{}, ErrNotFound
	}
	if rid == "" {
		return integration.Picture{}, integration.ErrNoPicture
	}
	height = min(max(height, 0), MaxPictureHeight)
	key := pictureKey{id, rid, height}

	m.pics.mu.Lock()
	if m.pics.entries == nil {
		m.pics.entries = map[pictureKey]*pictureEntry{}
	}
	e, ok := m.pics.entries[key]
	if ok && !e.at.IsZero() && m.o.Now().Sub(e.at) >= pictureTTL {
		ok = false
	}
	if !ok {
		e = &pictureEntry{done: make(chan struct{})}
		m.pics.put(key, e, m.o.Now())
		m.pics.mu.Unlock()
		// The fetch outlives the first caller's request: others wait on it.
		e.pic, e.err = m.fetchPicture(context.WithoutCancel(ctx), mi, rid, height)
		m.pics.mu.Lock()
		e.at = m.o.Now()
		m.pics.mu.Unlock()
		close(e.done)
	} else {
		m.pics.mu.Unlock()
	}
	select {
	case <-e.done:
		return e.pic, e.err
	case <-ctx.Done():
		return integration.Picture{}, ctx.Err()
	}
}

// put adds e under key, drops expired entries and evicts the oldest past
// pictureCacheSize. The caller holds pc.mu.
func (pc *pictureCache) put(key pictureKey, e *pictureEntry, now time.Time) {
	order := pc.order[:0]
	for _, k := range pc.order {
		old := pc.entries[k]
		if k == key || (!old.at.IsZero() && now.Sub(old.at) >= pictureTTL) {
			delete(pc.entries, k)
			continue
		}
		order = append(order, k)
	}
	pc.order = append(order, key)
	pc.entries[key] = e
	for len(pc.order) > pictureCacheSize {
		delete(pc.entries, pc.order[0])
		pc.order = pc.order[1:]
	}
}

// fetchPicture asks mi's coordinator and checks the answer.
func (m *Manager) fetchPicture(ctx context.Context, mi *managed, rid string, height int) (integration.Picture, error) {
	mi.life.Lock()
	c := mi.coord
	mi.life.Unlock()
	if c == nil {
		return integration.Picture{}, ErrNotRunning
	}
	pic, err := c.picture(ctx, rid, integration.PictureOptions{Height: height})
	fail := func(msg string, err error) (integration.Picture, error) {
		c.log.Debug("serving a picture failed", "resource", rid, "err", c.red.scrub(err.Error()))
		return integration.Picture{}, &PictureError{Message: msg, Err: err}
	}
	switch {
	case errors.Is(err, ErrNotRunning), errors.Is(err, integration.ErrNoPicture):
		return integration.Picture{}, err
	case err != nil:
		return fail(fmt.Sprintf("Cha Dash couldn't get the picture from %s.", c.name), err)
	}
	mt, _, perr := mime.ParseMediaType(pic.ContentType)
	switch {
	case perr != nil || !pictureTypes[mt]:
		return fail(fmt.Sprintf("%s answered with something that isn't a picture.", c.name),
			fmt.Errorf("content type %q", pic.ContentType))
	case len(pic.Body) == 0:
		return fail(fmt.Sprintf("%s answered with an empty picture.", c.name), errors.New("empty body"))
	case len(pic.Body) > integration.MaxPictureBytes:
		return fail(fmt.Sprintf("%s answered with a picture larger than Cha Dash accepts.", c.name),
			fmt.Errorf("%d bytes", len(pic.Body)))
	}
	pic.ContentType = mt
	return pic, nil
}

// handlePicture serves GET /api/integrations/instances/{id}/picture.
func (m *Manager) handlePicture(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	height := 0
	if h := q.Get("h"); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "h must be a whole number of pixels")
			return
		}
		height = n
	}
	pic, err := m.Picture(r.Context(), r.PathValue("id"), q.Get("resource"), height)
	var pe *PictureError
	switch {
	case errors.Is(err, integration.ErrNoPicture):
		httpx.Error(w, http.StatusNotFound, "no_picture", "there's no picture of that resource")
		return
	case errors.As(err, &pe):
		httpx.Error(w, http.StatusBadGateway, "upstream", pe.Message)
		return
	case r.Context().Err() != nil:
		return // the caller left
	case err != nil:
		m.writeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", pic.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(pic.Body)))
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if !pic.At.IsZero() {
		h.Set("Last-Modified", pic.At.UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusOK)
	w.Write(pic.Body)
}
