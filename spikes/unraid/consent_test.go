package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestConsentURL(t *testing.T) {
	u, err := buildConsentURL("http://192.168.77.14/", consentName, consentDescription, consentScopes, consentRedirect, "s1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/Tools/ApiKeyAuthorize" || p.Host != "192.168.77.14" {
		t.Errorf("unexpected location %s%s", p.Host, p.Path)
	}
	q := p.Query()
	if q.Get("name") != "Cha Dash spike" || q.Get("state") != "s1" || q.Get("redirect_uri") != consentRedirect {
		t.Errorf("unexpected params: %v", q)
	}
	scopes := strings.Split(q.Get("scopes"), ",")
	if len(scopes) != len(consentScopes) {
		t.Fatalf("scopes: got %v", scopes)
	}
	for _, s := range scopes {
		if !strings.HasSuffix(s, ":read_any") {
			t.Errorf("scope %q is not read_any", s)
		}
	}
}

func TestConsentURLRefusesUnsafeInput(t *testing.T) {
	base := "http://192.168.77.14"
	cases := map[string]func() error{
		"name with parentheses": func() error {
			_, err := buildConsentURL(base, "Cha Dash (spike)", "", consentScopes, "", "")
			return err
		},
		"write scope": func() error {
			_, err := buildConsentURL(base, consentName, "", []string{"docker:update_any"}, "", "")
			return err
		},
		"wildcard scope": func() error {
			_, err := buildConsentURL(base, consentName, "", []string{"vms:*"}, "", "")
			return err
		},
		"admin role": func() error {
			_, err := buildConsentURL(base, consentName, "", []string{"role:admin"}, "", "")
			return err
		},
		"docs-style read (decodes to nothing)": func() error {
			_, err := buildConsentURL(base, consentName, "", []string{"docker:read"}, "", "")
			return err
		},
		"plain http redirect": func() error {
			_, err := buildConsentURL(base, consentName, "", consentScopes, "http://192.168.77.20/cb", "")
			return err
		},
		"127.0.0.1 redirect": func() error {
			_, err := buildConsentURL(base, consentName, "", consentScopes, "http://127.0.0.1:1/cb", "")
			return err
		},
		"no scopes": func() error {
			_, err := buildConsentURL(base, consentName, "", nil, "", "")
			return err
		},
		"bad base": func() error {
			_, err := buildConsentURL("ftp://x", consentName, "", consentScopes, "", "")
			return err
		},
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestConsentRedirectUsesABlockedPort(t *testing.T) {
	// Browsers refuse to connect to port 1 (fetch spec "bad ports"), so the redirect carrying
	// the key ends at an error page and reaches no process, even one listening on that port.
	r, err := url.Parse(consentRedirect)
	if err != nil {
		t.Fatal(err)
	}
	if r.Scheme != "http" || r.Hostname() != "localhost" || r.Port() != "1" {
		t.Errorf("redirect should be http://localhost:1/…, got %s", consentRedirect)
	}
}
