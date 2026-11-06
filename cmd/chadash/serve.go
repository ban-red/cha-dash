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
	"strings"
	"sync"
	"syscall"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/backups"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/causes"
	"dash.cha.sh/internal/clock"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/icons"
	"dash.cha.sh/internal/incidents"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/mcp"
	"dash.cha.sh/internal/nodes"
	"dash.cha.sh/internal/notify"
	"dash.cha.sh/internal/onboarding"
	"dash.cha.sh/internal/pvehook"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/server"
	"dash.cha.sh/internal/specstore"
	"dash.cha.sh/internal/stacks"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/updates"
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

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", ln.Addr().String())
	if cfg.MetricsListen != "" {
		// /metrics on a listener of its own, and nothing else there.
		mln, err := lc.Listen(ctx, "tcp", cfg.MetricsListen)
		if err != nil {
			ln.Close()
			return fmt.Errorf("metrics listener: %w", err)
		}
		log.Info("serving /metrics", "addr", mln.Addr().String(), "token", cfg.MetricsToken != "")
		// It stops before the app closes (deferred above, so run after
		// this), so no scrape reads a closed history.db.
		mctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := server.Serve(mctx, mln, server.MetricsHandler(a.topics, a.history, cfg.MetricsToken), log.With("component", "metrics")); err != nil {
				log.Error("metrics listener stopped", "err", err)
			}
		}()
		defer func() {
			cancel()
			<-done
		}()
	}
	return server.Serve(ctx, ln, a.handler, log, a.closeSockets)
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
	// displayHub serves displays' sockets (GET /api/display/ws).
	displayHub *realtime.Hub
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
	// nodes serves chadash-node at /node: the registry, pairing, the caller.
	nodes *nodes.Hub
	// history records the instances' metrics in history.db; nil when
	// CHADASH_HISTORY is off, and in demo mode.
	history      *history.Recorder
	historyStore *store.Store

	stop context.CancelFunc // ends the background work below
	bg   sync.WaitGroup     // the demo driver or the inbox clock, and the correlator
}

// inboxClock is how often the inbox clock ticks outside demo mode, matching
// the demo's sample interval.
const inboxClock = 30 * time.Second

// sidecarEndpoint is the compose sidecar's socket proxy the onboarding
// wizard looks for; tests point it at a fake.
var sidecarEndpoint = onboarding.SidecarEndpoint

// localNodeEndpoint is the Docker endpoint of the node deploy/compose.yaml
// pairs on the hub's host (CHADASH_NODE_NAME=local).
const localNodeEndpoint = "node://local"

func newApp(ctx context.Context, cfg config.Config, log *slog.Logger) (_ *app, err error) {
	log.Info("starting "+buildinfo.String(), "data_dir", cfg.DataDir, "web_app", webui.Embedded)
	// The hub's clock: real time, or on a test hub one the automation key can
	// move forward (CHADASH_TEST_CLOCK, internal/clock). Every schedule reads it.
	clk := clock.Real()
	if cfg.TestClock {
		clk = clock.NewTest()
	}

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

	// Metrics history (ADR-0031), in its own database. Not in demo mode:
	// the Demo Home makes its own history, and instances there write to a
	// private graph. CHADASH_HISTORY=off (or a hand-built Config, History "")
	// leaves it out altogether; otherwise the level an admin chose in
	// Settings → History wins over CHADASH_HISTORY, off included.
	var rec *history.Recorder
	var hst *store.Store
	var hist runtime.History // a nil interface, not a nil *Recorder, when off
	if level, perr := history.ParseLevel(cfg.History); perr == nil && level != history.Off && !cfg.Demo {
		if chosen, ok, err := history.LoadLevel(ctx, st); err != nil {
			return nil, err
		} else if ok {
			level = chosen
		}
		if cfg.HistoryDir != "" {
			if err := os.MkdirAll(cfg.HistoryDir, 0o700); err != nil {
				return nil, fmt.Errorf("history dir: %w", err)
			}
		}
		if hst, err = store.OpenHistory(ctx, cfg.HistoryPath()); err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				hst.Close()
			}
		}()
		if _, err = hst.Migrate(ctx); err != nil {
			return nil, err
		}
		if rec, err = history.Open(ctx, history.Options{Store: hst, Level: level, Log: log.With("component", "history"), Now: clk.Now}); err != nil {
			return nil, err
		}
		limit, ok, err := history.LoadLimit(ctx, st)
		if err != nil {
			return nil, err
		}
		if !ok {
			limit = history.DefaultMaxBytes
		}
		rec.SetLimit(limit)
		excluded, err := history.LoadExcluded(ctx, st)
		if err != nil {
			return nil, err
		}
		rec.SetExcluded(excluded)
		patterns, err := history.LoadExcludedKeys(ctx, st)
		if err != nil {
			return nil, err
		}
		rec.SetExcludedKeys(patterns)
		keep, err := history.LoadKeep(ctx, st)
		if err != nil {
			return nil, err
		}
		rec.SetKeep(keep)
		hist = rec
		log.Info("recording metrics history", "level", level, "path", cfg.HistoryPath(), "limit_bytes", limit)
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
	if cfg.DemoPublic {
		// The public demo (config refuses it without demo mode): visitors
		// become one operator, and authz.Guard refuses everything but the
		// Demo Home's actions. A data directory that already holds
		// integrations isn't a demo's: they'd run, sandboxed, on a public
		// host, so the hub refuses to start.
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM integration_instances`).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fmt.Errorf("public demo: the data directory holds %d integration instances; start the public demo with an empty data directory", n)
		}
		if err := authSvc.EnablePublicDemo(ctx); err != nil {
			return nil, err
		}
	}
	if cfg.TestAutomation {
		// A lowered step-up for automated tests (config refuses it with the
		// public demo); the Inbox keeps a finding open while it's on, below.
		if err := authSvc.EnableAutomation(cfg.DataDir); err != nil {
			return nil, err
		}
		if cfg.TestClock {
			authSvc.EnableTestClock(clk)
		}
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
	tp := topics.New(g, clk.Now())
	var drv *demo.Driver
	if cfg.Demo {
		// The Demo Home owns the graph; its app:chadash (self: true) stands
		// for the hub.
		opts := demo.Options{Seed: cfg.DemoSeed, WallClock: cfg.DemoWallClock, Local: cfg.DemoLocal, At: cfg.DemoAt, Log: log, Posture: sec.Posture(), Timers: clk}
		if cfg.DemoPublic {
			// Every visitor finds the incidents open, whatever the last one fixed.
			opts.RestartEvery = demo.PublicRestart
		}
		drv, err = demo.New(tp, opts)
		if err != nil {
			return nil, err
		}
		log.Info("demo mode: running the Demo Home simulator", "seed", cfg.DemoSeed, "sim_time", drv.Now().Format(time.RFC3339))
	} else if _, err := g.Update(func(tx *graph.Tx) error {
		// The hub knows about itself, so the graph is never empty and the
		// "this would take Cha Dash down" checks have a node to start from.
		// Integrations that can tell where it runs relate it there (Docker,
		// Unraid, Proxmox: chadash:hub runs_on …); self marks it, as the
		// Demo Home's app:chadash is marked.
		return tx.Upsert(graph.Resource{
			ID: integration.HubResource, Kind: "app", Name: "Cha Dash",
			Attrs: map[string]any{"version": buildinfo.Version, "self": true},
			State: "running", Health: graph.HealthOK,
		})
	}); err != nil {
		return nil, err
	}

	// Instances poll at full rate only while a tab subscribes to something;
	// watch counts the realtime feeds.
	watch := runtime.NewWatch()
	if rec != nil {
		// While history is kept, instances read what a board shows once a
		// minute even when nobody's looking (Interest.Record).
		rec.OnLevel(func(l history.Level) { watch.SetRecord(l != history.Off) })
	}
	itp := tp
	if cfg.Demo {
		itp = topics.New(graph.New(), clk.Now())
	}
	// chadash-node: the registry and the /node channel. Its status goes
	// where the integrations publish (a private graph in demo mode).
	auditLog := audit.New(st)
	// Approving a node adds its Docker integration once the manager exists
	// (set below; approvals only come in over HTTP, after newApp).
	var onApproved func(context.Context, nodes.Record, bool, audit.Actor)
	nodeHub, err := nodes.New(ctx, nodes.Options{Store: st, Vault: v, Graph: itp.Graph, Audit: auditLog, Now: clk.Now,
		PairingFile: cfg.NodePairingFile, PairingFrom: cfg.NodePairingFrom, Proxies: proxies, Log: log.With("component", "nodes"),
		Inbox: itp.Inbox, PublicURL: cfg.PublicURL,
		OnApproved: func(ctx context.Context, rec nodes.Record, addDocker bool, by audit.Actor) {
			if onApproved != nil {
				onApproved(ctx, rec, addDocker, by)
			}
		}})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			nodeHub.Close()
		}
	}()
	// The action engine's registry. Instances register their actions in it
	// (one source per integration), except in demo mode: there they write to
	// a private graph the engine never sees, and the Demo Home's fixes are
	// the actions.
	actions := action.NewRegistry()
	// Specs the owner installed in Settings, registered beside the bundled
	// ones before any instance starts. Not in demo mode, which runs the
	// Demo Home's integrations only.
	var specs *specstore.Service
	if !cfg.Demo {
		specs = specstore.New(specstore.Options{Store: st, Log: log.With("component", "specs"), Now: clk.Now})
		if err := specs.Load(ctx); err != nil {
			return nil, err
		}
	}
	instanceActions := actions
	if cfg.Demo {
		instanceActions = nil
	}
	mgr, err := runtime.New(runtime.Options{Store: st, Vault: v, Topics: itp, Sandboxed: cfg.Demo, Watch: watch, Log: log, Posture: sec.Posture(), Now: clk.Now,
		Actions: instanceActions,
		// Integrations reach only the node verbs they use, by name (the Docker
		// integration's node:// endpoint and its actions).
		Nodes:   nodes.Allow(nodeHub, nodes.IntegrationVerbs...),
		History: hist})
	if err != nil {
		return nil, err
	}
	sec.OnChange(mgr.SetPosture)
	if specs != nil {
		specs.SetRuntime(mgr.Instances, mgr.Rebind)
	}
	onApproved = dockerOnApprove(mgr, auditLog, log.With("component", "nodes"))
	// A node that connects is read at once by the instances that reach it,
	// not at their next poll: a hub that started before its node shows the
	// node's Docker within seconds, watched or not.
	nodeHub.OnConnect(func(name string) {
		for _, in := range mgr.List() {
			if ep, _ := in.Config["endpoint"].(string); in.Enabled && ep == "node://"+name {
				_ = mgr.Refresh(in.ID)
			}
		}
	})
	if drv != nil {
		sec.OnChange(drv.SetPosture)
	}
	corr := correlate.New(correlate.Options{Topics: itp, Store: st, Log: log, Now: clk.Now})
	// Cause hints run over the topics the page reads: in demo mode the
	// Demo Home's, on its clock, as over a real home.
	causeOpts := causes.Options{Topics: tp, Log: log.With("component", "causes"), Now: clk.Now}
	if rec != nil {
		causeOpts.History = rec
	}
	if drv != nil {
		causeOpts.Now, causeOpts.Clock = drv.Now, drv.Clock
	}
	hints := causes.New(causeOpts)
	// Backup coverage, like the cause hints, over the topics the page
	// reads; the objectives are the hub's own.
	backupOpts := backups.Options{Topics: tp, Store: st, Log: log.With("component", "backups"), Now: clk.Now}
	if drv != nil {
		backupOpts.Now = drv.Now
	}
	cover, err := backups.New(ctx, backupOpts)
	if err != nil {
		return nil, err
	}

	// With local node pairing on (the compose file's default), the hub's own
	// Docker is read through the node, not a socket proxy.
	sidecar := sidecarEndpoint
	if cfg.NodePairingFile != "" {
		sidecar = localNodeEndpoint
	}
	ob, err := onboarding.New(onboarding.Options{Store: st, Integrations: mgr, Demo: cfg.Demo, Sidecar: sidecar, Log: log, Now: clk.Now})
	if err != nil {
		return nil, err
	}

	// Owned stacks on the nodes. Not in demo mode: the Demo Home has no
	// nodes, and its scripted fixes (stack.rollback among them) are the
	// actions there.
	var stk *stacks.Service
	if !cfg.Demo {
		stk, err = stacks.New(stacks.Options{Store: st, Vault: v, Caller: nodeHub, Graph: g, Inbox: tp.Inbox, Log: log.With("component", "stacks"), Proxies: proxies, Now: clk.Now,
			Nodes: func(ctx context.Context) ([]stacks.Node, error) {
				list, err := nodeHub.List(ctx)
				if err != nil {
					return nil, err
				}
				out := make([]stacks.Node, 0, len(list))
				for _, n := range list {
					if n.State == nodes.StateApproved { // not revoked, nor waiting for approval
						out = append(out, stacks.Node{ID: n.ID, Name: n.Name, Online: n.Connected})
					}
				}
				return out, nil
			}})
		if err != nil {
			return nil, err
		}
		if err := actions.Register(stacks.ActionSource, stk.Actions()...); err != nil {
			return nil, err
		}
		// Fleet stacks (wave 14): stack.move and chadash.update.
		if err := actions.Register(stacks.FleetSource, stk.FleetActions()...); err != nil {
			return nil, err
		}
	}

	// The Update Center (wave 22): the updates the Docker instances through
	// a node publish, holds and snoozes, and one quiet Inbox item. Not in
	// demo mode, which has no nodes.
	var upd *updates.Service
	if !cfg.Demo {
		upd, err = updates.New(updates.Options{Store: st, Graph: g, Inbox: tp.Inbox, Log: log.With("component", "updates"), Clock: clk,
			// Rollouts (wave 26): steps take their tiers from the registry,
			// an owned stack's commit or a PVE snapshot comes first, and a
			// scheduled one runs as the person who scheduled it.
			Registry:   actions,
			CanAct:     func(id, target string) bool { return mgr.Actions().Availability(id, target).PlanReady },
			Users:      authSvc.GetUser,
			OwnedStack: ownedOf(stk),
			// Check now: each node behind a Docker instance asks its
			// registries again (image.check with fresh, which the node
			// limits to once a minute per tag), then those instances sync
			// and publish the answers. In the background: registries can
			// be slow, and the page polls.
			Refresh: func() int {
				nodesOf := map[string][]string{} // node name → instance IDs
				for _, in := range mgr.List() {
					if ep, _ := in.Config["endpoint"].(string); in.Enabled && in.Integration == "docker" && strings.HasPrefix(ep, "node://") {
						name := strings.TrimPrefix(ep, "node://")
						nodesOf[name] = append(nodesOf[name], in.ID)
					}
				}
				n := 0
				for name, ids := range nodesOf {
					n += len(ids)
					go func() {
						cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
						defer cancel()
						if _, err := nodeHub.Call(cctx, name, "image.check", map[string]bool{"fresh": true}, nil); err != nil {
							log.Warn("updates: check now", "node", name, "err", err)
						}
						for _, id := range ids {
							_ = mgr.Refresh(id)
						}
					}()
				}
				return n
			}})
		if err != nil {
			return nil, err
		}
		if stk != nil {
			stk.Held = upd.HeldServices
		}
		if err := actions.Register(updates.ActionSource, upd.Actions()...); err != nil {
			return nil, err
		}
	}

	// The action engine and its audit log. In demo mode the Demo Home's
	// incident fixes are its actions.
	if drv != nil {
		if err := actions.Register(demo.ActionSource, drv.Actions()...); err != nil {
			return nil, err
		}
	}
	// The inbox per role: whole for viewers and up, the household's
	// projection of what its boards show otherwise (server.InboxView).
	bs := boards.New(st)
	// The incident timeline (wave 28): every incident's lifecycle, read off
	// the inbox the page shows, kept 90 days unless Settings → History says
	// otherwise. An incident is expected while a maintenance window is open
	// on its host (the Update Center's windows); the timeline records it and
	// notify holds it back until the window ends.
	inc := incidents.NewStore(st)
	if err := inc.Load(ctx); err != nil {
		return nil, err
	}
	expect := &incidents.Expectations{}
	if upd != nil {
		expect.Set(incidents.ExpecterFunc(upd.Expected))
	}
	// Acknowledgements ride on the inbox's items; the recorder puts them
	// back after a restart.
	if cfg.TestAutomation {
		server.AutomationFinding(tp.Inbox, clk.Now())
	}
	acks := incidents.NewAcks(incidents.AcksOptions{Store: inc, Inbox: tp.Inbox, Log: log.With("component", "incidents"), Now: clk.Now})
	incRec := incidents.NewRecorder(incidents.RecorderOptions{Store: inc, Inbox: tp.Inbox, Expect: expect, Acks: acks, Log: log.With("component", "incidents"), Clock: clk})
	// Notifications: the inbox's transitions (each device hears what its
	// user's role sees) and every T4 action, from the engine below.
	nt, err := notify.New(ctx, notify.Options{Store: st, Vault: v, Inbox: tp.Inbox, InboxView: server.InboxView(bs, g, log),
		PublicURL: cfg.PublicURL, Version: buildinfo.Version, Log: log.With("component", "notify"), Expected: expect, Clock: clk})
	if err != nil {
		return nil, err
	}
	// The morning digest reads the timeline, the Update Center and (once
	// the backups track wires it) stale backups; each may say nothing.
	nt.SetDigestSources(digestSources(inc, upd, cover))
	eng, err := action.New(ctx, action.Options{
		Store: st, Audit: auditLog, Registry: actions, Graph: g,
		// API tokens act as their user, narrowed to their scopes; their T2
		// plans wait in the Inbox for a person (tp.Inbox).
		// Agent tokens propose only, while Agents is on; host= and tag= on
		// their scopes read the target's place in the graph.
		Policy: authz.NewPolicy(st, log, authz.WithTokens(authSvc.TokenAccess), authz.WithUsers(authSvc.GetUser),
			authz.WithAgentsSwitch(sec.AgentsOn), authz.WithTargetFacts(mcp.Facts(g))).ForEngine(),
		// An agent token's proposals: at most 3 waiting and 20 an hour by default.
		AgentLimits: sec.AgentLimits,
		Inbox:       tp.Inbox,
		Sessions:    action.Sessions{Proxies: proxies, StepUpUntil: func(id auth.Identity) time.Time { return id.Session.StepUpUntil }},
		// A T4 spends the grant that allowed it, and tells the admins.
		ConsumeStepUp: authSvc.SpendStepUp,
		// A container update that rolled back on its own: the Update
		// Center remembers the digest, so Update all doesn't retry it.
		OnTaskDone: func(d action.TaskDone) {
			nt.TaskDone(d)
			if upd != nil {
				upd.TaskDone(d)
			}
		},
		// A container held on its image isn't updated, however it's asked.
		Guard: guardOf(upd),
		// A git sync's denied or expired approval closes its Inbox item.
		OnApprovalEnded: func(a action.Approval) {
			if stk != nil {
				stk.ApprovalEnded(a)
			}
		},
		Log:   log,
		Clock: clk,
	})
	if err != nil {
		return nil, err
	}
	if upd != nil {
		// Scheduled rollouts run through the engine, as their person.
		upd.Engine = eng
	}
	// Git sync (wave 12): the syncer polls and runs the syncs as their
	// owners' sync actors through the engine; a sync whose owner is demoted
	// or disabled pauses at once.
	var syncer *stacks.Syncer
	if stk != nil {
		if syncer, err = stacks.NewSyncer(stk, stacks.SyncerOptions{Engine: eng, Users: authSvc.GetUser}); err != nil {
			eng.Close()
			return nil, err
		}
		authSvc.OnEvent(func(ev auth.Event) {
			if ev.Kind == auth.EventUserChanged {
				// On the syncer's WaitGroup, so shutdown waits for it. Before
				// the syncer runs, or after, each sync's own run checks its
				// owner anyway.
				syncer.OwnerChangedAsync(ev.UserID)
			}
		})
	}

	// index.html carries the first board's snapshot for a signed-in user,
	// when the web app is built in (the placeholder page has no use for it).
	var firstPaint *firstpaint.Builder
	if webui.Embedded {
		firstPaint = &firstpaint.Builder{Topics: tp, Boards: bs, Demo: drv != nil, Automation: cfg.TestAutomation, Inbox: server.InboxView(bs, g, log), WholeGraph: authz.SeesWholeGraph}
		if drv != nil {
			firstPaint.DemoZone, firstPaint.DemoZoneLabel = drv.TimeZone()
		}
	}
	hub := realtime.New(watch.Source(tp.SourceWith(realtime.Mux{
		action.TasksTopic: eng.TasksSource(),
		topics.Inbox:      tp.InboxSource(server.InboxViewer(bs, g, log)),
		// Household gets the graph without chadash-node's resources and
		// without addresses (topics.HouseholdGraph).
		topics.Graph: tp.GraphSource(server.GraphViewer()),
		// Household gets the causes of what its graph shows.
		topics.Causes: tp.CausesSource(server.GraphViewer()),
		// Household gets the coverage of what its boards show.
		topics.Backups: tp.BackupsSource(server.BackupsViewer(bs, g, log)),
	})), realtime.Options{
		OriginPatterns: cfg.AllowedOrigins,
		Server:         "chadash/" + buildinfo.Version,
		Log:            log,
		// A socket's identity is rechecked every minute (and sign-outs and
		// account changes close it at once: server.New wires that).
		Recheck: server.SocketRecheck(authSvc),
	})
	// Displays' sockets: the topics a board shows, narrowed to each
	// display's board, rechecked every minute and closed at once when the
	// display is revoked.
	displayHub := realtime.New(watch.Source(server.DisplaySource(tp, bs, g, log)), realtime.Options{
		OriginPatterns: cfg.AllowedOrigins,
		Server:         "chadash/" + buildinfo.Version,
		Log:            log,
		Recheck:        server.DisplaySocketRecheck(authSvc),
	})
	// Tiles' icons, fetched and served by the hub (the CSP keeps tiles from
	// loading them). Never fetched in demo mode, which reads nothing real.
	icn := icons.New(icons.Options{Store: st, Log: log.With("component", "icons"), Fetch: !cfg.Demo, Now: clk.Now})
	h, err := server.New(server.Deps{
		Log:            log,
		Store:          st,
		Auth:           authSvc,
		Realtime:       hub,
		DisplaySocket:  displayHub,
		Web:            webui.New(),
		Proxies:        proxies,
		ProxyCheck:     proxyCheck,
		AllowedOrigins: cfg.AllowedOrigins,
		PublicURL:      cfg.PublicURL,
		Demo:           drv,
		Vault:          v,
		Integrations:   mgr,
		Correlator:     corr,
		Onboarding:     ob,
		FirstPaint:     firstPaint,
		Security:       sec,
		Actions:        eng,
		Audit:          auditLog,
		Nodes:          nodeHub,
		Stacks:         stk,
		Updates:        upd,
		Backups:        cover,
		Specs:          specs,
		Notify:         nt,
		Topics:         tp,
		Icons:          icn,
		History:        rec,
		Incidents:      inc,
		Acks:           acks,
		PVEHooks:       pveHooks(cfg, st, v, mgr, proxies, log),
	})
	if err != nil {
		eng.Close()
		return nil, err
	}

	a := &app{handler: h, store: st, vault: v, graph: g, topics: tp, demo: drv, hub: hub, displayHub: displayHub, integrations: mgr, integrationTopics: itp, correlator: corr, actions: eng, nodes: nodeHub,
		history: rec, historyStore: hst}
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
		a.bg.Go(func() { tp.RunClock(bg, inboxClock, clk.Now) })
	}
	a.bg.Go(func() { corr.Run(bg) })
	a.bg.Go(func() { hints.Run(bg) })
	a.bg.Go(func() { cover.Run(bg) })
	a.bg.Go(func() { eng.Run(bg) })
	a.bg.Go(func() { nt.Run(bg) })
	a.bg.Go(func() { incRec.Run(bg) })
	a.bg.Go(func() { icn.Run(bg) })
	if upd != nil {
		a.bg.Go(func() { upd.Run(bg) })
	}
	if syncer != nil {
		a.bg.Go(func() { syncer.Run(bg) })
	}
	if rec != nil {
		a.bg.Go(func() { rec.Run(bg) })
	}
	return a, nil
}

// closeSockets closes every realtime socket, people's and displays'.
func (a *app) closeSockets() {
	a.hub.Close()
	if a.displayHub != nil {
		a.displayHub.Close()
	}
}

func (a *app) close() error {
	a.stop()
	a.integrations.Close()
	a.bg.Wait()
	a.actions.Close() // running tasks record how they ended before the store closes
	a.nodes.Close()
	a.closeSockets()
	var errs []error
	if a.history != nil {
		// The instances have stopped, so this is the last of their samples.
		errs = append(errs, a.history.Close(context.Background()), a.historyStore.Close())
	}
	return errors.Join(append(errs, a.store.Close())...)
}

// ownedOf says which compose projects are owned stacks, or none without
// the stacks service.
func ownedOf(stk *stacks.Service) func(context.Context, string, string) bool {
	if stk == nil {
		return nil
	}
	return stk.Owned
}

// pveHooks receives Proxmox VE's notifications; not in demo mode, which
// reads nothing real.
func pveHooks(cfg config.Config, st *store.Store, v *vault.Vault, mgr *runtime.Manager, proxies httpx.Proxies, log *slog.Logger) *pvehook.Service {
	if cfg.Demo {
		return nil
	}
	return pvehook.New(pvehook.Options{Store: st, Vault: v, Instances: mgr, Proxies: proxies, Log: log.With("component", "pvehook")})
}

// digestSources are the morning digest's: incidents that opened from the
// timeline, updates waiting from the Update Center (none in demo mode),
// and stale backups from the coverage map.
func digestSources(inc *incidents.Store, upd *updates.Service, cover *backups.Service) notify.DigestSources {
	src := notify.DigestSources{
		Overnight: func(ctx context.Context, since, until time.Time) ([]notify.DigestIncident, error) {
			evs, err := inc.Events(ctx, incidents.Query{From: since, To: until, Kinds: []incidents.EventKind{incidents.Opened, incidents.Resolved}})
			if err != nil {
				return nil, err
			}
			var out []notify.DigestIncident
			index := map[string]int{}
			for _, e := range evs {
				d := incidents.DataOf(e)
				if e.Kind == incidents.Resolved {
					if i, ok := index[e.IncidentID]; ok {
						out[i].Resolved = true
					}
					continue
				}
				index[e.IncidentID] = len(out)
				out = append(out, notify.DigestIncident{ID: e.IncidentID, Title: d.Title, Severity: d.Severity})
			}
			return out, nil
		},
	}
	if cover != nil {
		src.StaleBackups = func(context.Context) ([]string, error) { return cover.Stale(), nil }
	}
	if upd != nil {
		src.Updates = func(ctx context.Context) (int, error) {
			rows, err := upd.List(ctx)
			n := 0
			for _, r := range rows {
				if r.State == integration.UpdateAvailable {
					n++
				}
			}
			return n, err
		}
	}
	return src
}

// guardOf is the action engine's Guard: the Update Center's holds, or none
// in demo mode.
func guardOf(upd *updates.Service) func(context.Context, action.Request) error {
	if upd == nil {
		return nil
	}
	return upd.Guard
}
