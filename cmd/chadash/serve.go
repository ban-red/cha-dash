package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/server"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

func serve(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	fs := flag.NewFlagSet("chadash serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	loader := config.Bind(fs, getenv)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, err := loader.Config()
	if err != nil {
		fmt.Fprintf(stderr, "chadash: %v\n", err)
		return 2
	}
	log := cfg.Logger(stderr)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runServer(ctx, cfg, log); err != nil {
		log.Error("hub stopped", "err", err)
		return 1
	}
	log.Info("hub stopped")
	return 0
}

func runServer(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	a, err := newApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer a.close()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", ln.Addr().String())
	return server.Serve(ctx, ln, a.handler, log, a.hub.Close)
}

// app is the wired hub, minus the listener, so tests can mount it on httptest.
type app struct {
	handler http.Handler
	store   *store.Store
	vault   *vault.Vault
	graph   *graph.Graph
	hub     *realtime.Hub
}

func newApp(ctx context.Context, cfg config.Config, log *slog.Logger) (_ *app, err error) {
	log.Info("starting "+buildinfo.String(), "data_dir", cfg.DataDir)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			st.Close()
		}
	}()
	applied, err := st.Migrate(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range applied {
		log.Info("applied migration", "version", m.Version, "name", m.Name, "took", m.Duration)
	}

	keyPath, explicit := cfg.KeyFile()
	// Nothing reads the vault yet; integrations (v0.1) will. Opening it now
	// creates the key on first boot and catches a wrong key early.
	v, err := vault.Open(ctx, st, vault.KeyConfig{Path: keyPath, Generate: !explicit}, log)
	if err != nil {
		return nil, err
	}

	proxies := httpx.NewProxies(cfg.TrustedProxies)
	authSvc, err := auth.New(ctx, st, log, proxies)
	if err != nil {
		return nil, err
	}

	g := graph.New()
	// The hub knows about itself, so the graph is never empty and later the
	// "this would take Cha Dash down" checks have a node to start from.
	if _, err := g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{
			ID: "chadash:hub", Kind: "app", Name: "Cha Dash",
			Attrs: map[string]any{"version": buildinfo.Version},
			State: "running", Health: graph.HealthOK,
		})
	}); err != nil {
		return nil, err
	}

	hub := realtime.New(realtime.GraphSource{Graph: g}, realtime.Options{
		OriginPatterns: cfg.AllowedOrigins,
		Server:         "chadash/" + buildinfo.Version,
		Log:            log,
	})
	h, err := server.New(server.Deps{
		Log:            log,
		Store:          st,
		Auth:           authSvc,
		Realtime:       hub,
		Web:            nil, // step 3.8: the embedded SPA
		Proxies:        proxies,
		AllowedOrigins: cfg.AllowedOrigins,
	})
	if err != nil {
		return nil, err
	}
	return &app{handler: h, store: st, vault: v, graph: g, hub: hub}, nil
}

func (a *app) close() error {
	a.hub.Close()
	return a.store.Close()
}
