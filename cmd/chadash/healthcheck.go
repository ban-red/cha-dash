package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"dash.cha.sh/internal/config"
)

// healthcheck GETs /healthz on the configured listen address. Scratch images
// have no curl, so Docker's HEALTHCHECK runs this. Exit codes follow Docker:
// 0 healthy, 1 unhealthy (2 is reserved, so bad flags also report 1).
func healthcheck(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	fs := flag.NewFlagSet("chadash healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	loader := config.Bind(fs, getenv)
	timeout := fs.Duration("timeout", 3*time.Second, "how long to wait for the hub")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg, err := loader.Config()
	if err != nil {
		fmt.Fprintf(stderr, "chadash healthcheck: %v\n", err)
		return 1
	}
	url := healthURL(cfg.Listen)

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(stderr, "chadash healthcheck: %v\n", err)
		return 1
	}
	// Never route a loopback check through HTTP(S)_PROXY from the environment.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "chadash healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "chadash healthcheck: %s returned %s\n", url, resp.Status)
		return 1
	}
	return 0
}

// healthURL turns a listen address into a URL to dial: wildcard hosts become
// loopback, since the check runs inside the hub's own container.
func healthURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "", "3696"
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}
