package frigate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"dash.cha.sh/internal/integration"
)

// pictureTypes are the content types a picture may have.
var pictureTypes = map[string]bool{"image/jpeg": true, "image/webp": true, "image/png": true}

// Picture implements integration.Picturer: a camera's latest frame, for its
// tile. Only with pictures on in the config, and only for a camera this
// instance publishes (the role's allowed cameras). It runs beside Sync, so
// it reads the camera list under mu and otherwise uses only the HTTP client.
func (in *instance) Picture(ctx context.Context, rid string, opts integration.PictureOptions) (integration.Picture, error) {
	if !in.pictures {
		return integration.Picture{}, integration.ErrNoPicture
	}
	key, ok := strings.CutPrefix(rid, in.prefix+"/camera/")
	if !ok || key == "" || strings.Contains(key, "/") {
		return integration.Picture{}, integration.ErrNoPicture
	}
	in.mu.Lock()
	c := in.cams[key]
	known := in.haveConfig && c != nil && c.inConfig
	in.mu.Unlock()
	if !known {
		return integration.Picture{}, integration.ErrNoPicture
	}
	q := url.Values{}
	if opts.Height > 0 {
		// Frigate scales the frame to this height (0.14+; spike 4.5 measured it).
		q.Set("height", strconv.Itoa(opts.Height))
	}
	if opts.Event == "" {
		return in.getPicture(ctx, q, "api", key, "latest.jpg")
	}
	// An event's snapshot, but only of this camera's newest events: the
	// id came from the graph, and one that didn't is never passed on.
	in.mu.Lock()
	mine := eventIDOK(opts.Event) && (c.hasRecent(opts.Event) || c.lastEvent.ID == opts.Event || c.lastHuman.ID == opts.Event)
	in.mu.Unlock()
	if !mine {
		return integration.Picture{}, integration.ErrNoPicture
	}
	return in.getPicture(ctx, q, "api", "events", opts.Event, "snapshot.jpg")
}

// eventIDOK is Frigate's event id shape ("1790989836.632026-bto7xe"), so
// nothing else reaches the URL path.
func eventIDOK(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return !strings.Contains(id, "..")
}

// getPicture fetches GET /api/<camera>/latest.jpg or /api/events/<id>/snapshot.jpg:
// the only media endpoints the integration calls. The answer must be a
// picture, and at most integration.MaxPictureBytes; anything else is refused.
func (in *instance) getPicture(ctx context.Context, q url.Values, path ...string) (integration.Picture, error) {
	u := in.base.JoinPath(path...)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return integration.Picture{}, err
	}
	resp, err := in.http.Do(req)
	if err != nil {
		return integration.Picture{}, err
	}
	defer drain(resp)
	if err := checkResponse(resp); err != nil {
		return integration.Picture{}, err
	}
	ct := resp.Header.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || !pictureTypes[mt] {
		return integration.Picture{}, fmt.Errorf("frigate answered a camera's latest picture with %q, not a picture", ct)
	}
	if resp.ContentLength > integration.MaxPictureBytes {
		return integration.Picture{}, errTooBig
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, integration.MaxPictureBytes+1))
	if err != nil {
		return integration.Picture{}, err
	}
	if len(body) > integration.MaxPictureBytes {
		return integration.Picture{}, errTooBig
	}
	if len(body) == 0 {
		return integration.Picture{}, errors.New("frigate answered an empty picture")
	}
	return integration.Picture{Body: body, ContentType: mt, At: in.d.Now()}, nil
}

var errTooBig = fmt.Errorf("frigate's picture is larger than %d bytes", integration.MaxPictureBytes)
