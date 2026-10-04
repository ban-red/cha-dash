package integration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Config is an instance's config as Connect sees it: the stored JSON with
// every secret resolved from the vault.
type Config struct {
	InstanceID string // "" for an unsaved config under test
	Name       string
	Values     map[string]any
}

// Decode unmarshals the config into dst (a struct with json tags).
func (c Config) Decode(dst any) error {
	b, err := json.Marshal(c.Values)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// String returns a top-level string value, or "".
func (c Config) String(key string) string {
	s, _ := c.Values[key].(string)
	return s
}

// TLS returns the config's tls policy (verify when absent).
func (c Config) TLS() TLSPolicy {
	p, err := ParseTLS(c.Values["tls"])
	if err != nil {
		return TLSPolicy{Mode: TLSVerify}
	}
	return p
}

// TLSMode is how an instance checks a server's certificate.
type TLSMode string

// TLS modes. The policy is per instance, never global.
const (
	// TLSVerify checks the chain against the system roots and the URL host.
	TLSVerify TLSMode = "verify"
	// TLSPin accepts only the pinned certificate: the leaf, or a CA in the
	// presented chain that the leaf verifies against (the host isn't checked;
	// the pin is the identity).
	TLSPin TLSMode = "pin"
	// TLSTOFU trusts the first certificate seen, records its SHA-256 as the
	// pin in the instance config, and then behaves like TLSPin.
	TLSTOFU TLSMode = "tofu"
	// TLSInsecure skips certificate checks. A labelled escape hatch; the test
	// endpoint warns about it.
	TLSInsecure TLSMode = "insecure"
)

// TLSPolicy is an instance's tls key: {"mode": …, "pin": "<sha256>"}.
type TLSPolicy struct {
	Mode TLSMode `json:"mode"`
	Pin  string  `json:"pin,omitempty"` // lowercase hex SHA-256 of a certificate's DER
}

// pinPattern accepts 64 hex digits, optionally colon-separated in pairs the
// way Proxmox and browsers show fingerprints.
const pinPattern = `^([0-9A-Fa-f]{64}|([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2})$`

var pinRE = regexp.MustCompile(pinPattern)

// NormalizePin turns a fingerprint in either accepted form into lowercase hex.
func NormalizePin(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !pinRE.MatchString(s) {
		return "", fmt.Errorf("a pin is a SHA-256 fingerprint: 64 hex digits, optionally colon-separated")
	}
	return strings.ToLower(strings.ReplaceAll(s, ":", "")), nil
}

// ParseTLS reads a tls value from a decoded config. nil means verify.
func ParseTLS(v any) (TLSPolicy, error) {
	if v == nil {
		return TLSPolicy{Mode: TLSVerify}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return TLSPolicy{}, fmt.Errorf("tls must be an object")
	}
	p := TLSPolicy{Mode: TLSVerify}
	if mode, ok := m["mode"].(string); ok && mode != "" {
		p.Mode = TLSMode(mode)
	}
	switch p.Mode {
	case TLSVerify, TLSPin, TLSTOFU, TLSInsecure:
	default:
		return TLSPolicy{}, fmt.Errorf("tls mode %q isn't one of verify, pin, tofu, insecure", p.Mode)
	}
	if pin, ok := m["pin"].(string); ok && pin != "" {
		n, err := NormalizePin(pin)
		if err != nil {
			return TLSPolicy{}, err
		}
		p.Pin = n
	}
	if p.Mode == TLSPin && p.Pin == "" {
		return TLSPolicy{}, fmt.Errorf("tls mode pin needs a pin")
	}
	return p, nil
}

// SecretRef is the reference stored in place of a secret value:
// ${secret:<vault name>}, the form the YAML export and the importer use.
func SecretRef(name string) string { return "${secret:" + name + "}" }

// ParseSecretRef returns the vault name in a ${secret:…} reference.
func ParseSecretRef(s string) (string, bool) {
	name, ok := strings.CutPrefix(s, "${secret:")
	if !ok {
		return "", false
	}
	name, ok = strings.CutSuffix(name, "}")
	if !ok || name == "" {
		return "", false
	}
	return name, true
}
