package main

import (
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
	"regexp"
	"strings"
	"sync"
	"time"
)

var errNotReadOnly = errors.New("refused: this client is read-only (GET and HEAD only)")

// readOnlyTransport: only GET/HEAD, only the configured origin.
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

// tofu pins the first certificate when it does not chain to system roots.
type tofu struct {
	mu   sync.Mutex
	pin  string
	mode string
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
				return errors.New("tls: certificate changed since first use; refusing")
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
		log.Printf("tls: self-signed certificate pinned on first use (fingerprint kept in memory)")
		return nil
	}
}

type Client struct {
	base    *url.URL
	hc      *http.Client
	tr      *http.Transport
	gap     time.Duration
	timeout time.Duration
	last    time.Time
	tofu    *tofu
}

func NewClient(rawURL string, gap, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("base URL is not an http(s) URL")
	}
	p := &tofu{}
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxConnsPerHost:     1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     30 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // replaced by VerifyConnection: system roots, else TOFU pin
			VerifyConnection:   p.verify(u.Hostname()),
			MinVersion:         tls.VersionTLS12,
		},
	}
	return &Client{
		base: u, tr: tr,
		hc: &http.Client{
			Transport:     &readOnlyTransport{base: tr, scheme: u.Scheme, host: u.Host},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		gap: gap, timeout: timeout, tofu: p,
	}, nil
}

type Result struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Status      int               `json:"status"`
	ContentType string            `json:"content_type,omitempty"`
	Bytes       int64             `json:"bytes"`
	Declared    int64             `json:"content_length,omitempty"`
	Millis      int64             `json:"ms"`
	Headers     map[string]string `json:"headers,omitempty"`
	Redirect    string            `json:"redirect,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
	Error       string            `json:"error,omitempty"`
	Body        []byte            `json:"-"`
}

var keptHeaders = []string{"Content-Type", "Content-Length", "Cache-Control", "Server", "Accept-Ranges", "Last-Modified", "ETag", "Transfer-Encoding", "Content-Encoding", "X-Cache-Status", "Content-Disposition"}

func (c *Client) wait() {
	if d := time.Until(c.last.Add(c.gap)); d > 0 {
		time.Sleep(d)
	}
}

func (c *Client) start(method, path string) (*http.Response, Result, context.CancelFunc, time.Time) {
	c.wait()
	res := Result{Method: method, Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, nil)
	if err != nil {
		res.Error = c.scrub(err.Error())
		return nil, res, cancel, time.Now()
	}
	req.Header.Set("User-Agent", "chadash-spike-4.5 (read-only)")
	t0 := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		res.Error = c.scrub(err.Error())
		res.Millis = time.Since(t0).Milliseconds()
		return nil, res, cancel, t0
	}
	res.Status, res.ContentType, res.Declared = resp.StatusCode, resp.Header.Get("Content-Type"), resp.ContentLength
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
			res.Redirect = lu.Path
		}
	}
	return resp, res, cancel, t0
}

// Do fetches a metadata endpoint and returns its body (capped). Callers must
// run the body through scrubImages before it touches disk.
func (c *Client) Do(method, path string, maxBody int64) Result {
	resp, res, cancel, t0 := c.start(method, path)
	defer cancel()
	defer func() { c.last = time.Now() }()
	if resp == nil {
		return res
	}
	defer resp.Body.Close()
	if isMedia(res.ContentType) {
		// Never keep media bytes, even in memory: count and discard.
		n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		res.Bytes, res.Millis = n, time.Since(t0).Milliseconds()
		res.Error = "media response discarded"
		return res
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res.Millis = time.Since(t0).Milliseconds()
	if err != nil {
		res.Error = c.scrub(err.Error())
	}
	res.Bytes, res.Body = int64(len(body)), body
	return res
}

// Measure requests a media endpoint (snapshot, thumbnail, clip) and records
// only status, content type and size. The bytes go to io.Discard; nothing is
// stored. readCap bounds how much is pulled before closing (clips can be big).
func (c *Client) Measure(method, path string, readCap int64) Result {
	resp, res, cancel, t0 := c.start(method, path)
	defer cancel()
	defer func() { c.last = time.Now() }()
	if resp == nil {
		return res
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, readCap))
	res.Bytes, res.Millis = n, time.Since(t0).Milliseconds()
	if n == readCap {
		res.Truncated = true
	}
	if err != nil {
		res.Error = c.scrub(err.Error())
	}
	return res
}

func isMedia(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/") || strings.Contains(ct, "mpegurl") || strings.HasPrefix(ct, "audio/")
}

var ipv4 = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

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
	s := fmt.Sprintf("%-4s %-58s %3d %8dB %5dms %s", r.Method, r.Path, r.Status, r.Bytes, r.Millis, r.ContentType)
	if r.Declared > 0 && r.Declared != r.Bytes {
		s += fmt.Sprintf(" (Content-Length %d)", r.Declared)
	}
	if r.Truncated {
		s += " [read capped]"
	}
	if r.Redirect != "" {
		s += " → " + r.Redirect
	}
	if r.Error != "" {
		s += " | " + r.Error
	}
	return s
}
