package labkit

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
)

// TLSResult records how the server's certificate was trusted.
type TLSResult struct {
	mu       sync.Mutex
	Verified bool // the chain verified against the system roots
	Pinned   bool // trust came from a TOFU pin instead
	FirstUse bool // the pin was created during this run
}

// TOFUTransport returns a transport that verifies the server certificate
// against the system roots first. Only when that fails (a self-signed or
// private-CA certificate) does it fall back to trust-on-first-use: the leaf's
// SHA-256 is pinned in pinFile (git-ignored, outside the repo) the first time,
// and any later mismatch is a hard error. Verification is never just skipped.
func TOFUTransport(pinFile string, res *TLSResult) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The default verifier is replaced by VerifyConnection below, which
		// runs the same system-roots verification and then the TOFU pin.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("tls: no peer certificate")
			}
			leaf := cs.PeerCertificates[0]
			opts := x509.VerifyOptions{DNSName: cs.ServerName, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			if _, err := leaf.Verify(opts); err == nil {
				res.set(true, false, false)
				return nil
			}
			sum := sha256.Sum256(leaf.Raw)
			fp := hex.EncodeToString(sum[:])
			pins := map[string]string{}
			if b, err := os.ReadFile(pinFile); err == nil {
				_ = json.Unmarshal(b, &pins)
			}
			if want, ok := pins[cs.ServerName]; ok {
				if want != fp {
					return errors.New("tls: TOFU pin mismatch: the certificate changed since first use")
				}
				res.set(false, true, false)
				return nil
			}
			pins[cs.ServerName] = fp
			b, _ := json.MarshalIndent(pins, "", "  ")
			if err := os.WriteFile(pinFile, b, 0o600); err != nil {
				return errors.New("tls: cannot store TOFU pin")
			}
			res.set(false, true, true)
			return nil
		},
	}
	return tr
}

func (r *TLSResult) set(verified, pinned, first bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Verified, r.Pinned = verified, pinned
	r.FirstUse = r.FirstUse || first
}
