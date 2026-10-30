package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freeAddr is a loopback address with a port nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// TestMetricsListener: with CHADASH_METRICS_LISTEN set, /metrics answers on
// its own address, the API doesn't, and the hub still stops cleanly.
func TestMetricsListener(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	cfg.Listen, cfg.MetricsListen, cfg.MetricsToken = freeAddr(t), freeAddr(t), "fake-scrape-token"
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan error, 1)
	go func() { stopped <- runServer(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	get := func(addr, path string) (int, string) {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://"+addr+path, nil)
		req.Header.Set("Authorization", "Bearer fake-scrape-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	deadline := time.Now().Add(10 * time.Second)
	status, body := get(cfg.MetricsListen, "/metrics")
	for status == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		status, body = get(cfg.MetricsListen, "/metrics")
	}
	if status != http.StatusOK || !strings.Contains(body, "chadash_build_info") || !strings.Contains(body, `chadash_resource_health{resource="chadash:hub"`) {
		t.Fatalf("/metrics: %d\n%s", status, body)
	}
	if status, _ := get(cfg.MetricsListen, "/healthz"); status != http.StatusNotFound {
		t.Errorf("/healthz on the metrics listener: %d", status)
	}
	// The hub's own listener answers unknown paths with the web app, never the metrics.
	if _, body := get(cfg.Listen, "/metrics"); strings.Contains(body, "chadash_build_info") {
		t.Error("/metrics answers on the hub's own listener")
	}
	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("runServer: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the hub didn't stop")
	}
}
