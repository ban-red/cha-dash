package integration

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Authorizer is an optional Integration method for sign-in flows that hand
// Cha Dash a credential, so people needn't dig one out (Plex's PIN): the user
// opens a link, approves Cha Dash there, and the hub polls until the source
// hands over the secret. The secrets go back only to the form being filled
// in; nothing is stored until the user saves the instance.
//
// Mark the secret fields a flow fills with Schema.Authorize, so the web form
// offers a "Sign in with …" button next to them.
type Authorizer interface {
	// BeginAuth starts a sign-in.
	BeginAuth(ctx context.Context, d AuthDeps) (Authorization, error)
	// PollAuth checks a sign-in BeginAuth started: done with the secrets
	// (write-only field → value) once the user approved, not done while
	// waiting. Return ErrAuthExpired when the source says the sign-in is
	// gone; other errors are classified like Sync's (a transient one keeps the
	// sign-in pending).
	PollAuth(ctx context.Context, d AuthDeps, a Authorization) (secrets map[string]string, done bool, err error)
}

// Authorization is a sign-in in progress.
type Authorization struct {
	Code    string    // a code to show the user, when the flow has one
	URL     string    // where the user signs in and approves Cha Dash
	Expires time.Time // when the sign-in stops working; zero: the hub's default (15 min)
	// State is the integration's own (a PIN's id, say). It never leaves the
	// hub.
	State string
}

// AuthDeps are what the framework hands a sign-in flow. There's no instance
// yet, so there's no instance config: the flow talks to the source's own
// sign-in service.
type AuthDeps struct {
	// HTTP verifies certificates against the system roots, whatever the form's
	// tls says (that's for the instance, not for the sign-in service), and
	// follows redirects only within the origin.
	HTTP *http.Client
	// ClientID identifies this hub to the source, the same on every call and
	// across restarts.
	ClientID string
	// Product is the name to show the user at the source ("Cha Dash").
	Product string
	Now     func() time.Time
	Log     *slog.Logger
}

// ErrAuthExpired is what PollAuth returns when the source says the sign-in
// expired or no longer exists.
var ErrAuthExpired = errors.New("integration: the sign-in expired")

// AuthorizeFields lists the manifest's secret fields a sign-in flow fills
// (Schema.Authorize), in schema order.
func (m Manifest) AuthorizeFields() []string {
	if m.ConfigSchema == nil {
		return nil
	}
	var out []string
	for _, p := range m.ConfigSchema.Properties {
		if p.Schema.Authorize && p.Schema.WriteOnly {
			out = append(out, p.Name)
		}
	}
	return out
}
