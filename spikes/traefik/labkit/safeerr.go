package labkit

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"syscall"
)

// SafeErr reduces a network error to its kind, so logs never carry the URL,
// hostname or address that net/url/x509 errors embed.
func SafeErr(err error) string {
	if err == nil {
		return ""
	}
	var dns *net.DNSError
	var op *net.OpError
	var hn x509.HostnameError
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	switch {
	case errors.Is(err, ErrRefused):
		return "refused by the read-only client"
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "DNS: name not found"
		}
		if dns.IsTimeout {
			return "DNS: timeout"
		}
		return "DNS: lookup failed"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "host or network unreachable"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.As(err, &hn):
		return "TLS: certificate does not match the hostname"
	case errors.As(err, &ua):
		return "TLS: unknown certificate authority"
	case errors.As(err, &ci):
		return "TLS: certificate invalid"
	case errors.As(err, &op):
		if op.Timeout() {
			return "timeout (" + op.Op + ")"
		}
		return "network error (" + op.Op + ")"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "request failed"
}
