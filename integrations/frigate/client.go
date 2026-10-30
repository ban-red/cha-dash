package frigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"dash.cha.sh/internal/integration"
)

// get fetches path (under the instance URL's path) with the instance's
// client. Every request but a camera's latest picture (getPicture) goes
// through here, and none of them is for media.
func (in *instance) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	u := in.base.JoinPath(strings.Split(strings.Trim(path, "/"), "/")...)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: err.Error()}
	}
	resp, err := in.http.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		drain(resp)
		return nil, err
	}
	// Frigate's API answers JSON or text. A picture or a video is never
	// read here: not even by accident through a proxy.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/") {
		drain(resp)
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Message: fmt.Sprintf("GET %s answered %s, not JSON; Cha Dash never reads media", u.Path, ct)}
	}
	return resp, nil
}

// errMissing marks an endpoint that answered 404 or 410.
var errMissing = errors.New("frigate: no such endpoint")

// missing is an api_changed Repair that errors.Is errMissing.
type missing struct{ *integration.RepairError }

func (m missing) Unwrap() []error { return []error{m.RepairError, errMissing} }

// checkResponse is integration.CheckResponse, with a 404 or 410 on the API
// read as "this isn't the Frigate API this integration knows".
func checkResponse(resp *http.Response) error {
	err := integration.CheckResponse(resp)
	var se *integration.StatusError
	if errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone) {
		return missing{&integration.RepairError{Code: integration.CodeAPIChanged,
			Message: se.Error() + ": this doesn't look like Frigate's API (check the URL), or the API changed"}}
	}
	return err
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// getJSON fetches path and decodes the JSON answer into dst.
func (in *instance) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	resp, err := in.get(ctx, path, query)
	if err != nil {
		return err
	}
	defer drain(resp)
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &integration.RepairError{Code: integration.CodeAPIChanged, Message: fmt.Sprintf("GET %s didn't answer JSON Cha Dash understands: %v", path, err)}
	}
	return nil
}

// fetchVersion reads /api/version ("0.18.0-7e08f7b", text/plain).
func (in *instance) fetchVersion(ctx context.Context) (string, error) {
	resp, err := in.get(ctx, "/api/version", nil)
	if err != nil {
		return "", err
	}
	defer drain(resp)
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	v := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if v == "" || strings.ContainsAny(v, "<{ ") {
		return "", &integration.RepairError{Code: integration.CodeAPIChanged, Message: "GET /api/version didn't answer a version: this doesn't look like Frigate's API (check the URL)"}
	}
	return v, nil
}

// semver is the release part of Frigate's version, without the build hash.
func semver(v string) string {
	if i := strings.IndexByte(v, '-'); i > 0 {
		return v[:i]
	}
	return v
}

// profile is /api/profile (Frigate 0.14+): who Frigate thinks we are.
type profile struct {
	Username       string   `json:"username"`
	Role           string   `json:"role"`
	AllowedCameras []string `json:"allowed_cameras"`
}

// fetchProfile reads /api/profile. missing is true when the endpoint doesn't
// exist (Frigate before 0.14, which has no users at all).
func (in *instance) fetchProfile(ctx context.Context) (p profile, missing bool, err error) {
	err = in.getJSON(ctx, "/api/profile", nil, &p)
	if errors.Is(err, errMissing) {
		return profile{}, true, nil
	}
	return p, false, err
}

// anonymousAdmin reports whether Frigate grants admin without a sign-in:
// the internal port answers as "anonymous" with role "admin", and a Frigate
// without /api/profile has no users at all.
func (in *instance) anonymousAdmin(p profile, missing bool) bool {
	if missing {
		return !in.creds
	}
	return p.Username == "anonymous" && p.Role == "admin"
}

// session is Frigate's cookie sign-in (0.14+, the authenticated port):
// POST /api/login {user, password} sets a JWT cookie (frigate_token by
// default). A 401 later means the token expired; the runtime signs in again
// and retries once.
type session struct {
	login    *url.URL
	user     string
	password string

	mu      sync.Mutex
	cookies []*http.Cookie
}

// Login implements integration.Session.
func (s *session) Login(ctx context.Context, c *http.Client) error {
	body, err := json.Marshal(map[string]string{"user": s.user, "password": s.password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.login.String(), bytes.NewReader(body))
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return &integration.AuthError{Field: "password", Message: "Frigate rejected the username or password"}
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return &integration.ConfigError{Field: "username",
			Message: "Frigate has no sign-in at this URL (it's older than 0.14); remove the username and password"}
	case http.StatusTooManyRequests:
		return errors.New("frigate is limiting sign-in attempts; Cha Dash will try again later")
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	var got []*http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Value != "" {
			got = append(got, &http.Cookie{Name: ck.Name, Value: ck.Value})
		}
	}
	if len(got) == 0 {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Message: "Frigate's sign-in answered without a session cookie"}
	}
	s.mu.Lock()
	s.cookies = got
	s.mu.Unlock()
	return nil
}

// Apply implements integration.Session.
func (s *session) Apply(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.cookies {
		r.AddCookie(c)
	}
}
