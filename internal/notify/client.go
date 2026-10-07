package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dash.cha.sh/internal/httpx"
)

// Outbound requests. Every message leaves through one of two hardened
// clients: no proxy from the environment, no redirects, short timeouts, a
// small response cap, and a dialer that checks the address it actually
// connects to (after DNS), so a name can't be pointed somewhere it
// shouldn't go.
//
//   - policyLAN (ntfy, webhooks): the admin names the server, and it's often
//     on the LAN, so private and loopback addresses are fine. Link-local
//     (169.254.0.0/16, where cloud metadata services answer, and fe80::/10),
//     unspecified, multicast and broadcast addresses are refused.
//   - policyPublic (Web Push): any signed-in user can register an endpoint,
//     and real ones are at the browser vendors' push services, so only
//     public unicast addresses are allowed.

type targetPolicy int

const (
	policyLAN targetPolicy = iota
	policyPublic
)

// Client limits.
const (
	dialTimeout     = 5 * time.Second
	requestTimeout  = 15 * time.Second
	maxResponseBody = 4 << 10
)

// errRefusedTarget is a dial the policy refused.
var errRefusedTarget = errors.New("notify: refused target address")

// allowed reports whether policy p lets the hub connect to ip.
func allowed(p targetPolicy, ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return false
	}
	// NAT64 and 6to4 carry an IPv4 address, which gets the same rule: a
	// translator would connect to it.
	for _, v4 := range httpx.EmbeddedIPv4(ip) {
		if !allowed(p, v4) {
			return false
		}
	}
	if p == policyLAN {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return false
	}
	for _, pre := range nonPublic {
		if pre.Contains(ip) {
			return false
		}
	}
	return true
}

// nonPublic are ranges that aren't private by netip's definition but still
// aren't the public internet.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT, and Tailscale
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	// Local-use NAT64 (RFC 8215) is never the public internet. The
	// well-known 64:ff9b::/96 and 6to4's 2002::/16 are allowed when the
	// IPv4 address they carry is (httpx.EmbeddedIPv4).
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// newClient is a hardened client for policy p.
func newClient(p targetPolicy) *http.Client {
	d := &net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errRefusedTarget
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !allowed(p, ip) {
				return errRefusedTarget
			}
			return nil
		},
	}
	t := &http.Transport{
		Proxy:                  nil,
		DialContext:            d.DialContext,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  requestTimeout,
		MaxResponseHeaderBytes: 16 << 10,
		MaxIdleConns:           8,
		IdleConnTimeout:        90 * time.Second,
		ForceAttemptHTTP2:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: t,
		Timeout:   requestTimeout,
		// A redirect is answered as it is, and counts as a failure: a
		// message never follows one somewhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// attempt is how one try went.
type attempt struct {
	status     int
	err        string        // a sentence for the delivery log; "" on success
	retry      bool          // worth trying again later
	retryAfter time.Duration // the server asked for a pause (429, 503)
	gone       bool          // Web Push: the subscription has expired
}

func (a attempt) ok() bool { return a.err == "" }

// do sends req and sums up the answer. The error sentence never carries
// the URL (a webhook URL can hold a token), a response body or a header.
func (s *Service) do(c *http.Client, req *http.Request) attempt {
	res, err := c.Do(req)
	if err != nil {
		return attempt{err: describeErr(err), retry: !errors.Is(err, errRefusedTarget)}
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxResponseBody))
	a := attempt{status: res.StatusCode}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return a
	case res.StatusCode >= 300 && res.StatusCode < 400:
		a.err = "It answered " + strconv.Itoa(res.StatusCode) + " (a redirect), and redirects aren't followed. Check the address."
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		a.err = "It answered " + strconv.Itoa(res.StatusCode) + ": the token or secret wasn't accepted."
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusServiceUnavailable:
		a.err, a.retry = "It answered "+strconv.Itoa(res.StatusCode)+" and asked to slow down.", true
		a.retryAfter = retryAfter(res.Header.Get("Retry-After"), s.now())
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode >= 500:
		a.err, a.retry = "It answered "+strconv.Itoa(res.StatusCode)+".", true
	default:
		a.err = "It answered " + strconv.Itoa(res.StatusCode) + "."
	}
	return a
}

// retryAfter reads Retry-After (seconds or an HTTP date), capped at an hour.
func retryAfter(v string, now time.Time) time.Duration {
	var d time.Duration
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), time.Hour)
}

// describeErr turns a transport error into a sentence, without the URL.
func describeErr(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	var uaErr x509.UnknownAuthorityError
	var ue *url.Error
	switch {
	case errors.Is(err, errRefusedTarget):
		return "Its address isn't one Cha Dash sends to (link-local, multicast, or not public for Web Push)."
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ue) && ue.Timeout()):
		return "It didn't answer within " + strconv.Itoa(int(requestTimeout/time.Second)) + " s."
	case errors.As(err, &dnsErr):
		return "Its name couldn't be resolved."
	case errors.As(err, &certErr) || errors.As(err, &hostErr) || errors.As(err, &uaErr):
		return "Its TLS certificate wasn't accepted."
	case errors.Is(err, syscall.ECONNREFUSED):
		return "It refused the connection."
	case errors.Is(err, context.Canceled):
		return "The hub stopped before it finished."
	}
	return "It couldn't be reached."
}
