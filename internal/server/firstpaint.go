package server

import (
	"errors"
	"net/http"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/firstpaint"
)

// firstPaint returns the snapshot index.html embeds for r: only for the
// owner's valid session, never for anyone else, and nil when building it
// fails or it's over the cap (the page then loads from the socket as usual).
func firstPaint(d Deps, r *http.Request) []byte {
	id, ok := d.Auth.Identify(r)
	if !ok || id.User.Role != auth.RoleOwner {
		return nil
	}
	b, err := d.FirstPaint.Build(r.Context(), firstpaint.User{ID: id.User.ID, Username: id.User.Username, Role: id.User.Role})
	var big *firstpaint.TooLargeError
	switch {
	case errors.As(err, &big):
		d.Log.Debug("first-paint snapshot not inlined", "err", err)
		return nil
	case err != nil:
		d.Log.Warn("first-paint snapshot failed", "err", err)
		return nil
	}
	return b
}
