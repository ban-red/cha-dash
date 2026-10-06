package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/node/docker"
)

// config is the node's settings, from CHADASH_NODE_* (nothing is required
// in the compose file this repo ships):
//
//	CHADASH_NODE_HUB           ws://chadash:3696/node     the hub's /node (wss:// through a proxy)
//	CHADASH_NODE_NAME          local                      what the hub calls this node
//	CHADASH_NODE_DIR           /etc/chadash-node          identity/ and policy.yaml live here
//	CHADASH_NODE_POLICY        <dir>/policy.yaml          the local policy
//	CHADASH_NODE_PAIRING       (none)                     a pairing token, cdp_<secret>.<hub pin>
//	CHADASH_NODE_PAIRING_FILE  /pairing/token             where the hub writes the pairing token
//	CHADASH_NODE_DOCKER_HOST   unix:///var/run/docker.sock
//	CHADASH_NODE_HEALTH_FILE   /tmp/chadash-node.health
//	CHADASH_NODE_LOG_LEVEL     info                       debug, info, warn, error
//	CHADASH_NODE_LOG_FORMAT    text                       text or json
type config struct {
	HubURL      string
	Name        string
	Dir         string
	PolicyPath  string
	Pairing     string
	PairingFile string
	DockerHost  string
	HealthFile  string
	LogLevel    slog.Level
	LogFormat   string
}

// Defaults.
const (
	defaultHub         = "ws://chadash:3696/node"
	defaultName        = "local"
	defaultDir         = "/etc/chadash-node"
	defaultPairingFile = "/pairing/token"
)

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

func loadConfig(getenv func(string) string) (config, error) {
	env := func(k, def string) string {
		if v := strings.TrimSpace(getenv("CHADASH_NODE_" + k)); v != "" {
			return v
		}
		return def
	}
	c := config{
		HubURL: env("HUB", defaultHub), Name: env("NAME", defaultName), Dir: filepath.Clean(env("DIR", defaultDir)),
		Pairing: env("PAIRING", ""), PairingFile: env("PAIRING_FILE", defaultPairingFile),
		DockerHost: env("DOCKER_HOST", docker.DefaultHost), HealthFile: env("HEALTH_FILE", node.DefaultHealthFile),
		LogFormat: strings.ToLower(env("LOG_FORMAT", "text")),
	}
	c.PolicyPath = env("POLICY", filepath.Join(c.Dir, "policy.yaml"))
	var errs []error
	if u, err := url.Parse(c.HubURL); err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		errs = append(errs, fmt.Errorf("CHADASH_NODE_HUB %q: want ws://host:port/node or wss://host/node", c.HubURL))
	}
	if !nameRE.MatchString(c.Name) {
		errs = append(errs, fmt.Errorf("CHADASH_NODE_NAME %q: 1–63 letters, digits, dots, dashes or underscores", c.Name))
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, errors.New("CHADASH_NODE_LOG_LEVEL: want debug, info, warn or error"))
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, errors.New("CHADASH_NODE_LOG_FORMAT: want text or json"))
	}
	if !strings.HasPrefix(c.DockerHost, "unix://") && !strings.HasPrefix(c.DockerHost, "tcp://") {
		errs = append(errs, fmt.Errorf("CHADASH_NODE_DOCKER_HOST %q: want unix:///path or tcp://host:port", c.DockerHost))
	}
	return c, errors.Join(errs...)
}

func (c config) identityDir() string { return filepath.Join(c.Dir, "identity") }

func (c config) logger(w io.Writer) *slog.Logger {
	o := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, o))
	}
	return slog.New(slog.NewTextHandler(w, o))
}
