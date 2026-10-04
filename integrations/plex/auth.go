package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// The PIN sign-in, from Plex's "Authenticating with Plex" guide: create a
// strong PIN at plex.tv, send the user to the Auth App with its code, and
// poll the PIN until it carries the account's token. plexTV is a var so the
// tests can point it at a fake; it must be https, and the client the hub
// hands BeginAuth always verifies certificates.
var (
	plexTV  = "https://plex.tv"
	authApp = "https://app.plex.tv/auth"
)

var errNotHTTPS = errors.New("plex: the sign-in service must be https")

// pin is plex.tv's PIN resource.
type pin struct {
	ID        int64      `json:"id"`
	Code      string     `json:"code"`
	ExpiresIn int        `json:"expiresIn"`
	ExpiresAt *time.Time `json:"expiresAt"`
	AuthToken *string    `json:"authToken"`
}

func (Integration) pinRequest(ctx context.Context, d integration.AuthDeps, method, path string, q url.Values) (*http.Request, error) {
	if !strings.HasPrefix(plexTV, "https://") {
		return nil, errNotHTTPS
	}
	u := plexTV + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", d.Product)
	req.Header.Set("X-Plex-Version", "0.1.0")
	req.Header.Set("X-Plex-Client-Identifier", d.ClientID)
	return req, nil
}

func do(d integration.AuthDeps, req *http.Request, dst any) error {
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return integration.ErrAuthExpired
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("plex.tv answered %s with something other than a PIN", req.URL.Path)
	}
	return nil
}

// BeginAuth implements integration.Authorizer: a strong PIN, and the Auth
// App link that approves it.
func (i Integration) BeginAuth(ctx context.Context, d integration.AuthDeps) (integration.Authorization, error) {
	req, err := i.pinRequest(ctx, d, http.MethodPost, "/api/v2/pins", url.Values{"strong": {"true"}})
	if err != nil {
		return integration.Authorization{}, err
	}
	var p pin
	if err := do(d, req, &p); err != nil {
		return integration.Authorization{}, err
	}
	if p.ID == 0 || p.Code == "" {
		return integration.Authorization{}, fmt.Errorf("plex.tv answered without a PIN")
	}
	a := integration.Authorization{
		Code:  p.Code,
		URL:   authApp + "#?clientID=" + esc(d.ClientID) + "&code=" + esc(p.Code) + "&context%5Bdevice%5D%5Bproduct%5D=" + esc(d.Product),
		State: strconv.FormatInt(p.ID, 10) + ":" + p.Code,
	}
	switch {
	case p.ExpiresAt != nil:
		a.Expires = *p.ExpiresAt
	case p.ExpiresIn > 0:
		a.Expires = d.Now().Add(time.Duration(p.ExpiresIn) * time.Second)
	}
	return a, nil
}

// PollAuth implements integration.Authorizer: the PIN carries the token once
// the user approved Cha Dash in the Auth App.
func (i Integration) PollAuth(ctx context.Context, d integration.AuthDeps, a integration.Authorization) (map[string]string, bool, error) {
	id, code, ok := strings.Cut(a.State, ":")
	if !ok || id == "" {
		return nil, false, integration.ErrAuthExpired
	}
	req, err := i.pinRequest(ctx, d, http.MethodGet, "/api/v2/pins/"+url.PathEscape(id), url.Values{"code": {code}})
	if err != nil {
		return nil, false, err
	}
	var p pin
	if err := do(d, req, &p); err != nil {
		return nil, false, err
	}
	if p.AuthToken == nil || *p.AuthToken == "" {
		return nil, false, nil
	}
	return map[string]string{"token": *p.AuthToken}, true, nil
}

// esc escapes a value for the Auth App's fragment, spaces as %20 the way
// Plex's guide writes them.
func esc(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
