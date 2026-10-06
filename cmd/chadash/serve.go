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
	"sync"
	"syscall"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/onboarding"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/server"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
	"dash.cha.sh/internal/webui"
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
	topics  *topics.Set
	demo    *demo.Driver // nil outside demo mode
	hub     *realtime.Hub
	// integrations runs the integration instances. In demo mode they write
	// to integrationTopics, a private set nobody subscribes to, so real
	// instances never reach the Demo Home; otherwise that's topics.
	integrations      *runtime.Manager
	integrationTopics *topics.Set
	// correlator merges what several instances report as one thing, over
	// integrationTopics: the Demo Home has nothing to merge.
	correlator *correlate.Correlator
	// actions is the action engine, under internal/authz's policy.
	actions *action.Engine

	stop context.CancelFunc // ends the background work below
	bg   sync.WaitGroup     // the demo driver or the inbox clock, and the correlator
}

// inboxClock is how often the inbox clock ticks outside demo mode, matching
// the demo's sample interval.
const inboxClock = 30 * time.Second

// sidecarEndpoint is the compose sidecar's socket proxy the onboarding
// wizard looks for; tests point it at a fake.
var sidecarEndpoint = onboarding.SidecarEndpoint

func newApp(ctx context.Context, cfg config.Config, log *slog.Logger) (_ *app, err error) {
	log.Info("starting "+buildinfo.String(), "data_dir", cfg.DataDir, "web_app", webui.Embedded)

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
	// The Homepage importer moves secrets into the vault; integrations
	// (v0.1) read them. Opening it at boot creates the key on first run and
	// catches a wrong key early.
	v, err := vault.Open(ctx, st, vault.KeyConfig{Path: keyPath, Generate: !explicit}, log)
	if err != nil {
		return nil, err
	}

	proxies := httpx.NewProxies(cfg.TrustedProxies)
	authSvc, err := auth.New(ctx, st, log, proxies, auth.WithPublicURL(cfg.PublicURL))
	if err != nil {
		return nil, err
	}

	// The security posture decides the severity security findings reach the
	// inbox at, for real instances and the Demo Home alike.
	sec, err := security.New(ctx, st, log)
	if err != nil {
		return nil, err
	}
	proxyCheck := httpx.NewProxyCheck(log, proxies, cfg.PublicURL)
	sec.ProxyNotes = proxyCheck.Notes

	g := graph.New()
	tp := topics.New(g, time.Now())
	var drv *demo.Driver
	if cfg.Demo {
		// The Demo Home owns the graph; its app:chadash (self: true) stands
		// for the hub.
		drv, err = demo.New(tp, demo.Options{Seed: cfg.DemoSeed, WallClock: cfg.DemoWallClock, At: cfg.DemoAt, Log: log, Posture: sec.Posture()})
		if err != nil {
			return nil, err
		}
		log.Info("demo mode: running the Demo Home simulator", "seed", cfg.DemoSeed, "sim_time", drv.Now().Format(time.RFC3339))
	} else if _, err := g.Update(func(tx *graph.Tx) error {
		// The hub knows about itself, so the graph is never empty and later
		// the "this would take Cha Dash down" checks have a node to start from.
		return tx.Upsert(graph.Resource{
			ID: "chadash:hub", Kind: "app", Name: "Cha Dash",
			Attrs: map[string]any{"version": buildinfo.Version},
			State: "running", Health: graph.HealthOK,
		})
	}); err != nil {
		return nil, err
	}

	// Instances poll at full rate only while a tab subscribes to something;
	// watch counts the realtime feeds.
	watch := runtime.NewWatch()
	itp := tp
	if cfg.Demo {
		itp = topics.New(graph.New(), time.Now())
	}
	mgr, err := runtime.New(runtime.Options{Store: st, Vault: v, Topics: itp, Sandboxed: cfg.Demo, Watch: watch, Log: log, Posture: sec.Posture()})
	if err != nil {
		return nil, err
	}
	sec.OnChange(mgr.SetPosture)
	if drv != nil {
		sec.OnChange(drv.SetPosture)
	}
	corr := correlate.New(correlate.Options{Topics: itp, Store: st, Log: log})

	ob, err := onboarding.New(onboarding.Options{Store: st, Integrations: mgr, Demo: cfg.Demo, Sidecar: sidecarEndpoint, Log: log})
	if err != nil {
		return nil, err
	}

	// The action engine and its audit log. In demo mode the Demo Home's
	// incident fixes are its actions.
	actions := action.NewRegistry()
	auditLog := audit.New(st)
	if drv != nil {
		if err := actions.Register(demo.ActionSource, drv.Actions()...); err != nil {
			return nil, err
		}
	}
	eng, err := action.New(ctx, action.Options{
		Store: st, Audit: auditLog, Registry: actions, Policy: authz.NewPolicy(st, log).ForEngine(), Graph: g,
		Sessions: action.Sessions{Proxies: proxies, StepUpUntil: func(id auth.Identity) time.Time { return id.Session.StepUpUntil }},
		// A T4 spends the grant that allowed it.
		ConsumeStepUp: authSvc.ClearStepUp,
		Log:           log,
	})
	if err != nil {
		return nil, err
	}

	// index.html carries the first board's snapshot for a signed-in owner,
	// when the web app is built in (the placeholder page has no use for it).
	var firstPaint *firstpaint.Builder
	if webui.Embedded {
		firstPaint = &firstpaint.Builder{Topics: tp, Boards: boards.New(st), Demo: drv != nil}
	}
	hub := realtime.New(watch.Source(tp.SourceWith(realtime.Mux{action.TasksTopic: eng.TasksSource()})), realtime.Options{
		OriginPatterns: cfg.AllowedOrigins,
		Server:         "chadash/" + buildinfo.Version,
		Log:            log,
	})
	h, err := server.New(server.Deps{
		Log:            log,
		Store:          st,
		Auth:           authSvc,
		Realtime:       hub,
		Web:            webui.New(),
		Proxies:        proxies,
		ProxyCheck:     proxyCheck,
		AllowedOrigins: cfg.AllowedOrigins,
		Demo:           drv,
		Vault:          v,
		Integrations:   mgr,
		Correlator:     corr,
		Onboarding:     ob,
		FirstPaint:     firstPaint,
		Security:       sec,
		Actions:        eng,
		Audit:          auditLog,
	})
	if err != nil {
		eng.Close()
		return nil, err
	}

	a := &app{handler: h, store: st, vault: v, graph: g, topics: tp, demo: drv, hub: hub, integrations: mgr, integrationTopics: itp, correlator: corr, actions: eng}
	bg, stop := context.WithCancel(ctx)
	a.stop = stop
	if err := mgr.Start(bg); err != nil {
		stop()
		eng.Close()
		return nil, err
	}
	if cfg.Demo {
		log.Info("demo mode: integration instances run, but write to a private graph, not the Demo Home")
	}
	if drv != nil {
		a.bg.Go(func() { drv.Run(bg) })
	} else {
		a.bg.Go(func() { tp.RunClock(bg, inboxClock, time.Now) })
	}
	a.bg.Go(func() { corr.Run(bg) })
	a.bg.Go(func() { eng.Run(bg) })
	return a, nil
}

func (a *app) close() error {
	a.stop()
	a.integrations.Close()
	a.bg.Wait()
	a.actions.Close() // running tasks record how they ended before the store closes
	a.hub.Close()
	return a.store.Close()
}
