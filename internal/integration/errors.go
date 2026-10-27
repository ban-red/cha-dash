package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
)

// Error codes, stable for the web app. They appear in TestResult.error,
// last_error and diagnostics.
const (
	CodeAuth               = "auth"                // the credential was rejected (401/403)
	CodeTLSUntrusted       = "tls_untrusted"       // the certificate doesn't verify
	CodeTLSPinMismatch     = "tls_pin_mismatch"    // the certificate isn't the pinned one
	CodeAPIChanged         = "api_changed"         // an endpoint the integration relies on is gone
	CodeUnsupportedVersion = "unsupported_version" // the upstream version isn't supported
	CodeInvalidConfig      = "invalid_config"      // the config can't work as written
	CodeTimeout            = "timeout"
	CodeUnreachable        = "unreachable" // refused, no route, DNS
	CodeUpstream           = "upstream"    // anything else the source did wrong
	CodeInternal           = "internal"    // a bug in Cha Dash (a panic in an integration)
)

// Class says how the runtime reacts to an error.
type Class int

// Error classes.
const (
	// Transient errors are retried with exponential backoff.
	Transient Class = iota
	// NeedsRepair errors need the user: they raise a Repair in the inbox and
	// are retried slowly, so fixing the upstream clears it on its own.
	NeedsRepair
	// Permanent errors (a config that can't work) are retried slowly, without
	// a Repair: the instance shows the error.
	Permanent
)

// StatusError is an HTTP response outside 2xx. 401 and 403 classify as
// CodeAuth. The message names the method and path, never the query string,
// which may carry a key.
type StatusError struct {
	Method string
	Path   string
	Code   int
}

func (e *StatusError) Error() string {
	text := http.StatusText(e.Code)
	if text == "" {
		text = "status"
	}
	return fmt.Sprintf("%s %s answered %d %s", e.Method, e.Path, e.Code, text)
}

// CheckResponse returns a *StatusError unless resp is 2xx. The caller still
// closes the body.
func CheckResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	e := &StatusError{Code: resp.StatusCode}
	if resp.Request != nil {
		e.Method = resp.Request.Method
		if resp.Request.URL != nil {
			e.Path = resp.Request.URL.Path
		}
	}
	return e
}

// AuthError is an explicit credential failure, for sources that report it
// inside a 200 (GraphQL's UNAUTHENTICATED, a JSON error field), or for any
// auth failure whose field the integration knows.
type AuthError struct {
	Message string
	Field   string // the secret that was rejected, when the integration knows it
}

func (e *AuthError) Error() string {
	if e.Message == "" {
		return "the credential was rejected"
	}
	return e.Message
}

// RepairError is a failure only the user can fix: the API changed, the
// version isn't supported, a needed permission is missing. Code is one of the
// Code constants or an integration-specific one.
type RepairError struct {
	Code    string
	Message string
	Field   string // the config or secret field to fix, when known
}

func (e *RepairError) Error() string { return e.Message }

// ConfigError is a config that can't work as written (it passed the schema
// but, say, names a socket that doesn't exist).
type ConfigError struct {
	Message string
	Field   string // the field that's wrong, when known
}

func (e *ConfigError) Error() string { return e.Message }

// FieldOf returns the config or secret field an error points at, when the
// integration said: AuthError, RepairError and ConfigError carry it, and a
// TLS refusal points at "tls". The runtime puts it in a repair's
// facts.field, so the UI opens the right input.
func FieldOf(err error) string {
	var (
		ae *AuthError
		re *RepairError
		ce *ConfigError
		te *TLSError
	)
	switch {
	case errors.As(err, &ae):
		return ae.Field
	case errors.As(err, &re):
		return re.Field
	case errors.As(err, &ce):
		return ce.Field
	case errors.As(err, &te):
		return "tls"
	}
	return ""
}

// TLSError is a certificate the instance's TLS policy refused.
type TLSError struct {
	Code        string // CodeTLSUntrusted or CodeTLSPinMismatch
	Fingerprint string // SHA-256 of the leaf the server presented, lowercase hex
	Expected    string // the pin, for a mismatch
	Err         error  // the verification error, for untrusted
}

func (e *TLSError) Error() string {
	switch e.Code {
	case CodeTLSPinMismatch:
		return fmt.Sprintf("the server presented a different certificate (SHA-256 %s, pinned %s)", e.Fingerprint, e.Expected)
	default:
		msg := "the server's certificate isn't trusted"
		if e.Err != nil {
			msg += " (" + e.Err.Error() + ")"
		}
		return msg + "; trust it on first use (tofu) or pin its SHA-256 fingerprint " + e.Fingerprint
	}
}

func (e *TLSError) Unwrap() error { return e.Err }

// Classify maps an error to a stable code, a class and a message for people.
// Integrations rarely need it; the runtime calls it on every failed Sync and
// Test.
func Classify(err error) (code string, class Class, message string) {
	if err == nil {
		return "", Transient, ""
	}
	var (
		se   *StatusError
		ae   *AuthError
		re   *RepairError
		ce   *ConfigError
		te   *TLSError
		cve  *tls.CertificateVerificationError
		ne   net.Error
		dnse *net.DNSError
		ope  *net.OpError
	)
	switch {
	case errors.As(err, &te):
		return te.Code, NeedsRepair, te.Error()
	case errors.As(err, &cve):
		t := &TLSError{Code: CodeTLSUntrusted, Err: cve.Err}
		if len(cve.UnverifiedCertificates) > 0 {
			t.Fingerprint = Fingerprint(cve.UnverifiedCertificates[0])
		}
		return t.Code, NeedsRepair, t.Error()
	case errors.As(err, &ae):
		return CodeAuth, NeedsRepair, ae.Error()
	case errors.As(err, &se):
		if se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden {
			return CodeAuth, NeedsRepair, se.Error() + ": the credential was rejected"
		}
		return CodeUpstream, Transient, se.Error()
	case errors.As(err, &re):
		code := re.Code
		if code == "" {
			code = CodeAPIChanged
		}
		return code, NeedsRepair, re.Message
	case errors.As(err, &ce):
		return CodeInvalidConfig, Permanent, ce.Message
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return CodeTimeout, Transient, "timed out: " + scrubURL(err)
	case errors.As(err, &dnse), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, syscall.ENETUNREACH), errors.As(err, &ope):
		return CodeUnreachable, Transient, scrubURL(err)
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return CodeTLSUntrusted, NeedsRepair, err.Error()
	}
	return CodeUpstream, Transient, scrubURL(err)
}

// scrubURL renders an error without the query string of any URL in it:
// url.Error prints the whole URL, and some APIs take keys as parameters.
func scrubURL(err error) string {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err.Error()
	}
	u := ue.URL
	if p, perr := url.Parse(u); perr == nil {
		p.RawQuery, p.Fragment, p.User = "", "", nil
		u = p.String()
	} else if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return fmt.Sprintf("%s %s: %v", ue.Op, u, ue.Err)
}
