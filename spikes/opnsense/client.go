package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// errNotReadOnly is returned for any method other than GET or HEAD. The
// router is production; this spike must never change its state.
var errNotReadOnly = errors.New("refused: this client is read-only (GET and HEAD only)")

// readOnlyTransport enforces the spike's safety rules below every request:
// only GET/HEAD, and only the one configured origin.
type readOnlyTransport struct {
	base   http.RoundTripper
	scheme string
	host   string
}

func (t *readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, errNotReadOnly
	}
	if r.URL.Scheme != t.scheme || r.URL.Host != t.host {
		return nil, errors.New("refused: request to an origin other than the configured one")
	}
	return t.base.RoundTrip(r)
}

// tofu pins the first certificate it sees when the chain does not verify
// against the system roots (self-signed OPNsense GUI cert). The pin lives in
// memory and, optionally, in a git-ignored file. A changed certificate is an
// error, never silently accepted.
type tofu struct {
	mu      sync.Mutex
	pinFile string
	pin     string // hex sha256 of the leaf DER
	mode    string // "system-roots" or "tofu"
}

func (p *tofu) load() {
	if p.pinFile == "" {
		return
	}
	if b, err := os.ReadFile(p.pinFile); err == nil {
		p.pin = strings.TrimSpace(string(b))
	}
}

func (p *tofu) verify(serverName string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("tls: no peer certificate")
		}
		leaf := cs.PeerCertificates[0]
		sum := sha256.Sum256(leaf.Raw)
		fp := hex.EncodeToString(sum[:])
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pin != "" {
			if fp != p.pin {
				return errors.New("tls: certificate changed since first use (TOFU pin mismatch); refusing")
			}
			return nil
		}
		inter := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: serverName, Intermediates: inter}); err == nil {
			p.mode = "system-roots"
			return nil
		}
		p.pin, p.mode = fp, "tofu"
		if p.pinFile != "" {
			_ = os.WriteFile(p.pinFile, []byte(fp+"\n"), 0o600)
		}
		log.Printf("tls: certificate does not chain to system roots; pinned on first use (fingerprint kept locally, not printed)")
		return nil
	}
}

// Client is a gentle, sequential, read-only HTTP client for one origin.
type Client struct {
	base    *url.URL
	hc      *http.Client
	user    string
	pass    string
	gap     time.Duration
	timeout time.Duration
	last    time.Time
	tofu    *tofu
}

func NewClient(rawURL, user, pass, pinFile string, gap, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("base URL is not an http(s) URL")
	}
	p := &tofu{pinFile: pinFile}
	p.load()
	tr := &http.Transport{
		Proxy:               nil, // straight to the device, never via a proxy
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxConnsPerHost:     1, // one connection at a time, to be gentle
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     30 * time.Second,
		TLSClientConfig: &tls.Config{
			// Verification is not disabled: VerifyConnection checks the chain
			// against system roots or, failing that, enforces the TOFU pin.
			InsecureSkipVerify: true,
			VerifyConnection:   p.verify(u.Hostname()),
			MinVersion:         tls.VersionTLS12,
		},
	}
	return &Client{
		base: u,
		hc: &http.Client{
			Transport: &readOnlyTransport{base: tr, scheme: u.Scheme, host: u.Host},
			// Never follow redirects: a redirect is recorded as a finding.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		user: user, pass: pass, gap: gap, timeout: timeout, tofu: p,
	}, nil
}

// Result is what we record about one request. It never holds the host.
type Result struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Status      int               `json:"status"`
	ContentType string            `json:"content_type,omitempty"`
	Bytes       int               `json:"bytes"`
	Millis      int64             `json:"ms"`
	Headers     map[string]string `json:"headers,omitempty"`
	Redirect    string            `json:"redirect,omitempty"` // scheme + path only
	Error       string            `json:"error,omitempty"`
	Body        []byte            `json:"-"`
}

var keptHeaders = []string{"Content-Type", "Content-Length", "Cache-Control", "Server", "X-Frame-Options", "Content-Security-Policy", "Transfer-Encoding", "Content-Encoding"}

func (c *Client) wait() {
	if d := time.Until(c.last.Add(c.gap)); d > 0 {
		time.Sleep(d)
	}
}

// Get performs one GET (or HEAD) and returns the full body (capped).
func (c *Client) Do(method, path string, maxBody int64) Result {
	c.wait()
	defer func() { c.last = time.Now() }()
	res := Result{Method: method, Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, nil)
	if err != nil {
		res.Error = c.scrub(err.Error())
		return res
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "chadash-spike-4.5 (read-only)")
	t0 := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		res.Error = c.scrub(err.Error())
		res.Millis = time.Since(t0).Milliseconds()
		return res
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res.Millis = time.Since(t0).Milliseconds()
	if err != nil {
		res.Error = c.scrub(err.Error())
	}
	res.Status, res.Bytes, res.Body = resp.StatusCode, len(body), body
	res.ContentType = resp.Header.Get("Content-Type")
	res.Headers = map[string]string{}
	for _, h := range keptHeaders {
		if v := resp.Header.Get(h); v != "" {
			res.Headers[h] = v
		}
	}
	if cs := resp.Cookies(); len(cs) > 0 {
		names := make([]string, 0, len(cs))
		for _, ck := range cs {
			names = append(names, ck.Name+"=<value not recorded>")
		}
		res.Headers["Set-Cookie"] = strings.Join(names, "; ")
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if lu, err := url.Parse(loc); err == nil {
			res.Redirect = lu.Scheme + "://<same-or-other-host>" + lu.Path
		}
	}
	return res
}

// Stream opens a GET and hands each line to fn until the window closes or fn
// returns false. The connection is always closed within the window.
func (c *Client) Stream(path string, window time.Duration, fn func(line []byte, at time.Duration) bool) (Result, error) {
	if window > 55*time.Second {
		window = 55 * time.Second
	}
	c.wait()
	defer func() { c.last = time.Now() }()
	res := Result{Method: http.MethodGet, Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.String()+path, nil)
	if err != nil {
		return res, errors.New(c.scrub(err.Error()))
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "chadash-spike-4.5 (read-only)")
	t0 := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return res, errors.New(c.scrub(err.Error()))
	}
	defer resp.Body.Close()
	res.Status, res.ContentType = resp.StatusCode, resp.Header.Get("Content-Type")
	res.Headers = map[string]string{}
	for _, h := range keptHeaders {
		if v := resp.Header.Get(h); v != "" {
			res.Headers[h] = v
		}
	}
	buf := make([]byte, 0, 64*1024)
	chunk := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			res.Bytes += n
			buf = append(buf, chunk[:n]...)
			for {
				i := bytes.IndexByte(buf, '\n')
				if i < 0 {
					break
				}
				line := bytes.TrimRight(buf[:i], "\r")
				if !fn(line, time.Since(t0)) {
					res.Millis = time.Since(t0).Milliseconds()
					return res, nil
				}
				buf = buf[i+1:]
			}
		}
		if err != nil {
			res.Millis = time.Since(t0).Milliseconds()
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return res, nil
			}
			if err == io.EOF {
				return res, nil
			}
			return res, errors.New(c.scrub(err.Error()))
		}
	}
}

var ipv4 = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

// scrub removes the host and any IPv4 address from error text, because Go's
// url.Error and net.OpError embed both.
func (c *Client) scrub(s string) string {
	s = strings.ReplaceAll(s, c.base.String(), "<base>")
	s = strings.ReplaceAll(s, c.base.Host, "<host>")
	s = strings.ReplaceAll(s, c.base.Hostname(), "<host>")
	return ipv4.ReplaceAllString(s, "<ip>")
}

func (c *Client) TLSMode() string {
	if c.base.Scheme != "https" {
		return "plain-http"
	}
	c.tofu.mu.Lock()
	defer c.tofu.mu.Unlock()
	return c.tofu.mode
}

func (r Result) String() string {
	s := fmt.Sprintf("%-4s %-60s %3d %6dB %5dms %s", r.Method, r.Path, r.Status, r.Bytes, r.Millis, r.ContentType)
	if r.Redirect != "" {
		s += " → " + r.Redirect
	}
	if r.Error != "" {
		s += " ERR " + r.Error
	}
	return s
}
