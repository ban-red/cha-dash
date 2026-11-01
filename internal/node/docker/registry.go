package docker

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

// registryTimeout bounds one registry exchange (the token, then the HEAD).
const registryTimeout = 15 * time.Second

// manifestTypes are what a HEAD accepts, index types first: an image pulled
// by tag records the index's digest in RepoDigests, so that's the digest to
// compare.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Registry answers that aren't a digest.
var (
	errRegistryLogin    = errors.New("registry: needs a login")
	errRegistryNotFound = errors.New("registry: no such tag")
	// errRegistryAnswered wraps any other answer (a 429, a 5xx, no digest):
	// the registry is reachable, so Docker's lookup, which Docker Hub
	// counts as a pull, isn't tried instead.
	errRegistryAnswered = errors.New("registry answered")
)

// registryBase is the API origin for a registry's domain: Docker Hub's
// images are named docker.io but served from registry-1.docker.io.
func registryBase(domain string) string {
	if domain == "docker.io" {
		return "https://registry-1.docker.io"
	}
	return "https://" + domain
}

// headTag asks a tag's registry for its manifest digest with a HEAD, which
// Docker Hub doesn't count against its pull limit (Docker's own
// /distribution lookup also GETs the manifest, which it does count). It
// sends no credentials: a registry that wants a token gets an anonymous
// one from the realm it names, and one that refuses that needs a login.
// A transport error (a registry only Docker can reach: plain http, a
// mirror) is returned as is, for the caller to ask Docker instead.
func (m *Module) headTag(ctx context.Context, tagged reference.NamedTagged) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	base := m.registry(reference.Domain(tagged))
	u := base + "/v2/" + reference.Path(tagged) + "/manifests/" + url.PathEscape(tagged.Tag())
	resp, err := m.manifestHead(ctx, u, "")
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		token, err := m.anonymousToken(ctx, challenge)
		if err != nil {
			return "", err
		}
		if resp, err = m.manifestHead(ctx, u, token); err != nil {
			return "", err
		}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", errRegistryLogin
	case resp.StatusCode == http.StatusNotFound:
		return "", errRegistryNotFound
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w %s", errRegistryAnswered, resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		return "", fmt.Errorf("%w without a digest", errRegistryAnswered)
	}
	return d, nil
}

func (m *Module) manifestHead(ctx context.Context, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestTypes)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.regHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return resp, nil
}

// anonymousToken gets a pull token for nobody from the realm a registry's
// Bearer challenge names. A registry that refuses one needs a login.
func (m *Module) anonymousToken(ctx context.Context, challenge string) (string, error) {
	scheme, params, ok := strings.Cut(challenge, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", errRegistryLogin // Basic: a login, which the node doesn't have
	}
	p := challengeParams(params)
	realm, err := url.Parse(p["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") || realm.Host == "" {
		return "", errRegistryLogin
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
	resp, err := m.regHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errRegistryLogin
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&t); err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" {
		return "", errRegistryLogin
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
