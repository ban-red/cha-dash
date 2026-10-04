package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// Test values only; none of these belong to a real server.
const (
	testKey = "3f9a1c0e5b7d2a4f6e8c0b1d3a5f7e9c2b4d6f8a0c1e3b5d7f9a2c4e6b8d0f1a"
	testURL = "http://192.168.77.14"
)

func TestRedactKeyAndURL(t *testing.T) {
	r := NewRedactor()
	r.AddURL(testURL)
	r.Add(testKey, "<api-key>")

	in := fmt.Sprintf("POST %s/graphql x-api-key=%s failed; ws://192.168.77.14/graphql; host 192.168.77.14:80", testURL, testKey)
	out := r.Redact(in)
	for _, leak := range []string{testKey, "192.168.77.14"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %q", leak, out)
		}
	}
	if !strings.Contains(out, "<unraid-url>/graphql") {
		t.Errorf("expected the URL label, got %q", out)
	}
}

func TestRedactNetHTTPError(t *testing.T) {
	r := NewRedactor()
	r.AddURL(testURL)
	err := &url.Error{Op: "Post", URL: testURL + "/graphql", Err: errors.New("dial tcp 192.168.77.14:80: connect: connection refused")}
	out := r.Err(err)
	if strings.Contains(out, "192.168.77.14") {
		t.Fatalf("leaked the IP: %q", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Errorf("lost the useful part: %q", out)
	}
}

func TestRedactHostnameCaseInsensitive(t *testing.T) {
	r := NewRedactor()
	r.AddURL("https://Tower-Nine.home.arpa:8443")
	out := r.Redact("Get \"https://tower-nine.home.arpa:8443/graphql\": x509: certificate is valid for TOWER-NINE, not tower-nine.home.arpa")
	for _, leak := range []string{"tower-nine", "TOWER-NINE", "home.arpa:8443"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(leak)) {
			t.Fatalf("leaked %q: %q", leak, out)
		}
	}
}

func TestRedactIgnoresEmptyAndShort(t *testing.T) {
	r := NewRedactor()
	r.Add("", "<x>")
	r.Add("ab", "<x>")
	r.AddURL("")
	in := "array STARTED, 7 disks, ab cd"
	if out := r.Redact(in); out != in {
		t.Errorf("empty or short secrets changed the output: %q", out)
	}
}

func TestRedactGenericShapes(t *testing.T) {
	r := NewRedactor() // nothing registered: the pattern scrubbers still apply
	in := "redirect to http://localhost:1/cb?api_key=SECRETVALUE123&state=abc " +
		"from 10.20.30.40, mac 02:42:ac:11:00:02, key " + testKey + ", header x-api-key: abcDEF123"
	out := r.Redact(in)
	for _, leak := range []string{"SECRETVALUE123", "10.20.30.40", "02:42:ac:11:00:02", testKey, "abcDEF123"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in %q", leak, out)
		}
	}
	if !strings.Contains(out, "state=abc") {
		t.Errorf("state is not a secret and should survive: %q", out)
	}
}

func TestRedactLongestFirst(t *testing.T) {
	r := NewRedactor()
	r.Add("192.168.77.14", "<host>")
	r.Add("http://192.168.77.14", "<url>")
	if out := r.Redact("http://192.168.77.14/x"); out != "<url>/x" {
		t.Errorf("want the longer secret replaced first, got %q", out)
	}
}
