package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dash.cha.sh/spikes/node-transport/internal/testnet"
)

// The reverse-proxy problem: mTLS on the outer connection, the textbook
// setup, only works when the node's TLS session reaches the hub untouched.
// A proxy that terminates TLS (Traefik, Caddy, nginx, Cloudflare Tunnel by
// default) opens its own connection to the hub and has no client certificate
// to give, so the hub never sees the node's.
func TestOuterMTLSBehindProxies(t *testing.T) {
	caKey, ca := testCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	nodeCert := testClientCert(t, caKey, ca)

	hub := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "no client certificate", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "hello ", r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	// VerifyClientCertIfGiven, so a request without a certificate reaches the
	// handler and the test can see why it fails.
	hub.TLS = &tls.Config{ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool}
	hub.StartTLS()
	t.Cleanup(hub.Close)
	hubRoots := hub.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	node := func(roots *x509.CertPool) *http.Client {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{nodeCert}}}}
	}
	proxyURL, proxyClient := testnet.TLSReencryptProxy(t, hub.URL, &http.Transport{TLSClientConfig: &tls.Config{RootCAs: hubRoots}})
	proxyRoots := proxyClient.Transport.(*http.Transport).TLSClientConfig.RootCAs

	for _, tc := range []struct {
		path   string
		client *http.Client
		url    string
		want   int
	}{
		{"direct", node(hubRoots), hub.URL, http.StatusOK},
		{"tcp-passthrough", node(hubRoots), "https://" + testnet.TCPPassthrough(t, hub.Listener.Addr().String()), http.StatusOK},
		{"tls-terminating-proxy", node(proxyRoots), proxyURL, http.StatusUnauthorized},
	} {
		t.Run(tc.path, func(t *testing.T) {
			res, err := tc.client.Get(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("%d %s, want %d", res.StatusCode, body, tc.want)
			}
			t.Logf("%s: %d %s", tc.path, res.StatusCode, body)
		})
	}
}

func testCA(t *testing.T) (ed25519.PrivateKey, *x509.Certificate) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return key, ca
}

func testClientCert(t *testing.T, caKey ed25519.PrivateKey, ca *x509.Certificate) tls.Certificate {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "n_test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, key.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
