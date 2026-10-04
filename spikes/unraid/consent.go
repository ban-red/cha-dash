package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// The consent page lives in the webGUI at /Tools/ApiKeyAuthorize (that's the URL Unraid's own
// generateAuthorizationUrl() builds; the docs show /ApiKeyAuthorize, which the webGUI also
// routes). The page reads name, description, scopes, redirect_uri and state from the query.
const consentPath = "/Tools/ApiKeyAuthorize"

// consentName becomes the API key's name, so it must pass the API's rule: letters, numbers and
// spaces only. "Cha Dash (spike)" would fail key creation because of the parentheses.
const consentName = "Cha Dash spike"

const consentDescription = "Cha Dash (spike) read-only plumbing test 4.2. " +
	"Reads system info, array and disks, Docker containers, VMs, shares and notifications. " +
	"No write access. Revoke it any time under Settings, Management Access, API Keys."

// consentScopes is the minimum read-only set for the v0.1 integration. The consent page's
// parser wants "resource:read_any": the docs' "docker:read" decodes to no permission at all.
// One scope per resource (rather than the grouped "a+b:read_any" form) keeps the URL legible.
var consentScopes = []string{
	"info:read_any",          // host identity, versions, CPU/memory metrics and their subscriptions
	"array:read_any",         // array state, capacity, parity status and history, pool devices
	"disk:read_any",          // physical disks: model, serial (correlator key), SMART status, spin state
	"docker:read_any",        // containers, ports, labels, networks, container stats subscription
	"vms:read_any",           // VM list and power state
	"share:read_any",         // user shares and where they live
	"notifications:read_any", // Unraid's own notifications (parity errors, disk warnings, updates)
	"me:read_any",            // the key's own roles and permissions, to see what the user actually granted
}

// consentRedirect needs no server. Unraid accepts only https:// or http://localhost redirect
// URIs; port 1 is on every browser's blocked-port list, so after "Send Key" the browser stops
// with an error page and never connects anywhere, leaving api_key=…&state=… readable in the
// address bar. The key never leaves the browser.
const consentRedirect = "http://localhost:1/cha-dash-spike/unraid-callback"

var keyNameOK = regexp.MustCompile(`^[\p{L}\p{N} ]+$`)

func newState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// buildConsentURL returns the ApiKeyAuthorize URL for base (the server's webGUI URL).
func buildConsentURL(base, name, description string, scopes []string, redirect, state string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("base URL must be an http(s) URL")
	}
	if !keyNameOK.MatchString(name) {
		return "", fmt.Errorf("name %q would fail Unraid's API key name rule (letters, numbers, spaces)", name)
	}
	if len(scopes) == 0 {
		return "", fmt.Errorf("at least one scope is required")
	}
	for _, s := range scopes {
		res, act, ok := strings.Cut(s, ":")
		if !ok || res == "" || act == "" {
			return "", fmt.Errorf("scope %q is not resource:action", s)
		}
		if res != "role" && act != "read_any" {
			return "", fmt.Errorf("scope %q is not read-only", s)
		}
		if res == "role" && act != "viewer" && act != "guest" {
			return "", fmt.Errorf("role scope %q is not read-only", s)
		}
	}
	if redirect != "" {
		r, err := url.Parse(redirect)
		if err != nil || !(r.Scheme == "https" || (r.Scheme == "http" && r.Hostname() == "localhost")) {
			return "", fmt.Errorf("redirect_uri must be https:// or http://localhost (Unraid rejects anything else)")
		}
	}
	q := url.Values{}
	q.Set("name", name)
	if description != "" {
		q.Set("description", description)
	}
	q.Set("scopes", strings.Join(scopes, ","))
	if redirect != "" {
		q.Set("redirect_uri", redirect)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.Path = consentPath
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}
