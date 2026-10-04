package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
)

// Client is a deliberately small, sequential, read-only GraphQL client for one Unraid server.
// Every request goes through checkReadOnly first, and requests are spaced by gap.
type Client struct {
	base   *url.URL
	apiKey string
	hc     *http.Client
	red    *Redactor
	gap    time.Duration

	mu   sync.Mutex // one request at a time
	last time.Time
}

const maxBody = 32 << 20 // 32 MiB is far more than any read here returns

func newClient(cfg instanceConfig, red *Redactor, timeout, gap time.Duration, rt http.RoundTripper) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("UNRAID_%s_URL must be an http(s) base URL", cfg.Name)
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &Client{
		base:   u,
		apiKey: cfg.APIKey,
		red:    red,
		gap:    gap,
		hc: &http.Client{
			Transport: rt,
			Timeout:   timeout,
			// Never follow redirects: a redirect is a finding (http → https, or to a
			// myunraid.net name), and following one could carry the key to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type gqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data,omitempty"`
	Errors []gqlError      `json:"errors,omitempty"`
}

// Result is one HTTP exchange. Body and Location may hold real values: never print them,
// only write them to the git-ignored capture directory.
type Result struct {
	Status   int
	Elapsed  time.Duration
	Body     []byte
	Resp     gqlResponse
	JSON     bool // the body parsed as a GraphQL response
	Location string
}

// codes returns the distinct GraphQL error codes (extensions.code) in the response.
func (r *Result) codes() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range r.Resp.Errors {
		c, _ := e.Extensions["code"].(string)
		if c == "" {
			c = "NO_CODE"
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Client) endpoint(path string) string {
	u := *c.base
	u.Path = path
	u.RawQuery = ""
	return u.String()
}

func (c *Client) pace(ctx context.Context) error {
	if wait := c.gap - time.Since(c.last); wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil
}

// Query POSTs one read-only operation to /graphql. Mutations (and anything that isn't a
// query) are refused before the request is built.
func (c *Client) Query(ctx context.Context, opName, doc string, vars map[string]any) (*Result, error) {
	if err := checkReadOnly(doc, ast.Query); err != nil {
		return nil, err
	}
	body, err := json.Marshal(gqlRequest{Query: doc, OperationName: opName, Variables: vars})
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, "/graphql", body, "application/json")
}

// Get issues a plain GET (used only by the unauthenticated probe: is the webGUI there, is
// the sandbox page enabled). It never sends the API key.
func (c *Client) Get(ctx context.Context, path, accept string) (*Result, error) {
	return c.do(ctx, http.MethodGet, path, nil, accept)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, accept string) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.pace(ctx); err != nil {
		return nil, err
	}
	defer func() { c.last = time.Now() }()

	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), rd)
	if err != nil {
		return nil, fmt.Errorf("build request: %s", c.red.Err(err))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "chadash-unraid-spike/0 (read-only)")
	if c.apiKey != "" && method == http.MethodPost {
		req.Header.Set("x-api-key", c.apiKey)
	}

	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %s", method, path, c.red.Err(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res := &Result{Status: resp.StatusCode, Elapsed: time.Since(start), Body: raw, Location: resp.Header.Get("Location")}
	if err != nil {
		return res, fmt.Errorf("%s %s: read body: %s", method, path, c.red.Err(err))
	}
	if json.Unmarshal(raw, &res.Resp) == nil && (res.Resp.Data != nil || res.Resp.Errors != nil) {
		res.JSON = true
	}
	return res, nil
}

// describeLocation says where a redirect points without revealing the address: the path if
// it stays on the same origin, otherwise only the scheme and whether the host changed.
func (c *Client) describeLocation(loc string) string {
	if loc == "" {
		return ""
	}
	u, err := c.base.Parse(loc)
	if err != nil {
		return "unparseable Location"
	}
	switch {
	case u.Scheme == c.base.Scheme && u.Host == c.base.Host:
		return "same origin " + u.Path
	case u.Hostname() == c.base.Hostname():
		return fmt.Sprintf("same host, %s, path %s", u.Scheme, u.Path)
	default:
		return fmt.Sprintf("another host (%s), path %s", u.Scheme, u.Path)
	}
}
