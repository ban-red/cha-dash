package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// ── TLS: trust on first use, per instance ────────────────────────────────
//
// PVE ships a self-signed cluster CA (pve-root-ca.pem) that signs each node's
// pve-ssl.pem. The system roots don't know it, so standard verification fails.
// The spike never skips verification silently: VerifyConnection runs on every
// handshake and does, in order,
//
//  1. system-roots verification with the URL host (mode "ca"): an ACME or other
//     publicly trusted cert passes here and needs no pin at all;
//  2. otherwise the pin check: the leaf's SHA-256 must equal the fingerprint
//     recorded for this instance. With no pin yet and -tofu set, it's recorded
//     (mode "tofu-new"); with a mismatch the handshake fails ("pin-mismatch").
//
// InsecureSkipVerify only turns off Go's built-in chain check so that
// VerifyConnection can do the pin check instead; it is never set without it.

// TOFUStore holds one pinned fingerprint per instance. It lives in the raw
// directory (git-ignored): fingerprints are identifying and stay out of fixtures.
type TOFUStore struct {
	path string
	mu   sync.Mutex
	Pins map[string]Pin `json:"pins"`
}

// Pin is what was trusted on first use.
type Pin struct {
	LeafSHA256 string    `json:"leaf_sha256"`
	CASHA256   string    `json:"ca_sha256,omitempty"` // the PVE cluster CA, when the server sends it
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}

func openTOFU(path string) (*TOFUStore, error) {
	s := &TOFUStore{path: path, Pins: map[string]Pin{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, s)
}

func (s *TOFUStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(s.path, b, 0o600)
}

// TLSFacts is what one handshake taught us about an instance's certificate.
// Fingerprints are here for the raw capture only; the sanitizer drops them.
type TLSFacts struct {
	Mode            string    `json:"mode"` // ca, tofu-new, tofu-match, pin-mismatch
	TLSVersion      string    `json:"tls_version"`
	Cipher          string    `json:"cipher"`
	ChainLen        int       `json:"chain_len"`
	LeafSelfSigned  bool      `json:"leaf_self_signed"`
	IssuerIsPVECA   bool      `json:"issuer_is_pve_ca"` // issuer O=PVE Cluster Manager CA
	ChainHasCA      bool      `json:"chain_has_ca"`     // the server sends its CA in the chain
	SANMatchesHost  bool      `json:"san_matches_host"` // leaf.VerifyHostname(url host)
	CAPinViable     bool      `json:"ca_pin_viable"`    // leaf verifies against the sent CA as the only root, with the URL host
	LeafNotAfter    time.Time `json:"leaf_not_after"`   // expiry; renewals change the leaf pin
	LeafDaysLeft    int       `json:"leaf_days_left"`
	LeafValidDays   int       `json:"leaf_valid_days"` // NotAfter − NotBefore
	CANotAfter      time.Time `json:"ca_not_after,omitempty"`
	LeafKey         string    `json:"leaf_key"`
	LeafSHA256      string    `json:"leaf_sha256"`
	CASHA256        string    `json:"ca_sha256,omitempty"`
	PinMatchedSince time.Time `json:"pin_matched_since,omitempty"`
	FirstMode       string    `json:"first_mode"` // the first handshake's mode in this run
	Handshakes      int       `json:"handshakes"`
}

func fp(der []byte) string { s := sha256.Sum256(der); return hex.EncodeToString(s[:]) }

// tofuConfig returns a tls.Config that verifies every handshake as described
// above and records the result in facts.
func tofuConfig(instance, host string, store *TOFUStore, allowNew bool, facts *TLSFacts) *tls.Config {
	set := func(f TLSFacts) {
		f.Handshakes = facts.Handshakes + 1
		f.FirstMode = facts.FirstMode
		if f.FirstMode == "" {
			f.FirstMode = f.Mode
		}
		*facts = f
	}
	return &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by VerifyConnection below; never used on its own
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("tls: no peer certificate")
			}
			leaf := cs.PeerCertificates[0]
			f := TLSFacts{
				TLSVersion:     tls.VersionName(cs.Version),
				Cipher:         tls.CipherSuiteName(cs.CipherSuite),
				ChainLen:       len(cs.PeerCertificates),
				LeafSelfSigned: leaf.Issuer.String() == leaf.Subject.String(),
				SANMatchesHost: leaf.VerifyHostname(host) == nil,
				LeafNotAfter:   leaf.NotAfter.UTC(),
				LeafDaysLeft:   int(time.Until(leaf.NotAfter).Hours() / 24),
				LeafValidDays:  int(leaf.NotAfter.Sub(leaf.NotBefore).Hours() / 24),
				LeafKey:        leaf.PublicKeyAlgorithm.String(),
				LeafSHA256:     fp(leaf.Raw),
			}
			for _, o := range leaf.Issuer.Organization {
				if o == "PVE Cluster Manager CA" {
					f.IssuerIsPVECA = true
				}
			}
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
				if c.IsCA {
					f.ChainHasCA = true
					f.CASHA256 = fp(c.Raw)
					f.CANotAfter = c.NotAfter.UTC()
					roots := x509.NewCertPool()
					roots.AddCert(c)
					_, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots})
					f.CAPinViable = err == nil
				}
			}
			// 1. Publicly trusted? Then no pin is needed.
			if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter}); err == nil {
				f.Mode = "ca"
				set(f)
				return nil
			}
			// 2. Pin check.
			store.mu.Lock()
			pin, ok := store.Pins[instance]
			now := time.Now().UTC()
			switch {
			case !ok && allowNew:
				store.Pins[instance] = Pin{LeafSHA256: f.LeafSHA256, CASHA256: f.CASHA256, FirstSeen: now, LastSeen: now}
				f.Mode = "tofu-new"
			case !ok:
				store.mu.Unlock()
				f.Mode = "no-pin"
				set(f)
				return fmt.Errorf("tls: %s has no pinned fingerprint (run with -tofu to trust on first use)", instance)
			case pin.LeafSHA256 == f.LeafSHA256:
				pin.LastSeen = now
				store.Pins[instance] = pin
				f.Mode = "tofu-match"
				f.PinMatchedSince = pin.FirstSeen
			default:
				store.mu.Unlock()
				f.Mode = "pin-mismatch"
				set(f)
				return fmt.Errorf("tls: %s presented a different certificate than the one pinned on first use", instance)
			}
			store.mu.Unlock()
			set(f)
			return nil
		},
	}
}

// ── A GET-only client ─────────────────────────────────────────────────────

// Client talks to one instance. It only has Get: there is no way to send
// anything else, and a transport guard refuses non-GET methods regardless.
type Client struct {
	in    Instance
	http  *http.Client
	auth  string
	delay time.Duration
	last  time.Time
	TLS   TLSFacts
	Debug bool
}

type getOnly struct{ rt http.RoundTripper }

func (g getOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, fmt.Errorf("refused: %s (this spike is read-only)", r.Method)
	}
	return g.rt.RoundTrip(r)
}

func newClient(in Instance, store *TOFUStore, allowNew bool, delay, timeout time.Duration) *Client {
	c := &Client{in: in, delay: delay, auth: "PVEAPIToken=" + in.TokenID + "=" + in.Secret}
	tr := &http.Transport{
		TLSClientConfig:     tofuConfig(in.Name, in.URL.Hostname(), store, allowNew, &c.TLS),
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns:        1,
		MaxConnsPerHost:     1, // sequential, one connection per instance
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true, // we ask for gzip ourselves so we can measure wire size
		ForceAttemptHTTP2:   false,
	}
	c.http = &http.Client{
		Transport: getOnly{tr},
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("refused: redirect (requests stay on the configured origin)")
		},
	}
	return c
}

// Response is one recorded GET.
type Response struct {
	Path      string        `json:"path"` // relative to /api2/json, with query
	Status    int           `json:"status"`
	Reason    string        `json:"reason,omitempty"` // PVE puts error text in the status line
	Latency   time.Duration `json:"-"`
	LatencyMS float64       `json:"ms"`
	WireBytes int           `json:"wire_bytes"` // as transferred (gzip if the server compressed)
	Bytes     int           `json:"bytes"`      // decoded body
	Encoding  string        `json:"encoding,omitempty"`
	Err       string        `json:"error,omitempty"`
	Body      []byte        `json:"-"`
}

// Get fetches /api2/json+path. It waits so requests are at least delay apart.
func (c *Client) Get(ctx context.Context, path string) Response {
	if wait := c.delay - time.Since(c.last); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	defer func() { c.last = time.Now() }()
	r := Response{Path: path}
	u := *c.in.URL
	u.Path = u.Path + "/api2/json"
	full := u.String() + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		r.Err = "bad request"
		return r
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "chadash-spike-pve/0 (read-only)")
	if c.Debug {
		fmt.Fprintf(os.Stderr, "  GET <%s>/api2/json%s  Authorization: PVEAPIToken=<redacted>\n", c.in.Name, path)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		r.Latency = time.Since(start)
		r.LatencyMS = ms(r.Latency)
		r.Err = scrubErr(err, c.in)
		return r
	}
	defer resp.Body.Close()
	wire, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	r.Latency = time.Since(start)
	r.LatencyMS = ms(r.Latency)
	r.Status = resp.StatusCode
	if len(resp.Status) > 4 {
		r.Reason = resp.Status[4:]
	}
	r.WireBytes = len(wire)
	r.Encoding = resp.Header.Get("Content-Encoding")
	if err != nil {
		r.Err = "read: " + scrubErr(err, c.in)
	}
	body := wire
	if r.Encoding == "gzip" {
		zr, err := gzip.NewReader(bytesReader(wire))
		if err == nil {
			body, err = io.ReadAll(zr)
		}
		if err != nil {
			r.Err = "gunzip failed"
		}
	}
	r.Body = body
	r.Bytes = len(body)
	return r
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// scrubErr turns a transport error into text without the URL or host.
func scrubErr(err error, in Instance) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timeout"
	}
	s := err.Error()
	for _, v := range []string{in.URL.String(), in.URL.Host, in.URL.Hostname(), in.TokenID, in.Secret} {
		s = replaceAllFold(s, v, "<"+in.Name+">")
	}
	return s
}
