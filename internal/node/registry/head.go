package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/distribution/reference"
)

// Timeout bounds one registry exchange (the token, then the HEAD).
const Timeout = 15 * time.Second

// manifestTypes are what a HEAD accepts, index types first: an image pulled
// by tag records the index's digest in RepoDigests, so that's the digest to
// compare.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Answers that aren't a digest.
var (
	ErrNotFound = errors.New("registry: no such tag")
	// ErrAnswered wraps any other answer (a 429, a 5xx, no digest): the
	// registry is reachable, so Docker's lookup, which Docker Hub counts
	// as a pull, isn't tried instead.
	ErrAnswered = errors.New("registry answered")
)

// LoginError is a registry asking for a login: one the node hasn't got
// (Had false), or one it refused.
type LoginError struct {
	Registry string
	Had      bool
	// Maybe is Docker's "repository does not exist or may require 'docker
	// login'": a registry such as Docker Hub answers the same for an image
	// that isn't there as for one behind a login.
	Maybe bool
}

func (e *LoginError) Error() string {
	switch {
	case e.Maybe && e.Had:
		return "Docker couldn't pull it from " + e.Registry + ": the image isn't there, or this node's login for it isn't enough."
	case e.Maybe:
		return "Docker couldn't pull it from " + e.Registry + ": the image isn't there, or it needs a login this node hasn't got."
	case e.Had:
		return LoginRefused(e.Registry)
	}
	return NeedsLogin(e.Registry)
}

// LoginErrorFor is the LoginError for err, a refusal IsAuthError
// recognised, from the registry domain; had is whether the node has a
// login for it.
func LoginErrorFor(err error, domain string, had bool) *LoginError {
	return &LoginError{Registry: domain, Had: had, Maybe: strings.Contains(strings.ToLower(err.Error()), "repository does not exist")}
}

// NeedsLogin is the sentence for a registry that wants a login the node
// hasn't got.
func NeedsLogin(registry string) string {
	return registry + " needs a login, and this node has none for it."
}

// LoginRefused is the sentence for a registry that refused the node's login.
func LoginRefused(registry string) string {
	return registry + " refused this node's login for it."
}

// IsAuthError reports whether an error from Docker (a pull, a lookup)
// says the registry wants a login.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[*LoginError](err); ok {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, w := range []string{"unauthorized", "no basic auth credentials", "authentication required", "denied", "may require 'docker login'", "403 forbidden"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// BaseFor is the API origin for a registry's domain: Docker Hub's images
// are named docker.io but served from registry-1.docker.io.
func BaseFor(domain string) string {
	if domain == "docker.io" {
		return "https://registry-1.docker.io"
	}
	return "https://" + domain
}

// NewHTTPClient is the client a Client asks registries with.
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: Timeout, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second}}
}

// Client asks registries about tags.
type Client struct {
	// Base is a domain's registry API origin (BaseFor when nil; tests
	// point it at a fake or at plain http).
	Base func(domain string) string
	HTTP *http.Client
	// Logins are what the node may log in with; nil is none.
	Logins *Logins
}

func (c *Client) base(domain string) string {
	if c.Base != nil {
		return c.Base(domain)
	}
	return BaseFor(domain)
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Head asks a tag's registry for its manifest digest with a HEAD, which
// Docker Hub doesn't count against its pull limit (Docker's own
// /distribution lookup also GETs the manifest, which it does count). With
// no login a registry that wants a token gets an anonymous one from the
// realm it names; with one, the token (or a Basic challenge) gets the
// login. used says the node's login was sent. A registry that refuses is a
// *LoginError. A transport error (a registry only Docker can reach: plain
// http, a mirror) is returned as is, for the caller to ask Docker instead.
func (c *Client) Head(ctx context.Context, tagged reference.NamedTagged) (digest string, used bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	domain := reference.Domain(tagged)
	login, have := c.Logins.For(domain)
	base := c.base(domain)
	u := base + "/v2/" + reference.Path(tagged) + "/manifests/" + url.PathEscape(tagged.Tag())
	resp, err := c.head(ctx, u, "")
	if err != nil {
		return "", false, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		scheme, _, _ := strings.Cut(challenge, " ")
		var auth string
		switch {
		case strings.EqualFold(scheme, "Bearer"):
			token, err := c.token(ctx, base, challenge, domain, login, have)
			if err != nil {
				return "", false, err
			}
			auth = "Bearer " + token
		case strings.EqualFold(scheme, "Basic") && have:
			auth = "Basic " + login.basic()
		default:
			return "", false, &LoginError{Registry: domain}
		}
		used = have
		if resp, err = c.head(ctx, u, auth); err != nil {
			return "", false, err
		}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden && (!have || used):
		return "", false, &LoginError{Registry: domain, Had: used}
	case resp.StatusCode == http.StatusNotFound:
		return "", used, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return "", used, fmt.Errorf("%w %s", ErrAnswered, resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		return "", used, fmt.Errorf("%w without a digest", ErrAnswered)
	}
	return d, used, nil
}

func (c *Client) head(ctx context.Context, u, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestTypes)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return resp, nil
}

// token gets a pull token from the realm a registry's Bearer challenge
// names: for the node's login when it has one, else for nobody. The login
// goes only to a realm on https, or on the registry's own origin (a test
// registry on plain http), never in the clear to another host.
func (c *Client) token(ctx context.Context, base, challenge, domain string, login Login, have bool) (string, error) {
	_, params, _ := strings.Cut(challenge, " ")
	p := challengeParams(params)
	realm, err := url.Parse(p["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") || realm.Host == "" {
		return "", &LoginError{Registry: domain}
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if v := p[k]; v != "" {
			q.Set(k, v)
		}
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	sent := false
	if have {
		if b, err := url.Parse(base); realm.Scheme == "https" || err == nil && b.Host == realm.Host {
			req.Header.Set("Authorization", "Basic "+login.basic())
			sent = true
		}
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &LoginError{Registry: domain, Had: sent}
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&t); err != nil {
		return "", fmt.Errorf("%w: its token didn't decode", ErrAnswered)
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" {
		return "", &LoginError{Registry: domain, Had: sent}
	}
	return t.Token, nil
}

// challengeParams reads a WWW-Authenticate challenge's key="value" pairs.
func challengeParams(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		s = strings.TrimLeft(s, " ,")
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		var v string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				break
			}
			v, s = rest[1:1+end], rest[2+end:]
		} else {
			v, s, _ = strings.Cut(rest, ",")
		}
		out[k] = v
	}
	return out
}
