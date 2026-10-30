package integration

import (
	"context"
	"errors"
	"time"
)

// Picturer is an optional interface for an Instance that serves a picture of
// one of its resources: a camera's latest frame. The hub fetches it on a
// tile's behalf (GET /api/integrations/instances/{id}/picture), so the
// browser never talks to the source and never holds its credential.
//
// Picture runs beside Sync, on another goroutine: it may only use what is
// safe to share (the HTTP client, fields set at Connect, state behind a
// lock). It returns ErrNoPicture when the instance has no picture of rid:
// not its resource, not a camera, or pictures are turned off in its config.
type Picturer interface {
	Picture(ctx context.Context, rid string, opts PictureOptions) (Picture, error)
}

// PictureOptions say how big a picture the tile wants.
type PictureOptions struct {
	// Height in pixels, scaled by the source when it can. 0: as the source
	// has it.
	Height int
}

// Picture is one still image.
type Picture struct {
	Body        []byte
	ContentType string // image/jpeg, image/webp or image/png
	At          time.Time
}

// MaxPictureBytes bounds a picture: a source answering more is refused.
const MaxPictureBytes = 4 << 20

// ErrNoPicture is Picture's answer when the instance has no picture of the
// resource.
var ErrNoPicture = errors.New("integration: no picture of that resource")
