package integration

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// TestResult is what "Test connection" reports. Build a failure with Failed;
// the runtime fills Latency when it's zero and adds the framework's warnings
// (write grants on a read-only connection, insecure TLS, credentials over
// plain http).
type TestResult struct {
	OK       bool
	Version  string        // upstream version, when the source tells
	Latency  time.Duration // how long the test took; the runtime measures it if zero
	Grants   []Grant       // what the credential can do, read back from the source
	Warnings []Warning
	// ErrorCode and ErrorMessage describe the failure when !OK; see the Code
	// constants.
	ErrorCode    string
	ErrorMessage string
	// Certificate is the leaf the server presented, set by the runtime.
	Certificate *Certificate
}

// Grant is one permission the credential holds, as the source reports it
// (Unraid's me, PVE's /access/permissions, OPNsense's ACL-filtered menu).
type Grant struct {
	Name  string `json:"name"`
	Write bool   `json:"write"` // it can change something
}

// Warning severities.
const (
	WarnInfo     = "info"
	WarnWarning  = "warning"
	WarnCritical = "critical"
)

// Warning is a quiet notice about an instance: a write grant on a read-only
// connection, an old upstream version, a capability not granted.
type Warning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Certificate identifies a server certificate.
type Certificate struct {
	SHA256 string `json:"sha256"` // lowercase hex of the DER
}

// Failed is a failed TestResult for err.
func Failed(err error) TestResult {
	code, _, msg := Classify(err)
	return TestResult{ErrorCode: code, ErrorMessage: msg}
}

// MarshalJSON writes the shape the test endpoints return:
// {ok, version, latency_ms, grants, warnings, error: {code, message} | null, certificate?}.
func (r TestResult) MarshalJSON() ([]byte, error) {
	type errBody struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	out := struct {
		OK          bool         `json:"ok"`
		Version     string       `json:"version"`
		LatencyMS   int64        `json:"latency_ms"`
		Grants      []Grant      `json:"grants"`
		Warnings    []Warning    `json:"warnings"`
		Error       *errBody     `json:"error"`
		Certificate *Certificate `json:"certificate,omitempty"`
	}{
		OK: r.OK, Version: r.Version, LatencyMS: r.Latency.Milliseconds(),
		Grants: r.Grants, Warnings: r.Warnings, Certificate: r.Certificate,
	}
	if out.Grants == nil {
		out.Grants = []Grant{}
	}
	if out.Warnings == nil {
		out.Warnings = []Warning{}
	}
	if !r.OK {
		code := r.ErrorCode
		if code == "" {
			code = CodeUpstream
		}
		out.Error = &errBody{Code: code, Message: r.ErrorMessage}
	}
	return json.Marshal(out)
}

// WriteGrantWarning is the warning for write grants on a read-only
// connection, or nil when there are none. Every v0.1 connection is read-only.
func WriteGrantWarning(grants []Grant) *Warning {
	var names []string
	for _, g := range grants {
		if g.Write {
			names = append(names, g.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return &Warning{
		Code:     "write_grant",
		Severity: WarnWarning,
		Message: fmt.Sprintf("This credential can change things (%s), but Cha Dash only reads. A read-only credential is safer.",
			strings.Join(names, ", ")),
	}
}

// Fingerprint is the lowercase hex SHA-256 of a certificate's DER, the form
// TLS pins use.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}
