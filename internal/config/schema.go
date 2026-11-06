package config

import (
	"encoding/json"
	"sort"
)

// The config schema (plan 15, "Toward 1.0"): every setting the hub and
// chadash-node take, from flags and the environment, published as a JSON
// Schema at GET /api/config/schema.json with a version. v1.0 promises the
// config; until then the schema makes it checkable. Tests tie it to what
// the code reads: TestSchemaMatchesFlags (the hub's FlagSet) and
// cmd/chadash-node's TestConfigSchema (the env keys the node asks for), so
// neither can drift from it.

// SchemaVersion is the config schema's version. It goes up when a setting
// is removed or changes meaning; adding one doesn't change it. A removed
// setting is deprecated for at least one minor version first
// (CONTRIBUTING.md, "Deprecations").
const SchemaVersion = 1

// Option is one setting.
type Option struct {
	// Env is its environment variable; Flag its command-line flag, "" when
	// it's environment only.
	Env  string `json:"env"`
	Flag string `json:"flag,omitempty"`
	// Type is string, boolean or integer.
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	// Enum, when set, are the only values taken.
	Enum []string `json:"enum,omitempty"`
	Doc  string   `json:"description"`
	// Secret: its value is a credential (never logged, never in the
	// process list: environment only).
	Secret bool `json:"secret,omitempty"`
	// Deprecated names the version that deprecated it.
	Deprecated string `json:"deprecated,omitempty"`
}

// HubOptions are the hub's settings, in the order README's Configuration
// table lists them.
var HubOptions = []Option{
	{Env: "CHADASH_LISTEN", Flag: "listen", Type: "string", Default: DefaultListen, Doc: "The address to listen on."},
	{Env: "CHADASH_DATA_DIR", Flag: "data-dir", Type: "string", Default: DefaultDataDir, Doc: "The data directory: the database, the vault key, the layout history."},
	{Env: "CHADASH_SECRET_KEY_FILE", Flag: "secret-key-file", Type: "string", Doc: "The vault key file. Default <data-dir>/secret.key, generated on first boot."},
	{Env: "CHADASH_LOG_LEVEL", Flag: "log-level", Type: "string", Default: "info", Enum: []string{"debug", "info", "warn", "error"}, Doc: "How much the hub logs."},
	{Env: "CHADASH_LOG_FORMAT", Flag: "log-format", Type: "string", Default: "text", Enum: []string{"text", "json"}, Doc: "The log's format."},
	{Env: "CHADASH_TRUSTED_PROXIES", Flag: "trusted-proxies", Type: "string", Doc: "Comma-separated IPs or CIDRs of reverse proxies whose X-Forwarded-* headers are believed."},
	{Env: "CHADASH_ALLOWED_ORIGINS", Flag: "allowed-origins", Type: "string", Doc: "Comma-separated extra browser origins (scheme://host[:port]) for proxies that rewrite Host."},
	{Env: "CHADASH_PUBLIC_URL", Flag: "public-url", Type: "string", Doc: "The address you open Cha Dash at (scheme://host[:port]). Passkeys need it, over HTTPS."},
	{Env: "CHADASH_DEMO", Flag: "demo", Type: "boolean", Default: "false", Doc: "Run the Demo Home simulator instead of a real home."},
	{Env: "CHADASH_DEMO_AT", Flag: "demo-at", Type: "string", Default: "local", Doc: "The demo's clock: local, a time of day (HH:MM[:SS], UTC), or now."},
	{Env: "CHADASH_DEMO_SEED", Flag: "demo-seed", Type: "integer", Default: "1", Doc: "The demo simulator's seed."},
	{Env: "CHADASH_DEMO_PUBLIC", Flag: "demo-public", Type: "boolean", Default: "false", Doc: "Host the demo for anyone. Needs the demo, and a data directory with no integration instances."},
	{Env: "CHADASH_TEST_AUTOMATION", Flag: "test-automation", Type: "boolean", Default: "false", Doc: "For automated tests only: a key in the data directory signs in as the owner. Never on a hub that holds anything real."},
	{Env: "CHADASH_TEST_CLOCK", Flag: "test-clock", Type: "boolean", Default: "false", Doc: "For automated tests only: the automation key can move the hub's clock forward. Needs test automation."},
	{Env: "CHADASH_NODE_PAIRING_FILE", Flag: "node-pairing-file", Type: "string", Doc: "Where the hub writes chadash-node's pairing token, in a volume shared with the node. Empty: no node can pair."},
	{Env: "CHADASH_NODE_PAIRING_FROM", Flag: "node-pairing-from", Type: "string", Doc: "Comma-separated CIDRs a node may pair from, or any. Default: private, loopback, link-local and CGNAT ranges."},
	{Env: "CHADASH_HISTORY", Flag: "history", Type: "string", Default: "standard", Enum: []string{"off", "standard", "long-term"}, Doc: "How much metrics history to keep. Settings → History wins, except over off."},
	{Env: "CHADASH_HISTORY_DIR", Flag: "history-dir", Type: "string", Doc: "The directory for history.db. Default: the data directory."},
	{Env: "CHADASH_METRICS_LISTEN", Flag: "metrics-listen", Type: "string", Doc: "The address for /metrics (Prometheus), on a listener of its own. Empty: off."},
	{Env: "CHADASH_METRICS_TOKEN", Type: "string", Secret: true, Doc: "The bearer token a /metrics scrape must send."},
	{Env: "CHADASH_EXPORT_PASSPHRASE", Type: "string", Secret: true, Doc: "chadash export and import: the backup's passphrase, when there's no terminal to ask on."},
	{Env: "TZ", Type: "string", Doc: "The hub's time zone (IANA): the morning digest, quiet hours, displays' night tint."},
}

// NodeOptions are chadash-node's settings, all from the environment.
var NodeOptions = []Option{
	{Env: "CHADASH_NODE_HUB", Type: "string", Default: "ws://chadash:3696/node", Doc: "The hub's /node endpoint (wss:// through a proxy)."},
	{Env: "CHADASH_NODE_NAME", Type: "string", Default: "local", Doc: "What the hub calls this node: 1–63 letters, digits, dots, dashes or underscores."},
	{Env: "CHADASH_NODE_DIR", Type: "string", Default: "/etc/chadash-node", Doc: "Where identity/ and policy.yaml live."},
	{Env: "CHADASH_NODE_POLICY", Type: "string", Doc: "The local policy file. Default <dir>/policy.yaml."},
	{Env: "CHADASH_NODE_PAIRING", Type: "string", Secret: true, Doc: "A pairing token, cdp_<secret>.<hub pin>."},
	{Env: "CHADASH_NODE_PAIRING_FILE", Type: "string", Default: "/pairing/token", Doc: "Where the hub writes the pairing token."},
	{Env: "CHADASH_NODE_TOKEN", Type: "string", Secret: true, Doc: "A join token, cdj_<secret>.<hub pin>, from Settings → Nodes → Add node."},
	{Env: "CHADASH_NODE_TOKEN_FILE", Type: "string", Doc: "A file holding the join token (a Docker secret, say)."},
	{Env: "CHADASH_NODE_DOCKER_HOST", Type: "string", Default: "unix:///var/run/docker.sock", Doc: "The Docker Engine: unix:///path or tcp://host:port."},
	{Env: "CHADASH_NODE_DOCKER_CONFIG", Type: "string", Doc: "Registry logins, a Docker config file; images.registries picks which are used. Default <dir>/docker-config.json."},
	{Env: "CHADASH_NODE_HEALTH_FILE", Type: "string", Default: "/tmp/chadash-node.health", Doc: "Where the node writes its health for the container's check."},
	{Env: "CHADASH_NODE_LOG_LEVEL", Type: "string", Default: "info", Enum: []string{"debug", "info", "warn", "error"}, Doc: "How much the node logs."},
	{Env: "CHADASH_NODE_LOG_FORMAT", Type: "string", Default: "text", Enum: []string{"text", "json"}, Doc: "The log's format."},
	{Env: "CHADASH_NODE_SELFUPDATE", Type: "boolean", Default: "false", Doc: "Grant stacks.selfupdate when there's no policy file."},
}

// Schema is the published JSON Schema of both: an object per program whose
// properties are its environment variables.
func Schema() []byte {
	section := func(title string, opts []Option) map[string]any {
		props := map[string]any{}
		for _, o := range opts {
			p := map[string]any{"type": o.Type, "description": o.Doc}
			if o.Default != "" {
				switch o.Type {
				case "boolean":
					p["default"] = o.Default == "true"
				case "integer":
					var n int
					_ = json.Unmarshal([]byte(o.Default), &n)
					p["default"] = n
				default:
					p["default"] = o.Default
				}
			}
			if len(o.Enum) > 0 {
				p["enum"] = o.Enum
			}
			if o.Flag != "" {
				p["x-chadash-flag"] = "--" + o.Flag
			}
			if o.Secret {
				p["x-chadash-secret"] = true
				p["writeOnly"] = true
			}
			if o.Deprecated != "" {
				p["deprecated"] = true
				p["x-chadash-deprecated-in"] = o.Deprecated
			}
			props[o.Env] = p
		}
		return map[string]any{"title": title, "type": "object", "additionalProperties": false, "properties": props}
	}
	doc := map[string]any{
		"$schema":                  "https://json-schema.org/draft/2020-12/schema",
		"$id":                      "https://dash.cha.sh/schema/config/v1.json",
		"title":                    "Cha Dash configuration",
		"description":              "Every setting the hub (chadash) and chadash-node take. Flags win over the environment, which wins over the default.",
		"x-chadash-config-version": SchemaVersion,
		"type":                     "object",
		"properties": map[string]any{
			"hub":  section("The hub (chadash)", HubOptions),
			"node": section("chadash-node", NodeOptions),
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// Envs lists opts' environment variables, sorted.
func Envs(opts []Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Env)
	}
	sort.Strings(out)
	return out
}
