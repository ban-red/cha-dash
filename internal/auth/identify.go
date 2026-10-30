package auth

import (
	"errors"
	"net/http"
)

// Identify returns the identity behind r's session cookie, if r carries a
// valid one. Unlike Require it never answers the request and never renews
// the session: it's for responses that differ only when someone is signed
// in, such as index.html with the first-paint snapshot.
func (s *Service) Identify(r *http.Request) (Identity, bool) {
	if id, ok := FromContext(r.Context()); ok && id.Token == nil {
		return id, true // forward-auth signed this request in (forwardauth.go)
	}
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Identity{}, false
	}
	sess, u, err := s.lookupSession(r.Context(), c.Value)
	if err != nil {
		if !errors.Is(err, ErrNoSession) {
			s.log.Warn("session lookup failed", "err", err)
		}
		return Identity{}, false
	}
	return Identity{User: u, Session: sess}, true
}
