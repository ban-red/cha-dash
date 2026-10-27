package unraid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"dash.cha.sh/internal/integration"
)

// The read queries, one GraphQL operation each, so a field an API version
// lacks fails only its own read. Every field here was read safely by spike
// 4.2 (spikes/unraid). There is deliberately no `disks` query: whatever
// fields it asks for, its resolver runs `smartctl -a` on every disk, which
// wakes spun-down drives. Temperature and spin state come from `array`,
// which reads emhttp's state and touches no disk.
const (
	qVersion = `query ChadashVersion { info { versions { core { unraid api } } } }`
	qInfo    = `query ChadashInfo {
  info {
    os { hostname uptime }
    cpu { brand cores threads }
    versions { core { unraid api } }
  }
}`
	qNetwork = `query ChadashNetwork { info { networkInterfaces { name ipAddress virtual operstate } } }`
	qMetrics = `query ChadashMetrics { metrics { cpu { percentTotal } memory { total percentTotal } } }`
	qArray   = `query ChadashArray {
  array {
    state
    capacity { kilobytes { free used total } }
    parityCheckStatus { status errors running paused date }
    boot { ...Device }
    parities { ...Device }
    disks { ...Device }
    caches { ...Device }
  }
}
fragment Device on ArrayDisk {
  id idx name device size status type temp isSpinning numErrors fsSize fsUsed fsType rotational transport
}`
	qDocker = `query ChadashDocker {
  docker { containers { id names image state autoStart autoStartOrder autoStartWait labels mounts webUiUrl } }
}`
	qShares = `query ChadashShares { shares { name include exclude } }`
	qVMs    = `query ChadashVMs { vms { domains { id name state } } }`
	qMe     = `query ChadashMe { me { name roles permissions { resource actions } } }`
)

// queries lists every read document the integration can send (tests check
// them); mutations (actions.go) are the only writes.
var queries = []string{qVersion, qInfo, qNetwork, qMetrics, qArray, qDocker, qShares, qVMs, qMe}

// What each operation reads, and the Unraid resource its permission names.
var reads = map[string]struct{ what, resource string }{
	"ChadashVersion": {"the API version", "INFO"},
	"ChadashInfo":    {"system information", "INFO"},
	"ChadashNetwork": {"network interfaces", "INFO"},
	"ChadashMetrics": {"CPU and memory use", "INFO"},
	"ChadashArray":   {"the array", "ARRAY"},
	"ChadashDocker":  {"Docker containers", "DOCKER"},
	"ChadashShares":  {"shares", "SHARE"},
	"ChadashVMs":     {"VMs", "VMS"},
	"ChadashMe":      {"its own permissions", "ME"},
}

// CodeMissingPermission is the error and warning code for a read the API key
// isn't allowed.
const CodeMissingPermission = "missing_permission"

// keysPage is where Unraid keeps API keys.
const keysPage = "Settings → Management Access → API Keys"

type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

// forbiddenError is a read the key isn't granted (GraphQL FORBIDDEN).
type forbiddenError struct{ op string }

func (e *forbiddenError) Error() string {
	r := reads[e.op]
	return fmt.Sprintf("the API key can't read %s (it needs %s: Read)", r.what, r.resource)
}

// repair turns a refused read that the integration can't do without into a
// Repair that says what to grant.
func (e *forbiddenError) repair() error {
	r := reads[e.op]
	return &integration.RepairError{Code: CodeMissingPermission, Field: "api_key",
		Message: fmt.Sprintf("The API key can't read %s. Give it %s: Read in Unraid (%s), or use a key that has it.", r.what, r.resource, keysPage)}
}

// apiMissing is what a server without the Unraid API answers.
func (in *instance) apiMissing(detail string) error {
	return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
		Message: fmt.Sprintf("%s doesn't answer like the Unraid API (%s). Unraid 7.2 and later include it; earlier releases need the Unraid Connect plugin.", in.endpoint, detail)}
}

// query runs one read and decodes its data into dst. GraphQL reports most
// failures inside a 200, so the error codes are mapped here: UNAUTHENTICATED
// is a rejected key, FORBIDDEN a missing permission (a *forbiddenError),
// validation failures an API that changed.
func (in *instance) query(ctx context.Context, op, doc string, dst any) error {
	return in.do(ctx, in.d.HTTP, op, doc, nil, dst)
}

// do sends one GraphQL operation with c: a read through query, or one of
// the actions' mutations, with its variables.
func (in *instance) do(ctx context.Context, c *http.Client, op, doc string, vars map[string]any, dst any) error {
	msg := map[string]any{"query": doc, "operationName": op}
	if vars != nil {
		msg["variables"] = vars
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.endpoint, bytes.NewReader(body))
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the url can't be used: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", in.apiKey)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var r gqlResponse
	jerr := json.Unmarshal(raw, &r)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if jerr == nil && len(r.Errors) > 0 {
			return in.failure(op, r.Errors)
		}
		if resp.StatusCode == http.StatusNotFound {
			return in.apiMissing("404 Not Found")
		}
		return integration.CheckResponse(resp)
	}
	if jerr != nil {
		return in.apiMissing("the answer isn't JSON")
	}
	if len(r.Errors) > 0 {
		return in.failure(op, r.Errors)
	}
	if len(r.Data) == 0 || string(r.Data) == "null" {
		return fmt.Errorf("the Unraid API answered %s without data", op)
	}
	if err := json.Unmarshal(r.Data, dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("The Unraid API's answer to %s has a shape this integration doesn't know: %v", op, err)}
	}
	return nil
}

// failure maps GraphQL errors to the framework's error classes, the most
// specific first.
func (in *instance) failure(op string, errs []gqlError) error {
	has := func(codes ...string) (gqlError, bool) {
		for _, e := range errs {
			if slices.Contains(codes, e.Extensions.Code) {
				return e, true
			}
		}
		return gqlError{}, false
	}
	if _, ok := has("UNAUTHENTICATED"); ok {
		return &integration.AuthError{Field: "api_key", Message: "Unraid rejected the API key"}
	}
	if _, ok := has("FORBIDDEN"); ok {
		return &forbiddenError{op: op}
	}
	if e, ok := has("GRAPHQL_VALIDATION_FAILED", "GRAPHQL_PARSE_FAILED", "BAD_USER_INPUT"); ok {
		v := "this version"
		if api := in.apiVersion(); api != "" {
			v = "API " + api
		}
		return &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("The Unraid API (%s) no longer accepts the %s read: %s", v, op, e.Message)}
	}
	return fmt.Errorf("the Unraid API answered %s with an error: %s", op, errs[0].Message)
}

// num is a number Unraid sends as a JSON number, or as a string for its
// BigInt values (capacity). ok is false for null.
type num struct {
	v  float64
	ok bool
}

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*n = num{}
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("%s isn't a number", b)
	}
	*n = num{v: f, ok: true}
	return nil
}

// or returns the value, or NaN (unknown) when it's null.
func (n num) or() float64 {
	if !n.ok {
		return integration.Unknown()
	}
	return n.v
}

// version is a semantic version; pre marks a pre-release, which sorts
// before its release.
type version struct {
	major, minor, patch int
	pre                 bool
}

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

// parseVersion reads "4.37.2+a00c99a5" (build metadata ignored) or
// "v4.38.0-beta.1".
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, v.pre = s[:i], true
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	n := make([]int, 3)
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return version{}, false
		}
		n[i] = x
	}
	v.major, v.minor, v.patch = n[0], n[1], n[2]
	return v, true
}

func (v version) less(w version) bool {
	switch {
	case v.major != w.major:
		return v.major < w.major
	case v.minor != w.minor:
		return v.minor < w.minor
	case v.patch != w.patch:
		return v.patch < w.patch
	}
	return v.pre && !w.pre
}

// checkVersion refuses an API older than MinAPI: before 4.37.4 the API's
// guard allowed any handler without permission metadata, so a read-only key
// could change UPS settings, notifications and more (unraid/api#2074).
func checkVersion(api string) error {
	v, ok := parseVersion(api)
	if !ok {
		return &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Cha Dash couldn't read the Unraid API's version (%q). It needs API %s or later.", api, MinAPI)}
	}
	if v.less(MinAPI) {
		return &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("The Unraid API is version %s. Cha Dash needs %s or later, which fixes an authorization bug: on older versions any API key, even a read-only one, can change settings. Update Unraid, or install the latest Unraid Connect plugin, to get API %s or later; Cha Dash reconnects on its own.",
				shortVersion(api), MinAPI, MinAPI)}
	}
	return nil
}

// shortVersion drops build metadata: "4.37.2+a00c99a5" is "4.37.2".
func shortVersion(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "+")
	return s
}
